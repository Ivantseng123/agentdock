package diagnosis

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"slack-issue-bot/internal/llm"
)

// initGitRepo creates a temp git repo with given files.
func initGitRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.name", "test")
	runGit(t, dir, "config", "user.email", "test@test.com")
	for path, content := range files {
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatalf("write %s: %v", full, err)
		}
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "init")
	return dir
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

// --- Tests ---

// TestEngine_Diagnose_AgentLoop verifies that the engine delegates to RunLoop
// and returns a triage card.
func TestEngine_Diagnose_AgentLoop(t *testing.T) {
	mock := &mockConvProvider{
		responses: []llm.ChatResponse{
			{
				Content:    triageJSON("login bug in auth module", "high"),
				StopReason: llm.StopReasonFinish,
			},
		},
	}

	engine := NewEngine(mock, EngineConfig{
		MaxTurns: 5,
		CacheTTL: 5 * time.Minute,
	})
	defer engine.Stop()

	resp, err := engine.Diagnose(context.Background(), DiagnoseInput{
		Type:     "bug",
		Message:  "login crashes",
		RepoPath: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Diagnose failed: %v", err)
	}
	if mock.calls != 1 {
		t.Errorf("expected 1 call, got %d", mock.calls)
	}
	if resp.Summary != "login bug in auth module" {
		t.Errorf("unexpected summary: %q", resp.Summary)
	}
	if resp.Confidence != "high" {
		t.Errorf("unexpected confidence: %q", resp.Confidence)
	}
}

// TestEngine_Diagnose_CacheHit verifies that two identical calls result
// in only one actual LLM invocation.
func TestEngine_Diagnose_CacheHit(t *testing.T) {
	mock := &mockConvProvider{
		responses: []llm.ChatResponse{
			{
				Content:    triageJSON("cached result", "medium"),
				StopReason: llm.StopReasonFinish,
			},
		},
	}

	engine := NewEngine(mock, EngineConfig{
		MaxTurns: 5,
		CacheTTL: 5 * time.Minute,
	})
	defer engine.Stop()

	repoDir := t.TempDir()
	input := DiagnoseInput{
		Type:     "bug",
		Message:  "something broken",
		RepoPath: repoDir,
	}

	// First call -- should hit the LLM.
	resp1, err := engine.Diagnose(context.Background(), input)
	if err != nil {
		t.Fatalf("first Diagnose failed: %v", err)
	}
	if mock.calls != 1 {
		t.Errorf("expected 1 call after first Diagnose, got %d", mock.calls)
	}

	// Second call -- should be cached.
	resp2, err := engine.Diagnose(context.Background(), input)
	if err != nil {
		t.Fatalf("second Diagnose failed: %v", err)
	}
	if mock.calls != 1 {
		t.Errorf("expected still 1 call after second Diagnose (cache hit), got %d", mock.calls)
	}

	if resp1.Summary != resp2.Summary {
		t.Errorf("cached response differs: %q vs %q", resp1.Summary, resp2.Summary)
	}
}

// TestEngine_FindFiles verifies the grep-only lite mode.
func TestEngine_FindFiles(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}

	repoDir := initGitRepo(t, map[string]string{
		"src/auth/login.go":  "package auth\n\nfunc Login() {}",
		"src/models/user.go": "package models\n\ntype User struct{}",
	})

	mock := &mockConvProvider{} // Should not be called.
	engine := NewEngine(mock, EngineConfig{
		MaxFiles: 10,
		CacheTTL: 5 * time.Minute,
	})
	defer engine.Stop()

	refs := engine.FindFiles(DiagnoseInput{
		Type:     "bug",
		Message:  "login issue",
		RepoPath: repoDir,
		Keywords: []string{"Login"},
	})

	if len(refs) == 0 {
		t.Fatal("expected at least 1 file ref")
	}

	found := false
	for _, r := range refs {
		if r.Path == "src/auth/login.go" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected src/auth/login.go in results, got: %v", refs)
	}

	if mock.calls != 0 {
		t.Errorf("FindFiles should not call LLM, but got %d calls", mock.calls)
	}
}

// --- parseStringArray tests (kept from original) ---

func TestParseStringArray(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int
	}{
		{"plain array", `["a.go", "b.go"]`, 2},
		{"with code block", "```json\n[\"a.go\"]\n```", 1},
		{"embedded in text", "Here are the terms: [\"reinsurance\", \"cession\"] for searching", 2},
		{"invalid", "no json here", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			arr, err := parseStringArray(tt.input)
			if tt.want == 0 {
				if err == nil {
					t.Error("expected error for invalid input")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(arr) != tt.want {
				t.Errorf("expected %d items, got %d: %v", tt.want, len(arr), arr)
			}
		})
	}
}
