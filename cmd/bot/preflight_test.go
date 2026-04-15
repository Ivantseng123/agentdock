package main

import (
	"testing"
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
