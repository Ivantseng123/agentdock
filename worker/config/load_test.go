package config

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func newTestCmd(t *testing.T) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "test"}
	RegisterFlags(cmd)
	return cmd
}

func clearWorkerEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"GITHUB_TOKEN", "REDIS_ADDR", "REDIS_PASSWORD",
		"SECRET_KEY", "PROVIDERS",
	} {
		t.Setenv(k, "")
	}
}

func TestBuildKoanf_FlatCountFlag(t *testing.T) {
	clearWorkerEnv(t)
	cmd := newTestCmd(t)
	if err := cmd.ParseFlags([]string{"--workers=7"}); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	cfg, _, _, _, err := BuildKoanf(cmd, "")
	if err != nil {
		t.Fatalf("BuildKoanf: %v", err)
	}
	if cfg.Count != 7 {
		t.Errorf("Count = %d, want 7", cfg.Count)
	}
}

func TestBuildKoanf_FlatPromptExtraRules(t *testing.T) {
	clearWorkerEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "worker.yaml")
	body := `
count: 2
prompt:
  extra_rules:
    - "rule-1"
    - "rule-2"
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cmd := newTestCmd(t)
	if err := cmd.ParseFlags(nil); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	cfg, _, _, _, err := BuildKoanf(cmd, path)
	if err != nil {
		t.Fatalf("BuildKoanf: %v", err)
	}
	if cfg.Count != 2 {
		t.Errorf("Count = %d, want 2", cfg.Count)
	}
	if len(cfg.Prompt.ExtraRules) != 2 {
		t.Errorf("ExtraRules len = %d, want 2", len(cfg.Prompt.ExtraRules))
	}
}

func TestMergeBuiltinAgents_FillsMissing(t *testing.T) {
	clearWorkerEnv(t)
	cmd := newTestCmd(t)
	if err := cmd.ParseFlags(nil); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	cfg, _, _, _, err := BuildKoanf(cmd, "")
	if err != nil {
		t.Fatalf("BuildKoanf: %v", err)
	}
	for _, name := range []string{"claude", "codex", "opencode"} {
		if _, ok := cfg.Agents[name]; !ok {
			t.Errorf("BuiltinAgents missing %q after merge", name)
		}
	}
}

func TestMergeBuiltinAgents_EmptyAgentsBlock(t *testing.T) {
	clearWorkerEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "worker.yaml")
	// A yaml that omits the agents: block entirely — mirrors what
	// `agentdock init worker` now generates.
	body := `
count: 1
providers: [claude]
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cmd := newTestCmd(t)
	if err := cmd.ParseFlags(nil); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	cfg, _, _, _, err := BuildKoanf(cmd, path)
	if err != nil {
		t.Fatalf("BuildKoanf: %v", err)
	}
	for name := range BuiltinAgents {
		got, ok := cfg.Agents[name]
		if !ok {
			t.Errorf("mergeBuiltinAgents did not fill %q", name)
			continue
		}
		want := BuiltinAgents[name]
		// Validate the full struct — Args especially, since stale Args were the
		// root cause of the --pure incident that motivated this PR.
		if !reflect.DeepEqual(got, want) {
			t.Errorf("agent %q: got %+v, want %+v", name, got, want)
		}
	}
}

