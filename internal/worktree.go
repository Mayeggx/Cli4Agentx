package internal

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Worktree struct {
	Path   string `json:"path"`
	Branch string `json:"branch,omitempty"`
	Head   string `json:"head,omitempty"`
	Bare   bool   `json:"bare,omitempty"`
}

type WorktreeManager struct {
	config RuntimeConfig
}

func NewWorktreeManager(config RuntimeConfig) *WorktreeManager {
	return &WorktreeManager{config: config.Normalized()}
}

func (m *WorktreeManager) Create(ctx context.Context, repo, name string) (*Worktree, error) {
	name, err := normalizeWorktreeName(name)
	if err != nil {
		return nil, err
	}
	repoRoot, err := resolveGitRepo(ctx, repo)
	if err != nil {
		return nil, err
	}
	base := m.config.Worktree.BaseBranch
	if err := ensureLocalBranch(ctx, repoRoot, base); err != nil {
		return nil, err
	}
	root, err := m.rootForRepo(repoRoot)
	if err != nil {
		return nil, err
	}
	if err := ensureWorktreeRoot(root); err != nil {
		return nil, err
	}
	path, err := secureChildPath(root, name)
	if err != nil {
		return nil, fmt.Errorf("resolve worktree path: %w", err)
	}
	branch := "agentx/" + name

	worktrees, err := m.List(ctx, repoRoot)
	if err != nil {
		return nil, err
	}
	for _, worktree := range worktrees {
		if filepath.Clean(worktree.Path) != filepath.Clean(path) {
			continue
		}
		if worktree.Branch != branch {
			return nil, fmt.Errorf("worktree path already belongs to branch %q", worktree.Branch)
		}
		return &worktree, nil
	}
	if _, err := os.Stat(path); err == nil {
		return nil, fmt.Errorf("worktree path already exists but is not registered by git: %s", path)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("check worktree path: %w", err)
	}
	if branchExists(ctx, repoRoot, branch) {
		return nil, fmt.Errorf("branch %q already exists; use a different worktree name or remove its existing worktree", branch)
	}
	if _, err := runGit(ctx, repoRoot, "worktree", "add", "-b", branch, path, base); err != nil {
		return nil, fmt.Errorf("create worktree: %w", err)
	}
	return &Worktree{Path: path, Branch: branch}, nil
}

func (m *WorktreeManager) List(ctx context.Context, repo string) ([]Worktree, error) {
	repoRoot, err := resolveGitRepo(ctx, repo)
	if err != nil {
		return nil, err
	}
	output, err := runGit(ctx, repoRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("list worktrees: %w", err)
	}
	worktrees := parseWorktreeList(output)
	sort.Slice(worktrees, func(i, j int) bool { return worktrees[i].Path < worktrees[j].Path })
	return worktrees, nil
}

func (m *WorktreeManager) Remove(ctx context.Context, repo, name string, force bool) error {
	name, err := normalizeWorktreeName(name)
	if err != nil {
		return err
	}
	repoRoot, err := resolveGitRepo(ctx, repo)
	if err != nil {
		return err
	}
	root, err := m.rootForRepo(repoRoot)
	if err != nil {
		return err
	}
	path, err := secureChildPath(root, name)
	if err != nil {
		return fmt.Errorf("resolve worktree path: %w", err)
	}
	branch := "agentx/" + name
	worktrees, err := m.List(ctx, repoRoot)
	if err != nil {
		return err
	}
	found := false
	for _, worktree := range worktrees {
		if filepath.Clean(worktree.Path) == filepath.Clean(path) && !worktree.Bare && worktree.Branch == branch {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("managed worktree %q not found or not owned by branch %q", name, branch)
	}
	if !force {
		status, err := runGit(ctx, path, "status", "--porcelain")
		if err != nil {
			return fmt.Errorf("inspect worktree status: %w", err)
		}
		if strings.TrimSpace(status) != "" {
			return fmt.Errorf("worktree %q has uncommitted changes; keep it or retry with force after reviewing the changes", name)
		}
	}
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, path)
	if _, err := runGit(ctx, repoRoot, args...); err != nil {
		return fmt.Errorf("remove worktree: %w", err)
	}
	return nil
}

func ensureWorktreeRoot(root string) error {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("create worktree root: %w", err)
	}
	ignorePath := filepath.Join(root, ".gitignore")
	if _, err := os.Lstat(ignorePath); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect worktree ignore file: %w", err)
	}
	if err := os.WriteFile(ignorePath, []byte("*\n!.gitignore\n"), 0o600); err != nil {
		return fmt.Errorf("write worktree ignore file: %w", err)
	}
	return nil
}

