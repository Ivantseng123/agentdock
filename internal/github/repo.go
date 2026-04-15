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
}

func NewRepoCache(dir string, maxAge time.Duration, githubPAT string) *RepoCache {
	return &RepoCache{
		dir:       dir,
		maxAge:    maxAge,
		githubPAT: githubPAT,
		lastPull:  make(map[string]time.Time),
	}
}

func (rc *RepoCache) EnsureRepo(repoRef string) (string, error) {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	cloneURL := rc.ResolveURL(repoRef)
	localPath := filepath.Join(rc.dir, rc.dirName(repoRef))

	if _, err := os.Stat(filepath.Join(localPath, ".git")); os.IsNotExist(err) {
		slog.Info("cloning repo", "repo", SanitizeURL(repoRef), "path", localPath)
		if err := os.MkdirAll(filepath.Dir(localPath), 0755); err != nil {
			return "", fmt.Errorf("mkdir: %w", err)
		}
		// Full clone (not shallow) so we can switch branches
		cmd := exec.Command("git", "clone", cloneURL, localPath)
		if _, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("git clone failed: %w", err)
		}
		rc.lastPull[repoRef] = time.Now()
		return localPath, nil
	}

	if last, ok := rc.lastPull[repoRef]; ok && rc.maxAge > 0 && time.Since(last) < rc.maxAge {
		return localPath, nil
	}

	slog.Info("fetching repo", "repo", SanitizeURL(repoRef))
	cmd := exec.Command("git", "-C", localPath, "fetch", "--all", "--prune")
	if out, err := cmd.CombinedOutput(); err != nil {
		slog.Warn("git fetch failed", "error", err)
		// Broken repo (e.g. interrupted clone) — remove and re-clone
		if strings.Contains(string(out), "not a git repository") {
			slog.Info("removing broken repo dir and re-cloning", "repo", SanitizeURL(repoRef))
			os.RemoveAll(localPath)
			if err := os.MkdirAll(filepath.Dir(localPath), 0755); err != nil {
				return "", fmt.Errorf("mkdir: %w", err)
			}
			cmd = exec.Command("git", "clone", cloneURL, localPath)
			if _, err := cmd.CombinedOutput(); err != nil {
				return "", fmt.Errorf("git clone (retry) failed: %w", err)
			}
			rc.lastPull[repoRef] = time.Now()
			return localPath, nil
		}
	}
	// Fast-forward current branch to match remote
	cmd = exec.Command("git", "-C", localPath, "pull", "--ff-only")
	if out, err := cmd.CombinedOutput(); err != nil {
		slog.Debug("git pull ff failed (may be on detached head)", "output", string(out))
	}
	rc.lastPull[repoRef] = time.Now()
	return localPath, nil
}

// ListBranches returns remote branch names for a cached repo.
func (rc *RepoCache) ListBranches(repoPath string) ([]string, error) {
	cmd := exec.Command("git", "-C", repoPath, "branch", "-r", "--format=%(refname:short)")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("list branches: %w", err)
	}

	var branches []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.Contains(line, "HEAD") {
			continue
		}
		// Remove "origin/" prefix
		name := strings.TrimPrefix(line, "origin/")
		branches = append(branches, name)
	}
	return branches, nil
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
