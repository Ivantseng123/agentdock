package config

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func loadFromString(t *testing.T, yamlContent string) *Config {
	t.Helper()
	var cfg Config
	if err := yaml.Unmarshal([]byte(yamlContent), &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	ApplyDefaults(&cfg)
	return &cfg
}

// loadWithSlogCapture loads config and captures slog output during
// ApplyDefaults. Used to assert migration warnings fire.
func loadWithSlogCapture(t *testing.T, yamlContent string) (*Config, string) {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	var cfg Config
	if err := yaml.Unmarshal([]byte(yamlContent), &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	ApplyDefaults(&cfg)
	return &cfg, buf.String()
}

func TestLoadConfig_AppFields(t *testing.T) {
	cfg := loadFromString(t, `
slack:
  bot_token: xoxb-test
  app_token: xapp-test
github:
  token: ghp-test
prompt_defaults:
  language: zh-TW
channels:
  C123:
    repos: [owner/repo-a]
channel_defaults:
  default_labels: [default-label]
auto_bind: true
max_thread_messages: 30
`)
	if cfg.Slack.BotToken != "xoxb-test" {
		t.Errorf("bot_token = %q", cfg.Slack.BotToken)
	}
	if cfg.PromptDefaults.Language != "zh-TW" {
		t.Errorf("language = %q", cfg.PromptDefaults.Language)
	}
	ch := cfg.Channels["C123"]
	if repos := ch.GetRepos(); len(repos) != 1 || repos[0] != "owner/repo-a" {
		t.Errorf("repos = %v", repos)
	}
	if cfg.MaxThreadMessages != 30 {
		t.Errorf("max_thread_messages = %d", cfg.MaxThreadMessages)
	}
}

func TestApplyDefaults_Timeouts(t *testing.T) {
	cfg := loadFromString(t, ``)
	if cfg.SemaphoreTimeout != 30*time.Second {
		t.Errorf("semaphore = %v", cfg.SemaphoreTimeout)
	}
	if cfg.Queue.JobTimeout != 35*time.Minute {
		t.Errorf("job_timeout = %v", cfg.Queue.JobTimeout)
	}
}

func TestApplyDefaults_QueueStore(t *testing.T) {
	cfg := loadFromString(t, ``)
	if cfg.Queue.Store != "redis" {
		t.Errorf("default queue.store = %q, want redis", cfg.Queue.Store)
	}
	if cfg.Queue.StoreTTL != time.Hour {
		t.Errorf("default queue.store_ttl = %v, want 1h", cfg.Queue.StoreTTL)
	}
}

func TestLoadConfig_QueueStoreMem(t *testing.T) {
	cfg := loadFromString(t, `
queue:
  store: mem
`)
	if cfg.Queue.Store != "mem" {
		t.Errorf("queue.store = %q, want mem", cfg.Queue.Store)
	}
}

func TestLoadConfig_QueueStoreRedis(t *testing.T) {
	cfg := loadFromString(t, `
queue:
  store: redis
  store_ttl: 2h
`)
	if cfg.Queue.Store != "redis" {
		t.Errorf("queue.store = %q, want redis", cfg.Queue.Store)
	}
	if cfg.Queue.StoreTTL != 2*time.Hour {
		t.Errorf("queue.store_ttl = %v, want 2h", cfg.Queue.StoreTTL)
	}
}

func TestValidate_QueueStoreUnknown(t *testing.T) {
	cfg := loadFromString(t, `
queue:
  store: postgres
`)
	err := Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "queue.store") {
		t.Errorf("expected validation error for unknown store, got %v", err)
	}
}

func TestValidate_QueueStoreRedisRequiresTTL(t *testing.T) {
	cfg := &Config{
		Queue: QueueConfig{
			Transport: "redis",
			Store:     "redis",
			StoreTTL:  0,
		},
	}
	err := Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "queue.store_ttl") {
		t.Errorf("expected validation error for redis store with zero TTL, got %v", err)
	}
}

func TestApplyDefaults_PromptGoal(t *testing.T) {
	cfg := loadFromString(t, ``)
	if cfg.Workflows.Issue.Prompt.Goal != defaultIssueGoal {
		t.Errorf("default Issue.Prompt.Goal = %q, want %q", cfg.Workflows.Issue.Prompt.Goal, defaultIssueGoal)
	}
}

