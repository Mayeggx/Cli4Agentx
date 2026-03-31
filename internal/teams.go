package internal

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

const teamConfigFileName = "team.yaml"

type TeamConfig struct {
	Name             string           `yaml:"name" json:"name"`
	Description      string           `yaml:"description,omitempty" json:"description,omitempty"`
	SharedPromptFile string           `yaml:"shared_prompt_file,omitempty" json:"shared_prompt_file,omitempty"`
	Roles            []TeamRoleConfig `yaml:"roles" json:"roles"`
}

type TeamRoleConfig struct {
	Name        string   `yaml:"name" json:"name"`
	Description string   `yaml:"description,omitempty" json:"description,omitempty"`
	PromptFile  string   `yaml:"prompt_file" json:"prompt_file"`
	Artifact    string   `yaml:"artifact,omitempty" json:"artifact,omitempty"`
	DependsOn   []string `yaml:"depends_on,omitempty" json:"depends_on,omitempty"`
}

type TeamDefinition struct {
	Name       string      `json:"name"`
	Source     string      `json:"source"`
	RootDir    string      `json:"root_dir"`
	ConfigPath string      `json:"config_path"`
	Config     *TeamConfig `json:"config"`
}

type TeamDefinitionInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Source      string `json:"source"`
	Path        string `json:"path"`
}

type TeamRoleState struct {
	Name        string   `json:"name"`
	TopicID     string   `json:"topic_id"`
	Artifact    string   `json:"artifact"`
	Stage       int      `json:"stage"`
	DependsOn   []string `json:"depends_on,omitempty"`
	Status      string   `json:"status"`
	RunID       string   `json:"run_id,omitempty"`
	Summary     string   `json:"summary,omitempty"`
	LastError   string   `json:"last_error,omitempty"`
	StartedAt   int64    `json:"started_at,omitempty"`
	FinishedAt  int64    `json:"finished_at,omitempty"`
	PromptFile  string   `json:"prompt_file,omitempty"`
	Description string   `json:"description,omitempty"`
}

type TeamMessage struct {
	ID             string `json:"id"`
	FromRole       string `json:"from_role"`
	ToRole         string `json:"to_role"`
	Content        string `json:"content"`
	CreatedAt      int64  `json:"created_at"`
	DeliveredAt    int64  `json:"delivered_at,omitempty"`
	DeliveryStatus string `json:"delivery_status,omitempty"`
}

type TeamRunState struct {
	ID                 string          `json:"id"`
	TeamName           string          `json:"team_name"`
	Runner             string          `json:"runner"`
	Task               string          `json:"task"`
	Status             string          `json:"status"`
	CoordinatorTopicID string          `json:"coordinator_topic_id"`
	CurrentStage       int             `json:"current_stage,omitempty"`
	CreatedAt          int64           `json:"created_at"`
	FinishedAt         int64           `json:"finished_at,omitempty"`
	Roles              []TeamRoleState `json:"roles"`
	Messages           []TeamMessage   `json:"messages,omitempty"`
}

func teamsDataRoot() string {
	return filepath.Join(dataRoot(), "teams")
}

func teamRunsRoot() string {
	return filepath.Join(teamsDataRoot(), "runs")
}

func teamsSeedRoot() string {
	return filepath.Join(clipBase(), "seed", "teams")
}

func teamConfigPath(root string) string {
	return filepath.Join(root, teamConfigFileName)
}

func teamRunPath(id string) string {
	return filepath.Join(teamRunsRoot(), id+".json")
}

func teamRunLockPath(id string) string {
	return filepath.Join(teamRunsRoot(), id+".lock")
}

func ListTeamDefinitions() ([]TeamDefinitionInfo, error) {
	resultByName := map[string]TeamDefinitionInfo{}
	for _, candidate := range []struct {
		root   string
		source string
	}{
		{root: teamsDataRoot(), source: "data"},
		{root: teamsSeedRoot(), source: "seed"},
	} {
		entries, err := os.ReadDir(candidate.root)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			cfgPath := teamConfigPath(filepath.Join(candidate.root, entry.Name()))
			raw, err := os.ReadFile(cfgPath)
			if err != nil {
				continue
			}
			var cfg TeamConfig
			if err := yaml.Unmarshal(raw, &cfg); err != nil {
				continue
			}
			if _, exists := resultByName[entry.Name()]; exists {
				continue
			}
			resultByName[entry.Name()] = TeamDefinitionInfo{
				Name:        entry.Name(),
				Description: cfg.Description,
				Source:      candidate.source,
				Path:        cfgPath,
			}
		}
	}
	names := make([]string, 0, len(resultByName))
	for name := range resultByName {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]TeamDefinitionInfo, 0, len(names))
	for _, name := range names {
		result = append(result, resultByName[name])
	}
	return result, nil
}

