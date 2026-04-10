package config

import (
	"os"
	"testing"
	"time"
)

func TestLoadConfig_V2(t *testing.T) {
	yaml := `
slack:
  bot_token: xoxb-test
  app_token: xapp-test

github:
  token: ghp-test

agents:
  claude:
    command: claude
    args: ["--print", "-p", "{prompt}"]
    timeout: 5m
  opencode:
    command: opencode
    args: ["--prompt", "{prompt}"]
    timeout: 3m

active_agent: claude
fallback: [claude, opencode]

prompt:
  language: zh-TW
  extra_rules:
    - "rule one"
    - "rule two"

channels:
  C123:
    repos: [owner/repo-a]
    default_labels: [from-slack]
    branch_select: true

channel_defaults:
  default_labels: [default-label]

auto_bind: true

max_concurrent: 5
max_thread_messages: 30

rate_limit:
  per_user: 10
  per_channel: 20
  window: 2m

semaphore_timeout: 45s

mantis:
  base_url: https://mantis.example.com
  api_token: mantis-token

repo_cache:
  dir: /tmp/repos
  max_age: 12h
`
	f, _ := os.CreateTemp("", "config-*.yaml")
	f.WriteString(yaml)
	f.Close()
	defer os.Remove(f.Name())

	cfg, err := Load(f.Name())
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// Slack
	if cfg.Slack.BotToken != "xoxb-test" {
		t.Errorf("bot_token = %q", cfg.Slack.BotToken)
	}
	if cfg.Slack.AppToken != "xapp-test" {
		t.Errorf("app_token = %q", cfg.Slack.AppToken)
	}

	// Agents
	if len(cfg.Agents) != 2 {
		t.Fatalf("agents count = %d", len(cfg.Agents))
	}
	claude := cfg.Agents["claude"]
	if claude.Command != "claude" {
		t.Errorf("claude command = %q", claude.Command)
	}
	if claude.Timeout != 5*time.Minute {
		t.Errorf("claude timeout = %v", claude.Timeout)
	}
	if len(claude.Args) != 3 {
		t.Errorf("claude args = %v", claude.Args)
	}

	if cfg.ActiveAgent != "claude" {
		t.Errorf("active_agent = %q", cfg.ActiveAgent)
	}
	if len(cfg.Fallback) != 2 || cfg.Fallback[0] != "claude" {
		t.Errorf("fallback = %v", cfg.Fallback)
	}

	// Prompt
	if cfg.Prompt.Language != "zh-TW" {
		t.Errorf("language = %q", cfg.Prompt.Language)
	}
	if len(cfg.Prompt.ExtraRules) != 2 {
		t.Errorf("extra_rules = %v", cfg.Prompt.ExtraRules)
	}

	// Channel
	ch, ok := cfg.Channels["C123"]
	if !ok {
		t.Fatal("channel C123 not found")
	}
	if repos := ch.GetRepos(); len(repos) != 1 || repos[0] != "owner/repo-a" {
		t.Errorf("repos = %v", repos)
	}
	if !ch.IsBranchSelectEnabled() {
		t.Error("branch_select should be true")
	}

	// Concurrency
	if cfg.MaxConcurrent != 5 {
		t.Errorf("max_concurrent = %d", cfg.MaxConcurrent)
	}
	if cfg.MaxThreadMessages != 30 {
		t.Errorf("max_thread_messages = %d", cfg.MaxThreadMessages)
	}
	if cfg.SemaphoreTimeout != 45*time.Second {
		t.Errorf("semaphore_timeout = %v", cfg.SemaphoreTimeout)
	}

	// Rate limit
	if cfg.RateLimit.PerUser != 10 {
		t.Errorf("per_user = %d", cfg.RateLimit.PerUser)
	}
	if cfg.RateLimit.Window != 2*time.Minute {
		t.Errorf("window = %v", cfg.RateLimit.Window)
	}

	// Mantis (top-level)
	if cfg.Mantis.BaseURL != "https://mantis.example.com" {
		t.Errorf("mantis base_url = %q", cfg.Mantis.BaseURL)
	}
	if cfg.Mantis.APIToken != "mantis-token" {
		t.Errorf("mantis api_token = %q", cfg.Mantis.APIToken)
	}

	// Repo cache
	if cfg.RepoCache.Dir != "/tmp/repos" {
		t.Errorf("repo_cache dir = %q", cfg.RepoCache.Dir)
	}
	if cfg.RepoCache.MaxAge != 12*time.Hour {
		t.Errorf("repo_cache max_age = %v", cfg.RepoCache.MaxAge)
	}
}

func TestLoadConfig_V1Warning(t *testing.T) {
	yaml := `
slack:
  bot_token: xoxb-test
  app_token: xapp-test
github:
  token: ghp-test
reactions:
  bug:
    type: bug
agents:
  claude:
    command: claude
    args: ["--print", "-p", "{prompt}"]
    timeout: 5m
active_agent: claude
`
	f, _ := os.CreateTemp("", "config-*.yaml")
	f.WriteString(yaml)
	f.Close()
	defer os.Remove(f.Name())

	cfg, err := Load(f.Name())
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.ActiveAgent != "claude" {
		t.Errorf("active_agent = %q", cfg.ActiveAgent)
	}
}

