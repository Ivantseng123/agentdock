package config

import (
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// ApplyDefaults fills in default values for fields the user didn't set.
func ApplyDefaults(cfg *Config) {
	if cfg.LogLevel == "" {
		cfg.LogLevel = "info"
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
		cfg.Queue.Transport = "redis"
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
	if cfg.Queue.CancelTimeout <= 0 {
		cfg.Queue.CancelTimeout = 60 * time.Second
	}
	if cfg.Queue.StatusInterval <= 0 {
		cfg.Queue.StatusInterval = 5 * time.Second
	}
	if cfg.RepoCache.Dir == "" {
		if cacheDir, err := os.UserCacheDir(); err == nil {
			cfg.RepoCache.Dir = filepath.Join(cacheDir, "agentdock", "repos")
		} else {
			cfg.RepoCache.Dir = filepath.Join(os.TempDir(), "agentdock", "repos")
		}
	}
	if cfg.RepoCache.MaxAge <= 0 {
		cfg.RepoCache.MaxAge = 10 * time.Minute
	}
	if cfg.Attachments.TempDir == "" {
		cfg.Attachments.TempDir = filepath.Join(os.TempDir(), "triage-attachments")
	}
	if cfg.Attachments.TTL <= 0 {
		cfg.Attachments.TTL = 30 * time.Minute
	}
	applyPromptDefaults(&cfg.Prompt)
	resolveSecrets(cfg)
}

// DefaultsMap returns a koanf-friendly map[string]any of all default values
// produced by ApplyDefaults. Round-trips via YAML to preserve nested struct
// shape and yaml tags.
func DefaultsMap() map[string]any {
	var cfg Config
	ApplyDefaults(&cfg)
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

// Hardcoded per-workflow defaults. Operator yaml wins over these.
const (
	defaultIssueGoal    = "Use the /triage-issue skill to investigate and produce a triage result."
	defaultAskGoal      = "Answer the user's question using the thread, and (if a codebase is attached) the repo. Output ===ASK_RESULT=== followed by JSON {\"answer\": \"<markdown>\"}."
	defaultPRReviewGoal = "Review the PR. Use the github-pr-review skill to analyze the diff and post line-level comments plus a summary review via agentdock pr-review-helper. Output ===REVIEW_RESULT=== with status (POSTED|SKIPPED|ERROR) + summary + severity_summary."
)

var (
	defaultAskOutputRules = []string{
		"Slack-friendly markdown, ≤30000 chars",
		"No title / labels",
		"Use fenced code blocks for code references",
	}
	defaultPRReviewOutputRules = []string{
		"Focus on correctness, security, style",
		"Summary ≤ 2000 chars",
	}
)

func applyPromptDefaults(p *PromptConfig) {
	// Alias: flat → Issue when Issue is empty.
	if p.Issue.Goal == "" && p.Goal != "" {
		p.Issue.Goal = p.Goal
	}
	if len(p.Issue.OutputRules) == 0 && len(p.OutputRules) > 0 {
		p.Issue.OutputRules = p.OutputRules
	}

	// Hardcoded defaults for each workflow.
	if p.Issue.Goal == "" {
		p.Issue.Goal = defaultIssueGoal
	}
	if p.Ask.Goal == "" {
		p.Ask.Goal = defaultAskGoal
	}
	if p.PRReview.Goal == "" {
		p.PRReview.Goal = defaultPRReviewGoal
	}
	if len(p.Ask.OutputRules) == 0 {
		p.Ask.OutputRules = defaultAskOutputRules
	}
	if len(p.PRReview.OutputRules) == 0 {
		p.PRReview.OutputRules = defaultPRReviewOutputRules
	}
	// Issue.OutputRules is intentionally left empty if operator didn't set
	// it; the current spec's hardcoded Issue rules travel in
	// app/workflow/issue.go as spec language, not as defaults here.

	// Preserve prior AllowWorkerRules default (pointer to true).
	if p.AllowWorkerRules == nil {
		t := true
		p.AllowWorkerRules = &t
	}
}

// resolveSecrets merges github.token into secrets and applies env var overrides.
func resolveSecrets(cfg *Config) {
	if cfg.Secrets == nil {
		cfg.Secrets = make(map[string]string)
	}
	if cfg.GitHub.Token != "" {
		if _, exists := cfg.Secrets["GH_TOKEN"]; !exists {
			cfg.Secrets["GH_TOKEN"] = cfg.GitHub.Token
		}
	}
	for k, v := range scanSecretEnvVars() {
		cfg.Secrets[k] = v
	}
}