func LoadTeamDefinition(name string) (*TeamDefinition, error) {
	for _, root := range []struct {
		base   string
		source string
	}{
		{base: teamsDataRoot(), source: "data"},
		{base: teamsSeedRoot(), source: "seed"},
	} {
		dir := filepath.Join(root.base, name)
		cfgPath := teamConfigPath(dir)
		raw, err := os.ReadFile(cfgPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("read team config: %w", err)
		}
		var cfg TeamConfig
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("parse team config %s: %w", cfgPath, err)
		}
		if cfg.Name == "" {
			cfg.Name = name
		}
		if err := normalizeAndValidateTeamConfig(&cfg); err != nil {
			return nil, fmt.Errorf("invalid team config %s: %w", cfgPath, err)
		}
		return &TeamDefinition{
			Name:       name,
			Source:     root.source,
			RootDir:    dir,
			ConfigPath: cfgPath,
			Config:     &cfg,
		}, nil
	}
	return nil, fmt.Errorf("team %q not found", name)
}

func (d *TeamDefinition) ReadPrompt(relPath string) (string, error) {
	resolved := relPath
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(d.RootDir, relPath)
	}
	raw, err := os.ReadFile(resolved)
	if err != nil {
		return "", fmt.Errorf("read prompt %s: %w", resolved, err)
	}
	return strings.TrimSpace(string(raw)), nil
}

func ScaffoldTeam(name string) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("team name is required")
	}
	srcDir := filepath.Join(teamsSeedRoot(), "default")
	dstDir := filepath.Join(teamsDataRoot(), name)
	if _, err := os.Stat(dstDir); err == nil {
		return "", fmt.Errorf("team %q already exists at %s", name, dstDir)
	}
	if err := copyDir(srcDir, dstDir); err != nil {
		return "", err
	}
	def, err := LoadTeamDefinition(name)
	if err != nil {
		return "", err
	}
	def.Config.Name = name
	out, err := yaml.Marshal(def.Config)
	if err != nil {
		return "", fmt.Errorf("marshal scaffolded team: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dstDir, teamConfigFileName), out, 0o644); err != nil {
		return "", fmt.Errorf("write scaffolded team: %w", err)
	}
	return filepath.Join(dstDir, teamConfigFileName), nil
}

func SaveTeamRunState(state *TeamRunState) error {
	if err := os.MkdirAll(teamRunsRoot(), 0o755); err != nil {
		return fmt.Errorf("create team runs dir: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal team run state: %w", err)
	}
	path := teamRunPath(state.ID)
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func LoadTeamRunState(id string) (*TeamRunState, error) {
	raw, err := os.ReadFile(teamRunPath(id))
	if err != nil {
		return nil, fmt.Errorf("read team run state: %w", err)
	}
	var state TeamRunState
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, fmt.Errorf("parse team run state: %w", err)
	}
	return &state, nil
}

func UpdateTeamRunState(id string, mutate func(*TeamRunState) error) error {
	return withTeamRunLock(id, func() error {
		state, err := LoadTeamRunState(id)
		if err != nil {
			return err
		}
		if err := mutate(state); err != nil {
			return err
		}
		return SaveTeamRunState(state)
	})
}

