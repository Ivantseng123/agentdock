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

func clearAppEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"SLACK_BOT_TOKEN", "SLACK_APP_TOKEN", "GITHUB_TOKEN",
		"GITHUB_APP_APP_ID", "GITHUB_APP_INSTALLATION_ID", "GITHUB_APP_PRIVATE_KEY_PATH",
		"MANTIS_API_TOKEN", "REDIS_ADDR", "REDIS_PASSWORD", "SECRET_KEY",
	} {
		t.Setenv(k, "")
	}
}

func TestBuildKoanf_DefaultsLayer(t *testing.T) {
	clearAppEnv(t)
	cmd := newTestCmd(t)
	if err := cmd.ParseFlags(nil); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	cfg, _, _, _, err := BuildKoanf(cmd, "")
	if err != nil {
		t.Fatalf("BuildKoanf: %v", err)
	}
	if cfg.Queue.Transport != "redis" {
		t.Errorf("Queue.Transport = %q, want redis", cfg.Queue.Transport)
	}
	if cfg.MaxThreadMessages != 50 {
		t.Errorf("MaxThreadMessages = %d, want 50", cfg.MaxThreadMessages)
	}
}

func TestBuildKoanf_FileLayerOverridesDefaults(t *testing.T) {
	clearAppEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "app.yaml")
	if err := os.WriteFile(path, []byte("max_thread_messages: 7\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cmd := newTestCmd(t)
	if err := cmd.ParseFlags(nil); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	cfg, _, _, delta, err := BuildKoanf(cmd, path)
	if err != nil {
		t.Fatalf("BuildKoanf: %v", err)
	}
	if !delta.FileExisted {
		t.Error("FileExisted should be true")
	}
	if cfg.MaxThreadMessages != 7 {
		t.Errorf("MaxThreadMessages = %d, want 7", cfg.MaxThreadMessages)
	}
}

func TestBuildKoanf_EnvOverridesFile_SaveKeepsFile(t *testing.T) {
	clearAppEnv(t)
	t.Setenv("REDIS_ADDR", "10.0.0.1:6379")

	dir := t.TempDir()
	path := filepath.Join(dir, "app.yaml")
	if err := os.WriteFile(path, []byte("redis:\n  addr: 127.0.0.1:6379\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cmd := newTestCmd(t)
	if err := cmd.ParseFlags(nil); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	cfg, _, kSave, _, err := BuildKoanf(cmd, path)
	if err != nil {
		t.Fatalf("BuildKoanf: %v", err)
	}
	if cfg.Redis.Addr != "10.0.0.1:6379" {
		t.Errorf("cfg.Redis.Addr = %q, want env value", cfg.Redis.Addr)
	}
	if got := kSave.String("redis.addr"); got != "127.0.0.1:6379" {
		t.Errorf("kSave redis.addr = %q, want file value (env must not bleed)", got)
	}
}

func TestBuildKoanf_FlagOverridesEverything(t *testing.T) {
	clearAppEnv(t)
	t.Setenv("REDIS_ADDR", "10.0.0.1:6379")

	dir := t.TempDir()
	path := filepath.Join(dir, "app.yaml")
	if err := os.WriteFile(path, []byte("redis:\n  addr: 127.0.0.1:6379\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cmd := newTestCmd(t)
	if err := cmd.ParseFlags([]string{"--redis-addr", "192.168.1.1:6379"}); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	cfg, _, _, delta, err := BuildKoanf(cmd, path)
	if err != nil {
		t.Fatalf("BuildKoanf: %v", err)
	}
	if cfg.Redis.Addr != "192.168.1.1:6379" {
		t.Errorf("cfg.Redis.Addr = %q, want flag value", cfg.Redis.Addr)
	}
	if !delta.HadFlagOverride {
		t.Error("HadFlagOverride should be true")
	}
}

func TestValidate_MissingRepoCacheDir(t *testing.T) {
	cfg := &Config{}
	ApplyDefaults(cfg)
	cfg.RepoCache.Dir = ""
	if err := Validate(cfg); err == nil {
		t.Error("expected error for empty repo_cache.dir")
	}
}

func TestValidate_OK(t *testing.T) {
	cfg := &Config{}
	ApplyDefaults(cfg)
	if err := Validate(cfg); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}
