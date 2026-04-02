package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server          ServerConfig              `yaml:"server"`
	Slack           SlackConfig               `yaml:"slack"`
	Channels        map[string]ChannelConfig  `yaml:"channels"`
	ChannelDefaults ChannelConfig             `yaml:"channel_defaults"` // defaults for auto-bound channels
	AutoBind        bool                      `yaml:"auto_bind"`       // auto-register when bot joins a channel
	Reactions       map[string]ReactionConfig `yaml:"reactions"`
	GitHub          GitHubConfig              `yaml:"github"`
	LLM             LLMConfig                 `yaml:"llm"`
	RepoCache       RepoCacheConfig           `yaml:"repo_cache"`
	RateLimit       RateLimitConfig           `yaml:"rate_limit"`
	Diagnosis       DiagnosisConfig           `yaml:"diagnosis"`
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
	Repo          string   `yaml:"repo"`            // Single repo (backward compatible)
	Repos         []string `yaml:"repos"`           // Multiple repos
	DefaultLabels []string `yaml:"default_labels"`
	Branches      []string `yaml:"branches"`        // Whitelist of branches to show (empty = auto-detect)
	BranchSelect  *bool    `yaml:"branch_select"`   // Enable branch selection (default: false)
}

// IsBranchSelectEnabled returns whether branch selection is enabled.
func (c ChannelConfig) IsBranchSelectEnabled() bool {
	return c.BranchSelect != nil && *c.BranchSelect
}

// GetRepos returns the list of repos, handling both single and multi config.
func (c ChannelConfig) GetRepos() []string {
	if len(c.Repos) > 0 {
		return c.Repos
	}
	if c.Repo != "" {
		return []string{c.Repo}
	}
	return nil
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
}

type LLMProvider struct {
	Name       string        `yaml:"name"`
	APIKey     string        `yaml:"api_key"`
	Model      string        `yaml:"model"`
	BaseURL    string        `yaml:"base_url"`
	Command    string        `yaml:"command"`     // CLI provider: command to exec
	Args       []string      `yaml:"args"`        // CLI provider: extra args
	MaxRetries int           `yaml:"max_retries"` // Per-provider retry count (default 1)
	Timeout    time.Duration `yaml:"timeout"`     // Per-provider timeout (overrides global)
}

type RepoCacheConfig struct {
	Dir    string        `yaml:"dir"`
	MaxAge time.Duration `yaml:"max_age"`
}

type RateLimitConfig struct {
	PerUser    int           `yaml:"per_user"`    // Max triggers per user per window
	PerChannel int           `yaml:"per_channel"` // Max triggers per channel per window
	Window     time.Duration `yaml:"window"`      // Time window for rate limits
}

type DiagnosisConfig struct {
	Mode      string        `yaml:"mode"`       // "full" = use LLM, "lite" = grep only + handoff spec
	MaxTurns  int           `yaml:"max_turns"`  // Max agent loop iterations (default decided by engine)
	MaxTokens int           `yaml:"max_tokens"` // Max tokens per LLM call
	CacheTTL  time.Duration `yaml:"cache_ttl"`  // Response cache TTL (0 = no caching)
	Prompt    PromptConfig  `yaml:"prompt"`
}

type PromptConfig struct {
	Language    string   `yaml:"language"`     // Response language (e.g. "繁體中文", "English")
	ExtraRules  []string `yaml:"extra_rules"`  // Additional instructions appended to system prompt
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