func TestApplyDefaults_AllowWorkerRules(t *testing.T) {
	cfg := loadFromString(t, ``)
	if cfg.PromptDefaults.AllowWorkerRules == nil || !*cfg.PromptDefaults.AllowWorkerRules {
		t.Errorf("allow_worker_rules default = %v, want true", cfg.PromptDefaults.AllowWorkerRules)
	}
}

func TestResolveSecrets_MergesGitHubToken(t *testing.T) {
	cfg := loadFromString(t, `
github:
  token: ghp-merge
`)
	if cfg.Secrets["GH_TOKEN"] != "ghp-merge" {
		t.Errorf("GH_TOKEN = %q, want ghp-merge", cfg.Secrets["GH_TOKEN"])
	}
}

func TestDefaultsMap_ShapeMatchesYAMLTags(t *testing.T) {
	m := DefaultsMap()
	if _, ok := m["queue"]; !ok {
		t.Error("DefaultsMap missing queue key")
	}
	q, _ := m["queue"].(map[string]any)
	if q["transport"] != "redis" {
		t.Errorf("queue.transport = %v, want redis", q["transport"])
	}
	// New shape: workflows / prompt_defaults present, legacy prompt / pr_review
	// cleared after migration.
	if _, ok := m["workflows"]; !ok {
		t.Error("DefaultsMap missing workflows key")
	}
	if _, ok := m["prompt_defaults"]; !ok {
		t.Error("DefaultsMap missing prompt_defaults key")
	}
}

// --- NEW SHAPE ---

func TestLoadConfig_NewShape(t *testing.T) {
	cfg := loadFromString(t, `
workflows:
  issue:
    prompt:
      goal: "new issue goal"
      output_rules:
        - "new rule"
  ask:
    prompt:
      goal: "new ask goal"
  pr_review:
    enabled: false
    prompt:
      goal: "new pr_review goal"

prompt_defaults:
  language: "日本語"
  allow_worker_rules: false
`)
	if cfg.Workflows.Issue.Prompt.Goal != "new issue goal" {
		t.Errorf("Issue.Prompt.Goal = %q", cfg.Workflows.Issue.Prompt.Goal)
	}
	if cfg.Workflows.Ask.Prompt.Goal != "new ask goal" {
		t.Errorf("Ask.Prompt.Goal = %q", cfg.Workflows.Ask.Prompt.Goal)
	}
	if cfg.Workflows.PRReview.Prompt.Goal != "new pr_review goal" {
		t.Errorf("PRReview.Prompt.Goal = %q", cfg.Workflows.PRReview.Prompt.Goal)
	}
	if cfg.Workflows.PRReview.IsEnabled() {
		t.Error("explicit enabled:false should disable PRReview")
	}
	if cfg.PromptDefaults.Language != "日本語" {
		t.Errorf("Language = %q", cfg.PromptDefaults.Language)
	}
	if cfg.PromptDefaults.IsWorkerRulesAllowed() {
		t.Error("allow_worker_rules:false should disable worker rules")
	}
}

// --- Old-A (pre-#124 flat) alias ---

func TestPromptConfig_LegacyOldA_FlatAliasedToIssue(t *testing.T) {
	cfg := loadFromString(t, `
prompt:
  language: "zh-TW"
  goal: "legacy flat goal"
  output_rules:
    - "legacy rule"
`)
	if cfg.Workflows.Issue.Prompt.Goal != "legacy flat goal" {
		t.Errorf("Issue.Prompt.Goal = %q, want legacy flat alias", cfg.Workflows.Issue.Prompt.Goal)
	}
	if len(cfg.Workflows.Issue.Prompt.OutputRules) != 1 || cfg.Workflows.Issue.Prompt.OutputRules[0] != "legacy rule" {
		t.Errorf("Issue.Prompt.OutputRules = %v", cfg.Workflows.Issue.Prompt.OutputRules)
	}
	if cfg.PromptDefaults.Language != "zh-TW" {
		t.Errorf("language alias failed: %q", cfg.PromptDefaults.Language)
	}
	// Legacy block must be cleared after migration.
	if !cfg.Prompt.IsZero() {
		t.Errorf("legacy Prompt block should be cleared post-migration: %+v", cfg.Prompt)
	}
}

// --- Old-B (#124 nested-under-prompt) alias ---

