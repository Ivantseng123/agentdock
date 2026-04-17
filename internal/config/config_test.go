package config

import (
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// loadFromString parses YAML and applies defaults. Replaces the old
// Load()/writeAndLoad helpers now that Load() is removed.
func loadFromString(t *testing.T, yamlContent string) *Config {
	t.Helper()
	var cfg Config
	if err := yaml.Unmarshal([]byte(yamlContent), &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	applyDefaults(&cfg)
	return &cfg
}

func TestLoadConfig_V2(t *testing.T) {
	cfg := loadFromString(t, `
slack:
  bot_token: xoxb-test
  app_token: xapp-test

github:
  token: ghp-test

agents:
  claude:
    command: claude
    args: ["--print", "-p", "{prompt}"]
    timeout: 5m
  opencode:
    command: opencode
    args: ["--prompt", "{prompt}"]
    timeout: 3m

active_agent: claude
providers: [claude, opencode]

prompt:
  language: zh-TW

channels:
  C123:
    repos: [owner/repo-a]
    default_labels: [from-slack]
    branch_select: true

channel_defaults:
  default_labels: [default-label]

auto_bind: true

max_concurrent: 5
max_thread_messages: 30

rate_limit:
  per_user: 10
  per_channel: 20
  window: 2m

semaphore_timeout: 45s

mantis:
  base_url: https://mantis.example.com
  api_token: mantis-token

repo_cache:
  dir: /tmp/repos
  max_age: 12h
`)

	// Slack
	if cfg.Slack.BotToken != "xoxb-test" {
		t.Errorf("bot_token = %q", cfg.Slack.BotToken)
	}
	if cfg.Slack.AppToken != "xapp-test" {
		t.Errorf("app_token = %q", cfg.Slack.AppToken)
	}

	// Agents
	if len(cfg.Agents) != 2 {
		t.Fatalf("agents count = %d", len(cfg.Agents))
	}
	claude := cfg.Agents["claude"]
	if claude.Command != "claude" {
		t.Errorf("claude command = %q", claude.Command)
	}
	if claude.Timeout != 5*time.Minute {
		t.Errorf("claude timeout = %v", claude.Timeout)
	}
	if len(claude.Args) != 3 {
		t.Errorf("claude args = %v", claude.Args)
	}

	if cfg.ActiveAgent != "claude" {
		t.Errorf("active_agent = %q", cfg.ActiveAgent)
	}
	if len(cfg.Providers) != 2 || cfg.Providers[0] != "claude" {
		t.Errorf("providers = %v", cfg.Providers)
	}

	// Prompt
	if cfg.Prompt.Language != "zh-TW" {
		t.Errorf("language = %q", cfg.Prompt.Language)
	}

	// Channel
	ch, ok := cfg.Channels["C123"]
	if !ok {
		t.Fatal("channel C123 not found")
	}
	if repos := ch.GetRepos(); len(repos) != 1 || repos[0] != "owner/repo-a" {
		t.Errorf("repos = %v", repos)
	}
	if !ch.IsBranchSelectEnabled() {
		t.Error("branch_select should be true")
	}

	// Concurrency
	if cfg.MaxConcurrent != 5 {
		t.Errorf("max_concurrent = %d", cfg.MaxConcurrent)
	}
	if cfg.MaxThreadMessages != 30 {
		t.Errorf("max_thread_messages = %d", cfg.MaxThreadMessages)
	}
	if cfg.SemaphoreTimeout != 45*time.Second {
		t.Errorf("semaphore_timeout = %v", cfg.SemaphoreTimeout)
	}

	// Rate limit
	if cfg.RateLimit.PerUser != 10 {
		t.Errorf("per_user = %d", cfg.RateLimit.PerUser)
	}
	if cfg.RateLimit.Window != 2*time.Minute {
		t.Errorf("window = %v", cfg.RateLimit.Window)
	}

	// Mantis (top-level)
	if cfg.Mantis.BaseURL != "https://mantis.example.com" {
		t.Errorf("mantis base_url = %q", cfg.Mantis.BaseURL)
	}
	if cfg.Mantis.APIToken != "mantis-token" {
		t.Errorf("mantis api_token = %q", cfg.Mantis.APIToken)
	}

	// Repo cache
	if cfg.RepoCache.Dir != "/tmp/repos" {
		t.Errorf("repo_cache dir = %q", cfg.RepoCache.Dir)
	}
	if cfg.RepoCache.MaxAge != 12*time.Hour {
		t.Errorf("repo_cache max_age = %v", cfg.RepoCache.MaxAge)
	}
}

func TestLoggingConfigDefaults(t *testing.T) {
	cfg := loadFromString(t, `
slack:
  bot_token: "xoxb-test"
  app_token: "xapp-test"
`)
	if cfg.Logging.Dir != "logs" {
		t.Errorf("Logging.Dir = %q, want %q", cfg.Logging.Dir, "logs")
	}
	if cfg.Logging.Level != "debug" {
		t.Errorf("Logging.Level = %q, want %q", cfg.Logging.Level, "debug")
	}
	if cfg.Logging.RetentionDays != 30 {
		t.Errorf("Logging.RetentionDays = %d, want 30", cfg.Logging.RetentionDays)
	}
	if cfg.Logging.AgentOutputDir != "logs/agent-outputs" {
		t.Errorf("Logging.AgentOutputDir = %q, want %q", cfg.Logging.AgentOutputDir, "logs/agent-outputs")
	}
}

func TestLoadConfig_Defaults(t *testing.T) {
	cfg := loadFromString(t, `
slack:
  bot_token: xoxb-test
  app_token: xapp-test
github:
  token: ghp-test
agents:
  claude:
    command: claude
    args: ["--print", "-p", "{prompt}"]
active_agent: claude
`)

	if cfg.MaxConcurrent != 3 {
		t.Errorf("default max_concurrent = %d, want 3", cfg.MaxConcurrent)
	}
	if cfg.MaxThreadMessages != 50 {
		t.Errorf("default max_thread_messages = %d, want 50", cfg.MaxThreadMessages)
	}
	if cfg.SemaphoreTimeout != 30*time.Second {
		t.Errorf("default semaphore_timeout = %v, want 30s", cfg.SemaphoreTimeout)
	}
	if cfg.RateLimit.Window != time.Minute {
		t.Errorf("default rate_limit.window = %v, want 1m", cfg.RateLimit.Window)
	}
	claude := cfg.Agents["claude"]
	if claude.Timeout != 5*time.Minute {
		t.Errorf("default agent timeout = %v, want 5m", claude.Timeout)
	}
}

func TestLoad_QueueConfig(t *testing.T) {
	cfg := loadFromString(t, `
queue:
  capacity: 100
  transport: inmem
channel_priority:
  C_INCIDENTS: 100
  C_ONCALL: 80
worker:
  count: 5
attachments:
  store: local
  temp_dir: /tmp/test-attach
  ttl: 15m
agents:
  claude:
    command: claude
    args: ["--print"]
    skill_dir: ".claude/skills"
`)
	if cfg.Queue.Capacity != 100 {
		t.Errorf("queue capacity = %d, want 100", cfg.Queue.Capacity)
	}
	if cfg.Worker.Count != 5 {
		t.Errorf("workers count = %d, want 5", cfg.Worker.Count)
	}
	pri, ok := cfg.ChannelPriority["C_INCIDENTS"]
	if !ok || pri != 100 {
		t.Errorf("channel priority = %d, want 100", pri)
	}
	agent := cfg.Agents["claude"]
	if agent.SkillDir != ".claude/skills" {
		t.Errorf("skill_dir = %q", agent.SkillDir)
	}
	if cfg.Attachments.TempDir != "/tmp/test-attach" {
		t.Errorf("temp_dir = %q", cfg.Attachments.TempDir)
	}
	if cfg.Attachments.TTL != 15*time.Minute {
		t.Errorf("ttl = %v", cfg.Attachments.TTL)
	}
}

func TestLoad_QueueDefaults(t *testing.T) {
	cfg := loadFromString(t, `
agents:
  claude:
    command: claude
`)
	if cfg.Queue.Capacity != 50 {
		t.Errorf("default queue capacity = %d, want 50", cfg.Queue.Capacity)
	}
	if cfg.Worker.Count != 3 {
		t.Errorf("default workers count = %d, want 3", cfg.Worker.Count)
	}
}

func TestLoad_MaxConcurrentBackwardCompat(t *testing.T) {
	cfg := loadFromString(t, `
max_concurrent: 7
agents:
  claude:
    command: claude
`)
	if cfg.Worker.Count != 7 {
		t.Errorf("workers count = %d, want 7 (from max_concurrent)", cfg.Worker.Count)
	}
}

func TestLoad_AgentStream(t *testing.T) {
	cfg := loadFromString(t, `
agents:
  claude:
    command: claude
    args: ["--print", "--output-format", "stream-json", "-p", "{prompt}"]
    stream: true
  opencode:
    command: opencode
    args: ["--prompt", "{prompt}"]
`)
	if !cfg.Agents["claude"].Stream {
		t.Error("claude stream should be true")
	}
	if cfg.Agents["opencode"].Stream {
		t.Error("opencode stream should be false")
	}
}

func TestLoad_TrackingTimeouts(t *testing.T) {
	cfg := loadFromString(t, `
queue:
  agent_idle_timeout: 3m
  prepare_timeout: 2m
  status_interval: 10s
agents:
  claude:
    command: claude
`)
	if cfg.Queue.AgentIdleTimeout != 3*time.Minute {
		t.Errorf("agent_idle_timeout = %v", cfg.Queue.AgentIdleTimeout)
	}
	if cfg.Queue.PrepareTimeout != 2*time.Minute {
		t.Errorf("prepare_timeout = %v", cfg.Queue.PrepareTimeout)
	}
	if cfg.Queue.StatusInterval != 10*time.Second {
		t.Errorf("status_interval = %v", cfg.Queue.StatusInterval)
	}
}

func TestLoad_TrackingTimeoutDefaults(t *testing.T) {
	cfg := loadFromString(t, `
agents:
  claude:
    command: claude
`)
	if cfg.Queue.AgentIdleTimeout != 5*time.Minute {
		t.Errorf("default agent_idle_timeout = %v, want 5m", cfg.Queue.AgentIdleTimeout)
	}
	if cfg.Queue.PrepareTimeout != 3*time.Minute {
		t.Errorf("default prepare_timeout = %v, want 3m", cfg.Queue.PrepareTimeout)
	}
	if cfg.Queue.StatusInterval != 5*time.Second {
		t.Errorf("default status_interval = %v, want 5s", cfg.Queue.StatusInterval)
	}
}

func TestEnvOverrideMap(t *testing.T) {
	t.Setenv("REDIS_ADDR", "10.0.0.1:6379")
	t.Setenv("GITHUB_TOKEN", "ghp_test")
	t.Setenv("PROVIDERS", "claude,codex")

	m := EnvOverrideMap()

	if got := m["redis.addr"]; got != "10.0.0.1:6379" {
		t.Errorf("redis.addr = %v, want 10.0.0.1:6379", got)
	}
	if got := m["github.token"]; got != "ghp_test" {
		t.Errorf("github.token = %v, want ghp_test", got)
	}
	providers, ok := m["providers"].([]string)
	if !ok || len(providers) != 2 || providers[0] != "claude" || providers[1] != "codex" {
		t.Errorf("providers = %v, want [claude codex]", m["providers"])
	}
}

func TestEnvOverrideMap_UnsetAbsent(t *testing.T) {
	t.Setenv("REDIS_ADDR", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("PROVIDERS", "")
	t.Setenv("SLACK_BOT_TOKEN", "")
	t.Setenv("SLACK_APP_TOKEN", "")
	t.Setenv("MANTIS_API_TOKEN", "")
	t.Setenv("REDIS_PASSWORD", "")
	t.Setenv("ACTIVE_AGENT", "")

	m := EnvOverrideMap()
	for _, key := range []string{"redis.addr", "github.token", "providers", "slack.bot_token"} {
		if _, ok := m[key]; ok {
			t.Errorf("%s should be absent when env empty, got %v", key, m[key])
		}
	}
}

func TestEnvOverrideMap_ProvidersFiltersEmpty(t *testing.T) {
	t.Setenv("PROVIDERS", "claude,,codex,")
	m := EnvOverrideMap()
	providers, ok := m["providers"].([]string)
	if !ok {
		t.Fatalf("providers missing or wrong type: %v", m["providers"])
	}
	if len(providers) != 2 || providers[0] != "claude" || providers[1] != "codex" {
		t.Errorf("providers should filter empty tokens, got %v", providers)
	}
}

func TestDefaultsMap(t *testing.T) {
	m := DefaultsMap()

	worker, ok := m["worker"].(map[string]any)
	if !ok {
		t.Fatalf("worker should be a map, got %T", m["worker"])
	}
	if got := worker["count"]; got != 3 {
		t.Errorf("worker.count = %v, want 3", got)
	}

	queue, ok := m["queue"].(map[string]any)
	if !ok {
		t.Fatalf("queue should be a map, got %T", m["queue"])
	}
	if got := queue["transport"]; got != "inmem" {
		t.Errorf("queue.transport = %v, want inmem", got)
	}
	if got := queue["capacity"]; got != 50 {
		t.Errorf("queue.capacity = %v, want 50", got)
	}

	logging, ok := m["logging"].(map[string]any)
	if !ok {
		t.Fatalf("logging should be a map, got %T", m["logging"])
	}
	if got := logging["dir"]; got != "logs" {
		t.Errorf("logging.dir = %v, want logs", got)
	}
}

func TestDefaultsMap_AgreesWithApplyDefaults(t *testing.T) {
	var cfg Config
	applyDefaults(&cfg)

	m := DefaultsMap()
	worker := m["worker"].(map[string]any)
	if got := worker["count"]; got != cfg.Worker.Count {
		t.Errorf("DefaultsMap.worker.count=%v != applyDefaults.Worker.Count=%v", got, cfg.Worker.Count)
	}
}

func TestResolveSecrets_GitHubTokenAutoMerge(t *testing.T) {
	cfg := &Config{
		GitHub: GitHubConfig{Token: "ghp_from_github"},
	}
	resolveSecrets(cfg)
	if cfg.Secrets["GH_TOKEN"] != "ghp_from_github" {
		t.Errorf("got %q, want ghp_from_github", cfg.Secrets["GH_TOKEN"])
	}
}

func TestResolveSecrets_ExplicitSecretsWin(t *testing.T) {
	cfg := &Config{
		GitHub:  GitHubConfig{Token: "ghp_from_github"},
		Secrets: map[string]string{"GH_TOKEN": "ghp_explicit"},
	}
	resolveSecrets(cfg)
	if cfg.Secrets["GH_TOKEN"] != "ghp_explicit" {
		t.Errorf("got %q, want ghp_explicit", cfg.Secrets["GH_TOKEN"])
	}
}

func TestResolveSecrets_InitializesNilMap(t *testing.T) {
	cfg := &Config{}
	resolveSecrets(cfg)
	if cfg.Secrets == nil {
		t.Error("Secrets should be initialized to non-nil map")
	}
}

func TestEnvOverrideMap_SecretKey(t *testing.T) {
	t.Setenv("SECRET_KEY", "abc123")
	m := EnvOverrideMap()
	if m["secret_key"] != "abc123" {
		t.Errorf("got %q, want abc123", m["secret_key"])
	}
}

func TestScanSecretEnvVars(t *testing.T) {
	t.Setenv("AGENTDOCK_SECRET_K8S_TOKEN", "k8s-val")
	t.Setenv("AGENTDOCK_SECRET_NPM_TOKEN", "npm-val")
	t.Setenv("UNRELATED_VAR", "ignore")

	got := ScanSecretEnvVars()
	if got["K8S_TOKEN"] != "k8s-val" {
		t.Errorf("K8S_TOKEN = %q, want k8s-val", got["K8S_TOKEN"])
	}
	if got["NPM_TOKEN"] != "npm-val" {
		t.Errorf("NPM_TOKEN = %q, want npm-val", got["NPM_TOKEN"])
	}
	if _, exists := got["UNRELATED_VAR"]; exists {
		t.Error("should not include UNRELATED_VAR")
	}
}

func TestValidateSecretKey_Valid(t *testing.T) {
	key := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	_, err := DecodeSecretKey(key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateSecretKey_Invalid(t *testing.T) {
	cases := []string{
		"tooshort",
		"not-hex-not-hex-not-hex-not-hex-not-hex-not-hex-not-hex-not-hex",
		"",
	}
	for _, c := range cases {
		if _, err := DecodeSecretKey(c); err == nil {
			t.Errorf("expected error for %q", c)
		}
	}
}

func TestLoad_CancelTimeoutDefault(t *testing.T) {
	cfg := loadFromString(t, `
agents:
  claude:
    command: claude
`)
	if cfg.Queue.CancelTimeout != 60*time.Second {
		t.Errorf("default cancel_timeout = %v, want 60s", cfg.Queue.CancelTimeout)
	}
}

func TestLoad_CancelTimeoutOverride(t *testing.T) {
	cfg := loadFromString(t, `
queue:
  cancel_timeout: 20s
agents:
  claude:
    command: claude
`)
	if cfg.Queue.CancelTimeout != 20*time.Second {
		t.Errorf("cancel_timeout = %v, want 20s", cfg.Queue.CancelTimeout)
	}
}

func TestPromptConfig_NewFields_YAMLLoad(t *testing.T) {
	yaml := `
prompt:
  language: zh-TW
  goal: "custom goal"
  output_rules:
    - "one"
    - "two"
  allow_worker_rules: false
`
	cfg := loadFromString(t, yaml)
	if cfg.Prompt.Goal != "custom goal" {
		t.Errorf("Goal = %q, want 'custom goal'", cfg.Prompt.Goal)
	}
	if len(cfg.Prompt.OutputRules) != 2 || cfg.Prompt.OutputRules[0] != "one" {
		t.Errorf("OutputRules = %v, want [one two]", cfg.Prompt.OutputRules)
	}
	if cfg.Prompt.AllowWorkerRules == nil {
		t.Fatal("AllowWorkerRules is nil after YAML explicitly set false")
	}
	if *cfg.Prompt.AllowWorkerRules {
		t.Error("*AllowWorkerRules = true, want false")
	}
}

func TestPromptConfig_Defaults(t *testing.T) {
	cfg := loadFromString(t, "")

	if cfg.Prompt.Goal != defaultPromptGoal {
		t.Errorf("default Goal = %q, want %q", cfg.Prompt.Goal, defaultPromptGoal)
	}
	if cfg.Prompt.OutputRules == nil || len(cfg.Prompt.OutputRules) != 0 {
		t.Errorf("default OutputRules = %v, want empty non-nil", cfg.Prompt.OutputRules)
	}
	if cfg.Prompt.AllowWorkerRules == nil || !*cfg.Prompt.AllowWorkerRules {
		t.Errorf("default AllowWorkerRules = %v, want &true", cfg.Prompt.AllowWorkerRules)
	}
}

func TestPromptConfig_IsWorkerRulesAllowed(t *testing.T) {
	t.Run("nil_pointer_defaults_true", func(t *testing.T) {
		pc := PromptConfig{AllowWorkerRules: nil}
		if !pc.IsWorkerRulesAllowed() {
			t.Error("nil should default to true")
		}
	})
	t.Run("explicit_true", func(t *testing.T) {
		v := true
		pc := PromptConfig{AllowWorkerRules: &v}
		if !pc.IsWorkerRulesAllowed() {
			t.Error("explicit true should return true")
		}
	})
	t.Run("explicit_false", func(t *testing.T) {
		v := false
		pc := PromptConfig{AllowWorkerRules: &v}
		if pc.IsWorkerRulesAllowed() {
			t.Error("explicit false should return false")
		}
	})
}

func TestWorkerConfig_NewSection_YAMLLoad(t *testing.T) {
	yaml := `
worker:
  count: 7
  prompt:
    extra_rules:
      - "no guessing"
      - "only real files"
`
	cfg := loadFromString(t, yaml)
	if cfg.Worker.Count != 7 {
		t.Errorf("Worker.Count = %d, want 7", cfg.Worker.Count)
	}
	if len(cfg.Worker.Prompt.ExtraRules) != 2 {
		t.Errorf("ExtraRules len = %d, want 2", len(cfg.Worker.Prompt.ExtraRules))
	}
	if cfg.Worker.Prompt.ExtraRules[0] != "no guessing" {
		t.Errorf("ExtraRules[0] = %q, want 'no guessing'", cfg.Worker.Prompt.ExtraRules[0])
	}
}