func withTeamRunLock(id string, fn func() error) error {
	if err := os.MkdirAll(teamRunsRoot(), 0o755); err != nil {
		return err
	}
	lockPath := teamRunLockPath(id)
	deadline := time.Now().Add(5 * time.Second)
	for {
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			_ = f.Close()
			defer os.Remove(lockPath)
			return fn()
		}
		if !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("acquire team run lock: %w", err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout acquiring team run lock for %s", id)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func normalizeAndValidateTeamConfig(cfg *TeamConfig) error {
	if len(cfg.Roles) == 0 {
		return fmt.Errorf("at least one role is required")
	}
	seen := map[string]int{}
	for i := range cfg.Roles {
		role := &cfg.Roles[i]
		role.Name = strings.TrimSpace(role.Name)
		role.PromptFile = strings.TrimSpace(role.PromptFile)
		role.Artifact = strings.TrimSpace(role.Artifact)
		if role.Name == "" {
			return fmt.Errorf("role #%d missing name", i+1)
		}
		if role.PromptFile == "" {
			return fmt.Errorf("role %q missing prompt_file", role.Name)
		}
		if role.Artifact == "" {
			role.Artifact = role.Name + ".md"
		}
		if prev, exists := seen[role.Name]; exists {
			return fmt.Errorf("duplicate role %q (positions %d and %d)", role.Name, prev+1, i+1)
		}
		seen[role.Name] = i
	}
	for i := range cfg.Roles {
		role := cfg.Roles[i]
		for _, dep := range role.DependsOn {
			prevIndex, ok := seen[dep]
			if !ok {
				return fmt.Errorf("role %q depends on unknown role %q", role.Name, dep)
			}
			if prevIndex >= i {
				return fmt.Errorf("role %q depends on %q, but dependencies must appear earlier in config order", role.Name, dep)
			}
		}
	}
	return nil
}

func ComputeTeamStages(cfg *TeamConfig) ([][]int, error) {
	stageByRole := make(map[string]int, len(cfg.Roles))
	stagesByIndex := make(map[int][]int)
	maxStage := 0
	for i, role := range cfg.Roles {
		stage := 0
		for _, dep := range role.DependsOn {
			depStage, ok := stageByRole[dep]
			if !ok {
				return nil, fmt.Errorf("role %q depends on unknown role %q", role.Name, dep)
			}
			if depStage+1 > stage {
				stage = depStage + 1
			}
		}
		stageByRole[role.Name] = stage
		stagesByIndex[stage] = append(stagesByIndex[stage], i)
		if stage > maxStage {
			maxStage = stage
		}
	}
	result := make([][]int, 0, maxStage+1)
	for stage := 0; stage <= maxStage; stage++ {
		if len(stagesByIndex[stage]) == 0 {
			continue
		}
		result = append(result, stagesByIndex[stage])
	}
	return result, nil
}

func copyDir(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("stat source dir: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", src)
	}
	if err := os.MkdirAll(dst, info.Mode()); err != nil {
		return fmt.Errorf("create destination dir: %w", err)
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return fmt.Errorf("read source dir: %w", err)
	}
	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
			continue
		}
		if err := copyFile(srcPath, dstPath); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source file: %w", err)
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return fmt.Errorf("stat source file: %w", err)
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode())
	if err != nil {
		return fmt.Errorf("create destination file: %w", err)
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy file: %w", err)
	}
	return nil
}

func NewTeamRunState(id string, def *TeamDefinition, runner, task, coordinatorTopicID string, roleTopics map[string]string) *TeamRunState {
	stages, _ := ComputeTeamStages(def.Config)
	stageByRole := make(map[string]int, len(def.Config.Roles))
	for stageIndex, roleIndices := range stages {
		for _, roleIndex := range roleIndices {
			stageByRole[def.Config.Roles[roleIndex].Name] = stageIndex
		}
	}
	state := &TeamRunState{
		ID:                 id,
		TeamName:           def.Name,
		Runner:             runner,
		Task:               task,
		Status:             "running",
		CoordinatorTopicID: coordinatorTopicID,
		CreatedAt:          time.Now().Unix(),
		Roles:              make([]TeamRoleState, 0, len(def.Config.Roles)),
	}
	for _, role := range def.Config.Roles {
		state.Roles = append(state.Roles, TeamRoleState{
			Name:        role.Name,
			TopicID:     roleTopics[role.Name],
			Artifact:    role.Artifact,
			Stage:       stageByRole[role.Name],
			DependsOn:   append([]string{}, role.DependsOn...),
			Status:      "pending",
			PromptFile:  role.PromptFile,
			Description: role.Description,
		})
	}
	return state
}

