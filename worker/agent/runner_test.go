package agent

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Ivantseng123/agentdock/worker/config"
)

func TestRunner_Success(t *testing.T) {
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

	runner := NewRunner([]config.AgentConfig{
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

func TestRunner_ProviderChain(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "good-agent")
	os.WriteFile(script, []byte("#!/bin/sh\necho 'fallback output with enough characters to pass the minimum length check of fifty chars'\n"), 0755)

	runner := NewRunner([]config.AgentConfig{
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

func TestRunner_AllFail(t *testing.T) {
	runner := NewRunner([]config.AgentConfig{
		{Command: "/nonexistent/agent1", Args: []string{"{prompt}"}, Timeout: 5 * time.Second},
		{Command: "/nonexistent/agent2", Args: []string{"{prompt}"}, Timeout: 5 * time.Second},
	})

	_, err := runner.Run(context.Background(), slog.Default(), t.TempDir(), "test", RunOptions{})
	if err == nil {
		t.Error("expected error when all agents fail")
	}
}

func TestRunner_Timeout(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "slow-agent")
	os.WriteFile(script, []byte("#!/bin/sh\nsleep 10\n"), 0755)

	runner := NewRunner([]config.AgentConfig{
		{Command: script, Args: []string{"{prompt}"}, Timeout: 100 * time.Millisecond},
	})

	_, err := runner.Run(context.Background(), slog.Default(), dir, "test", RunOptions{})
	if err == nil {
		t.Error("expected timeout error")
	}
}

func TestRunner_PromptSubstitution(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "echo-agent")
	os.WriteFile(script, []byte(`#!/bin/sh
echo "$1"
echo "padding padding padding padding padding padding padding"
`), 0755)

	runner := NewRunner([]config.AgentConfig{
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

func TestRunner_SecretsInjected(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "env-agent")
	os.WriteFile(script, []byte(`#!/bin/sh
env | grep TOKEN | sort
echo "padding padding padding padding padding padding padding"
`), 0755)

	runner := NewRunner([]config.AgentConfig{
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

func TestRunner_GithubTokenFallback(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "env-agent")
	os.WriteFile(script, []byte(`#!/bin/sh
env | grep GH_TOKEN
echo "padding padding padding padding padding padding padding"
`), 0755)

	runner := NewRunner([]config.AgentConfig{
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

func TestRunner_OutputFilePlaceholder(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "outfile-agent")
	// Script writes clean text to $2 (the -o path) and prints noise to stdout.
	// Runner must return the file content, not the stdout noise.
	os.WriteFile(script, []byte(`#!/bin/sh
echo "noisy stdout header"
printf '%s' "clean answer from file" > "$2"
echo "noisy stdout footer"
`), 0755)

	runner := NewRunner([]config.AgentConfig{
		{Command: script, Args: []string{"-o", "{output_file}", "{prompt}"}, Timeout: 5 * time.Second},
	})

	output, err := runner.Run(context.Background(), slog.Default(), dir, "test prompt", RunOptions{})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if output != "clean answer from file" {
		t.Errorf("expected file content, got: %q", output)
	}
	if strings.Contains(output, "noisy") {
		t.Errorf("output must not contain stdout noise, got: %q", output)
	}
}

func TestRunner_OutputFileCleanedUp(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "outfile-agent")
	os.WriteFile(script, []byte(`#!/bin/sh
printf 'x' > "$2"
echo "$2" > `+filepath.Join(dir, "captured-path.txt")+`
`), 0755)

	runner := NewRunner([]config.AgentConfig{
		{Command: script, Args: []string{"-o", "{output_file}", "{prompt}"}, Timeout: 5 * time.Second},
	})

	_, err := runner.Run(context.Background(), slog.Default(), dir, "p", RunOptions{})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	captured, err := os.ReadFile(filepath.Join(dir, "captured-path.txt"))
	if err != nil {
		t.Fatalf("read captured path: %v", err)
	}
	tmpPath := strings.TrimSpace(string(captured))
	if _, statErr := os.Stat(tmpPath); !os.IsNotExist(statErr) {
		t.Errorf("temp output file %q should have been removed, statErr=%v", tmpPath, statErr)
	}
}

func TestNewRunnerFromConfig_EmptyProviders(t *testing.T) {
	cfg := &config.Config{
		Agents:    map[string]config.AgentConfig{},
		Providers: []string{},
	}
	runner := NewRunnerFromConfig(cfg)
	_, err := runner.Run(context.Background(), slog.Default(), t.TempDir(), "test", RunOptions{})
	if err == nil {
		t.Error("expected error when providers is empty")
	}
}

func TestNewRunnerFromConfig_SingleProvider(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "single-agent")
	os.WriteFile(script, []byte("#!/bin/sh\necho 'output from single provider agent padding padding padding'\n"), 0755)

	cfg := &config.Config{
		Agents: map[string]config.AgentConfig{
			"myagent": {Command: script, Args: []string{"{prompt}"}, Timeout: 5 * time.Second},
		},
		Providers: []string{"myagent"},
	}
	runner := NewRunnerFromConfig(cfg)
	output, err := runner.Run(context.Background(), slog.Default(), dir, "test", RunOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(output, "single provider") {
		t.Errorf("unexpected output: %q", output)
	}
}

func TestExpandExtraArgs_Nil(t *testing.T) {
	args := []string{"run", "--pure", "{extra_args}", "{prompt}"}
	got := expandExtraArgs(args, nil)
	want := []string{"run", "--pure", "{prompt}"}
	if !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	// No empty string leftovers.
	for _, a := range got {
		if a == "" {
			t.Errorf("empty-string arg leaked into result: %q", got)
		}
	}
}

func TestExpandExtraArgs_Empty(t *testing.T) {
	args := []string{"run", "--pure", "{extra_args}", "{prompt}"}
	got := expandExtraArgs(args, []string{})
	want := []string{"run", "--pure", "{prompt}"}
	if !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExpandExtraArgs_Single(t *testing.T) {
	args := []string{"run", "--pure", "{extra_args}", "{prompt}"}
	got := expandExtraArgs(args, []string{"--foo"})
	want := []string{"run", "--pure", "--foo", "{prompt}"}
	if !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExpandExtraArgs_Multi(t *testing.T) {
	args := []string{"run", "--pure", "{extra_args}", "{prompt}"}
	got := expandExtraArgs(args, []string{"-m", "opencode/claude-opus-4-7"})
	want := []string{"run", "--pure", "-m", "opencode/claude-opus-4-7", "{prompt}"}
	if !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestExpandExtraArgs_ThenStringSubstitute verifies the two-step pipeline used
// by runOne: expandExtraArgs first, then substituteStringPlaceholders. After
// both steps the argv must contain the extra args in the right slot AND the
// substituted prompt.
func TestExpandExtraArgs_ThenStringSubstitute(t *testing.T) {
	args := []string{"run", "--pure", "{extra_args}", "{prompt}"}
	expanded := expandExtraArgs(args, []string{"-m", "x"})
	got := substituteStringPlaceholders(expanded, map[string]string{"{prompt}": "hi"})
	want := []string{"run", "--pure", "-m", "x", "hi"}
	if !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestRunner_ExtraArgsSpliced is the integration-ish check: spawn a real
// process with a built-in-shaped Args ({prompt} at the end, {extra_args}
// right before it) and verify the shell sees the extra flags between --pure
// and the prompt positional.
func TestRunner_ExtraArgsSpliced(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "argv-agent")
	// Echo each positional on its own line so we can assert the exact ordering.
	os.WriteFile(script, []byte(`#!/bin/sh
for a in "$@"; do echo "ARG=$a"; done
echo "padding padding padding padding padding padding padding padding padding padding"
`), 0755)

	runner := NewRunner([]config.AgentConfig{
		{
			Command:   script,
			Args:      []string{"run", "--pure", "{extra_args}", "{prompt}"},
			ExtraArgs: []string{"-m", "opencode/claude-opus-4-7"},
			Timeout:   5 * time.Second,
		},
	})
	output, err := runner.Run(context.Background(), slog.Default(), dir, "hello", RunOptions{})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	// Verify order: run, --pure, -m, opencode/claude-opus-4-7, hello
	wantOrder := []string{
		"ARG=run",
		"ARG=--pure",
		"ARG=-m",
		"ARG=opencode/claude-opus-4-7",
		"ARG=hello",
	}
	idx := 0
	for _, line := range strings.Split(output, "\n") {
		if line == wantOrder[idx] {
			idx++
			if idx == len(wantOrder) {
				break
			}
		}
	}
	if idx != len(wantOrder) {
		t.Errorf("argv order wrong. got output:\n%s\nstuck at wantOrder[%d]=%q", output, idx, wantOrder[idx])
	}
}

// TestRunner_ExtraArgsSplicedInStdinPath covers the large-prompt path where
// runOne drops the {prompt} arg and feeds the prompt via stdin. extra_args
// must still splice into the same slot as the small-prompt path — both paths
// share the single expandExtraArgs splice site.
func TestRunner_ExtraArgsSplicedInStdinPath(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "argv-agent")
	os.WriteFile(script, []byte(`#!/bin/sh
for a in "$@"; do echo "ARG=$a"; done
echo "padding padding padding padding padding padding padding padding padding padding"
`), 0755)

	runner := NewRunner([]config.AgentConfig{
		{
			Command:   script,
			Args:      []string{"run", "--pure", "{extra_args}", "{prompt}"},
			ExtraArgs: []string{"-m", "opencode/claude-opus-4-7"},
			Timeout:   5 * time.Second,
		},
	})
	// 33KB prompt forces the stdin path (maxArgLen = 32KB).
	bigPrompt := strings.Repeat("x", 33*1024)
	output, err := runner.Run(context.Background(), slog.Default(), dir, bigPrompt, RunOptions{})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	wantOrder := []string{
		"ARG=run",
		"ARG=--pure",
		"ARG=-m",
		"ARG=opencode/claude-opus-4-7",
	}
	idx := 0
	for _, line := range strings.Split(output, "\n") {
		if line == wantOrder[idx] {
			idx++
			if idx == len(wantOrder) {
				break
			}
		}
	}
	if idx != len(wantOrder) {
		t.Errorf("argv order wrong in stdin path. got output:\n%s\nstuck at wantOrder[%d]=%q", output, idx, wantOrder[idx])
	}
	// Prompt must NOT appear as an arg — it should have been dropped and sent via stdin.
	if strings.Contains(output, "ARG=xxx") {
		t.Errorf("prompt leaked into argv (should be on stdin), got:\n%s", output)
	}
}

func TestRunner_CancelShortCircuitsProviderChain(t *testing.T) {
	runner := &Runner{
		agents: []config.AgentConfig{
			{Command: "nonexistent-agent-one", Args: []string{"{prompt}"}, Timeout: time.Second},
			{Command: "nonexistent-agent-two", Args: []string{"{prompt}"}, Timeout: time.Second},
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := runner.Run(ctx, slog.Default(), t.TempDir(), "noop", RunOptions{})
	if err == nil {
		t.Fatal("expected error on cancelled ctx")
	}
	if err.Error() != "cancelled" {
		t.Errorf("err = %q, want \"cancelled\" (chain must not try the second agent)", err.Error())
	}
}

func TestTailStderr(t *testing.T) {
	if got := tailStderr("short"); got != "short" {
		t.Errorf("tailStderr(short) = %q, want %q", got, "short")
	}
	if got := tailStderr("  trim  "); got != "trim" {
		t.Errorf("tailStderr did not trim whitespace: got %q", got)
	}
	long := strings.Repeat("x", stderrTailLen+100)
	got := tailStderr(long)
	if !strings.HasPrefix(got, "…") {
		t.Errorf("tailStderr did not prefix with marker: got prefix %q", got[:6])
	}
	if len(got) != stderrTailLen+len("…") {
		t.Errorf("tailStderr len = %d, want %d", len(got), stderrTailLen+len("…"))
	}
}

func TestDetectBlockedArgs(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"empty", []string{}, nil},
		{"clean", []string{"--print", "-p", "hi"}, nil},
		{"exact match", []string{"--dangerously-skip-permissions"}, []string{"--dangerously-skip-permissions"}},
		{"with value", []string{"--dangerously-skip-permissions=yes"}, []string{"--dangerously-skip-permissions=yes"}},
		{"substring no match", []string{"--mention-dangerously-skip-permissions"}, nil},
		{"mixed", []string{"--print", "--dangerously-skip-permissions", "-p"}, []string{"--dangerously-skip-permissions"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := detectBlockedArgs(tc.in)
			if !slices.Equal(got, tc.want) {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFilterClaudeCodeEnv(t *testing.T) {
	in := []string{
		"PATH=/usr/bin",
		"CLAUDE_CODE_NO_FLICKER=1",
		"CLAUDE_CODE_RESIDUE=oops",
		"HOME=/home/me",
		"CLAUDE_CODE_BAD_FORMAT", // no = sign, malformed
	}
	got := filterClaudeCodeEnv(in)
	want := []string{
		"PATH=/usr/bin",
		"CLAUDE_CODE_NO_FLICKER=1",
		"HOME=/home/me",
	}
	if !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRunner_BlockedArgsRejected(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "should-not-run")
	os.WriteFile(script, []byte("#!/bin/sh\necho should not run\n"), 0755)

	runner := NewRunner([]config.AgentConfig{{
		Command:   script,
		Args:      []string{"{extra_args}", "{prompt}"},
		ExtraArgs: []string{"--dangerously-skip-permissions"},
		Timeout:   5 * time.Second,
	}})
	_, err := runner.Run(context.Background(), slog.Default(), dir, "test", RunOptions{})
	if err == nil {
		t.Fatal("expected error when blocked arg present")
	}
	if !strings.Contains(err.Error(), "blocked args rejected") {
		t.Errorf("err = %q, want substring \"blocked args rejected\"", err.Error())
	}
}

func TestRunner_StderrTailTruncated(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "noisy-failer")
	// Print > stderrTailLen bytes to stderr then exit non-zero. yes |head gives
	// repeatable bulk content without depending on /dev/urandom.
	os.WriteFile(script, []byte(`#!/bin/sh
yes "spam-line-content-padding-padding-padding-padding-padding" | head -n 200 >&2
exit 1
`), 0755)

	runner := NewRunner([]config.AgentConfig{{
		Command: script, Args: []string{"{prompt}"}, Timeout: 5 * time.Second,
	}})
	_, err := runner.Run(context.Background(), slog.Default(), dir, "test", RunOptions{})
	if err == nil {
		t.Fatal("expected non-zero exit error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "…") {
		t.Errorf("err message missing truncation marker: %q", msg)
	}
	// Sanity bound: even with 200 lines of spam, total error message stays
	// within stderrTailLen + a small wrapper budget.
	if len(msg) > stderrTailLen+500 {
		t.Errorf("err message too long: %d bytes (stderrTailLen=%d)", len(msg), stderrTailLen)
	}
}

func TestRunner_ClaudeCodeEnvStripped(t *testing.T) {
	t.Setenv("CLAUDE_CODE_RESIDUE", "should-be-stripped")
	t.Setenv("CLAUDE_CODE_NO_FLICKER", "1")

	dir := t.TempDir()
	script := filepath.Join(dir, "env-probe")
	os.WriteFile(script, []byte(`#!/bin/sh
env | grep '^CLAUDE_CODE_' | sort
echo "padding padding padding padding padding padding padding padding"
`), 0755)

	runner := NewRunner([]config.AgentConfig{{
		Command: script, Args: []string{"{prompt}"}, Timeout: 5 * time.Second,
	}})
	output, err := runner.Run(context.Background(), slog.Default(), dir, "test", RunOptions{})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if strings.Contains(output, "CLAUDE_CODE_RESIDUE") {
		t.Errorf("CLAUDE_CODE_RESIDUE leaked into agent env: %q", output)
	}
	if !strings.Contains(output, "CLAUDE_CODE_NO_FLICKER=1") {
		t.Errorf("CLAUDE_CODE_NO_FLICKER did not pass through: %q", output)
	}
}
