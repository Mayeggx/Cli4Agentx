package internal

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Checkpoint is a durable, resumable snapshot of an agent loop. It stores only
// conversation state and execution metadata; repository files remain managed by
// Git worktrees.
type Checkpoint struct {
	ID           string         `json:"id"`
	RunID        string         `json:"run_id"`
	TopicID      string         `json:"topic_id"`
	ParentNodeID string         `json:"parent_node_id,omitempty"`
	SystemPrompt string         `json:"system_prompt"`
	WorkingDir   string         `json:"working_dir,omitempty"`
	Turn         int            `json:"turn"`
	Stage        string         `json:"stage"`
	CreatedAt    time.Time      `json:"created_at"`
	Messages     []AgentMessage `json:"messages"`
}

// Context reconstructs the LLM context needed to continue from a checkpoint.
func (c Checkpoint) Context() *ContextResult {
	messages := append([]AgentMessage(nil), c.Messages...)
	return &ContextResult{
		SystemPrompt: c.SystemPrompt,
		Messages:     messages,
		ParentNodeID: c.ParentNodeID,
	}
}

// CheckpointMeta is intentionally small so callers can list checkpoints
// without loading full LLM histories.
type CheckpointMeta struct {
	ID        string    `json:"id"`
	RunID     string    `json:"run_id"`
	TopicID   string    `json:"topic_id"`
	Turn      int       `json:"turn"`
	Stage     string    `json:"stage"`
	CreatedAt time.Time `json:"created_at"`
}

type CheckpointStore struct {
	root string
}

func DefaultCheckpointStore() *CheckpointStore {
	return NewCheckpointStore(filepath.Join(dataRoot(), "checkpoints"))
}

func NewCheckpointStore(root string) *CheckpointStore {
	return &CheckpointStore{root: filepath.Clean(root)}
}

func (s *CheckpointStore) Save(checkpoint Checkpoint) (CheckpointMeta, error) {
	if s == nil || s.root == "" {
		return CheckpointMeta{}, fmt.Errorf("checkpoint store is not configured")
	}
	if !safeCheckpointPart(checkpoint.RunID) || !safeCheckpointPart(checkpoint.TopicID) {
		return CheckpointMeta{}, fmt.Errorf("checkpoint requires safe run and topic IDs")
	}
	if checkpoint.SystemPrompt == "" || len(checkpoint.Messages) == 0 {
		return CheckpointMeta{}, fmt.Errorf("checkpoint requires a system prompt and at least one message")
	}
	if checkpoint.CreatedAt.IsZero() {
		checkpoint.CreatedAt = time.Now().UTC()
	}
	if checkpoint.ID == "" {
		id, err := newCheckpointID(checkpoint.CreatedAt)
		if err != nil {
			return CheckpointMeta{}, err
		}
		checkpoint.ID = id
	}
	if !safeCheckpointPart(checkpoint.ID) {
		return CheckpointMeta{}, fmt.Errorf("invalid checkpoint ID")
	}

	dir, err := s.runDir(checkpoint.RunID)
	if err != nil {
		return CheckpointMeta{}, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return CheckpointMeta{}, fmt.Errorf("create checkpoint directory: %w", err)
	}
	data, err := json.Marshal(checkpoint)
	if err != nil {
		return CheckpointMeta{}, fmt.Errorf("encode checkpoint: %w", err)
	}
	path, err := s.checkpointPath(checkpoint.RunID, checkpoint.ID)
	if err != nil {
		return CheckpointMeta{}, err
	}
	if err := writePrivateAtomic(path, append(data, '\n')); err != nil {
		return CheckpointMeta{}, fmt.Errorf("write checkpoint: %w", err)
	}
	return checkpoint.meta(), nil
}

