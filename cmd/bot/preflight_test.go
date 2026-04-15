package main

import (
	"testing"

	"agentdock/internal/config"
)

func TestCheckRedis_InvalidAddr(t *testing.T) {
	err := checkRedis("localhost:99999")
	if err == nil {
		t.Fatal("expected error for invalid redis address")
	}
}

func TestCheckRedis_EmptyAddr(t *testing.T) {
	err := checkRedis("")
	if err == nil {
		t.Fatal("expected error for empty address")
	}
}

func TestCheckGitHubToken_EmptyToken(t *testing.T) {
	_, err := checkGitHubToken("")
	if err == nil {
		t.Fatal("expected error for empty token")
	}
}

func TestCheckGitHubToken_InvalidToken(t *testing.T) {
	_, err := checkGitHubToken("ghp_invalid_token_value")
	if err == nil {
		t.Fatal("expected error for invalid token")
	}
}

func TestCheckAgentCLI_NotFound(t *testing.T) {
	_, err := checkAgentCLI("nonexistent_binary_xyz")
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
}

func TestCheckAgentCLI_ValidBinary(t *testing.T) {
	version, err := checkAgentCLI("go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if version == "" {
		t.Fatal("expected non-empty version string")
	}
}

func TestParseSelection_Valid(t *testing.T) {
	agents := []string{"claude", "codex", "opencode"}
	got := parseSelection("1,3", agents)
	want := []string{"claude", "opencode"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestParseSelection_Invalid(t *testing.T) {
	agents := []string{"claude", "codex"}
	got := parseSelection("0,5,abc", agents)
	if len(got) != 0 {
		t.Fatalf("expected empty, got %v", got)
	}
}

func TestParseSelection_Empty(t *testing.T) {
	agents := []string{"claude"}
	got := parseSelection("", agents)
	if len(got) != 0 {
		t.Fatalf("expected empty, got %v", got)
	}
}

func TestNeedsInput_AllEmpty(t *testing.T) {
	cfg := &config.Config{}
	if !needsInput(cfg) {
		t.Fatal("expected true when all values empty")
	}
}

func TestNeedsInput_AllSet(t *testing.T) {
	cfg := &config.Config{
		Providers: []string{"claude"},
	}
	cfg.Redis.Addr = "localhost:6379"
	cfg.GitHub.Token = "ghp_test"
	if needsInput(cfg) {
		t.Fatal("expected false when all values set")
	}
}