func loadFromString(t *testing.T, yamlContent string) *Config {
	t.Helper()
	tmpFile, err := os.CreateTemp("", "config-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.WriteString(yamlContent)
	tmpFile.Close()
	cfg, err := Load(tmpFile.Name())
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func writeAndLoad(t *testing.T, yamlContent string) *Config {
	t.Helper()
	f, err := os.CreateTemp("", "config-*.yaml")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	if _, err := f.WriteString(yamlContent); err != nil {
		t.Fatalf("WriteString: %v", err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })

	cfg, err := Load(f.Name())
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	return cfg
}

func TestLoggingConfigDefaults(t *testing.T) {
	cfg := writeAndLoad(t, `
slack:
  bot_token: "xoxb-test"
  app_token: "xapp-test"
`)
	if cfg.Logging.Dir != "logs" {
		t.Errorf("Logging.Dir = %q, want %q", cfg.Logging.Dir, "logs")
	}
	if cfg.Logging.Level != "debug" {
		t.Errorf("Logging.Level = %q, want %q", cfg.Logging.Level, "debug")
	}
	if cfg.Logging.RetentionDays != 30 {
		t.Errorf("Logging.RetentionDays = %d, want 30", cfg.Logging.RetentionDays)
	}
	if cfg.Logging.AgentOutputDir != "logs/agent-outputs" {
		t.Errorf("Logging.AgentOutputDir = %q, want %q", cfg.Logging.AgentOutputDir, "logs/agent-outputs")
	}
}

func TestLoadConfig_Defaults(t *testing.T) {
	yaml := `
slack:
  bot_token: xoxb-test
  app_token: xapp-test
github:
  token: ghp-test
agents:
  claude:
    command: claude
    args: ["--print", "-p", "{prompt}"]
active_agent: claude
`
	f, _ := os.CreateTemp("", "config-*.yaml")
	f.WriteString(yaml)
	f.Close()
	defer os.Remove(f.Name())

	cfg, err := Load(f.Name())
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.MaxConcurrent != 3 {
		t.Errorf("default max_concurrent = %d, want 3", cfg.MaxConcurrent)
	}
	if cfg.MaxThreadMessages != 50 {
		t.Errorf("default max_thread_messages = %d, want 50", cfg.MaxThreadMessages)
	}
	if cfg.SemaphoreTimeout != 30*time.Second {
		t.Errorf("default semaphore_timeout = %v, want 30s", cfg.SemaphoreTimeout)
	}
	if cfg.RateLimit.Window != time.Minute {
		t.Errorf("default rate_limit.window = %v, want 1m", cfg.RateLimit.Window)
	}
	claude := cfg.Agents["claude"]
	if claude.Timeout != 5*time.Minute {
		t.Errorf("default agent timeout = %v, want 5m", claude.Timeout)
	}
}

func TestLoad_QueueConfig(t *testing.T) {
	yaml := `
queue:
  capacity: 100
  transport: inmem
channel_priority:
  C_INCIDENTS: 100
  C_ONCALL: 80
workers:
  count: 5
attachments:
  store: local
  temp_dir: /tmp/test-attach
  ttl: 15m
agents:
  claude:
    command: claude
    args: ["--print"]
    skill_dir: ".claude/skills"
`
	cfg := loadFromString(t, yaml)
	if cfg.Queue.Capacity != 100 {
		t.Errorf("queue capacity = %d, want 100", cfg.Queue.Capacity)
	}
	if cfg.Workers.Count != 5 {
		t.Errorf("workers count = %d, want 5", cfg.Workers.Count)
	}
	pri, ok := cfg.ChannelPriority["C_INCIDENTS"]
	if !ok || pri != 100 {
		t.Errorf("channel priority = %d, want 100", pri)
	}
	agent := cfg.Agents["claude"]
	if agent.SkillDir != ".claude/skills" {
		t.Errorf("skill_dir = %q", agent.SkillDir)
	}
	if cfg.Attachments.TempDir != "/tmp/test-attach" {
		t.Errorf("temp_dir = %q", cfg.Attachments.TempDir)
	}
	if cfg.Attachments.TTL != 15*time.Minute {
		t.Errorf("ttl = %v", cfg.Attachments.TTL)
	}
}

func TestLoad_QueueDefaults(t *testing.T) {
	yaml := `
agents:
  claude:
    command: claude
`
	cfg := loadFromString(t, yaml)
	if cfg.Queue.Capacity != 50 {
		t.Errorf("default queue capacity = %d, want 50", cfg.Queue.Capacity)
	}
	if cfg.Workers.Count != 3 {
		t.Errorf("default workers count = %d, want 3", cfg.Workers.Count)
	}
}

func TestLoad_MaxConcurrentBackwardCompat(t *testing.T) {
	yaml := `
max_concurrent: 7
agents:
  claude:
    command: claude
`
	cfg := loadFromString(t, yaml)
	if cfg.Workers.Count != 7 {
		t.Errorf("workers count = %d, want 7 (from max_concurrent)", cfg.Workers.Count)
	}
}
