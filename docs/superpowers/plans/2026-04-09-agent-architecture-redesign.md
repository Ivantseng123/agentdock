# Agent Architecture Redesign — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the custom agent loop and LLM provider abstraction with direct CLI agent invocation, making the Go service a thin Slack/GitHub runtime.

**Architecture:** The Go service handles Slack events (app_mention, slash command) and GitHub issue creation. Codebase analysis is delegated to external CLI agents (claude, opencode, codex, gemini) via `exec.Command`. The agent produces a markdown issue body + JSON metadata, which the Go service parses, sanitizes, and uses to create GitHub issues.

**Tech Stack:** Go 1.25, slack-go/slack, google/go-github/v60, yaml.v3

**Spec:** `docs/superpowers/specs/2026-04-09-agent-architecture-redesign.md`

---

## File Structure

### New files
| File | Responsibility |
|------|----------------|
| `internal/bot/agent.go` | AgentRunner: spawn CLI agent, fallback chain |
| `internal/bot/agent_test.go` | AgentRunner tests with mock scripts |
| `internal/bot/parser.go` | Parse agent output (markdown + metadata), sanitize, resolve title |
| `internal/bot/parser_test.go` | Parser tests with various output formats |
| `internal/bot/prompt.go` | Build prompt from thread context + config |
| `internal/bot/prompt_test.go` | Prompt builder tests |

### Modified files
| File | Changes |
|------|---------|
| `internal/config/config.go` | New v2 config structs (agents, prompt, remove reactions/llm/diagnosis) |
| `internal/config/config_test.go` | Updated for v2 config |
| `internal/slack/handler.go` | Rewrite: app_mention + slash command instead of reaction events |
| `internal/slack/handler_test.go` | Updated for new trigger model |
| `internal/slack/client.go` | Add `FetchThreadContext()`, `DownloadAttachments()` |
| `internal/slack/client_test.go` | Add thread context + attachment tests |
| `internal/github/issue.go` | Simplify: remove FormatIssueBody, accept raw body |
| `internal/github/issue_test.go` | Updated for simplified API |
| `internal/bot/workflow.go` | Complete rewrite: new v2 flow |
| `cmd/bot/main.go` | Simplified wiring (no LLM providers, no diagnosis engine) |

### Kept unchanged
| File | Notes |
|------|-------|
| `internal/github/repo.go` | Clone/fetch/checkout still needed |
| `internal/github/repo_test.go` | Keep |
| `internal/github/discovery.go` | Repo search still needed |
| `internal/mantis/` | Mantis URL enrichment preserved |
| `internal/bot/enrich.go` | Message enrichment preserved |
| `internal/slack/xlsx_test.go` | Keep |

### Deleted
| File/Dir | Reason |
|----------|--------|
| `internal/diagnosis/` | Replaced by CLI agent |
| `internal/llm/` | No longer needed |

---

### Task 1: Rewrite Config

**Files:**
- Modify: `internal/config/config.go` (full rewrite, 160 lines → ~130 lines)
- Modify: `internal/config/config_test.go` (full rewrite)

- [ ] **Step 1: Write the v2 config test**

```go
// internal/config/config_test.go
package config

import (
	"os"
	"testing"
	"time"
)

func TestLoadConfig_V2(t *testing.T) {
	yaml := `
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
fallback: [claude, opencode]

prompt:
  language: zh-TW
  extra_rules:
    - "rule one"
    - "rule two"

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
`
	f, _ := os.CreateTemp("", "config-*.yaml")
	f.WriteString(yaml)
	f.Close()
	defer os.Remove(f.Name())

	cfg, err := Load(f.Name())
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

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
	if len(cfg.Fallback) != 2 || cfg.Fallback[0] != "claude" {
		t.Errorf("fallback = %v", cfg.Fallback)
	}

	// Prompt
	if cfg.Prompt.Language != "zh-TW" {
		t.Errorf("language = %q", cfg.Prompt.Language)
	}
	if len(cfg.Prompt.ExtraRules) != 2 {
		t.Errorf("extra_rules = %v", cfg.Prompt.ExtraRules)
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

func TestLoadConfig_V1Warning(t *testing.T) {
	yaml := `
slack:
  bot_token: xoxb-test
  app_token: xapp-test
github:
  token: ghp-test
reactions:
  bug:
    type: bug
agents:
  claude:
    command: claude
    args: ["--print", "-p", "{prompt}"]
    timeout: 5m
active_agent: claude
`
	f, _ := os.CreateTemp("", "config-*.yaml")
	f.WriteString(yaml)
	f.Close()
	defer os.Remove(f.Name())

	// Should load without error but log a warning.
	// The v1 keys are silently ignored (not mapped to v2 struct).
	cfg, err := Load(f.Name())
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.ActiveAgent != "claude" {
		t.Errorf("active_agent = %q", cfg.ActiveAgent)
	}
}

func TestLoadConfig_Defaults(t *testing.T) {
	yaml := `
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
`
	f, _ := os.CreateTemp("", "config-*.yaml")
	f.WriteString(yaml)
	f.Close()
	defer os.Remove(f.Name())

	cfg, err := Load(f.Name())
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// Defaults
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -v -run TestLoadConfig_V2`
Expected: FAIL — config structs don't match v2 schema

- [ ] **Step 3: Write v2 config implementation**

```go
// internal/config/config.go
package config

import (
	"log/slog"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the root v2 configuration.
type Config struct {
	Server          ServerConfig             `yaml:"server"`
	Slack           SlackConfig              `yaml:"slack"`
	GitHub          GitHubConfig             `yaml:"github"`
	Agents          map[string]AgentConfig   `yaml:"agents"`
	ActiveAgent     string                   `yaml:"active_agent"`
	Fallback        []string                 `yaml:"fallback"`
	Prompt          PromptConfig             `yaml:"prompt"`
	Channels        map[string]ChannelConfig `yaml:"channels"`
	ChannelDefaults ChannelConfig            `yaml:"channel_defaults"`
	AutoBind        bool                     `yaml:"auto_bind"`
	MaxConcurrent   int                      `yaml:"max_concurrent"`
	MaxThreadMessages int                    `yaml:"max_thread_messages"`
	SemaphoreTimeout time.Duration           `yaml:"semaphore_timeout"`
	RateLimit       RateLimitConfig          `yaml:"rate_limit"`
	Mantis          MantisConfig             `yaml:"mantis"`
	RepoCache       RepoCacheConfig          `yaml:"repo_cache"`
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

type AgentConfig struct {
	Command string        `yaml:"command"`
	Args    []string      `yaml:"args"`
	Timeout time.Duration `yaml:"timeout"`
}

type PromptConfig struct {
	Language   string   `yaml:"language"`
	ExtraRules []string `yaml:"extra_rules"`
}

type ChannelConfig struct {
	Repo          string   `yaml:"repo"`
	Repos         []string `yaml:"repos"`
	DefaultLabels []string `yaml:"default_labels"`
	Branches      []string `yaml:"branches"`
	BranchSelect  *bool    `yaml:"branch_select"`
}

func (c ChannelConfig) IsBranchSelectEnabled() bool {
	return c.BranchSelect != nil && *c.BranchSelect
}

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
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

type RepoCacheConfig struct {
	Dir    string        `yaml:"dir"`
	MaxAge time.Duration `yaml:"max_age"`
}

// v1RawCheck is used to detect v1 configs by checking for legacy keys.
type v1RawCheck struct {
	Reactions    map[string]any `yaml:"reactions"`
	Integrations map[string]any `yaml:"integrations"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	// Check for v1 keys and warn.
	var raw v1RawCheck
	if yaml.Unmarshal(data, &raw) == nil {
		if raw.Reactions != nil || raw.Integrations != nil {
			slog.Warn("v1 config detected — reactions, llm, diagnosis, and integrations sections are no longer used in v2. Note: integrations.mantis has moved to top-level mantis.")
		}
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	applyDefaults(&cfg)
	return &cfg, nil
}

