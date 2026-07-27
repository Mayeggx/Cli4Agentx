package internal

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckpointStoreRoundTrip(t *testing.T) {
	store := NewCheckpointStore(t.TempDir())
	meta, err := store.Save(Checkpoint{
		RunID:        "run-1",
		TopicID:      "topic-1",
		ParentNodeID: "node-1",
		SystemPrompt: "system",
		Turn:         2,
		Stage:        "after_tool",
		Messages:     []AgentMessage{AgentTextMessage("user_input", "user", "hello", true, true)},
	})
	if err != nil {
		t.Fatalf("save checkpoint: %v", err)
	}
	loaded, err := store.Load("run-1", meta.ID)
	if err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}
	if loaded.TopicID != "topic-1" || loaded.Turn != 2 || len(loaded.Messages) != 1 {
		t.Fatalf("unexpected checkpoint: %#v", loaded)
	}
	contextResult := loaded.Context()
	if contextResult.SystemPrompt != "system" || contextResult.ParentNodeID != "node-1" {
		t.Fatalf("checkpoint context was not reconstructed")
	}
	if err := store.Delete("run-1", meta.ID); err != nil {
		t.Fatalf("delete checkpoint: %v", err)
	}
}

func TestCheckpointStoreRejectsTraversal(t *testing.T) {
	store := NewCheckpointStore(t.TempDir())
	message := []AgentMessage{AgentTextMessage("user_input", "user", "hello", true, true)}
	for _, runID := range []string{"../escape", ".", "..", ".hidden"} {
		if _, err := store.Save(Checkpoint{RunID: runID, TopicID: "topic", SystemPrompt: "system", Messages: message}); err == nil {
			t.Fatalf("expected unsafe run ID %q to be rejected", runID)
		}
	}
	if _, err := store.Save(Checkpoint{RunID: "run", TopicID: "topic", SystemPrompt: "system"}); err == nil {
		t.Fatal("expected empty checkpoint history to be rejected")
	}
}

func TestCommandExecutorRequiresApproval(t *testing.T) {
	executor, err := NewCommandExecutor(RuntimeConfig{Sandbox: SandboxConfig{Mode: "disabled"}, CommandApproval: CommandApprovalConfig{Mode: ApprovalAlways}}, t.TempDir(), nil)
	if err != nil {
		t.Fatalf("new executor: %v", err)
	}
	if _, err := executor.Execute(context.Background(), "echo", []string{"hello"}, ""); err == nil || !strings.Contains(err.Error(), "requires user approval") {
		t.Fatalf("expected approval rejection, got %v", err)
	}
}

func TestCommandExecutorRejectsShellInterpreter(t *testing.T) {
	executor, err := NewCommandExecutor(RuntimeConfig{Sandbox: SandboxConfig{Mode: "disabled"}, CommandApproval: CommandApprovalConfig{Mode: ApprovalNever}}, t.TempDir(), nil)
	if err != nil {
		t.Fatalf("new executor: %v", err)
	}
	if _, err := executor.Execute(context.Background(), "sh", []string{"-c", "echo unsafe"}, ""); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("expected shell interpreter rejection, got %v", err)
	}
	if _, err := executor.Execute(context.Background(), "echo", []string{"ok\nforged"}, ""); err == nil || !strings.Contains(err.Error(), "control characters") {
		t.Fatalf("expected terminal-control argument rejection, got %v", err)
	}
}

func TestWorktreeNameAndPorcelainParsing(t *testing.T) {
	if _, err := normalizeWorktreeName("../escape"); err == nil {
		t.Fatal("expected invalid worktree name to be rejected")
	}
	items := parseWorktreeList("worktree /tmp/repo\nHEAD abc\nbranch refs/heads/main\n\nworktree /tmp/repo/.agentx/worktrees/task\nHEAD def\nbranch refs/heads/agentx/task\n")
	if len(items) != 2 || items[1].Branch != "agentx/task" {
		t.Fatalf("unexpected parsed worktrees: %#v", items)
	}
}

func TestWorktreeCreateAndCleanRemove(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required for worktree lifecycle test")
	}
	repo := t.TempDir()
	runTestGit(t, repo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repo, "add", "README.md")
	runTestGit(t, repo, "-c", "user.name=test", "-c", "user.email=test@example.invalid", "commit", "-m", "initial")

	manager := NewWorktreeManager(RuntimeConfig{Worktree: WorktreeConfig{BaseBranch: "main", RootDir: ".agentx/worktrees"}})
	worktree, err := manager.Create(context.Background(), repo, "task1")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	ignore, err := os.ReadFile(filepath.Join(repo, ".agentx", "worktrees", ".gitignore"))
	if err != nil || string(ignore) != "*\n!.gitignore\n" {
		t.Fatalf("managed worktree root was not ignored: %q, %v", ignore, err)
	}
	if worktree.Branch != "agentx/task1" {
		t.Fatalf("unexpected branch: %s", worktree.Branch)
	}
	if _, err := os.Stat(filepath.Join(worktree.Path, "README.md")); err != nil {
		t.Fatalf("worktree did not contain repository file: %v", err)
	}
	if err := ValidateManagedWorktree(RuntimeConfig{Worktree: WorktreeConfig{BaseBranch: "main", RootDir: ".agentx/worktrees"}}, worktree.Path); err != nil {
		t.Fatalf("validate managed worktree: %v", err)
	}
	if err := manager.Remove(context.Background(), repo, "task1", false); err != nil {
		t.Fatalf("remove clean worktree: %v", err)
	}
	if err := ValidateManagedWorktree(RuntimeConfig{Worktree: WorktreeConfig{BaseBranch: "main", RootDir: ".agentx/worktrees"}}, repo); err == nil {
		t.Fatal("expected primary repository to be rejected as an unmanaged worktree")
	}
}

func TestRuntimeConfigDefaultsFailClosed(t *testing.T) {
	config := RuntimeConfig{}.Normalized()
	if config.Sandbox.Mode != "required" || config.CommandApproval.Mode != ApprovalAlways || !config.CommandApproval.Audit {
		t.Fatalf("unexpected insecure defaults: %#v", config)
	}
}

func runTestGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}