// TestMergeBuiltinAgents_ExtraArgsLayeredOnBuiltin verifies the user case
// where yaml contains only `extra_args` for an agent — command/args/timeout/
// skill_dir must all inherit from BuiltinAgents, and ExtraArgs must be the
// user's value.
func TestMergeBuiltinAgents_ExtraArgsLayeredOnBuiltin(t *testing.T) {
	clearWorkerEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "worker.yaml")
	body := `
count: 1
providers: [opencode]
agents:
  opencode:
    extra_args: ["-m", "opencode/claude-opus-4-7"]
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cmd := newTestCmd(t)
	if err := cmd.ParseFlags(nil); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	cfg, _, _, _, err := BuildKoanf(cmd, path)
	if err != nil {
		t.Fatalf("BuildKoanf: %v", err)
	}
	got := cfg.Agents["opencode"]
	builtin := BuiltinAgents["opencode"]
	if got.Command != builtin.Command {
		t.Errorf("Command = %q, want built-in %q", got.Command, builtin.Command)
	}
	if !reflect.DeepEqual(got.Args, builtin.Args) {
		t.Errorf("Args = %v, want built-in %v", got.Args, builtin.Args)
	}
	if got.SkillDir != builtin.SkillDir {
		t.Errorf("SkillDir = %q, want built-in %q", got.SkillDir, builtin.SkillDir)
	}
	if got.Timeout != builtin.Timeout {
		t.Errorf("Timeout = %v, want built-in %v", got.Timeout, builtin.Timeout)
	}
	want := []string{"-m", "opencode/claude-opus-4-7"}
	if !reflect.DeepEqual(got.ExtraArgs, want) {
		t.Errorf("ExtraArgs = %v, want %v", got.ExtraArgs, want)
	}
}

// TestMergeBuiltinAgents_ConflictWarnsWhenOverrideArgsMissToken: user writes
// both a full `args` override AND `extra_args`, but the override doesn't
// contain `{extra_args}`. Startup must emit a warn log so the operator knows
// their extra_args never reached the CLI.
func TestMergeBuiltinAgents_ConflictWarnsWhenOverrideArgsMissToken(t *testing.T) {
	clearWorkerEnv(t)
	// Capture slog output.
	var buf bytes.Buffer
	origDefault := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(origDefault)

	dir := t.TempDir()
	path := filepath.Join(dir, "worker.yaml")
	body := `
count: 1
providers: [opencode]
agents:
  opencode:
    command: opencode
    args: ["run", "--pure", "{prompt}"]
    extra_args: ["-m", "opencode/claude-opus-4-7"]
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cmd := newTestCmd(t)
	if err := cmd.ParseFlags(nil); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	if _, _, _, _, err := BuildKoanf(cmd, path); err != nil {
		t.Fatalf("BuildKoanf: %v", err)
	}
	logs := buf.String()
	if !strings.Contains(logs, "extra_args 被忽略") {
		t.Errorf("expected warn about extra_args being dropped, got logs:\n%s", logs)
	}
	if !strings.Contains(logs, "agent=opencode") {
		t.Errorf("expected agent=opencode in warn attrs, got:\n%s", logs)
	}
}

// TestMergeBuiltinAgents_NoWarnWhenTokenPresent: user supplied a full args
// override that DOES include `{extra_args}` — no warn, and extra_args flow
// through to the resulting config untouched.
func TestMergeBuiltinAgents_NoWarnWhenTokenPresent(t *testing.T) {
	clearWorkerEnv(t)
	var buf bytes.Buffer
	origDefault := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(origDefault)

	dir := t.TempDir()
	path := filepath.Join(dir, "worker.yaml")
	body := `
count: 1
providers: [opencode]
agents:
  opencode:
    command: opencode
    args: ["run", "--pure", "{extra_args}", "{prompt}"]
    extra_args: ["-m", "opencode/claude-opus-4-7"]
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cmd := newTestCmd(t)
	if err := cmd.ParseFlags(nil); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	cfg, _, _, _, err := BuildKoanf(cmd, path)
	if err != nil {
		t.Fatalf("BuildKoanf: %v", err)
	}
	if strings.Contains(buf.String(), "extra_args 被忽略") {
		t.Errorf("unexpected warn when {extra_args} token present:\n%s", buf.String())
	}
	if got := cfg.Agents["opencode"].ExtraArgs; !reflect.DeepEqual(got, []string{"-m", "opencode/claude-opus-4-7"}) {
		t.Errorf("ExtraArgs = %v", got)
	}
}

func TestValidate_FlagSetsWorkerCount(t *testing.T) {
	cfg := &Config{}
	ApplyDefaults(cfg)
	cfg.Count = 0
	if err := Validate(cfg); err == nil {
		t.Error("expected error for count < 1")
	}
}

func TestValidate_OK(t *testing.T) {
	cfg := &Config{}
	ApplyDefaults(cfg)
	if err := Validate(cfg); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}