func SendTeamMessages(db *sql.DB, teamRunID, fromRole, targetRole, content string) ([]TeamMessage, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, fmt.Errorf("message content is required")
	}

	type delivery struct {
		messageID string
		runID     string
		text      string
	}

	var (
		createdMessages []TeamMessage
		deliveries      []delivery
		coordinatorID   string
	)

	err := withTeamRunLock(teamRunID, func() error {
		state, err := LoadTeamRunState(teamRunID)
		if err != nil {
			return err
		}
		coordinatorID = state.CoordinatorTopicID
		if fromRole != "" && teamRoleByName(state, fromRole) == nil {
			return fmt.Errorf("unknown sender role %q", fromRole)
		}
		targets, err := resolveTeamMessageTargets(state, fromRole, targetRole)
		if err != nil {
			return err
		}
		now := time.Now().Unix()
		for _, role := range targets {
			msg := TeamMessage{
				ID:             uuid.NewString()[:8],
				FromRole:       fromRole,
				ToRole:         role.Name,
				Content:        content,
				CreatedAt:      now,
				DeliveryStatus: "pending",
			}
			if role.Status == "running" && role.RunID != "" {
				deliveries = append(deliveries, delivery{
					messageID: msg.ID,
					runID:     role.RunID,
					text:      FormatTeamInjectedMessage(msg),
				})
				msg.DeliveryStatus = "queued"
			}
			state.Messages = append(state.Messages, msg)
			createdMessages = append(createdMessages, msg)
		}
		if err := SaveTeamRunState(state); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	delivered := map[string]bool{}
	for _, item := range deliveries {
		if err := InjectMessage(db, item.runID, item.text); err == nil {
			delivered[item.messageID] = true
		}
	}
	if len(delivered) > 0 {
		_ = UpdateTeamRunState(teamRunID, func(state *TeamRunState) error {
			now := time.Now().Unix()
			for i := range state.Messages {
				if delivered[state.Messages[i].ID] {
					state.Messages[i].DeliveryStatus = "delivered"
					state.Messages[i].DeliveredAt = now
				}
			}
			return nil
		})
		for i := range createdMessages {
			if delivered[createdMessages[i].ID] {
				createdMessages[i].DeliveryStatus = "delivered"
				createdMessages[i].DeliveredAt = time.Now().Unix()
			}
		}
	}

	if coordinatorID != "" {
		_ = appendTeamMessageLog(coordinatorID, createdMessages)
	}
	return createdMessages, nil
}

func ConsumePendingTeamMessages(teamRunID, roleName string) ([]TeamMessage, error) {
	var pending []TeamMessage
	err := UpdateTeamRunState(teamRunID, func(state *TeamRunState) error {
		now := time.Now().Unix()
		for i := range state.Messages {
			msg := &state.Messages[i]
			if msg.ToRole != roleName || msg.DeliveredAt != 0 {
				continue
			}
			msg.DeliveryStatus = "delivered"
			msg.DeliveredAt = now
			pending = append(pending, *msg)
		}
		return nil
	})
	return pending, err
}

func ListTeamMessages(teamRunID string, limit int) ([]TeamMessage, error) {
	state, err := LoadTeamRunState(teamRunID)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || len(state.Messages) <= limit {
		return append([]TeamMessage{}, state.Messages...), nil
	}
	return append([]TeamMessage{}, state.Messages[len(state.Messages)-limit:]...), nil
}

func FormatTeamInjectedMessage(msg TeamMessage) string {
	from := msg.FromRole
	if strings.TrimSpace(from) == "" {
		from = "system"
	}
	return fmt.Sprintf("<team_message from=%q to=%q>\n%s\n</team_message>", from, msg.ToRole, msg.Content)
}

func teamRoleByName(state *TeamRunState, name string) *TeamRoleState {
	for i := range state.Roles {
		if state.Roles[i].Name == name {
			return &state.Roles[i]
		}
	}
	return nil
}

func resolveTeamMessageTargets(state *TeamRunState, fromRole, targetRole string) ([]TeamRoleState, error) {
	if targetRole == "" {
		return nil, fmt.Errorf("target role is required")
	}
	sender := (*TeamRoleState)(nil)
	if fromRole != "" {
		sender = teamRoleByName(state, fromRole)
		if sender == nil {
			return nil, fmt.Errorf("unknown sender role %q", fromRole)
		}
	}
	if targetRole == "*" || targetRole == "all" {
		result := make([]TeamRoleState, 0, len(state.Roles))
		for _, role := range state.Roles {
			if role.Name == fromRole {
				continue
			}
			if !isEligibleTeamMessageTarget(sender, &role) {
				continue
			}
			result = append(result, role)
		}
		if len(result) == 0 {
			return nil, fmt.Errorf("no eligible target roles available in the current running stage")
		}
		return result, nil
	}
	role := teamRoleByName(state, targetRole)
	if role == nil {
		return nil, fmt.Errorf("unknown target role %q", targetRole)
	}
	if role.Name == fromRole {
		return nil, fmt.Errorf("cannot send a message to yourself")
	}
	if !isEligibleTeamMessageTarget(sender, role) {
		if sender != nil {
			return nil, fmt.Errorf("role %q can only message roles in the same running stage; %q is stage %d with status %s", sender.Name, role.Name, role.Stage, role.Status)
		}
		return nil, fmt.Errorf("role %q is not eligible to receive team messages right now", role.Name)
	}
	return []TeamRoleState{*role}, nil
}

func isEligibleTeamMessageTarget(sender, target *TeamRoleState) bool {
	if target == nil {
		return false
	}
	if sender == nil {
		return target.Status == "running"
	}
	return target.Status == "running" && target.Stage == sender.Stage
}

func appendTeamMessageLog(coordinatorTopicID string, messages []TeamMessage) error {
	if len(messages) == 0 {
		return nil
	}
	path := filepath.Join(TopicDir(coordinatorTopicID), "team-messages.md")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, msg := range messages {
		from := msg.FromRole
		if strings.TrimSpace(from) == "" {
			from = "system"
		}
		line := fmt.Sprintf("- [%s] `%s -> %s`: %s\n", time.Unix(msg.CreatedAt, 0).Format("15:04:05"), from, msg.ToRole, msg.Content)
		if _, err := f.WriteString(line); err != nil {
			return err
		}
	}
	return nil
}
