package config

import (
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func loadFromString(t *testing.T, yamlContent string) *Config {
	t.Helper()
	var cfg Config
	if err := yaml.Unmarshal([]byte(yamlContent), &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	ApplyDefaults(&cfg)
	return &cfg
}

func TestLoadConfig_AppFields(t *testing.T) {
	cfg := loadFromString(t, `
slack:
  bot_token: xoxb-test
  app_token: xapp-test
github:
  token: ghp-test
prompt:
  language: zh-TW
channels:
  C123:
    repos: [owner/repo-a]
channel_defaults:
  default_labels: [default-label]
auto_bind: true
max_thread_messages: 30
`)
	if cfg.Slack.BotToken != "xoxb-test" {
		t.Errorf("bot_token = %q", cfg.Slack.BotToken)
	}
	if cfg.Prompt.Language != "zh-TW" {
		t.Errorf("language = %q", cfg.Prompt.Language)
	}
	ch := cfg.Channels["C123"]
	if repos := ch.GetRepos(); len(repos) != 1 || repos[0] != "owner/repo-a" {
		t.Errorf("repos = %v", repos)
	}
	if cfg.MaxThreadMessages != 30 {
		t.Errorf("max_thread_messages = %d", cfg.MaxThreadMessages)
	}
}

func TestApplyDefaults_Timeouts(t *testing.T) {
	cfg := loadFromString(t, ``)
	if cfg.SemaphoreTimeout != 30*time.Second {
		t.Errorf("semaphore = %v", cfg.SemaphoreTimeout)
	}
	if cfg.Queue.JobTimeout != 20*time.Minute {
		t.Errorf("job_timeout = %v", cfg.Queue.JobTimeout)
	}
}

func TestApplyDefaults_PromptGoal(t *testing.T) {
	cfg := loadFromString(t, ``)
	if cfg.Prompt.Goal != defaultPromptGoal {
		t.Errorf("default Goal = %q", cfg.Prompt.Goal)
	}
}

func TestApplyDefaults_AllowWorkerRules(t *testing.T) {
	cfg := loadFromString(t, ``)
	if cfg.Prompt.AllowWorkerRules == nil || !*cfg.Prompt.AllowWorkerRules {
		t.Errorf("allow_worker_rules default = %v, want true", cfg.Prompt.AllowWorkerRules)
	}
}

func TestResolveSecrets_MergesGitHubToken(t *testing.T) {
	cfg := loadFromString(t, `
github:
  token: ghp-merge
`)
	if cfg.Secrets["GH_TOKEN"] != "ghp-merge" {
		t.Errorf("GH_TOKEN = %q, want ghp-merge", cfg.Secrets["GH_TOKEN"])
	}
}

func TestDefaultsMap_ShapeMatchesYAMLTags(t *testing.T) {
	m := DefaultsMap()
	if _, ok := m["queue"]; !ok {
		t.Error("DefaultsMap missing queue key")
	}
	q, _ := m["queue"].(map[string]any)
	if q["transport"] != "redis" {
		t.Errorf("queue.transport = %v, want redis", q["transport"])
	}
}
