package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"cli-agentx/internal"
	"github.com/spf13/cobra"
)

type rootOptions struct {
	format string
}

func main() {
	root := newRootCommand()
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func newRootCommand() *cobra.Command {
	opts := &rootOptions{}
	root := &cobra.Command{
		Use:   "agent",
		Short: "Local-first AI agent with isolated execution",
	}
	root.PersistentFlags().StringVar(&opts.format, "format", "raw", "Output format: raw or jsonl")
	root.AddCommand(newSendCommand(opts), newWorktreeCommand(), newCheckpointCommand())
	return root
}

func newSendCommand(root *rootOptions) *cobra.Command {
	var prompt, topicID, worktreeName, worktreeBase, resumeRunID, checkpointID string
	var approveAll bool
	cmd := &cobra.Command{
		Use:   "send",
		Short: "Run an agent message, optionally in an isolated worktree",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(prompt) == "" && checkpointID == "" {
				return fmt.Errorf("--prompt is required unless resuming a checkpoint")
			}
			cfg, err := internal.LoadConfig()
			if err != nil {
				return err
			}

			originalWD, err := os.Getwd()
			if err != nil {
				return err
			}
			defer os.Chdir(originalWD)

			if checkpointID != "" && worktreeName != "" {
				return fmt.Errorf("cannot combine --checkpoint with --worktree; a checkpoint must resume in its recorded worktree")
			}
			if worktreeName != "" {
				runtimeConfig := cfg.Runtime.Normalized()
				if worktreeBase != "" {
					runtimeConfig.Worktree.BaseBranch = worktreeBase
				}
				worktree, err := internal.NewWorktreeManager(runtimeConfig).Create(cmd.Context(), originalWD, worktreeName)
				if err != nil {
					return err
				}
				if err := os.Chdir(worktree.Path); err != nil {
					return fmt.Errorf("enter worktree: %w", err)
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "worktree: %s (%s)\n", worktree.Path, worktree.Branch)
			}

			db, err := internal.OpenDB()
			if err != nil {
				return err
			}
			defer db.Close()

			var contextResult *internal.ContextResult
			if checkpointID != "" {
				if resumeRunID == "" {
					return fmt.Errorf("--resume-run is required with --checkpoint")
				}
				checkpoint, err := internal.DefaultCheckpointStore().Load(resumeRunID, checkpointID)
				if err != nil {
					return err
				}
				if topicID != "" && topicID != checkpoint.TopicID {
					return fmt.Errorf("checkpoint belongs to topic %s, not %s", checkpoint.TopicID, topicID)
				}
				topicID = checkpoint.TopicID
				if checkpoint.WorkingDir == "" {
					return fmt.Errorf("checkpoint has no recorded working directory")
				}
				if err := internal.ValidateManagedWorktree(cfg.Runtime, checkpoint.WorkingDir); err != nil {
					return fmt.Errorf("checkpoint working directory is not a valid managed worktree: %w", err)
				}
				if err := os.Chdir(checkpoint.WorkingDir); err != nil {
					return fmt.Errorf("enter checkpoint working directory: %w", err)
				}
				contextResult = checkpoint.Context()
			}

			if topicID == "" {
				topic, err := internal.CreateTopic(db, topicName(prompt))
				if err != nil {
					return err
				}
				topicID = topic.ID
			}
			if _, err := internal.GetTopic(db, topicID); err != nil {
				return err
			}
			if contextResult == nil {
				contextResult, err = internal.BuildContext(db, cfg, topicID, prompt)
				if err != nil {
					return err
				}
			}

			run, err := internal.CreateRun(db, topicID, contextResult.ParentNodeID, os.Getpid(), false)
			if err != nil {
				return err
			}
			workingDir, err := os.Getwd()
			if err != nil {
				return err
			}
			if err := internal.ValidateManagedWorktree(cfg.Runtime, workingDir); err != nil {
				_ = internal.FinishRun(db, run.ID, "error")
				return fmt.Errorf("external commands require a managed worktree; rerun with --worktree <name>: %w", err)
			}
			registry, err := buildRegistry(db, cfg, workingDir, approveAll, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
			if err != nil {
				_ = internal.FinishRun(db, run.ID, "error")
				return err
			}
			internal.SetCurrentTopic(topicID)
			out := internal.NewOutput(root.format)
			messages, runErr := internal.RunLoop(cfg, contextResult, registry, internal.DefaultHooks(), out, &internal.RunContext{
				DB:           db,
				RunID:        run.ID,
				TopicID:      topicID,
				ParentNodeID: contextResult.ParentNodeID,
				WorkingDir:   workingDir,
				Checkpoints:  internal.DefaultCheckpointStore(),
			}, internal.NewLLMLogger(topicID, run.ID))
			if runErr != nil {
				_ = internal.FinishRun(db, run.ID, "error")
				return runErr
			}
			if _, err := internal.SaveMessagesAndAdvanceLeaf(db, topicID, run.ID, internal.PersistedMessages(messages)); err != nil {
				_ = internal.FinishRun(db, run.ID, "error")
				return err
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&prompt, "prompt", "p", "", "Message for the agent")
	cmd.Flags().StringVarP(&topicID, "topic", "t", "", "Existing topic ID")
	cmd.Flags().StringVar(&worktreeName, "worktree", "", "Create or reuse this isolated worktree")
	cmd.Flags().StringVar(&worktreeBase, "worktree-base", "", "Base branch for --worktree (default from config)")
	cmd.Flags().StringVar(&resumeRunID, "resume-run", "", "Run ID that owns the checkpoint")
	cmd.Flags().StringVar(&checkpointID, "checkpoint", "", "Checkpoint ID to resume")
	cmd.Flags().BoolVar(&approveAll, "yes", false, "Approve all external commands for this invocation")
	return cmd
}

func newWorktreeCommand() *cobra.Command {
	var repo, base string
	root := &cobra.Command{Use: "worktree", Short: "Manage agent-isolated Git worktrees"}
	root.PersistentFlags().StringVar(&repo, "repo", ".", "Repository or existing worktree path")
	root.PersistentFlags().StringVar(&base, "base", "", "Base branch override")
	manager := func() (*internal.WorktreeManager, error) {
		cfg, err := internal.LoadConfig()
		if err != nil {
			return nil, err
		}
		runtimeConfig := cfg.Runtime.Normalized()
		if base != "" {
			runtimeConfig.Worktree.BaseBranch = base
		}
		return internal.NewWorktreeManager(runtimeConfig), nil
	}
	root.AddCommand(&cobra.Command{
		Use: "create <name>", Args: cobra.ExactArgs(1), Short: "Create or reuse a managed worktree",
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := manager()
			if err != nil { return err }
			worktree, err := m.Create(cmd.Context(), repo, args[0])
			if err != nil { return err }
			return json.NewEncoder(cmd.OutOrStdout()).Encode(worktree)
		},
	})
	root.AddCommand(&cobra.Command{
		Use: "list", Args: cobra.NoArgs, Short: "List Git worktrees",
		RunE: func(cmd *cobra.Command, _ []string) error {
			m, err := manager()
			if err != nil { return err }
			worktrees, err := m.List(cmd.Context(), repo)
			if err != nil { return err }
			return json.NewEncoder(cmd.OutOrStdout()).Encode(worktrees)
		},
	})
	var force bool
	remove := &cobra.Command{
		Use: "remove <name>", Args: cobra.ExactArgs(1), Short: "Remove a clean managed worktree",
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := manager()
			if err != nil { return err }
			return m.Remove(cmd.Context(), repo, args[0], force)
		},
	}
	remove.Flags().BoolVar(&force, "force", false, "Remove even when the worktree has uncommitted changes")
	root.AddCommand(remove)
	return root
}

func newCheckpointCommand() *cobra.Command {
	var runID string
	root := &cobra.Command{Use: "checkpoint", Short: "Inspect or remove durable agent checkpoints"}
	root.PersistentFlags().StringVar(&runID, "run", "", "Run ID")
	requireRun := func() error {
		if runID == "" { return fmt.Errorf("--run is required") }
		return nil
	}
	root.AddCommand(&cobra.Command{
		Use: "list", Args: cobra.NoArgs, Short: "List checkpoints for a run",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := requireRun(); err != nil { return err }
			items, err := internal.DefaultCheckpointStore().List(runID)
			if err != nil { return err }
			return json.NewEncoder(cmd.OutOrStdout()).Encode(items)
		},
	})
	root.AddCommand(&cobra.Command{
		Use: "show <checkpoint-id>", Args: cobra.ExactArgs(1), Short: "Show one checkpoint",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireRun(); err != nil { return err }
			item, err := internal.DefaultCheckpointStore().Load(runID, args[0])
			if err != nil { return err }
			return json.NewEncoder(cmd.OutOrStdout()).Encode(item)
		},
	})
	root.AddCommand(&cobra.Command{
		Use: "delete <checkpoint-id>", Args: cobra.ExactArgs(1), Short: "Delete one checkpoint",
		RunE: func(_ *cobra.Command, args []string) error {
			if err := requireRun(); err != nil { return err }
			return internal.DefaultCheckpointStore().Delete(runID, args[0])
		},
	})
	return root
}

