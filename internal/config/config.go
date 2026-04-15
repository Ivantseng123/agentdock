package config

import (
	"log/slog"
	"os"
	"strings"
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
	Providers         []string                 `yaml:"providers"`
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
	Logging           LoggingConfig            `yaml:"logging"`
	Queue             QueueConfig              `yaml:"queue"`
	ChannelPriority   map[string]int           `yaml:"channel_priority"`
	Workers           WorkersConfig            `yaml:"workers"`
	Attachments       AttachmentsConfig        `yaml:"attachments"`
	Redis             RedisConfig              `yaml:"redis"`
	SkillsConfig      string                   `yaml:"skills_config"`
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
	Command  string        `yaml:"command"`
	Args     []string      `yaml:"args"`
	Timeout  time.Duration `yaml:"timeout"`
	SkillDir string        `yaml:"skill_dir"`
	Stream   bool          `yaml:"stream"`
}

type QueueConfig struct {
	Capacity         int           `yaml:"capacity"`
	Transport        string        `yaml:"transport"`
	JobTimeout       time.Duration `yaml:"job_timeout"`
	AgentIdleTimeout time.Duration `yaml:"agent_idle_timeout"`
	PrepareTimeout   time.Duration `yaml:"prepare_timeout"`
	StatusInterval   time.Duration `yaml:"status_interval"`
}

type WorkersConfig struct {
	Count int `yaml:"count"`
}

type AttachmentsConfig struct {
	Store   string        `yaml:"store"`
	TempDir string        `yaml:"temp_dir"`
	TTL     time.Duration `yaml:"ttl"`
}

type RedisConfig struct {
	Addr     string `yaml:"addr"`
	Password string `yaml:"password"`
	DB       int    `yaml:"db"`
	TLS      bool   `yaml:"tls"`
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

type LoggingConfig struct {
	Dir            string `yaml:"dir"`
	Level          string `yaml:"level"`
	RetentionDays  int    `yaml:"retention_days"`
	AgentOutputDir string `yaml:"agent_output_dir"`
}

func applyDefaults(cfg *Config) {
	// Workers.Count must be resolved before MaxConcurrent gets its own default,
	// so that we can distinguish "user set max_concurrent" from "default applied".
	if cfg.Workers.Count <= 0 {
		if cfg.MaxConcurrent > 0 {
			cfg.Workers.Count = cfg.MaxConcurrent
			slog.Warn("max_concurrent is deprecated, use workers.count instead")
		} else {
			cfg.Workers.Count = 3
		}
	}

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
	if cfg.Logging.Dir == "" {
		cfg.Logging.Dir = "logs"
	}
	if cfg.Logging.Level == "" {
		cfg.Logging.Level = "debug"
	}
	if cfg.Logging.RetentionDays <= 0 {
		cfg.Logging.RetentionDays = 30
	}
	if cfg.Logging.AgentOutputDir == "" {
		cfg.Logging.AgentOutputDir = "logs/agent-outputs"
	}
	if cfg.Queue.Capacity <= 0 {
		cfg.Queue.Capacity = 50
	}
	if cfg.Queue.Transport == "" {
		cfg.Queue.Transport = "inmem"
	}
	if cfg.ChannelPriority == nil {
		cfg.ChannelPriority = map[string]int{"default": 50}
	}
	if cfg.Queue.JobTimeout <= 0 {
		cfg.Queue.JobTimeout = 20 * time.Minute
	}
	if cfg.Queue.AgentIdleTimeout <= 0 {
		cfg.Queue.AgentIdleTimeout = 5 * time.Minute
	}
	if cfg.Queue.PrepareTimeout <= 0 {
		cfg.Queue.PrepareTimeout = 3 * time.Minute
	}
	if cfg.Queue.StatusInterval <= 0 {
		cfg.Queue.StatusInterval = 5 * time.Second
	}
	if cfg.Attachments.TempDir == "" {
		cfg.Attachments.TempDir = "/tmp/triage-attachments"
	}
	if cfg.Attachments.TTL <= 0 {
		cfg.Attachments.TTL = 30 * time.Minute
	}
}

// DefaultsMap returns a koanf-friendly map[string]any of all default values
// produced by applyDefaults. Round-trips via YAML to preserve nested struct
// shape and yaml tags. applyDefaults is the single source of truth; this is
// just a different representation for the koanf provider chain (D12).
func DefaultsMap() map[string]any {
	var cfg Config
	applyDefaults(&cfg)
	data, err := yaml.Marshal(&cfg)
	if err != nil {
		panic("DefaultsMap marshal: " + err.Error())
	}
	out := map[string]any{}
	if err := yaml.Unmarshal(data, &out); err != nil {
		panic("DefaultsMap unmarshal: " + err.Error())
	}
	return out
}

// EnvOverrideMap returns a koanf-friendly map[string]any of values currently
// set in env vars. Maps each known env var to its koanf path. Unset env vars
// are absent from the result. Used by cmd/agentdock to build the env layer in
// the koanf provider chain (D1: env is its own layer, not persisted).
func EnvOverrideMap() map[string]any {
	out := map[string]any{}
	if v := os.Getenv("SLACK_BOT_TOKEN"); v != "" {
		out["slack.bot_token"] = v
	}
	if v := os.Getenv("SLACK_APP_TOKEN"); v != "" {
		out["slack.app_token"] = v
	}
	if v := os.Getenv("GITHUB_TOKEN"); v != "" {
		out["github.token"] = v
	}
	if v := os.Getenv("MANTIS_API_TOKEN"); v != "" {
		out["mantis.api_token"] = v
	}
	if v := os.Getenv("REDIS_ADDR"); v != "" {
		out["redis.addr"] = v
	}
	if v := os.Getenv("REDIS_PASSWORD"); v != "" {
		out["redis.password"] = v
	}
	if v := os.Getenv("ACTIVE_AGENT"); v != "" {
		out["active_agent"] = v
	}
	if v := os.Getenv("PROVIDERS"); v != "" {
		var providers []string
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				providers = append(providers, p)
			}
		}
		if len(providers) > 0 {
			out["providers"] = providers
		}
	}
	return out
}