func (m *WorktreeManager) rootForRepo(repo string) (string, error) {
	repo, err := canonicalDirectory(repo)
	if err != nil {
		return "", fmt.Errorf("resolve repository root: %w", err)
	}
	configured := filepath.Clean(m.config.Worktree.RootDir)
	if filepath.IsAbs(configured) || configured == "." || configured == ".." || strings.HasPrefix(configured, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("worktree root_dir must be a relative path inside the repository")
	}
	root := filepath.Join(repo, configured)
	root = filepath.Clean(root)
	if !isWithin(root, repo) {
		return "", fmt.Errorf("worktree root escapes repository")
	}
	if err := rejectSymlinkComponents(repo, root); err != nil {
		return "", err
	}
	return root, nil
}

// ValidateManagedWorktree confirms that cwd is one of the tool-created
// worktrees, not simply an arbitrary directory inside a repository.
func ValidateManagedWorktree(config RuntimeConfig, cwd string) error {
	workingDir, err := canonicalDirectory(cwd)
	if err != nil {
		return fmt.Errorf("resolve working directory: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	repo, err := resolveGitRepo(ctx, workingDir)
	if err != nil {
		return err
	}
	manager := NewWorktreeManager(config)
	root, err := manager.rootForRepo(repo)
	if err != nil {
		return err
	}
	root, err = canonicalDirectory(root)
	if err != nil {
		return fmt.Errorf("managed worktree root does not exist: %w", err)
	}
	name := filepath.Base(workingDir)
	if filepath.Dir(workingDir) != root {
		return fmt.Errorf("working directory is outside the managed worktree root")
	}
	if _, err := normalizeWorktreeName(name); err != nil {
		return err
	}
	branch := "agentx/" + name
	worktrees, err := manager.List(ctx, repo)
	if err != nil {
		return err
	}
	for _, worktree := range worktrees {
		path, pathErr := canonicalDirectory(worktree.Path)
		if pathErr == nil && path == workingDir && !worktree.Bare && worktree.Branch == branch {
			return nil
		}
	}
	return fmt.Errorf("working directory is not managed as branch %q", branch)
}

func resolveGitRepo(ctx context.Context, repo string) (string, error) {
	if strings.TrimSpace(repo) == "" {
		var err error
		repo, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("get working directory: %w", err)
		}
	}
	abs, err := canonicalDirectory(repo)
	if err != nil {
		return "", fmt.Errorf("resolve repository path: %w", err)
	}
	commonDir, err := runGit(ctx, abs, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", fmt.Errorf("%s is not a Git worktree: %w", abs, err)
	}
	commonDir = strings.TrimSpace(commonDir)
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(abs, commonDir)
	}
	commonDir, err = canonicalDirectory(commonDir)
	if err != nil {
		return "", fmt.Errorf("resolve Git common directory: %w", err)
	}
	if filepath.Base(commonDir) != ".git" {
		return "", fmt.Errorf("unsupported Git common directory: %s", commonDir)
	}
	return canonicalDirectory(filepath.Dir(commonDir))
}

func ensureLocalBranch(ctx context.Context, repo, branch string) error {
	if _, err := runGit(ctx, repo, "show-ref", "--verify", "--quiet", "refs/heads/"+branch); err != nil {
		return fmt.Errorf("base branch %q is not available locally", branch)
	}
	return nil
}

func branchExists(ctx context.Context, repo, branch string) bool {
	_, err := runGit(ctx, repo, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(safeExecutionEnv(), "GIT_TERMINAL_PROMPT=0")
	var stdout, stderr limitedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("git command timed out")
		}
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), message)
	}
	return stdout.String(), nil
}

func parseWorktreeList(output string) []Worktree {
	blocks := strings.Split(strings.TrimSpace(output), "\n\n")
	worktrees := make([]Worktree, 0, len(blocks))
	for _, block := range blocks {
		var worktree Worktree
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "worktree "):
				worktree.Path = strings.TrimPrefix(line, "worktree ")
			case strings.HasPrefix(line, "HEAD "):
				worktree.Head = strings.TrimPrefix(line, "HEAD ")
			case strings.HasPrefix(line, "branch refs/heads/"):
				worktree.Branch = strings.TrimPrefix(line, "branch refs/heads/")
			case line == "bare":
				worktree.Bare = true
			}
		}
		if worktree.Path != "" {
			worktrees = append(worktrees, worktree)
		}
	}
	return worktrees
}

func normalizeWorktreeName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 80 {
		return "", fmt.Errorf("worktree name must be 1 to 80 characters")
	}
	if name != filepath.Base(name) || name == "." || name == ".." {
		return "", fmt.Errorf("worktree name must not contain path separators")
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '-' && r != '_' {
			return "", fmt.Errorf("worktree name may contain only letters, numbers, hyphens, and underscores")
		}
	}
	return name, nil
}

func canonicalDirectory(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", resolved)
	}
	return filepath.Clean(resolved), nil
}

func rejectSymlinkComponents(root, target string) error {
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("worktree root escapes repository")
	}
	current := root
	for _, part := range strings.Split(rel, string(os.PathSeparator)) {
		if part == "." || part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect worktree root: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("worktree root must not traverse symbolic links")
		}
	}
	return nil
}

func isWithin(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}
