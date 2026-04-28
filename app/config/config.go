// Package config holds the app module's yaml-backed configuration. App and
// worker declare their config types separately; shared yaml-tagged types are
// intentionally NOT extracted to shared/ in order to keep each module's
// schema free to evolve independently.
package config

import "time"

// Config is the app module's yaml-backed configuration.
//
// Workflow config lives under top-level `workflows:`, and cross-workflow
// prompt defaults under `prompt_defaults:`. Legacy `prompt:` / `pr_review:`
// top-level blocks are accepted as aliases (see migrateLegacy) — those
// fields remain on Config so unknown-key warners still recognise the legacy
// paths and mixed yaml (old + new) is handled deterministically.
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

	Workflows      WorkflowsConfig      `yaml:"workflows"`
	PromptDefaults PromptDefaultsConfig `yaml:"prompt_defaults"`

	// Legacy aliases. ApplyDefaults migrates these into Workflows /
	// PromptDefaults and then zeroes them so downstream marshalling only
	// emits the new shape. See docs/MIGRATION-v2.md.
	Prompt   LegacyPromptConfig   `yaml:"prompt,omitempty"`
	PRReview LegacyPRReviewConfig `yaml:"pr_review,omitempty"`

	SkillsConfig string             `yaml:"skills_config"`
	Attachments  AttachmentsConfig  `yaml:"attachments"`
	RepoCache    RepoCacheConfig    `yaml:"repo_cache"`
	Queue        QueueConfig        `yaml:"queue"`
	Availability AvailabilityConfig `yaml:"availability"`
	Logging      LoggingConfig      `yaml:"logging"`
	Redis        RedisConfig        `yaml:"redis"`
	SecretKey    string             `yaml:"secret_key"`
	Secrets      map[string]string  `yaml:"secrets"`
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

// WorkflowsConfig groups the three workflows (issue / ask / pr_review) under
// one top-level key. Fixed struct (not a map) because the set is closed —
// adding a new workflow type is a code change anyway.
type WorkflowsConfig struct {
	Issue    WorkflowConfig         `yaml:"issue"`
	Ask      WorkflowConfig         `yaml:"ask"`
	PRReview PRReviewWorkflowConfig `yaml:"pr_review"`
}

// WorkflowConfig is the per-workflow block. Currently only `prompt` lives
// here; future per-workflow feature flags should be added as siblings.
type WorkflowConfig struct {
	Prompt WorkflowPromptConfig `yaml:"prompt"`
}

// PRReviewWorkflowConfig is the pr_review workflow's block. Unlike Issue / Ask,
// it has a feature flag (`enabled`) because the workflow can be gated off.
type PRReviewWorkflowConfig struct {
	Enabled *bool                `yaml:"enabled"`
	Prompt  WorkflowPromptConfig `yaml:"prompt"`
}

// IsEnabled reports whether the PR Review workflow should run. Nil pointer
// is treated as enabled — the default ships on.
func (w PRReviewWorkflowConfig) IsEnabled() bool {
	return w.Enabled == nil || *w.Enabled
}

// PromptDefaultsConfig carries prompt knobs that apply across all workflows.
type PromptDefaultsConfig struct {
	Language         string `yaml:"language"`
	AllowWorkerRules *bool  `yaml:"allow_worker_rules"`
}

// IsWorkerRulesAllowed returns whether worker-side ExtraRules should be
// rendered into the prompt. Nil pointer is treated as true (default) so
// callers don't have to duplicate the ApplyDefaults invariant.
func (p PromptDefaultsConfig) IsWorkerRulesAllowed() bool {
	return p.AllowWorkerRules == nil || *p.AllowWorkerRules
}

// LegacyPromptConfig accepts the legacy top-level `prompt:` block shape.
// migrateLegacy copies its values into Workflows / PromptDefaults and zeroes
// it. See docs/MIGRATION-v2.md for the alias table.
type LegacyPromptConfig struct {
	Language         string `yaml:"language,omitempty"`
	AllowWorkerRules *bool  `yaml:"allow_worker_rules,omitempty"`

	// Old-A: flat goal/response_schema/output_rules under prompt:.
	Goal           string   `yaml:"goal,omitempty"`
	ResponseSchema string   `yaml:"response_schema,omitempty"`
	OutputRules    []string `yaml:"output_rules,omitempty"`

	// Old-B: per-workflow nesting under prompt:.
	Issue    WorkflowPromptConfig `yaml:"issue,omitempty"`
	Ask      WorkflowPromptConfig `yaml:"ask,omitempty"`
	PRReview WorkflowPromptConfig `yaml:"pr_review,omitempty"`
}

// IsZero reports whether this legacy block carries any operator-supplied
// data. An all-zero block is treated as absent so ApplyDefaults can skip
// migration logging.
func (l LegacyPromptConfig) IsZero() bool {
	return l.Language == "" && l.AllowWorkerRules == nil &&
		l.Goal == "" && l.ResponseSchema == "" && len(l.OutputRules) == 0 &&
		isWorkflowPromptZero(l.Issue) && isWorkflowPromptZero(l.Ask) && isWorkflowPromptZero(l.PRReview)
}

func isWorkflowPromptZero(w WorkflowPromptConfig) bool {
	return w.Goal == "" && w.ResponseSchema == "" && len(w.OutputRules) == 0
}

// LegacyPRReviewConfig accepts the legacy top-level `pr_review:` block; its
// enabled flag is migrated to Workflows.PRReview.Enabled.
type LegacyPRReviewConfig struct {
	Enabled *bool `yaml:"enabled,omitempty"`
}

func (l LegacyPRReviewConfig) IsZero() bool {
	return l.Enabled == nil
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
	Capacity  int    `yaml:"capacity"`
	Transport string `yaml:"transport"`
	// Store selects the JobStore backend. "mem" keeps state in the app
	// process (lost on restart); "redis" persists state so the app can
	// resume in-flight jobs after restart. See #123 for the incident that
	// motivated the redis backend.
	Store string `yaml:"store"`
	// StoreTTL is the per-record TTL applied by RedisJobStore on every
	// write. Pick something comfortably larger than the longest expected
	// job runtime — terminal-state jobs are evicted by TTL, not deleted.
	// Ignored when Store == "mem".
	StoreTTL         time.Duration `yaml:"store_ttl"`
	JobTimeout       time.Duration `yaml:"job_timeout"`
	AgentIdleTimeout time.Duration `yaml:"agent_idle_timeout"`
	PrepareTimeout   time.Duration `yaml:"prepare_timeout"`
	CancelTimeout    time.Duration `yaml:"cancel_timeout"`
	StatusInterval   time.Duration `yaml:"status_interval"`
}

type AvailabilityConfig struct {
	AvgJobDuration time.Duration `yaml:"avg_job_duration"`
}
