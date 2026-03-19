package internal

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const maxIterations = 20

type RunContext struct {
	DB    *sql.DB
	RunID string
}

// RunLoop executes the agentic loop.
// contextResult comes from BuildContext (pre-assembled system prompt + messages).
// logger is optional; pass nil to disable LLM call logging.
func RunLoop(cfg *Config, ctx *ContextResult, registry *Registry, out Output, rc *RunContext, logger *LLMLogger) ([]Message, error) {
	context := []Message{TextMessage("system", ctx.SystemPrompt)}
	context = append(context, ctx.Messages...)

	lastMsg := ctx.Messages[len(ctx.Messages)-1]
	newMsgs := []Message{lastMsg}

	tools := []ToolDef{RunToolDef(registry.Help())}

	for i := 0; i < maxIterations; i++ {
		// check inbox
		if rc != nil && rc.DB != nil {
			if injected, _ := DrainInbox(rc.DB, rc.RunID); len(injected) > 0 {
				for _, msg := range injected {
					out.Inject(msg)
					injectMsg := TextMessage("user", fmt.Sprintf("<user>\n%s\n</user>", msg))
					context = append(context, injectMsg)
					newMsgs = append(newMsgs, injectMsg)
				}
			}
		}

		thinkingStarted := false
		resp, err := CallLLM(cfg, context, tools, func(token string) {
			out.Text(token)
		}, func(token string) {
			if !thinkingStarted {
				out.Thinking("[thinking] ")
				thinkingStarted = true
			}
			out.Thinking(token)
		}, logger)
		if err != nil {
			return nil, err
		}

		// --- tool_calls ---
		if len(resp.ToolCalls) > 0 {
			assistantMsg := Message{Role: "assistant", ToolCalls: resp.ToolCalls}
			if resp.Content != "" {
				assistantMsg.Content = &resp.Content
			}
			if resp.Reasoning != "" {
				assistantMsg.Reasoning = &resp.Reasoning
			}
			context = append(context, assistantMsg)
			newMsgs = append(newMsgs, assistantMsg)

			for _, tc := range resp.ToolCalls {
				out.ToolCall(tc.Function.Name, tc.Function.Arguments)
				result := execToolCall(registry, tc)
				out.ToolResult(result)
				toolResult := ToolResultMessage(tc.ID, result)
				// Auto-attach vision data for images referenced in tool results
				toolResult.Images = extractImagesFromResult(result)
				context = append(context, toolResult)
				newMsgs = append(newMsgs, toolResult)
			}
			continue
		}

		// --- stop → atomic finish ---
		assistantMsg := TextMessage("assistant", resp.Content)
		if resp.Reasoning != "" {
			assistantMsg.Reasoning = &resp.Reasoning
		}

		if rc != nil && rc.DB != nil {
			injected, err := TryFinishRun(rc.DB, rc.RunID, "done")
			if err != nil {
				return nil, fmt.Errorf("finish run: %w", err)
			}
			if len(injected) > 0 {
				newMsgs = append(newMsgs, assistantMsg)
				context = append(context, assistantMsg)
				for _, msg := range injected {
					out.Inject(msg)
					injectMsg := TextMessage("user", fmt.Sprintf("<user>\n%s\n</user>", msg))
					context = append(context, injectMsg)
					newMsgs = append(newMsgs, injectMsg)
				}
				continue
			}
		}

		newMsgs = append(newMsgs, assistantMsg)
		out.Done()
		return newMsgs, nil
	}

	return nil, fmt.Errorf("agentic loop exceeded %d iterations", maxIterations)
}

