package github

import (
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestRepoCache_EnsureRepo_ClonesNewRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	bareDir := t.TempDir()
	run(t, bareDir, "git", "init", "--bare")

	workDir := t.TempDir()
	run(t, workDir, "git", "clone", bareDir, ".")
	os.WriteFile(filepath.Join(workDir, "main.go"), []byte("package main"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "-c", "user.name=test", "-c", "user.email=test@test.com", "commit", "-m", "init")
	run(t, workDir, "git", "push")

	cacheDir := t.TempDir()
	cache := NewRepoCache(cacheDir, time.Hour, "", slog.Default())

	repoPath, err := cache.EnsureRepo("file://"+bareDir, "")
	if err != nil {
		t.Fatalf("EnsureRepo failed: %v", err)
	}

	// Bare repo — verify HEAD exists, not working files.
	if _, err := os.Stat(filepath.Join(repoPath, "HEAD")); err != nil {
		t.Fatalf("bare repo missing HEAD: %v", err)
	}
}

func TestRepoCache_EnsureRepo_PullsExistingRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	bareDir := t.TempDir()
	run(t, bareDir, "git", "init", "--bare")

	workDir := t.TempDir()
	run(t, workDir, "git", "clone", bareDir, ".")
	os.WriteFile(filepath.Join(workDir, "main.go"), []byte("v1"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "-c", "user.name=test", "-c", "user.email=test@test.com", "commit", "-m", "v1")
	run(t, workDir, "git", "push")

	cacheDir := t.TempDir()
	cache := NewRepoCache(cacheDir, 0, "", slog.Default())

	repoPath, err := cache.EnsureRepo("file://"+bareDir, "")
	if err != nil {
		t.Fatalf("first EnsureRepo failed: %v", err)
	}

	os.WriteFile(filepath.Join(workDir, "main.go"), []byte("v2"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "-c", "user.name=test", "-c", "user.email=test@test.com", "commit", "-m", "v2")
	run(t, workDir, "git", "push")

	repoPath2, err := cache.EnsureRepo("file://"+bareDir, "")
	if err != nil {
		t.Fatalf("second EnsureRepo failed: %v", err)
	}
	if repoPath != repoPath2 {
		t.Error("expected same path for cached repo")
	}
}

func TestSanitizeURL(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"https://ghp_abc123@github.com/org/repo.git", "https://***@github.com/org/repo.git"},
		{"https://user:pass@github.com/org/repo.git", "https://***@github.com/org/repo.git"},
		{"https://github.com/org/repo.git", "https://github.com/org/repo.git"},
		{"git@github.com:org/repo.git", "git@github.com:org/repo.git"},
		{"file:///tmp/repo", "file:///tmp/repo"},
		{"org/repo", "org/repo"},
	}
	for _, tt := range tests {
		if got := SanitizeURL(tt.input); got != tt.want {
			t.Errorf("SanitizeURL(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestRepoCache_BareCloneAndWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	// Create a source bare repo with one commit.
	sourceDir := t.TempDir()
	run(t, sourceDir, "git", "init", "--bare")

	workDir := t.TempDir()
	run(t, workDir, "git", "clone", sourceDir, ".")
	os.WriteFile(filepath.Join(workDir, "main.go"), []byte("package main"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "-c", "user.name=test", "-c", "user.email=test@test.com", "commit", "-m", "init")
	run(t, workDir, "git", "push")

	cacheDir := t.TempDir()
	cache := NewRepoCache(cacheDir, time.Hour, "", slog.Default())

	// EnsureRepo should create a bare clone.
	barePath, err := cache.EnsureRepo("file://"+sourceDir, "")
	if err != nil {
		t.Fatalf("EnsureRepo failed: %v", err)
	}

	// Bare repo should NOT have working tree files.
	if _, err := os.Stat(filepath.Join(barePath, "main.go")); !os.IsNotExist(err) {
		t.Error("bare repo should not have working tree files")
	}
	// But should have HEAD (bare repo indicator).
	if _, err := os.Stat(filepath.Join(barePath, "HEAD")); err != nil {
		t.Errorf("bare repo missing HEAD: %v", err)
	}

	// AddWorktree should create a working directory.
	wtPath := filepath.Join(t.TempDir(), "wt1")
	if err := cache.AddWorktree(barePath, "", wtPath); err != nil {
		t.Fatalf("AddWorktree failed: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(wtPath, "main.go"))
	if err != nil {
		t.Fatalf("worktree file not found: %v", err)
	}
	if string(content) != "package main" {
		t.Errorf("unexpected content: %s", string(content))
	}

	// RemoveWorktree should delete it.
	if err := cache.RemoveWorktree(wtPath); err != nil {
		t.Fatalf("RemoveWorktree failed: %v", err)
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Error("worktree dir should be deleted")
	}
}

func TestRepoCache_CleanAll(t *testing.T) {
	cacheDir := t.TempDir()
	cache := NewRepoCache(cacheDir, time.Hour, "", slog.Default())

	os.WriteFile(filepath.Join(cacheDir, "dummy"), []byte("x"), 0644)

	if err := cache.CleanAll(); err != nil {
		t.Fatalf("CleanAll failed: %v", err)
	}
	if _, err := os.Stat(cacheDir); !os.IsNotExist(err) {
		t.Error("cache dir should be deleted")
	}
}

func TestRepoCache_PurgeStale(t *testing.T) {
	cacheDir := t.TempDir()
	cache := NewRepoCache(cacheDir, time.Hour, "", slog.Default())

	os.WriteFile(filepath.Join(cacheDir, "leftover"), []byte("x"), 0644)

	if err := cache.PurgeStale(); err != nil {
		t.Fatalf("PurgeStale failed: %v", err)
	}
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		t.Fatalf("ReadDir failed: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected empty cache dir, got %d entries", len(entries))
	}
}

func TestRepoCache_ResolveURLWithToken_PerCall(t *testing.T) {
	dir := t.TempDir()
	rc := NewRepoCache(dir, 0, "", slog.Default())
	url := rc.resolveURLWithToken("owner/repo", "ghp_percall")
	if url != "https://ghp_percall@github.com/owner/repo.git" {
		t.Errorf("got %q", url)
	}
}

func TestRepoCache_ResolveURLWithToken_Fallback(t *testing.T) {
	dir := t.TempDir()
	rc := NewRepoCache(dir, 0, "ghp_default", slog.Default())
	url := rc.resolveURLWithToken("owner/repo", "")
	if url != "https://ghp_default@github.com/owner/repo.git" {
		t.Errorf("got %q", url)
	}
}

func TestRepoCache_ResolveURLWithToken_NoToken(t *testing.T) {
	dir := t.TempDir()
	rc := NewRepoCache(dir, 0, "", slog.Default())
	url := rc.resolveURLWithToken("owner/repo", "")
	if url != "https://github.com/owner/repo.git" {
		t.Errorf("got %q", url)
	}
}

func TestRepoCache_ResolveURLWithToken_FullGithubURLInjectsToken(t *testing.T) {
	dir := t.TempDir()
	rc := NewRepoCache(dir, 0, "", slog.Default())
	url := rc.resolveURLWithToken("https://github.com/owner/repo.git", "ghp_percall")
	if url != "https://ghp_percall@github.com/owner/repo.git" {
		t.Errorf("got %q", url)
	}
}

func TestRepoCache_ResolveURLWithToken_FullGithubURLFallbackPAT(t *testing.T) {
	dir := t.TempDir()
	rc := NewRepoCache(dir, 0, "ghp_default", slog.Default())
	url := rc.resolveURLWithToken("https://github.com/owner/repo.git", "")
	if url != "https://ghp_default@github.com/owner/repo.git" {
		t.Errorf("got %q", url)
	}
}

func TestRepoCache_ResolveURLWithToken_FullGithubURLNoTokenPassthrough(t *testing.T) {
	dir := t.TempDir()
	rc := NewRepoCache(dir, 0, "", slog.Default())
	url := rc.resolveURLWithToken("https://github.com/owner/repo.git", "")
	if url != "https://github.com/owner/repo.git" {
		t.Errorf("got %q", url)
	}
}

func TestRepoCache_ResolveURLWithToken_URLWithExistingUserinfoPassthrough(t *testing.T) {
	dir := t.TempDir()
	rc := NewRepoCache(dir, 0, "ghp_default", slog.Default())
	url := rc.resolveURLWithToken("https://ghp_old@github.com/owner/repo.git", "ghp_new")
	if url != "https://ghp_old@github.com/owner/repo.git" {
		t.Errorf("got %q", url)
	}
}

func TestRepoCache_ResolveURLWithToken_NonGithubHostPassthrough(t *testing.T) {
	dir := t.TempDir()
	rc := NewRepoCache(dir, 0, "ghp_default", slog.Default())
	url := rc.resolveURLWithToken("https://gitlab.com/owner/repo.git", "ghp_percall")
	if url != "https://gitlab.com/owner/repo.git" {
		t.Errorf("got %q", url)
	}
}

func TestRepoCache_ResolveURLWithToken_SSHAndFilePassthrough(t *testing.T) {
	dir := t.TempDir()
	rc := NewRepoCache(dir, 0, "ghp_default", slog.Default())
	if got := rc.resolveURLWithToken("git@github.com:owner/repo.git", "ghp_x"); got != "git@github.com:owner/repo.git" {
		t.Errorf("git@: got %q", got)
	}
	if got := rc.resolveURLWithToken("file:///tmp/repo", "ghp_x"); got != "file:///tmp/repo" {
		t.Errorf("file://: got %q", got)
	}
}

func run(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v failed: %v\n%s", name, args, err, out)
	}
}
