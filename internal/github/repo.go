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

	cloneURL := rc.resolveURL(repoRef)
	localPath := filepath.Join(rc.dir, rc.dirName(repoRef))

	if _, err := os.Stat(filepath.Join(localPath, ".git")); os.IsNotExist(err) {
		slog.Info("cloning repo", "repo", repoRef, "path", localPath)
		if err := os.MkdirAll(filepath.Dir(localPath), 0755); err != nil {
			return "", fmt.Errorf("mkdir: %w", err)
		}
		cmd := exec.Command("git", "clone", "--depth", "1", cloneURL, localPath)
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("git clone: %w\n%s", err, out)
		}
		rc.lastPull[repoRef] = time.Now()
		return localPath, nil
	}

	if last, ok := rc.lastPull[repoRef]; ok && rc.maxAge > 0 && time.Since(last) < rc.maxAge {
		return localPath, nil
	}

	slog.Info("pulling repo", "repo", repoRef)
	cmd := exec.Command("git", "-C", localPath, "pull", "--ff-only")
	if out, err := cmd.CombinedOutput(); err != nil {
		slog.Warn("git pull failed, continuing with cached version", "error", err, "output", string(out))
	}
	rc.lastPull[repoRef] = time.Now()
	return localPath, nil
}

func (rc *RepoCache) resolveURL(repoRef string) string {
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
