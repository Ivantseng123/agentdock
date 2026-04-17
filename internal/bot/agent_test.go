package bot

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentdock/internal/config"
)

func TestAgentRunner_Success(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "mock-agent")
	os.WriteFile(script, []byte(`#!/bin/sh
echo "## Summary"
echo ""
echo "Test issue body"
echo ""
echo "===TRIAGE_METADATA==="
echo '{"issue_type":"bug","confidence":"high","files":[],"open_questions":[],"suggested_title":"test"}'
`), 0755)

	runner := NewAgentRunner([]config.AgentConfig{
		{Command: script, Args: []string{"{prompt}"}, Timeout: 10 * time.Second},
	})

	output, err := runner.Run(context.Background(), slog.Default(), dir, "test prompt", RunOptions{})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if output == "" {
		t.Error("output is empty")
	}
	if !strings.Contains(output, "Test issue body") {
		t.Errorf("output missing expected content: %q", output)
	}
}

func TestAgentRunner_ProviderChain(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "good-agent")
	os.WriteFile(script, []byte("#!/bin/sh\necho 'fallback output with enough characters to pass the minimum length check of fifty chars'\n"), 0755)

	runner := NewAgentRunner([]config.AgentConfig{
		{Command: "/nonexistent/agent", Args: []string{"{prompt}"}, Timeout: 5 * time.Second},
		{Command: script, Args: []string{"{prompt}"}, Timeout: 5 * time.Second},
	})

	output, err := runner.Run(context.Background(), slog.Default(), dir, "test", RunOptions{})
	if err != nil {
		t.Fatalf("Run with provider chain failed: %v", err)
	}
	if !strings.Contains(output, "fallback output") {
		t.Errorf("output = %q, want fallback output", output)
	}
}

func TestAgentRunner_AllFail(t *testing.T) {
	runner := NewAgentRunner([]config.AgentConfig{
		{Command: "/nonexistent/agent1", Args: []string{"{prompt}"}, Timeout: 5 * time.Second},
		{Command: "/nonexistent/agent2", Args: []string{"{prompt}"}, Timeout: 5 * time.Second},
	})

	_, err := runner.Run(context.Background(), slog.Default(), t.TempDir(), "test", RunOptions{})
	if err == nil {
		t.Error("expected error when all agents fail")
	}
}

func TestAgentRunner_Timeout(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "slow-agent")
	os.WriteFile(script, []byte("#!/bin/sh\nsleep 10\n"), 0755)

	runner := NewAgentRunner([]config.AgentConfig{
		{Command: script, Args: []string{"{prompt}"}, Timeout: 100 * time.Millisecond},
	})

	_, err := runner.Run(context.Background(), slog.Default(), dir, "test", RunOptions{})
	if err == nil {
		t.Error("expected timeout error")
	}
}

func TestAgentRunner_PromptSubstitution(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "echo-agent")
	os.WriteFile(script, []byte(`#!/bin/sh
echo "$1"
echo "padding padding padding padding padding padding padding"
`), 0755)

	runner := NewAgentRunner([]config.AgentConfig{
		{Command: script, Args: []string{"{prompt}"}, Timeout: 5 * time.Second},
	})

	output, err := runner.Run(context.Background(), slog.Default(), dir, "hello world", RunOptions{})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if !strings.Contains(output, "hello world") {
		t.Errorf("prompt not substituted in output: %q", output)
	}
}

func TestAgentRunner_SecretsInjected(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "env-agent")
	os.WriteFile(script, []byte(`#!/bin/sh
env | grep TOKEN | sort
echo "padding padding padding padding padding padding padding"
`), 0755)

	runner := NewAgentRunner([]config.AgentConfig{
		{Command: script, Args: []string{"{prompt}"}, Timeout: 5 * time.Second},
	})

	secrets := map[string]string{
		"GH_TOKEN":  "ghp_from_secrets",
		"K8S_TOKEN": "k8s_val",
	}
	output, err := runner.Run(context.Background(), slog.Default(), dir, "test", RunOptions{Secrets: secrets})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if !strings.Contains(output, "GH_TOKEN=ghp_from_secrets") {
		t.Errorf("GH_TOKEN not injected: %q", output)
	}
	if !strings.Contains(output, "K8S_TOKEN=k8s_val") {
		t.Errorf("K8S_TOKEN not injected: %q", output)
	}
}

func TestAgentRunner_GithubTokenFallback(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "env-agent")
	os.WriteFile(script, []byte(`#!/bin/sh
env | grep GH_TOKEN
echo "padding padding padding padding padding padding padding"
`), 0755)

	runner := NewAgentRunner([]config.AgentConfig{
		{Command: script, Args: []string{"{prompt}"}, Timeout: 5 * time.Second},
	})
	runner.githubToken = "ghp_fallback"

	output, err := runner.Run(context.Background(), slog.Default(), dir, "test", RunOptions{})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if !strings.Contains(output, "GH_TOKEN=ghp_fallback") {
		t.Errorf("githubToken fallback not working: %q", output)
	}
}
