package internal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

type CommandApprovalMode string

const (
	ApprovalAlways    CommandApprovalMode = "always"
	ApprovalDangerous CommandApprovalMode = "dangerous"
	ApprovalNever     CommandApprovalMode = "never"
)

type CommandApprovalConfig struct {
	Mode  CommandApprovalMode `yaml:"mode" json:"mode"`
	Audit bool                `yaml:"audit" json:"audit"`
}

type SandboxConfig struct {
	// Mode is "required" by default. Set it explicitly to "disabled" only for
	// trusted local development where an OS sandbox is unavailable.
	Mode           string `yaml:"mode" json:"mode"`
	AllowNetwork   bool   `yaml:"allow_network" json:"allow_network"`
	TimeoutSeconds int    `yaml:"timeout_seconds" json:"timeout_seconds"`
}

type WorktreeConfig struct {
	BaseBranch string `yaml:"base_branch" json:"base_branch"`
	RootDir    string `yaml:"root_dir" json:"root_dir"`
}

type RuntimeConfig struct {
	Sandbox         SandboxConfig         `yaml:"sandbox" json:"sandbox"`
	CommandApproval CommandApprovalConfig `yaml:"command_approval" json:"command_approval"`
	Worktree        WorktreeConfig        `yaml:"worktree" json:"worktree"`
}

func DefaultRuntimeConfig() RuntimeConfig {
	return RuntimeConfig{
		Sandbox: SandboxConfig{Mode: "required", TimeoutSeconds: 120},
		CommandApproval: CommandApprovalConfig{Mode: ApprovalAlways, Audit: true},
		Worktree: WorktreeConfig{BaseBranch: "main", RootDir: ".agentx/worktrees"},
	}
}

func (c RuntimeConfig) Normalized() RuntimeConfig {
	defaults := DefaultRuntimeConfig()
	if c.Sandbox.Mode == "" {
		c.Sandbox.Mode = defaults.Sandbox.Mode
	}
	if c.Sandbox.TimeoutSeconds <= 0 {
		c.Sandbox.TimeoutSeconds = defaults.Sandbox.TimeoutSeconds
	}
	if c.CommandApproval.Mode == "" {
		c.CommandApproval.Mode = defaults.CommandApproval.Mode
	}
	if !c.CommandApproval.Audit {
		// Keep the zero-value configuration secure and auditable. Explicitly
		// disabling audit is intentionally unsupported for agent commands.
		c.CommandApproval.Audit = true
	}
	if c.Worktree.BaseBranch == "" {
		c.Worktree.BaseBranch = defaults.Worktree.BaseBranch
	}
	if c.Worktree.RootDir == "" {
		c.Worktree.RootDir = defaults.Worktree.RootDir
	}
	return c
}

type ApprovalRequest struct {
	Program    string    `json:"program"`
	Args       []string  `json:"args"`
	WorkingDir string    `json:"working_dir"`
	Reason     string    `json:"reason"`
	RequestedAt time.Time `json:"requested_at"`
}

type ApprovalFunc func(context.Context, ApprovalRequest) (bool, error)

type CommandResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
	Duration string `json:"duration"`
}

type CommandExecutor struct {
	config   RuntimeConfig
	cwd      string
	approver ApprovalFunc
	audit    *approvalAudit
}

