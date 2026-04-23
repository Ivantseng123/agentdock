package config

import (
	"os"
	"path/filepath"
	"strings"
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
	if cfg.Queue.Store == "" {
		// Default to the in-process store so existing deployments keep the
		// pre-#123 behaviour. Operators running multiple app pods or who
		// rely on restart-safety should set queue.store: redis.
		cfg.Queue.Store = "mem"
	}
	if cfg.Queue.StoreTTL <= 0 {
		cfg.Queue.StoreTTL = 1 * time.Hour
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
	if cfg.Availability.AvgJobDuration <= 0 {
		cfg.Availability.AvgJobDuration = 3 * time.Minute
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
	if cfg.PRReview.Enabled == nil {
		t := true
		cfg.PRReview.Enabled = &t
	}
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
//
// Goals describe the task; ResponseSchema describes the machine-readable
// output contract. Splitting them lets weaker models handle each
// concern without mixing task-framing with exact-string requirements.
const (
	defaultIssueGoal    = "Use the /triage-issue skill to investigate and produce a triage result."
	defaultAskGoal      = "Answer the user's question using the thread, and (if a codebase is attached) the repo. Follow the ask-assistant skill for scope, boundaries, and punt rules."
	defaultPRReviewGoal = "Review the PR. Use the github-pr-review skill to analyze the diff and post line-level comments plus a summary review via agentdock pr-review-helper."

	defaultAskResponseSchema = `Your final response MUST end with this exact block (no leading whitespace, no markdown fence around it):

===ASK_RESULT===
{"answer": "<your full markdown answer as a single JSON string>"}

The JSON key MUST be literally "answer" (six letters: a-n-s-w-e-r). Do NOT use "text", "content", "response", "result", "message", or any synonym. Any other key causes a parse failure.`

	// Mirrors app/workflow/pr_review_parser.go:ReviewResult and the
	// github-pr-review skill's "Emit the result marker" section. Keep the
	// three shapes in sync with that parser; extra fields are ignored but
	// missing required fields degrade Slack feedback (0 comments / empty
	// error).
	defaultPRReviewResponseSchema = `Your final response MUST end with this exact block:

===REVIEW_RESULT===
<ONE of the three JSON shapes below, chosen by status>

POSTED (review landed on the PR):
{"status": "POSTED", "summary": "<same text posted to GitHub>", "comments_posted": <int>, "comments_skipped": <int>, "severity_summary": "clean|minor|major"}

SKIPPED (short-circuited — e.g. lockfile_only, vendored, generated, pure_docs, pure_config):
{"status": "SKIPPED", "summary": "<short markdown>", "reason": "<one of: lockfile_only|vendored|generated|pure_docs|pure_config>"}

ERROR (review failed, helper exit != 0):
{"status": "ERROR", "error": "<diagnostic message operators can act on>", "summary": "<what you would have posted>"}

Use exactly these keys — no synonyms. Do NOT merge shapes (e.g. never emit "reason" when status is POSTED).`

	// Mirrors app/workflow/issue_parser.go:TriageResult and the
	// triage-issue skill's "Output result" section. Three discriminated
	// shapes by status. Title is required when status is CREATED; missing
	// title is a hard parse failure in the workflow.
	//
	// LOAD-BEARING HEADERS: app/workflow/issue.go:stripTriageSection uses
	// literal "## Root Cause Analysis" and "## TDD Fix Plan" to trim
	// low-confidence content in degraded runs (files_found == 0 ||
	// open_questions >= 5). The schema promises those headers exist in the
	// body; the triage-issue skill's body template carries their actual
	// content. Change one and the other must follow — the test
	// TestPromptConfig_IssueSchemaCarriesStripTriageHeaders enforces it.
	defaultIssueResponseSchema = `Your final response MUST end with this exact block:

===TRIAGE_RESULT===
<ONE of the three JSON shapes below, chosen by status>

CREATED (confidence is high or medium — issue should be filed):
{"status": "CREATED", "title": "<concise issue title — REQUIRED>", "body": "<full markdown body as a single JSON string, \n for newlines>", "labels": ["bug"], "confidence": "high|medium", "files_found": <int>, "open_questions": <int>}

REJECTED (confidence is low — not related to this repo):
{"status": "REJECTED", "message": "<brief explanation why this is out of scope>"}

ERROR (investigation couldn't complete):
{"status": "ERROR", "message": "<what went wrong>"}

Use exactly these keys — no synonyms. CREATED without a non-empty title is a hard failure. Do NOT run gh issue create yourself — just emit the JSON; the app creates the issue from your output.

BODY STRUCTURE (load-bearing): When status is CREATED, the "body" JSON string MUST contain these two markdown headers verbatim, spelled exactly as shown:
  "## Root Cause Analysis"
  "## TDD Fix Plan"
The app strips those sections in degraded runs (low files_found / high open_questions). Without them, low-confidence analysis leaks into the published GitHub issue.`
)

var (
	defaultAskOutputRules = []string{
		"Format the answer in Slack mrkdwn — NOT GitHub markdown. Use *text* (single asterisk) for bold, _text_ for italic, ~text~ for strikethrough, <url|label> for links. Do not use # / ## / ### headings; use *bold* as section labels. Triple-backtick fenced code blocks and single-backtick inline code both render correctly.",
		"No title, no labels — output the answer content only. Keep it ≤30000 chars.",
		"When referring to yourself, use the exact Slack handle from the <bot> tag in <issue_context> (e.g. 'ai_trigger_issue_bot') verbatim. Do NOT invent shorthand like '@bot ask', 'Ask 助理', 'AI 助理', '程式碼助手'. If a sentence doesn't need self-reference, just answer without name-dropping.",
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
	if p.Issue.ResponseSchema == "" {
		p.Issue.ResponseSchema = defaultIssueResponseSchema
	}
	if p.Ask.ResponseSchema == "" {
		p.Ask.ResponseSchema = defaultAskResponseSchema
	}
	if p.PRReview.ResponseSchema == "" {
		p.PRReview.ResponseSchema = defaultPRReviewResponseSchema
	}
	if len(p.Ask.OutputRules) == 0 {
		p.Ask.OutputRules = defaultAskOutputRules
	}
	if len(p.PRReview.OutputRules) == 0 {
		p.PRReview.OutputRules = defaultPRReviewOutputRules
	}
	// Issue.OutputRules is intentionally left empty — formatting rules for
	// triage output live in the triage-issue skill's SKILL.md body template,
	// not here. The machine-readable contract (CREATED/REJECTED/ERROR
	// shapes) now lives in Issue.ResponseSchema instead of being implicit
	// in the skill.

	// Preserve prior AllowWorkerRules default (pointer to true).
	if p.AllowWorkerRules == nil {
		t := true
		p.AllowWorkerRules = &t
	}
}

// resolveSecrets merges github.token and mantis.* into secrets and
// applies env var overrides.
func resolveSecrets(cfg *Config) {
	if cfg.Secrets == nil {
		cfg.Secrets = make(map[string]string)
	}
	if cfg.GitHub.Token != "" {
		if _, exists := cfg.Secrets["GH_TOKEN"]; !exists {
			cfg.Secrets["GH_TOKEN"] = cfg.GitHub.Token
		}
	}
	if cfg.Mantis.BaseURL != "" && cfg.Mantis.APIToken != "" {
		if _, exists := cfg.Secrets["MANTIS_API_URL"]; !exists {
			cfg.Secrets["MANTIS_API_URL"] = strings.TrimRight(cfg.Mantis.BaseURL, "/") + "/api/rest"
		}
		if _, exists := cfg.Secrets["MANTIS_API_TOKEN"]; !exists {
			cfg.Secrets["MANTIS_API_TOKEN"] = cfg.Mantis.APIToken
		}
	}
	for k, v := range scanSecretEnvVars() {
		cfg.Secrets[k] = v
	}
}
