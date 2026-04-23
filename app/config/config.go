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
	PRReview          PRReviewConfig           `yaml:"pr_review"`
	SkillsConfig      string                   `yaml:"skills_config"`
	Attachments       AttachmentsConfig        `yaml:"attachments"`
	RepoCache         RepoCacheConfig          `yaml:"repo_cache"`
	Queue             QueueConfig              `yaml:"queue"`
	Availability      AvailabilityConfig       `yaml:"availability"`
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

// PromptConfig nests per-workflow goal / output rules. Legacy flat
// Goal / OutputRules are aliased at load time into Issue.* so pre-v2.2
// operators keep working.
type PromptConfig struct {
	Language         string `yaml:"language"`
	AllowWorkerRules *bool  `yaml:"allow_worker_rules"`

	// Legacy flat fields — at load time, these are copied into Issue.* if
	// Issue.* is unset. Operators may remove these from their yaml once they
	// migrate to the nested form.
	Goal        string   `yaml:"goal,omitempty"`
	OutputRules []string `yaml:"output_rules,omitempty"`

	// Per-workflow sections.
	Issue    WorkflowPromptConfig `yaml:"issue"`
	Ask      WorkflowPromptConfig `yaml:"ask"`
	PRReview WorkflowPromptConfig `yaml:"pr_review"`
}

// WorkflowPromptConfig holds one workflow's prompt knobs. All fields are
// optional at the yaml layer; ApplyDefaults fills gaps with hardcoded
// defaults so zero-config is valid.
//
// ResponseSchema carries the machine-readable output contract (marker + JSON
// shape). It is rendered verbatim — NOT xml-escaped — so literal `"` and
// `<` reach the LLM unencoded. Keep the schema text ASCII-safe where
// possible.
type WorkflowPromptConfig struct {
	Goal           string   `yaml:"goal"`
	ResponseSchema string   `yaml:"response_schema"`
	OutputRules    []string `yaml:"output_rules"`
}

// IsWorkerRulesAllowed returns whether worker-side ExtraRules should be
// rendered into the prompt. Nil pointer is treated as true (default) so
// callers don't have to duplicate the ApplyDefaults invariant.
func (p PromptConfig) IsWorkerRulesAllowed() bool {
	return p.AllowWorkerRules == nil || *p.AllowWorkerRules
}

// PRReviewConfig gates the PR Review workflow. **Enabled by default** —
// the github-pr-review skill and `agentdock pr-review-helper` subcommand
// are now baked into the release image, so opting in was just ceremony.
// Operators can still force it off with `pr_review.enabled: false`.
//
// Pointer-to-bool (not plain bool) lets us distinguish "unset" (→ default
// true) from "explicit false" (→ disabled). See IsEnabled() for the gate.
type PRReviewConfig struct {
	Enabled *bool `yaml:"enabled"`
}

// IsEnabled reports whether the PR Review workflow should run. Nil pointer
// is treated as enabled — the default ships on.
func (c PRReviewConfig) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
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

type AvailabilityConfig struct {
	AvgJobDuration time.Duration `yaml:"avg_job_duration"`
}
