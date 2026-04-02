package github

import (
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
	cache := NewRepoCache(cacheDir, time.Hour, "")

	repoPath, err := cache.EnsureRepo("file://" + bareDir)
	if err != nil {
		t.Fatalf("EnsureRepo failed: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(repoPath, "main.go"))
	if err != nil {
		t.Fatalf("cloned file not found: %v", err)
	}
	if string(content) != "package main" {
		t.Errorf("unexpected content: %s", string(content))
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
	cache := NewRepoCache(cacheDir, 0, "")

	repoPath, err := cache.EnsureRepo("file://" + bareDir)
	if err != nil {
		t.Fatalf("first EnsureRepo failed: %v", err)
	}

	os.WriteFile(filepath.Join(workDir, "main.go"), []byte("v2"), 0644)
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "-c", "user.name=test", "-c", "user.email=test@test.com", "commit", "-m", "v2")
	run(t, workDir, "git", "push")

	repoPath2, err := cache.EnsureRepo("file://" + bareDir)
	if err != nil {
		t.Fatalf("second EnsureRepo failed: %v", err)
	}
	if repoPath != repoPath2 {
		t.Error("expected same path for cached repo")
	}

	content, _ := os.ReadFile(filepath.Join(repoPath, "main.go"))
	if string(content) != "v2" {
		t.Errorf("expected v2 after pull, got %s", string(content))
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
