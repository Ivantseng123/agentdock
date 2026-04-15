package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentdock/internal/config"

	"github.com/spf13/cobra"
)

// newFlagCmd returns a fresh cobra.Command with the same persistent+app flag
// set the real CLI uses, so we can drive buildKoanf with explicit flag values.
func newFlagCmd(t *testing.T) *cobra.Command {
	t.Helper()
	root := &cobra.Command{Use: "root"}
	addPersistentFlags(root)
	app := &cobra.Command{Use: "app", RunE: func(*cobra.Command, []string) error { return nil }}
	app.Flags().StringP("config", "c", "", "path to config file")
	addAppFlags(app)
	root.AddCommand(app)
	return app
}

// clearEnv unsets every env var EnvOverrideMap reads from, so tests stay
// isolated regardless of host environment.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"SLACK_BOT_TOKEN", "SLACK_APP_TOKEN", "GITHUB_TOKEN",
		"MANTIS_API_TOKEN", "REDIS_ADDR", "REDIS_PASSWORD",
		"ACTIVE_AGENT", "PROVIDERS",
	} {
		t.Setenv(k, "")
	}
}

func TestBuildKoanf_DefaultsLayer(t *testing.T) {
	clearEnv(t)
	cmd := newFlagCmd(t)
	cmd.SetArgs([]string{})
	if err := cmd.ParseFlags(nil); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}

	cfg, kEff, _, _, err := buildKoanf(cmd, "")
	if err != nil {
		t.Fatalf("buildKoanf: %v", err)
	}
	if cfg.Workers.Count != 3 {
		t.Errorf("Workers.Count = %d, want 3", cfg.Workers.Count)
	}
	if cfg.Queue.Transport != "inmem" {
		t.Errorf("Queue.Transport = %q, want inmem", cfg.Queue.Transport)
	}
	if got := kEff.Int("workers.count"); got != 3 {
		t.Errorf("kEff workers.count = %d, want 3", got)
	}
}