func NewCommandExecutor(config RuntimeConfig, cwd string, approver ApprovalFunc) (*CommandExecutor, error) {
	if strings.TrimSpace(cwd) == "" {
		return nil, fmt.Errorf("command working directory is required")
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return nil, fmt.Errorf("resolve command working directory: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("stat command working directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("command working directory is not a directory: %s", abs)
	}
	config = config.Normalized()
	return &CommandExecutor{
		config:   config,
		cwd:      filepath.Clean(abs),
		approver: approver,
		audit:    newApprovalAudit(filepath.Join(dataRoot(), "audit", "command-approvals.jsonl")),
	}, nil
}

// Execute starts one directly-addressed executable. It deliberately does not
// invoke a shell, so command substitution, redirections, and shell injection
// are unavailable to the model.
func (e *CommandExecutor) Execute(ctx context.Context, program string, args []string, stdin string) (CommandResult, error) {
	if e == nil {
		return CommandResult{}, fmt.Errorf("command executor is not configured")
	}
	program = strings.TrimSpace(program)
	if program == "" {
		return CommandResult{}, fmt.Errorf("program is required")
	}
	if err := validateExecutable(program, args); err != nil {
		return CommandResult{}, err
	}
	resolved, err := exec.LookPath(program)
	if err != nil {
		return CommandResult{}, fmt.Errorf("resolve executable %q: %w", program, err)
	}
	request := ApprovalRequest{
		Program:     resolved,
		Args:        append([]string(nil), args...),
		WorkingDir:  e.cwd,
		Reason:      "agent requested an external command",
		RequestedAt: time.Now().UTC(),
	}
	if approvalRequired(e.config.CommandApproval.Mode, resolved, args) {
		if e.approver == nil {
			e.record(request, false, "no approver configured")
			return CommandResult{}, fmt.Errorf("command requires user approval: %s", displayCommand(program, args))
		}
		approved, approvalErr := e.approver(ctx, request)
		if approvalErr != nil {
			e.record(request, false, approvalErr.Error())
			return CommandResult{}, fmt.Errorf("command approval failed: %w", approvalErr)
		}
		e.record(request, approved, "")
		if !approved {
			return CommandResult{}, fmt.Errorf("command rejected by user: %s", displayCommand(program, args))
		}
	} else {
		e.record(request, true, "policy did not require confirmation")
	}

	timeout := time.Duration(e.config.Sandbox.TimeoutSeconds) * time.Second
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	cmd, err := e.sandboxedCommand(ctx, resolved, args)
	if err != nil {
		return CommandResult{}, err
	}
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Env = safeExecutionEnv()
	var stdout, stderr limitedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	result := CommandResult{Stdout: stdout.String(), Stderr: stderr.String(), Duration: time.Since(start).Round(time.Millisecond).String()}
	if ctx.Err() == context.DeadlineExceeded {
		return result, fmt.Errorf("command timed out after %s", timeout)
	}
	if runErr == nil {
		return result, nil
	}
	if exitErr, ok := runErr.(*exec.ExitError); ok {
		result.ExitCode = exitErr.ExitCode()
		return result, nil
	}
	return result, fmt.Errorf("run command: %w", runErr)
}

func (e *CommandExecutor) sandboxedCommand(ctx context.Context, program string, args []string) (*exec.Cmd, error) {
	switch strings.ToLower(e.config.Sandbox.Mode) {
	case "disabled":
		cmd := exec.CommandContext(ctx, program, args...)
		cmd.Dir = e.cwd
		return cmd, nil
	case "required":
		return newSandboxCommand(ctx, e.cwd, program, args, e.config.Sandbox.AllowNetwork)
	default:
		return nil, fmt.Errorf("unsupported sandbox mode %q (use required or disabled)", e.config.Sandbox.Mode)
	}
}

func validateExecutable(program string, args []string) error {
	base := strings.ToLower(filepath.Base(program))
	switch base {
	case "sh", "bash", "zsh", "fish", "dash", "cmd", "cmd.exe", "powershell", "pwsh":
		return fmt.Errorf("shell interpreters are not allowed; invoke a single executable with explicit arguments")
	}
	if strings.ContainsAny(program, "\x00\r\n") {
		return fmt.Errorf("invalid executable name")
	}
	for _, arg := range args {
		for _, r := range arg {
			if r < 0x20 || r == 0x7f {
				return fmt.Errorf("command arguments must not contain control characters")
			}
		}
	}
	return nil
}

func approvalRequired(mode CommandApprovalMode, program string, args []string) bool {
	switch mode {
	case ApprovalNever:
		return false
	case ApprovalDangerous:
		return looksDangerous(program, args)
	case ApprovalAlways, "":
		return true
	default:
		return true
	}
}

func looksDangerous(program string, args []string) bool {
	base := strings.ToLower(filepath.Base(program))
	switch base {
	case "rm", "dd", "mkfs", "chmod", "chown", "curl", "wget", "ssh", "scp", "rsync":
		return true
	case "git":
		for _, arg := range args {
			switch arg {
			case "push", "reset", "clean", "rebase", "checkout", "restore", "worktree":
				return true
			}
		}
	}
	return false
}

func newSandboxCommand(ctx context.Context, cwd, program string, args []string, allowNetwork bool) (*exec.Cmd, error) {
	gitDataDir := ""
	if strings.EqualFold(filepath.Base(program), "git") {
		var err error
		gitDataDir, err = sandboxGitDataDir(ctx, cwd)
		if err != nil {
			return nil, err
		}
	}
	switch runtime.GOOS {
	case "darwin":
		binary, err := exec.LookPath("sandbox-exec")
		if err != nil {
			return nil, fmt.Errorf("OS sandbox is required but sandbox-exec is unavailable; install or explicitly set runtime.sandbox.mode: disabled for trusted development")
		}
		profile := darwinSandboxProfile(cwd, gitDataDir, allowNetwork)
		allArgs := append([]string{"-p", profile, "--", program}, args...)
		cmd := exec.CommandContext(ctx, binary, allArgs...)
		cmd.Dir = cwd
		return cmd, nil
	case "linux":
		binary, err := exec.LookPath("bwrap")
		if err != nil {
			return nil, fmt.Errorf("OS sandbox is required but bubblewrap (bwrap) is unavailable; install it or explicitly set runtime.sandbox.mode: disabled for trusted development")
		}
		return linuxSandboxCommand(ctx, binary, cwd, program, args, gitDataDir, allowNetwork), nil
	default:
		return nil, fmt.Errorf("OS sandbox mode is not implemented for %s; explicitly set runtime.sandbox.mode: disabled only for trusted development", runtime.GOOS)
	}
}

func sandboxGitDataDir(ctx context.Context, cwd string) (string, error) {
	gitDir, err := runGit(ctx, cwd, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", fmt.Errorf("resolve Git metadata directory for sandbox: %w", err)
	}
	gitDir = strings.TrimSpace(gitDir)
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(cwd, gitDir)
	}
	gitDir, err = canonicalDirectory(gitDir)
	if err != nil {
		return "", fmt.Errorf("resolve Git metadata directory for sandbox: %w", err)
	}
	if filepath.Base(gitDir) != ".git" {
		return "", fmt.Errorf("unsupported Git metadata directory: %s", gitDir)
	}
	return gitDir, nil
}