func (s *CheckpointStore) Load(runID, checkpointID string) (*Checkpoint, error) {
	path, err := s.checkpointPath(runID, checkpointID)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read checkpoint: %w", err)
	}
	var checkpoint Checkpoint
	if err := json.Unmarshal(data, &checkpoint); err != nil {
		return nil, fmt.Errorf("parse checkpoint: %w", err)
	}
	if checkpoint.ID != checkpointID || checkpoint.RunID != runID || !safeCheckpointPart(checkpoint.TopicID) || checkpoint.SystemPrompt == "" || len(checkpoint.Messages) == 0 {
		return nil, fmt.Errorf("checkpoint identity validation failed")
	}
	return &checkpoint, nil
}

func (s *CheckpointStore) List(runID string) ([]CheckpointMeta, error) {
	dir, err := s.runDir(runID)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return []CheckpointMeta{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read checkpoint directory: %w", err)
	}

	metas := make([]CheckpointMeta, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		checkpoint, err := s.Load(runID, id)
		if err != nil {
			continue
		}
		metas = append(metas, checkpoint.meta())
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].CreatedAt.After(metas[j].CreatedAt) })
	return metas, nil
}

func (s *CheckpointStore) Delete(runID, checkpointID string) error {
	if _, err := s.Load(runID, checkpointID); err != nil {
		return err
	}
	path, err := s.checkpointPath(runID, checkpointID)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("delete checkpoint: %w", err)
	}
	return nil
}

func (c Checkpoint) meta() CheckpointMeta {
	return CheckpointMeta{ID: c.ID, RunID: c.RunID, TopicID: c.TopicID, Turn: c.Turn, Stage: c.Stage, CreatedAt: c.CreatedAt}
}

func checkpointStore(rc *RunContext) *CheckpointStore {
	if rc == nil {
		return nil
	}
	if rc.Checkpoints != nil {
		return rc.Checkpoints
	}
	return DefaultCheckpointStore()
}

func saveLoopCheckpoint(store *CheckpointStore, rc *RunContext, systemPrompt string, messages []AgentMessage, turn int, stage string) error {
	if store == nil || rc == nil || rc.RunID == "" || rc.TopicID == "" {
		return nil
	}
	_, err := store.Save(Checkpoint{
		RunID:        rc.RunID,
		TopicID:      rc.TopicID,
		ParentNodeID: rc.ParentNodeID,
		SystemPrompt: systemPrompt,
		WorkingDir:   rc.WorkingDir,
		Turn:         turn,
		Stage:        stage,
		Messages:     append([]AgentMessage(nil), messages...),
	})
	return err
}

func newCheckpointID(now time.Time) (string, error) {
	var random [6]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("create checkpoint ID: %w", err)
	}
	return now.UTC().Format("20060102T150405.000000000Z") + "-" + hex.EncodeToString(random[:]), nil
}

func (s *CheckpointStore) runDir(runID string) (string, error) {
	if s == nil || s.root == "" || !safeCheckpointPart(runID) {
		return "", fmt.Errorf("invalid run ID")
	}
	return secureChildPath(s.root, runID)
}

func (s *CheckpointStore) checkpointPath(runID, checkpointID string) (string, error) {
	if !safeCheckpointPart(checkpointID) {
		return "", fmt.Errorf("invalid checkpoint reference")
	}
	dir, err := s.runDir(runID)
	if err != nil {
		return "", err
	}
	return secureChildPath(dir, checkpointID+".json")
}

func safeCheckpointPart(value string) bool {
	if value == "" || value == "." || value == ".." || len(value) > 160 || value != filepath.Base(value) {
		return false
	}
	for i, r := range value {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '-' && r != '_' && r != '.' {
			return false
		}
		if i == 0 && r == '.' {
			return false
		}
	}
	return true
}

func secureChildPath(root string, child string) (string, error) {
	root = filepath.Clean(root)
	path := filepath.Join(root, child)
	if !isWithin(path, root) {
		return "", fmt.Errorf("path escapes checkpoint store")
	}
	return path, nil
}

func writePrivateAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".checkpoint-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
