package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server    ServerConfig              `yaml:"server"`
	Slack     SlackConfig               `yaml:"slack"`
	Channels  map[string]ChannelConfig  `yaml:"channels"`
	Reactions map[string]ReactionConfig `yaml:"reactions"`
	GitHub    GitHubConfig              `yaml:"github"`
	LLM       LLMConfig                `yaml:"llm"`
	RepoCache RepoCacheConfig           `yaml:"repo_cache"`
}

type ServerConfig struct {
	Port int `yaml:"port"`
}

type SlackConfig struct {
	BotToken      string `yaml:"bot_token"`
	SigningSecret string `yaml:"signing_secret"`
	AppToken      string `yaml:"app_token"`
}

type ChannelConfig struct {
	Repo          string   `yaml:"repo"`
	DefaultLabels []string `yaml:"default_labels"`
}

type ReactionConfig struct {
	Type             string   `yaml:"type"`
	IssueLabels      []string `yaml:"issue_labels"`
	IssueTitlePrefix string   `yaml:"issue_title_prefix"`
}

type GitHubConfig struct {
	Token string `yaml:"token"`
}

type LLMConfig struct {
	Providers  []LLMProvider `yaml:"providers"`
	Timeout    time.Duration `yaml:"timeout"`
	MaxRetries int           `yaml:"max_retries"`
}

type LLMProvider struct {
	Name    string `yaml:"name"`
	APIKey  string `yaml:"api_key"`
	Model   string `yaml:"model"`
	BaseURL string `yaml:"base_url"`
}

type RepoCacheConfig struct {
	Dir    string        `yaml:"dir"`
	MaxAge time.Duration `yaml:"max_age"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	applyEnvOverrides(&cfg)
	return &cfg, nil
}

func applyEnvOverrides(cfg *Config) {
	if v := os.Getenv("SLACK_BOT_TOKEN"); v != "" {
		cfg.Slack.BotToken = v
	}
	if v := os.Getenv("SLACK_SIGNING_SECRET"); v != "" {
		cfg.Slack.SigningSecret = v
	}
	if v := os.Getenv("SLACK_APP_TOKEN"); v != "" {
		cfg.Slack.AppToken = v
	}
	if v := os.Getenv("GITHUB_TOKEN"); v != "" {
		cfg.GitHub.Token = v
	}
	// LLM provider API keys: LLM_<NAME>_API_KEY
	for i, p := range cfg.LLM.Providers {
		envKey := fmt.Sprintf("LLM_%s_API_KEY", strings.ToUpper(p.Name))
		if v := os.Getenv(envKey); v != "" {
			cfg.LLM.Providers[i].APIKey = v
		}
	}
}