func darwinSandboxProfile(cwd, gitDataDir string, allowNetwork bool) string {
	quoted := sandboxQuote(cwd)
	tmp := sandboxQuote(os.TempDir())
	var b strings.Builder
	b.WriteString("(version 1)\n")
	b.WriteString("(deny default)\n")
	b.WriteString("(import \"system.sb\")\n")
	b.WriteString("(allow process*)\n")
	for _, path := range []string{"/usr", "/bin", "/sbin", "/System", "/Library", "/opt", "/private/var/db"} {
		b.WriteString(fmt.Sprintf("(allow file-read* (subpath %s))\n", sandboxQuote(path)))
	}
	b.WriteString(fmt.Sprintf("(allow file-read* (subpath %s))\n", quoted))
	b.WriteString(fmt.Sprintf("(allow file-write* (subpath %s))\n", quoted))
	if gitDataDir != "" {
		gitData := sandboxQuote(gitDataDir)
		b.WriteString(fmt.Sprintf("(allow file-read* (subpath %s))\n", gitData))
		b.WriteString(fmt.Sprintf("(allow file-write* (subpath %s))\n", gitData))
	}
	b.WriteString(fmt.Sprintf("(allow file-write* (subpath %s))\n", tmp))
	if allowNetwork {
		b.WriteString("(allow network*)\n")
	}
	return b.String()
}

func linuxSandboxCommand(ctx context.Context, bwrap, cwd, program string, args []string, gitDataDir string, allowNetwork bool) *exec.Cmd {
	// Bind only system runtime locations, the selected worktree, and (for Git)
	// the linked worktree's metadata directory. Parent directories are empty.
	bwrapArgs := []string{"--die-with-parent", "--new-session", "--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp", "--ro-bind", "/usr", "/usr", "--ro-bind", "/bin", "/bin"}
	for _, path := range []string{"/lib", "/lib64", "/etc/ssl/certs", "/opt"} {
		if _, err := os.Stat(path); err == nil {
			bwrapArgs = append(bwrapArgs, "--ro-bind", path, path)
		}
	}
	if !allowNetwork {
		bwrapArgs = append(bwrapArgs, "--unshare-net")
	}
	bwrapArgs = append(bwrapArgs, bubblewrapParentDirs(cwd)...)
	if gitDataDir != "" {
		bwrapArgs = append(bwrapArgs, bubblewrapParentDirs(gitDataDir)...)
		bwrapArgs = append(bwrapArgs, "--bind", gitDataDir, gitDataDir)
	}
	bwrapArgs = append(bwrapArgs, "--bind", cwd, cwd, "--chdir", cwd, "--", program)
	bwrapArgs = append(bwrapArgs, args...)
	return exec.CommandContext(ctx, bwrap, bwrapArgs...)
}

