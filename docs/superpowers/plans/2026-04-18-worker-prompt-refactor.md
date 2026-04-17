# Worker Prompt Refactor Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move prompt assembly from app to worker, switch format from markdown to XML, and split prompt-related config between app (Language/Goal/OutputRules/AllowWorkerRules) and worker (ExtraRules).

**Architecture:** App assembles `queue.PromptContext` struct from Slack thread + config and puts it in `Job.PromptContext`. Worker reads it, combines with its own `ExtraRules` config (gated by `AllowWorkerRules`), renders XML, passes to agent CLI. Old `Job.Prompt string` field is deleted, `BuildPrompt` moves from `internal/bot/` to `internal/worker/`, and `Workers` YAML section renames to `Worker` (absorbing `count` and adding `prompt.extra_rules`).

**Tech Stack:** Go, `encoding/xml` for escaping, `strings.Builder` for prompt rendering, `koanf` for config, existing `slog` logger, `testing` + table-driven tests.

**Spec:** `docs/superpowers/specs/2026-04-18-worker-prompt-refactor-design.md`

**Migration stance:** Drain-and-cut (no dual-schema support). This plan includes config migration warnings for legacy keys but does not remap values.

---

## File Structure

### New files
- `internal/worker/prompt.go` — `BuildPrompt` function that renders `queue.PromptContext` + worker-config `ExtraRules` + `AttachmentInfo` to XML. Also defines `AttachmentInfo` struct (moved from `internal/bot`).
- `internal/worker/prompt_test.go` — XML rendering tests (escaping, optional sections, worker-rules toggle, ordering, attachment hints).
- `internal/bot/prompt_context.go` — `AssemblePromptContext` helper that packages inputs into a `queue.PromptContext` for app-side use.
- `internal/bot/prompt_context_test.go` — Tests for defaults-application and pass-through.

### Deleted files (at end of plan)
- `internal/bot/prompt.go` — old `BuildPrompt` / `AppendAttachmentSection` (unused after Task 5).
- `internal/bot/prompt_test.go` — replaced by `internal/worker/prompt_test.go`.

### Modified files
- `internal/queue/job.go` — add `PromptContext`, `ThreadMessage` types; add `Job.PromptContext` field; delete `Job.Prompt` field (Task 7).
- `internal/config/config.go` — expand `PromptConfig` (add Goal/OutputRules/AllowWorkerRules, delete ExtraRules); rename `Workers`→`Worker` and nest `Prompt WorkerPromptConfig`; update `applyDefaults`; add migration warnings.
- `internal/config/config_test.go` — update `cfg.Workers.Count` references to `cfg.Worker.Count`; remove ExtraRules test; add new tests.
- `internal/worker/executor.go` — remove `bot.AppendAttachmentSection` call; call new `worker.BuildPrompt`; inject `ExtraRules` via `executionDeps`; add nil-check on `PromptContext`; update `writeAttachments` return type.
- `internal/worker/pool.go` — add `ExtraRules []string` field to `worker.Config`; plumb to `executionDeps`.
- `internal/bot/workflow.go` — replace `BuildPrompt(PromptInput{...})` call with `AssemblePromptContext(...)`; use `queue.ThreadMessage` instead of local `bot.ThreadMessage`; set `job.PromptContext`.
- `cmd/agentdock/worker.go` — pass `cfg.Worker.Prompt.ExtraRules` to `worker.Config.ExtraRules`.
- `cmd/agentdock/local_adapter.go` — same plumbing for local mode.
- `cmd/agentdock/app.go` — rename `cfg.Workers.Count` → `cfg.Worker.Count`.
- `cmd/agentdock/validate_test.go` — same rename.
- `internal/queue/integration_test.go`, `internal/queue/redis_integration_test.go` — update to submit `PromptContext` instead of `Prompt` string.

---

## Task 1: Add `PromptContext` and `ThreadMessage` to queue package (additive)

**Purpose:** Introduce wire-protocol types. This is purely additive: `Job.Prompt string` and `bot.ThreadMessage` stay for now, so all existing code continues to compile.

**Files:**
- Modify: `internal/queue/job.go`
- Test: `internal/queue/job_test.go` (create if missing)

- [ ] **Step 1: Write failing test for `PromptContext` JSON round-trip**

