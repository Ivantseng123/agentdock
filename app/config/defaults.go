package config

import (
	"log/slog"
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
	// Default JobStore backend is "redis" because production is the common
	// deployment shape and #123 is a production-hurt bug (orphaned Slack
	// threads on app restart). Making the fix opt-in would leave production
	// defaulting to broken. Local dev / single-pod test deployments without
	// Redis persistence set "mem" explicitly. See docs/MIGRATION-v2.md for
	// the v2.5 → v2.6 upgrade note.
	if cfg.Queue.Store == "" {
		cfg.Queue.Store = "redis"
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
	migrateLegacy(cfg)
	applyPromptDefaults(cfg)
	if cfg.Workflows.PRReview.Enabled == nil {
		t := true
		cfg.Workflows.PRReview.Enabled = &t
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

// cwdOnlySandboxRule is shared across all three workflows. It guards against
// the silent-failure class where the LLM (or a SKILL example) writes to
// /tmp/* or other cwd-external paths in a worktree-cwd job. opencode's
// sandbox treats those as external_directory asks; headless `opencode run`
// auto-rejects them and the reject cascade-fails the whole session, so the
// task exits 0 with no result marker. Two pr_review failures on 2026-04-24
// matched this exact pattern. See worker/agent/runner.go for the post-mortem
// detection log added at the same time.
const cwdOnlySandboxRule = "All temporary files and shell redirections MUST be written inside the current working directory (e.g. `./fp.json`, `./screenshot.png`). Never write to `/tmp/`, `/var/`, `$HOME`, `~`, or any other path outside cwd — opencode's sandbox treats those as external_directory and headless `opencode run` auto-rejects the write, silently failing the task with no result marker."

var (
	defaultAskOutputRules = []string{
		"Format the answer in Slack mrkdwn — NOT GitHub markdown. Use *text* (single asterisk) for bold, _text_ for italic, ~text~ for strikethrough, <url|label> for links. Do not use # / ## / ### headings; use *bold* as section labels. Triple-backtick fenced code blocks and single-backtick inline code both render correctly.",
		// Old rule was "No title, no labels — output the answer content only."
		// That directly contradicted Rule 1 above and SKILL.md §4a, which both
		// expect *bold* section labels (e.g. *簡答* / *依據*). Agents (correctly)
		// prioritised this hard-sounding rule and dropped skill structure.
		// Now: only ban opening heading/title lines, keep the length cap, and
		// explicitly bless inline *bold* labels so skill structure survives.
		"Do not open with an H1/H2/H3 heading or standalone title line — start directly with the answer content. *bold* section labels inside the body (e.g. *簡答*, *依據*, *延伸*) are encouraged per the ask-assistant skill. Keep the total answer ≤30000 chars.",
		"When referring to yourself, use the exact Slack handle from the <bot> tag in <issue_context> (e.g. 'ai_trigger_issue_bot') verbatim. Do NOT invent shorthand like '@bot ask', 'Ask 助理', 'AI 助理', '程式碼助手'. If a sentence doesn't need self-reference, just answer without name-dropping.",
		cwdOnlySandboxRule,
	}
	defaultPRReviewOutputRules = []string{
		"Focus on correctness, security, style",
		"Summary ≤ 2000 chars",
		cwdOnlySandboxRule,
	}
	// Issue.OutputRules used to be intentionally empty — formatting rules live
	// in the triage-issue skill's SKILL.md body template, machine schema lives
	// in Issue.ResponseSchema. The cwd-only rule is the one exception: it's a
	// sandbox guard that applies regardless of the skill in use, so it's
	// promoted from skill text to a hard output_rule per the project convention
	// "硬規則直接升格到 output_rules".
	defaultIssueOutputRules = []string{
		cwdOnlySandboxRule,
	}
)

// migrateLegacy copies legacy `prompt:` / `pr_review:` blocks into the new
// Workflows / PromptDefaults shape. When BOTH legacy and new shapes carry
// data, the new shape wins and a warning is logged. After migration the
// legacy blocks are zeroed so downstream marshalling only emits the new shape.
func migrateLegacy(cfg *Config) {
	legacyPrompt := !cfg.Prompt.IsZero()
	legacyPRReview := !cfg.PRReview.IsZero()
	if !legacyPrompt && !legacyPRReview {
		return
	}

	newShape := !isWorkflowsZero(cfg.Workflows) ||
		cfg.PromptDefaults.Language != "" ||
		cfg.PromptDefaults.AllowWorkerRules != nil
	if newShape {
		var offending []string
		if legacyPrompt {
			offending = append(offending, "prompt:")
		}
		if legacyPRReview {
			offending = append(offending, "pr_review:")
		}
		slog.Warn("設定同時使用新舊 schema，採用新 shape（workflows: / prompt_defaults:），請移除 legacy "+strings.Join(offending, " / ")+" 區塊",
			"component", "config", "phase", "載入")
	}

	// Old-B nested first: prompt.{issue,ask,pr_review}.* → workflows.<name>.prompt.*
	// Old-A flat falls back into Issue only when Old-B left it empty.
	mergeWorkflowPrompt(&cfg.Workflows.Issue.Prompt, cfg.Prompt.Issue)
	mergeWorkflowPrompt(&cfg.Workflows.Ask.Prompt, cfg.Prompt.Ask)
	mergeWorkflowPrompt(&cfg.Workflows.PRReview.Prompt, cfg.Prompt.PRReview)
	mergeWorkflowPrompt(&cfg.Workflows.Issue.Prompt, WorkflowPromptConfig{
		Goal:           cfg.Prompt.Goal,
		ResponseSchema: cfg.Prompt.ResponseSchema,
		OutputRules:    cfg.Prompt.OutputRules,
	})

	if cfg.PromptDefaults.Language == "" {
		cfg.PromptDefaults.Language = cfg.Prompt.Language
	}
	if cfg.PromptDefaults.AllowWorkerRules == nil {
		cfg.PromptDefaults.AllowWorkerRules = cfg.Prompt.AllowWorkerRules
	}
	if cfg.Workflows.PRReview.Enabled == nil {
		cfg.Workflows.PRReview.Enabled = cfg.PRReview.Enabled
	}

	cfg.Prompt = LegacyPromptConfig{}
	cfg.PRReview = LegacyPRReviewConfig{}
}

func mergeWorkflowPrompt(dst *WorkflowPromptConfig, src WorkflowPromptConfig) {
	if dst.Goal == "" && src.Goal != "" {
		dst.Goal = src.Goal
	}
	if dst.ResponseSchema == "" && src.ResponseSchema != "" {
		dst.ResponseSchema = src.ResponseSchema
	}
	if len(dst.OutputRules) == 0 && len(src.OutputRules) > 0 {
		dst.OutputRules = src.OutputRules
	}
}

func isWorkflowsZero(w WorkflowsConfig) bool {
	return isWorkflowPromptZero(w.Issue.Prompt) &&
		isWorkflowPromptZero(w.Ask.Prompt) &&
		isWorkflowPromptZero(w.PRReview.Prompt) &&
		w.PRReview.Enabled == nil
}

func applyPromptDefaults(cfg *Config) {
	issue := &cfg.Workflows.Issue.Prompt
	ask := &cfg.Workflows.Ask.Prompt
	pr := &cfg.Workflows.PRReview.Prompt

	if issue.Goal == "" {
		issue.Goal = defaultIssueGoal
	}
	if ask.Goal == "" {
		ask.Goal = defaultAskGoal
	}
	if pr.Goal == "" {
		pr.Goal = defaultPRReviewGoal
	}
	if issue.ResponseSchema == "" {
		issue.ResponseSchema = defaultIssueResponseSchema
	}
	if ask.ResponseSchema == "" {
		ask.ResponseSchema = defaultAskResponseSchema
	}
	if pr.ResponseSchema == "" {
		pr.ResponseSchema = defaultPRReviewResponseSchema
	}
	if len(ask.OutputRules) == 0 {
		ask.OutputRules = defaultAskOutputRules
	}
	if len(pr.OutputRules) == 0 {
		pr.OutputRules = defaultPRReviewOutputRules
	}
	if len(issue.OutputRules) == 0 {
		issue.OutputRules = defaultIssueOutputRules
	}

	if cfg.PromptDefaults.AllowWorkerRules == nil {
		t := true
		cfg.PromptDefaults.AllowWorkerRules = &t
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