func TestPromptConfig_LegacyOldB_NestedAliasedToWorkflows(t *testing.T) {
	cfg := loadFromString(t, `
prompt:
  language: "繁體中文"
  allow_worker_rules: false
  issue:
    goal: "old-B issue"
    output_rules: ["r1"]
  ask:
    goal: "old-B ask"
  pr_review:
    goal: "old-B pr_review"

pr_review:
  enabled: false
`)
	if cfg.Workflows.Issue.Prompt.Goal != "old-B issue" {
		t.Errorf("Issue.Prompt.Goal = %q", cfg.Workflows.Issue.Prompt.Goal)
	}
	if cfg.Workflows.Ask.Prompt.Goal != "old-B ask" {
		t.Errorf("Ask.Prompt.Goal = %q", cfg.Workflows.Ask.Prompt.Goal)
	}
	if cfg.Workflows.PRReview.Prompt.Goal != "old-B pr_review" {
		t.Errorf("PRReview.Prompt.Goal = %q", cfg.Workflows.PRReview.Prompt.Goal)
	}
	if cfg.Workflows.PRReview.IsEnabled() {
		t.Error("old-B pr_review.enabled:false should carry through")
	}
	if cfg.PromptDefaults.Language != "繁體中文" {
		t.Errorf("language alias failed: %q", cfg.PromptDefaults.Language)
	}
	if cfg.PromptDefaults.IsWorkerRulesAllowed() {
		t.Error("old-B allow_worker_rules:false should carry through")
	}
	// Legacy blocks must be cleared.
	if !cfg.Prompt.IsZero() || !cfg.PRReview.IsZero() {
		t.Errorf("legacy blocks should be cleared: prompt=%+v pr_review=%+v", cfg.Prompt, cfg.PRReview)
	}
}

// --- Mixed new + legacy: new shape wins + warning emitted ---

func TestPromptConfig_MixedNewWinsOverLegacyWithWarning(t *testing.T) {
	cfg, logs := loadWithSlogCapture(t, `
workflows:
  issue:
    prompt:
      goal: "new-wins issue goal"

prompt:
  goal: "legacy-should-lose"
  issue:
    goal: "legacy-nested-should-lose"
`)
	if cfg.Workflows.Issue.Prompt.Goal != "new-wins issue goal" {
		t.Errorf("new shape must win: got %q", cfg.Workflows.Issue.Prompt.Goal)
	}
	if !strings.Contains(logs, "設定同時使用新舊 schema") {
		t.Errorf("expected migration warning in slog output, got:\n%s", logs)
	}
	// Only legacy prompt: was set in this fixture, so the warn must name
	// prompt: but NOT pr_review:.
	if !strings.Contains(logs, "legacy prompt: 區塊") {
		t.Errorf("warn should name the offending legacy block 'prompt:' only, got:\n%s", logs)
	}
	if strings.Contains(logs, "pr_review:") {
		t.Errorf("warn should NOT name pr_review: when only prompt: was set, got:\n%s", logs)
	}
}

func TestPromptConfig_NestedOverridesFlatWithinLegacy(t *testing.T) {
	// Within the legacy prompt: block, nested issue.* beats flat goal.
	cfg := loadFromString(t, `
prompt:
  goal: "flat legacy"
  issue:
    goal: "nested legacy"
`)
	if cfg.Workflows.Issue.Prompt.Goal != "nested legacy" {
		t.Errorf("nested must win over flat within legacy: got %q", cfg.Workflows.Issue.Prompt.Goal)
	}
}

func TestPromptConfig_DefaultsPopulated(t *testing.T) {
	cfg := &Config{}
	ApplyDefaults(cfg)
	if cfg.Workflows.Issue.Prompt.Goal == "" {
		t.Error("Issue.Prompt.Goal default is empty")
	}
	if cfg.Workflows.Ask.Prompt.Goal == "" {
		t.Error("Ask.Prompt.Goal default is empty")
	}
	if cfg.Workflows.PRReview.Prompt.Goal == "" {
		t.Error("PRReview.Prompt.Goal default is empty")
	}
}

// TestPromptConfig_CwdOnlyRuleInAllWorkflows pins the sandbox guard to all
// three workflows.
func TestPromptConfig_CwdOnlyRuleInAllWorkflows(t *testing.T) {
	cfg := &Config{}
	ApplyDefaults(cfg)

	cases := map[string][]string{
		"Issue":    cfg.Workflows.Issue.Prompt.OutputRules,
		"Ask":      cfg.Workflows.Ask.Prompt.OutputRules,
		"PRReview": cfg.Workflows.PRReview.Prompt.OutputRules,
	}
	for name, rules := range cases {
		var found bool
		for _, r := range rules {
			if strings.Contains(r, "outside cwd") && strings.Contains(r, "/tmp/") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s.OutputRules missing cwdOnlySandboxRule (got %d rules, none mention 'outside cwd' + '/tmp/')",
				name, len(rules))
		}
	}
}

