package github

import (
	"crypto/sha256"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type RepoCache struct {
	dir       string
	maxAge    time.Duration
	githubPAT string
	mu        sync.Mutex
	lastPull  map[string]time.Time
	logger    *slog.Logger
}

func NewRepoCache(dir string, maxAge time.Duration, githubPAT string, logger *slog.Logger) *RepoCache {
	return &RepoCache{
		dir:       dir,
		maxAge:    maxAge,
		githubPAT: githubPAT,
		lastPull:  make(map[string]time.Time),
		logger:    logger,
	}
}

// resolveURLWithToken builds a clone URL. Uses perCallToken if non-empty,
// otherwise falls back to rc.githubPAT. For bare slugs (owner/repo), builds a
// github.com HTTPS URL and injects the token. For full github.com HTTPS URLs
// without userinfo, injects the token in place. URLs that already carry
// credentials, non-github hosts, git@ SSH, and file:// all pass through.
func (rc *RepoCache) resolveURLWithToken(repoRef, perCallToken string) string {
	token := perCallToken
	if token == "" {
		token = rc.githubPAT
	}
	if strings.HasPrefix(repoRef, "git@") || strings.HasPrefix(repoRef, "file://") {
		return repoRef
	}
	const githubPrefix = "https://github.com/"
	if strings.HasPrefix(repoRef, "http") {
		if token != "" && strings.HasPrefix(repoRef, githubPrefix) {
			return "https://" + token + "@github.com/" + strings.TrimPrefix(repoRef, githubPrefix)
		}
		return repoRef
	}
	if token != "" {
		return fmt.Sprintf("https://%s@github.com/%s.git", token, repoRef)
	}
	return fmt.Sprintf("https://github.com/%s.git", repoRef)
}

func (rc *RepoCache) getRemoteURL(repoPath string) string {
	out, err := exec.Command("git", "-C", repoPath, "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (rc *RepoCache) EnsureRepo(repoRef string, token string) (string, error) {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	start := time.Now()
	cloneURL := rc.resolveURLWithToken(repoRef, token)
	localPath := filepath.Join(rc.dir, rc.dirName(repoRef))

	if _, err := os.Stat(filepath.Join(localPath, "HEAD")); os.IsNotExist(err) {
		rc.logger.Info("開始 clone repo", "phase", "處理中", "repo", SanitizeURL(repoRef), "path", localPath)
		if err := os.MkdirAll(filepath.Dir(localPath), 0755); err != nil {
			return "", fmt.Errorf("mkdir: %w", err)
		}
		// Bare clone so multiple worktrees can share the same cache safely
		cmd := exec.Command("git", "clone", "--bare", cloneURL, localPath)
		if _, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("git clone failed: %w", err)
		}
		rc.lastPull[repoRef] = time.Now()
		rc.logger.Info("Repo 同步完成", "phase", "完成", "repo", SanitizeURL(repoRef), "duration_ms", time.Since(start).Milliseconds())
		return localPath, nil
	}

	if last, ok := rc.lastPull[repoRef]; ok && rc.maxAge > 0 && time.Since(last) < rc.maxAge {
		return localPath, nil
	}

	// Update remote URL if token changed
	currentURL := rc.getRemoteURL(localPath)
	if cloneURL != currentURL && cloneURL != "" {
		setCmd := exec.Command("git", "-C", localPath, "remote", "set-url", "origin", cloneURL)
		setCmd.Run() // best-effort
	}

	rc.logger.Info("開始 fetch repo", "phase", "處理中", "repo", SanitizeURL(repoRef))
	cmd := exec.Command("git", "-C", localPath, "fetch", "--all", "--prune")
	if out, err := cmd.CombinedOutput(); err != nil {
		rc.logger.Warn("Git fetch 失敗", "phase", "失敗", "error", err)
		// Broken repo (e.g. interrupted clone) — remove and re-clone
		if strings.Contains(string(out), "not a git repository") {
			rc.logger.Info("移除損壞目錄並重新 clone", "phase", "處理中", "repo", SanitizeURL(repoRef))
			os.RemoveAll(localPath)
			if err := os.MkdirAll(filepath.Dir(localPath), 0755); err != nil {
				return "", fmt.Errorf("mkdir: %w", err)
			}
			cmd = exec.Command("git", "clone", "--bare", cloneURL, localPath)
			if _, err := cmd.CombinedOutput(); err != nil {
				return "", fmt.Errorf("git clone (retry) failed: %w", err)
			}
			rc.lastPull[repoRef] = time.Now()
			rc.logger.Info("Repo 同步完成", "phase", "完成", "repo", SanitizeURL(repoRef), "duration_ms", time.Since(start).Milliseconds())
			return localPath, nil
		}
	}
	rc.lastPull[repoRef] = time.Now()
	rc.logger.Info("Repo 同步完成", "phase", "完成", "repo", SanitizeURL(repoRef), "duration_ms", time.Since(start).Milliseconds())
	return localPath, nil
}

// ListBranches returns branch names for a cached repo.
// Works with both bare repos (refs/heads/) and regular clones (refs/remotes/origin/).
func (rc *RepoCache) ListBranches(repoPath string) ([]string, error) {
	// Use for-each-ref which works consistently on both bare and non-bare repos.
	cmd := exec.Command("git", "-C", repoPath, "for-each-ref", "--format=%(refname:short)", "refs/heads/", "refs/remotes/origin/")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("list branches: %w", err)
	}

	// Detect default branch from HEAD.
	var defaultBranch string
	if headOut, err := exec.Command("git", "-C", repoPath, "symbolic-ref", "--short", "HEAD").Output(); err == nil {
		defaultBranch = strings.TrimSpace(string(headOut))
	}

	seen := make(map[string]bool)
	var rest []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.Contains(line, "HEAD") {
			continue
		}
		name := strings.TrimPrefix(line, "origin/")
		if !seen[name] {
			seen[name] = true
			if name != defaultBranch {
				rest = append(rest, name)
			}
		}
	}
	if defaultBranch != "" {
		return append([]string{defaultBranch}, rest...), nil
	}
	return rest, nil
}

