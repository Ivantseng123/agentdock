package config

import (
	"log/slog"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	LogLevel          string                   `yaml:"log_level"`
	Server            ServerConfig             `yaml:"server"`
	Slack             SlackConfig              `yaml:"slack"`
	GitHub            GitHubConfig             `yaml:"github"`
	Agents            map[string]AgentConfig   `yaml:"agents"`
	ActiveAgent       string                   `yaml:"active_agent"`
	Fallback          []string                 `yaml:"fallback"`
	Prompt            PromptConfig             `yaml:"prompt"`
	Channels          map[string]ChannelConfig `yaml:"channels"`
	ChannelDefaults   ChannelConfig            `yaml:"channel_defaults"`
	AutoBind          bool                     `yaml:"auto_bind"`
	MaxConcurrent     int                      `yaml:"max_concurrent"`
	MaxThreadMessages int                      `yaml:"max_thread_messages"`
	SemaphoreTimeout  time.Duration            `yaml:"semaphore_timeout"`
	RateLimit         RateLimitConfig          `yaml:"rate_limit"`
	Mantis            MantisConfig             `yaml:"mantis"`
	RepoCache         RepoCacheConfig          `yaml:"repo_cache"`
}

type ServerConfig struct {
	Port int `yaml:"port"`
}

type SlackConfig struct {
	BotToken string `yaml:"bot_token"`
	AppToken string `yaml:"app_token"`
}

type GitHubConfig struct {
	Token string `yaml:"token"`
}

type AgentConfig struct {
	Command string        `yaml:"command"`
	Args    []string      `yaml:"args"`
	Timeout time.Duration `yaml:"timeout"`
}

type PromptConfig struct {
	Language   string   `yaml:"language"`
	ExtraRules []string `yaml:"extra_rules"`
}

type ChannelConfig struct {
	Repo          string   `yaml:"repo"`
	Repos         []string `yaml:"repos"`
	DefaultLabels []string `yaml:"default_labels"`
	Branches      []string `yaml:"branches"`
	BranchSelect  *bool    `yaml:"branch_select"`
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

type RateLimitConfig struct {
	PerUser    int           `yaml:"per_user"`
	PerChannel int           `yaml:"per_channel"`
	Window     time.Duration `yaml:"window"`
}

type MantisConfig struct {
	BaseURL  string `yaml:"base_url"`
	APIToken string `yaml:"api_token"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

type RepoCacheConfig struct {
	Dir    string        `yaml:"dir"`
	MaxAge time.Duration `yaml:"max_age"`
}

type v1RawCheck struct {
	Reactions    map[string]any `yaml:"reactions"`
	Integrations map[string]any `yaml:"integrations"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var raw v1RawCheck
	if yaml.Unmarshal(data, &raw) == nil {
		if raw.Reactions != nil || raw.Integrations != nil {
			slog.Warn("v1 config detected — reactions, llm, diagnosis, and integrations sections are no longer used in v2. Note: integrations.mantis has moved to top-level mantis.")
		}
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	applyDefaults(&cfg)
	applyEnvOverrides(&cfg)
	return &cfg, nil
}

func applyDefaults(cfg *Config) {
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 3
	}
	if cfg.MaxThreadMessages <= 0 {
		cfg.MaxThreadMessages = 50
	}
	if cfg.SemaphoreTimeout <= 0 {
		cfg.SemaphoreTimeout = 30 * time.Second
	}
	if cfg.RateLimit.Window <= 0 {
		cfg.RateLimit.Window = time.Minute
	}
	for name, agent := range cfg.Agents {
		if agent.Timeout <= 0 {
			agent.Timeout = 5 * time.Minute
			cfg.Agents[name] = agent
		}
	}
}

func applyEnvOverrides(cfg *Config) {
	if v := os.Getenv("SLACK_BOT_TOKEN"); v != "" {
		cfg.Slack.BotToken = v
	}
	if v := os.Getenv("SLACK_APP_TOKEN"); v != "" {
		cfg.Slack.AppToken = v
	}
	if v := os.Getenv("GITHUB_TOKEN"); v != "" {
		cfg.GitHub.Token = v
	}
	if v := os.Getenv("MANTIS_API_TOKEN"); v != "" {
		cfg.Mantis.APIToken = v
	}
}