Add to `internal/queue/job_test.go` (create the file if it doesn't exist):

```go
package queue

import (
	"encoding/json"
	"testing"
)

func TestPromptContext_JSONRoundTrip(t *testing.T) {
	orig := PromptContext{
		ThreadMessages: []ThreadMessage{
			{User: "Alice", Timestamp: "2026-04-09 10:30", Text: "login broken"},
		},
		ExtraDescription: "after 3 retries",
		Channel:          "general",
		Reporter:         "Alice",
		Branch:           "main",
		Language:         "zh-TW",
		Goal:             "Use the /triage-issue skill ...",
		OutputRules:      []string{"一句話", "< 100 字"},
		AllowWorkerRules: true,
	}

	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got PromptContext
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.ThreadMessages[0].User != "Alice" {
		t.Errorf("User = %q, want Alice", got.ThreadMessages[0].User)
	}
	if got.Goal != orig.Goal {
		t.Errorf("Goal = %q, want %q", got.Goal, orig.Goal)
	}
	if len(got.OutputRules) != 2 {
		t.Errorf("OutputRules len = %d, want 2", len(got.OutputRules))
	}
	if !got.AllowWorkerRules {
		t.Error("AllowWorkerRules = false, want true")
	}
}

func TestJob_PromptContextField_JSONRoundTrip(t *testing.T) {
	job := Job{
		ID: "test-1",
		PromptContext: &PromptContext{
			Channel:  "general",
			Reporter: "Bob",
			Goal:     "triage",
		},
	}
	data, err := json.Marshal(&job)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Job
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.PromptContext == nil {
		t.Fatal("PromptContext is nil after round-trip")
	}
	if got.PromptContext.Goal != "triage" {
		t.Errorf("Goal = %q, want triage", got.PromptContext.Goal)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/queue/ -run 'TestPromptContext_JSONRoundTrip|TestJob_PromptContextField_JSONRoundTrip' -v
```

Expected: FAIL — undefined: `PromptContext`, undefined: `ThreadMessage`, undefined: `Job.PromptContext`.

- [ ] **Step 3: Add types to `internal/queue/job.go`**

Insert the following near the existing type definitions (e.g. right after `AttachmentMeta` at line 51). Do not touch `Job.Prompt` yet — it stays for Task 7.

```go
type ThreadMessage struct {
	User      string `json:"user"`
	Timestamp string `json:"timestamp"`
	Text      string `json:"text"`
}

type PromptContext struct {
	ThreadMessages   []ThreadMessage `json:"thread_messages"`
	ExtraDescription string          `json:"extra_description,omitempty"`
	Channel          string          `json:"channel"`
	Reporter         string          `json:"reporter"`
	Branch           string          `json:"branch,omitempty"`
	Language         string          `json:"language"`
	Goal             string          `json:"goal"`
	OutputRules      []string        `json:"output_rules"`
	AllowWorkerRules bool            `json:"allow_worker_rules"`
}
```

Add a new field to `Job` struct (near line 36–37, before `EncryptedSecrets`):

```go
	PromptContext    *PromptContext    `json:"prompt_context,omitempty"`
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./internal/queue/ -run 'TestPromptContext_JSONRoundTrip|TestJob_PromptContextField_JSONRoundTrip' -v
```

Expected: PASS.

- [ ] **Step 5: Run full queue test suite to verify no regressions**

```bash
go test ./internal/queue/ -count=1
```

Expected: all tests pass. `Job.Prompt` still exists, nothing else broke.

- [ ] **Step 6: Commit**

```bash
git add internal/queue/job.go internal/queue/job_test.go
git commit -m "feat(queue): add PromptContext and ThreadMessage types

Additive wire-protocol types for prompt refactor (#61). Job.Prompt
string field retained temporarily for backward compat during migration;
will be removed in the final cleanup task."
```

---

## Task 2: Expand `PromptConfig` with Goal/OutputRules/AllowWorkerRules (additive)

**Purpose:** Add new app-side config fields and defaults. `ExtraRules` stays for now (deleted in Task 7) so existing code and tests keep compiling.

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go`

- [ ] **Step 1: Write failing tests for new fields and defaults**

This file already has a helper `loadFromString(t, yamlStr)` at the top of `internal/config/config_test.go` (line ~12) that does `yaml.Unmarshal` + `applyDefaults`. Use it. Append to the file:

```go
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

	wantGoal := "Use the /triage-issue skill to investigate and produce a triage result."
	if cfg.Prompt.Goal != wantGoal {
		t.Errorf("default Goal = %q, want %q", cfg.Prompt.Goal, wantGoal)
	}
	if cfg.Prompt.OutputRules == nil || len(cfg.Prompt.OutputRules) != 0 {
		t.Errorf("default OutputRules = %v, want empty non-nil", cfg.Prompt.OutputRules)
	}
	if cfg.Prompt.AllowWorkerRules == nil || !*cfg.Prompt.AllowWorkerRules {
		t.Errorf("default AllowWorkerRules = %v, want &true", cfg.Prompt.AllowWorkerRules)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/config/ -run 'TestPromptConfig_NewFields_YAMLLoad|TestPromptConfig_Defaults' -v
```

Expected: FAIL — undefined fields `Goal`, `OutputRules`, `AllowWorkerRules` on `PromptConfig`.

**Design note:** `AllowWorkerRules` is declared as `*bool` (pointer), not plain `bool`. Reason: we need to tell apart "operator didn't set it" (nil → default to `true`) from "operator explicitly set false" (non-nil, `false`). A plain `bool` collapses these two cases. All read sites must deref (`*cfg.Prompt.AllowWorkerRules`); the tests and the `AssemblePromptContext` helper (Task 5) handle this.

Also, the tests in Step 1 were written assuming pointer semantics — verify they look like:

```go
	if cfg.Prompt.AllowWorkerRules == nil {
		t.Fatal(...)
	}
	if *cfg.Prompt.AllowWorkerRules { ... }
```

- [ ] **Step 3: Expand `PromptConfig` in `internal/config/config.go`**

Replace the existing `PromptConfig` struct (around line 92) with:

```go
type PromptConfig struct {
	Language         string   `yaml:"language"`
	ExtraRules       []string `yaml:"extra_rules"` // deprecated — removed in Task 7
	Goal             string   `yaml:"goal"`
	OutputRules      []string `yaml:"output_rules"`
	AllowWorkerRules *bool    `yaml:"allow_worker_rules"` // tri-state: nil = default true
}
```

- [ ] **Step 4: Add defaults to `applyDefaults` in `internal/config/config.go`**

Add these lines at the end of `applyDefaults` (just before `resolveSecrets(cfg)` at line 228):

```go
	if cfg.Prompt.Goal == "" {
		cfg.Prompt.Goal = "Use the /triage-issue skill to investigate and produce a triage result."
	}
	// OutputRules default is empty slice — no <output_rules> section rendered unless operator sets values.
	if cfg.Prompt.OutputRules == nil {
		cfg.Prompt.OutputRules = []string{}
	}
	if cfg.Prompt.AllowWorkerRules == nil {
		t := true
		cfg.Prompt.AllowWorkerRules = &t
	}
```

- [ ] **Step 5: Run tests to verify they pass**

```bash
go test ./internal/config/ -run 'TestPromptConfig_NewFields_YAMLLoad|TestPromptConfig_Defaults' -v
```

Expected: PASS.

- [ ] **Step 6: Run full config test suite to verify no regressions**

```bash
go test ./internal/config/ -count=1
```

Expected: all tests pass. The existing `TestLoadConfig` test at line ~111 checks `cfg.Prompt.ExtraRules` — this still works because `ExtraRules` is still in the struct.

- [ ] **Step 7: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): add Goal, OutputRules, AllowWorkerRules to PromptConfig

Additive fields with defaults (Goal hardcoded string, OutputRules empty
slice, AllowWorkerRules pointer defaulting to true). ExtraRules stays
deprecated; removed in cleanup task."
```

---

## Task 3: Rename `Workers` → `Worker`, add `Prompt WorkerPromptConfig`

**Purpose:** End the plural/singular smell and prepare worker-side ExtraRules storage. This is a breaking YAML change; migration warn is added in Task 6.

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `cmd/agentdock/app.go`
- Modify: `cmd/agentdock/validate_test.go`

- [ ] **Step 1: Write failing test for new `worker.count` and `worker.prompt.extra_rules` YAML**

Append to `internal/config/config_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/config/ -run 'TestWorkerConfig_NewSection_YAMLLoad' -v
```

Expected: FAIL — `cfg.Worker` undefined.

- [ ] **Step 3: Rename struct and field in `internal/config/config.go`**

Replace the line (around line 36):
```go
	Workers           WorkersConfig            `yaml:"workers"`
```
with:
```go
	Worker            WorkerConfig             `yaml:"worker"`
```

Replace the existing `WorkersConfig` type definition (around line 75-77):
```go
type WorkersConfig struct {
	Count int `yaml:"count"`
}
```
with:
```go
type WorkerConfig struct {
	Count  int                `yaml:"count"`
	Prompt WorkerPromptConfig `yaml:"prompt"`
}

type WorkerPromptConfig struct {
	ExtraRules []string `yaml:"extra_rules"`
}
```

Update all `cfg.Workers.Count` references to `cfg.Worker.Count` in the same file. There are three such references around lines 149, 151, 154 (in `applyDefaults`). Also update the deprecation message at line 152 from `"max_concurrent 已棄用，請改用 workers.count"` to `"max_concurrent 已棄用，請改用 worker.count"`.

- [ ] **Step 4: Update `internal/config/config_test.go` to use `cfg.Worker.Count`**

Replace all six occurrences of `cfg.Workers.Count` with `cfg.Worker.Count` (around lines 241, 242, 269, 270, 281, 282). Also the map-key check at line 429: change `m["workers"]` handling to `m["worker"]` — for example:

Original (around 427–431):
```go
	workers := m["workers"].(map[string]any)
	if got := workers["count"]; got != cfg.Workers.Count {
		t.Errorf("DefaultsMap.workers.count=%v != applyDefaults.Workers.Count=%v", got, cfg.Workers.Count)
	}
```

Replace with:
```go
	worker := m["worker"].(map[string]any)
	if got := worker["count"]; got != cfg.Worker.Count {
		t.Errorf("DefaultsMap.worker.count=%v != applyDefaults.Worker.Count=%v", got, cfg.Worker.Count)
	}
```

- [ ] **Step 5: Update `cmd/agentdock/app.go`**

Find both `cfg.Workers.Count` references (lines 148, 185) and change to `cfg.Worker.Count`:

```bash
grep -n 'cfg\.Workers\.Count' cmd/agentdock/app.go
```

Replace each occurrence.

- [ ] **Step 6: Update `cmd/agentdock/validate_test.go`**

Find all `cfg.Workers.Count` references (lines 20, 29, 65) and change to `cfg.Worker.Count`.

- [ ] **Step 7: Run tests to verify pass and no regressions**

```bash
go build ./...
go test ./internal/config/ ./cmd/agentdock/ -count=1
```

Expected: build clean; all tests pass including the new `TestWorkerConfig_NewSection_YAMLLoad`.

Note: the existing tests reference YAML that says `workers:` (lines ~241 test setup). Search for remaining `workers:` YAML strings and change them to `worker:`:

```bash
grep -rn 'workers:' internal/config/ cmd/agentdock/ --include='*.go'
```

For any test YAML literal (usually backtick strings) containing `workers:\n  count: N`, change to `worker:\n  count: N`. Also search for `workers.count` in quoted strings:

```bash
grep -rn 'workers\.count' internal/config/ cmd/agentdock/ --include='*.go'
```

These appear in error messages or in `DefaultsMap` map-key lookups like `m["workers"]` — change to `m["worker"]` accordingly.

- [ ] **Step 8: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go cmd/agentdock/app.go cmd/agentdock/validate_test.go
git commit -m "refactor(config): rename workers -> worker, add WorkerPromptConfig

Ends the workers/worker plural/singular split by absorbing count into
a singular 'worker' section that also holds prompt.extra_rules. Breaking
YAML change: operators must rename 'workers:' to 'worker:' in config.yaml.
Migration warning added in a later task."
```

---

## Task 4: New `internal/worker/prompt.go` with XML `BuildPrompt`

**Purpose:** The heart of the refactor. Write the XML renderer and its tests — no callers yet, so this task is self-contained.

**Files:**
- Create: `internal/worker/prompt.go`
- Create: `internal/worker/prompt_test.go`

- [ ] **Step 1: Write the failing test file**

Create `internal/worker/prompt_test.go` with this content:

```go
package worker

import (
	"strings"
	"testing"

	"agentdock/internal/queue"
)

func TestBuildPrompt_Basic(t *testing.T) {
	ctx := queue.PromptContext{
		ThreadMessages: []queue.ThreadMessage{
			{User: "Alice", Timestamp: "2026-04-09 10:30", Text: "login broken"},
		},
		Channel:     "general",
		Reporter:    "Alice",
		Language:    "zh-TW",
		Goal:        "triage this",
		OutputRules: []string{"short reply"},
	}
	got := BuildPrompt(ctx, nil, nil)

	wants := []string{
		`<goal>triage this</goal>`,
		`<message user="Alice" ts="2026-04-09 10:30">login broken</message>`,
		`<channel>general</channel>`,
		`<reporter>Alice</reporter>`,
		`<response_language>zh-TW</response_language>`,
		`<rule>short reply</rule>`,
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("missing fragment %q in:\n%s", w, got)
		}
	}
}

func TestBuildPrompt_Ordering_GoalFirst_OutputRulesLast(t *testing.T) {
	ctx := queue.PromptContext{
		ThreadMessages: []queue.ThreadMessage{{User: "A", Timestamp: "1", Text: "t"}},
		Channel:        "c",
		Reporter:       "r",
		Language:       "en",
		Goal:           "g",
		OutputRules:    []string{"o"},
	}
	got := BuildPrompt(ctx, nil, nil)

	goalIdx := strings.Index(got, "<goal>")
	ctxIdx := strings.Index(got, "<thread_context>")
	rulesIdx := strings.Index(got, "<output_rules>")

	if goalIdx == -1 || ctxIdx == -1 || rulesIdx == -1 {
		t.Fatalf("missing sections: goal=%d thread=%d rules=%d", goalIdx, ctxIdx, rulesIdx)
	}
	if !(goalIdx < ctxIdx && ctxIdx < rulesIdx) {
		t.Errorf("expected goal < thread < output_rules, got goal=%d thread=%d rules=%d", goalIdx, ctxIdx, rulesIdx)
	}
}

func TestBuildPrompt_OptionalOmitted_ExtraDescriptionAndBranch(t *testing.T) {
	ctx := queue.PromptContext{
		ThreadMessages: []queue.ThreadMessage{{User: "A", Timestamp: "1", Text: "t"}},
		Channel:        "c",
		Reporter:       "r",
		// ExtraDescription and Branch intentionally empty.
		Language: "en",
		Goal:     "g",
	}
	got := BuildPrompt(ctx, nil, nil)

	if strings.Contains(got, "<extra_description>") {
		t.Errorf("expected no <extra_description>, got:\n%s", got)
	}
	if strings.Contains(got, "<branch>") {
		t.Errorf("expected no <branch>, got:\n%s", got)
	}
}

func TestBuildPrompt_OptionalOmitted_NoAttachments(t *testing.T) {
	ctx := queue.PromptContext{
		ThreadMessages: []queue.ThreadMessage{{User: "A", Timestamp: "1", Text: "t"}},
		Goal:           "g",
	}
	got := BuildPrompt(ctx, nil, nil)
	if strings.Contains(got, "<attachments>") {
		t.Errorf("expected no <attachments> when attachments nil, got:\n%s", got)
	}
}

func TestBuildPrompt_OptionalOmitted_EmptyOutputRules(t *testing.T) {
	ctx := queue.PromptContext{
		ThreadMessages: []queue.ThreadMessage{{User: "A", Timestamp: "1", Text: "t"}},
		Goal:           "g",
		OutputRules:    nil, // empty
	}
	got := BuildPrompt(ctx, nil, nil)
	if strings.Contains(got, "<output_rules>") {
		t.Errorf("expected no <output_rules> when empty, got:\n%s", got)
	}
}

func TestBuildPrompt_WorkerRulesToggle_AllowFalse_NoRules(t *testing.T) {
	allow := false
	ctx := queue.PromptContext{
		ThreadMessages:   []queue.ThreadMessage{{User: "A", Timestamp: "1", Text: "t"}},
		Goal:             "g",
		AllowWorkerRules: allow,
	}
	got := BuildPrompt(ctx, []string{"rule1"}, nil)
	if strings.Contains(got, "<additional_rules>") {
		t.Errorf("expected no <additional_rules> when AllowWorkerRules=false, got:\n%s", got)
	}
}

func TestBuildPrompt_WorkerRulesToggle_AllowTrue_EmptyRules_NoSection(t *testing.T) {
	ctx := queue.PromptContext{
		ThreadMessages:   []queue.ThreadMessage{{User: "A", Timestamp: "1", Text: "t"}},
		Goal:             "g",
		AllowWorkerRules: true,
	}
	got := BuildPrompt(ctx, nil, nil)
	if strings.Contains(got, "<additional_rules>") {
		t.Errorf("expected no <additional_rules> when ExtraRules empty, got:\n%s", got)
	}
}

func TestBuildPrompt_WorkerRulesToggle_AllowTrue_WithRules_Rendered(t *testing.T) {
	ctx := queue.PromptContext{
		ThreadMessages:   []queue.ThreadMessage{{User: "A", Timestamp: "1", Text: "t"}},
		Goal:             "g",
		AllowWorkerRules: true,
	}
	got := BuildPrompt(ctx, []string{"no guess", "real files only"}, nil)
	if !strings.Contains(got, "<additional_rules>") {
		t.Errorf("expected <additional_rules>, got:\n%s", got)
	}
	if !strings.Contains(got, "<rule>no guess</rule>") {
		t.Errorf("missing rule1 in:\n%s", got)
	}
	if !strings.Contains(got, "<rule>real files only</rule>") {
		t.Errorf("missing rule2 in:\n%s", got)
	}
}

func TestBuildPrompt_XMLEscaping(t *testing.T) {
	ctx := queue.PromptContext{
		ThreadMessages: []queue.ThreadMessage{
			{User: `<Alice & "Bob">`, Timestamp: "1", Text: `<script>alert("x")</script>`},
		},
		Channel:     "c",
		Reporter:    "r",
		Goal:        "g",
		OutputRules: []string{"< 100 chars"},
	}
	got := BuildPrompt(ctx, nil, nil)

	// No raw angle brackets in escaped content — only in XML tags we control.
	if strings.Contains(got, "<script>") {
		t.Errorf("unescaped <script> in output:\n%s", got)
	}
	if !strings.Contains(got, "&lt;script&gt;") && !strings.Contains(got, "&#60;script&#62;") {
		t.Errorf("expected escaped <script>, got:\n%s", got)
	}
	if !strings.Contains(got, "&amp;") {
		t.Errorf("expected escaped &amp;, got:\n%s", got)
	}
	if !strings.Contains(got, "&lt; 100 chars") && !strings.Contains(got, "&#60; 100 chars") {
		t.Errorf("expected escaped '< 100 chars', got:\n%s", got)
	}
}

func TestBuildPrompt_Attachments_ImageTextDocument(t *testing.T) {
	ctx := queue.PromptContext{
		ThreadMessages: []queue.ThreadMessage{{User: "A", Timestamp: "1", Text: "t"}},
		Goal:           "g",
	}
	atts := []AttachmentInfo{
		{Path: "/tmp/a.png", Name: "a.png", Type: "image"},
		{Path: "/tmp/b.log", Name: "b.log", Type: "text"},
		{Path: "/tmp/c.pdf", Name: "c.pdf", Type: "document"},
	}
	got := BuildPrompt(ctx, nil, atts)
	if !strings.Contains(got, `<attachment path="/tmp/a.png" type="image">use your file reading tools to view</attachment>`) {
		t.Errorf("image hint wrong, got:\n%s", got)
	}
	if !strings.Contains(got, `<attachment path="/tmp/b.log" type="text">read directly</attachment>`) {
		t.Errorf("text hint wrong, got:\n%s", got)
	}
	if !strings.Contains(got, `<attachment path="/tmp/c.pdf" type="document">document</attachment>`) {
		t.Errorf("document hint wrong, got:\n%s", got)
	}
}

func TestBuildPrompt_Attachments_UnknownTypeSelfClosing(t *testing.T) {
	ctx := queue.PromptContext{
		ThreadMessages: []queue.ThreadMessage{{User: "A", Timestamp: "1", Text: "t"}},
		Goal:           "g",
	}
	atts := []AttachmentInfo{
		{Path: "/tmp/z.bin", Name: "z.bin", Type: "binary"},
	}
	got := BuildPrompt(ctx, nil, atts)
	if !strings.Contains(got, `<attachment path="/tmp/z.bin" type="binary"/>`) {
		t.Errorf("unknown type should render self-closing, got:\n%s", got)
	}
}

func TestBuildPrompt_OutputRulesArray_MultipleRendered(t *testing.T) {
	ctx := queue.PromptContext{
		ThreadMessages: []queue.ThreadMessage{{User: "A", Timestamp: "1", Text: "t"}},
		Goal:           "g",
		OutputRules:    []string{"one-liner", "< 100 chars", "slack-friendly"},
	}
	got := BuildPrompt(ctx, nil, nil)
	for _, r := range []string{"one-liner", "slack-friendly"} {
		if !strings.Contains(got, "<rule>"+r+"</rule>") {
			t.Errorf("missing rule %q in output_rules:\n%s", r, got)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify all fail**

```bash
go test ./internal/worker/ -run TestBuildPrompt -v
```

Expected: all tests FAIL — `BuildPrompt` and `AttachmentInfo` undefined.

- [ ] **Step 3: Implement `internal/worker/prompt.go`**

Create `internal/worker/prompt.go` with this content:

```go
package worker

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"strings"

	"agentdock/internal/queue"
)

// AttachmentInfo describes a downloaded attachment available to the agent.
// Moved here from internal/bot when prompt assembly became worker-owned.
type AttachmentInfo struct {
	Path string
	Name string
	Type string // "image", "text", "document", or other mime-type
}

// BuildPrompt renders a queue.PromptContext plus worker-provided ExtraRules
// (gated by ctx.AllowWorkerRules) plus locally-resolved attachments into an
// XML-ish prompt string. The output is for LLM consumption, not for a strict
// XML parser — it is intentionally a sequence of top-level fragments rather
// than a single rooted document.
func BuildPrompt(ctx queue.PromptContext, extraRules []string, attachments []AttachmentInfo) string {
	var b strings.Builder

	// <goal> — always first for LLM attention; trust app to have defaulted it.
	fmt.Fprintf(&b, "<goal>%s</goal>\n\n", xmlEscape(ctx.Goal))

	// <thread_context> — the core content.
	b.WriteString("<thread_context>\n")
	for _, m := range ctx.ThreadMessages {
		fmt.Fprintf(&b,
			"  <message user=\"%s\" ts=\"%s\">%s</message>\n",
			xmlEscape(m.User), xmlEscape(m.Timestamp), xmlEscape(m.Text),
		)
	}
	b.WriteString("</thread_context>\n\n")

	// <extra_description> — optional.
	if ctx.ExtraDescription != "" {
		fmt.Fprintf(&b, "<extra_description>%s</extra_description>\n\n", xmlEscape(ctx.ExtraDescription))
	}

	// <issue_context> — channel, reporter, optional branch.
	b.WriteString("<issue_context>\n")
	fmt.Fprintf(&b, "  <channel>%s</channel>\n", xmlEscape(ctx.Channel))
	fmt.Fprintf(&b, "  <reporter>%s</reporter>\n", xmlEscape(ctx.Reporter))
	if ctx.Branch != "" {
		fmt.Fprintf(&b, "  <branch>%s</branch>\n", xmlEscape(ctx.Branch))
	}
	b.WriteString("</issue_context>\n\n")

	// <response_language> — always rendered if non-empty (the app has a default).
	if ctx.Language != "" {
		fmt.Fprintf(&b, "<response_language>%s</response_language>\n\n", xmlEscape(ctx.Language))
	}

	// <additional_rules> — only if AllowWorkerRules AND non-empty.
	if ctx.AllowWorkerRules && len(extraRules) > 0 {
		b.WriteString("<additional_rules>\n")
		for _, r := range extraRules {
			fmt.Fprintf(&b, "  <rule>%s</rule>\n", xmlEscape(r))
		}
		b.WriteString("</additional_rules>\n\n")
	}

	// <attachments> — only if present.
	if len(attachments) > 0 {
		b.WriteString("<attachments>\n")
		for _, a := range attachments {
			hint := attachmentHint(a.Type)
			if hint == "" {
				fmt.Fprintf(&b,
					"  <attachment path=\"%s\" type=\"%s\"/>\n",
					xmlEscape(a.Path), xmlEscape(a.Type),
				)
			} else {
				fmt.Fprintf(&b,
					"  <attachment path=\"%s\" type=\"%s\">%s</attachment>\n",
					xmlEscape(a.Path), xmlEscape(a.Type), xmlEscape(hint),
				)
			}
		}
		b.WriteString("</attachments>\n\n")
	}

	// <output_rules> — last, for LLM "what to produce" emphasis.
	if len(ctx.OutputRules) > 0 {
		b.WriteString("<output_rules>\n")
		for _, r := range ctx.OutputRules {
			fmt.Fprintf(&b, "  <rule>%s</rule>\n", xmlEscape(r))
		}
		b.WriteString("</output_rules>\n")
	}

	return strings.TrimRight(b.String(), "\n")
}

// xmlEscape escapes < > & ' " for XML text or attribute-value context.
// Uses encoding/xml.EscapeText which covers both.
func xmlEscape(s string) string {
	var buf bytes.Buffer
	_ = xml.EscapeText(&buf, []byte(s))
	return buf.String()
}

func attachmentHint(attType string) string {
	switch attType {
	case "image":
		return "use your file reading tools to view"
	case "text":
		return "read directly"
	case "document":
		return "document"
	default:
		return ""
	}
}
```

- [ ] **Step 4: Run tests to verify all pass**

```bash
go test ./internal/worker/ -run TestBuildPrompt -v
```

Expected: all 12 tests PASS.

- [ ] **Step 5: Run full worker test suite to ensure no unexpected interactions**

```bash
go test ./internal/worker/ -count=1
```

Expected: all tests pass.

- [ ] **Step 6: Commit**

```bash
git add internal/worker/prompt.go internal/worker/prompt_test.go
git commit -m "feat(worker): add XML prompt builder

New BuildPrompt renders queue.PromptContext + worker ExtraRules +
AttachmentInfo into XML. AttachmentInfo type lives here now (will be
cut from internal/bot in the cleanup task). Not yet wired to callers."
```

---

## Task 5: Wire app-side assembly and worker-side consumption

**Purpose:** Switch both sides at once. App stops calling `bot.BuildPrompt` and populates `Job.PromptContext`; worker reads `Job.PromptContext` and calls the new `worker.BuildPrompt`. The old `bot.BuildPrompt` function and `Job.Prompt` string field become unused (deleted in Task 7).

**Files:**
- Create: `internal/bot/prompt_context.go`
- Create: `internal/bot/prompt_context_test.go`
- Modify: `internal/bot/workflow.go`
- Modify: `internal/worker/executor.go`
- Modify: `internal/worker/pool.go`
- Modify: `internal/worker/pool_test.go` (if helpers break)
- Modify: `cmd/agentdock/worker.go`
- Modify: `cmd/agentdock/local_adapter.go`
- Modify: `internal/queue/integration_test.go`, `internal/queue/redis_integration_test.go`

- [ ] **Step 1: Write failing test for `AssemblePromptContext`**

Create `internal/bot/prompt_context_test.go`:

```go
package bot

import (
	"testing"

	"agentdock/internal/config"
	"agentdock/internal/queue"
)

func TestAssemblePromptContext_PassesConfigThrough(t *testing.T) {
	allow := false
	pc := config.PromptConfig{
		Language:         "zh-TW",
		Goal:             "custom",
		OutputRules:      []string{"one", "two"},
		AllowWorkerRules: &allow,
	}
	msgs := []queue.ThreadMessage{{User: "Alice", Timestamp: "1", Text: "t"}}

	got := AssemblePromptContext(msgs, "extra", "general", "Alice", "main", pc)

	if got.Language != "zh-TW" {
		t.Errorf("Language = %q", got.Language)
	}
	if got.Goal != "custom" {
		t.Errorf("Goal = %q", got.Goal)
	}
	if len(got.OutputRules) != 2 {
		t.Errorf("OutputRules = %v", got.OutputRules)
	}
	if got.AllowWorkerRules {
		t.Error("AllowWorkerRules = true, expected false")
	}
	if got.ExtraDescription != "extra" || got.Channel != "general" || got.Reporter != "Alice" || got.Branch != "main" {
		t.Errorf("pass-through fields wrong: %+v", got)
	}
	if got.ThreadMessages[0].User != "Alice" {
		t.Errorf("ThreadMessages not passed through: %+v", got.ThreadMessages)
	}
}

func TestAssemblePromptContext_NilAllowWorkerRulesDefaultsFalse(t *testing.T) {
	// Defensive: if cfg.Prompt.AllowWorkerRules is nil (applyDefaults
	// should have filled it, but we don't want a nil deref here),
	// the assembler treats it as false.
	pc := config.PromptConfig{AllowWorkerRules: nil}
	got := AssemblePromptContext(nil, "", "", "", "", pc)
	if got.AllowWorkerRules {
		t.Error("nil AllowWorkerRules pointer should assemble as false")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/bot/ -run TestAssemblePromptContext -v
```

Expected: FAIL — `AssemblePromptContext` undefined.

- [ ] **Step 3: Create `internal/bot/prompt_context.go`**

```go
package bot

import (
	"agentdock/internal/config"
	"agentdock/internal/queue"
)

// AssemblePromptContext packages Slack-thread inputs and app-side config
// into the wire struct the worker consumes. The app is intentionally
// unaware of the XML format — that concern lives in internal/worker.
func AssemblePromptContext(
	threadMsgs []queue.ThreadMessage,
	extraDesc, channel, reporter, branch string,
	pc config.PromptConfig,
) queue.PromptContext {
	allow := false
	if pc.AllowWorkerRules != nil {
		allow = *pc.AllowWorkerRules
	}
	return queue.PromptContext{
		ThreadMessages:   threadMsgs,
		ExtraDescription: extraDesc,
		Channel:          channel,
		Reporter:         reporter,
		Branch:           branch,
		Language:         pc.Language,
		Goal:             pc.Goal,
		OutputRules:      pc.OutputRules,
		AllowWorkerRules: allow,
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./internal/bot/ -run TestAssemblePromptContext -v
```

Expected: PASS.

- [ ] **Step 5: Update `internal/bot/workflow.go`**

Replace the existing block (around lines 404–437) that constructs `threadMsgs` and calls `BuildPrompt`:

Original:
```go
	// 2. Enrich messages.
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

	// 3. Collect attachment metadata.
	tempDir, err := os.MkdirTemp("", "triage-meta-*")
	...
	downloads := w.slack.DownloadAttachments(rawMsgs, tempDir)

	// 4. Build prompt.
	prompt := BuildPrompt(PromptInput{
		ThreadMessages:   threadMsgs,
		ExtraDescription: pt.ExtraDesc,
		Branch:           pt.SelectedBranch,
		Channel:          pt.ChannelName,
		Reporter:         pt.Reporter,
		Prompt:           w.cfg.Prompt,
	})
	pt.Logger.Info("Prompt 已組裝", "phase", "處理中", "length", len(prompt))
```

Replace with (removing the `BuildPrompt` call and switching to `queue.ThreadMessage`):

```go
	// 2. Enrich messages.
	var threadMsgs []queue.ThreadMessage
	for _, m := range rawMsgs {
		text := m.Text
		if w.mantisClient != nil {
			text = enrichMessage(text, w.mantisClient)
		}
		threadMsgs = append(threadMsgs, queue.ThreadMessage{
			User:      w.slack.ResolveUser(m.User),
			Timestamp: m.Timestamp,
			Text:      text,
		})
	}

	// 3. Collect attachment metadata.
	tempDir, err := os.MkdirTemp("", "triage-meta-*")
	...
	downloads := w.slack.DownloadAttachments(rawMsgs, tempDir)

	// 4. Assemble structured prompt context (worker renders the actual prompt).
	promptCtx := AssemblePromptContext(
		threadMsgs,
		pt.ExtraDesc,
		pt.ChannelName,
		pt.Reporter,
		pt.SelectedBranch,
		w.cfg.Prompt,
	)
	pt.Logger.Info("Prompt context 已組裝", "phase", "處理中",
		"thread_messages", len(promptCtx.ThreadMessages),
		"has_extra_desc", promptCtx.ExtraDescription != "",
	)
```

Then update the `job := &queue.Job{...}` literal (around lines 464–478): remove `Prompt: prompt,` and add `PromptContext: &promptCtx,`.

Specifically, replace:
```go
	job := &queue.Job{
		ID:          pt.RequestID,
		Priority:    w.channelPriority(pt.ChannelID),
		ChannelID:   pt.ChannelID,
		ThreadTS:    pt.ThreadTS,
		UserID:      pt.UserID,
		Repo:        pt.SelectedRepo,
		Branch:      pt.SelectedBranch,
		CloneURL:    cleanCloneURL(pt.SelectedRepo),
		Prompt:      prompt,
		Skills:      w.loadSkills(ctx),
		RequestID:   pt.RequestID,
		Attachments: attachMeta,
		SubmittedAt: time.Now(),
	}
```

With:
```go
	job := &queue.Job{
		ID:            pt.RequestID,
		Priority:      w.channelPriority(pt.ChannelID),
		ChannelID:     pt.ChannelID,
		ThreadTS:      pt.ThreadTS,
		UserID:        pt.UserID,
		Repo:          pt.SelectedRepo,
		Branch:        pt.SelectedBranch,
		CloneURL:      cleanCloneURL(pt.SelectedRepo),
		PromptContext: &promptCtx,
		Skills:        w.loadSkills(ctx),
		RequestID:     pt.RequestID,
		Attachments:   attachMeta,
		SubmittedAt:   time.Now(),
	}
```

Also add a guard for empty ThreadMessages just before building promptCtx:

```go
	if len(threadMsgs) == 0 {
		w.notifyError(pt.Logger, pt.ChannelID, pt.ThreadTS, "Thread has no messages to process")
		w.clearDedup(pt)
		return
	}
```

Place this guard right before the `// 3. Collect attachment metadata.` comment.

- [ ] **Step 6: Update `internal/worker/pool.go`**

Add `ExtraRules []string` to `worker.Config` (around line 28, just after `WorkerSecrets`):

Original:
```go
type Config struct {
	Queue          queue.JobQueue
	...
	SecretKey      []byte
	WorkerSecrets  map[string]string
}
```

Updated:
```go
type Config struct {
	Queue          queue.JobQueue
	...
	SecretKey      []byte
	WorkerSecrets  map[string]string
	ExtraRules     []string // from cfg.Worker.Prompt.ExtraRules
}
```

Then plumb it into `executionDeps` in `runWorker` (around line 193):

Find the block:
```go
	deps := executionDeps{
		attachments:   p.cfg.Attachments,
		repoCache:     p.cfg.RepoCache,
		runner:        p.cfg.Runner,
		store:         p.cfg.Store,
		skillDirs:     p.cfg.SkillDirs,
		secretKey:     p.cfg.SecretKey,
		workerSecrets: p.cfg.WorkerSecrets,
	}
```

Change to:
```go
	deps := executionDeps{
		attachments:   p.cfg.Attachments,
		repoCache:     p.cfg.RepoCache,
		runner:        p.cfg.Runner,
		store:         p.cfg.Store,
		skillDirs:     p.cfg.SkillDirs,
		secretKey:     p.cfg.SecretKey,
		workerSecrets: p.cfg.WorkerSecrets,
		extraRules:    p.cfg.ExtraRules,
	}
```

- [ ] **Step 7: Update `internal/worker/executor.go`**

Add `extraRules []string` to `executionDeps` struct (around line 31–39):

Original:
```go
type executionDeps struct {
	attachments   queue.AttachmentStore
	repoCache     RepoProvider
	runner        Runner
	store         queue.JobStore
	skillDirs     []string
	secretKey     []byte
	workerSecrets map[string]string
}
```

Updated:
```go
type executionDeps struct {
	attachments   queue.AttachmentStore
	repoCache     RepoProvider
	runner        Runner
	store         queue.JobStore
	skillDirs     []string
	secretKey     []byte
	workerSecrets map[string]string
	extraRules    []string
}
```

Replace the prompt-building block (around lines 99–110):

Original:
```go
	// Write attachments into worktree — cleaned up together with RemoveWorktree.
	prompt := job.Prompt
	if len(attachments) > 0 {
		attachDir := filepath.Join(repoPath, ".attachments")
		attachInfos, err := writeAttachments(attachments, attachDir)
		if err != nil {
			logger.Warn("附件寫入失敗，繼續執行", "phase", "處理中", "error", err)
		} else {
			prompt = bot.AppendAttachmentSection(prompt, attachInfos)
			logger.Info("附件已寫入", "phase", "處理中", "count", len(attachInfos), "dir", attachDir)
		}
	}
```

With:
```go
	// Defensive: new schema requires PromptContext. drain-and-cut means old
	// Job.Prompt-only jobs shouldn't exist, but fail clearly if one slips through.
	if job.PromptContext == nil {
		return failedResult(job, startedAt, fmt.Errorf("malformed job: missing prompt_context"), repoPath)
	}

	// Write attachments into worktree — cleaned up together with RemoveWorktree.
	var attachInfos []AttachmentInfo
	if len(attachments) > 0 {
		attachDir := filepath.Join(repoPath, ".attachments")
		var err error
		attachInfos, err = writeAttachments(attachments, attachDir)
		if err != nil {
			logger.Warn("附件寫入失敗，繼續執行", "phase", "處理中", "error", err)
		} else {
			logger.Info("附件已寫入", "phase", "處理中", "count", len(attachInfos), "dir", attachDir)
		}
	}

	// Build XML prompt from structured context + worker-owned extra rules.
	prompt := BuildPrompt(*job.PromptContext, deps.extraRules, attachInfos)
	logger.Info("Prompt 已組裝", "phase", "處理中", "length", len(prompt))
```

Update `writeAttachments` return type (around line 193):

Original:
```go
func writeAttachments(attachments []queue.AttachmentReady, dir string) ([]bot.AttachmentInfo, error) {
	...
	var infos []bot.AttachmentInfo
	...
		infos = append(infos, bot.AttachmentInfo{
			Path: path,
			Name: filename,
			Type: att.MimeType,
		})
```

Updated (drop `bot.` prefix since `AttachmentInfo` lives in worker package now):

```go
func writeAttachments(attachments []queue.AttachmentReady, dir string) ([]AttachmentInfo, error) {
	...
	var infos []AttachmentInfo
	...
		infos = append(infos, AttachmentInfo{
			Path: path,
			Name: filename,
			Type: att.MimeType,
		})
```

Remove any now-unused `"agentdock/internal/bot"` import if it's no longer needed. (It's still needed for `bot.RunOptions`, so keep it — just drop the `bot.AttachmentInfo` reference.)

- [ ] **Step 8: Update `cmd/agentdock/worker.go`**

Find the `worker.NewPool(worker.Config{...})` call (around line 93) and add the `ExtraRules` field:

```go
	pool := worker.NewPool(worker.Config{
		Queue:          bundle.Queue,
		...
		WorkerSecrets:  workerSecrets,
		ExtraRules:     cfg.Worker.Prompt.ExtraRules,
	})
```

- [ ] **Step 9: Update `cmd/agentdock/local_adapter.go`**

The adapter exposes a `LocalAdapterConfig` struct (around line 12) that the caller populates. Add a field to that struct, then plumb to `worker.Config`.

Add to `LocalAdapterConfig`:
```go
	ExtraRules     []string
```

In `Start()` (around line 39), add to the `worker.Config{...}` literal:
```go
		Logger:         a.cfg.Logger,
		ExtraRules:     a.cfg.ExtraRules,
```

Callers of `NewLocalAdapter` (search with `grep -rn 'NewLocalAdapter' cmd/agentdock/`) must pass `ExtraRules: cfg.Worker.Prompt.ExtraRules` in their `LocalAdapterConfig{...}` literal. Typical caller is `cmd/agentdock/app.go` — add the field to that literal.

- [ ] **Step 10: Update `internal/queue/integration_test.go` and `redis_integration_test.go`**

These tests submit jobs directly to the queue and previously set `job.Prompt = "..."`. Grep for them:

```bash
grep -n 'Prompt:' internal/queue/integration_test.go internal/queue/redis_integration_test.go
```

For each `Prompt: "...",` line in a `queue.Job{}` literal, replace with a minimal `PromptContext`. Example:

Original:
```go
	job := queue.Job{
		ID:     "test-1",
		Prompt: "hello",
		...
	}
```

Replace with:
```go
	job := queue.Job{
		ID: "test-1",
		PromptContext: &queue.PromptContext{
			ThreadMessages: []queue.ThreadMessage{{User: "T", Timestamp: "1", Text: "hello"}},
			Channel:        "test",
			Reporter:       "tester",
			Goal:           "test goal",
		},
		...
	}
```

The assertions in these tests that check `job.Prompt` need updating — most likely they check the raw string that the runner receives. That runner now receives the XML-rendered string. If tests assert `runner got "hello"`, change to assert `strings.Contains(runnerGot, "hello")` since "hello" will now appear inside `<message>` tags.

- [ ] **Step 11: Update `internal/worker/pool_test.go` if it breaks**

Pool tests submit jobs and previously used `Prompt:` strings. Search:

```bash
grep -n 'Prompt:' internal/worker/pool_test.go
```

For each `Prompt: "...",` in a `queue.Job{...}` literal, replace with:

```go
		PromptContext: &queue.PromptContext{
			ThreadMessages: []queue.ThreadMessage{{User: "T", Timestamp: "1", Text: "hello"}},
			Channel:        "test",
			Reporter:       "tester",
			Goal:           "test goal",
		},
```

If a test asserts on the exact prompt string (e.g., `if gotPrompt != "hello"`), change it to a substring check: `if !strings.Contains(gotPrompt, "hello")`. The worker now wraps the text in XML so exact-match assertions fail.

- [ ] **Step 12: Build and run all tests**

```bash
go build ./...
go test ./... -count=1
```

Expected: build clean; all tests pass.

If a test fails because it asserts on the literal prompt string, update the assertion to match XML output (use `strings.Contains` for substrings).

- [ ] **Step 13: Commit**

```bash
git add internal/bot/prompt_context.go internal/bot/prompt_context_test.go \
        internal/bot/workflow.go \
        internal/worker/executor.go internal/worker/pool.go internal/worker/pool_test.go \
        cmd/agentdock/worker.go cmd/agentdock/local_adapter.go \
        internal/queue/integration_test.go internal/queue/redis_integration_test.go
git commit -m "refactor: wire app to PromptContext, worker to BuildPrompt

App (bot.AssemblePromptContext) now assembles structured context and
sets job.PromptContext. Worker (executor.go) calls worker.BuildPrompt
with the context, worker-config extra rules, and locally-resolved
attachments. bot.BuildPrompt and bot.AppendAttachmentSection are no
longer called from anywhere; removed in the cleanup task.

Worker defensively fails if PromptContext is nil (drain-and-cut means
this should never happen, but better a clear error than empty prompt).

Config plumbing: Pool.Config gains ExtraRules field, passed from
cfg.Worker.Prompt.ExtraRules via worker and local-adapter commands."
```

---

## Task 6: Config migration warnings for legacy keys

**Purpose:** Detect `prompt.extra_rules` and `workers.count` in loaded YAML and emit specific WARN messages pointing operators to the new locations. Does not remap values; operator must fix their YAML.

**Files:**
- Modify: `cmd/agentdock/config.go` (koanf key inspection)
- Modify: `cmd/agentdock/config_test.go` (create if missing) or append to existing test file

- [ ] **Step 1: Write failing test**

Create or append to `cmd/agentdock/config_test.go` (check if it exists first with `ls cmd/agentdock/config_test.go`):

```go
func TestConfig_LegacyPromptExtraRules_Warns(t *testing.T) {
	yaml := `
prompt:
  language: zh-TW
  extra_rules:
    - "legacy rule"
`
	logs := captureLogs(t, func() {
		loadConfigTestHelper(t, yaml)
	})
	if !strings.Contains(logs, "prompt.extra_rules") {
		t.Errorf("expected migration warn mentioning 'prompt.extra_rules', got:\n%s", logs)
	}
	if !strings.Contains(logs, "worker.prompt.extra_rules") {
		t.Errorf("expected warn to point at new location, got:\n%s", logs)
	}
}

func TestConfig_LegacyWorkersCount_Warns(t *testing.T) {
	yaml := `
workers:
  count: 5
`
	logs := captureLogs(t, func() {
		loadConfigTestHelper(t, yaml)
	})
	if !strings.Contains(logs, "workers.count") {
		t.Errorf("expected migration warn mentioning 'workers.count', got:\n%s", logs)
	}
	if !strings.Contains(logs, "worker.count") {
		t.Errorf("expected warn to point at new location, got:\n%s", logs)
	}
}
```

Helpers (add to the same test file):

```go
// captureLogs redirects slog.Default to a buffer, runs fn, returns captured text.
func captureLogs(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	orig := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(orig)
	fn()
	return buf.String()
}

// loadConfigTestHelper runs buildKoanf against a temp YAML and returns the Config.
func loadConfigTestHelper(t *testing.T, yamlStr string) *config.Config {
	t.Helper()
	tmp := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(tmp, []byte(yamlStr), 0600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	cmd := &cobra.Command{}
	cfg, _, _, _, err := buildKoanf(cmd, tmp)
	if err != nil {
		t.Fatalf("buildKoanf: %v", err)
	}
	return cfg
}
```

Imports needed: `bytes`, `log/slog`, `os`, `path/filepath`, `strings`, `testing`, `agentdock/internal/config`, `github.com/spf13/cobra`.

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./cmd/agentdock/ -run 'TestConfig_Legacy.*_Warns' -v
```

Expected: FAIL — no warn output yet.

- [ ] **Step 3: Add specific migration warning function to `cmd/agentdock/config.go`**

Insert this function after `warnUnknownKeys` (around line 302):

```go
// warnLegacyMigrationKeys emits WARN logs for specific YAML keys that were
// renamed in the prompt refactor (docs/superpowers/specs/2026-04-18-*),
// pointing operators at the new location. This is in addition to
// warnUnknownKeys, which also fires generically.
func warnLegacyMigrationKeys(k *koanf.Koanf) {
	if k.Exists("prompt.extra_rules") {
		slog.Warn(
			"prompt.extra_rules 已搬到 worker.prompt.extra_rules，本設定忽略",
			"phase", "設定",
			"migration", "prompt-refactor",
			"old_key", "prompt.extra_rules",
			"new_key", "worker.prompt.extra_rules",
		)
	}
	if k.Exists("workers.count") && !k.Exists("worker.count") {
		slog.Warn(
			"workers.count 已 rename 為 worker.count，本設定忽略",
			"phase", "設定",
			"migration", "prompt-refactor",
			"old_key", "workers.count",
			"new_key", "worker.count",
		)
	}
}
```

Call it from `buildKoanf` — insert one line right after the existing `warnUnknownKeys(kEff)` call (around line 67):

```go
			warnUnknownKeys(kEff)
			warnLegacyMigrationKeys(kEff)
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./cmd/agentdock/ -run 'TestConfig_Legacy.*_Warns' -v
```

Expected: PASS.

- [ ] **Step 5: Run full cmd test suite to verify no regressions**

```bash
go test ./cmd/agentdock/ -count=1
```

Expected: all tests pass.

- [ ] **Step 6: Commit**

```bash
git add cmd/agentdock/config.go cmd/agentdock/config_test.go
git commit -m "feat(config): specific migration warns for prompt-refactor legacy keys

warnLegacyMigrationKeys fires alongside the generic warnUnknownKeys and
tells operators exactly where prompt.extra_rules and workers.count have
moved. No remap, no fail-fast — operator reads the warn and edits YAML."
```

---

## Task 7: Delete legacy code

**Purpose:** Remove the dead code: `Job.Prompt` string, `PromptConfig.ExtraRules`, `internal/bot/prompt.go`, `internal/bot/prompt_test.go`. After this, `grep -r 'BuildPrompt\|AppendAttachmentSection' internal/bot/` returns nothing.

**Files:**
- Delete: `internal/bot/prompt.go`
- Delete: `internal/bot/prompt_test.go`
- Modify: `internal/queue/job.go` (drop `Job.Prompt`)
- Modify: `internal/config/config.go` (drop `PromptConfig.ExtraRules`)
- Modify: `internal/config/config_test.go` (drop ExtraRules test)

- [ ] **Step 1: Verify no remaining callers**

```bash
grep -rn 'bot.BuildPrompt\|bot.AppendAttachmentSection\|bot.ThreadMessage\|bot.AttachmentInfo\|bot.PromptInput' --include='*.go'
```

Expected: zero results. If any remain, fix them before proceeding.

```bash
grep -rn 'job.Prompt\b\|\.Prompt\s*=\s*' internal/ cmd/ --include='*.go' | grep -v 'PromptContext' | grep -v 'promptCtx'
```

Expected: results should only be in tests or other unrelated contexts, not wiring to the agent runner. If you see `job.Prompt = ...` or `runner.Run(ctx, repoPath, job.Prompt, ...)`, that's a bug — fix before proceeding.

```bash
grep -rn 'cfg.Prompt.ExtraRules\|Prompt.ExtraRules' --include='*.go'
```

Expected: zero results. The only remaining reference to `ExtraRules` should be in `internal/config/config.go` (the field itself) and in the old `internal/config/config_test.go` test asserting on it.

- [ ] **Step 2: Delete `internal/bot/prompt.go`**

```bash
rm internal/bot/prompt.go
```

- [ ] **Step 3: Delete `internal/bot/prompt_test.go`**

```bash
rm internal/bot/prompt_test.go
```

- [ ] **Step 4: Remove `Job.Prompt` field from `internal/queue/job.go`**

Find the line (around line 33):
```go
	Prompt      string                    `json:"prompt"`
```

Delete it.

- [ ] **Step 5: Remove `PromptConfig.ExtraRules` field**

In `internal/config/config.go`, find the `PromptConfig` struct and delete the `ExtraRules` field:

Before:
```go
type PromptConfig struct {
	Language         string   `yaml:"language"`
	ExtraRules       []string `yaml:"extra_rules"` // deprecated
	Goal             string   `yaml:"goal"`
	OutputRules      []string `yaml:"output_rules"`
	AllowWorkerRules *bool    `yaml:"allow_worker_rules"`
}
```

After:
```go
type PromptConfig struct {
	Language         string   `yaml:"language"`
	Goal             string   `yaml:"goal"`
	OutputRules      []string `yaml:"output_rules"`
	AllowWorkerRules *bool    `yaml:"allow_worker_rules"`
}
```

- [ ] **Step 6: Remove the existing ExtraRules test**

In `internal/config/config_test.go`, find and delete the assertion (around line 114):
```go
	if len(cfg.Prompt.ExtraRules) != 2 {
		t.Errorf("extra_rules = %v", cfg.Prompt.ExtraRules)
	}
```

If there's surrounding YAML that sets `extra_rules:` to establish this test case, that YAML input is now the migration-warn trigger — either drop the extra_rules lines from the YAML setup or leave them (they should now produce a WARN rather than populate a struct field, which is fine for this test).

- [ ] **Step 7: Build and run all tests**

```bash
go build ./...
go test ./... -count=1
```

Expected: build clean, all tests pass.

If anything fails with "undefined: bot.BuildPrompt" or similar, there's a caller I missed — grep again, fix, re-run.

- [ ] **Step 8: Commit**

```bash
git add -u  # stages deletions and modifications
git commit -m "refactor: delete legacy prompt builder and dead fields

Removes internal/bot/prompt.go, its tests, Job.Prompt string field,
and PromptConfig.ExtraRules. All callers migrated in earlier tasks.

Closes #61."
```

---

## Task 8: Migration doc update

**Purpose:** Record the breaking YAML changes so operators upgrading past this release know what to edit.

**Files:**
- Modify: `docs/MIGRATION-v1.md` or create `docs/MIGRATION-prompt-refactor.md`

- [ ] **Step 1: Check existing migration doc structure**

```bash
head -60 docs/MIGRATION-v1.md
```

Decide whether to append a new section to `MIGRATION-v1.md` or create a dedicated `MIGRATION-prompt-refactor.md`. If the existing doc has a clear "post-v1 changes" section, append. Otherwise, create a new file.

- [ ] **Step 2: Write the migration note**

Use this content (adapt path depending on Step 1 decision):

```markdown
## Prompt refactor (#61) — breaking YAML changes

As of release X.Y.0 (worker prompt refactor), three YAML changes are required:

### Required

1. **Rename `workers:` → `worker:`** and move `count` inside.

   Before:
   ```yaml
   workers:
     count: 3
   ```

   After:
   ```yaml
   worker:
     count: 3
   ```

2. **Move `prompt.extra_rules` → `worker.prompt.extra_rules`.**

   Before:
   ```yaml
   prompt:
     language: zh-TW
     extra_rules:
       - "no guessing"
   ```

   After:
   ```yaml
   prompt:
     language: zh-TW
   worker:
     prompt:
       extra_rules:
         - "no guessing"
   ```

The app logs a specific WARN at startup if either old key is present, pointing you at the new location. Values at the old paths are ignored; no automatic remap.

### Optional additions (have defaults)

```yaml
prompt:
  goal: "Use the /triage-issue skill to investigate and produce a triage result."
  output_rules: []           # default empty; set to rendered rules you want the agent to follow
  allow_worker_rules: true   # gate whether worker.prompt.extra_rules applies per job
```

### Deploy procedure

This refactor changed the job payload wire format. Dual-schema support was explicitly rejected; you must drain the queue before rolling the new binary:

1. Scale the app deployment to zero (or manually stop it) so no new jobs arrive.
2. Wait for `/status` to report `queue_depth: 0` with no running jobs.
3. Deploy the new binary + config to both app and worker roles.
4. Start workers first, then the app.
5. Verify the first few jobs complete successfully.
```

- [ ] **Step 3: Commit**

```bash
git add docs/MIGRATION-v1.md # or the new file
git commit -m "docs: migration notes for worker prompt refactor (#61)"
```

---

## Self-Review Checklist (run before handoff)

- [ ] **Spec coverage check.** Every numbered section in the spec should map to a task:
  - Spec §5 Architecture → Tasks 1, 5
  - Spec §6 Components & Config → Tasks 2, 3, 4
  - Spec §7 Wire Protocol → Task 1
  - Spec §8 XML Template → Task 4
  - Spec §9 Error Handling → Task 5 (nil check), Task 4 (optional section rules)
  - Spec §10 Deploy & Migration → Task 6 (warnings), Task 8 (docs)
  - Spec §11 Testing → spread across tasks (each task writes its own tests)

- [ ] **No placeholders.** Scan for: "TBD", "TODO", "similar to", "fill in", "add error handling". Should be zero hits.

- [ ] **Type consistency.**
  - `queue.PromptContext`, `queue.ThreadMessage` — defined Task 1, used Tasks 4, 5
  - `worker.AttachmentInfo` — defined Task 4, used Task 5 (executor.go writeAttachments)
  - `worker.BuildPrompt(ctx queue.PromptContext, extraRules []string, attachments []AttachmentInfo) string` — defined Task 4, called Task 5
  - `bot.AssemblePromptContext(msgs, extraDesc, channel, reporter, branch, pc) queue.PromptContext` — defined Task 5, called Task 5 workflow.go
  - `config.WorkerConfig{Count int, Prompt WorkerPromptConfig}` — defined Task 3, read Tasks 5, 7
  - `config.PromptConfig.AllowWorkerRules *bool` (pointer for tri-state) — defined Task 2, read Task 5
  - `worker.Config.ExtraRules []string` — defined Task 5, consumed by executor.go `deps.extraRules`

- [ ] **Compilation order.** Each task should leave the build green:
  - Task 1: adds new types without touching existing code. ✓
  - Task 2: adds new fields; keeps `ExtraRules` for now. ✓
  - Task 3: renames `Workers`→`Worker`; touches all callers atomically. ✓
  - Task 4: new file, no callers yet. ✓
  - Task 5: rewires callers; the old `bot.BuildPrompt` has no more callers afterward. ✓
  - Task 6: pure addition (warning function). ✓
  - Task 7: deletes what Task 5 orphaned. ✓

---

## Execution Handoff

**Plan complete and saved to `docs/superpowers/plans/2026-04-18-worker-prompt-refactor.md`. Two execution options:**

**1. Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks, fast iteration.

**2. Inline Execution** — Execute tasks in this session using executing-plans, batch execution with checkpoints.

**Which approach?**
