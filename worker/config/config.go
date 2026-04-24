// Package config holds the worker module's yaml-backed configuration. Schema
// is FLAT: the legacy `worker:` nest is dropped (worker.yaml is already at
// worker scope, so the nest was redundant). Types live in this package so the
// worker module owns its schema end-to-end.
package config

import "time"

// Config is the worker module's yaml-backed configuration.
type Config struct {
	LogLevel     string                 `yaml:"log_level"`
	Logging      LoggingConfig          `yaml:"logging"`
	GitHub       GitHubConfig           `yaml:"github"`
	Agents       map[string]AgentConfig `yaml:"agents"`
	Providers    []string               `yaml:"providers"`
	Count        int                    `yaml:"count"`
	NicknamePool []string               `yaml:"nickname_pool"`
	Prompt       PromptConfig           `yaml:"prompt"`
	RepoCache    RepoCacheConfig        `yaml:"repo_cache"`
	Queue        QueueConfig            `yaml:"queue"`
	Redis        RedisConfig            `yaml:"redis"`
	SecretKey    string                 `yaml:"secret_key"`
	Secrets      map[string]string      `yaml:"secrets"`
}

// ExtraArgsToken is the placeholder element in AgentConfig.Args that gets
// expanded into AgentConfig.ExtraArgs at runtime. Substring matches don't
// count — the token must stand alone as its own arg slot.
const ExtraArgsToken = "{extra_args}"

// AgentConfig is the worker's agent CLI description.
//
// ExtraArgs is a user-supplied flag list that's spliced into Args in place of
// the `{extra_args}` placeholder at runtime. It lets operators layer per-site
// flags (e.g. `-m opencode/claude-opus-4-7`) on top of the built-in Args
// without copying the whole Args slice. If a user also overrides Args (whose
// override does NOT contain `{extra_args}`), ExtraArgs is silently dropped —
// `mergeBuiltinAgents` emits a startup warn when that combo is detected.
type AgentConfig struct {
	Command   string        `yaml:"command"`
	Args      []string      `yaml:"args"`
	ExtraArgs []string      `yaml:"extra_args"`
	Timeout   time.Duration `yaml:"timeout"`
	SkillDir  string        `yaml:"skill_dir"`
	Stream    bool          `yaml:"stream"`
}

// PromptConfig is the worker-owned prompt extension (the extra_rules segment
// appended to the app-side prompt, subject to app's AllowWorkerRules toggle).
type PromptConfig struct {
	ExtraRules []string `yaml:"extra_rules"`
}

type GitHubConfig struct {
	Token string `yaml:"token"`
}

type LoggingConfig struct {
	Dir            string `yaml:"dir"`
	Level          string `yaml:"level"`
	RetentionDays  int    `yaml:"retention_days"`
	AgentOutputDir string `yaml:"agent_output_dir"`
}

type RepoCacheConfig struct {
	Dir    string        `yaml:"dir"`
	MaxAge time.Duration `yaml:"max_age"`
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

type RedisConfig struct {
	Addr     string `yaml:"addr"`
	Password string `yaml:"password"`
	DB       int    `yaml:"db"`
	TLS      bool   `yaml:"tls"`
}
