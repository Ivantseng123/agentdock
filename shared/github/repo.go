package github

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// tokenFreeGitHubURL returns a clean https://github.com/owner/repo.git URL for
// a bare `owner/repo` slug or a full github.com HTTPS URL. Non-github refs
// (git@, file://, https://other-host/...) pass through unchanged so test
// fixtures and custom remotes keep working. The returned URL never embeds
// credentials; callers that need auth must supply it via gitAuthEnv.
func tokenFreeGitHubURL(repoRef string) string {
	if strings.HasPrefix(repoRef, "git@") || strings.HasPrefix(repoRef, "file://") {
		return repoRef
	}
	// Strip any embedded userinfo from an HTTPS URL so callers who pre-tokenise
	// their refs still get a clean URL written to .git/config post-clone.
	if strings.HasPrefix(repoRef, "http") {
		if at := strings.Index(repoRef, "@"); at > 0 {
			if schemeEnd := strings.Index(repoRef, "://"); schemeEnd > 0 && schemeEnd < at {
				return repoRef[:schemeEnd+3] + repoRef[at+1:]
			}
		}
		return repoRef
	}
	return fmt.Sprintf("https://github.com/%s.git", repoRef)
}

// gitAuthEnv returns env vars that inject an HTTP Authorization header for
// github.com requests without persisting the token to the repo's .git/config.
// Uses GIT_CONFIG_COUNT / GIT_CONFIG_KEY_N / GIT_CONFIG_VALUE_N so the token
// never appears on the command line (which would leak via `ps`). Returns nil
// when no token is available; callers should append to os.Environ().
//
// Scheme is Basic with `x-access-token:<PAT>` base64-encoded — the same shape
// GitHub Actions' actions/checkout uses. Bearer is rejected by GitHub's Smart
// HTTP backend with "invalid credentials" even for valid PATs (GitHub's REST
// API accepts Bearer, the git-over-HTTPS backend does not).
func gitAuthEnv(token string) []string {
	if token == "" {
		return nil
	}
	basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
	return []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http.https://github.com/.extraheader",
		"GIT_CONFIG_VALUE_0=AUTHORIZATION: basic " + basic,
	}
}

// runGitWithAuth runs a git subcommand with the auth env injected. The token
// is supplied per-operation rather than persisted in .git/config, so an agent
// spawned in a worktree that does `git remote -v` or `git config --list` does
// not see the PAT.
func runGitWithAuth(token string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	if extra := gitAuthEnv(token); extra != nil {
		cmd.Env = append(os.Environ(), extra...)
	}
	return cmd.CombinedOutput()
}

type RepoCache struct {
	dir      string
	maxAge   time.Duration
	tokenFn  func() (string, error)
	mu       sync.Mutex
	lastPull map[string]time.Time
	logger   *slog.Logger
}

// NewRepoCache builds a RepoCache that always uses the given PAT. Worker
// processes call this — they receive a per-job token via secrets rather
// than rotating one in place.
func NewRepoCache(dir string, maxAge time.Duration, githubPAT string, logger *slog.Logger) *RepoCache {
	return newRepoCacheImpl(dir, maxAge, func() (string, error) { return githubPAT, nil }, logger)
}

// NewRepoCacheWithTokenFn builds a RepoCache that resolves its auth token
// per call via tokenFn. App-side callers wire githubapp.TokenSource.Get
// here so the cache stays current across installation-token rotations
// without holding onto a stale string.
func NewRepoCacheWithTokenFn(dir string, maxAge time.Duration, tokenFn func() (string, error), logger *slog.Logger) *RepoCache {
	return newRepoCacheImpl(dir, maxAge, tokenFn, logger)
}

func newRepoCacheImpl(dir string, maxAge time.Duration, tokenFn func() (string, error), logger *slog.Logger) *RepoCache {
	// Abs-resolve so relative paths don't leak clones into the worker's cwd.
	if dir != "" {
		if abs, err := filepath.Abs(dir); err == nil {
			dir = abs
		}
	}
	return &RepoCache{
		dir:      dir,
		maxAge:   maxAge,
		tokenFn:  tokenFn,
		lastPull: make(map[string]time.Time),
		logger:   logger,
	}
}

