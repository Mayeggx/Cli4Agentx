package internal

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

// LLMLogger writes one JSON file per LLM call under logs/{sessionID}/.
// session = topicID, so all runs within a topic share the same log folder.
type LLMLogger struct {
	sessionDir string
	sessionID  string
	runID      string
	callCount  atomic.Int64
}

// LLMLogEntry is the structure written to each log file.
type LLMLogEntry struct {
	SessionID  string          `json:"session_id"`
	RunID      string          `json:"run_id"`
	CallIndex  int64           `json:"call_index"`
	Timestamp  string          `json:"timestamp"`
	DurationMs int64           `json:"duration_ms"`
	Provider   string          `json:"provider"`
	Model      string          `json:"model"`
	Request    json.RawMessage `json:"request"`
	Response   *llmLogResponse `json:"response,omitempty"`
	Error      string          `json:"error,omitempty"`
}

type llmLogResponse struct {
	Content   string     `json:"content"`
	Reasoning string     `json:"reasoning,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

// NewLLMLogger creates a logger for a session (topicID) and run.
// Log files will be placed at: logs/{sessionID}/{runID}_call_{N:03d}_{timestamp}.json
func NewLLMLogger(topicID, runID string) *LLMLogger {
	dir := filepath.Join(clipBase(), "logs", topicID)
	_ = os.MkdirAll(dir, 0o755)
	return &LLMLogger{
		sessionDir: dir,
		sessionID:  topicID,
		runID:      runID,
	}
}

// Log records a completed LLM call.
// reqBody is the raw JSON request body sent to the API.
// resp is the assembled response (nil on error).
// errStr is the error message if the call failed.
// durationMs is the call duration in milliseconds.
func (l *LLMLogger) Log(provider, model string, reqBody json.RawMessage, resp *LLMResponse, errStr string, durationMs int64) {
	idx := l.callCount.Add(1)
	ts := time.Now()
	filename := fmt.Sprintf("%s_call_%03d_%s.json", l.runID, idx, ts.Format("20060102-150405"))
	path := filepath.Join(l.sessionDir, filename)

	entry := LLMLogEntry{
		SessionID:  l.sessionID,
		RunID:      l.runID,
		CallIndex:  idx,
		Timestamp:  ts.Format(time.RFC3339),
		DurationMs: durationMs,
		Provider:   provider,
		Model:      model,
		Request:    reqBody,
	}

	if resp != nil {
		entry.Response = &llmLogResponse{
			Content:   resp.Content,
			Reasoning: resp.Reasoning,
			ToolCalls: resp.ToolCalls,
		}
	}
	if errStr != "" {
		entry.Error = errStr
	}

	data, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o644)
}