func TestPromptConfig_ResponseSchemaDefaults(t *testing.T) {
	cfg := &Config{}
	ApplyDefaults(cfg)

	if cfg.Workflows.Issue.Prompt.ResponseSchema == "" {
		t.Error("Issue.Prompt.ResponseSchema default is empty")
	}
	if !strings.Contains(cfg.Workflows.Issue.Prompt.ResponseSchema, "===TRIAGE_RESULT===") {
		t.Errorf("Issue.Prompt.ResponseSchema missing TRIAGE_RESULT marker: %q", cfg.Workflows.Issue.Prompt.ResponseSchema)
	}
	for _, field := range []string{
		`"status"`, `"title"`, `"body"`, `"labels"`,
		`"confidence"`, `"files_found"`, `"open_questions"`, `"message"`,
	} {
		if !strings.Contains(cfg.Workflows.Issue.Prompt.ResponseSchema, field) {
			t.Errorf("Issue.Prompt.ResponseSchema missing required field %s", field)
		}
	}
}

func TestPromptConfig_IssueSchemaCarriesStripTriageHeaders(t *testing.T) {
	cfg := &Config{}
	ApplyDefaults(cfg)
	for _, header := range []string{"## Root Cause Analysis", "## TDD Fix Plan"} {
		if !strings.Contains(cfg.Workflows.Issue.Prompt.ResponseSchema, header) {
			t.Errorf("Issue.Prompt.ResponseSchema missing stripTriageSection header %q", header)
		}
	}

	if cfg.Workflows.Ask.Prompt.ResponseSchema == "" {
		t.Error("Ask.Prompt.ResponseSchema default is empty")
	}
	if !strings.Contains(cfg.Workflows.Ask.Prompt.ResponseSchema, "===ASK_RESULT===") {
		t.Errorf("Ask.Prompt.ResponseSchema missing ASK_RESULT marker")
	}
	if !strings.Contains(cfg.Workflows.Ask.Prompt.ResponseSchema, `"answer"`) {
		t.Errorf("Ask.Prompt.ResponseSchema missing literal \"answer\" key")
	}

	if cfg.Workflows.PRReview.Prompt.ResponseSchema == "" {
		t.Error("PRReview.Prompt.ResponseSchema default is empty")
	}
	if !strings.Contains(cfg.Workflows.PRReview.Prompt.ResponseSchema, "===REVIEW_RESULT===") {
		t.Errorf("PRReview.Prompt.ResponseSchema missing REVIEW_RESULT marker")
	}
	for _, field := range []string{
		`"status"`, `"summary"`, `"comments_posted"`, `"comments_skipped"`,
		`"severity_summary"`, `"reason"`, `"error"`,
	} {
		if !strings.Contains(cfg.Workflows.PRReview.Prompt.ResponseSchema, field) {
			t.Errorf("PRReview.Prompt.ResponseSchema missing required field %s", field)
		}
	}
}

func TestPromptConfig_GoalDoesNotDuplicateSchema(t *testing.T) {
	cfg := &Config{}
	ApplyDefaults(cfg)

	if strings.Contains(cfg.Workflows.Issue.Prompt.Goal, "===TRIAGE_RESULT===") {
		t.Errorf("Issue.Prompt.Goal must NOT contain TRIAGE_RESULT marker")
	}
	if strings.Contains(cfg.Workflows.Ask.Prompt.Goal, "===ASK_RESULT===") {
		t.Errorf("Ask.Prompt.Goal must NOT contain ASK_RESULT marker")
	}
	if strings.Contains(cfg.Workflows.PRReview.Prompt.Goal, "===REVIEW_RESULT===") {
		t.Errorf("PRReview.Prompt.Goal must NOT contain REVIEW_RESULT marker")
	}
}

func TestPromptConfig_OperatorResponseSchemaWins(t *testing.T) {
	cfg := loadFromString(t, `
workflows:
  ask:
    prompt:
      response_schema: "custom schema"
`)
	if cfg.Workflows.Ask.Prompt.ResponseSchema != "custom schema" {
		t.Errorf("operator-provided ResponseSchema dropped: got %q", cfg.Workflows.Ask.Prompt.ResponseSchema)
	}
}