func TestBuildKoanf_FileLayerOverridesDefaults(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.yaml")
	if err := os.WriteFile(path, []byte("workers:\n  count: 7\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cmd := newFlagCmd(t)
	if err := cmd.ParseFlags(nil); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	cfg, _, _, delta, err := buildKoanf(cmd, path)
	if err != nil {
		t.Fatalf("buildKoanf: %v", err)
	}
	if !delta.FileExisted {
		t.Error("FileExisted should be true")
	}
	if cfg.Workers.Count != 7 {
		t.Errorf("Workers.Count = %d, want 7", cfg.Workers.Count)
	}
}

func TestBuildKoanf_EnvLayerOverridesFile(t *testing.T) {
	clearEnv(t)
	t.Setenv("REDIS_ADDR", "10.0.0.1:6379")

	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.yaml")
	if err := os.WriteFile(path, []byte("redis:\n  addr: 127.0.0.1:6379\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cmd := newFlagCmd(t)
	if err := cmd.ParseFlags(nil); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}

	cfg, kEff, kSave, _, err := buildKoanf(cmd, path)
	if err != nil {
		t.Fatalf("buildKoanf: %v", err)
	}
	if cfg.Redis.Addr != "10.0.0.1:6379" {
		t.Errorf("cfg.Redis.Addr = %q, want 10.0.0.1:6379", cfg.Redis.Addr)
	}
	if got := kEff.String("redis.addr"); got != "10.0.0.1:6379" {
		t.Errorf("kEff redis.addr = %q, want env value", got)
	}
	if got := kSave.String("redis.addr"); got != "127.0.0.1:6379" {
		t.Errorf("kSave redis.addr = %q, want file value (env must not bleed)", got)
	}
}

func TestBuildKoanf_FlagLayerOverridesEverything(t *testing.T) {
	clearEnv(t)
	t.Setenv("REDIS_ADDR", "10.0.0.1:6379")

	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.yaml")
	if err := os.WriteFile(path, []byte("redis:\n  addr: 127.0.0.1:6379\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cmd := newFlagCmd(t)
	if err := cmd.ParseFlags([]string{"--redis-addr", "192.168.1.1:6379"}); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}

	cfg, _, kSave, delta, err := buildKoanf(cmd, path)
	if err != nil {
		t.Fatalf("buildKoanf: %v", err)
	}
	if cfg.Redis.Addr != "192.168.1.1:6379" {
		t.Errorf("cfg.Redis.Addr = %q, want flag value", cfg.Redis.Addr)
	}
	if got := kSave.String("redis.addr"); got != "192.168.1.1:6379" {
		t.Errorf("kSave redis.addr = %q, want flag value", got)
	}
	if !delta.HadFlagOverride {
		t.Error("HadFlagOverride should be true")
	}
}

func TestMergeBuiltinAgents_FillsMissing(t *testing.T) {
	cfg := &config.Config{
		Agents: map[string]config.AgentConfig{
			"claude": {Command: "/custom/claude", Args: []string{"hello"}},
		},
	}
	mergeBuiltinAgents(cfg)

	claude := cfg.Agents["claude"]
	if claude.Command != "/custom/claude" {
		t.Errorf("claude.Command = %q, user override should win", claude.Command)
	}
	if _, ok := cfg.Agents["codex"]; !ok {
		t.Error("codex should be filled from BuiltinAgents")
	}
	if _, ok := cfg.Agents["opencode"]; !ok {
		t.Error("opencode should be filled from BuiltinAgents")
	}
}

func TestResolveConfigPath_DefaultsToHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir: %v", err)
	}
	got, err := resolveConfigPath("")
	if err != nil {
		t.Fatalf("resolveConfigPath: %v", err)
	}
	want := filepath.Join(home, ".config/agentdock/config.yaml")
	if got != want {
		t.Errorf("resolveConfigPath(\"\") = %q, want %q", got, want)
	}
}

func TestSaveConfig_NoDelta_NoWrite(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("workers:\n  count: 7\n"), 0600); err != nil {
		t.Fatal(err)
	}

	cmd := &cobra.Command{Use: "test"}
	addPersistentFlags(cmd)
	_, _, kSave, delta, _ := buildKoanf(cmd, path)

	written, err := saveConfig(kSave, path, map[string]any{}, delta)
	if err != nil {
		t.Fatal(err)
	}
	if written {
		t.Error("saveConfig should skip when no delta")
	}
}

func TestSaveConfig_FlagOverride_Writes(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("workers:\n  count: 7\n"), 0600); err != nil {
		t.Fatal(err)
	}

	cmd := &cobra.Command{Use: "test"}
	addPersistentFlags(cmd)
	if err := cmd.ParseFlags([]string{"--workers=5"}); err != nil {
		t.Fatal(err)
	}
	_, _, kSave, delta, _ := buildKoanf(cmd, path)

	written, err := saveConfig(kSave, path, map[string]any{}, delta)
	if err != nil {
		t.Fatal(err)
	}
	if !written {
		t.Error("saveConfig should write when flag override present")
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "count: 5") {
		t.Errorf("file should contain count: 5, got: %s", data)
	}
}

func TestSaveConfig_PreflightPrompt_Writes(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("workers:\n  count: 3\n"), 0600); err != nil {
		t.Fatal(err)
	}

	cmd := &cobra.Command{Use: "test"}
	addPersistentFlags(cmd)
	_, _, kSave, delta, _ := buildKoanf(cmd, path)

	prompted := map[string]any{"redis.addr": "10.0.0.1:6379"}
	written, err := saveConfig(kSave, path, prompted, delta)
	if err != nil {
		t.Fatal(err)
	}
	if !written {
		t.Error("saveConfig should write when preflight prompted")
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "10.0.0.1:6379") {
		t.Errorf("file should contain 10.0.0.1:6379, got: %s", data)
	}
}

func TestSaveConfig_Chmod0600(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	cmd := &cobra.Command{Use: "test"}
	addPersistentFlags(cmd)
	_, _, kSave, delta, _ := buildKoanf(cmd, path)

	delta.FileExisted = false
	if _, err := saveConfig(kSave, path, map[string]any{}, delta); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("mode = %o, want 0600", info.Mode().Perm())
	}
}

func TestResolveConfigPath_ExpandsTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir: %v", err)
	}
	got, err := resolveConfigPath("~/foo.yaml")
	if err != nil {
		t.Fatalf("resolveConfigPath: %v", err)
	}
	want := filepath.Join(home, "foo.yaml")
	if got != want {
		t.Errorf("resolveConfigPath(~/foo.yaml) = %q, want %q", got, want)
	}
	if !strings.HasPrefix(got, home) {
		t.Errorf("resolved path should start with home: %q", got)
	}
}