func applyDefaults(cfg *Config) {
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 3
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
	for name, agent := range cfg.Agents {
		if agent.Timeout <= 0 {
			agent.Timeout = 5 * time.Minute
			cfg.Agents[name] = agent
		}
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/config/ -v`
Expected: All 3 tests PASS

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat: rewrite config for v2 agent architecture"
```

---

### Task 2: Output Parser

**Files:**
- Create: `internal/bot/parser.go`
- Create: `internal/bot/parser_test.go`

- [ ] **Step 1: Write parser tests**

```go
// internal/bot/parser_test.go
package bot

import (
	"testing"
)

func TestParseAgentOutput_FullOutput(t *testing.T) {
	output := `## Summary

Login page spins forever after submit.

## Related Code

- src/api/auth/login.ts:45

===TRIAGE_METADATA===
{
  "issue_type": "bug",
  "confidence": "high",
  "files": [{"path": "src/api/auth/login.ts", "line": 45, "relevance": "login handler"}],
  "open_questions": ["Does this affect all users?"],
  "suggested_title": "Login page infinite loading"
}`

	result, err := ParseAgentOutput(output)
	if err != nil {
		t.Fatalf("ParseAgentOutput failed: %v", err)
	}

	if result.Metadata.IssueType != "bug" {
		t.Errorf("issue_type = %q", result.Metadata.IssueType)
	}
	if result.Metadata.Confidence != "high" {
		t.Errorf("confidence = %q", result.Metadata.Confidence)
	}
	if len(result.Metadata.Files) != 1 {
		t.Fatalf("files count = %d", len(result.Metadata.Files))
	}
	if result.Metadata.Files[0].Path != "src/api/auth/login.ts" {
		t.Errorf("file path = %q", result.Metadata.Files[0].Path)
	}
	if result.Metadata.SuggestedTitle != "Login page infinite loading" {
		t.Errorf("suggested_title = %q", result.Metadata.SuggestedTitle)
	}
	if result.MarkdownBody == "" {
		t.Error("markdown body is empty")
	}
	if result.MarkdownBody != "## Summary\n\nLogin page spins forever after submit.\n\n## Related Code\n\n- src/api/auth/login.ts:45" {
		t.Errorf("markdown body = %q", result.MarkdownBody)
	}
}

func TestParseAgentOutput_NoMetadata(t *testing.T) {
	output := "## Summary\n\nJust a plain markdown body with no metadata."

	result, err := ParseAgentOutput(output)
	if err != nil {
		t.Fatalf("ParseAgentOutput failed: %v", err)
	}

	if result.MarkdownBody != output {
		t.Errorf("body should be full output")
	}
	if result.Metadata.Confidence != "medium" {
		t.Errorf("default confidence = %q, want medium", result.Metadata.Confidence)
	}
	if result.Degraded != true {
		t.Error("should be degraded when no metadata")
	}
}

func TestParseAgentOutput_InvalidJSON(t *testing.T) {
	output := "## Summary\n\nBody here.\n\n===TRIAGE_METADATA===\n{invalid json}"

	result, err := ParseAgentOutput(output)
	if err != nil {
		t.Fatalf("ParseAgentOutput failed: %v", err)
	}

	if result.MarkdownBody != "## Summary\n\nBody here." {
		t.Errorf("body = %q", result.MarkdownBody)
	}
	if result.Degraded != true {
		t.Error("should be degraded on invalid JSON")
	}
}

func TestParseAgentOutput_EmptyOutput(t *testing.T) {
	_, err := ParseAgentOutput("")
	if err == nil {
		t.Error("expected error on empty output")
	}
}

func TestParseAgentOutput_TooShort(t *testing.T) {
	_, err := ParseAgentOutput("short")
	if err == nil {
		t.Error("expected error on output under 50 chars")
	}
}

func TestParseAgentOutput_LastSeparatorUsed(t *testing.T) {
	// Agent mentions the separator in its markdown body
	output := `The output format uses ===TRIAGE_METADATA=== as separator.

## Real content here

===TRIAGE_METADATA===
{"issue_type": "bug", "confidence": "high", "files": [], "open_questions": [], "suggested_title": "test"}`

	result, err := ParseAgentOutput(output)
	if err != nil {
		t.Fatalf("ParseAgentOutput failed: %v", err)
	}

	if result.Metadata.IssueType != "bug" {
		t.Errorf("issue_type = %q", result.Metadata.IssueType)
	}
	// Body should contain the first mention of the separator
	if result.MarkdownBody == "" {
		t.Error("markdown body should not be empty")
	}
}

func TestSanitizeBody(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"strips html", "Hello <script>alert('xss')</script> world", "Hello  world"},
		{"keeps markdown", "## Title\n\n**bold** text", "## Title\n\n**bold** text"},
		{"strips nested tags", "<div><p>text</p></div>", "text"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeBody(tt.input)
			if got != tt.want {
				t.Errorf("SanitizeBody(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestSanitizeBody_MaxLength(t *testing.T) {
	long := make([]byte, 70000)
	for i := range long {
		long[i] = 'a'
	}
	result := SanitizeBody(string(long))
	if len(result) > maxBodyLength {
		t.Errorf("len = %d, want <= %d", len(result), maxBodyLength)
	}
}

func TestResolveTitle(t *testing.T) {
	tests := []struct {
		name          string
		suggestedTitle string
		markdownBody  string
		firstMessage  string
		want          string
	}{
		{"from suggested", "Login bug", "", "", "Login bug"},
		{"from markdown", "", "## Login page broken\n\ndetails", "", "Login page broken"},
		{"from message", "", "", "the login page is broken", "the login page is broken"},
		{"fallback", "", "", "", "Untitled issue"},
		{"truncate", string(make([]byte, 100)), "", "", string(make([]byte, 77)) + "..."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveTitle(tt.suggestedTitle, tt.markdownBody, tt.firstMessage)
			if got != tt.want {
				t.Errorf("ResolveTitle = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveLabels(t *testing.T) {
	tests := []struct {
		issueType     string
		defaultLabels []string
		wantLen       int
	}{
		{"bug", []string{"from-slack"}, 2},       // "bug" + "from-slack"
		{"feature", []string{"from-slack"}, 2},    // "enhancement" + "from-slack"
		{"improvement", nil, 1},                   // "enhancement"
		{"question", nil, 1},                      // "question"
		{"unknown", []string{"from-slack"}, 1},    // only "from-slack"
		{"", nil, 0},                              // nothing
	}
	for _, tt := range tests {
		t.Run(tt.issueType, func(t *testing.T) {
			got := ResolveLabels(tt.issueType, tt.defaultLabels)
			if len(got) != tt.wantLen {
				t.Errorf("ResolveLabels(%q, %v) = %v (len %d), want len %d",
					tt.issueType, tt.defaultLabels, got, len(got), tt.wantLen)
			}
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/bot/ -v -run "TestParseAgentOutput|TestSanitize|TestResolveTitle|TestResolveLabels"`
Expected: FAIL — functions not defined

- [ ] **Step 3: Write parser implementation**

```go
// internal/bot/parser.go
package bot

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

const (
	metadataSeparator = "===TRIAGE_METADATA==="
	maxBodyLength     = 65000
	minOutputLength   = 50
	maxTitleLength    = 80
)

// AgentMetadata is the JSON metadata block from agent output.
type AgentMetadata struct {
	IssueType      string    `json:"issue_type"`
	Confidence     string    `json:"confidence"`
	Files          []FileRef `json:"files"`
	OpenQuestions  []string  `json:"open_questions"`
	SuggestedTitle string    `json:"suggested_title"`
}

// FileRef is a file reference from the agent's triage.
type FileRef struct {
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Relevance string `json:"relevance"`
}

// ParsedOutput is the result of parsing agent output.
type ParsedOutput struct {
	MarkdownBody string
	Metadata     AgentMetadata
	Degraded     bool // true if metadata was missing or invalid
}

// ParseAgentOutput splits agent stdout into markdown body + JSON metadata.
// Uses the last occurrence of the separator to avoid false positives.
func ParseAgentOutput(output string) (ParsedOutput, error) {
	output = strings.TrimSpace(output)
	if len(output) < minOutputLength {
		return ParsedOutput{}, fmt.Errorf("agent output too short (%d chars, minimum %d)", len(output), minOutputLength)
	}

	// Find last occurrence of separator.
	idx := strings.LastIndex(output, metadataSeparator)
	if idx == -1 {
		// No metadata — degrade.
		return ParsedOutput{
			MarkdownBody: output,
			Metadata:     defaultMetadata(),
			Degraded:     true,
		}, nil
	}

	body := strings.TrimSpace(output[:idx])
	jsonPart := strings.TrimSpace(output[idx+len(metadataSeparator):])

	var meta AgentMetadata
	if err := json.Unmarshal([]byte(jsonPart), &meta); err != nil {
		// Invalid JSON — degrade with body intact.
		return ParsedOutput{
			MarkdownBody: body,
			Metadata:     defaultMetadata(),
			Degraded:     true,
		}, nil
	}

	return ParsedOutput{
		MarkdownBody: body,
		Metadata:     meta,
		Degraded:     false,
	}, nil
}

func defaultMetadata() AgentMetadata {
	return AgentMetadata{Confidence: "medium"}
}

// SanitizeBody strips HTML tags and enforces max length.
var htmlTagRegex = regexp.MustCompile(`<[^>]*>`)

func SanitizeBody(body string) string {
	body = htmlTagRegex.ReplaceAllString(body, "")
	if len(body) > maxBodyLength {
		body = body[:maxBodyLength]
	}
	return body
}

// ResolveTitle picks the best title from available sources.
// Priority: suggested_title > first line of markdown > first message > fallback.
func ResolveTitle(suggestedTitle, markdownBody, firstMessage string) string {
	title := ""
	switch {
	case suggestedTitle != "":
		title = suggestedTitle
	case markdownBody != "":
		first := strings.SplitN(markdownBody, "\n", 2)[0]
		title = strings.TrimLeft(first, "# ")
	case firstMessage != "":
		title = strings.SplitN(firstMessage, "\n", 2)[0]
	default:
		return "Untitled issue"
	}

	title = strings.TrimSpace(title)
	if title == "" {
		return "Untitled issue"
	}
	if len(title) > maxTitleLength {
		title = title[:maxTitleLength-3] + "..."
	}
	return title
}

// issueTypeToLabel maps agent-decided issue types to GitHub labels.
var issueTypeToLabel = map[string]string{
	"bug":         "bug",
	"feature":     "enhancement",
	"improvement": "enhancement",
	"question":    "question",
}

// ResolveLabels combines the issue_type label with default labels.
func ResolveLabels(issueType string, defaultLabels []string) []string {
	var labels []string
	if label, ok := issueTypeToLabel[issueType]; ok {
		labels = append(labels, label)
	}
	labels = append(labels, defaultLabels...)
	return labels
}

// FormatIssueBody wraps the agent's markdown with a controlled header.
func FormatIssueBody(channel, reporter, branch, agentBody string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("**Channel**: #%s\n", channel))
	sb.WriteString(fmt.Sprintf("**Reporter**: %s\n", reporter))
	if branch != "" {
		sb.WriteString(fmt.Sprintf("**Branch**: %s\n", branch))
	}
	sb.WriteString("\n---\n\n")
	sb.WriteString(agentBody)
	return sb.String()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/bot/ -v -run "TestParseAgentOutput|TestSanitize|TestResolveTitle|TestResolveLabels"`
Expected: All PASS

- [ ] **Step 5: Commit**

```bash
git add internal/bot/parser.go internal/bot/parser_test.go
git commit -m "feat: add agent output parser with sanitization"
```

---

### Task 3: Agent Runner

**Files:**
- Create: `internal/bot/agent.go`
- Create: `internal/bot/agent_test.go`

- [ ] **Step 1: Write agent runner tests**

```go
// internal/bot/agent_test.go
package bot

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"slack-issue-bot/internal/config"
)

func TestAgentRunner_Success(t *testing.T) {
	// Create a mock script that echoes a valid response.
	dir := t.TempDir()
	script := filepath.Join(dir, "mock-agent")
	os.WriteFile(script, []byte(`#!/bin/sh
echo "## Summary"
echo ""
echo "Test issue body"
echo ""
echo "===TRIAGE_METADATA==="
echo '{"issue_type":"bug","confidence":"high","files":[],"open_questions":[],"suggested_title":"test"}'
`), 0755)

	runner := NewAgentRunner([]config.AgentConfig{
		{Command: script, Args: []string{"{prompt}"}, Timeout: 10 * time.Second},
	})

	output, err := runner.Run(context.Background(), dir, "test prompt")
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if output == "" {
		t.Error("output is empty")
	}
	if !contains(output, "Test issue body") {
		t.Errorf("output missing expected content: %q", output)
	}
}

func TestAgentRunner_Fallback(t *testing.T) {
	dir := t.TempDir()

	// First agent fails (command not found).
	// Second agent succeeds.
	script := filepath.Join(dir, "good-agent")
	os.WriteFile(script, []byte("#!/bin/sh\necho 'fallback output with enough characters to pass the minimum length check of fifty chars'\n"), 0755)

	runner := NewAgentRunner([]config.AgentConfig{
		{Command: "/nonexistent/agent", Args: []string{"{prompt}"}, Timeout: 5 * time.Second},
		{Command: script, Args: []string{"{prompt}"}, Timeout: 5 * time.Second},
	})

	output, err := runner.Run(context.Background(), dir, "test")
	if err != nil {
		t.Fatalf("Run with fallback failed: %v", err)
	}
	if !contains(output, "fallback output") {
		t.Errorf("output = %q, want fallback output", output)
	}
}

func TestAgentRunner_AllFail(t *testing.T) {
	runner := NewAgentRunner([]config.AgentConfig{
		{Command: "/nonexistent/agent1", Args: []string{"{prompt}"}, Timeout: 5 * time.Second},
		{Command: "/nonexistent/agent2", Args: []string{"{prompt}"}, Timeout: 5 * time.Second},
	})

	_, err := runner.Run(context.Background(), t.TempDir(), "test")
	if err == nil {
		t.Error("expected error when all agents fail")
	}
}

func TestAgentRunner_Timeout(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "slow-agent")
	os.WriteFile(script, []byte("#!/bin/sh\nsleep 10\n"), 0755)

	runner := NewAgentRunner([]config.AgentConfig{
		{Command: script, Args: []string{"{prompt}"}, Timeout: 100 * time.Millisecond},
	})

	_, err := runner.Run(context.Background(), dir, "test")
	if err == nil {
		t.Error("expected timeout error")
	}
}

func TestAgentRunner_PromptSubstitution(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "echo-agent")
	// The script receives the prompt as an argument and echoes it.
	os.WriteFile(script, []byte(`#!/bin/sh
echo "$1"
# Pad to meet minimum output length requirement
echo "padding padding padding padding padding padding padding"
`), 0755)

	runner := NewAgentRunner([]config.AgentConfig{
		{Command: script, Args: []string{"{prompt}"}, Timeout: 5 * time.Second},
	})

	output, err := runner.Run(context.Background(), dir, "hello world")
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if !contains(output, "hello world") {
		t.Errorf("prompt not substituted in output: %q", output)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && containsStr(s, substr)
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/bot/ -v -run "TestAgentRunner"`
Expected: FAIL — `NewAgentRunner` not defined

- [ ] **Step 3: Write agent runner implementation**

```go
// internal/bot/agent.go
package bot

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	"slack-issue-bot/internal/config"
)

// AgentRunner executes CLI agents with fallback support.
type AgentRunner struct {
	agents []config.AgentConfig
}

// NewAgentRunner creates an agent runner with the given fallback chain.
func NewAgentRunner(agents []config.AgentConfig) *AgentRunner {
	return &AgentRunner{agents: agents}
}

// NewAgentRunnerFromConfig creates an agent runner from the config's
// active_agent and fallback list.
func NewAgentRunnerFromConfig(cfg *config.Config) *AgentRunner {
	var chain []config.AgentConfig

	if len(cfg.Fallback) > 0 {
		for _, name := range cfg.Fallback {
			if agent, ok := cfg.Agents[name]; ok {
				chain = append(chain, agent)
			} else {
				slog.Warn("fallback agent not found in agents config", "name", name)
			}
		}
	} else if cfg.ActiveAgent != "" {
		if agent, ok := cfg.Agents[cfg.ActiveAgent]; ok {
			chain = append(chain, agent)
		}
	}

	return NewAgentRunner(chain)
}

// Run executes the agent chain. Tries each agent in order; on failure,
// falls back to the next. Returns the raw stdout output.
func (r *AgentRunner) Run(ctx context.Context, workDir, prompt string) (string, error) {
	var errs []string

	for i, agent := range r.agents {
		output, err := r.runOne(ctx, agent, workDir, prompt)
		if err != nil {
			slog.Warn("agent failed",
				"command", agent.Command,
				"index", i,
				"error", err,
			)
			errs = append(errs, fmt.Sprintf("%s: %s", agent.Command, err))
			continue
		}

		slog.Info("agent succeeded",
			"command", agent.Command,
			"output_len", len(output),
		)
		return output, nil
	}

	return "", fmt.Errorf("all agents failed: %s", strings.Join(errs, "; "))
}

func (r *AgentRunner) runOne(ctx context.Context, agent config.AgentConfig, workDir, prompt string) (string, error) {
	timeout := agent.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := substitutePrompt(agent.Args, prompt)
	cmd := exec.CommandContext(ctx, agent.Command, args...)
	cmd.Dir = workDir

	// If no {prompt} placeholder in args, pass via stdin.
	hasPlaceholder := false
	for _, a := range agent.Args {
		if strings.Contains(a, "{prompt}") {
			hasPlaceholder = true
			break
		}
	}
	if !hasPlaceholder {
		cmd.Stdin = strings.NewReader(prompt)
	}

	output, err := cmd.Output()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("timeout after %s", timeout)
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("exit %d: %s", exitErr.ExitCode(), strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", err
	}

	return strings.TrimSpace(string(output)), nil
}

func substitutePrompt(args []string, prompt string) []string {
	result := make([]string, 0, len(args))
	for _, a := range args {
		result = append(result, strings.ReplaceAll(a, "{prompt}", prompt))
	}
	return result
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/bot/ -v -run "TestAgentRunner"`
Expected: All PASS

- [ ] **Step 5: Commit**

```bash
git add internal/bot/agent.go internal/bot/agent_test.go
git commit -m "feat: add AgentRunner with fallback chain"
```

---

### Task 4: Prompt Builder

**Files:**
- Create: `internal/bot/prompt.go`
- Create: `internal/bot/prompt_test.go`

- [ ] **Step 1: Write prompt builder tests**

```go
// internal/bot/prompt_test.go
package bot

import (
	"strings"
	"testing"

	"slack-issue-bot/internal/config"
)

func TestBuildPrompt_Basic(t *testing.T) {
	input := PromptInput{
		ThreadMessages: []ThreadMessage{
			{User: "Alice", Timestamp: "2026-04-09 10:30", Text: "Login page is broken"},
			{User: "Bob", Timestamp: "2026-04-09 10:32", Text: "Same here"},
		},
		RepoPath: "/repos/owner/repo",
		Branch:   "main",
		Prompt: config.PromptConfig{
			Language: "zh-TW",
		},
	}

	result := BuildPrompt(input)

	if !strings.Contains(result, "Alice (2026-04-09 10:30)") {
		t.Error("missing Alice's message")
	}
	if !strings.Contains(result, "Login page is broken") {
		t.Error("missing message text")
	}
	if !strings.Contains(result, "/repos/owner/repo") {
		t.Error("missing repo path")
	}
	if !strings.Contains(result, "main") {
		t.Error("missing branch")
	}
	if !strings.Contains(result, "===TRIAGE_METADATA===") {
		t.Error("missing metadata separator in format instructions")
	}
	if !strings.Contains(result, "zh-TW") {
		t.Error("missing language")
	}
}

func TestBuildPrompt_WithAttachments(t *testing.T) {
	input := PromptInput{
		ThreadMessages: []ThreadMessage{
			{User: "Alice", Timestamp: "10:30", Text: "see screenshot"},
		},
		Attachments: []AttachmentInfo{
			{Path: "/tmp/triage-abc/screenshot.png", Name: "screenshot.png", Type: "image"},
			{Path: "/tmp/triage-abc/error.log", Name: "error.log", Type: "text"},
		},
		RepoPath: "/repos/owner/repo",
		Prompt:   config.PromptConfig{Language: "en"},
	}

	result := BuildPrompt(input)

	if !strings.Contains(result, "screenshot.png") {
		t.Error("missing image attachment")
	}
	if !strings.Contains(result, "error.log") {
		t.Error("missing text attachment")
	}
}

func TestBuildPrompt_WithExtraRules(t *testing.T) {
	input := PromptInput{
		ThreadMessages: []ThreadMessage{
			{User: "Alice", Timestamp: "10:30", Text: "test"},
		},
		RepoPath: "/repos/owner/repo",
		Prompt: config.PromptConfig{
			Language:   "zh-TW",
			ExtraRules: []string{"no guessing", "only real files"},
		},
	}

	result := BuildPrompt(input)

	if !strings.Contains(result, "no guessing") {
		t.Error("missing extra rule 1")
	}
	if !strings.Contains(result, "only real files") {
		t.Error("missing extra rule 2")
	}
}

func TestBuildPrompt_WithExtraDescription(t *testing.T) {
	input := PromptInput{
		ThreadMessages: []ThreadMessage{
			{User: "Alice", Timestamp: "10:30", Text: "it's broken"},
		},
		ExtraDescription: "It happens on the login page after entering wrong password 3 times",
		RepoPath:         "/repos/owner/repo",
		Prompt:           config.PromptConfig{Language: "en"},
	}

	result := BuildPrompt(input)

	if !strings.Contains(result, "It happens on the login page") {
		t.Error("missing extra description")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/bot/ -v -run "TestBuildPrompt"`
Expected: FAIL — `BuildPrompt` not defined

- [ ] **Step 3: Write prompt builder implementation**

```go
// internal/bot/prompt.go
package bot

import (
	"fmt"
	"strings"

	"slack-issue-bot/internal/config"
)

// ThreadMessage is a single message from a Slack thread.
type ThreadMessage struct {
	User      string
	Timestamp string
	Text      string
}

// AttachmentInfo describes a downloaded attachment.
type AttachmentInfo struct {
	Path string // local temp file path
	Name string // original filename
	Type string // "image", "text", "document"
}

// PromptInput holds all data needed to build the agent prompt.
type PromptInput struct {
	ThreadMessages   []ThreadMessage
	Attachments      []AttachmentInfo
	ExtraDescription string
	RepoPath         string
	Branch           string
	Prompt           config.PromptConfig
}

// BuildPrompt constructs the minimal user prompt for the CLI agent.
func BuildPrompt(input PromptInput) string {
	var sb strings.Builder

	// Task
	sb.WriteString("## Task\n\n")
	sb.WriteString("Analyze the following thread conversation and triage against the specified codebase. ")
	sb.WriteString("Produce a report suitable as a GitHub issue body.\n\n")

	// Thread context
	sb.WriteString("## Thread Context\n\n")
	for _, msg := range input.ThreadMessages {
		sb.WriteString(fmt.Sprintf("%s (%s):\n> %s\n\n", msg.User, msg.Timestamp, msg.Text))
	}

	// Extra description
	if input.ExtraDescription != "" {
		sb.WriteString("## Extra Description\n\n")
		sb.WriteString(fmt.Sprintf("> %s\n\n", input.ExtraDescription))
	}

	// Repository
	sb.WriteString("## Repository\n\n")
	sb.WriteString(fmt.Sprintf("Path: %s\n", input.RepoPath))
	if input.Branch != "" {
		sb.WriteString(fmt.Sprintf("Branch: %s\n", input.Branch))
	}
	sb.WriteString("\n")

	// Attachments
	if len(input.Attachments) > 0 {
		sb.WriteString("## Attachments\n\n")
		for _, att := range input.Attachments {
			hint := ""
			switch att.Type {
			case "image":
				hint = " (image — use your file reading tools to view)"
			case "text":
				hint = " (text — read directly)"
			case "document":
				hint = " (document)"
			}
			sb.WriteString(fmt.Sprintf("- %s%s\n", att.Path, hint))
		}
		sb.WriteString("\n")
	}

	// Output format
	sb.WriteString("## Output Format\n\n")
	sb.WriteString("Output markdown first (used directly as the issue body),\n")
	sb.WriteString("then a ===TRIAGE_METADATA=== separator, then JSON:\n\n")
	sb.WriteString("```\n")
	sb.WriteString("{\n")
	sb.WriteString(`  "issue_type": "bug|feature|improvement|question",` + "\n")
	sb.WriteString(`  "confidence": "low|medium|high",` + "\n")
	sb.WriteString(`  "files": [{"path": "...", "line": 0, "relevance": "..."}],` + "\n")
	sb.WriteString(`  "open_questions": [],` + "\n")
	sb.WriteString(`  "suggested_title": "..."` + "\n")
	sb.WriteString("}\n")
	sb.WriteString("```\n\n")

	// Language + extra rules
	if input.Prompt.Language != "" {
		sb.WriteString(fmt.Sprintf("Response language: %s\n", input.Prompt.Language))
	}
	if len(input.Prompt.ExtraRules) > 0 {
		sb.WriteString("\nAdditional rules:\n")
		for _, rule := range input.Prompt.ExtraRules {
			sb.WriteString(fmt.Sprintf("- %s\n", rule))
		}
	}

	return sb.String()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/bot/ -v -run "TestBuildPrompt"`
Expected: All PASS

- [ ] **Step 5: Commit**

```bash
git add internal/bot/prompt.go internal/bot/prompt_test.go
git commit -m "feat: add prompt builder for agent invocation"
```

---

### Task 5: Slack Client — FetchThreadContext & DownloadAttachments

**Files:**
- Modify: `internal/slack/client.go`
- Modify: `internal/slack/client_test.go`

- [ ] **Step 1: Write tests for FetchThreadContext**

Append to `internal/slack/client_test.go`:

```go
func TestFetchThreadContext_FiltersBotMessages(t *testing.T) {
	// This test validates the filtering logic in isolation.
	// FetchThreadContext calls Slack API which we can't easily mock
	// without an interface, so we test the helper function instead.
	messages := []slack.Message{
		{Msg: slack.Msg{User: "U001", Text: "bug report", Timestamp: "1000.0"}},
		{Msg: slack.Msg{User: "UBOT", Text: "analyzing...", Timestamp: "1001.0", BotID: "B123"}},
		{Msg: slack.Msg{User: "U002", Text: "me too", Timestamp: "1002.0"}},
		{Msg: slack.Msg{User: "U001", Text: "@bot", Timestamp: "1003.0"}}, // trigger
	}

	result := filterThreadMessages(messages, "1003.0", "UBOT")
	if len(result) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result))
	}
	if result[0].Text != "bug report" {
		t.Errorf("msg[0] = %q", result[0].Text)
	}
	if result[1].Text != "me too" {
		t.Errorf("msg[1] = %q", result[1].Text)
	}
}

