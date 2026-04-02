package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	yaml := `
server:
  port: 9090

slack:
  bot_token: "xoxb-test"
  signing_secret: "secret"
  app_token: "xapp-test"

channels:
  C123:
    repo: "org/repo"
    default_labels: ["from-slack"]

reactions:
  bug:
    type: "bug"
    issue_labels: ["bug", "triage"]
    issue_title_prefix: "[Bug]"
  rocket:
    type: "feature"
    issue_labels: ["enhancement"]
    issue_title_prefix: "[Feature]"

github:
  token: "ghp-test"

llm:
  providers:
    - name: "claude"
      api_key: "sk-ant-test"
      model: "claude-sonnet-4-20250514"
      base_url: "https://api.anthropic.com"
  timeout: 30s
  max_retries: 2

repo_cache:
  dir: "/tmp/test-repos"
  max_age: 1h
`
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte(yaml), 0644)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.Server.Port != 9090 {
		t.Errorf("expected port 9090, got %d", cfg.Server.Port)
	}
	if cfg.Slack.BotToken != "xoxb-test" {
		t.Errorf("expected bot token xoxb-test, got %s", cfg.Slack.BotToken)
	}
	ch, ok := cfg.Channels["C123"]
	if !ok {
		t.Fatal("expected channel C123")
	}
	if ch.Repo != "org/repo" {
		t.Errorf("expected repo org/repo, got %s", ch.Repo)
	}
	r, ok := cfg.Reactions["bug"]
	if !ok {
		t.Fatal("expected reaction bug")
	}
	if r.Type != "bug" {
		t.Errorf("expected type bug, got %s", r.Type)
	}
	if cfg.LLM.Providers[0].Name != "claude" {
		t.Errorf("expected provider claude, got %s", cfg.LLM.Providers[0].Name)
	}
}

func TestLoadConfig_EnvOverride(t *testing.T) {
	yaml := `
server:
  port: 8080
slack:
  bot_token: "from-yaml"
  signing_secret: "secret"
  app_token: "xapp-test"
channels: {}
reactions: {}
github:
  token: "from-yaml"
llm:
  providers: []
  timeout: 30s
  max_retries: 2
repo_cache:
  dir: "/tmp/repos"
  max_age: 1h
`
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte(yaml), 0644)

	t.Setenv("SLACK_BOT_TOKEN", "from-env")
	t.Setenv("GITHUB_TOKEN", "from-env-gh")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.Slack.BotToken != "from-env" {
		t.Errorf("expected env override from-env, got %s", cfg.Slack.BotToken)
	}
	if cfg.GitHub.Token != "from-env-gh" {
		t.Errorf("expected env override from-env-gh, got %s", cfg.GitHub.Token)
	}
}
