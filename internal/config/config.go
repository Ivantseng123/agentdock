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

// LoadDefaults creates a Config with sensible defaults + env overrides, no YAML file needed.
// Includes a default claude agent config so workers can run with just env vars.
func LoadDefaults() (*Config, error) {
	cfg := Config{
		Agents: map[string]AgentConfig{
			"claude": {
				Command:  "claude",
				Args:     []string{"--print", "--output-format", "stream-json", "-p", "{prompt}"},
				SkillDir: ".claude/skills",
				Stream:   true,
			},
			"codex": {
				Command:  "codex",
				Args:     []string{"--print", "--output-format", "stream-json", "-p", "{prompt}"},
				SkillDir: ".codex/skills",
				Stream:   true,
			},
			"opencode": {
				Command:  "opencode",
				Args:     []string{"--prompt", "{prompt}"},
				SkillDir: ".opencode/skills",
			},
		},
		Providers: []string{"claude"},
	}
	applyDefaults(&cfg)
	applyEnvOverrides(&cfg)
	return &cfg, nil
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
	if v := os.Getenv("REDIS_ADDR"); v != "" {
		cfg.Redis.Addr = v
	}
	if v := os.Getenv("REDIS_PASSWORD"); v != "" {
		cfg.Redis.Password = v
	}
	if v := os.Getenv("ACTIVE_AGENT"); v != "" {
		cfg.ActiveAgent = v
	}
	if v := os.Getenv("PROVIDERS"); v != "" {
		cfg.Providers = strings.Split(v, ",")
	}
}