func bubblewrapParentDirs(path string) []string {
	parents := make([]string, 0, 8)
	for dir := filepath.Dir(path); dir != "." && dir != string(os.PathSeparator); dir = filepath.Dir(dir) {
		parents = append(parents, dir)
	}
	for left, right := 0, len(parents)-1; left < right; left, right = left+1, right-1 {
		parents[left], parents[right] = parents[right], parents[left]
	}
	args := make([]string, 0, len(parents)*2)
	for _, dir := range parents {
		if dir == "/tmp" || dir == "/usr" || dir == "/bin" || dir == "/lib" || dir == "/lib64" || dir == "/opt" {
			continue
		}
		args = append(args, "--dir", dir)
	}
	return args
}

func sandboxQuote(value string) string {
	return "\"" + strings.ReplaceAll(strings.ReplaceAll(value, "\\", "\\\\"), "\"", "\\\"") + "\""
}

func safeExecutionEnv() []string {
	allowed := map[string]bool{"PATH": true, "LANG": true, "LC_ALL": true, "LC_CTYPE": true, "TERM": true, "TMPDIR": true, "TZ": true, "USER": true, "LOGNAME": true}
	env := make([]string, 0, 10)
	for _, item := range os.Environ() {
		key, _, ok := strings.Cut(item, "=")
		if allowed[key] || strings.HasPrefix(key, "LC_") {
			env = append(env, item)
		}
	}
	env = append(env, "GIT_TERMINAL_PROMPT=0")
	return env
}

func displayCommand(program string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, strconv.Quote(program))
	for _, arg := range args {
		parts = append(parts, strconv.Quote(arg))
	}
	return strings.Join(parts, " ")
}

type limitedBuffer struct {
	buf bytes.Buffer
}

const maxCommandOutputBytes = 4 << 20

func (b *limitedBuffer) Write(p []byte) (int, error) {
	remaining := maxCommandOutputBytes - b.buf.Len()
	if remaining <= 0 {
		return len(p), nil
	}
	if len(p) > remaining {
		_, _ = b.buf.Write(p[:remaining])
		return len(p), nil
	}
	return b.buf.Write(p)
}

func (b *limitedBuffer) String() string {
	result := b.buf.String()
	if b.buf.Len() >= maxCommandOutputBytes {
		return result + "\n[output truncated at 4 MiB]"
	}
	return result
}

type approvalAudit struct {
	path string
	mu   sync.Mutex
}

type approvalAuditEntry struct {
	At          time.Time `json:"at"`
	Program     string    `json:"program"`
	Args        []string  `json:"args"`
	WorkingDir  string    `json:"working_dir"`
	Approved    bool      `json:"approved"`
	Explanation string    `json:"explanation,omitempty"`
}

func newApprovalAudit(path string) *approvalAudit {
	return &approvalAudit{path: path}
}

func (e *CommandExecutor) record(request ApprovalRequest, approved bool, explanation string) {
	if e == nil || !e.config.CommandApproval.Audit || e.audit == nil {
		return
	}
	e.audit.append(approvalAuditEntry{At: time.Now().UTC(), Program: request.Program, Args: redactArgs(request.Args), WorkingDir: request.WorkingDir, Approved: approved, Explanation: explanation})
}

func (a *approvalAudit) append(entry approvalAuditEntry) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(a.path), 0o700); err != nil {
		return
	}
	file, err := os.OpenFile(a.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	_ = json.NewEncoder(file).Encode(entry)
}

func redactArgs(args []string) []string {
	out := append([]string(nil), args...)
	for i, arg := range out {
		lower := strings.ToLower(arg)
		if strings.Contains(lower, "token") || strings.Contains(lower, "secret") || strings.Contains(lower, "password") || strings.Contains(lower, "api_key") {
			out[i] = "[redacted]"
		}
	}
	return out
}

var _ io.Writer = (*limitedBuffer)(nil)