// currentToken resolves tokenFn into a token at call time. tokenFn errors
// log at Error level and return "" so the outer git operation surfaces a
// clean 401 (private repos) instead of a manufactured wrapper error.
//
// Error level — not Warn — because falling through to unauthenticated
// fetches against public mirrors silently succeeds and returns stale
// data, which can mask token-rotation bugs for hours. Operators should
// see this in alerts.
func (rc *RepoCache) currentToken() string {
	if rc.tokenFn == nil {
		return ""
	}
	tok, err := rc.tokenFn()
	if err != nil {
		rc.logger.Error("Token 取得失敗，將以無認證方式 fetch（公開 repo 可能取得過期資料）",
			"phase", "降級", "error", err)
		return ""
	}
	return tok
}

// resolveURLWithToken builds a clone URL. Uses perCallToken if non-empty,
// otherwise falls back to the cache's tokenFn. For bare slugs (owner/repo),
// builds a github.com HTTPS URL and injects the token. For full github.com
// HTTPS URLs without userinfo, injects the token in place. URLs that already
// carry credentials, non-github hosts, git@ SSH, and file:// all pass through.
func (rc *RepoCache) resolveURLWithToken(repoRef, perCallToken string) string {
	token := perCallToken
	if token == "" {
		token = rc.currentToken()
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

// effectiveToken picks the per-call token over the cache's tokenFn-resolved
// token when both exist. Kept tiny and local so call sites stay linear.
func (rc *RepoCache) effectiveToken(perCall string) string {
	if perCall != "" {
		return perCall
	}
	return rc.currentToken()
}

// clonePath bare-clones cleanURL into localPath. Auth flows through gitAuthEnv
// so the PAT never sits in argv (which would leak via `ps` / /proc/PID/cmdline)
// and the URL written into .git/config stays credential-free from the first
// write — no post-clone rewrite required (#179).
func (rc *RepoCache) clonePath(authToken, cleanURL, localPath string) error {
	// Bare clone so multiple worktrees can share the same cache safely.
	if _, err := runGitWithAuth(authToken, "clone", "--bare", cleanURL, localPath); err != nil {
		return fmt.Errorf("git clone failed: %w", err)
	}
	return nil
}

func (rc *RepoCache) EnsureRepo(repoRef string, token string) (string, error) {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	start := time.Now()
	cleanURL := tokenFreeGitHubURL(repoRef)
	authToken := rc.effectiveToken(token)
	localPath := filepath.Join(rc.dir, rc.dirName(repoRef))

	if _, err := os.Stat(filepath.Join(localPath, "HEAD")); os.IsNotExist(err) {
		rc.logger.Info("開始 clone repo", "phase", "處理中", "repo", SanitizeURL(repoRef), "path", localPath)
		if err := os.MkdirAll(filepath.Dir(localPath), 0755); err != nil {
			return "", fmt.Errorf("mkdir: %w", err)
		}
		if err := rc.clonePath(authToken, cleanURL, localPath); err != nil {
			return "", err
		}
		rc.lastPull[repoRef] = time.Now()
		rc.logger.Info("Repo 同步完成", "phase", "完成", "repo", SanitizeURL(repoRef), "duration_ms", time.Since(start).Milliseconds())
		return localPath, nil
	}

	if last, ok := rc.lastPull[repoRef]; ok && rc.maxAge > 0 && time.Since(last) < rc.maxAge {
		return localPath, nil
	}

	// Heal a legacy cache whose remote.origin.url still has a token baked in
	// (pre-#179 deployments). Rewrite to the clean URL unconditionally; auth
	// for fetch is supplied out-of-band via gitAuthEnv, so we never need the
	// URL to carry credentials.
	if cleanURL != "" {
		currentURL := rc.getRemoteURL(localPath)
		if currentURL != cleanURL {
			setCmd := exec.Command("git", "-C", localPath, "remote", "set-url", "origin", cleanURL)
			if out, err := setCmd.CombinedOutput(); err != nil {
				// Non-fatal: fetch below will still run, but against the old
				// URL. Surface the silent failure so token-rotation regressions
				// are observable instead of manifesting as a mysterious 401.
				rc.logger.Warn("更新 remote URL 失敗，將以舊 URL 繼續 fetch",
					"phase", "降級",
					"repo", SanitizeURL(repoRef),
					"error", err,
					"output", strings.TrimSpace(string(out)),
				)
			}
		}
	}

	rc.logger.Info("開始 fetch repo", "phase", "處理中", "repo", SanitizeURL(repoRef))
	out, err := runGitWithAuth(authToken, "-C", localPath, "fetch", "--all", "--prune")
	if err != nil {
		rc.logger.Warn("Git fetch 失敗", "phase", "失敗", "error", err)
		// Broken repo (e.g. interrupted clone) — remove and re-clone
		if strings.Contains(string(out), "not a git repository") {
			rc.logger.Info("移除損壞目錄並重新 clone", "phase", "處理中", "repo", SanitizeURL(repoRef))
			os.RemoveAll(localPath)
			if err := os.MkdirAll(filepath.Dir(localPath), 0755); err != nil {
				return "", fmt.Errorf("mkdir: %w", err)
			}
			if err := rc.clonePath(authToken, cleanURL, localPath); err != nil {
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

// Checkout switches the repo to the specified branch. token is the
// per-call auth that the caller wants used for the post-checkout pull;
// pass "" to fall back to the cache's tokenFn.
func (rc *RepoCache) Checkout(repoPath, branch, token string) error {
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

	// Pull latest for this branch. Token flows through env (#179) so the
	// remote URL stored in this worktree's config stays credential-free.
	_, _ = runGitWithAuth(rc.effectiveToken(token), "-C", repoPath, "pull", "--ff-only") // best-effort
	return nil
}

// AddWorktree creates a detached-HEAD worktree from a bare cache. --detach
// avoids locking the branch so concurrent jobs on the same branch coexist and
// a prior crash's orphan admin record can't block new adds. Prunes first to
// clear any orphan <bare>/worktrees/NAME records left by past crashes.
//
// If the first add fails (typically: ref unknown to the local cache because
// the branch was deleted on origin or the SHA was never fetched — e.g. PR
// merged + branch deleted, or squash-merged PR head SHA), AddWorktree fetches
// the ref directly from origin and retries once. GitHub enables
// uploadpack.allowReachableSHA1InWant, so a direct fetch-by-SHA works even
// when no remote ref still points at the SHA.
func (rc *RepoCache) AddWorktree(barePath, branch, worktreePath, token string) error {
	pruneCmd := exec.Command("git", "-C", barePath, "worktree", "prune")
	_, _ = pruneCmd.CombinedOutput() // best-effort

	ref := "HEAD"
	if branch != "" {
		ref = branch
	}

	addErr := tryAddWorktree(barePath, worktreePath, ref)
	if addErr == nil {
		return nil
	}

	// Auth is supplied per-op via env so the token never sits in .git/config
	// (#179). Caller (worker/pool/adapters.go) plumbs the per-job token from
	// merged secrets; if empty, fall back to the cache's tokenFn so app-side
	// callers in App mode still get a fresh installation token.
	fetchOut, fetchErr := runGitWithAuth(rc.effectiveToken(token), "-C", barePath, "fetch", "origin", ref)
	if fetchErr != nil {
		return fmt.Errorf("git worktree add failed: %w; git fetch origin %s also failed: %v\n%s",
			addErr, ref, fetchErr, fetchOut)
	}

	if retryErr := tryAddWorktree(barePath, worktreePath, ref); retryErr != nil {
		return fmt.Errorf("git worktree add failed even after fetch retry: %w", retryErr)
	}
	return nil
}

// tryAddWorktree runs `git worktree add --detach` once and returns nil on
// success or a wrapped error containing git's stderr on failure. Extracted
// from AddWorktree so the retry path can call it without duplicating the
// command construction.
func tryAddWorktree(barePath, worktreePath, ref string) error {
	cmd := exec.Command("git", "-C", barePath, "worktree", "add", "--detach", worktreePath, ref)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w\n%s", err, out)
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
	if tok := rc.currentToken(); tok != "" {
		return fmt.Sprintf("https://%s@github.com/%s.git", tok, repoRef)
	}
	return fmt.Sprintf("https://github.com/%s.git", repoRef)
}

func (rc *RepoCache) dirName(repoRef string) string {
	h := sha256.Sum256([]byte(repoRef))
	return fmt.Sprintf("%x", h[:8])
}

// LocalPath returns the expected on-disk bare-clone path for repoRef and
// whether it already exists. Unlike EnsureRepo, it takes no lock and does
// no I/O beyond an os.Stat — safe for latency-sensitive callers like
// Slack BlockSuggestion handlers (issue #153) that must answer within
// Slack's ~3s deadline and can't afford the fetch --all --prune path.
// Returns (path, true) when a bare clone exists, (path, false) otherwise;
// callers that need a guaranteed-fresh clone should still use EnsureRepo.
func (rc *RepoCache) LocalPath(repoRef string) (string, bool) {
	localPath := filepath.Join(rc.dir, rc.dirName(repoRef))
	if _, err := os.Stat(filepath.Join(localPath, "HEAD")); err != nil {
		return localPath, false
	}
	return localPath, true
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