// Checkout switches the repo to the specified branch.
func (rc *RepoCache) Checkout(repoPath, branch string) error {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	// Try local branch first, then track remote
	cmd := exec.Command("git", "-C", repoPath, "checkout", branch)
	if out, err := cmd.CombinedOutput(); err != nil {
		// Try creating a tracking branch
		cmd = exec.Command("git", "-C", repoPath, "checkout", "-b", branch, "origin/"+branch)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("checkout %s: %w\n%s", branch, err, out)
		}
		_ = out
	}

	// Pull latest for this branch
	cmd = exec.Command("git", "-C", repoPath, "pull", "--ff-only")
	cmd.CombinedOutput() // best-effort
	return nil
}

// AddWorktree creates an isolated working directory from a bare cache.
// If branch is empty, checks out the default branch (HEAD).
func (rc *RepoCache) AddWorktree(barePath, branch, worktreePath string) error {
	var cmd *exec.Cmd
	if branch == "" {
		cmd = exec.Command("git", "-C", barePath, "worktree", "add", worktreePath, "HEAD")
	} else {
		// Bare repo branches are in refs/heads/, not refs/remotes/origin/.
		cmd = exec.Command("git", "-C", barePath, "worktree", "add", worktreePath, branch)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git worktree add: %w\n%s", err, out)
	}
	return nil
}

// WorktreeDir returns the base directory for worktrees, adjacent to the bare
// repo cache. Callers should create subdirectories under this path.
func (rc *RepoCache) WorktreeDir() string {
	return filepath.Join(rc.dir, "worktrees")
}

// RemoveWorktree removes a worktree directory.
func (rc *RepoCache) RemoveWorktree(worktreePath string) error {
	cmd := exec.Command("git", "worktree", "remove", "--force", worktreePath)
	if err := cmd.Run(); err != nil {
		return os.RemoveAll(worktreePath)
	}
	return nil
}

// CleanAll removes the entire cache directory (bare repos + any leftover worktrees).
func (rc *RepoCache) CleanAll() error {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.lastPull = make(map[string]time.Time)
	return os.RemoveAll(rc.dir)
}

// PurgeStale wipes and recreates the cache directory.
func (rc *RepoCache) PurgeStale() error {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.lastPull = make(map[string]time.Time)
	os.RemoveAll(rc.dir)
	return os.MkdirAll(rc.dir, 0755)
}

func (rc *RepoCache) ResolveURL(repoRef string) string {
	if strings.HasPrefix(repoRef, "http") || strings.HasPrefix(repoRef, "git@") || strings.HasPrefix(repoRef, "file://") {
		return repoRef
	}
	if rc.githubPAT != "" {
		return fmt.Sprintf("https://%s@github.com/%s.git", rc.githubPAT, repoRef)
	}
	return fmt.Sprintf("https://github.com/%s.git", repoRef)
}

func (rc *RepoCache) dirName(repoRef string) string {
	h := sha256.Sum256([]byte(repoRef))
	return fmt.Sprintf("%x", h[:8])
}

// SanitizeURL strips embedded credentials from a URL for safe logging.
func SanitizeURL(raw string) string {
	// Matches https://TOKEN@github.com/... or https://user:pass@host/...
	if idx := strings.Index(raw, "@"); idx > 0 {
		if schemeEnd := strings.Index(raw, "://"); schemeEnd > 0 && schemeEnd < idx {
			return raw[:schemeEnd+3] + "***@" + raw[idx+1:]
		}
	}
	return raw
}