func TestPRReviewConfig_DefaultEnabled(t *testing.T) {
	cfg := &Config{}
	ApplyDefaults(cfg)
	if !cfg.Workflows.PRReview.IsEnabled() {
		t.Error("PRReview default should be enabled (opt-out, not opt-in)")
	}
}

func TestPRReviewConfig_ExplicitFalseWins_NewShape(t *testing.T) {
	cfg := loadFromString(t, `
workflows:
  pr_review:
    enabled: false
`)
	if cfg.Workflows.PRReview.IsEnabled() {
		t.Error("explicit workflows.pr_review.enabled: false should turn the feature off")
	}
}

func TestPRReviewConfig_LegacyTopLevelFalseWins(t *testing.T) {
	// Old-B alias: top-level pr_review.enabled:false must still disable.
	cfg := loadFromString(t, `
pr_review:
  enabled: false
`)
	if cfg.Workflows.PRReview.IsEnabled() {
		t.Error("legacy top-level pr_review.enabled: false should turn the feature off")
	}
}

func TestResolveSecrets_MantisInjected(t *testing.T) {
	cfg := loadFromString(t, `
mantis:
  base_url: https://mantis.example.com
  api_token: mantis-token
`)
	if got := cfg.Secrets["MANTIS_API_URL"]; got != "https://mantis.example.com/api/rest" {
		t.Errorf("MANTIS_API_URL = %q", got)
	}
	if got := cfg.Secrets["MANTIS_API_TOKEN"]; got != "mantis-token" {
		t.Errorf("MANTIS_API_TOKEN = %q", got)
	}
}

func TestResolveSecrets_MantisStripsTrailingSlash(t *testing.T) {
	cfg := loadFromString(t, `
mantis:
  base_url: https://mantis.example.com/
  api_token: t
`)
	if got := cfg.Secrets["MANTIS_API_URL"]; got != "https://mantis.example.com/api/rest" {
		t.Errorf("MANTIS_API_URL = %q", got)
	}
}

func TestResolveSecrets_MantisEmpty_NoInjection(t *testing.T) {
	cfg := loadFromString(t, ``)
	if _, ok := cfg.Secrets["MANTIS_API_URL"]; ok {
		t.Error("MANTIS_API_URL should not be set when Mantis is unconfigured")
	}
	if _, ok := cfg.Secrets["MANTIS_API_TOKEN"]; ok {
		t.Error("MANTIS_API_TOKEN should not be set when Mantis is unconfigured")
	}
}

func TestResolveSecrets_MantisExistingSecretNotOverridden(t *testing.T) {
	cfg := loadFromString(t, `
mantis:
  base_url: https://mantis.example.com
  api_token: from-config
secrets:
  MANTIS_API_TOKEN: from-secrets
`)
	if got := cfg.Secrets["MANTIS_API_TOKEN"]; got != "from-secrets" {
		t.Errorf("MANTIS_API_TOKEN = %q, want from-secrets", got)
	}
}

func TestValidate_Mantis_PartialConfigBaseURLOnly(t *testing.T) {
	cfg := loadFromString(t, `
mantis:
  base_url: https://mantis.example.com
`)
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected validation error for partial mantis config")
	}
	if !strings.Contains(err.Error(), "mantis.base_url and mantis.api_token") {
		t.Errorf("error = %v", err)
	}
}

func TestValidate_Mantis_PartialConfigTokenOnly(t *testing.T) {
	cfg := loadFromString(t, `
mantis:
  api_token: just-a-token
`)
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected validation error for partial mantis config")
	}
}

func TestValidate_Mantis_BothEmpty_OK(t *testing.T) {
	cfg := loadFromString(t, ``)
	if err := Validate(cfg); err != nil {
		if strings.Contains(err.Error(), "mantis") {
			t.Errorf("got unexpected mantis error: %v", err)
		}
	}
}

func TestValidate_Mantis_BothSet_OK(t *testing.T) {
	cfg := loadFromString(t, `
mantis:
  base_url: https://mantis.example.com
  api_token: t
`)
	if err := Validate(cfg); err != nil {
		if strings.Contains(err.Error(), "mantis") {
			t.Errorf("got unexpected mantis error: %v", err)
		}
	}
}