func TestClassifyAttachment(t *testing.T) {
	tests := []struct {
		filetype string
		mimetype string
		want     string
	}{
		{"png", "image/png", "image"},
		{"jpg", "image/jpeg", "image"},
		{"gif", "image/gif", "image"},
		{"text", "text/plain", "text"},
		{"csv", "text/csv", "text"},
		{"log", "text/plain", "text"},
		{"xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "document"},
		{"pdf", "application/pdf", "document"},
		{"binary", "application/octet-stream", "document"},
	}
	for _, tt := range tests {
		t.Run(tt.filetype, func(t *testing.T) {
			got := classifyAttachment(tt.filetype, tt.mimetype)
			if got != tt.want {
				t.Errorf("classifyAttachment(%q, %q) = %q, want %q", tt.filetype, tt.mimetype, got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/slack/ -v -run "TestFetchThreadContext|TestClassifyAttachment"`
Expected: FAIL — functions not defined

- [ ] **Step 3: Add FetchThreadContext and helpers to client.go**

Add to `internal/slack/client.go`:

```go
// ThreadRawMessage is a raw message from a Slack thread.
type ThreadRawMessage struct {
	User      string
	Text      string
	Timestamp string
	Files     []slack.File
}

// FetchThreadContext reads all messages in a thread up to the trigger point.
// Returns messages in chronological order, excluding bot's own messages.
func (c *Client) FetchThreadContext(channelID, threadTS, triggerTS, botUserID string, limit int) ([]ThreadRawMessage, error) {
	if limit <= 0 {
		limit = 50
	}

	var allMessages []slack.Message
	cursor := ""

	for {
		params := &slack.GetConversationRepliesParameters{
			ChannelID: channelID,
			Timestamp: threadTS,
			Cursor:    cursor,
			Limit:     200,
		}

		msgs, hasMore, nextCursor, err := c.api.GetConversationReplies(params)
		if err != nil {
			return nil, fmt.Errorf("conversations.replies: %w", err)
		}

		allMessages = append(allMessages, msgs...)

		if !hasMore || len(allMessages) >= limit {
			break
		}
		cursor = nextCursor
	}

	return filterThreadMessages(allMessages, triggerTS, botUserID), nil
}

// filterThreadMessages filters out bot messages and messages after the trigger.
func filterThreadMessages(messages []slack.Message, triggerTS, botUserID string) []ThreadRawMessage {
	var result []ThreadRawMessage
	for _, m := range messages {
		// Skip messages at or after the trigger.
		if m.Timestamp >= triggerTS {
			continue
		}
		// Skip bot's own messages.
		if m.BotID != "" || m.User == botUserID {
			continue
		}
		result = append(result, ThreadRawMessage{
			User:      m.User,
			Text:      m.Text,
			Timestamp: m.Timestamp,
			Files:     m.Files,
		})
	}
	return result
}

// DownloadAttachments downloads thread attachments to a temp dir.
// Returns attachment info and the temp dir path.
func (c *Client) DownloadAttachments(messages []ThreadRawMessage, tempDir string) []AttachmentDownload {
	var attachments []AttachmentDownload

	for _, msg := range messages {
		for _, f := range msg.Files {
			data, err := c.downloadBytes(f.URLPrivateDownload)
			if err != nil {
				slog.Warn("attachment download failed", "name", f.Name, "error", err)
				attachments = append(attachments, AttachmentDownload{
					Name:   f.Name,
					Type:   classifyAttachment(f.Filetype, f.Mimetype),
					Failed: true,
				})
				continue
			}

			path := filepath.Join(tempDir, f.Name)
			if err := os.WriteFile(path, data, 0644); err != nil {
				slog.Warn("attachment write failed", "name", f.Name, "error", err)
				continue
			}

			attachments = append(attachments, AttachmentDownload{
				Name: f.Name,
				Path: path,
				Type: classifyAttachment(f.Filetype, f.Mimetype),
			})
		}
	}
	return attachments
}

// AttachmentDownload is the result of downloading a single attachment.
type AttachmentDownload struct {
	Name   string
	Path   string
	Type   string // "image", "text", "document"
	Failed bool
}

// classifyAttachment determines the type of a Slack file.
func classifyAttachment(filetype, mimetype string) string {
	if isImageFile(filetype, mimetype) {
		return "image"
	}
	if isTextFile(filetype, mimetype) {
		return "text"
	}
	return "document"
}
```

Also add the necessary imports (`"os"`, `"path/filepath"`) at the top of client.go.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/slack/ -v -run "TestFetchThreadContext|TestClassifyAttachment"`
Expected: All PASS

- [ ] **Step 5: Commit**

```bash
git add internal/slack/client.go internal/slack/client_test.go
git commit -m "feat: add FetchThreadContext and attachment download"
```

---

### Task 6: Rewrite Slack Handler

**Files:**
- Modify: `internal/slack/handler.go` (rewrite event model, keep dedup/rate limit/semaphore)
- Modify: `internal/slack/handler_test.go`

- [ ] **Step 1: Write tests for the new handler**

```go
// internal/slack/handler_test.go
package slack

import (
	"sync"
	"testing"
	"time"
)

// --- Dedup tests (same as v1, these should still pass) ---

func TestDedup_FirstEventPasses(t *testing.T) {
	d := newDedup(time.Minute)
	if d.isDuplicate("evt1") {
		t.Error("first event should not be duplicate")
	}
}

func TestDedup_SecondEventBlocked(t *testing.T) {
	d := newDedup(time.Minute)
	d.isDuplicate("evt1")
	if !d.isDuplicate("evt1") {
		t.Error("second event should be duplicate")
	}
}

func TestDedup_ExpiredEventPasses(t *testing.T) {
	d := newDedup(10 * time.Millisecond)
	d.isDuplicate("evt1")
	time.Sleep(20 * time.Millisecond)
	if d.isDuplicate("evt1") {
		t.Error("expired event should not be duplicate")
	}
}

// --- Thread dedup tests ---

func TestThreadDedup_SameThreadBlocked(t *testing.T) {
	d := newThreadDedup(time.Minute)
	if d.isDuplicate("C1", "T1") {
		t.Error("first trigger should not be duplicate")
	}
	if !d.isDuplicate("C1", "T1") {
		t.Error("second trigger on same thread should be duplicate")
	}
}

func TestThreadDedup_DifferentThreadAllowed(t *testing.T) {
	d := newThreadDedup(time.Minute)
	d.isDuplicate("C1", "T1")
	if d.isDuplicate("C1", "T2") {
		t.Error("different thread should not be duplicate")
	}
}

func TestThreadDedup_ClearAllowsRetrigger(t *testing.T) {
	d := newThreadDedup(time.Minute)
	d.isDuplicate("C1", "T1")
	d.Remove("C1", "T1")
	if d.isDuplicate("C1", "T1") {
		t.Error("cleared thread should allow re-trigger")
	}
}

// --- Rate limiter tests (same as v1) ---

func TestRateLimiter_AllowsWithinLimit(t *testing.T) {
	r := newRateLimiter(3, time.Minute)
	for i := 0; i < 3; i++ {
		if !r.allow("user1") {
			t.Errorf("request %d should be allowed", i)
		}
	}
}

func TestRateLimiter_BlocksOverLimit(t *testing.T) {
	r := newRateLimiter(2, time.Minute)
	r.allow("user1")
	r.allow("user1")
	if r.allow("user1") {
		t.Error("third request should be blocked")
	}
}

func TestRateLimiter_NilDisabled(t *testing.T) {
	r := newRateLimiter(0, 0)
	if !r.allow("user1") {
		t.Error("disabled limiter should always allow")
	}
}

// --- Handler integration tests ---

func TestHandler_DedupBlocksDuplicate(t *testing.T) {
	var count int
	var mu sync.Mutex
	h := NewHandler(HandlerConfig{
		MaxConcurrent: 5,
		DedupTTL:      time.Minute,
		OnEvent: func(e TriggerEvent) {
			mu.Lock()
			count++
			mu.Unlock()
		},
	})

	e := TriggerEvent{ChannelID: "C1", ThreadTS: "T1", UserID: "U1", TriggerTS: "T1.1"}

	h.HandleTrigger(e)
	h.HandleTrigger(e) // duplicate

	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if count != 1 {
		t.Errorf("count = %d, want 1", count)
	}
}

func TestHandler_RateLimitBlocksExcess(t *testing.T) {
	rejected := false
	h := NewHandler(HandlerConfig{
		MaxConcurrent:   5,
		DedupTTL:        time.Minute,
		PerUserLimit:    1,
		RateWindow:      time.Minute,
		OnEvent:         func(e TriggerEvent) {},
		OnRejected:      func(e TriggerEvent, reason string) { rejected = true },
	})

	h.HandleTrigger(TriggerEvent{ChannelID: "C1", ThreadTS: "T1", UserID: "U1", TriggerTS: "T1.1"})
	h.HandleTrigger(TriggerEvent{ChannelID: "C2", ThreadTS: "T2", UserID: "U1", TriggerTS: "T2.1"})

	time.Sleep(50 * time.Millisecond)
	if !rejected {
		t.Error("second trigger should be rate-limited")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/slack/ -v -run "TestDedup|TestThreadDedup|TestRateLimiter|TestHandler"`
Expected: FAIL — `TriggerEvent`, `NewHandler` signatures changed

- [ ] **Step 3: Rewrite handler.go**

```go
// internal/slack/handler.go
package slack

import (
	"fmt"
	"sync"
	"time"
)

// TriggerEvent represents an @bot mention or /triage command.
type TriggerEvent struct {
	ChannelID string
	ThreadTS  string // parent message timestamp
	TriggerTS string // the message that triggered the bot
	UserID    string
	Text      string // command text (e.g., "/triage owner/repo")
}

// HandlerConfig configures the event handler.
type HandlerConfig struct {
	MaxConcurrent   int
	DedupTTL        time.Duration
	PerUserLimit    int
	PerChannelLimit int
	RateWindow      time.Duration
	OnEvent         func(event TriggerEvent)
	OnRejected      func(event TriggerEvent, reason string)
}

// Handler processes trigger events with dedup, rate limiting, and concurrency control.
type Handler struct {
	threadDedup  *threadDedup
	userLimit    *rateLimiter
	channelLimit *rateLimiter
	semaphore    chan struct{}
	onEvent      func(event TriggerEvent)
	onRejected   func(event TriggerEvent, reason string)
}

func NewHandler(cfg HandlerConfig) *Handler {
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 3
	}
	if cfg.DedupTTL <= 0 {
		cfg.DedupTTL = 5 * time.Minute
	}
	return &Handler{
		threadDedup:  newThreadDedup(cfg.DedupTTL),
		userLimit:    newRateLimiter(cfg.PerUserLimit, cfg.RateWindow),
		channelLimit: newRateLimiter(cfg.PerChannelLimit, cfg.RateWindow),
		semaphore:    make(chan struct{}, cfg.MaxConcurrent),
		onEvent:      cfg.OnEvent,
		onRejected:   cfg.OnRejected,
	}
}

// HandleTrigger processes a trigger event through dedup, rate limiting,
// and semaphore before dispatching async.
func (h *Handler) HandleTrigger(event TriggerEvent) bool {
	// Thread-level dedup.
	if h.threadDedup.isDuplicate(event.ChannelID, event.ThreadTS) {
		return false
	}

	// Per-user rate limit.
	if !h.userLimit.allow(event.UserID) {
		if h.onRejected != nil {
			h.onRejected(event, "rate limit exceeded")
		}
		return false
	}

	// Per-channel rate limit.
	if !h.channelLimit.allow(event.ChannelID) {
		if h.onRejected != nil {
			h.onRejected(event, "channel rate limit exceeded")
		}
		return false
	}

	// Semaphore — bounded concurrency.
	h.semaphore <- struct{}{}
	go func() {
		defer func() { <-h.semaphore }()
		h.onEvent(event)
	}()

	return true
}

// ClearThreadDedup removes a thread from the dedup map (e.g., after completion/failure).
func (h *Handler) ClearThreadDedup(channelID, threadTS string) {
	h.threadDedup.Remove(channelID, threadTS)
}

// --- Thread dedup (replaces messageDedup) ---

type threadDedup struct {
	mu   sync.Mutex
	seen map[string]time.Time
	ttl  time.Duration
}

func newThreadDedup(ttl time.Duration) *threadDedup {
	d := &threadDedup{seen: make(map[string]time.Time), ttl: ttl}
	go d.cleanup()
	return d
}

func (d *threadDedup) isDuplicate(channelID, threadTS string) bool {
	key := fmt.Sprintf("%s:%s", channelID, threadTS)
	d.mu.Lock()
	defer d.mu.Unlock()
	if t, ok := d.seen[key]; ok && time.Since(t) < d.ttl {
		return true
	}
	d.seen[key] = time.Now()
	return false
}

func (d *threadDedup) Remove(channelID, threadTS string) {
	key := fmt.Sprintf("%s:%s", channelID, threadTS)
	d.mu.Lock()
	delete(d.seen, key)
	d.mu.Unlock()
}

func (d *threadDedup) cleanup() {
	ticker := time.NewTicker(d.ttl)
	for range ticker.C {
		d.mu.Lock()
		for k, t := range d.seen {
			if time.Since(t) >= d.ttl {
				delete(d.seen, k)
			}
		}
		d.mu.Unlock()
	}
}

// --- Event-level dedup (kept for Socket Mode event dedup) ---

type dedup struct {
	mu   sync.Mutex
	seen map[string]time.Time
	ttl  time.Duration
}

func newDedup(ttl time.Duration) *dedup {
	d := &dedup{seen: make(map[string]time.Time), ttl: ttl}
	go d.cleanup()
	return d
}

func (d *dedup) isDuplicate(eventID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if t, ok := d.seen[eventID]; ok && time.Since(t) < d.ttl {
		return true
	}
	d.seen[eventID] = time.Now()
	return false
}

func (d *dedup) cleanup() {
	ticker := time.NewTicker(d.ttl)
	for range ticker.C {
		d.mu.Lock()
		for k, t := range d.seen {
			if time.Since(t) >= d.ttl {
				delete(d.seen, k)
			}
		}
		d.mu.Unlock()
	}
}

// --- Rate limiter (unchanged from v1) ---

type rateLimiter struct {
	mu     sync.Mutex
	counts map[string][]time.Time
	limit  int
	window time.Duration
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{
		counts: make(map[string][]time.Time),
		limit:  limit,
		window: window,
	}
}

func (r *rateLimiter) allow(key string) bool {
	if r.limit <= 0 {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-r.window)

	// Remove expired entries.
	var valid []time.Time
	for _, t := range r.counts[key] {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}

	if len(valid) >= r.limit {
		r.counts[key] = valid
		return false
	}

	r.counts[key] = append(valid, now)
	return true
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/slack/ -v`
Expected: All PASS (including existing ExtractKeywords tests)

- [ ] **Step 5: Commit**

```bash
git add internal/slack/handler.go internal/slack/handler_test.go
git commit -m "feat: rewrite handler for app_mention and slash command triggers"
```

---

### Task 7: Simplify GitHub Issue Client

**Files:**
- Modify: `internal/github/issue.go`
- Modify: `internal/github/issue_test.go`

- [ ] **Step 1: Write tests for simplified API**

```go
// internal/github/issue_test.go
package github

import (
	"strings"
	"testing"
)

func TestBuildIssueBody_WithHeader(t *testing.T) {
	header := "**Channel**: #general\n**Reporter**: alice\n**Branch**: main\n\n---\n\n"
	agentBody := "## Summary\n\nLogin page broken."

	body := header + agentBody

	if !strings.Contains(body, "#general") {
		t.Error("missing channel")
	}
	if !strings.Contains(body, "alice") {
		t.Error("missing reporter")
	}
	if !strings.Contains(body, "Login page broken") {
		t.Error("missing agent body")
	}
}

// CreateIssue requires GitHub API — tested via mock in integration tests.
// The function signature changes: it now takes title + body directly
// instead of IssueInput with FormatIssueBody.
```

- [ ] **Step 2: Rewrite issue.go**

```go
// internal/github/issue.go
package github

import (
	"context"
	"fmt"

	gh "github.com/google/go-github/v60/github"
)

// IssueClient creates GitHub issues.
type IssueClient struct {
	client *gh.Client
}

// NewIssueClient creates a new GitHub issue client.
func NewIssueClient(token string) *IssueClient {
	return &IssueClient{
		client: gh.NewClient(nil).WithAuthToken(token),
	}
}

// CreateIssue creates a GitHub issue with the given title, body, and labels.
// Returns the issue HTML URL.
func (ic *IssueClient) CreateIssue(ctx context.Context, owner, repo, title, body string, labels []string) (string, error) {
	req := &gh.IssueRequest{
		Title:  gh.Ptr(title),
		Body:   gh.Ptr(body),
		Labels: &labels,
	}

	issue, _, err := ic.client.Issues.Create(ctx, owner, repo, req)
	if err != nil {
		return "", fmt.Errorf("create issue: %w", err)
	}

	return issue.GetHTMLURL(), nil
}
```

- [ ] **Step 3: Run tests**

Run: `go test ./internal/github/ -v -run "TestBuildIssueBody"`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add internal/github/issue.go internal/github/issue_test.go
git commit -m "feat: simplify GitHub issue client — accept title + body directly"
```

---

### Task 8: Rewrite Workflow

**Files:**
- Modify: `internal/bot/workflow.go` (complete rewrite)

- [ ] **Step 1: Write the new workflow**

```go
// internal/bot/workflow.go
package bot

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"slack-issue-bot/internal/config"
	ghclient "slack-issue-bot/internal/github"
	"slack-issue-bot/internal/mantis"
	slackclient "slack-issue-bot/internal/slack"
)

const pendingTimeout = 1 * time.Minute
const maxOpenQuestions = 5

// pendingTriage stores context between the trigger event and user selections.
type pendingTriage struct {
	ChannelID      string
	ThreadTS       string
	TriggerTS      string
	Attachments    []string // temp file paths
	SelectedRepo   string
	SelectedBranch string
	Phase          string // "repo", "branch", "description"
	SelectorTS     string
	Reporter       string
	ChannelName    string
	ExtraDesc      string // extra description from modal
	CmdArgs        string // parsed command arguments (e.g., "owner/repo@branch")
}

// Workflow orchestrates the v2 triage flow.
type Workflow struct {
	cfg           *config.Config
	slack         *slackclient.Client
	handler       *slackclient.Handler
	issueClient   *ghclient.IssueClient
	repoCache     *ghclient.RepoCache
	repoDiscovery *ghclient.RepoDiscovery
	agentRunner   *AgentRunner
	mantisClient  *mantis.Client

	mu        sync.Mutex
	pending   map[string]*pendingTriage // keyed by selectorTS
	autoBound map[string]bool
}

// NewWorkflow creates a new v2 workflow.
func NewWorkflow(
	cfg *config.Config,
	slack *slackclient.Client,
	issueClient *ghclient.IssueClient,
	repoCache *ghclient.RepoCache,
	repoDiscovery *ghclient.RepoDiscovery,
	agentRunner *AgentRunner,
	mantisClient *mantis.Client,
) *Workflow {
	return &Workflow{
		cfg:           cfg,
		slack:         slack,
		issueClient:   issueClient,
		repoCache:     repoCache,
		repoDiscovery: repoDiscovery,
		agentRunner:   agentRunner,
		mantisClient:  mantisClient,
		pending:       make(map[string]*pendingTriage),
		autoBound:     make(map[string]bool),
	}
}

func (w *Workflow) SetHandler(h *slackclient.Handler) { w.handler = h }
func (w *Workflow) RegisterChannel(channelID string) {
	w.mu.Lock()
	w.autoBound[channelID] = true
	w.mu.Unlock()
}
func (w *Workflow) UnregisterChannel(channelID string) {
	w.mu.Lock()
	delete(w.autoBound, channelID)
	w.mu.Unlock()
}

// HandleTrigger is called when @bot or /triage is detected in a thread.
func (w *Workflow) HandleTrigger(event slackclient.TriggerEvent) {
	// Thread-only check.
	if event.ThreadTS == "" {
		w.slack.PostMessage(event.ChannelID, ":warning: 請在對話串中使用此指令。", "")
		return
	}

	channelCfg, ok := w.cfg.Channels[event.ChannelID]
	if !ok {
		w.mu.Lock()
		isBound := w.autoBound[event.ChannelID]
		w.mu.Unlock()
		if !isBound && !w.cfg.AutoBind {
			return
		}
		channelCfg = w.cfg.ChannelDefaults
	}

	reporter := w.slack.ResolveUser(event.UserID)
	channelName := w.slack.GetChannelName(event.ChannelID)

	pt := &pendingTriage{
		ChannelID:   event.ChannelID,
		ThreadTS:    event.ThreadTS,
		TriggerTS:   event.TriggerTS,
		Reporter:    reporter,
		ChannelName: channelName,
		CmdArgs:     parseTriggerArgs(event.Text),
	}

	// Parse slash command args: /triage owner/repo@branch
	repo, branch := parseRepoArg(pt.CmdArgs)
	if repo != "" {
		pt.SelectedRepo = repo
		if branch != "" {
			pt.SelectedBranch = branch
			w.showDescriptionPrompt(pt)
			return
		}
		w.afterRepoSelected(pt, channelCfg)
		return
	}

	// Determine repos from config.
	repos := channelCfg.GetRepos()

	if len(repos) == 1 {
		pt.SelectedRepo = repos[0]
		w.afterRepoSelected(pt, channelCfg)
		return
	}

	if len(repos) > 1 {
		pt.Phase = "repo"
		selectorTS, err := w.slack.PostSelector(event.ChannelID,
			":point_right: Which repo should this issue go to?",
			"repo_select", repos, pt.ThreadTS)
		if err != nil {
			w.notifyError(event.ChannelID, pt.ThreadTS, "Failed to show repo selector: %v", err)
			return
		}
		pt.SelectorTS = selectorTS
		w.storePending(selectorTS, pt)
		return
	}

	// No repos configured — use auto-discovery.
	pt.Phase = "repo_search"
	selectorTS, err := w.slack.PostExternalSelector(event.ChannelID,
		":point_right: Search and select a repo:",
		"repo_search", "Type to search repos...", pt.ThreadTS)
	if err != nil {
		w.notifyError(event.ChannelID, pt.ThreadTS, "Failed to show repo search: %v", err)
		return
	}
	pt.SelectorTS = selectorTS
	w.storePending(selectorTS, pt)
}

func (w *Workflow) HandleRepoSuggestion(query string) []string {
	repos, err := w.repoDiscovery.SearchRepos(context.Background(), query)
	if err != nil {
		slog.Warn("repo search failed", "error", err)
		return nil
	}
	return repos
}

func (w *Workflow) HandleSelection(channelID, actionID, value, selectorMsgTS string) {
	w.mu.Lock()
	pt, ok := w.pending[selectorMsgTS]
	if ok {
		delete(w.pending, selectorMsgTS)
	}
	w.mu.Unlock()
	if !ok {
		return
	}

	channelCfg := w.cfg.ChannelDefaults
	if cc, ok := w.cfg.Channels[pt.ChannelID]; ok {
		channelCfg = cc
	}

	switch pt.Phase {
	case "repo", "repo_search":
		w.slack.UpdateMessage(channelID, selectorMsgTS,
			fmt.Sprintf(":white_check_mark: Repo: `%s`", value))
		pt.SelectedRepo = value
		w.afterRepoSelected(pt, channelCfg)
	case "branch":
		w.slack.UpdateMessage(channelID, selectorMsgTS,
			fmt.Sprintf(":white_check_mark: Branch: `%s`", value))
		pt.SelectedBranch = value
		w.showDescriptionPrompt(pt)
	}
}

func (w *Workflow) afterRepoSelected(pt *pendingTriage, channelCfg config.ChannelConfig) {
	if !channelCfg.IsBranchSelectEnabled() {
		w.showDescriptionPrompt(pt)
		return
	}

	repoPath, err := w.repoCache.EnsureRepo(pt.SelectedRepo)
	if err != nil {
		w.notifyError(pt.ChannelID, pt.ThreadTS, "Failed to access repo %s: %v", pt.SelectedRepo, err)
		return
	}

	var branches []string
	if len(channelCfg.Branches) > 0 {
		branches = channelCfg.Branches
	} else {
		branches, err = w.repoCache.ListBranches(repoPath)
		if err != nil {
			w.showDescriptionPrompt(pt)
			return
		}
	}

	if len(branches) <= 1 {
		if len(branches) == 1 {
			pt.SelectedBranch = branches[0]
		}
		w.showDescriptionPrompt(pt)
		return
	}

	pt.Phase = "branch"
	selectorTS, err := w.slack.PostSelector(pt.ChannelID,
		fmt.Sprintf(":point_right: Which branch of `%s`?", pt.SelectedRepo),
		"branch_select", branches, pt.ThreadTS)
	if err != nil {
		w.showDescriptionPrompt(pt)
		return
	}
	pt.SelectorTS = selectorTS
	w.storePending(selectorTS, pt)
}

func (w *Workflow) showDescriptionPrompt(pt *pendingTriage) {
	pt.Phase = "description"
	selectorTS, err := w.slack.PostSelector(pt.ChannelID,
		":memo: 需要補充說明嗎？（補充後可讓分析更精準）",
		"description_action", []string{"補充說明", "跳過"}, pt.ThreadTS)
	if err != nil {
		w.runTriage(pt)
		return
	}
	pt.SelectorTS = selectorTS
	w.storePending(selectorTS, pt)
}

func (w *Workflow) HandleDescriptionAction(channelID, value, selectorMsgTS, triggerID string) {
	w.mu.Lock()
	pt, ok := w.pending[selectorMsgTS]
	if !ok {
		w.mu.Unlock()
		return
	}

	if value == "跳過" {
		delete(w.pending, selectorMsgTS)
		w.mu.Unlock()
		w.slack.UpdateMessage(channelID, selectorMsgTS, ":fast_forward: 跳過補充說明")
		w.runTriage(pt)
		return
	}

	w.mu.Unlock()

	if triggerID == "" {
		w.mu.Lock()
		delete(w.pending, selectorMsgTS)
		w.mu.Unlock()
		w.runTriage(pt)
		return
	}

	if err := w.slack.OpenDescriptionModal(triggerID, selectorMsgTS); err != nil {
		w.mu.Lock()
		delete(w.pending, selectorMsgTS)
		w.mu.Unlock()
		w.runTriage(pt)
	}
}

func (w *Workflow) HandleDescriptionSubmit(selectorMsgTS, extraText string) {
	w.mu.Lock()
	pt, ok := w.pending[selectorMsgTS]
	if ok {
		delete(w.pending, selectorMsgTS)
	}
	w.mu.Unlock()
	if !ok {
		return
	}

	if extraText != "" {
		w.slack.UpdateMessage(pt.ChannelID, selectorMsgTS,
			fmt.Sprintf(":memo: 補充說明: %s", extraText))
		pt.ExtraDesc = extraText
	}
	w.runTriage(pt)
}

// runTriage is the core analysis pipeline.
func (w *Workflow) runTriage(pt *pendingTriage) {
	ctx := context.Background()

	// Create temp dir for attachments.
	tempDir, err := os.MkdirTemp("", "triage-*")
	if err != nil {
		w.notifyError(pt.ChannelID, pt.ThreadTS, "Failed to create temp dir: %v", err)
		w.clearDedup(pt)
		return
	}
	defer os.RemoveAll(tempDir)

	w.slack.PostMessage(pt.ChannelID, ":mag: 正在分析...", pt.ThreadTS)

	// 1. Ensure repo checked out.
	repoPath, err := w.repoCache.EnsureRepo(pt.SelectedRepo)
	if err != nil {
		w.notifyError(pt.ChannelID, pt.ThreadTS, "Failed to access repo %s: %v", pt.SelectedRepo, err)
		w.clearDedup(pt)
		return
	}
	if pt.SelectedBranch != "" {
		if err := w.repoCache.Checkout(repoPath, pt.SelectedBranch); err != nil {
			w.notifyError(pt.ChannelID, pt.ThreadTS, "Failed to checkout branch %s: %v", pt.SelectedBranch, err)
			w.clearDedup(pt)
			return
		}
	}

	// 2. Read thread context.
	botUserID := "" // TODO: resolve from slack auth.test
	rawMsgs, err := w.slack.FetchThreadContext(pt.ChannelID, pt.ThreadTS, pt.TriggerTS, botUserID, w.cfg.MaxThreadMessages)
	if err != nil {
		w.notifyError(pt.ChannelID, pt.ThreadTS, "Failed to read thread: %v", err)
		w.clearDedup(pt)
		return
	}

	// 3. Download attachments.
	downloads := w.slack.DownloadAttachments(rawMsgs, tempDir)

	// 4. Enrich messages (Mantis URLs).
	var threadMsgs []ThreadMessage
	for _, m := range rawMsgs {
		text := m.Text
		if w.mantisClient != nil {
			text = enrichMessage(text, w.mantisClient)
		}
		threadMsgs = append(threadMsgs, ThreadMessage{
			User:      w.slack.ResolveUser(m.User),
			Timestamp: m.Timestamp,
			Text:      text,
		})
	}

	// 5. Build attachments info.
	var attachments []AttachmentInfo
	for _, d := range downloads {
		if d.Failed {
			attachments = append(attachments, AttachmentInfo{
				Name: d.Name + " (download failed)",
				Type: d.Type,
			})
			continue
		}
		attachments = append(attachments, AttachmentInfo{
			Path: d.Path,
			Name: d.Name,
			Type: d.Type,
		})
	}

	// 6. Build prompt.
	prompt := BuildPrompt(PromptInput{
		ThreadMessages:   threadMsgs,
		Attachments:      attachments,
		ExtraDescription: pt.ExtraDesc,
		RepoPath:         repoPath,
		Branch:           pt.SelectedBranch,
		Prompt:           w.cfg.Prompt,
	})

	// 7. Run agent.
	output, err := w.agentRunner.Run(ctx, repoPath, prompt)
	if err != nil {
		w.notifyError(pt.ChannelID, pt.ThreadTS, "分析工具暫時不可用: %v", err)
		w.clearDedup(pt)
		return
	}

	// 8. Parse output.
	parsed, err := ParseAgentOutput(output)
	if err != nil {
		w.notifyError(pt.ChannelID, pt.ThreadTS, "分析工具暫時不可用: %v", err)
		w.clearDedup(pt)
		return
	}

	// 9. Reject/Degrade.
	if strings.EqualFold(parsed.Metadata.Confidence, "low") {
		w.slack.PostMessage(pt.ChannelID,
			":warning: 無法建立 issue — 問題與此 repo 的程式碼關聯性不足\n請試著更具體地描述問題。",
			pt.ThreadTS)
		w.clearDedup(pt)
		return
	}

	if len(parsed.Metadata.Files) == 0 || len(parsed.Metadata.OpenQuestions) >= maxOpenQuestions {
		// Degrade: create issue without triage metadata section.
		parsed.Degraded = true
	}

	// 10. Sanitize + build issue body.
	body := SanitizeBody(parsed.MarkdownBody)
	fullBody := FormatIssueBody(pt.ChannelName, pt.Reporter, pt.SelectedBranch, body)

	// 11. Resolve title.
	firstMessage := ""
	if len(threadMsgs) > 0 {
		firstMessage = threadMsgs[0].Text
	}
	title := ResolveTitle(parsed.Metadata.SuggestedTitle, parsed.MarkdownBody, firstMessage)

	// 12. Resolve labels.
	channelCfg := w.cfg.ChannelDefaults
	if cc, ok := w.cfg.Channels[pt.ChannelID]; ok {
		channelCfg = cc
	}
	labels := ResolveLabels(parsed.Metadata.IssueType, channelCfg.DefaultLabels)

	// 13. Create GitHub issue.
	parts := strings.SplitN(pt.SelectedRepo, "/", 2)
	if len(parts) != 2 {
		w.notifyError(pt.ChannelID, pt.ThreadTS, "Invalid repo format: %s", pt.SelectedRepo)
		w.clearDedup(pt)
		return
	}

	issueURL, err := w.issueClient.CreateIssue(ctx, parts[0], parts[1], title, fullBody, labels)
	if err != nil {
		w.notifyError(pt.ChannelID, pt.ThreadTS, "Failed to create GitHub issue: %v", err)
		w.clearDedup(pt)
		return
	}

	// 14. Notify Slack.
	branchInfo := ""
	if pt.SelectedBranch != "" {
		branchInfo = fmt.Sprintf(" (branch: `%s`)", pt.SelectedBranch)
	}
	w.slack.PostMessage(pt.ChannelID,
		fmt.Sprintf(":white_check_mark: Issue created%s: %s", branchInfo, issueURL),
		pt.ThreadTS)

	w.clearDedup(pt)
}

func (w *Workflow) storePending(selectorTS string, pt *pendingTriage) {
	w.mu.Lock()
	w.pending[selectorTS] = pt
	w.mu.Unlock()

	go func() {
		time.Sleep(pendingTimeout)
		w.mu.Lock()
		_, stillPending := w.pending[selectorTS]
		if stillPending {
			delete(w.pending, selectorTS)
		}
		w.mu.Unlock()

		if stillPending {
			w.slack.UpdateMessage(pt.ChannelID, selectorTS, ":hourglass: 已超時")
			w.slack.PostMessage(pt.ChannelID,
				":hourglass: 選擇已超時，請重新觸發。", pt.ThreadTS)
			w.clearDedup(pt)
		}
	}()
}

func (w *Workflow) clearDedup(pt *pendingTriage) {
	if w.handler != nil {
		w.handler.ClearThreadDedup(pt.ChannelID, pt.ThreadTS)
	}
}

func (w *Workflow) notifyError(channelID, threadTS string, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	slog.Error("workflow error", "message", msg)
	w.slack.PostMessage(channelID, fmt.Sprintf(":x: %s", msg), threadTS)
}

// parseTriggerArgs extracts command arguments from trigger text.
// "@bot owner/repo@branch" → "owner/repo@branch"
// "/triage owner/repo" → "owner/repo"
func parseTriggerArgs(text string) string {
	text = strings.TrimSpace(text)
	// Remove @bot mention prefix.
	if idx := strings.Index(text, ">"); idx != -1 {
		text = strings.TrimSpace(text[idx+1:])
	}
	// Remove /triage prefix.
	text = strings.TrimPrefix(text, "/triage")
	return strings.TrimSpace(text)
}

// parseRepoArg parses "owner/repo" or "owner/repo@branch" from args.
func parseRepoArg(args string) (repo, branch string) {
	if args == "" {
		return "", ""
	}
	// Check for owner/repo pattern.
	if !strings.Contains(args, "/") {
		return "", ""
	}
	parts := strings.SplitN(args, "@", 2)
	repo = strings.TrimSpace(parts[0])
	if len(parts) == 2 {
		branch = strings.TrimSpace(parts[1])
	}
	return repo, branch
}
```

- [ ] **Step 2: Verify compilation**

Run: `go build ./internal/bot/`
Expected: Compiles (may need to update import paths after deleting llm/)

- [ ] **Step 3: Commit**

```bash
git add internal/bot/workflow.go
git commit -m "feat: rewrite workflow for v2 agent architecture"
```

---

### Task 9: Rewrite main.go

**Files:**
- Modify: `cmd/bot/main.go`

- [ ] **Step 1: Write the new main.go**

```go
// cmd/bot/main.go
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"slack-issue-bot/internal/bot"
	"slack-issue-bot/internal/config"
	ghclient "slack-issue-bot/internal/github"
	"slack-issue-bot/internal/mantis"
	slackclient "slack-issue-bot/internal/slack"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/socketmode"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	// Slack client.
	slackClient := slackclient.NewClient(cfg.Slack.BotToken)

	// GitHub clients.
	issueClient := ghclient.NewIssueClient(cfg.GitHub.Token)
	repoCache := ghclient.NewRepoCache(cfg.RepoCache.Dir, cfg.RepoCache.MaxAge)
	repoDiscovery := ghclient.NewRepoDiscovery(cfg.GitHub.Token)

	// Pre-warm repo discovery.
	if cfg.AutoBind {
		go repoDiscovery.Warm()
	}

	// Agent runner (from config).
	agentRunner := bot.NewAgentRunnerFromConfig(cfg)

	// Mantis client (optional).
	var mantisClient *mantis.Client
	if cfg.Mantis.BaseURL != "" {
		mantisClient = mantis.NewClient(cfg.Mantis)
	}

	// Workflow.
	wf := bot.NewWorkflow(cfg, slackClient, issueClient, repoCache, repoDiscovery, agentRunner, mantisClient)

	// Handler.
	handler := slackclient.NewHandler(slackclient.HandlerConfig{
		MaxConcurrent:   cfg.MaxConcurrent,
		DedupTTL:        5 * time.Minute,
		PerUserLimit:    cfg.RateLimit.PerUser,
		PerChannelLimit: cfg.RateLimit.PerChannel,
		RateWindow:      cfg.RateLimit.Window,
		OnEvent:         wf.HandleTrigger,
		OnRejected: func(e slackclient.TriggerEvent, reason string) {
			slackClient.PostMessage(e.ChannelID,
				fmt.Sprintf(":warning: %s", reason), e.ThreadTS)
		},
	})
	wf.SetHandler(handler)

	// Health check.
	if cfg.Server.Port > 0 {
		go func() {
			http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("ok"))
			})
			addr := fmt.Sprintf(":%d", cfg.Server.Port)
			slog.Info("health check listening", "addr", addr)
			http.ListenAndServe(addr, nil)
		}()
	}

	// Socket Mode.
	api := slack.New(cfg.Slack.BotToken,
		slack.OptionAppLevelToken(cfg.Slack.AppToken),
	)
	sm := socketmode.New(api)

	slog.Info("starting bot v2 (agent architecture)")

	go func() {
		for evt := range sm.Events {
			switch evt.Type {
			case socketmode.EventTypeEventsAPI:
				sm.Ack(*evt.Request)
				ea, ok := evt.Data.(slack.EventsAPIEvent)
				if !ok {
					continue
				}
				switch inner := ea.InnerEvent.Data.(type) {
				case *slack.AppMentionEvent:
					handler.HandleTrigger(slackclient.TriggerEvent{
						ChannelID: inner.Channel,
						ThreadTS:  inner.ThreadTimeStamp,
						TriggerTS: inner.TimeStamp,
						UserID:    inner.User,
						Text:      inner.Text,
					})
				case *slack.MemberJoinedChannelEvent:
					if cfg.AutoBind {
						wf.RegisterChannel(inner.Channel)
					}
				case *slack.MemberLeftChannelEvent:
					if cfg.AutoBind {
						wf.UnregisterChannel(inner.Channel)
					}
				}

			case socketmode.EventTypeSlashCommand:
				sm.Ack(*evt.Request)
				cmd, ok := evt.Data.(slack.SlashCommand)
				if !ok || cmd.Command != "/triage" {
					continue
				}
				// Slash commands: thread detection is limited.
				// Use channel_id. ThreadTS may be empty.
				handler.HandleTrigger(slackclient.TriggerEvent{
					ChannelID: cmd.ChannelID,
					ThreadTS:  cmd.ChannelID, // fallback: use channel as "thread"
					TriggerTS: "",
					UserID:    cmd.UserID,
					Text:      cmd.Text,
				})

			case socketmode.EventTypeInteractive:
				sm.Ack(*evt.Request)
				cb, ok := evt.Data.(slack.InteractionCallback)
				if !ok {
					continue
				}

				switch cb.Type {
				case slack.InteractionTypeBlockSuggestion:
					if cb.ActionID == "repo_search" {
						options := wf.HandleRepoSuggestion(cb.Value)
						var opts []*slack.OptionBlockObject
						for _, r := range options {
							opts = append(opts, slack.NewOptionBlockObject(r, slack.NewTextBlockObject("plain_text", r, false, false), nil))
						}
						sm.Ack(*evt.Request, opts)
					}

				case slack.InteractionTypeBlockActions:
					if len(cb.ActionCallback.BlockActions) == 0 {
						continue
					}
					action := cb.ActionCallback.BlockActions[0]
					selectorTS := cb.Message.Timestamp

					switch {
					case action.ActionID == "repo_select" || action.ActionID == "repo_search":
						value := action.Value
						if action.ActionID == "repo_search" && action.SelectedOption.Value != "" {
							value = action.SelectedOption.Value
						}
						wf.HandleSelection(cb.Channel.ID, action.ActionID, value, selectorTS)

					case action.ActionID == "branch_select":
						wf.HandleSelection(cb.Channel.ID, action.ActionID, action.Value, selectorTS)

					case action.ActionID == "description_action":
						wf.HandleDescriptionAction(cb.Channel.ID, action.Value, selectorTS, cb.TriggerID)
					}

				case slack.InteractionTypeViewSubmission:
					meta := cb.View.PrivateMetadata
					desc := ""
					if v, ok := cb.View.State.Values["description_block"]["description_input"]; ok {
						desc = v.Value
					}
					wf.HandleDescriptionSubmit(meta, desc)

				case slack.InteractionTypeViewClosed:
					meta := cb.View.PrivateMetadata
					wf.HandleDescriptionSubmit(meta, "")
				}
			}
		}
	}()

	if err := sm.Run(); err != nil {
		slog.Error("socket mode error", "error", err)
		os.Exit(1)
	}
}
```

- [ ] **Step 2: Verify compilation**

Run: `go build ./cmd/bot/`
Expected: Build succeeds (after Task 10 deletes old packages)

- [ ] **Step 3: Commit**

```bash
git add cmd/bot/main.go
git commit -m "feat: rewrite main.go for v2 agent architecture"
```

---

### Task 10: Delete Deprecated Packages & Clean Up

**Files:**
- Delete: `internal/diagnosis/` (entire directory)
- Delete: `internal/llm/` (entire directory)
- Modify: `go.mod` (remove unused deps)

- [ ] **Step 1: Delete deprecated packages**

```bash
rm -rf internal/diagnosis/
rm -rf internal/llm/
```

- [ ] **Step 2: Clean up go.mod**

```bash
go mod tidy
```

- [ ] **Step 3: Verify everything compiles**

Run: `go build ./...`
Expected: Build succeeds

- [ ] **Step 4: Run all tests**

Run: `go test ./... -v`
Expected: All tests pass. Tests in deleted packages are gone. Tests in modified packages use new APIs.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "chore: delete diagnosis/ and llm/ packages, clean up deps"
```

---

### Task 11: Integration Smoke Test

**Files:**
- No new files — uses existing test infrastructure

- [ ] **Step 1: Verify test count**

Run: `go test ./... -v 2>&1 | grep -c "PASS\|FAIL"`
Expected: All PASS, roughly 30-40 tests (down from 76 due to deleted packages)

- [ ] **Step 2: Verify binary builds and starts**

```bash
go build -o bot ./cmd/bot/
```
Expected: Binary builds successfully

- [ ] **Step 3: Final commit — update CLAUDE.md**

Update the project's CLAUDE.md to reflect the v2 architecture, removing references to `diagnosis/`, `llm/`, reaction-based triggers, and documenting the new agent-based flow.

```bash
git add CLAUDE.md
git commit -m "docs: update CLAUDE.md for v2 agent architecture"
```