func execToolCall(registry *Registry, tc ToolCall) string {
	var args struct {
		Command string `json:"command"`
		Stdin   string `json:"stdin"`
	}
	if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
		return fmt.Sprintf("[error] parse arguments: %v", err)
	}

	// If LLM uses a command name as the tool name instead of "run",
	// prepend it to the command string (avoid double-prefix).
	if tc.Function.Name != "run" {
		if args.Command == "" || !strings.HasPrefix(args.Command, tc.Function.Name) {
			cmd := tc.Function.Name
			if args.Command != "" {
				cmd += " " + args.Command
			}
			args.Command = cmd
		}
	}

	if args.Command == "" {
		return "[error] empty command"
	}

	// Layer 1: pure Unix execution — no metadata, no truncation.
	// Pipeline output flows between commands unmodified.
	start := time.Now()
	result, exitCode := registry.Exec(args.Command, args.Stdin)
	elapsed := time.Since(start)

	// Layer 2: LLM presentation — applied once, after the full pipeline completes.
	return presentForLLM(result, exitCode, elapsed)
}

// presentForLLM applies LLM-specific output processing after Layer 1 finishes.
// This is the only place where output is modified before reaching the LLM.
//
//  1. Binary interception: prevents garbage tokens from binary content.
//  2. Truncation: caps large outputs with an overflow indicator.
//  3. Exit footer: appends [exit:N | Xms] so the LLM can read success/cost signals.
func presentForLLM(output string, exitCode int, elapsed time.Duration) string {
	// 1. Binary interception
	if looksLikeBinary(output) {
		output = "[binary data omitted — use 'cat -b' for base64 or 'see' for images]"
		if exitCode == 0 {
			exitCode = 1
		}
	}

	// 2. Truncation with overflow indicator
	const maxChars = 8000
	if len(output) > maxChars {
		overflow := len(output) - maxChars
		output = output[:maxChars] + fmt.Sprintf("\n[truncated — %d chars omitted, use head/tail/grep to narrow down]", overflow)
	}

	// 3. Exit footer
	ms := elapsed.Milliseconds()
	var dur string
	if ms < 1000 {
		dur = fmt.Sprintf("%dms", ms)
	} else {
		dur = fmt.Sprintf("%.1fs", elapsed.Seconds())
	}
	footer := fmt.Sprintf("[exit:%d | %s]", exitCode, dur)
	if output == "" {
		return footer
	}
	return output + "\n" + footer
}

// looksLikeBinary returns true if the string contains a high proportion of
// non-printable bytes (>5%), indicating binary rather than text content.
func looksLikeBinary(s string) bool {
	if len(s) == 0 {
		return false
	}
	check := len(s)
	if check > 512 {
		check = 512
	}
	nonPrintable := 0
	for i := 0; i < check; i++ {
		b := s[i]
		// Allow: tab(0x09), newline(0x0a), carriage return(0x0d)
		if b < 0x09 || (b > 0x0d && b < 0x20) || b == 0x7f {
			nonPrintable++
		}
	}
	return nonPrintable*20 > check // >5% non-printable = binary
}

// localDataURLRe matches local-data://data/... paths in tool results
var localDataURLRe = regexp.MustCompile(`local-data://data/((?:topics/[^/]+/|images/)[^\s)]+)`)

// extractImagesFromResult scans a tool result for local-data:// image URLs,
// reads the corresponding files from data/, and returns ImageData for vision.
func extractImagesFromResult(result string) []ImageData {
	matches := localDataURLRe.FindAllStringSubmatch(result, -1)
	if len(matches) == 0 {
		return nil
	}

	var images []ImageData
	for _, m := range matches {
		relPath := m[1] // e.g., "topics/{id}/screenshot.png"
		if !IsImageFile(relPath) {
			continue
		}

		absPath := filepath.Join(dataRoot(), relPath)
		data, err := os.ReadFile(absPath)
		if err != nil {
			continue
		}

		mime := "image/png"
		ext := strings.ToLower(filepath.Ext(relPath))
		switch ext {
		case ".jpg", ".jpeg":
			mime = "image/jpeg"
		case ".webp":
			mime = "image/webp"
		case ".gif":
			mime = "image/gif"
		}

		images = append(images, ImageData{
			Base64:   base64.StdEncoding.EncodeToString(data),
			MimeType: mime,
		})
	}

	return images
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}
