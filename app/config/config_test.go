package config

import (
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

func TestLoadConfig_AppFields(t *testing.T) {
	cfg := loadFromString(t, `
slack:
  bot_token: xoxb-test
  app_token: xapp-test
github:
  token: ghp-test
prompt:
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
	if cfg.Prompt.Language != "zh-TW" {
		t.Errorf("language = %q", cfg.Prompt.Language)
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
	if cfg.Queue.JobTimeout != 20*time.Minute {
		t.Errorf("job_timeout = %v", cfg.Queue.JobTimeout)
	}
}

func TestApplyDefaults_PromptGoal(t *testing.T) {
	cfg := loadFromString(t, ``)
	if cfg.Prompt.Issue.Goal != defaultIssueGoal {
		t.Errorf("default Issue.Goal = %q, want %q", cfg.Prompt.Issue.Goal, defaultIssueGoal)
	}
}

func TestApplyDefaults_AllowWorkerRules(t *testing.T) {
	cfg := loadFromString(t, ``)
	if cfg.Prompt.AllowWorkerRules == nil || !*cfg.Prompt.AllowWorkerRules {
		t.Errorf("allow_worker_rules default = %v, want true", cfg.Prompt.AllowWorkerRules)
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
}

func TestPromptConfig_LegacyFlatAliasedToIssue(t *testing.T) {
	cfg := loadFromString(t, `
prompt:
  language: "zh-TW"
  goal: "legacy flat goal"
  output_rules:
    - "legacy rule"
`)
	if cfg.Prompt.Issue.Goal != "legacy flat goal" {
		t.Errorf("Issue.Goal = %q, want legacy flat alias", cfg.Prompt.Issue.Goal)
	}
	if len(cfg.Prompt.Issue.OutputRules) != 1 || cfg.Prompt.Issue.OutputRules[0] != "legacy rule" {
		t.Errorf("Issue.OutputRules = %v", cfg.Prompt.Issue.OutputRules)
	}
}

func TestPromptConfig_NestedOverridesFlat(t *testing.T) {
	cfg := loadFromString(t, `
prompt:
  goal: "legacy"
  issue:
    goal: "nested issue goal"
`)
	if cfg.Prompt.Issue.Goal != "nested issue goal" {
		t.Errorf("nested must win over flat: got %q", cfg.Prompt.Issue.Goal)
	}
}

func TestPromptConfig_DefaultsPopulated(t *testing.T) {
	cfg := &Config{}
	ApplyDefaults(cfg)
	if cfg.Prompt.Issue.Goal == "" {
		t.Error("Issue.Goal default is empty")
	}
	if cfg.Prompt.Ask.Goal == "" {
		t.Error("Ask.Goal default is empty")
	}
	if cfg.Prompt.PRReview.Goal == "" {
		t.Error("PRReview.Goal default is empty")
	}
}

func TestPromptConfig_ResponseSchemaDefaults(t *testing.T) {
	cfg := &Config{}
	ApplyDefaults(cfg)

	// Ask and PRReview must have a ResponseSchema; Issue intentionally does
	// not (its output contract lives in app/workflow/issue.go as spec
	// language).
	if cfg.Prompt.Ask.ResponseSchema == "" {
		t.Error("Ask.ResponseSchema default is empty")
	}
	if !strings.Contains(cfg.Prompt.Ask.ResponseSchema, "===ASK_RESULT===") {
		t.Errorf("Ask.ResponseSchema missing ASK_RESULT marker: %q", cfg.Prompt.Ask.ResponseSchema)
	}
	if !strings.Contains(cfg.Prompt.Ask.ResponseSchema, `"answer"`) {
		t.Errorf("Ask.ResponseSchema missing literal \"answer\" key: %q", cfg.Prompt.Ask.ResponseSchema)
	}

	if cfg.Prompt.PRReview.ResponseSchema == "" {
		t.Error("PRReview.ResponseSchema default is empty")
	}
	if !strings.Contains(cfg.Prompt.PRReview.ResponseSchema, "===REVIEW_RESULT===") {
		t.Errorf("PRReview.ResponseSchema missing REVIEW_RESULT marker: %q", cfg.Prompt.PRReview.ResponseSchema)
	}
	// Must mention every field the pr_review_parser.ReviewResult cares
	// about — losing any of these silently degrades Slack output.
	for _, field := range []string{
		`"status"`,
		`"summary"`,
		`"comments_posted"`,
		`"comments_skipped"`,
		`"severity_summary"`,
		`"reason"`,
		`"error"`,
	} {
		if !strings.Contains(cfg.Prompt.PRReview.ResponseSchema, field) {
			t.Errorf("PRReview.ResponseSchema missing required field %s; current:\n%s",
				field, cfg.Prompt.PRReview.ResponseSchema)
		}
	}
}

func TestPromptConfig_GoalDoesNotDuplicateSchema(t *testing.T) {
	// Regression: the marker + JSON shape belong in ResponseSchema, not in
	// Goal. Keeping them separate prevents weak models from mixing task
	// framing with exact-string requirements.
	cfg := &Config{}
	ApplyDefaults(cfg)

	if strings.Contains(cfg.Prompt.Ask.Goal, "===ASK_RESULT===") {
		t.Errorf("Ask.Goal must NOT contain the ASK_RESULT marker (belongs in ResponseSchema): %q", cfg.Prompt.Ask.Goal)
	}
	if strings.Contains(cfg.Prompt.PRReview.Goal, "===REVIEW_RESULT===") {
		t.Errorf("PRReview.Goal must NOT contain the REVIEW_RESULT marker (belongs in ResponseSchema): %q", cfg.Prompt.PRReview.Goal)
	}
}

func TestPromptConfig_OperatorResponseSchemaWins(t *testing.T) {
	cfg := loadFromString(t, `
prompt:
  ask:
    response_schema: "custom schema"
`)
	if cfg.Prompt.Ask.ResponseSchema != "custom schema" {
		t.Errorf("operator-provided ResponseSchema dropped: got %q", cfg.Prompt.Ask.ResponseSchema)
	}
}

func TestPRReviewConfig_DefaultEnabled(t *testing.T) {
	cfg := &Config{}
	ApplyDefaults(cfg)
	if !cfg.PRReview.IsEnabled() {
		t.Error("PRReview default should be enabled (opt-out, not opt-in)")
	}
}

func TestPRReviewConfig_ExplicitFalseWins(t *testing.T) {
	// ApplyDefaults must not clobber an explicit `enabled: false` — operator
	// override beats the new default-on behavior.
	cfg := loadFromString(t, `
pr_review:
  enabled: false
`)
	if cfg.PRReview.IsEnabled() {
		t.Error("explicit pr_review.enabled: false should turn the feature off")
	}
}

func TestResolveSecrets_MantisInjected(t *testing.T) {
	cfg := loadFromString(t, `
mantis:
  base_url: https://mantis.example.com
  api_token: mantis-token
`)
	if got := cfg.Secrets["MANTIS_API_URL"]; got != "https://mantis.example.com/api/rest" {
		t.Errorf("MANTIS_API_URL = %q, want https://mantis.example.com/api/rest", got)
	}
	if got := cfg.Secrets["MANTIS_API_TOKEN"]; got != "mantis-token" {
		t.Errorf("MANTIS_API_TOKEN = %q, want mantis-token", got)
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
		t.Errorf("MANTIS_API_TOKEN = %q, want from-secrets (user override preserved)", got)
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
		t.Errorf("error = %v, want message naming both fields", err)
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
