package config

import (
	"os"
	"path/filepath"
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
