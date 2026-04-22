// Package config holds the app module's yaml-backed configuration. App and
// worker declare their config types separately; shared yaml-tagged types are
// intentionally NOT extracted to shared/ in order to keep each module's
// schema free to evolve independently.
package config

import "time"

// Config is the app module's yaml-backed configuration.
type Config struct {
	LogLevel          string                   `yaml:"log_level"`
	Server            ServerConfig             `yaml:"server"`
	Slack             SlackConfig              `yaml:"slack"`
	GitHub            GitHubConfig             `yaml:"github"`
	Channels          map[string]ChannelConfig `yaml:"channels"`
	ChannelDefaults   ChannelConfig            `yaml:"channel_defaults"`
	AutoBind          bool                     `yaml:"auto_bind"`
	MaxThreadMessages int                      `yaml:"max_thread_messages"`
	SemaphoreTimeout  time.Duration            `yaml:"semaphore_timeout"`
	RateLimit         RateLimitConfig          `yaml:"rate_limit"`
	Mantis            MantisConfig             `yaml:"mantis"`
	ChannelPriority   map[string]int           `yaml:"channel_priority"`
	Prompt            PromptConfig             `yaml:"prompt"`
	SkillsConfig      string                   `yaml:"skills_config"`
	Attachments       AttachmentsConfig        `yaml:"attachments"`
	RepoCache         RepoCacheConfig          `yaml:"repo_cache"`
	Queue             QueueConfig              `yaml:"queue"`
	Logging           LoggingConfig            `yaml:"logging"`
	Redis             RedisConfig              `yaml:"redis"`
	SecretKey         string                   `yaml:"secret_key"`
	Secrets           map[string]string        `yaml:"secrets"`
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

// PromptConfig is the app-owned prompt assembly. Worker holds ExtraRules
// separately (worker/config.PromptConfig); app controls whether worker rules
// are allowed via AllowWorkerRules.
type PromptConfig struct {
	Language         string   `yaml:"language"`
	Goal             string   `yaml:"goal"`
	OutputRules      []string `yaml:"output_rules"`
	AllowWorkerRules *bool    `yaml:"allow_worker_rules"`
}

// IsWorkerRulesAllowed returns whether worker-side ExtraRules should be
// rendered into the prompt. Nil pointer is treated as true (default) so
// callers don't have to duplicate the ApplyDefaults invariant.
func (p PromptConfig) IsWorkerRulesAllowed() bool {
	return p.AllowWorkerRules == nil || *p.AllowWorkerRules
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

type QueueConfig struct {
	Capacity         int           `yaml:"capacity"`
	Transport        string        `yaml:"transport"`
	JobTimeout       time.Duration `yaml:"job_timeout"`
	AgentIdleTimeout time.Duration `yaml:"agent_idle_timeout"`
	PrepareTimeout   time.Duration `yaml:"prepare_timeout"`
	CancelTimeout    time.Duration `yaml:"cancel_timeout"`
	StatusInterval   time.Duration `yaml:"status_interval"`
}

// defaultPromptGoal is the hardcoded Goal applied when the operator hasn't
// set one in YAML.
const defaultPromptGoal = "Use the /triage-issue skill to investigate and produce a triage result."