func topicName(prompt string) string {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" { return "resumed run" }
	runes := []rune(prompt)
	if len(runes) > 40 { return string(runes[:40]) }
	return prompt
}

func buildRegistry(db *sql.DB, cfg *internal.Config, workingDir string, approveAll bool, input io.Reader, output, errOutput io.Writer) (*internal.Registry, error) {
	registry := internal.NewRegistry()
	internal.RegisterMemoryCommands(registry, db, cfg)
	internal.RegisterTopicCommands(registry, db, cfg)
	internal.RegisterFSCommands(registry)
	internal.RegisterSkillCommands(registry, cfg)
	internal.RegisterReadOnlyConfigCommands(registry)
	internal.RegisterBrowserCommands(registry, cfg)
	executor, err := internal.NewCommandExecutor(cfg.Runtime, workingDir, interactiveApprover(approveAll, input, output, errOutput))
	if err != nil {
		return nil, err
	}
	internal.RegisterSecureCommands(registry, executor)
	return registry, nil
}

func interactiveApprover(approveAll bool, input io.Reader, output, errOutput io.Writer) internal.ApprovalFunc {
	reader := bufio.NewReader(input)
	return func(_ context.Context, request internal.ApprovalRequest) (bool, error) {
		if approveAll { return true, nil }
		fmt.Fprintf(errOutput, "Approve external command in %s?\n  program: %q\n  args: %q\n[y/N] ", request.WorkingDir, request.Program, request.Args)
		line, err := reader.ReadString('\n')
		if err != nil { return false, err }
		return strings.EqualFold(strings.TrimSpace(line), "y") || strings.EqualFold(strings.TrimSpace(line), "yes"), nil
	}
}
