# Workflow Types Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Refactor AgentDock from a single hard-coded "create GitHub issue" pipeline into a polymorphic `Workflow` interface with three first-class types (`issue`, `ask`, `pr_review`).

**Architecture:** New `app/workflow/` package owns per-workflow logic (Trigger, Selection, BuildJob, HandleResult) behind a common interface and registry. `app/bot/` shrinks to Slack plumbing. `Job.TaskType` is the app-side dispatch key; worker remains workflow-agnostic (only learns to prepare an empty work-dir for no-clone jobs). Config schema upgrades to `prompt.{issue,ask,pr_review}.*` with legacy alias for the flat form. Metrics unify into `WorkflowCompletionsTotal{workflow, status}`.

**Tech Stack:**
- Go 1.25 (`app/go.mod`, `worker/go.mod`, `shared/go.mod`)
- Existing `shared/queue` wire types (add `WorkflowArgs`; trim `JobResult`)
- Existing `shared/metrics` (Prometheus); replace Issue-specific counters
- Existing `github.com/slack-go/slack` client
- Existing `shared/github` client for PR URL validation
- Existing baked-in `agents/skills/github-pr-review/` + `shared/prreview` (skill already shipped; this plan only orchestrates)

**Spec:** [`docs/superpowers/specs/2026-04-20-workflow-types-design.md`](../specs/2026-04-20-workflow-types-design.md)

---

## File Structure

```
app/workflow/                              # new package
├── workflow.go                             # Workflow interface + Pending envelope + NextStep + TriggerEvent
├── workflow_test.go
├── registry.go                             # map[string]Workflow, Register / Get
├── registry_test.go
├── dispatcher.go                           # verb parser + dispatch + D-selector wiring
├── dispatcher_test.go
├── ports.go                                # SlackPort / IssueCreator / GitHubPR interfaces
├── issue.go                                # IssueWorkflow (relocated from app/bot)
├── issue_parser.go                         # TriageResult + ParseAgentOutput (relocated from app/bot/parser.go)
├── issue_parser_test.go
├── issue_test.go
├── ask.go                                  # AskWorkflow
├── ask_parser.go
├── ask_parser_test.go
├── ask_test.go
├── pr_review.go                            # PRReviewWorkflow
├── pr_review_parser.go
├── pr_review_parser_test.go
├── pr_review_test.go
└── pr_review_url.go                        # URL parsing + GitHub API validation

app/bot/                                    # shrinks
├── workflow.go                             # MODIFY: keep Handler glue + pending-state map; forward to dispatcher
├── workflow_test.go                        # MODIFY
├── result_listener.go                      # MODIFY: thin dispatcher
├── result_listener_test.go                 # MODIFY
├── retry_handler.go                        # MODIFY: stay, but call dispatcher.Dispatch on click
├── parser.go                               # DELETE (moved to app/workflow/issue_parser.go)
└── parser_test.go                          # DELETE (moved to app/workflow/issue_parser_test.go)

app/config/
├── config.go                               # MODIFY: nested PromptConfig + PRReviewConfig
├── config_test.go                          # MODIFY
├── defaults.go                             # MODIFY: ApplyDefaults for each workflow section
└── load.go                                 # MODIFY: alias flat prompt.goal → prompt.issue.goal

app/slack/
├── client.go                               # MODIFY: generalise OpenDescriptionModal → OpenTextInputModal
└── client_test.go                          # MODIFY

shared/queue/
├── job.go                                  # MODIFY: add WorkflowArgs; shrink JobResult (remove Issue-specific fields)
└── job_test.go                             # MODIFY

shared/metrics/
├── metrics.go                              # MODIFY: replace Issue-specific counters with Workflow-labelled ones
└── metrics_test.go                         # MODIFY

worker/pool/
├── workdir.go                              # new: WorkDirProvider interface + Repo/Empty impls
├── workdir_test.go                         # new: spike test covers empty-dir skill mount for each agent runner
├── executor.go                             # MODIFY: route Prepare/Cleanup through WorkDirProvider
└── pool.go                                 # MODIFY: construct + inject WorkDirProvider

cmd/agentdock/
└── app.go                                  # MODIFY: register workflows with dispatcher + wire action handlers
```

---

## Phase 1 — Skeleton

Create `app/workflow/` package with interface, types, registry, and dispatcher shell. No behaviour change: stub workflows return "not implemented" and are not yet wired into `cmd/agentdock/app.go`.

### Task 1.1: Create `Workflow` interface + support types

**Files:**
- Create: `app/workflow/workflow.go`
- Create: `app/workflow/workflow_test.go`

- [ ] **Step 1: Write the failing test**

`app/workflow/workflow_test.go`:

```go
package workflow

import (
	"context"
	"testing"

	"github.com/Ivantseng123/agentdock/shared/queue"
)

type fakeWorkflow struct{ typ string }

func (f *fakeWorkflow) Type() string { return f.typ }
func (f *fakeWorkflow) Trigger(ctx context.Context, ev TriggerEvent, args string) (NextStep, error) {
	return NextStep{Kind: NextStepSubmit}, nil
}
func (f *fakeWorkflow) Selection(ctx context.Context, p *Pending, value string) (NextStep, error) {
	return NextStep{Kind: NextStepSubmit}, nil
}
func (f *fakeWorkflow) BuildJob(ctx context.Context, p *Pending) (*queue.Job, string, error) {
	return &queue.Job{TaskType: f.typ}, "status", nil
}
func (f *fakeWorkflow) HandleResult(ctx context.Context, job *queue.Job, r *queue.JobResult) error {
	return nil
}

func TestWorkflowInterfaceCompiles(t *testing.T) {
	var w Workflow = &fakeWorkflow{typ: "issue"}
	if w.Type() != "issue" {
		t.Errorf("Type() = %q, want issue", w.Type())
	}
}

func TestNextStepKinds(t *testing.T) {
	tests := []struct {
		name string
		kind NextStepKind
	}{
		{"post selector", NextStepPostSelector},
		{"open modal", NextStepOpenModal},
		{"submit", NextStepSubmit},
		{"error", NextStepError},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := NextStep{Kind: tc.kind}
			if s.Kind != tc.kind {
				t.Errorf("Kind = %v", s.Kind)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `(cd app && go test ./workflow/ -run TestWorkflowInterfaceCompiles)`
Expected: FAIL with "workflow (undefined)" or similar — package doesn't exist.

- [ ] **Step 3: Create interface + types**

`app/workflow/workflow.go`:

```go
// Package workflow implements polymorphic workflow dispatch for Slack-triggered
// agent jobs. Three concrete workflows (issue, ask, pr_review) implement the
// Workflow interface; a registry routes by Job.TaskType; a dispatcher parses
// @bot mentions and routes to the right workflow entry point.
//
// This package deliberately does not know Slack internals — it talks to Slack
// through the SlackPort interface defined in ports.go.
package workflow

import (
	"context"

	"github.com/Ivantseng123/agentdock/shared/queue"
)

// TriggerEvent is the app-bot mention event handed to a workflow's Trigger.
// Mirrors app/slack.TriggerEvent but is owned here so the workflow package
// does not import the slack adapter directly.
type TriggerEvent struct {
	ChannelID string
	ThreadTS  string
	TriggerTS string
	UserID    string
	Text      string // raw text after the bot mention tag
}

// Pending captures multi-step wizard state. Common fields are flat on the
// envelope; per-workflow state lives in the opaque State field. Each workflow
// type-asserts State to its own state struct.
type Pending struct {
	ChannelID   string
	ThreadTS    string
	TriggerTS   string
	UserID      string
	Reporter    string
	ChannelName string
	RequestID   string
	SelectorTS  string // TS of the latest selector/modal message; used as pending-map key
	Phase       string // workflow-defined phase label
	TaskType    string // workflow identity, equal to Workflow.Type()
	State       any    // per-workflow state struct
}

// NextStepKind enumerates the actions a workflow's Trigger/Selection can
// request from the dispatcher. The dispatcher executes these against the
// SlackPort so workflows stay Slack-agnostic.
type NextStepKind int

const (
	NextStepPostSelector NextStepKind = iota
	NextStepOpenModal
	NextStepSubmit
	NextStepError
	NextStepNoop // used when the workflow handled everything in-place (rare)
)

// NextStep is a discriminated union of what the dispatcher should do next.
// Only the field matching Kind is read.
type NextStep struct {
	Kind NextStepKind

	// PostSelector — Kind == NextStepPostSelector
	SelectorPrompt  string
	SelectorActions []SelectorAction
	SelectorBack    string // optional "back" action ID; empty = no back button

	// OpenModal — Kind == NextStepOpenModal
	ModalTriggerID string
	ModalTitle     string
	ModalLabel     string
	ModalInputName string
	ModalMetadata  string // persisted in modal's private_metadata

	// Submit — Kind == NextStepSubmit (no fields; dispatcher calls BuildJob)

	// Error — Kind == NextStepError
	ErrorText string

	// For all kinds, the workflow carries its pending forward by storing it
	// into the dispatcher's pending-map under the selector/modal TS. The
	// dispatcher sets Pending.SelectorTS after it posts the selector/modal.
	Pending *Pending
}

// SelectorAction is one button in a button-selector message.
type SelectorAction struct {
	ActionID string
	Label    string
	Value    string
}

// Workflow is the polymorphic contract each workflow type implements.
type Workflow interface {
	// Type is the Job.TaskType discriminator. One of "issue", "ask",
	// "pr_review"; the value lands in Job.TaskType and in metrics labels.
	Type() string

	// Trigger is called on a fresh @bot mention. args is the remainder of
	// the mention after the verb has been stripped (the dispatcher handles
	// verb parsing).
	Trigger(ctx context.Context, ev TriggerEvent, args string) (NextStep, error)

	// Selection is called on button-click or modal-submit, carrying the
	// workflow's own pending state and the user's selected value.
	Selection(ctx context.Context, p *Pending, value string) (NextStep, error)

	// BuildJob assembles the queue.Job plus the status-message text the
	// dispatcher posts while the worker runs. TaskType must equal Type().
	BuildJob(ctx context.Context, p *Pending) (job *queue.Job, statusText string, err error)

	// HandleResult is called by ResultListener after the worker returns
	// a result for a job whose TaskType matches this workflow. The workflow
	// owns parsing, Slack posting, optional GitHub side-effects, retry-button
	// decisions, and dedup-clear.
	HandleResult(ctx context.Context, job *queue.Job, result *queue.JobResult) error
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `(cd app && go test ./workflow/ -run TestWorkflowInterfaceCompiles -v && cd ../app && go test ./workflow/ -run TestNextStepKinds -v)`
Expected: PASS for both.

- [ ] **Step 5: Commit**

```bash
git add app/workflow/workflow.go app/workflow/workflow_test.go
git commit -m "feat(workflow): add Workflow interface + Pending / NextStep types"
```

### Task 1.2: Create registry

**Files:**
- Create: `app/workflow/registry.go`
- Create: `app/workflow/registry_test.go`

- [ ] **Step 1: Write the failing test**

`app/workflow/registry_test.go`:

```go
package workflow

import "testing"

func TestRegistry_RegisterAndGet(t *testing.T) {
	r := NewRegistry()
	wf := &fakeWorkflow{typ: "issue"}
	r.Register(wf)

	got, ok := r.Get("issue")
	if !ok {
		t.Fatal("Get(issue) not found")
	}
	if got.Type() != "issue" {
		t.Errorf("got %q", got.Type())
	}
}

func TestRegistry_GetUnknown(t *testing.T) {
	r := NewRegistry()
	if _, ok := r.Get("unknown"); ok {
		t.Error("unknown task type should not be found")
	}
	if _, ok := r.Get(""); ok {
		t.Error("empty task type should not be found")
	}
}

func TestRegistry_RegisterEmptyTypePanic(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on empty Type()")
		}
	}()
	r := NewRegistry()
	r.Register(&fakeWorkflow{typ: ""})
}

func TestRegistry_RegisterDuplicatePanic(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on duplicate Type()")
		}
	}()
	r := NewRegistry()
	r.Register(&fakeWorkflow{typ: "issue"})
	r.Register(&fakeWorkflow{typ: "issue"})
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `(cd app && go test ./workflow/ -run TestRegistry)`
Expected: FAIL — NewRegistry / Registry undefined.

- [ ] **Step 3: Write implementation**

`app/workflow/registry.go`:

```go
package workflow

import "fmt"

// Registry maps Job.TaskType → Workflow. It is populated at app startup and
// read by the dispatcher and ResultListener. Registration failures (empty or
// duplicate Type) panic at startup so misconfiguration is caught immediately.
type Registry struct {
	workflows map[string]Workflow
}

// NewRegistry returns an empty registry. Callers add workflows via Register.
func NewRegistry() *Registry {
	return &Registry{workflows: make(map[string]Workflow)}
}

// Register adds a workflow. Panics if the workflow reports an empty Type()
// or if a workflow with the same Type() is already registered.
func (r *Registry) Register(w Workflow) {
	t := w.Type()
	if t == "" {
		panic("workflow: Register called with empty Type()")
	}
	if _, exists := r.workflows[t]; exists {
		panic(fmt.Sprintf("workflow: duplicate registration for %q", t))
	}
	r.workflows[t] = w
}

// Get returns the workflow for the given task type. Callers that receive
// ok==false should surface "unknown task type" to the user — this is the
// natural enforcement point for the spec's "app-side dispatch" contract.
func (r *Registry) Get(taskType string) (Workflow, bool) {
	w, ok := r.workflows[taskType]
	return w, ok
}

// Types returns the registered workflow types in no particular order. Used
// for logging and by the D-selector to build its button list dynamically.
func (r *Registry) Types() []string {
	out := make([]string, 0, len(r.workflows))
	for t := range r.workflows {
		out = append(out, t)
	}
	return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `(cd app && go test ./workflow/ -run TestRegistry -v)`
Expected: PASS for all four tests.

- [ ] **Step 5: Commit**

```bash
git add app/workflow/registry.go app/workflow/registry_test.go
git commit -m "feat(workflow): add Registry with panic-on-misconfig guards"
```

### Task 1.3: Create ports (SlackPort / IssueCreator / GitHubPR)

**Files:**
- Create: `app/workflow/ports.go`

- [ ] **Step 1: Write ports**

`app/workflow/ports.go`:

```go
package workflow

import (
	"context"

	slackclient "github.com/Ivantseng123/agentdock/app/slack"
)

// SlackPort is the narrow Slack surface each workflow + the dispatcher need.
// Mirrors the app/bot.slackAPI surface but is owned here so the workflow
// package does not import app/bot.
type SlackPort interface {
	PostMessage(channelID, text, threadTS string) error
	PostMessageWithTS(channelID, text, threadTS string) (string, error)
	PostMessageWithButton(channelID, text, threadTS, actionID, buttonText, value string) (string, error)
	UpdateMessage(channelID, messageTS, text string) error
	UpdateMessageWithButton(channelID, messageTS, text, actionID, buttonText, value string) error
	PostSelector(channelID, prompt, actionPrefix string, options []string, threadTS string) (string, error)
	PostSelectorWithBack(channelID, prompt, actionPrefix string, options []string, threadTS, backActionID, backLabel string) (string, error)
	PostExternalSelector(channelID, prompt, actionID, placeholder, threadTS string) (string, error)
	OpenTextInputModal(triggerID, title, label, inputName, metadata string) error
	ResolveUser(userID string) string
	GetChannelName(channelID string) string
	FetchThreadContext(channelID, threadTS, triggerTS, botUserID string, limit int) ([]slackclient.ThreadRawMessage, error)
	DownloadAttachments(messages []slackclient.ThreadRawMessage, tempDir string) []slackclient.AttachmentDownload
}

// IssueCreator abstracts GitHub issue creation. Only IssueWorkflow consumes
// this; the interface lives in the workflow package because that is where
// its single consumer lives.
type IssueCreator interface {
	CreateIssue(ctx context.Context, owner, repo, title, body string, labels []string) (string, error)
}

// GitHubPR abstracts the PR endpoints PR Review needs for URL validation.
// PRReviewWorkflow uses this to verify a URL references a real, accessible PR
// before submitting work.
type GitHubPR interface {
	GetPullRequest(ctx context.Context, owner, repo string, number int) (*PullRequest, error)
}

// PullRequest is the subset of the GitHub PR payload we care about. Field
// names match the GitHub REST response so shared/github can populate this
// from its httpGet directly.
type PullRequest struct {
	Number int    `json:"number"`
	State  string `json:"state"` // "open" / "closed"
	Draft  bool   `json:"draft"`
	Merged bool   `json:"merged"`
	Title  string `json:"title"`
	HTMLURL string `json:"html_url"`
	Head   struct {
		Ref  string `json:"ref"`
		SHA  string `json:"sha"`
		Repo struct {
			FullName string `json:"full_name"`
			CloneURL string `json:"clone_url"`
		} `json:"repo"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}
```

- [ ] **Step 2: Verify build**

Run: `(cd app && go build ./workflow/)`
Expected: success (no tests yet; types must compile).

- [ ] **Step 3: Commit**

```bash
git add app/workflow/ports.go
git commit -m "feat(workflow): define SlackPort / IssueCreator / GitHubPR interfaces"
```

### Task 1.4: Create dispatcher shell + verb parser

**Files:**
- Create: `app/workflow/dispatcher.go`
- Create: `app/workflow/dispatcher_test.go`

- [ ] **Step 1: Write the failing tests (verb parser only for this task)**

`app/workflow/dispatcher_test.go`:

```go
package workflow

import "testing"

func TestParseTrigger(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		wantVerb  string
		wantArgs  string
		wantKnown bool
	}{
		{"empty", "", "", "", false},
		{"verb only ask", "ask", "ask", "", true},
		{"verb + args", "ask what does X do?", "ask", "what does X do?", true},
		{"case-insensitive ASK", "ASK question", "ask", "question", true},
		{"case-insensitive Ask", "Ask Q", "ask", "Q", true},
		{"review with url", "review https://github.com/foo/bar/pull/123", "review", "https://github.com/foo/bar/pull/123", true},
		{"review with slack-wrapped url", "review <https://github.com/foo/bar/pull/123>", "review", "https://github.com/foo/bar/pull/123", true},
		{"issue verb", "issue foo/bar", "issue", "foo/bar", true},
		{"no verb repo-shaped", "foo/bar", "", "foo/bar", false},
		{"no verb repo@branch", "foo/bar@dev", "", "foo/bar@dev", false},
		{"unknown verb treated as unknown", "askme please", "askme", "please", false},
		{"slack mention tag stripped", "<@U123> ask Q", "ask", "Q", true},
		{"trailing whitespace", "ask Q  ", "ask", "Q", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseTrigger(tc.text)
			if got.Verb != tc.wantVerb {
				t.Errorf("Verb = %q, want %q", got.Verb, tc.wantVerb)
			}
			if got.Args != tc.wantArgs {
				t.Errorf("Args = %q, want %q", got.Args, tc.wantArgs)
			}
			if got.KnownVerb != tc.wantKnown {
				t.Errorf("KnownVerb = %v, want %v", got.KnownVerb, tc.wantKnown)
			}
		})
	}
}

func TestLooksLikeRepo(t *testing.T) {
	cases := map[string]bool{
		"":              false,
		"foo/bar":       true,
		"foo/bar@main":  true,
		"foo":           false,
		"foo bar":       false,
		"https://x/y":   false, // multi-slash (colon in scheme also disqualifies)
		"a/b/c":         false, // more than one "/"
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			if got := LooksLikeRepo(in); got != want {
				t.Errorf("LooksLikeRepo(%q) = %v, want %v", in, got, want)
			}
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `(cd app && go test ./workflow/ -run TestParseTrigger -run TestLooksLikeRepo)`
Expected: FAIL — ParseTrigger / LooksLikeRepo undefined.

- [ ] **Step 3: Write dispatcher shell (implementation for ParseTrigger + LooksLikeRepo, no Dispatch yet)**

`app/workflow/dispatcher.go`:

```go
package workflow

import (
	"log/slog"
	"strings"
)

// KnownVerbs enumerates verbs recognised by the dispatcher. Adding a verb
// here is not enough — the corresponding workflow must also be registered.
var KnownVerbs = []string{"issue", "ask", "review"}

// TriggerParse is the result of running ParseTrigger on an @bot mention.
// Verb is always lowercase for case-insensitive matching.
type TriggerParse struct {
	Verb      string // lowercase; "" if no verb (legacy bare-repo or empty)
	Args      string // remainder after verb + whitespace; unwrapped from Slack <...>
	KnownVerb bool   // true iff Verb is in KnownVerbs
}

// ParseTrigger extracts the verb and args from a mention's raw text. It:
//   - Strips the leading Slack mention tag (<@U...>)
//   - Strips the legacy /triage prefix
//   - Lowercases the first token to match verbs case-insensitively
//   - Strips Slack URL auto-wrapping (<...>) from the remaining args
//   - Sets KnownVerb iff the verb matches one of KnownVerbs
func ParseTrigger(text string) TriggerParse {
	// Strip Slack mention tag <@U...>
	text = strings.TrimSpace(text)
	if idx := strings.Index(text, ">"); idx != -1 && strings.HasPrefix(text, "<@") {
		text = strings.TrimSpace(text[idx+1:])
	}
	// Strip legacy /triage prefix
	text = strings.TrimSpace(strings.TrimPrefix(text, "/triage"))

	if text == "" {
		return TriggerParse{}
	}

	// Split into first token + rest
	var first, rest string
	if sp := strings.IndexAny(text, " \t"); sp >= 0 {
		first = text[:sp]
		rest = strings.TrimSpace(text[sp+1:])
	} else {
		first = text
		rest = ""
	}

	verb := strings.ToLower(first)
	rest = stripSlackURLWrap(rest)

	for _, kv := range KnownVerbs {
		if verb == kv {
			return TriggerParse{Verb: verb, Args: rest, KnownVerb: true}
		}
	}

	// Unknown first token. Decide whether it should be treated as legacy
	// bare-repo ("foo/bar") or as an unknown verb.
	if LooksLikeRepo(first) {
		// Bare repo — empty verb, whole text as args.
		return TriggerParse{Verb: "", Args: stripSlackURLWrap(text), KnownVerb: false}
	}
	// Unknown verb — surface the typed verb so dispatcher can tell the user.
	return TriggerParse{Verb: verb, Args: rest, KnownVerb: false}
}

// LooksLikeRepo returns true iff s matches owner/repo or owner/repo@branch.
// Used by the dispatcher to keep `@bot foo/bar` routing to Issue (legacy).
func LooksLikeRepo(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	// Reject anything that looks like a URL.
	if strings.Contains(s, "://") {
		return false
	}
	// Split off optional @branch.
	if at := strings.IndexByte(s, '@'); at >= 0 {
		s = s[:at]
	}
	// Must contain exactly one "/"
	return strings.Count(s, "/") == 1 && !strings.HasPrefix(s, "/") && !strings.HasSuffix(s, "/")
}

func stripSlackURLWrap(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '<' && s[len(s)-1] == '>' {
		inner := s[1 : len(s)-1]
		// Slack wraps URLs and sometimes appends "|display" — drop that part.
		if pipe := strings.IndexByte(inner, '|'); pipe >= 0 {
			inner = inner[:pipe]
		}
		if strings.HasPrefix(inner, "http://") || strings.HasPrefix(inner, "https://") {
			return inner
		}
	}
	return s
}

// Dispatcher routes parsed triggers to the right Workflow via the Registry.
// Constructed once at app startup; safe to call Dispatch concurrently.
type Dispatcher struct {
	registry *Registry
	slack    SlackPort
	logger   *slog.Logger
}

// NewDispatcher wires a dispatcher around a populated registry and a
// SlackPort. Panics if registry is nil.
func NewDispatcher(reg *Registry, slack SlackPort, logger *slog.Logger) *Dispatcher {
	if reg == nil {
		panic("workflow: NewDispatcher called with nil registry")
	}
	return &Dispatcher{registry: reg, slack: slack, logger: logger}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `(cd app && go test ./workflow/ -run TestParseTrigger -run TestLooksLikeRepo -v)`
Expected: PASS for every sub-test.

- [ ] **Step 5: Commit**

```bash
git add app/workflow/dispatcher.go app/workflow/dispatcher_test.go
git commit -m "feat(workflow): add ParseTrigger + LooksLikeRepo + Dispatcher shell"
```

### Task 1.5: Phase 1 build gate

**Files:**
- (no file changes; verification only)

- [ ] **Step 1: Run entire app module tests**

Run: `(cd app && go test ./... -race)`
Expected: PASS (workflow package has tests, nothing else changed).

- [ ] **Step 2: Run module-boundary check**

Run: `go test ./test/ -run TestImportDirection -v`
Expected: PASS — no new boundary violations.

- [ ] **Step 3: Commit a phase tag (empty commit)**

```bash
git commit --allow-empty -m "chore: phase 1 skeleton complete — app/workflow/ package compiles"
```

---

## Phase 2 — `IssueWorkflow` refactor + nested config

Relocate existing Issue logic into `app/workflow/issue.go` behind the `Workflow` interface. Introduce nested `prompt.{issue,ask,pr_review}.*` config with the legacy flat `prompt.goal` / `prompt.output_rules` aliased to `prompt.issue.*`. Behaviour is preserved end-to-end for Issue flow — user sees no difference.

### Task 2.1: Relocate triage parser

**Files:**
- Create: `app/workflow/issue_parser.go`
- Create: `app/workflow/issue_parser_test.go`
- Delete: `app/bot/parser.go`
- Delete: `app/bot/parser_test.go`

- [ ] **Step 1: Copy parser to new location under `workflow` package**

Move the contents of `app/bot/parser.go` into `app/workflow/issue_parser.go`, changing only the package declaration from `package bot` to `package workflow`. All types (`TriageResult`, `Labels`) and functions (`ParseAgentOutput`, `extractJSON`, `extractIssueURL`) keep their exported names — they become `workflow.TriageResult`, `workflow.ParseAgentOutput`, etc.

- [ ] **Step 2: Copy parser tests to new location**

Move the contents of `app/bot/parser_test.go` into `app/workflow/issue_parser_test.go`, changing only the package declaration from `package bot` to `package workflow`.

- [ ] **Step 3: Delete old files**

```bash
rm app/bot/parser.go app/bot/parser_test.go
```

- [ ] **Step 4: Update callers in `app/bot/result_listener.go`**

Replace every `ParseAgentOutput` / `TriageResult` reference with `workflow.ParseAgentOutput` / `workflow.TriageResult`. Add import:

```go
import (
	// ... existing imports ...
	"github.com/Ivantseng123/agentdock/app/workflow"
)
```

- [ ] **Step 5: Verify**

Run: `(cd app && go build ./... && go test ./workflow/ -run TestParseAgentOutput -v && go test ./bot/ -run Result)`
Expected: PASS. Parser tests must pass in their new location; result listener tests must still work.

- [ ] **Step 6: Commit**

```bash
git add app/workflow/issue_parser.go app/workflow/issue_parser_test.go app/bot/parser.go app/bot/parser_test.go app/bot/result_listener.go
git commit -m "refactor(workflow): relocate triage parser to app/workflow package"
```

### Task 2.2: Nested `PromptConfig` + legacy alias

**Files:**
- Modify: `app/config/config.go`
- Modify: `app/config/defaults.go`
- Modify: `app/config/load.go`
- Modify: `app/config/config_test.go`

- [ ] **Step 1: Write failing tests for alias + defaults**

`app/config/config_test.go` (add these tests):

```go
func TestPromptConfig_LegacyFlatAliasedToIssue(t *testing.T) {
	yaml := `
prompt:
  language: "zh-TW"
  goal: "legacy flat goal"
  output_rules:
    - "legacy rule"
`
	cfg, err := LoadFromBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Prompt.Issue.Goal != "legacy flat goal" {
		t.Errorf("Issue.Goal = %q, want legacy flat alias", cfg.Prompt.Issue.Goal)
	}
	if len(cfg.Prompt.Issue.OutputRules) != 1 || cfg.Prompt.Issue.OutputRules[0] != "legacy rule" {
		t.Errorf("Issue.OutputRules = %v", cfg.Prompt.Issue.OutputRules)
	}
}

func TestPromptConfig_NestedOverridesFlat(t *testing.T) {
	yaml := `
prompt:
  goal: "legacy"
  issue:
    goal: "nested issue goal"
`
	cfg, err := LoadFromBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
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

func TestPRReviewConfig_DefaultDisabled(t *testing.T) {
	cfg := &Config{}
	ApplyDefaults(cfg)
	if cfg.PRReview.Enabled {
		t.Error("PRReview.Enabled default should be false (opt-in)")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `(cd app && go test ./config/ -run 'PromptConfig|PRReviewConfig')`
Expected: FAIL — struct fields `Issue`, `Ask`, `PRReview` don't exist yet.

- [ ] **Step 3: Update `PromptConfig` struct**

`app/config/config.go` — replace the existing `PromptConfig` + add `PRReviewConfig`:

```go
// PromptConfig nests per-workflow goal / output rules. Legacy flat
// Goal / OutputRules are aliased at load time into Issue.* so pre-v2.2
// operators keep working.
type PromptConfig struct {
	Language         string                  `yaml:"language"`
	AllowWorkerRules *bool                   `yaml:"allow_worker_rules"`

	// Legacy flat fields — at load time, these are copied into Issue.* if
	// Issue.* is unset. Operators may remove these from their yaml once they
	// migrate to the nested form.
	Goal        string   `yaml:"goal,omitempty"`
	OutputRules []string `yaml:"output_rules,omitempty"`

	// Per-workflow sections.
	Issue    WorkflowPromptConfig `yaml:"issue"`
	Ask      WorkflowPromptConfig `yaml:"ask"`
	PRReview WorkflowPromptConfig `yaml:"pr_review"`
}

// WorkflowPromptConfig holds one workflow's prompt knobs. All fields are
// optional at the yaml layer; ApplyDefaults fills gaps with hardcoded
// defaults so zero-config is valid.
type WorkflowPromptConfig struct {
	Goal        string   `yaml:"goal"`
	OutputRules []string `yaml:"output_rules"`
}

// IsWorkerRulesAllowed returns whether worker-side ExtraRules should be
// rendered into the prompt. Nil pointer is treated as true (default).
func (p PromptConfig) IsWorkerRulesAllowed() bool {
	return p.AllowWorkerRules == nil || *p.AllowWorkerRules
}

// PRReviewConfig gates the PR Review workflow. Disabled by default so
// operators opt in after verifying the github-pr-review skill and its
// agentdock pr-review-helper subcommand are available on their workers.
type PRReviewConfig struct {
	Enabled bool `yaml:"enabled"`
}
```

And add `PRReview PRReviewConfig` to the outer `Config` struct (wherever other top-level config fields live):

```go
type Config struct {
	// ... existing fields ...
	Prompt   PromptConfig   `yaml:"prompt"`
	PRReview PRReviewConfig `yaml:"pr_review"`
	// ... existing fields ...
}
```

- [ ] **Step 4: Add defaults**

`app/config/defaults.go` — inside `ApplyDefaults`, after the existing `p.Goal` / `p.OutputRules` handling, add:

```go
// Hardcoded per-workflow defaults. Operator yaml wins over these.
const (
	defaultIssueGoal   = "Use the /triage-issue skill to investigate and produce a triage result."
	defaultAskGoal     = "Answer the user's question using the thread, and (if a codebase is attached) the repo. Output ===ASK_RESULT=== followed by JSON {\"answer\": \"<markdown>\"}."
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
	// it; the current spec's hardcoded Issue rules travel in app/workflow/issue.go
	// as the spec language, not as defaults here.
}
```

Call `applyPromptDefaults(&cfg.Prompt)` from the existing `ApplyDefaults(cfg *Config)` function, after whatever prompt-related handling is already there. Remove any now-obsolete assignments to the removed `defaultPromptGoal` string.

- [ ] **Step 5: Run tests to verify they pass**

Run: `(cd app && go test ./config/ -run 'PromptConfig|PRReviewConfig' -v)`
Expected: PASS for all four new tests.

- [ ] **Step 6: Run full config test suite**

Run: `(cd app && go test ./config/ -race)`
Expected: PASS — existing tests must still work. If any break because of the Goal / OutputRules field shape, update them to use the nested form.

- [ ] **Step 7: Commit**

```bash
git add app/config/config.go app/config/defaults.go app/config/load.go app/config/config_test.go
git commit -m "feat(config): nested prompt.{issue,ask,pr_review} with legacy alias"
```

### Task 2.3: `IssueWorkflow` skeleton (Type + wizard state struct)

**Files:**
- Create: `app/workflow/issue.go`
- Create: `app/workflow/issue_test.go`

- [ ] **Step 1: Write the failing test**

`app/workflow/issue_test.go`:

```go
package workflow

import (
	"context"
	"testing"
)

func TestIssueWorkflow_Type(t *testing.T) {
	w := &IssueWorkflow{}
	if w.Type() != "issue" {
		t.Errorf("Type() = %q, want issue", w.Type())
	}
}

func TestIssueWorkflow_TriggerWithRepoArg_ShortCircuits(t *testing.T) {
	w := &IssueWorkflow{}
	ctx := context.Background()
	ev := TriggerEvent{ChannelID: "C1", ThreadTS: "1.0", UserID: "U1"}

	step, err := w.Trigger(ctx, ev, "foo/bar")
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	// With repo provided, wizard moves directly to branch/description; spec
	// §Issue wizard step sequence. For skeleton, we only assert it returns a
	// NextStep that is not NextStepError.
	if step.Kind == NextStepError {
		t.Errorf("expected non-error NextStep, got error: %q", step.ErrorText)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `(cd app && go test ./workflow/ -run TestIssueWorkflow)`
Expected: FAIL — `IssueWorkflow` undefined.

- [ ] **Step 3: Write skeleton**

`app/workflow/issue.go`:

```go
package workflow

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/Ivantseng123/agentdock/app/config"
	ghclient "github.com/Ivantseng123/agentdock/shared/github"
	"github.com/Ivantseng123/agentdock/shared/queue"
)

// IssueWorkflow handles the legacy `@bot <repo>` and `@bot issue <repo>` flow.
// Behaviour is preserved end-to-end from the pre-refactor `app/bot/workflow.go`
// implementation — users see no change.
type IssueWorkflow struct {
	cfg            *config.Config
	slack          SlackPort
	github         IssueCreator
	repoCache      *ghclient.RepoCache
	repoDiscovery  *ghclient.RepoDiscovery
	logger         *slog.Logger
}

// issueState is the workflow-specific Pending.State for IssueWorkflow.
type issueState struct {
	SelectedRepo   string
	SelectedBranch string
	ExtraDesc      string
	RepoWasPicked  bool
	CmdArgs        string
}

// NewIssueWorkflow constructs a workflow instance. All dependencies are
// required. Panics on nil pointers to fail fast at startup.
func NewIssueWorkflow(
	cfg *config.Config,
	slack SlackPort,
	github IssueCreator,
	repoCache *ghclient.RepoCache,
	repoDiscovery *ghclient.RepoDiscovery,
	logger *slog.Logger,
) *IssueWorkflow {
	if cfg == nil || slack == nil || logger == nil {
		panic("workflow: NewIssueWorkflow missing required dep")
	}
	return &IssueWorkflow{
		cfg:           cfg,
		slack:         slack,
		github:        github,
		repoCache:     repoCache,
		repoDiscovery: repoDiscovery,
		logger:        logger,
	}
}

// Type returns the TaskType discriminator.
func (w *IssueWorkflow) Type() string { return "issue" }

// Trigger is the entry point from the dispatcher for `@bot issue ...` and
// the legacy `@bot <repo>` paths. This skeleton returns an error step so
// the interface is satisfied; Task 2.4 fills in the real wizard logic.
func (w *IssueWorkflow) Trigger(ctx context.Context, ev TriggerEvent, args string) (NextStep, error) {
	// Filled in by Task 2.4 — for now, hand back a benign NextStep so
	// nothing panics at wiring time.
	return NextStep{
		Kind:    NextStepError,
		ErrorText: fmt.Sprintf("IssueWorkflow.Trigger not yet implemented (args=%q)", args),
	}, nil
}

// Selection handles follow-up button clicks.
func (w *IssueWorkflow) Selection(ctx context.Context, p *Pending, value string) (NextStep, error) {
	return NextStep{Kind: NextStepError, ErrorText: "IssueWorkflow.Selection not yet implemented"}, nil
}

// BuildJob assembles the queue.Job from the completed pending state.
func (w *IssueWorkflow) BuildJob(ctx context.Context, p *Pending) (*queue.Job, string, error) {
	return nil, "", fmt.Errorf("IssueWorkflow.BuildJob not yet implemented")
}

// HandleResult parses the agent's ===TRIAGE_RESULT=== output and posts back
// to Slack / creates the GitHub issue.
func (w *IssueWorkflow) HandleResult(ctx context.Context, job *queue.Job, r *queue.JobResult) error {
	return fmt.Errorf("IssueWorkflow.HandleResult not yet implemented")
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `(cd app && go test ./workflow/ -run TestIssueWorkflow -v)`
Expected: PASS for both tests (Trigger returns an error-kind step but the test only checks non-error when args is a repo; adjust the skeleton if needed to satisfy both tests — for v0, make the empty-args path return `NextStepError` and the repo-arg path return `NextStepSubmit` with a fake pending just to satisfy `TriggerWithRepoArg_ShortCircuits`).

Patch inside `Trigger` to satisfy the test:

```go
func (w *IssueWorkflow) Trigger(ctx context.Context, ev TriggerEvent, args string) (NextStep, error) {
	if args != "" {
		// Real implementation lands in Task 2.4; for skeleton, acknowledge
		// the arg path so tests compile.
		return NextStep{Kind: NextStepSubmit, Pending: &Pending{
			ChannelID: ev.ChannelID, ThreadTS: ev.ThreadTS, UserID: ev.UserID,
			TaskType: "issue", State: &issueState{CmdArgs: args},
		}}, nil
	}
	return NextStep{Kind: NextStepError, ErrorText: "IssueWorkflow.Trigger not yet implemented"}, nil
}
```

Re-run tests to confirm PASS.

- [ ] **Step 5: Commit**

```bash
git add app/workflow/issue.go app/workflow/issue_test.go
git commit -m "feat(workflow): IssueWorkflow skeleton satisfying Workflow interface"
```

### Task 2.4: Port the Issue wizard (Trigger / Selection / BuildJob)

**Files:**
- Modify: `app/workflow/issue.go`
- Modify: `app/workflow/issue_test.go`

Port the existing wizard from `app/bot/workflow.go` — `HandleTrigger`, `postRepoSelector`, `HandleRepoSuggestion`, `HandleSelection` (for phases `repo`, `repo_search`, `branch`), `afterRepoSelected`, `showDescriptionPrompt`, `HandleDescriptionAction`, `HandleDescriptionSubmit`, `HandleBackToRepo`, and `runTriage` — into the methods of `IssueWorkflow`. The mapping is:

| Old method on `Workflow` | New method on `IssueWorkflow` |
|---|---|
| `HandleTrigger(event)` | `Trigger(ctx, ev, args)` — returns NextStep instead of calling `PostSelector` directly |
| `postRepoSelector(pt, channelCfg)` | helper `w.postRepoSelector(p *Pending) NextStep` |
| `HandleSelection(..., "repo" / "repo_search")` | `Selection` branch on `p.Phase == "repo" / "repo_search"` |
| `HandleSelection(..., "branch")` | `Selection` branch on `p.Phase == "branch"` |
| `afterRepoSelected` | helper on `IssueWorkflow` |
| `showDescriptionPrompt` | returns `NextStep{Kind: NextStepPostSelector}` |
| `HandleDescriptionAction` | `Selection` branch on `p.Phase == "description"` |
| `HandleDescriptionSubmit` | `Selection` branch on `p.Phase == "description_modal"` |
| `HandleBackToRepo` | `Selection` branch on `value == "back_to_repo"` |
| `runTriage` / `runTriage` submit path | `BuildJob` |

- [ ] **Step 1: Write tests for each branch of Trigger / Selection / BuildJob**

`app/workflow/issue_test.go` (extend):

```go
func TestIssueWorkflow_Trigger_NoRepoSingleConfigured(t *testing.T) {
	// Single-repo channel config: Trigger returns submit or description prompt,
	// not a selector (short-circuit). Assert Kind is not NextStepPostSelector.
	w, _ := newTestIssueWorkflow(t, withChannelRepos([]string{"foo/bar"}))
	ev := TriggerEvent{ChannelID: "C1", ThreadTS: "1.0", UserID: "U1"}

	step, err := w.Trigger(context.Background(), ev, "")
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if step.Kind == NextStepPostSelector {
		t.Error("single-repo channel should skip repo selector")
	}
}

func TestIssueWorkflow_Trigger_MultiRepoShowsSelector(t *testing.T) {
	w, _ := newTestIssueWorkflow(t, withChannelRepos([]string{"foo/bar", "baz/qux"}))
	ev := TriggerEvent{ChannelID: "C1", ThreadTS: "1.0", UserID: "U1"}

	step, err := w.Trigger(context.Background(), ev, "")
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if step.Kind != NextStepPostSelector {
		t.Errorf("expected NextStepPostSelector, got %v", step.Kind)
	}
	if len(step.SelectorActions) != 2 {
		t.Errorf("expected 2 selector options, got %d", len(step.SelectorActions))
	}
}

func TestIssueWorkflow_Selection_RepoPhase_TransitionsToBranchOrDescription(t *testing.T) {
	// After picking a repo, workflow transitions to branch selector (if
	// multi-branch) or description prompt (if single/no branch list).
	w, _ := newTestIssueWorkflow(t)
	p := &Pending{Phase: "repo", State: &issueState{}, ChannelID: "C1", ThreadTS: "1.0"}

	step, err := w.Selection(context.Background(), p, "foo/bar")
	if err != nil {
		t.Fatalf("Selection: %v", err)
	}
	if step.Kind == NextStepError {
		t.Errorf("unexpected error: %q", step.ErrorText)
	}
}

func TestIssueWorkflow_BuildJob_SetsTaskType(t *testing.T) {
	w, _ := newTestIssueWorkflow(t)
	p := &Pending{
		ChannelID: "C1", ThreadTS: "1.0", UserID: "U1",
		State: &issueState{SelectedRepo: "foo/bar", SelectedBranch: "main"},
	}

	job, status, err := w.BuildJob(context.Background(), p)
	if err != nil {
		t.Fatalf("BuildJob: %v", err)
	}
	if job.TaskType != "issue" {
		t.Errorf("TaskType = %q, want issue", job.TaskType)
	}
	if job.Repo != "foo/bar" {
		t.Errorf("Repo = %q", job.Repo)
	}
	if job.Branch != "main" {
		t.Errorf("Branch = %q", job.Branch)
	}
	if job.PromptContext == nil || job.PromptContext.Goal == "" {
		t.Error("PromptContext.Goal must be populated (from config or default)")
	}
	if status == "" {
		t.Error("status text should be non-empty; spec says :mag: 分析 codebase 中...")
	}
}
```

Add a helper in `issue_test.go` that returns a minimally-wired `*IssueWorkflow` for tests:

```go
type issueOpt func(*config.Config)

func withChannelRepos(repos []string) issueOpt {
	return func(c *config.Config) {
		c.ChannelDefaults.Repos = repos
	}
}

func newTestIssueWorkflow(t *testing.T, opts ...issueOpt) (*IssueWorkflow, *fakeSlackPort) {
	t.Helper()
	cfg := &config.Config{}
	config.ApplyDefaults(cfg) // populates Prompt.Issue defaults
	for _, o := range opts {
		o(cfg)
	}
	slack := newFakeSlackPort()
	w := NewIssueWorkflow(cfg, slack, &fakeIssueCreator{}, nil, nil, slog.Default())
	return w, slack
}
```

Where `fakeSlackPort` + `fakeIssueCreator` are shared test helpers — put them in a new `app/workflow/ports_test.go` file:

```go
package workflow

import (
	"context"
	"fmt"

	slackclient "github.com/Ivantseng123/agentdock/app/slack"
)

type fakeSlackPort struct {
	Posted    []string
	Selectors []string
	Modal     bool
}

func newFakeSlackPort() *fakeSlackPort { return &fakeSlackPort{} }

func (f *fakeSlackPort) PostMessage(ch, text, ts string) error { f.Posted = append(f.Posted, text); return nil }
func (f *fakeSlackPort) PostMessageWithTS(ch, text, ts string) (string, error) { f.Posted = append(f.Posted, text); return "ts", nil }
func (f *fakeSlackPort) PostMessageWithButton(ch, text, ts, aid, bt, val string) (string, error) { f.Posted = append(f.Posted, text); return "ts", nil }
func (f *fakeSlackPort) UpdateMessage(ch, mts, text string) error { f.Posted = append(f.Posted, text); return nil }
func (f *fakeSlackPort) UpdateMessageWithButton(ch, mts, text, aid, bt, val string) error { f.Posted = append(f.Posted, text); return nil }
func (f *fakeSlackPort) PostSelector(ch, prompt, prefix string, opts []string, ts string) (string, error) {
	f.Selectors = append(f.Selectors, prompt); return "sel-ts", nil
}
func (f *fakeSlackPort) PostSelectorWithBack(ch, prompt, prefix string, opts []string, ts, back, bl string) (string, error) {
	f.Selectors = append(f.Selectors, prompt); return "sel-ts", nil
}
func (f *fakeSlackPort) PostExternalSelector(ch, prompt, aid, ph, ts string) (string, error) {
	f.Selectors = append(f.Selectors, prompt); return "sel-ts", nil
}
func (f *fakeSlackPort) OpenTextInputModal(tid, title, label, name, metadata string) error { f.Modal = true; return nil }
func (f *fakeSlackPort) ResolveUser(uid string) string { return "user-" + uid }
func (f *fakeSlackPort) GetChannelName(cid string) string { return "ch-" + cid }
func (f *fakeSlackPort) FetchThreadContext(c, ts, tts, bot string, lim int) ([]slackclient.ThreadRawMessage, error) { return nil, nil }
func (f *fakeSlackPort) DownloadAttachments(msgs []slackclient.ThreadRawMessage, dir string) []slackclient.AttachmentDownload { return nil }

type fakeIssueCreator struct {
	URL     string
	LastArg string
}

func (f *fakeIssueCreator) CreateIssue(ctx context.Context, owner, repo, title, body string, labels []string) (string, error) {
	f.LastArg = fmt.Sprintf("%s/%s %s", owner, repo, title)
	if f.URL == "" {
		f.URL = fmt.Sprintf("https://github.com/%s/%s/issues/1", owner, repo)
	}
	return f.URL, nil
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `(cd app && go test ./workflow/ -run TestIssueWorkflow -v)`
Expected: FAIL for the new tests — wizard logic not yet ported.

- [ ] **Step 3: Port wizard logic**

In `app/workflow/issue.go`, replace the skeleton `Trigger`, `Selection`, `BuildJob` methods with implementations that mirror `app/bot/workflow.go`'s `HandleTrigger`, `HandleSelection`, `runTriage`. Key differences:

- Instead of calling `w.slack.PostSelector(...)` directly, return `NextStep{Kind: NextStepPostSelector, SelectorPrompt: "...", SelectorActions: [...], Pending: &Pending{...}}` — the dispatcher executes.
- Pending state goes into `Pending.State.(*issueState)`.
- `Pending.Phase` carries the wizard phase (`"repo"`, `"repo_search"`, `"branch"`, `"description"`, `"description_modal"`).
- `BuildJob` reads `PromptContext.Goal` / `.OutputRules` from `w.cfg.Prompt.Issue.Goal` / `.OutputRules`, falling back to nothing (defaults are populated by `ApplyDefaults`).
- Status text returned alongside the Job: `":mag: 分析 codebase 中..."`.

(The full ported implementation is approximately a 300-line port from `app/bot/workflow.go`; the mapping table at the top of this task is the guide. Keep method names + Chinese Slack text identical so user-visible behaviour is unchanged.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `(cd app && go test ./workflow/ -run TestIssueWorkflow -v)`
Expected: PASS for all IssueWorkflow tests.

- [ ] **Step 5: Commit**

```bash
git add app/workflow/issue.go app/workflow/issue_test.go app/workflow/ports_test.go
git commit -m "feat(workflow): port IssueWorkflow wizard from app/bot"
```

### Task 2.5: Port `HandleResult` for Issue (including retry button decisions)

**Files:**
- Modify: `app/workflow/issue.go`
- Modify: `app/workflow/issue_test.go`

Port `ResultListener.handleResult`'s Issue-specific code plus `handleFailure` / `createAndPostIssue` / `stripTriageSection` / `formatDiagnostics` / `shortWorkerID` / `humanDuration` into `IssueWorkflow.HandleResult`. The new method owns: parse `===TRIAGE_RESULT===` JSON, handle REJECTED / ERROR / CREATED branches, call `IssueCreator.CreateIssue`, post Slack URL, decide retry button.

- [ ] **Step 1: Write failing tests for each status branch**

Extend `app/workflow/issue_test.go`:

```go
func TestIssueWorkflow_HandleResult_Created_PostsIssueURL(t *testing.T) {
	w, slack := newTestIssueWorkflow(t)
	job := &queue.Job{ID: "j1", ChannelID: "C1", ThreadTS: "1.0", Repo: "foo/bar", StatusMsgTS: "s-ts", TaskType: "issue"}
	result := &queue.JobResult{
		JobID:  "j1",
		Status: "completed",
		RawOutput: `===TRIAGE_RESULT===
{"status":"CREATED","title":"T","body":"B","confidence":"high","files_found":3,"open_questions":0}`,
	}

	if err := w.HandleResult(context.Background(), job, result); err != nil {
		t.Fatalf("HandleResult: %v", err)
	}
	joined := strings.Join(slack.Posted, " | ")
	if !strings.Contains(joined, "Issue created") {
		t.Errorf("expected issue URL post, got: %v", slack.Posted)
	}
}

func TestIssueWorkflow_HandleResult_Rejected_PostsLowConfidence(t *testing.T) {
	w, slack := newTestIssueWorkflow(t)
	job := &queue.Job{ID: "j1", ChannelID: "C1", ThreadTS: "1.0", StatusMsgTS: "s-ts", TaskType: "issue"}
	result := &queue.JobResult{
		JobID:  "j1",
		Status: "completed",
		RawOutput: `===TRIAGE_RESULT===
{"status":"REJECTED","message":"not our repo"}`,
	}

	if err := w.HandleResult(context.Background(), job, result); err != nil {
		t.Fatalf("HandleResult: %v", err)
	}
	joined := strings.Join(slack.Posted, " | ")
	if !strings.Contains(joined, "判斷不屬於此 repo") {
		t.Errorf("expected low-confidence text, got: %v", slack.Posted)
	}
}

func TestIssueWorkflow_HandleResult_Failed_FirstAttempt_AttachesRetryButton(t *testing.T) {
	w, slack := newTestIssueWorkflow(t)
	job := &queue.Job{ID: "j1", ChannelID: "C1", ThreadTS: "1.0", Repo: "foo/bar", TaskType: "issue", RetryCount: 0}
	result := &queue.JobResult{JobID: "j1", Status: "failed", Error: "agent timeout"}

	if err := w.HandleResult(context.Background(), job, result); err != nil {
		t.Fatalf("HandleResult: %v", err)
	}
	// fakeSlackPort treats PostMessageWithButton like PostMessage and records
	// the text — assert the retry button text appears in what was posted.
	joined := strings.Join(slack.Posted, " | ")
	if !strings.Contains(joined, "分析失敗") {
		t.Errorf("expected failure text, got: %v", slack.Posted)
	}
}

func TestIssueWorkflow_HandleResult_Failed_Retried_NoButton(t *testing.T) {
	w, slack := newTestIssueWorkflow(t)
	job := &queue.Job{ID: "j1", ChannelID: "C1", ThreadTS: "1.0", TaskType: "issue", RetryCount: 1, StatusMsgTS: "s-ts"}
	result := &queue.JobResult{JobID: "j1", Status: "failed", Error: "agent timeout"}

	if err := w.HandleResult(context.Background(), job, result); err != nil {
		t.Fatalf("HandleResult: %v", err)
	}
	joined := strings.Join(slack.Posted, " | ")
	if !strings.Contains(joined, "重試後仍失敗") {
		t.Errorf("expected exhausted-retry text, got: %v", slack.Posted)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `(cd app && go test ./workflow/ -run TestIssueWorkflow_HandleResult)`
Expected: FAIL — HandleResult still returns "not yet implemented".

- [ ] **Step 3: Port `HandleResult` + helpers**

Copy the content of `ResultListener.handleResult`'s non-common path (starting from the `switch` that branches on REJECTED / ERROR / CREATED confidence) plus `handleFailure`, `createAndPostIssue`, `stripTriageSection`, `formatDiagnostics`, `shortWorkerID`, `humanDuration` into `app/workflow/issue.go`. Rename them to be private methods on `*IssueWorkflow`. Update `ParseAgentOutput` calls — it's now in this same package, so the call is bare `ParseAgentOutput(...)`.

Then implement the public method:

```go
func (w *IssueWorkflow) HandleResult(ctx context.Context, job *queue.Job, r *queue.JobResult) error {
	if r.Status == "failed" {
		w.handleFailure(job, r)
		return nil
	}

	if r.RawOutput != "" {
		parsed, err := ParseAgentOutput(r.RawOutput)
		if err != nil {
			truncated := r.RawOutput
			if len(truncated) > 2000 {
				truncated = truncated[:2000] + "…(truncated)"
			}
			w.logger.Warn("issue parse failed", "phase", "失敗", "output", truncated)
			r.Status = "failed"
			r.Error = fmt.Sprintf("parse failed: %v", err)
			w.handleFailure(job, r)
			return nil
		}
		switch parsed.Status {
		case "REJECTED":
			w.postLowConfidence(job, parsed.Message)
			return nil
		case "ERROR":
			msg := parsed.Message
			if msg == "" {
				msg = "agent reported ERROR without message"
			}
			r.Status = "failed"
			r.Error = "agent error: " + msg
			w.handleFailure(job, r)
			return nil
		case "CREATED":
			return w.createAndPostIssue(ctx, job, r, parsed)
		default:
			return fmt.Errorf("unknown parsed status %q", parsed.Status)
		}
	}
	return fmt.Errorf("empty RawOutput for completed job")
}
```

(Full private-method ports are a mechanical copy from `app/bot/result_listener.go` lines 177-465; rename receivers from `r *ResultListener` to `w *IssueWorkflow`, swap `r.slack` → `w.slack`, `r.github` → `w.github`, `r.store` → omit (we operate on the Job argument directly), `r.logger` → `w.logger`. Calls to `r.store.UpdateStatus` are removed from the workflow — ResultListener does that in Phase 3.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `(cd app && go test ./workflow/ -run TestIssueWorkflow_HandleResult -v)`
Expected: PASS for all four tests.

- [ ] **Step 5: Commit**

```bash
git add app/workflow/issue.go app/workflow/issue_test.go
git commit -m "feat(workflow): port IssueWorkflow.HandleResult with retry-button decisions"
```

### Task 2.6: Thin out `app/bot/workflow.go`

**Files:**
- Modify: `app/bot/workflow.go`
- Modify: `app/bot/workflow_test.go`

The old `bot.Workflow` becomes a thin Slack handler that keeps the pending-state map and forwards to the dispatcher.

- [ ] **Step 1: Replace `app/bot/workflow.go` logic with thin shim**

Keep in `app/bot/workflow.go`:
- `Workflow` struct but trim to: `dispatcher *workflow.Dispatcher`, pending-state map, Slack handler reference, `autoBound` set.
- `NewWorkflow` now takes a dispatcher instead of per-workflow deps.
- `HandleTrigger(event)` — parses the mention via `workflow.ParseTrigger` and calls `w.dispatcher.Dispatch(ctx, trigger, ev)`.
- `HandleSelection(channelID, actionID, value, selectorMsgTS)` — looks up the pending by `selectorMsgTS`, calls `w.dispatcher.HandleSelection(ctx, pending, value)`.
- `HandleDescriptionAction`, `HandleDescriptionSubmit`, `HandleRepoSuggestion`, `HandleBackToRepo` — all become thin forwarders; the real logic is now owned by `IssueWorkflow.Selection`.
- `storePending`, `clearDedup`, `notifyError`, `parseTriggerArgs`, `parseRepoArg` — delete (moved to dispatcher / workflow package).
- `slackAPI` interface — keep as-is so the constructor still has a narrow Slack surface for testing.

Full new file:

```go
package bot

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/Ivantseng123/agentdock/app/workflow"
	slackclient "github.com/Ivantseng123/agentdock/app/slack"
)

const pendingTimeout = 1 * time.Minute

type Workflow struct {
	dispatcher *workflow.Dispatcher
	slack      workflow.SlackPort
	handler    *slackclient.Handler
	logger     *slog.Logger

	mu        sync.Mutex
	pending   map[string]*workflow.Pending
	autoBound map[string]bool
}

func NewWorkflow(dispatcher *workflow.Dispatcher, slack workflow.SlackPort, logger *slog.Logger) *Workflow {
	return &Workflow{
		dispatcher: dispatcher,
		slack:      slack,
		logger:     logger,
		pending:    make(map[string]*workflow.Pending),
		autoBound:  make(map[string]bool),
	}
}

func (w *Workflow) SetHandler(h *slackclient.Handler) { w.handler = h }

func (w *Workflow) RegisterChannel(channelID string) {
	w.mu.Lock(); defer w.mu.Unlock()
	w.autoBound[channelID] = true
}

func (w *Workflow) UnregisterChannel(channelID string) {
	w.mu.Lock(); defer w.mu.Unlock()
	delete(w.autoBound, channelID)
}

// HandleTrigger parses the @bot mention and hands it to the dispatcher.
func (w *Workflow) HandleTrigger(event slackclient.TriggerEvent) {
	if event.ThreadTS == "" {
		w.slack.PostMessage(event.ChannelID, ":warning: 請在對話串中使用此指令。", "")
		return
	}

	ctx := context.Background()
	ev := workflow.TriggerEvent{
		ChannelID: event.ChannelID,
		ThreadTS:  event.ThreadTS,
		TriggerTS: event.TriggerTS,
		UserID:    event.UserID,
		Text:      event.Text,
	}
	pending, step, err := w.dispatcher.Dispatch(ctx, ev)
	if err != nil {
		w.logger.Error("dispatch failed", "phase", "失敗", "error", err)
		return
	}
	w.executeStep(ctx, pending, step)
}

// executeStep applies a NextStep from a workflow: post selector, open modal,
// submit job, or render an error message. Selector TSes are recorded in the
// pending map for later Selection lookups.
func (w *Workflow) executeStep(ctx context.Context, pending *workflow.Pending, step workflow.NextStep) {
	// Implementation: read step.Kind, call the appropriate SlackPort method,
	// and on Post/Modal kinds store pending[selectorTS] = pending.
	// See Task 2.7 for the dispatcher-owned variant; this app/bot shim only
	// needs to hand off to the dispatcher (which in Phase 7 posts directly).
	_ = ctx
	_ = pending
	_ = step
	// Left minimal here; the dispatcher wires Slack posting in Phase 7. For
	// Phase 2 we preserve the existing app/bot path: the dispatcher returns
	// the pending + step, and cmd/agentdock/app.go wires them.
}
```

This is the smallest possible shim. Most of the old wiring moves into the dispatcher in Task 2.7 and Phase 7.

- [ ] **Step 2: Delete obsolete bot workflow test content**

`app/bot/workflow_test.go` — drop tests whose behaviour is now covered by `app/workflow/*_test.go`. Keep only tests that exercise the shim's role (e.g. "HandleTrigger returns early when no ThreadTS"). If nothing remains, delete the file.

- [ ] **Step 3: Verify build**

Run: `(cd app && go build ./...)`
Expected: likely FAILS — `cmd/agentdock/app.go` constructs `bot.NewWorkflow` with the old signature. Task 2.7 updates the constructor call.

- [ ] **Step 4: Commit (partial — build broken until Task 2.7)**

```bash
git add app/bot/workflow.go app/bot/workflow_test.go
git commit -m "refactor(bot): thin Workflow shim that forwards to dispatcher"
```

### Task 2.7: Wire dispatcher in `cmd/agentdock/app.go`

**Files:**
- Modify: `cmd/agentdock/app.go`
- Modify: `app/workflow/dispatcher.go` (add `Dispatch` + `HandleSelection` methods)

- [ ] **Step 1: Add `Dispatch` + `HandleSelection` to the dispatcher**

`app/workflow/dispatcher.go` (append):

```go
// Dispatch parses the trigger event and routes it to the matching workflow.
// Unknown verbs / no-verb-no-args cases return a D-selector NextStep.
// Returns the initial Pending (the dispatcher will fill SelectorTS after the
// caller posts the selector/modal) and the NextStep to execute.
func (d *Dispatcher) Dispatch(ctx context.Context, ev TriggerEvent) (*Pending, NextStep, error) {
	tp := ParseTrigger(ev.Text)

	if tp.Verb == "" && tp.Args == "" {
		// Plain @bot with no args → D-selector.
		return d.postDSelector(ev, "")
	}
	if tp.KnownVerb {
		wf, ok := d.registry.Get(tp.Verb)
		if !ok {
			// Verb declared known but no workflow registered — registry
			// misconfiguration; fail loudly.
			return nil, NextStep{Kind: NextStepError, ErrorText: "workflow " + tp.Verb + " not registered"}, nil
		}
		step, err := wf.Trigger(ctx, ev, tp.Args)
		if err != nil {
			return nil, NextStep{Kind: NextStepError, ErrorText: err.Error()}, err
		}
		if step.Pending != nil {
			step.Pending.TaskType = wf.Type()
		}
		return step.Pending, step, nil
	}
	// Not a known verb. Either legacy bare-repo (LooksLikeRepo) → Issue,
	// or unknown verb → D-selector with warning.
	if tp.Verb == "" && LooksLikeRepo(tp.Args) {
		wf, _ := d.registry.Get("issue")
		if wf == nil {
			return nil, NextStep{Kind: NextStepError, ErrorText: "issue workflow not registered"}, nil
		}
		step, err := wf.Trigger(ctx, ev, tp.Args)
		if err != nil {
			return nil, NextStep{Kind: NextStepError, ErrorText: err.Error()}, err
		}
		if step.Pending != nil {
			step.Pending.TaskType = "issue"
		}
		return step.Pending, step, nil
	}

	// Unknown verb — D-selector with warning.
	warning := ""
	if tp.Verb != "" {
		warning = ":warning: 不認得 `" + tp.Verb + "`，請選一個："
	}
	return d.postDSelector(ev, warning)
}

// postDSelector returns a NextStep that renders the three-button selector.
// `warning` prepends a :warning: line (empty string = no warning).
func (d *Dispatcher) postDSelector(ev TriggerEvent, warning string) (*Pending, NextStep, error) {
	prompt := warning
	if prompt != "" {
		prompt += "\n"
	}
	prompt += ":point_right: 你想做什麼？"

	pending := &Pending{
		ChannelID: ev.ChannelID,
		ThreadTS:  ev.ThreadTS,
		TriggerTS: ev.TriggerTS,
		UserID:    ev.UserID,
		Phase:     "d_selector",
	}
	step := NextStep{
		Kind:           NextStepPostSelector,
		SelectorPrompt: prompt,
		SelectorActions: []SelectorAction{
			{ActionID: "d_selector", Label: "📝 建 Issue", Value: "issue"},
			{ActionID: "d_selector", Label: "❓ 問問題", Value: "ask"},
			{ActionID: "d_selector", Label: "🔍 Review PR", Value: "pr_review"},
		},
		Pending: pending,
	}
	return pending, step, nil
}

// HandleSelection routes a button-click or modal-submit to the owning workflow.
// For D-selector clicks, synthesises a fresh TriggerEvent and re-enters
// the workflow's Trigger.
func (d *Dispatcher) HandleSelection(ctx context.Context, p *Pending, value string) (NextStep, error) {
	if p.Phase == "d_selector" {
		// Value is one of the registered task types. Treat like a synthetic
		// @bot <verb> with no args.
		wf, ok := d.registry.Get(value)
		if !ok {
			return NextStep{Kind: NextStepError, ErrorText: "unknown workflow: " + value}, nil
		}
		ev := TriggerEvent{ChannelID: p.ChannelID, ThreadTS: p.ThreadTS, TriggerTS: p.TriggerTS, UserID: p.UserID}
		step, err := wf.Trigger(ctx, ev, "")
		if err != nil {
			return NextStep{Kind: NextStepError, ErrorText: err.Error()}, err
		}
		if step.Pending != nil {
			step.Pending.TaskType = wf.Type()
		}
		return step, nil
	}

	wf, ok := d.registry.Get(p.TaskType)
	if !ok {
		return NextStep{Kind: NextStepError, ErrorText: "unknown task_type: " + p.TaskType}, nil
	}
	return wf.Selection(ctx, p, value)
}
```

Add a dispatcher test:

```go
func TestDispatcher_UnknownVerb_YieldsDSelectorWithWarning(t *testing.T) {
	r := NewRegistry()
	r.Register(&fakeWorkflow{typ: "issue"})
	d := NewDispatcher(r, newFakeSlackPort(), slog.Default())
	_, step, err := d.Dispatch(context.Background(), TriggerEvent{Text: "askme something"})
	if err != nil {
		t.Fatal(err)
	}
	if step.Kind != NextStepPostSelector {
		t.Errorf("expected NextStepPostSelector, got %v", step.Kind)
	}
	if !strings.Contains(step.SelectorPrompt, "不認得") {
		t.Errorf("expected warning text, got %q", step.SelectorPrompt)
	}
}

func TestDispatcher_BareRepo_RoutesToIssue(t *testing.T) {
	r := NewRegistry()
	r.Register(&fakeWorkflow{typ: "issue"})
	d := NewDispatcher(r, newFakeSlackPort(), slog.Default())
	_, step, _ := d.Dispatch(context.Background(), TriggerEvent{Text: "foo/bar"})
	// fakeWorkflow.Trigger returns NextStep{Kind: NextStepSubmit}
	if step.Kind != NextStepSubmit {
		t.Errorf("expected NextStepSubmit (from fakeWorkflow), got %v", step.Kind)
	}
}
```

- [ ] **Step 2: Update `cmd/agentdock/app.go` wiring**

In the app bootstrap, construct the registry + dispatcher + workflows:

```go
// Build workflow registry and dispatcher.
reg := workflow.NewRegistry()
slackPort := slackAdapterPort{client: slackClient} // adapter — thin wrapper exposing app/slack Client as workflow.SlackPort
reg.Register(workflow.NewIssueWorkflow(cfg, slackPort, githubIssueClient, repoCache, repoDiscovery, logger))
// Ask + PR Review registered in later phases (5, 6).

dispatcher := workflow.NewDispatcher(reg, slackPort, logger)
workflowHandler := bot.NewWorkflow(dispatcher, slackPort, logger)
```

Update action-handler wiring so that on any button-click / modal-submit, the handler locates the `*Pending` from its selector-TS map and calls `dispatcher.HandleSelection(...)`. The action handler then executes the returned `NextStep` (post selector → `slackPort.PostSelector`, open modal → `slackPort.OpenTextInputModal`, submit → `workflowHandler.submitJob(pending)` using the shared queue plumbing that used to live in `runTriage`).

The `slackAdapterPort` is a 20-line adapter file wrapping the existing `*slack.Client` to satisfy `workflow.SlackPort`. If `*slack.Client` already has the signature for `OpenTextInputModal` after Phase 6, the adapter simply passes through; until then, `OpenTextInputModal` on the adapter is a stub that returns `errors.New("not implemented until Phase 6")`.

- [ ] **Step 3: Move `runTriage`'s queue-submission logic into a shared helper**

`cmd/agentdock/app.go` gains a small helper that takes a `*workflow.Pending`, a `*queue.Job`, and a `statusText string` and does the existing: post status message → submit to queue → prepare attachments. This is exactly what the old `Workflow.runTriage` did from line 387 to the end; extract it verbatim but accept the job from the caller instead of building it in-place.

- [ ] **Step 4: Verify build**

Run: `go build ./...`
Expected: PASS — all wiring resolved.

- [ ] **Step 5: Run full test suite**

Run: `(cd app && go test ./... -race) && (cd shared && go test ./... -race) && go test ./test/ -race`
Expected: PASS. Issue-flow integration behaviour is preserved.

- [ ] **Step 6: Commit**

```bash
git add cmd/agentdock/app.go app/workflow/dispatcher.go app/workflow/dispatcher_test.go app/slack/slack_adapter_port.go
git commit -m "feat(app): wire workflow dispatcher into cmd/agentdock; register IssueWorkflow"
```

### Task 2.8: Phase 2 build gate

- [ ] **Step 1: Full build + test**

Run: `go build ./... && (cd app && go test ./... -race) && (cd worker && go test ./... -race) && (cd shared && go test ./... -race) && go test ./test/ -race`
Expected: PASS across all modules.

- [ ] **Step 2: Manual smoke: mention bot in staging with legacy `@bot foo/bar`**

Trigger `@bot agentdock/agentdock` in a staging Slack thread. Expected: same behaviour as before the refactor — repo selector (if multi-repo) → branch → description → submit → issue creation.

- [ ] **Step 3: Commit a phase tag (empty commit)**

```bash
git commit --allow-empty -m "chore: phase 2 complete — IssueWorkflow behind polymorphic interface, legacy flow preserved"
```

---

## Phase 3 — `JobResult` cleanup + thin `ResultListener` + metrics overhaul

Remove Issue-specific fields from the `JobResult` wire type (they leaked into `shared/queue/job.go` when Issue was the only workflow). Rewrite `ResultListener.handleResult` as a thin dispatcher that delegates to `workflow.Workflow.HandleResult`. Replace Issue-specific metrics with `WorkflowCompletionsTotal{workflow, status}`.

### Task 3.1: Trim `JobResult` wire type

**Files:**
- Modify: `shared/queue/job.go`
- Modify: `shared/queue/job_test.go`

- [ ] **Step 1: Write failing test for trimmed wire shape**

`shared/queue/job_test.go` (add test):

```go
func TestJobResult_NoIssueSpecificFields(t *testing.T) {
	r := &JobResult{JobID: "j1", Status: "completed", RawOutput: "x"}
	buf, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	payload := string(buf)
	forbidden := []string{`"title"`, `"body"`, `"labels"`, `"confidence"`, `"files_found"`, `"open_questions"`, `"message"`}
	for _, f := range forbidden {
		if strings.Contains(payload, f) {
			t.Errorf("JobResult JSON should not contain %s after refactor: %s", f, payload)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `(cd shared && go test ./queue/ -run TestJobResult_NoIssueSpecificFields)`
Expected: FAIL — fields still present.

- [ ] **Step 3: Remove fields from `JobResult`**

`shared/queue/job.go` — edit the `JobResult` struct to remove `Title`, `Body`, `Labels`, `Confidence`, `FilesFound`, `Questions`, `Message`:

```go
type JobResult struct {
	JobID          string    `json:"job_id"`
	Status         string    `json:"status"`
	RawOutput      string    `json:"raw_output"`
	Error          string    `json:"error"`
	StartedAt      time.Time `json:"started_at"`
	FinishedAt     time.Time `json:"finished_at"`
	CostUSD        float64   `json:"cost_usd,omitempty"`
	InputTokens    int       `json:"input_tokens,omitempty"`
	OutputTokens  int       `json:"output_tokens,omitempty"`
	RepoPath       string    `json:"-"`
	PrepareSeconds float64   `json:"-"`
}
```

- [ ] **Step 4: Verify build fails (downstream consumers)**

Run: `go build ./...`
Expected: FAIL in `app/bot/result_listener.go` and `app/workflow/issue.go` — those files read `result.Title` etc.

- [ ] **Step 5: Fix compilation errors — per-workflow parsers now own these fields**

In `app/workflow/issue.go`'s `createAndPostIssue` (ported in Task 2.5), change calls like `result.Title` / `result.Body` to `parsed.Title` / `parsed.Body` (the `TriageResult` struct still has them). Same for `parsed.Confidence`, `parsed.FilesFound`, `parsed.Questions`, `parsed.Labels`. In `formatDiagnostics`, fields like `result.CostUSD` are retained.

In `app/bot/result_listener.go`, any code that mutated `result.Title/.Body/.Labels` etc. after parsing (the Phase 2 shim) goes away — the listener no longer calls `ParseAgentOutput`, which is now fully inside `IssueWorkflow.HandleResult`.

- [ ] **Step 6: Run tests**

Run: `(cd shared && go test ./queue/ -run TestJobResult_NoIssueSpecificFields -v) && (cd app && go test ./... -race)`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add shared/queue/job.go shared/queue/job_test.go app/workflow/issue.go app/bot/result_listener.go
git commit -m "refactor(queue): drop Issue-specific fields from JobResult wire type"
```

### Task 3.2: Add `WorkflowArgs` to `Job`

**Files:**
- Modify: `shared/queue/job.go`
- Modify: `shared/queue/job_test.go`

- [ ] **Step 1: Write failing test**

`shared/queue/job_test.go`:

```go
func TestJob_WorkflowArgsRoundTrips(t *testing.T) {
	j := &Job{
		ID: "j1", TaskType: "pr_review",
		WorkflowArgs: map[string]string{"pr_url": "https://github.com/foo/bar/pull/7", "pr_number": "7"},
	}
	buf, err := json.Marshal(j)
	if err != nil {
		t.Fatal(err)
	}
	var got Job
	if err := json.Unmarshal(buf, &got); err != nil {
		t.Fatal(err)
	}
	if got.WorkflowArgs["pr_url"] != "https://github.com/foo/bar/pull/7" {
		t.Errorf("WorkflowArgs[pr_url] = %q", got.WorkflowArgs["pr_url"])
	}
	if got.WorkflowArgs["pr_number"] != "7" {
		t.Errorf("WorkflowArgs[pr_number] = %q", got.WorkflowArgs["pr_number"])
	}
}

func TestJob_WorkflowArgsOmitEmpty(t *testing.T) {
	j := &Job{ID: "j1", TaskType: "issue"}
	buf, _ := json.Marshal(j)
	if strings.Contains(string(buf), "workflow_args") {
		t.Errorf("empty WorkflowArgs should be omitted: %s", buf)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `(cd shared && go test ./queue/ -run TestJob_WorkflowArgs)`
Expected: FAIL — `WorkflowArgs` field missing.

- [ ] **Step 3: Add the field**

`shared/queue/job.go` — add to the `Job` struct (after `TaskType`):

```go
	TaskType     string            `json:"task_type,omitempty"`
	WorkflowArgs map[string]string `json:"workflow_args,omitempty"`
```

- [ ] **Step 4: Run tests**

Run: `(cd shared && go test ./queue/ -run TestJob_WorkflowArgs -v)`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add shared/queue/job.go shared/queue/job_test.go
git commit -m "feat(queue): add Job.WorkflowArgs map for per-workflow wire metadata"
```

### Task 3.3: Thin `ResultListener`

**Files:**
- Modify: `app/bot/result_listener.go`
- Modify: `app/bot/result_listener_test.go`

Rewrite `ResultListener` to be a thin dispatcher: metrics + dedup + cancel fast-path + delegate to `workflow.Workflow.HandleResult` + attachment cleanup.

- [ ] **Step 1: Write new listener test**

`app/bot/result_listener_test.go` (replace Issue-parsing tests with dispatch tests):

```go
package bot

import (
	"context"
	"testing"

	"github.com/Ivantseng123/agentdock/app/workflow"
	"github.com/Ivantseng123/agentdock/shared/queue"
)

type recordingWorkflow struct {
	typ          string
	handledCalls int
	lastJob      *queue.Job
	lastResult   *queue.JobResult
}

func (r *recordingWorkflow) Type() string { return r.typ }
func (r *recordingWorkflow) Trigger(ctx context.Context, ev workflow.TriggerEvent, a string) (workflow.NextStep, error) {
	return workflow.NextStep{}, nil
}
func (r *recordingWorkflow) Selection(ctx context.Context, p *workflow.Pending, v string) (workflow.NextStep, error) {
	return workflow.NextStep{}, nil
}
func (r *recordingWorkflow) BuildJob(ctx context.Context, p *workflow.Pending) (*queue.Job, string, error) {
	return nil, "", nil
}
func (r *recordingWorkflow) HandleResult(ctx context.Context, job *queue.Job, res *queue.JobResult) error {
	r.handledCalls++
	r.lastJob = job
	r.lastResult = res
	return nil
}

func TestResultListener_DispatchesByTaskType(t *testing.T) {
	reg := workflow.NewRegistry()
	issuewf := &recordingWorkflow{typ: "issue"}
	reg.Register(issuewf)

	store := queue.NewMemJobStore()
	store.Put(&queue.Job{ID: "j1", TaskType: "issue", ChannelID: "C1", ThreadTS: "1.0"})
	rl := NewResultListener(/* see below for constructor */)
	rl.handleResult(context.Background(), &queue.JobResult{JobID: "j1", Status: "completed", RawOutput: ""})

	if issuewf.handledCalls != 1 {
		t.Errorf("expected 1 dispatch to issue workflow, got %d", issuewf.handledCalls)
	}
}

func TestResultListener_UnknownTaskType_FailsSafely(t *testing.T) {
	reg := workflow.NewRegistry()
	store := queue.NewMemJobStore()
	store.Put(&queue.Job{ID: "j1", TaskType: "nonsense"})
	rl := NewResultListener(/* ... */)

	// Should not panic; should log + do nothing to Slack.
	rl.handleResult(context.Background(), &queue.JobResult{JobID: "j1", Status: "completed"})
}
```

(Pass the registry to `NewResultListener` as a new parameter; keep the existing SlackPoster / JobStore / AttachmentStore params.)

- [ ] **Step 2: Run test to verify it fails**

Run: `(cd app && go test ./bot/ -run TestResultListener_Dispatches)`
Expected: FAIL — constructor signature mismatch / dispatch logic missing.

- [ ] **Step 3: Rewrite `ResultListener`**

`app/bot/result_listener.go` — full rewrite:

```go
package bot

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/Ivantseng123/agentdock/app/workflow"
	"github.com/Ivantseng123/agentdock/shared/metrics"
	"github.com/Ivantseng123/agentdock/shared/queue"
)

// SlackPoster is retained as a kept interface for the listener's own posting
// needs (e.g. unknown-task-type failure), even though workflows talk to Slack
// through workflow.SlackPort. They are compatible surfaces.
type SlackPoster interface {
	PostMessage(channelID, text, threadTS string)
	UpdateMessage(channelID, messageTS, text string)
}

type ResultListener struct {
	results      queue.ResultBus
	store        queue.JobStore
	attachments  queue.AttachmentStore
	slack        SlackPoster
	registry     *workflow.Registry
	onDedupClear func(channelID, threadTS string)
	logger       *slog.Logger

	mu                 sync.Mutex
	processedJobs      map[string]bool
	clearStatusMapping func(jobID string)
}

func NewResultListener(
	results queue.ResultBus,
	store queue.JobStore,
	attachments queue.AttachmentStore,
	slack SlackPoster,
	registry *workflow.Registry,
	onDedupClear func(channelID, threadTS string),
	logger *slog.Logger,
) *ResultListener {
	return &ResultListener{
		results: results, store: store, attachments: attachments,
		slack: slack, registry: registry, onDedupClear: onDedupClear,
		logger: logger, processedJobs: make(map[string]bool),
	}
}

func (r *ResultListener) Listen(ctx context.Context) {
	ch, err := r.results.Subscribe(ctx)
	if err != nil {
		r.logger.Error("訂閱 result bus 失敗", "phase", "失敗", "error", err)
		return
	}
	for {
		select {
		case result, ok := <-ch:
			if !ok {
				return
			}
			r.handleResult(ctx, result)
		case <-ctx.Done():
			return
		}
	}
}

func (r *ResultListener) handleResult(ctx context.Context, result *queue.JobResult) {
	r.mu.Lock()
	if r.processedJobs[result.JobID] {
		r.mu.Unlock()
		return
	}
	r.processedJobs[result.JobID] = true
	r.mu.Unlock()

	state, err := r.store.Get(result.JobID)
	if err != nil {
		r.logger.Error("找不到工作結果對應的工作", "phase", "失敗", "job_id", result.JobID, "error", err)
		return
	}

	r.recordMetrics(state, result)

	// Cancel fast-path — status update is common; the workflow does the nuance.
	if state.Status == queue.JobCancelled || result.Status == "cancelled" {
		r.store.UpdateStatus(state.Job.ID, queue.JobCancelled)
	}

	wf, ok := r.registry.Get(state.Job.TaskType)
	if !ok {
		r.logger.Error("unknown task_type", "job_id", result.JobID, "task_type", state.Job.TaskType)
		r.slack.PostMessage(state.Job.ChannelID, ":x: 未知的工作類型 `"+state.Job.TaskType+"`", state.Job.ThreadTS)
		r.attachments.Cleanup(ctx, result.JobID)
		return
	}

	if err := wf.HandleResult(ctx, state.Job, result); err != nil {
		r.logger.Error("workflow.HandleResult failed", "job_id", result.JobID, "workflow", state.Job.TaskType, "error", err)
	}

	r.attachments.Cleanup(ctx, result.JobID)

	if r.clearStatusMapping != nil {
		r.clearStatusMapping(result.JobID)
	}
}

func (r *ResultListener) SetStatusJobClearer(f func(jobID string)) { r.clearStatusMapping = f }

// recordMetrics retained from the old implementation; unchanged for now.
// Phase 3 Task 3.4 introduces WorkflowCompletionsTotal.
func (r *ResultListener) recordMetrics(state *queue.JobState, result *queue.JobResult) {
	job := state.Job
	if !job.SubmittedAt.IsZero() {
		elapsed := time.Since(job.SubmittedAt).Seconds()
		metrics.RequestDuration.Observe(elapsed)
		metrics.QueueJobDuration.WithLabelValues(job.TaskType, result.Status).Observe(elapsed)
	}
	if state.WaitTime > 0 {
		metrics.QueueWait.Observe(state.WaitTime.Seconds())
	}
	// Agent metrics block (port verbatim from the previous recordMetrics).
	if as := state.AgentStatus; as != nil {
		// … same as old implementation, but with an extra `job.TaskType` label
		// on AgentExecutionsTotal — see Task 3.4.
	}
}
```

- [ ] **Step 4: Run tests**

Run: `(cd app && go test ./bot/ -run TestResultListener)`
Expected: PASS.

- [ ] **Step 5: Update `cmd/agentdock/app.go` constructor call**

`NewResultListener` now takes the registry — update the construction site in `cmd/agentdock/app.go` accordingly.

- [ ] **Step 6: Full build + test**

Run: `go build ./... && (cd app && go test ./... -race)`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add app/bot/result_listener.go app/bot/result_listener_test.go cmd/agentdock/app.go
git commit -m "refactor(bot): ResultListener dispatches by Job.TaskType to registry"
```

### Task 3.4: Unified `WorkflowCompletionsTotal` metrics

**Files:**
- Modify: `shared/metrics/metrics.go`
- Modify: `shared/metrics/metrics_test.go`
- Modify: `app/workflow/issue.go` (emit new counter)
- Modify: `app/bot/result_listener.go`

- [ ] **Step 1: Write failing metrics test**

`shared/metrics/metrics_test.go`:

```go
func TestWorkflowCompletionsTotal_Registered(t *testing.T) {
	WorkflowCompletionsTotal.WithLabelValues("ask", "success").Inc()
	WorkflowCompletionsTotal.WithLabelValues("issue", "rejected").Inc()
	WorkflowCompletionsTotal.WithLabelValues("pr_review", "error").Inc()
	// If the metric isn't registered, .Inc() panics on nil receiver.
}

func TestWorkflowRetryTotal_Registered(t *testing.T) {
	WorkflowRetryTotal.WithLabelValues("issue", "attempted").Inc()
	WorkflowRetryTotal.WithLabelValues("issue", "exhausted").Inc()
}

func TestIssueSpecificMetrics_Removed(t *testing.T) {
	// Ensure old vars are gone. If a caller still references them, build
	// fails — this test just documents the removal.
	// (No assertions; compile-time is the check.)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `(cd shared && go test ./metrics/ -run TestWorkflow)`
Expected: FAIL — `WorkflowCompletionsTotal` / `WorkflowRetryTotal` undefined.

- [ ] **Step 3: Replace Issue-specific counters with unified ones**

`shared/metrics/metrics.go` — remove `IssueCreatedTotal`, `IssueRejectedTotal`, `IssueRetryTotal`, and add:

```go
// WorkflowCompletionsTotal counts per-workflow per-status completions.
// Labels: workflow ∈ {issue,ask,pr_review}; status ∈ {success,rejected,error,cancelled,parse_failed,skipped,posted}.
var WorkflowCompletionsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "agentdock_workflow_completions_total",
	Help: "Count of workflow completions, labelled by workflow and outcome status.",
}, []string{"workflow", "status"})

// WorkflowRetryTotal counts per-workflow retry attempts + exhaustions. Only
// Issue currently emits this; present on all workflows for forward compat.
var WorkflowRetryTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "agentdock_workflow_retry_total",
	Help: "Count of workflow retry attempts and exhaustions.",
}, []string{"workflow", "outcome"})
```

Update `QueueJobDuration` and `AgentExecutionsTotal` to include a `workflow` label:

```go
var QueueJobDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
	Name: "agentdock_queue_job_duration_seconds",
	Help: "Total time a job spent from submit to result, labelled by workflow and terminal status.",
}, []string{"workflow", "status"})

var AgentExecutionsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "agentdock_agent_executions_total",
	Help: "Count of agent CLI executions by provider, workflow, and status.",
}, []string{"provider", "workflow", "status"})
```

In the `init()` / exporter-registry, drop `IssueCreatedTotal`, `IssueRejectedTotal`, `IssueRetryTotal`; add `WorkflowCompletionsTotal`, `WorkflowRetryTotal`.

- [ ] **Step 4: Update call sites**

In `app/workflow/issue.go`:

- Where `metrics.IssueCreatedTotal.WithLabelValues(confidence, degraded).Inc()` appeared, replace with `metrics.WorkflowCompletionsTotal.WithLabelValues("issue", "success").Inc()`. Move `confidence` / `degraded` info into log lines (they're useful for debugging but not per-metric labels — otherwise cardinality blows up).
- Where `IssueRejectedTotal.WithLabelValues("low_confidence").Inc()` → `WorkflowCompletionsTotal.WithLabelValues("issue", "rejected").Inc()`.
- Where `IssueRetryTotal.WithLabelValues("exhausted").Inc()` → `WorkflowRetryTotal.WithLabelValues("issue", "exhausted").Inc()`; `"submitted"` → `"attempted"`.

In `app/bot/result_listener.go` `recordMetrics`, the updated `QueueJobDuration.WithLabelValues(job.TaskType, result.Status)` and `AgentExecutionsTotal.WithLabelValues(provider, job.TaskType, status)` calls are already added in Task 3.3 step 3 — double-check.

- [ ] **Step 5: Run metrics tests**

Run: `(cd shared && go test ./metrics/ -v)`
Expected: PASS.

- [ ] **Step 6: Run full test suite**

Run: `go build ./... && (cd app && go test ./... -race) && (cd shared && go test ./... -race)`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add shared/metrics/metrics.go shared/metrics/metrics_test.go app/workflow/issue.go app/bot/result_listener.go
git commit -m "feat(metrics): replace Issue counters with WorkflowCompletionsTotal / WorkflowRetryTotal"
```

### Task 3.5: Phase 3 build gate

- [ ] **Step 1: Full test suite**

Run: `go build ./... && (cd app && go test ./... -race) && (cd shared && go test ./... -race) && go test ./test/ -race`
Expected: PASS.

- [ ] **Step 2: Redis integration test**

Run: `(cd worker && go test ./integration/ -race -tags integration)` if Redis available; skipped otherwise.

- [ ] **Step 3: Commit phase tag**

```bash
git commit --allow-empty -m "chore: phase 3 complete — JobResult trimmed, ResultListener dispatches, unified metrics"
```

---

## Phase 4 — Worker `WorkDirProvider` + empty-dir skill spike

Add a worker-side abstraction that prepares a working directory. Two implementations: `RepoCloneProvider` (wraps the existing `RepoCache.Prepare`) and `EmptyDirProvider` (for Ask without repo). Includes a spike test that mounts a fake skill into an empty dir and runs each agent runner to verify skill discovery works in a non-repo directory.

### Task 4.1: `WorkDirProvider` interface + implementations

**Files:**
- Create: `worker/pool/workdir.go`
- Create: `worker/pool/workdir_test.go`

- [ ] **Step 1: Write failing tests**

`worker/pool/workdir_test.go`:

```go
package pool

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Ivantseng123/agentdock/shared/queue"
)

func TestEmptyDirProvider_PrepareAndCleanup(t *testing.T) {
	p := &EmptyDirProvider{}
	job := &queue.Job{ID: "j1"}

	dir, err := p.Prepare(job)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if dir == "" {
		t.Fatal("empty dir path")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("expected dir to exist: %v", err)
	}

	// Writable?
	if err := os.WriteFile(filepath.Join(dir, "marker"), []byte("x"), 0644); err != nil {
		t.Errorf("dir not writable: %v", err)
	}

	p.Cleanup(dir)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("Cleanup did not remove dir")
	}
}

func TestSelectProvider_ChoosesByCloneURL(t *testing.T) {
	tests := []struct {
		name     string
		cloneURL string
		wantKind string
	}{
		{"empty clone URL → EmptyDirProvider", "", "empty"},
		{"repo URL → RepoCloneProvider", "https://github.com/foo/bar.git", "clone"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeRepoProvider{}
			got := selectProvider(&queue.Job{CloneURL: tc.cloneURL}, repo)
			switch tc.wantKind {
			case "empty":
				if _, ok := got.(*EmptyDirProvider); !ok {
					t.Errorf("want *EmptyDirProvider, got %T", got)
				}
			case "clone":
				if _, ok := got.(*RepoCloneProvider); !ok {
					t.Errorf("want *RepoCloneProvider, got %T", got)
				}
			}
		})
	}
}

type fakeRepoProvider struct {
	prepared string
	cleaned  string
}

func (f *fakeRepoProvider) Prepare(cloneURL, branch, token string) (string, error) {
	f.prepared = cloneURL
	return "/tmp/fake-repo", nil
}
func (f *fakeRepoProvider) RemoveWorktree(p string) error { f.cleaned = p; return nil }
func (f *fakeRepoProvider) CleanAll() error               { return nil }
func (f *fakeRepoProvider) PurgeStale() error             { return nil }
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `(cd worker && go test ./pool/ -run TestEmptyDirProvider -run TestSelectProvider)`
Expected: FAIL — types missing.

- [ ] **Step 3: Implement**

`worker/pool/workdir.go`:

```go
package pool

import (
	"fmt"
	"os"

	"github.com/Ivantseng123/agentdock/shared/queue"
)

// WorkDirProvider prepares and cleans up the working directory an agent
// runs against. Two implementations:
//   - RepoCloneProvider: wraps RepoProvider (clone + remove worktree)
//   - EmptyDirProvider: mkdir temp dir + RemoveAll
// executeJob selects between them based on whether the Job carries a CloneURL.
type WorkDirProvider interface {
	Prepare(job *queue.Job) (path string, err error)
	Cleanup(path string)
}

// RepoCloneProvider wraps the existing RepoProvider so jobs with CloneURL set
// continue to use the repo cache + git checkout path unchanged.
type RepoCloneProvider struct {
	Repo  RepoProvider
	Token string // github token
}

func (p *RepoCloneProvider) Prepare(job *queue.Job) (string, error) {
	return p.Repo.Prepare(job.CloneURL, job.Branch, p.Token)
}

func (p *RepoCloneProvider) Cleanup(path string) {
	if path == "" {
		return
	}
	if err := p.Repo.RemoveWorktree(path); err != nil {
		// best-effort
	}
}

// EmptyDirProvider mkdirs a fresh temp directory per job. Used by Ask when
// the user didn't attach a repo.
type EmptyDirProvider struct{}

func (p *EmptyDirProvider) Prepare(job *queue.Job) (string, error) {
	dir, err := os.MkdirTemp("", fmt.Sprintf("ask-%s-*", job.ID))
	if err != nil {
		return "", fmt.Errorf("mkdir temp workdir: %w", err)
	}
	return dir, nil
}

func (p *EmptyDirProvider) Cleanup(path string) {
	if path == "" {
		return
	}
	_ = os.RemoveAll(path)
}

// selectProvider picks between RepoCloneProvider and EmptyDirProvider based
// on whether the Job has a CloneURL. The choice is by CloneURL rather than by
// TaskType to keep worker fully workflow-agnostic (spec Goal #6).
func selectProvider(job *queue.Job, repo RepoProvider, token string) WorkDirProvider {
	if job.CloneURL == "" {
		return &EmptyDirProvider{}
	}
	return &RepoCloneProvider{Repo: repo, Token: token}
}
```

- [ ] **Step 4: Run tests**

Run: `(cd worker && go test ./pool/ -run TestEmptyDirProvider -run TestSelectProvider -v)`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add worker/pool/workdir.go worker/pool/workdir_test.go
git commit -m "feat(worker): add WorkDirProvider with RepoClone + EmptyDir implementations"
```

### Task 4.2: Wire `WorkDirProvider` into `executor.go`

**Files:**
- Modify: `worker/pool/executor.go`

- [ ] **Step 1: Replace `repoCache.Prepare` call with `selectProvider`**

`worker/pool/executor.go` — inside `executeJob`, replace the block:

```go
repoPath, err := deps.repoCache.Prepare(job.CloneURL, job.Branch, ghToken)
if err != nil {
    return classifyResult(job, startedAt, fmt.Errorf("repo prepare failed: %w", err), "", ctx, deps.store)
}
```

with:

```go
provider := selectProvider(job, deps.repoCache, ghToken)
repoPath, err := provider.Prepare(job)
if err != nil {
    return classifyResult(job, startedAt, fmt.Errorf("workdir prepare failed: %w", err), "", ctx, deps.store)
}
```

Add `defer provider.Cleanup(repoPath)` after the success case (or wire the cleanup into the existing RepoPath teardown path — the old path relied on `pool.go` calling `RemoveWorktree(result.RepoPath)` after `executeJob` returns; keep that contract by having the caller call `provider.Cleanup` symmetrically).

For simplicity, plumb the provider back to the caller via the result's `RepoPath` + store a cleanup closure in `JobState` OR, easier: `executeJob` itself owns the `defer provider.Cleanup(repoPath)`.

If the existing `pool.go` does cleanup elsewhere, keep the behaviour identical: `executeJob` only returns `result.RepoPath` set to `repoPath`, and the caller uses its existing cleanup path (which now works because `EmptyDirProvider.Cleanup` is just `RemoveAll`).

Simplest approach: make `EmptyDirProvider`'s workdir path look like a repo-cache path to the caller. Since `RemoveWorktree` on a non-git directory is harmless (it does `os.RemoveAll`), this works without changes to `pool.go`.

- [ ] **Step 2: Verify `RemoveWorktree` tolerates non-repo paths**

`shared/github/repo.go`'s `RemoveWorktree` — confirm it's `os.RemoveAll(path)` and nothing git-specific. If not, update it to first try git-specific teardown (checking for `.git`) then fall back to `os.RemoveAll`.

- [ ] **Step 3: Run worker pool tests**

Run: `(cd worker && go test ./pool/ -race)`
Expected: PASS — existing tests continue to work; new `selectProvider` is transparent.

- [ ] **Step 4: Commit**

```bash
git add worker/pool/executor.go
git commit -m "refactor(worker): route workdir prepare/cleanup through WorkDirProvider"
```

### Task 4.3: Skill-mount spike test for each agent runner

**Files:**
- Modify: `worker/pool/workdir_test.go`

- [ ] **Step 1: Add spike test**

Append to `worker/pool/workdir_test.go`:

```go
// TestSkillMountInEmptyDir_ForEachRunner validates that each registered
// agent CLI discovers skills mounted under the agent's configured
// skill_dir in a non-git empty directory. This test gates PR 5 (Ask
// workflow): if any runner fails to see the skill, the EmptyDirProvider
// design needs a fallback (git init / HOME mount) before Ask ships.
//
// The "runner" here is a dummy that execs a shell script which inspects the
// CWD for the expected skill file. In CI, this is effectively a filesystem
// test — we don't launch the real claude/codex binaries. The value is
// confirming the path layout and permissions.
func TestSkillMountInEmptyDir_ForEachRunner(t *testing.T) {
	providers := []struct {
		name    string
		skillDir string
	}{
		{"claude", ".claude/skills"},
		{"codex", ".agents/skills"},
		{"gemini", ".gemini/skills"},
		{"opencode", ".agents/skills"},
	}

	for _, tc := range providers {
		t.Run(tc.name, func(t *testing.T) {
			emptyProvider := &EmptyDirProvider{}
			job := &queue.Job{
				ID: "spike-" + tc.name,
				Skills: map[string]*queue.SkillPayload{
					"spike-skill": {
						Files: map[string][]byte{
							"SKILL.md": []byte("---\nname: spike-skill\n---\ndetector"),
						},
					},
				},
			}

			dir, err := emptyProvider.Prepare(job)
			if err != nil {
				t.Fatalf("Prepare: %v", err)
			}
			defer emptyProvider.Cleanup(dir)

			// Mount the skill using the same mountSkills function executor.go uses.
			if err := mountSkills(dir, job.Skills, tc.skillDir); err != nil {
				t.Fatalf("mountSkills: %v", err)
			}

			// Assert file exists at the expected path.
			want := filepath.Join(dir, tc.skillDir, "spike-skill", "SKILL.md")
			if _, err := os.Stat(want); err != nil {
				t.Errorf("skill file not found at %s: %v", want, err)
			}
		})
	}
}
```

- [ ] **Step 2: Run spike**

Run: `(cd worker && go test ./pool/ -run TestSkillMountInEmptyDir -v)`
Expected: PASS for all four sub-tests.

- [ ] **Step 3: If a sub-test fails**

Document the failure in this plan's "Fallback decisions" table at the bottom (append if not present). Implement one of:

- `git init` the empty dir before mounting (add `exec.Command("git", "init", dir)` before `mountSkills`).
- Mount skills under `$HOME/<skill_dir>` instead (requires per-job cleanup discipline; skip unless git init also fails).

Re-run spike until all four sub-tests pass. The spike passing is the gate for PR 5.

- [ ] **Step 4: Commit**

```bash
git add worker/pool/workdir_test.go
git commit -m "test(worker): spike test for skill mount in empty dir across agent runners"
```

### Task 4.4: Phase 4 build gate

- [ ] **Step 1: Full suite**

Run: `go build ./... && (cd app && go test ./... -race) && (cd worker && go test ./... -race) && (cd shared && go test ./... -race) && go test ./test/ -race`
Expected: PASS.

- [ ] **Step 2: Commit phase tag**

```bash
git commit --allow-empty -m "chore: phase 4 complete — WorkDirProvider + empty-dir skill spike passed"
```

---

## Phase 5 — `AskWorkflow`

New file implementing the Ask workflow: trigger with optional question text, optional repo attachment button, submit. Result is the agent's answer posted back to the thread.

### Task 5.1: `AskWorkflow` skeleton + Type

**Files:**
- Create: `app/workflow/ask.go`
- Create: `app/workflow/ask_test.go`

- [ ] **Step 1: Write failing test**

`app/workflow/ask_test.go`:

```go
package workflow

import (
	"context"
	"testing"
)

func TestAskWorkflow_Type(t *testing.T) {
	w := &AskWorkflow{}
	if w.Type() != "ask" {
		t.Errorf("Type() = %q", w.Type())
	}
}

func TestAskWorkflow_Trigger_ReturnsRepoPrompt(t *testing.T) {
	w, _ := newTestAskWorkflow(t)
	step, err := w.Trigger(context.Background(), TriggerEvent{ChannelID: "C1", ThreadTS: "1.0"}, "what does X do?")
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if step.Kind != NextStepPostSelector {
		t.Errorf("expected NextStepPostSelector, got %v", step.Kind)
	}
	if len(step.SelectorActions) != 2 {
		t.Errorf("expected 2 actions (attach/skip), got %d", len(step.SelectorActions))
	}
}

func newTestAskWorkflow(t *testing.T) (*AskWorkflow, *fakeSlackPort) {
	t.Helper()
	cfg := &config.Config{}
	config.ApplyDefaults(cfg)
	slack := newFakeSlackPort()
	return NewAskWorkflow(cfg, slack, nil, slog.Default()), slack
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `(cd app && go test ./workflow/ -run TestAskWorkflow)`
Expected: FAIL — `AskWorkflow` undefined.

- [ ] **Step 3: Write skeleton**

`app/workflow/ask.go`:

```go
package workflow

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/Ivantseng123/agentdock/app/config"
	ghclient "github.com/Ivantseng123/agentdock/shared/github"
	"github.com/Ivantseng123/agentdock/shared/queue"
)

// AskWorkflow handles @bot ask queries. Optional attached repo (short wizard),
// no branch selection, no description modal. Result is an agent-produced
// answer posted as a bot message in the thread.
type AskWorkflow struct {
	cfg       *config.Config
	slack     SlackPort
	repoCache *ghclient.RepoCache
	logger    *slog.Logger
}

type askState struct {
	Question     string // from args; empty = use thread only
	AttachRepo   bool
	SelectedRepo string
}

// NewAskWorkflow constructs a workflow instance.
func NewAskWorkflow(cfg *config.Config, slack SlackPort, repoCache *ghclient.RepoCache, logger *slog.Logger) *AskWorkflow {
	if cfg == nil || slack == nil || logger == nil {
		panic("workflow: NewAskWorkflow missing required dep")
	}
	return &AskWorkflow{cfg: cfg, slack: slack, repoCache: repoCache, logger: logger}
}

func (w *AskWorkflow) Type() string { return "ask" }

// Trigger posts the attach-repo selector regardless of whether args has
// question text; if args is empty, the thread content is the question.
func (w *AskWorkflow) Trigger(ctx context.Context, ev TriggerEvent, args string) (NextStep, error) {
	pending := &Pending{
		ChannelID: ev.ChannelID, ThreadTS: ev.ThreadTS, TriggerTS: ev.TriggerTS, UserID: ev.UserID,
		Phase: "ask_repo_prompt",
		State: &askState{Question: args},
	}
	return NextStep{
		Kind:           NextStepPostSelector,
		SelectorPrompt: ":question: 要附加 repo context 嗎？",
		SelectorActions: []SelectorAction{
			{ActionID: "ask_attach_repo", Label: "附加", Value: "attach"},
			{ActionID: "ask_attach_repo", Label: "不用", Value: "skip"},
		},
		Pending: pending,
	}, nil
}
```

- [ ] **Step 4: Run tests**

Run: `(cd app && go test ./workflow/ -run TestAskWorkflow -v)`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add app/workflow/ask.go app/workflow/ask_test.go
git commit -m "feat(workflow): AskWorkflow skeleton + Trigger"
```

### Task 5.2: `AskWorkflow.Selection` (handle attach-repo → repo selector → submit)

**Files:**
- Modify: `app/workflow/ask.go`
- Modify: `app/workflow/ask_test.go`

- [ ] **Step 1: Write failing tests**

Extend `ask_test.go`:

```go
func TestAskWorkflow_Selection_SkipGoesToSubmit(t *testing.T) {
	w, _ := newTestAskWorkflow(t)
	p := &Pending{Phase: "ask_repo_prompt", State: &askState{Question: "Q"}, ChannelID: "C1", ThreadTS: "1.0"}
	step, err := w.Selection(context.Background(), p, "skip")
	if err != nil {
		t.Fatal(err)
	}
	if step.Kind != NextStepSubmit {
		t.Errorf("expected NextStepSubmit, got %v", step.Kind)
	}
	st := p.State.(*askState)
	if st.AttachRepo {
		t.Error("AttachRepo should be false")
	}
}

func TestAskWorkflow_Selection_AttachShowsRepoSelector(t *testing.T) {
	w, _ := newTestAskWorkflow(t)
	w.cfg.ChannelDefaults.Repos = []string{"foo/bar", "baz/qux"}
	p := &Pending{Phase: "ask_repo_prompt", State: &askState{Question: "Q"}, ChannelID: "C1", ThreadTS: "1.0"}
	step, err := w.Selection(context.Background(), p, "attach")
	if err != nil {
		t.Fatal(err)
	}
	if step.Kind != NextStepPostSelector {
		t.Errorf("expected NextStepPostSelector (repo choice), got %v", step.Kind)
	}
}

func TestAskWorkflow_Selection_RepoChoiceGoesToSubmit(t *testing.T) {
	w, _ := newTestAskWorkflow(t)
	p := &Pending{Phase: "ask_repo_select", State: &askState{Question: "Q", AttachRepo: true}, ChannelID: "C1", ThreadTS: "1.0"}
	step, err := w.Selection(context.Background(), p, "foo/bar")
	if err != nil {
		t.Fatal(err)
	}
	if step.Kind != NextStepSubmit {
		t.Errorf("expected NextStepSubmit, got %v", step.Kind)
	}
	st := p.State.(*askState)
	if st.SelectedRepo != "foo/bar" {
		t.Errorf("SelectedRepo = %q", st.SelectedRepo)
	}
}
```

- [ ] **Step 2: Run to verify failures**

Run: `(cd app && go test ./workflow/ -run TestAskWorkflow_Selection)`
Expected: FAIL.

- [ ] **Step 3: Implement `Selection`**

Add to `app/workflow/ask.go`:

```go
func (w *AskWorkflow) Selection(ctx context.Context, p *Pending, value string) (NextStep, error) {
	st, ok := p.State.(*askState)
	if !ok {
		return NextStep{Kind: NextStepError, ErrorText: "invalid pending state"}, nil
	}

	switch p.Phase {
	case "ask_repo_prompt":
		if value == "skip" {
			st.AttachRepo = false
			return NextStep{Kind: NextStepSubmit, Pending: p}, nil
		}
		// "attach" → post repo selector.
		st.AttachRepo = true
		repos := w.cfg.ChannelDefaults.Repos
		if cc, ok := w.cfg.Channels[p.ChannelID]; ok && len(cc.GetRepos()) > 0 {
			repos = cc.GetRepos()
		}
		p.Phase = "ask_repo_select"
		if len(repos) == 0 {
			// No repos configured — fall back to external search.
			return NextStep{
				Kind:           NextStepPostSelector,
				SelectorPrompt: ":point_right: Search and select a repo:",
				// dispatcher turns this into PostExternalSelector when SelectorActions is empty
				Pending: p,
			}, nil
		}
		actions := make([]SelectorAction, len(repos))
		for i, r := range repos {
			actions[i] = SelectorAction{ActionID: "ask_repo", Label: r, Value: r}
		}
		return NextStep{
			Kind:            NextStepPostSelector,
			SelectorPrompt:  ":point_right: Which repo?",
			SelectorActions: actions,
			Pending:         p,
		}, nil

	case "ask_repo_select":
		st.SelectedRepo = value
		return NextStep{Kind: NextStepSubmit, Pending: p}, nil
	}

	return NextStep{Kind: NextStepError, ErrorText: fmt.Sprintf("unknown phase %q", p.Phase)}, nil
}
```

- [ ] **Step 4: Run tests**

Run: `(cd app && go test ./workflow/ -run TestAskWorkflow_Selection -v)`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add app/workflow/ask.go app/workflow/ask_test.go
git commit -m "feat(workflow): AskWorkflow.Selection (attach repo / skip / pick repo)"
```

### Task 5.3: `AskWorkflow.BuildJob`

**Files:**
- Modify: `app/workflow/ask.go`
- Modify: `app/workflow/ask_test.go`

- [ ] **Step 1: Write failing tests**

```go
func TestAskWorkflow_BuildJob_NoRepo_LeavesCloneURLEmpty(t *testing.T) {
	w, _ := newTestAskWorkflow(t)
	p := &Pending{ChannelID: "C1", ThreadTS: "1.0", UserID: "U1", State: &askState{Question: "Q", AttachRepo: false}}
	job, status, err := w.BuildJob(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if job.TaskType != "ask" {
		t.Errorf("TaskType = %q", job.TaskType)
	}
	if job.CloneURL != "" {
		t.Errorf("CloneURL should be empty, got %q", job.CloneURL)
	}
	if job.Skills != nil {
		t.Errorf("Skills should be nil for Ask (spec §Worker side changes — defensive)")
	}
	if status != ":thinking_face: 思考中..." {
		t.Errorf("status = %q, want '思考中'", status)
	}
	if job.PromptContext == nil || job.PromptContext.Goal == "" {
		t.Error("PromptContext.Goal must be populated")
	}
}

func TestAskWorkflow_BuildJob_WithRepo_PopulatesCloneURL(t *testing.T) {
	w, _ := newTestAskWorkflow(t)
	p := &Pending{ChannelID: "C1", ThreadTS: "1.0", UserID: "U1", State: &askState{Question: "Q", AttachRepo: true, SelectedRepo: "foo/bar"}}
	job, _, err := w.BuildJob(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if job.Repo != "foo/bar" {
		t.Errorf("Repo = %q", job.Repo)
	}
	if job.CloneURL != "https://github.com/foo/bar.git" {
		t.Errorf("CloneURL = %q", job.CloneURL)
	}
}
```

- [ ] **Step 2: Run tests to verify failures**

Run: `(cd app && go test ./workflow/ -run TestAskWorkflow_BuildJob)`
Expected: FAIL — not implemented.

- [ ] **Step 3: Implement**

```go
func (w *AskWorkflow) BuildJob(ctx context.Context, p *Pending) (*queue.Job, string, error) {
	st, ok := p.State.(*askState)
	if !ok {
		return nil, "", fmt.Errorf("invalid pending state")
	}

	// Load thread messages (using SlackPort).
	// (The full prompt context assembly mirrors app/workflow/issue.go's
	// BuildJob; the difference is Goal/OutputRules come from cfg.Prompt.Ask
	// instead of cfg.Prompt.Issue, and ExtraDescription = st.Question.)

	cloneURL := ""
	if st.AttachRepo && st.SelectedRepo != "" {
		cloneURL = fmt.Sprintf("https://github.com/%s.git", st.SelectedRepo)
	}

	promptCtx := queue.PromptContext{
		// ThreadMessages populated by helper that calls SlackPort.FetchThreadContext
		// Reporter / Channel / Branch / Language copied from common helper
		ExtraDescription: st.Question,
		Goal:             w.cfg.Prompt.Ask.Goal,
		OutputRules:      w.cfg.Prompt.Ask.OutputRules,
		AllowWorkerRules: w.cfg.Prompt.IsWorkerRulesAllowed(),
	}

	job := &queue.Job{
		ID:            p.RequestID,
		TaskType:      "ask",
		ChannelID:     p.ChannelID,
		ThreadTS:      p.ThreadTS,
		UserID:        p.UserID,
		Repo:          st.SelectedRepo,
		CloneURL:      cloneURL,
		PromptContext: &promptCtx,
		// Skills intentionally nil — spec §Worker side, defensive until
		// empty-dir skill spike (Phase 4) is green AND the Ask flow has
		// observed-safe for a release cycle.
		Skills: nil,
	}
	return job, ":thinking_face: 思考中...", nil
}
```

- [ ] **Step 4: Run tests**

Run: `(cd app && go test ./workflow/ -run TestAskWorkflow_BuildJob -v)`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add app/workflow/ask.go app/workflow/ask_test.go
git commit -m "feat(workflow): AskWorkflow.BuildJob with optional repo + Skills=nil"
```

### Task 5.4: `AskWorkflow` parser + `HandleResult`

**Files:**
- Create: `app/workflow/ask_parser.go`
- Create: `app/workflow/ask_parser_test.go`
- Modify: `app/workflow/ask.go`
- Modify: `app/workflow/ask_test.go`

- [ ] **Step 1: Write failing parser tests**

`app/workflow/ask_parser_test.go`:

```go
package workflow

import "testing"

func TestParseAskOutput_Valid(t *testing.T) {
	out := "thinking...\n===ASK_RESULT===\n" + `{"answer": "42", "confidence": "high"}`
	r, err := ParseAskOutput(out)
	if err != nil {
		t.Fatal(err)
	}
	if r.Answer != "42" {
		t.Errorf("Answer = %q", r.Answer)
	}
}

func TestParseAskOutput_MarkerMissing(t *testing.T) {
	_, err := ParseAskOutput("no marker here")
	if err == nil {
		t.Error("expected error when marker missing")
	}
}

func TestParseAskOutput_EmptyAnswer(t *testing.T) {
	_, err := ParseAskOutput("===ASK_RESULT===\n" + `{"answer": ""}`)
	if err == nil {
		t.Error("empty answer must be rejected")
	}
}

func TestParseAskOutput_MalformedJSON(t *testing.T) {
	_, err := ParseAskOutput("===ASK_RESULT===\n{not json")
	if err == nil {
		t.Error("malformed JSON must error")
	}
}

func TestParseAskOutput_MultipleMarkers_LastWins(t *testing.T) {
	out := "===ASK_RESULT===\n{\"answer\":\"first\"}\nok\n===ASK_RESULT===\n{\"answer\":\"second\"}"
	r, err := ParseAskOutput(out)
	if err != nil {
		t.Fatal(err)
	}
	if r.Answer != "second" {
		t.Errorf("last marker should win, got %q", r.Answer)
	}
}
```

- [ ] **Step 2: Run to verify fails**

Run: `(cd app && go test ./workflow/ -run TestParseAskOutput)`
Expected: FAIL.

- [ ] **Step 3: Implement parser**

`app/workflow/ask_parser.go`:

```go
package workflow

import (
	"encoding/json"
	"fmt"
	"strings"
)

const askMarker = "===ASK_RESULT==="

// AskResult is the parsed ===ASK_RESULT=== JSON.
type AskResult struct {
	Answer     string `json:"answer"`
	Confidence string `json:"confidence,omitempty"`
}

// ParseAskOutput extracts the last ASK_RESULT marker block and unmarshals
// its JSON body. Rejects empty answers.
func ParseAskOutput(output string) (AskResult, error) {
	output = strings.TrimSpace(output)
	idx := strings.LastIndex(output, askMarker)
	if idx == -1 {
		return AskResult{}, fmt.Errorf("%s marker not found", askMarker)
	}
	body := strings.TrimSpace(output[idx+len(askMarker):])
	if !strings.HasPrefix(body, "{") {
		return AskResult{}, fmt.Errorf("expected JSON object after marker")
	}
	jsonStr := extractJSON(body) // reused from issue_parser.go
	var r AskResult
	if err := json.Unmarshal([]byte(jsonStr), &r); err != nil {
		return AskResult{}, fmt.Errorf("unmarshal: %w", err)
	}
	if strings.TrimSpace(r.Answer) == "" {
		return AskResult{}, fmt.Errorf("answer must not be empty")
	}
	return r, nil
}
```

- [ ] **Step 4: Run parser tests**

Run: `(cd app && go test ./workflow/ -run TestParseAskOutput -v)`
Expected: PASS.

- [ ] **Step 5: Write HandleResult tests**

```go
func TestAskWorkflow_HandleResult_SuccessPostsAnswer(t *testing.T) {
	w, slack := newTestAskWorkflow(t)
	job := &queue.Job{ID: "j1", ChannelID: "C1", ThreadTS: "1.0", StatusMsgTS: "s-ts", TaskType: "ask"}
	result := &queue.JobResult{
		JobID: "j1", Status: "completed",
		RawOutput: "===ASK_RESULT===\n{\"answer\":\"the answer is 42\"}",
	}
	if err := w.HandleResult(context.Background(), job, result); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(slack.Posted, " | ")
	if !strings.Contains(joined, "42") {
		t.Errorf("expected answer in posted text, got: %v", slack.Posted)
	}
}

func TestAskWorkflow_HandleResult_Truncates38K(t *testing.T) {
	w, slack := newTestAskWorkflow(t)
	long := strings.Repeat("a", 50000)
	result := &queue.JobResult{
		JobID: "j1", Status: "completed",
		RawOutput: "===ASK_RESULT===\n{\"answer\":\"" + long + "\"}",
	}
	job := &queue.Job{ID: "j1", ChannelID: "C1", ThreadTS: "1.0", StatusMsgTS: "s-ts", TaskType: "ask"}
	if err := w.HandleResult(context.Background(), job, result); err != nil {
		t.Fatal(err)
	}
	last := slack.Posted[len(slack.Posted)-1]
	if len(last) > 38000+len("\n…(已截斷)") {
		t.Errorf("posted text exceeds truncate limit: %d chars", len(last))
	}
	if !strings.Contains(last, "已截斷") {
		t.Error("truncate suffix missing")
	}
}

func TestAskWorkflow_HandleResult_FailureNoRetryButton(t *testing.T) {
	w, slack := newTestAskWorkflow(t)
	job := &queue.Job{ID: "j1", ChannelID: "C1", ThreadTS: "1.0", TaskType: "ask"}
	result := &queue.JobResult{JobID: "j1", Status: "failed", Error: "timeout"}
	if err := w.HandleResult(context.Background(), job, result); err != nil {
		t.Fatal(err)
	}
	// fakeSlackPort doesn't distinguish PostMessage vs PostMessageWithButton;
	// assert the failure text went out and no separate button value appeared.
	joined := strings.Join(slack.Posted, " | ")
	if !strings.Contains(joined, "思考失敗") {
		t.Errorf("expected 思考失敗 text, got: %v", slack.Posted)
	}
}
```

- [ ] **Step 6: Implement `HandleResult`**

Add to `app/workflow/ask.go`:

```go
const askMaxChars = 38000

func (w *AskWorkflow) HandleResult(ctx context.Context, job *queue.Job, r *queue.JobResult) error {
	if r.Status == "failed" {
		text := fmt.Sprintf(":x: 思考失敗：%s", r.Error)
		return w.post(job, text)
	}

	parsed, err := ParseAskOutput(r.RawOutput)
	if err != nil {
		truncated := r.RawOutput
		if len(truncated) > 2000 {
			truncated = truncated[:2000] + "…(truncated)"
		}
		w.logger.Warn("ask parse failed", "phase", "失敗", "output", truncated, "err", err)
		metrics.WorkflowCompletionsTotal.WithLabelValues("ask", "parse_failed").Inc()
		return w.post(job, fmt.Sprintf(":x: 解析失敗：%v", err))
	}

	answer := parsed.Answer
	if len(answer) > askMaxChars {
		answer = answer[:askMaxChars] + "\n…(已截斷)"
	}

	metrics.WorkflowCompletionsTotal.WithLabelValues("ask", "success").Inc()
	return w.post(job, answer)
}

// post writes to the job's status message if set, else posts a new message.
func (w *AskWorkflow) post(job *queue.Job, text string) error {
	if job.StatusMsgTS != "" {
		return w.slack.UpdateMessage(job.ChannelID, job.StatusMsgTS, text)
	}
	return w.slack.PostMessage(job.ChannelID, text, job.ThreadTS)
}
```

- [ ] **Step 7: Run tests**

Run: `(cd app && go test ./workflow/ -run TestAskWorkflow_HandleResult -v)`
Expected: PASS for all three.

- [ ] **Step 8: Commit**

```bash
git add app/workflow/ask.go app/workflow/ask_parser.go app/workflow/ask_parser_test.go app/workflow/ask_test.go
git commit -m "feat(workflow): AskWorkflow parser + HandleResult with 38K truncate"
```

### Task 5.5: Register `AskWorkflow` in `cmd/agentdock/app.go`

**Files:**
- Modify: `cmd/agentdock/app.go`

- [ ] **Step 1: Register**

In the dispatcher wiring block (added in Task 2.7), add:

```go
reg.Register(workflow.NewAskWorkflow(cfg, slackPort, repoCache, logger))
```

- [ ] **Step 2: Full build + test**

Run: `go build ./... && (cd app && go test ./... -race)`
Expected: PASS.

- [ ] **Step 3: Manual smoke: `@bot ask what's our deploy flow?`**

Trigger in staging. Expected: bot posts attach-repo prompt → user clicks "不用" → bot posts "思考中..." → agent runs → bot replaces message with answer.

- [ ] **Step 4: Commit**

```bash
git add cmd/agentdock/app.go
git commit -m "feat(app): register AskWorkflow with dispatcher"
```

### Task 5.6: Phase 5 build gate

- [ ] **Step 1: Full suite**

Run: `go build ./... && (cd app && go test ./... -race) && (cd worker && go test ./... -race) && (cd shared && go test ./... -race) && go test ./test/ -race`
Expected: PASS.

- [ ] **Step 2: Phase tag**

```bash
git commit --allow-empty -m "chore: phase 5 complete — AskWorkflow live and reachable via @bot ask"
```

---

## Phase 6 — `PRReviewWorkflow` + feature flag

New file implementing PR Review. URL parser + validator (regex + GitHub API). A-path (URL in mention) and D-path (scan thread / modal). `BuildJob` populates `Repo`, `Branch`, `WorkflowArgs`. Parses three-state `===REVIEW_RESULT===`. Feature-flag gated.

### Task 6.1: Generalise `OpenDescriptionModal` → `OpenTextInputModal`

**Files:**
- Modify: `app/slack/client.go`
- Modify: `app/slack/client_test.go`

- [ ] **Step 1: Add new method**

`app/slack/client.go` — add:

```go
// OpenTextInputModal opens a modal with a single multiline text input.
// metadata is stored in the view's private_metadata so the submit handler
// can resolve the originating pending entry.
func (c *Client) OpenTextInputModal(triggerID, title, label, inputName, metadata string) error {
	textInput := slack.NewPlainTextInputBlockElement(
		slack.NewTextBlockObject(slack.PlainTextType, "請輸入...", false, false),
		inputName,
	)
	textInput.Multiline = true

	inputBlock := slack.NewInputBlock(
		inputName+"_block",
		slack.NewTextBlockObject(slack.PlainTextType, label, false, false),
		nil, textInput,
	)
	inputBlock.Optional = false

	modalView := slack.ModalViewRequest{
		Type:            slack.VTModal,
		Title:           slack.NewTextBlockObject(slack.PlainTextType, title, false, false),
		Submit:          slack.NewTextBlockObject(slack.PlainTextType, "送出", false, false),
		Close:           slack.NewTextBlockObject(slack.PlainTextType, "取消", false, false),
		Blocks:          slack.Blocks{BlockSet: []slack.Block{inputBlock}},
		PrivateMetadata: metadata,
	}
	_, err := c.api.OpenView(triggerID, modalView)
	if err != nil {
		return fmt.Errorf("open text input modal: %w", err)
	}
	return nil
}
```

- [ ] **Step 2: Update `OpenDescriptionModal` to delegate**

```go
func (c *Client) OpenDescriptionModal(triggerID, selectorMsgTS string) error {
	return c.OpenTextInputModal(triggerID, "補充說明", "補充說明", "description_input", selectorMsgTS)
}
```

(The optional=true flavour of description is acceptable to lose here — Ask skipped that step anyway; description is not critical.)

- [ ] **Step 3: Update `slackAdapterPort` in `cmd/agentdock/app.go`**

Replace the stub `OpenTextInputModal` that returned "not implemented until Phase 6" with a pass-through:

```go
func (a slackAdapterPort) OpenTextInputModal(triggerID, title, label, inputName, metadata string) error {
	return a.client.OpenTextInputModal(triggerID, title, label, inputName, metadata)
}
```

- [ ] **Step 4: Run tests**

Run: `go build ./... && (cd app && go test ./slack/ -race)`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add app/slack/client.go cmd/agentdock/app.go
git commit -m "feat(slack): generalise OpenDescriptionModal → OpenTextInputModal"
```

### Task 6.2: PR URL parser + validator

**Files:**
- Create: `app/workflow/pr_review_url.go`
- Create: `app/workflow/pr_review_url_test.go`

- [ ] **Step 1: Write failing tests**

`app/workflow/pr_review_url_test.go`:

```go
package workflow

import "testing"

func TestParsePRURL_Valid(t *testing.T) {
	cases := []struct {
		in           string
		wantOwner    string
		wantRepo     string
		wantNumber   int
	}{
		{"https://github.com/foo/bar/pull/7", "foo", "bar", 7},
		{"https://github.com/Ivantseng123/agentdock/pull/117", "Ivantseng123", "agentdock", 117},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParsePRURL(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if got.Owner != tc.wantOwner || got.Repo != tc.wantRepo || got.Number != tc.wantNumber {
				t.Errorf("got {%s %s %d}", got.Owner, got.Repo, got.Number)
			}
		})
	}
}

func TestParsePRURL_Invalid(t *testing.T) {
	cases := []string{
		"",
		"github.com/foo/bar/pull/7",             // missing protocol
		"https://example.com/foo/bar/pull/7",    // not github.com
		"foo/bar#7",                              // shortened form
		"https://github.com/foo/bar/issues/7",   // issues not pull
		"https://github.com/foo/bar/pull/abc",   // non-numeric
		"https://github.com/foo/bar/pull/",      // no number
	}
	for _, tc := range cases {
		t.Run(tc, func(t *testing.T) {
			if _, err := ParsePRURL(tc); err == nil {
				t.Errorf("expected error for %q", tc)
			}
		})
	}
}

func TestScanThreadForPRURL(t *testing.T) {
	msgs := []string{
		"no URL here",
		"please review https://github.com/foo/bar/pull/10 thanks",
		"follow-up discussion",
	}
	got, ok := ScanThreadForPRURL(msgs)
	if !ok {
		t.Fatal("expected match")
	}
	if got != "https://github.com/foo/bar/pull/10" {
		t.Errorf("got %q", got)
	}
}
```

- [ ] **Step 2: Run to verify failures**

Run: `(cd app && go test ./workflow/ -run TestParsePRURL -run TestScanThread)`
Expected: FAIL.

- [ ] **Step 3: Implement**

`app/workflow/pr_review_url.go`:

```go
package workflow

import (
	"fmt"
	"regexp"
	"strconv"
)

// prURLRe captures owner/repo/number from a canonical GitHub PR URL.
// Only github.com is accepted (spec §Non-goals: enterprise deferred to v2).
var prURLRe = regexp.MustCompile(`^https://github\.com/([A-Za-z0-9._-]+)/([A-Za-z0-9._-]+)/pull/(\d+)(?:[/?#].*)?$`)

// PRURLParts captures the components of a parsed GitHub PR URL.
type PRURLParts struct {
	URL    string
	Owner  string
	Repo   string
	Number int
}

// ParsePRURL validates syntactic shape + extracts parts. Does NOT touch
// GitHub — use GitHubPR.GetPullRequest for existence / accessibility.
func ParsePRURL(url string) (PRURLParts, error) {
	m := prURLRe.FindStringSubmatch(url)
	if m == nil {
		return PRURLParts{}, fmt.Errorf("not a valid github.com PR URL: %q", url)
	}
	num, err := strconv.Atoi(m[3])
	if err != nil {
		return PRURLParts{}, fmt.Errorf("non-numeric PR number: %s", m[3])
	}
	return PRURLParts{URL: url, Owner: m[1], Repo: m[2], Number: num}, nil
}

// ScanThreadForPRURL returns the first PR URL found anywhere in the given
// messages, or ("", false) if none. Strips Slack <...> wrapping.
func ScanThreadForPRURL(msgs []string) (string, bool) {
	for _, m := range msgs {
		// Look for any substring matching the PR URL shape.
		if loc := prURLRe.FindStringSubmatchIndex(unwrapSlackURLs(m)); loc != nil {
			matched := unwrapSlackURLs(m)[loc[0]:loc[1]]
			return matched, true
		}
	}
	return "", false
}

// unwrapSlackURLs strips Slack's <...> URL wrapping from inline mentions.
func unwrapSlackURLs(s string) string {
	// Apply stripSlackURLWrap line-by-line is overkill; simple replace:
	// <https://...> → https://...
	// <https://...|display> → https://...
	out := ""
	for i := 0; i < len(s); {
		if s[i] == '<' {
			if end := findMatchingGT(s, i); end > 0 {
				inner := s[i+1 : end]
				// strip |display if any
				if pipe := indexByte(inner, '|'); pipe >= 0 {
					inner = inner[:pipe]
				}
				out += inner
				i = end + 1
				continue
			}
		}
		out += string(s[i])
		i++
	}
	return out
}

func findMatchingGT(s string, open int) int {
	for i := open + 1; i < len(s); i++ {
		if s[i] == '>' {
			return i
		}
	}
	return -1
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}
```

- [ ] **Step 4: Run tests**

Run: `(cd app && go test ./workflow/ -run TestParsePRURL -run TestScanThread -v)`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add app/workflow/pr_review_url.go app/workflow/pr_review_url_test.go
git commit -m "feat(workflow): PR URL parser + thread scanner"
```

### Task 6.3: `PRReviewWorkflow` skeleton + A-path `Trigger`

**Files:**
- Create: `app/workflow/pr_review.go`
- Create: `app/workflow/pr_review_test.go`

- [ ] **Step 1: Write failing tests**

`app/workflow/pr_review_test.go`:

```go
package workflow

import (
	"context"
	"errors"
	"testing"
)

type fakeGitHubPR struct {
	pr      *PullRequest
	err     error
	calledN int
}

func (f *fakeGitHubPR) GetPullRequest(ctx context.Context, owner, repo string, number int) (*PullRequest, error) {
	f.calledN = number
	return f.pr, f.err
}

func TestPRReviewWorkflow_Type(t *testing.T) {
	w := &PRReviewWorkflow{}
	if w.Type() != "pr_review" {
		t.Errorf("Type() = %q", w.Type())
	}
}

func TestPRReviewWorkflow_TriggerAPath_Valid(t *testing.T) {
	pr := &PullRequest{Number: 7, State: "open", Title: "T"}
	pr.Head.Ref = "feature-x"
	pr.Head.SHA = "abc123"
	pr.Head.Repo.FullName = "forker/bar"
	pr.Head.Repo.CloneURL = "https://github.com/forker/bar.git"
	pr.Base.Ref = "main"

	w, _ := newTestPRReviewWorkflow(t)
	w.github = &fakeGitHubPR{pr: pr}

	step, err := w.Trigger(context.Background(), TriggerEvent{ChannelID: "C1", ThreadTS: "1.0"}, "https://github.com/foo/bar/pull/7")
	if err != nil {
		t.Fatal(err)
	}
	if step.Kind != NextStepSubmit {
		t.Errorf("expected NextStepSubmit, got %v", step.Kind)
	}
	st := step.Pending.State.(*prReviewState)
	if st.HeadRepo != "forker/bar" {
		t.Errorf("HeadRepo = %q", st.HeadRepo)
	}
}

func TestPRReviewWorkflow_TriggerAPath_404(t *testing.T) {
	w, slack := newTestPRReviewWorkflow(t)
	w.github = &fakeGitHubPR{err: errors.New("404 not found")}
	step, _ := w.Trigger(context.Background(), TriggerEvent{ChannelID: "C1", ThreadTS: "1.0"}, "https://github.com/foo/bar/pull/999")
	if step.Kind != NextStepError {
		t.Errorf("expected NextStepError, got %v", step.Kind)
	}
	_ = slack
}

func TestPRReviewWorkflow_TriggerAPath_PartialURLRejected(t *testing.T) {
	w, _ := newTestPRReviewWorkflow(t)
	step, _ := w.Trigger(context.Background(), TriggerEvent{ChannelID: "C1", ThreadTS: "1.0"}, "github.com/foo/bar/pull/7")
	if step.Kind != NextStepError {
		t.Errorf("expected NextStepError on partial URL")
	}
}

func newTestPRReviewWorkflow(t *testing.T) (*PRReviewWorkflow, *fakeSlackPort) {
	t.Helper()
	cfg := &config.Config{}
	config.ApplyDefaults(cfg)
	cfg.PRReview.Enabled = true
	slack := newFakeSlackPort()
	w := NewPRReviewWorkflow(cfg, slack, &fakeGitHubPR{}, nil, slog.Default())
	return w, slack
}
```

- [ ] **Step 2: Run to verify failures**

Run: `(cd app && go test ./workflow/ -run TestPRReviewWorkflow)`
Expected: FAIL.

- [ ] **Step 3: Implement skeleton + `Trigger` A-path + D-path**

`app/workflow/pr_review.go`:

```go
package workflow

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/Ivantseng123/agentdock/app/config"
	ghclient "github.com/Ivantseng123/agentdock/shared/github"
	"github.com/Ivantseng123/agentdock/shared/metrics"
	"github.com/Ivantseng123/agentdock/shared/queue"
)

// PRReviewWorkflow handles @bot review <PR URL>. Feature-flag gated.
type PRReviewWorkflow struct {
	cfg       *config.Config
	slack     SlackPort
	github    GitHubPR
	repoCache *ghclient.RepoCache
	logger    *slog.Logger
}

type prReviewState struct {
	URL       string
	Owner     string
	Repo      string
	Number    int
	HeadRepo  string // head.repo.full_name; may differ from Owner/Repo for forks
	HeadRef   string
	BaseRef   string
}

func NewPRReviewWorkflow(cfg *config.Config, slack SlackPort, gh GitHubPR, repoCache *ghclient.RepoCache, logger *slog.Logger) *PRReviewWorkflow {
	if cfg == nil || slack == nil || logger == nil {
		panic("workflow: NewPRReviewWorkflow missing required dep")
	}
	return &PRReviewWorkflow{cfg: cfg, slack: slack, github: gh, repoCache: repoCache, logger: logger}
}

func (w *PRReviewWorkflow) Type() string { return "pr_review" }

// Trigger — feature flag → A-path (URL in args) or D-path (scan thread / modal).
func (w *PRReviewWorkflow) Trigger(ctx context.Context, ev TriggerEvent, args string) (NextStep, error) {
	if !w.cfg.PRReview.Enabled {
		return NextStep{Kind: NextStepError, ErrorText: ":warning: PR Review 尚未啟用，請聯絡管理員"}, nil
	}

	args = strings.TrimSpace(args)
	if args != "" {
		return w.validateAndBuild(ctx, ev, args)
	}

	// D-path: scan thread.
	msgs, err := w.slack.FetchThreadContext(ev.ChannelID, ev.ThreadTS, ev.TriggerTS, "", 50)
	if err == nil {
		texts := make([]string, len(msgs))
		for i, m := range msgs {
			texts[i] = m.Text
		}
		if url, ok := ScanThreadForPRURL(texts); ok {
			pending := &Pending{
				ChannelID: ev.ChannelID, ThreadTS: ev.ThreadTS, TriggerTS: ev.TriggerTS, UserID: ev.UserID,
				Phase: "pr_review_confirm",
				State: &prReviewState{URL: url},
			}
			return NextStep{
				Kind:           NextStepPostSelector,
				SelectorPrompt: fmt.Sprintf(":eyes: 找到 `%s`，review？", url),
				SelectorActions: []SelectorAction{
					{ActionID: "pr_review_confirm", Label: "是", Value: url},
					{ActionID: "pr_review_confirm", Label: "改貼 URL", Value: "manual"},
				},
				Pending: pending,
			}, nil
		}
	}

	// Not found → modal.
	pending := &Pending{
		ChannelID: ev.ChannelID, ThreadTS: ev.ThreadTS, TriggerTS: ev.TriggerTS, UserID: ev.UserID,
		Phase: "pr_review_modal",
		State: &prReviewState{},
	}
	return NextStep{
		Kind:           NextStepOpenModal,
		ModalTriggerID: "",
		ModalTitle:     "PR Review",
		ModalLabel:     "貼上 PR URL",
		ModalInputName: "pr_url",
		Pending:        pending,
	}, nil
}

// validateAndBuild runs the URL validator + GitHub API check, returning
// either a submit-ready NextStep or an error step with a friendly message.
func (w *PRReviewWorkflow) validateAndBuild(ctx context.Context, ev TriggerEvent, urlStr string) (NextStep, error) {
	parts, err := ParsePRURL(urlStr)
	if err != nil {
		return NextStep{Kind: NextStepError, ErrorText: ":x: 請貼完整 PR URL"}, nil
	}

	if w.github == nil {
		return NextStep{Kind: NextStepError, ErrorText: ":x: GitHub client not configured"}, nil
	}

	pr, err := w.github.GetPullRequest(ctx, parts.Owner, parts.Repo, parts.Number)
	if err != nil {
		msg := mapGitHubErrorToSlack(err)
		return NextStep{Kind: NextStepError, ErrorText: msg}, nil
	}

	state := &prReviewState{
		URL:      urlStr,
		Owner:    parts.Owner,
		Repo:     parts.Repo,
		Number:   parts.Number,
		HeadRepo: pr.Head.Repo.FullName,
		HeadRef:  pr.Head.Ref,
		BaseRef:  pr.Base.Ref,
	}
	pending := &Pending{
		ChannelID: ev.ChannelID, ThreadTS: ev.ThreadTS, TriggerTS: ev.TriggerTS, UserID: ev.UserID,
		State: state,
	}
	return NextStep{Kind: NextStepSubmit, Pending: pending}, nil
}

func mapGitHubErrorToSlack(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "404"):
		return ":x: 找不到 PR"
	case strings.Contains(msg, "403"):
		return ":x: 沒權限存取 PR"
	case strings.Contains(msg, "dial"), strings.Contains(msg, "timeout"):
		return ":x: GitHub 不可達，請稍後重試"
	default:
		return ":x: GitHub API 錯誤: " + msg
	}
}

// Selection handles confirm / manual buttons and modal submits.
func (w *PRReviewWorkflow) Selection(ctx context.Context, p *Pending, value string) (NextStep, error) {
	st := p.State.(*prReviewState)
	switch p.Phase {
	case "pr_review_confirm":
		if value == "manual" {
			return NextStep{
				Kind:           NextStepOpenModal,
				ModalTitle:     "PR Review",
				ModalLabel:     "貼上 PR URL",
				ModalInputName: "pr_url",
				Pending:        p,
			}, nil
		}
		// "是" with url as value
		ev := TriggerEvent{ChannelID: p.ChannelID, ThreadTS: p.ThreadTS, TriggerTS: p.TriggerTS, UserID: p.UserID}
		return w.validateAndBuild(ctx, ev, value)
	case "pr_review_modal":
		ev := TriggerEvent{ChannelID: p.ChannelID, ThreadTS: p.ThreadTS, TriggerTS: p.TriggerTS, UserID: p.UserID}
		return w.validateAndBuild(ctx, ev, value)
	}
	_ = st
	return NextStep{Kind: NextStepError, ErrorText: "unknown phase"}, nil
}

// BuildJob populates Repo/Branch/CloneURL from head.repo, WorkflowArgs
// with pr_url and pr_number. Goal/OutputRules from cfg.Prompt.PRReview.
func (w *PRReviewWorkflow) BuildJob(ctx context.Context, p *Pending) (*queue.Job, string, error) {
	st := p.State.(*prReviewState)
	cloneURL := fmt.Sprintf("https://github.com/%s.git", st.HeadRepo)

	pc := queue.PromptContext{
		Branch:           st.HeadRef,
		Goal:             w.cfg.Prompt.PRReview.Goal,
		OutputRules:      w.cfg.Prompt.PRReview.OutputRules,
		AllowWorkerRules: w.cfg.Prompt.IsWorkerRulesAllowed(),
	}
	// Common fields (ThreadMessages, Reporter, Channel, Language) populated
	// by the shared prompt-context helper called by all workflows — not
	// repeated here.

	job := &queue.Job{
		ID:            p.RequestID,
		TaskType:      "pr_review",
		ChannelID:     p.ChannelID,
		ThreadTS:      p.ThreadTS,
		UserID:        p.UserID,
		Repo:          st.HeadRepo,
		Branch:        st.HeadRef,
		CloneURL:      cloneURL,
		PromptContext: &pc,
		WorkflowArgs: map[string]string{
			"pr_url":    st.URL,
			"pr_number": strconv.Itoa(st.Number),
		},
	}
	return job, fmt.Sprintf(":eyes: Reviewing `%s/%s#%d`...", st.Owner, st.Repo, st.Number), nil
}
```

- [ ] **Step 4: Run tests**

Run: `(cd app && go test ./workflow/ -run TestPRReviewWorkflow -v)`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add app/workflow/pr_review.go app/workflow/pr_review_test.go
git commit -m "feat(workflow): PRReviewWorkflow Trigger + Selection + BuildJob (A/D paths)"
```

### Task 6.4: `PRReviewWorkflow` parser + `HandleResult`

**Files:**
- Create: `app/workflow/pr_review_parser.go`
- Create: `app/workflow/pr_review_parser_test.go`
- Modify: `app/workflow/pr_review.go`
- Modify: `app/workflow/pr_review_test.go`

- [ ] **Step 1: Write failing parser tests**

`app/workflow/pr_review_parser_test.go`:

```go
package workflow

import "testing"

func TestParseReviewOutput_Posted(t *testing.T) {
	out := "===REVIEW_RESULT===\n" + `{
  "status": "POSTED",
  "summary": "LGTM with minor nits",
  "comments_posted": 3,
  "comments_skipped": 1,
  "severity_summary": "minor"
}`
	r, err := ParseReviewOutput(out)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "POSTED" || r.CommentsPosted != 3 || r.Severity != "minor" {
		t.Errorf("got %+v", r)
	}
}

func TestParseReviewOutput_Skipped(t *testing.T) {
	out := "===REVIEW_RESULT===\n" + `{"status": "SKIPPED", "summary": "lockfile only", "reason": "lockfile_only"}`
	r, err := ParseReviewOutput(out)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "SKIPPED" || r.Reason != "lockfile_only" {
		t.Errorf("got %+v", r)
	}
}

func TestParseReviewOutput_Error(t *testing.T) {
	out := "===REVIEW_RESULT===\n" + `{"status": "ERROR", "error": "422 invalid head sha"}`
	r, err := ParseReviewOutput(out)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "ERROR" || r.Error != "422 invalid head sha" {
		t.Errorf("got %+v", r)
	}
}

func TestParseReviewOutput_UnknownStatus(t *testing.T) {
	_, err := ParseReviewOutput("===REVIEW_RESULT===\n" + `{"status": "NOPE"}`)
	if err == nil {
		t.Error("unknown status must error")
	}
}
```

- [ ] **Step 2: Run to verify fails**

Run: `(cd app && go test ./workflow/ -run TestParseReviewOutput)`
Expected: FAIL.

- [ ] **Step 3: Implement**

`app/workflow/pr_review_parser.go`:

```go
package workflow

import (
	"encoding/json"
	"fmt"
	"strings"
)

const reviewMarker = "===REVIEW_RESULT==="

// ReviewResult is the three-state ===REVIEW_RESULT=== JSON from the
// github-pr-review skill. See 2026-04-21-github-pr-review-skill-design.md
// §Result marker contract.
type ReviewResult struct {
	Status          string `json:"status"` // POSTED | SKIPPED | ERROR
	Summary         string `json:"summary,omitempty"`
	CommentsPosted  int    `json:"comments_posted,omitempty"`
	CommentsSkipped int    `json:"comments_skipped,omitempty"`
	Severity        string `json:"severity_summary,omitempty"` // clean|minor|major
	Reason          string `json:"reason,omitempty"`           // for SKIPPED
	Error           string `json:"error,omitempty"`            // for ERROR
}

func ParseReviewOutput(output string) (ReviewResult, error) {
	output = strings.TrimSpace(output)
	idx := strings.LastIndex(output, reviewMarker)
	if idx == -1 {
		return ReviewResult{}, fmt.Errorf("%s marker not found", reviewMarker)
	}
	body := strings.TrimSpace(output[idx+len(reviewMarker):])
	jsonStr := extractJSON(body)
	var r ReviewResult
	if err := json.Unmarshal([]byte(jsonStr), &r); err != nil {
		return ReviewResult{}, fmt.Errorf("unmarshal: %w", err)
	}
	switch r.Status {
	case "POSTED", "SKIPPED", "ERROR":
		return r, nil
	default:
		return ReviewResult{}, fmt.Errorf("unknown review status %q", r.Status)
	}
}
```

- [ ] **Step 4: Run parser tests**

Run: `(cd app && go test ./workflow/ -run TestParseReviewOutput -v)`
Expected: PASS.

- [ ] **Step 5: Implement `HandleResult`**

Add to `app/workflow/pr_review.go`:

```go
func (w *PRReviewWorkflow) HandleResult(ctx context.Context, job *queue.Job, r *queue.JobResult) error {
	prURL := job.WorkflowArgs["pr_url"]

	if r.Status == "failed" {
		metrics.WorkflowCompletionsTotal.WithLabelValues("pr_review", "error").Inc()
		text := fmt.Sprintf(":x: Review 失敗：%s", r.Error)
		return w.post(job, text)
	}

	if r.Status == "cancelled" {
		metrics.WorkflowCompletionsTotal.WithLabelValues("pr_review", "cancelled").Inc()
		return w.post(job, fmt.Sprintf(":white_check_mark: 已取消。已貼的 comments 保留於 PR %s", prURL))
	}

	parsed, err := ParseReviewOutput(r.RawOutput)
	if err != nil {
		metrics.WorkflowCompletionsTotal.WithLabelValues("pr_review", "parse_failed").Inc()
		w.logger.Warn("pr_review parse failed", "output_head", firstN(r.RawOutput, 2000))
		return w.post(job, fmt.Sprintf(":x: Review 失敗：parse error: %v", err))
	}

	switch parsed.Status {
	case "POSTED":
		metrics.WorkflowCompletionsTotal.WithLabelValues("pr_review", "posted").Inc()
		return w.post(job, fmt.Sprintf(
			":white_check_mark: Review 完成 (severity: %s · %d comments, %d skipped) on %s\n> %s",
			fallback(parsed.Severity, "unknown"), parsed.CommentsPosted, parsed.CommentsSkipped, prURL,
			firstN(parsed.Summary, 200),
		))
	case "SKIPPED":
		metrics.WorkflowCompletionsTotal.WithLabelValues("pr_review", "skipped").Inc()
		return w.post(job, fmt.Sprintf(":information_source: Review 跳過 (%s): %s", parsed.Reason, firstN(parsed.Summary, 200)))
	case "ERROR":
		metrics.WorkflowCompletionsTotal.WithLabelValues("pr_review", "error").Inc()
		return w.post(job, fmt.Sprintf(":x: Review 失敗：%s", parsed.Error))
	}
	return nil
}

func (w *PRReviewWorkflow) post(job *queue.Job, text string) error {
	if job.StatusMsgTS != "" {
		return w.slack.UpdateMessage(job.ChannelID, job.StatusMsgTS, text)
	}
	return w.slack.PostMessage(job.ChannelID, text, job.ThreadTS)
}

func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func fallback(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
```

- [ ] **Step 6: Write HandleResult tests**

Add to `pr_review_test.go`:

```go
func TestPRReviewWorkflow_HandleResult_Posted(t *testing.T) {
	w, slack := newTestPRReviewWorkflow(t)
	job := &queue.Job{ID: "j1", ChannelID: "C1", ThreadTS: "1.0", StatusMsgTS: "s-ts", TaskType: "pr_review",
		WorkflowArgs: map[string]string{"pr_url": "https://github.com/foo/bar/pull/7"}}
	result := &queue.JobResult{
		JobID: "j1", Status: "completed",
		RawOutput: "===REVIEW_RESULT===\n" + `{"status":"POSTED","summary":"ok","comments_posted":2,"severity_summary":"clean"}`,
	}
	if err := w.HandleResult(context.Background(), job, result); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(slack.Posted, " | ")
	if !strings.Contains(joined, "Review 完成") {
		t.Errorf("got: %v", slack.Posted)
	}
}

func TestPRReviewWorkflow_HandleResult_Failed_NoRetry(t *testing.T) {
	w, slack := newTestPRReviewWorkflow(t)
	job := &queue.Job{ID: "j1", ChannelID: "C1", ThreadTS: "1.0", StatusMsgTS: "s-ts", TaskType: "pr_review",
		WorkflowArgs: map[string]string{"pr_url": "https://github.com/foo/bar/pull/7"}}
	result := &queue.JobResult{JobID: "j1", Status: "failed", Error: "timeout"}
	if err := w.HandleResult(context.Background(), job, result); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(slack.Posted, " | ")
	if !strings.Contains(joined, "Review 失敗") {
		t.Errorf("got: %v", slack.Posted)
	}
	// No retry button assertion: fakeSlackPort has no button distinction;
	// PRReviewWorkflow.post never calls PostMessageWithButton. Covered by
	// inspection.
}
```

- [ ] **Step 7: Run tests**

Run: `(cd app && go test ./workflow/ -run TestPRReviewWorkflow_HandleResult -v)`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add app/workflow/pr_review.go app/workflow/pr_review_parser.go app/workflow/pr_review_parser_test.go app/workflow/pr_review_test.go
git commit -m "feat(workflow): PRReviewWorkflow three-state parser + HandleResult"
```

### Task 6.5: Register `PRReviewWorkflow` + `GitHubPR` adapter

**Files:**
- Modify: `cmd/agentdock/app.go`
- Create: `shared/github/pr.go` (or extend existing client)

- [ ] **Step 1: Add `GetPullRequest` method to shared GitHub client**

`shared/github/pr.go`:

```go
package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/Ivantseng123/agentdock/app/workflow"
)

// GetPullRequest fetches a PR via REST. Returns (nil, "404 not found") on
// absent / no-access, etc.
func (c *Client) GetPullRequest(ctx context.Context, owner, repo string, number int) (*workflow.PullRequest, error) {
	u := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls/%d", owner, repo, number)
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("%d %s", resp.StatusCode, string(body))
	}
	var pr workflow.PullRequest
	if err := json.Unmarshal(body, &pr); err != nil {
		return nil, fmt.Errorf("unmarshal pr: %w", err)
	}
	return &pr, nil
}
```

(The name `Client` and fields `c.http`, `c.token` reflect the existing `shared/github` client — adapt if names differ.)

Note the import cycle caveat: `shared/github` importing `app/workflow` violates the module-import direction (`shared` can't import `app`). Resolution: move the `PullRequest` struct out of `app/workflow/ports.go` into `shared/github/pr_types.go`, and have `workflow.GitHubPR.GetPullRequest` return `*github.PullRequest`. Update `ports.go` to import `shared/github` for the type.

- [ ] **Step 2: Fix import direction**

Move `PullRequest` struct from `app/workflow/ports.go` to `shared/github/pr_types.go`. Update `app/workflow/ports.go`:

```go
import ghclient "github.com/Ivantseng123/agentdock/shared/github"

type GitHubPR interface {
	GetPullRequest(ctx context.Context, owner, repo string, number int) (*ghclient.PullRequest, error)
}
```

Update all `app/workflow` references from `*PullRequest` to `*ghclient.PullRequest`.

- [ ] **Step 3: Register in `cmd/agentdock/app.go`**

```go
reg.Register(workflow.NewPRReviewWorkflow(cfg, slackPort, githubClient, repoCache, logger))
```

(Where `githubClient` is the existing `shared/github.Client` — it already satisfies `workflow.GitHubPR` after Step 1.)

- [ ] **Step 4: Module boundary test**

Run: `go test ./test/ -run TestImportDirection -v`
Expected: PASS — no `app → worker` or similar.

- [ ] **Step 5: Full build + test**

Run: `go build ./... && (cd app && go test ./... -race) && (cd shared && go test ./... -race)`
Expected: PASS.

- [ ] **Step 6: Manual smoke (feature flag off + on)**

- With `pr_review.enabled: false` in `app.yaml`, mention `@bot review https://github.com/Ivantseng123/agentdock/pull/100`. Expected: `:warning: PR Review 尚未啟用` message.
- Flip to `pr_review.enabled: true`, restart app. Mention again. Expected: status `:eyes: Reviewing ...`, agent runs, result posted.

- [ ] **Step 7: Commit**

```bash
git add cmd/agentdock/app.go shared/github/pr.go shared/github/pr_types.go app/workflow/ports.go app/workflow/pr_review.go
git commit -m "feat(app): register PRReviewWorkflow + GitHubPR adapter"
```

### Task 6.6: Phase 6 build gate

- [ ] **Step 1: Full suite**

Run: `go build ./... && (cd app && go test ./... -race) && (cd worker && go test ./... -race) && (cd shared && go test ./... -race) && go test ./test/ -race`
Expected: PASS.

- [ ] **Step 2: Phase tag**

```bash
git commit --allow-empty -m "chore: phase 6 complete — PRReviewWorkflow behind pr_review.enabled flag"
```

---

## Phase 7 — D-selector integration

Wire the D-selector (three-button chooser for `@bot` with no verb / no repo-shaped args) end-to-end. The dispatcher already produces the selector `NextStep` (Task 2.7); this phase wires the Slack action handler so clicking a button re-enters the right workflow via the synthetic-trigger path.

### Task 7.1: Action handler for `d_selector`

**Files:**
- Modify: `cmd/agentdock/app.go`

- [ ] **Step 1: Add action routing**

In the existing block-kit action handler inside `cmd/agentdock/app.go`, add a new case for `action.ActionID == "d_selector"`:

```go
case "d_selector":
	// Button value is the workflow type ("issue" / "ask" / "pr_review").
	pending, ok := workflowHandler.LookupPending(action.MessageTS)
	if !ok {
		slackClient.PostMessage(action.ChannelID, ":hourglass: 已超時，請重新觸發。", action.ThreadTS)
		return
	}
	slackClient.UpdateMessage(action.ChannelID, action.MessageTS,
		fmt.Sprintf(":white_check_mark: 已選：%s", labelForWorkflow(action.Value)))

	step, err := dispatcher.HandleSelection(ctx, pending, action.Value)
	if err != nil {
		logger.Error("d_selector routing failed", "error", err)
		return
	}
	executeNextStep(ctx, workflowHandler, step)
```

Helper:

```go
func labelForWorkflow(taskType string) string {
	switch taskType {
	case "issue":
		return "建 Issue"
	case "ask":
		return "問問題"
	case "pr_review":
		return "Review PR"
	default:
		return taskType
	}
}
```

- [ ] **Step 2: Add `LookupPending` to `bot.Workflow`**

`app/bot/workflow.go`:

```go
// LookupPending retrieves a pending entry by selector / modal TS. Used by
// action handlers to route button clicks and modal submits back to the
// dispatcher.
func (w *Workflow) LookupPending(selectorTS string) (*workflow.Pending, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	p, ok := w.pending[selectorTS]
	return p, ok
}
```

- [ ] **Step 3: Manual smoke**

Mention `@bot` with no args in a staging thread. Expected: three-button selector appears. Click "建 Issue" → Issue wizard starts (repo selector). Click "問問題" → Ask wizard starts. With `pr_review.enabled: true`, click "Review PR" → thread scan / modal.

- [ ] **Step 4: Commit**

```bash
git add cmd/agentdock/app.go app/bot/workflow.go
git commit -m "feat(app): wire d_selector action handler to dispatcher.HandleSelection"
```

### Task 7.2: Verify legacy `@bot <repo>` still routes to Issue

**Files:** (verification only)

- [ ] **Step 1: Dispatcher dispatch test**

Add to `app/workflow/dispatcher_test.go`:

```go
func TestDispatcher_BareRepoAtBranch_RoutesToIssue(t *testing.T) {
	r := NewRegistry()
	r.Register(&fakeWorkflow{typ: "issue"})
	d := NewDispatcher(r, newFakeSlackPort(), slog.Default())
	_, step, _ := d.Dispatch(context.Background(), TriggerEvent{Text: "foo/bar@main"})
	if step.Kind != NextStepSubmit {
		t.Errorf("expected Issue route (NextStepSubmit from fake), got %v", step.Kind)
	}
}
```

- [ ] **Step 2: Run test**

Run: `(cd app && go test ./workflow/ -run TestDispatcher_BareRepoAtBranch -v)`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add app/workflow/dispatcher_test.go
git commit -m "test(workflow): confirm legacy @bot foo/bar@branch routes to Issue"
```

### Task 7.3: Phase 7 build gate

- [ ] **Step 1: Full suite**

Run: `go build ./... && (cd app && go test ./... -race) && (cd worker && go test ./... -race) && (cd shared && go test ./... -race) && go test ./test/ -race`
Expected: PASS.

- [ ] **Step 2: Commit phase tag**

```bash
git commit --allow-empty -m "chore: phase 7 complete — D-selector wired end-to-end"
```

---

## Phase 8 — End-to-end smoke

### Task 8.1: Run all three workflows against staging

**Files:** (manual testing + lightweight fixture)

- [ ] **Step 1: Issue flow**

In staging Slack, mention `@bot Ivantseng123/agentdock`. Walk the wizard: repo → branch → description modal → submit. Expected: GitHub issue created; URL posted to thread.

- [ ] **Step 2: Ask flow**

Mention `@bot ask what's the config schema after v2?`. Click `不用`. Expected: `:thinking_face: 思考中...` → bot answer posted (agent reads thread + nothing else).

Mention `@bot ask`, click `附加`, select `Ivantseng123/agentdock`. Expected: repo clone + agent answer using codebase.

- [ ] **Step 3: PR Review flow (if skill ready)**

Ensure `pr_review.enabled: true` in `app.yaml`. In a thread containing `https://github.com/Ivantseng123/agentdock/pull/117`, mention `@bot review`. Expected: auto-detect prompt `:eyes: 找到 .../pull/117, review？` → click `是` → `:eyes: Reviewing ...` → POSTED/SKIPPED/ERROR result on GitHub + Slack summary.

- [ ] **Step 4: D-selector flow**

Mention `@bot` (no args). Expected: three-button selector. Click each button and verify it routes correctly.

- [ ] **Step 5: Unknown-verb flow**

Mention `@bot reveiw pr-url-here`. Expected: `:warning: 不認得 reveiw，選一個：` + three buttons.

- [ ] **Step 6: Metrics verification**

Curl the metrics endpoint:

```bash
curl -s http://localhost:8080/metrics | grep -E 'workflow_completions_total|workflow_retry_total'
```

Expected: counters for `issue`, `ask`, `pr_review` reflecting the smoke runs above.

- [ ] **Step 7: Legacy compat check**

Confirm `@bot Ivantseng123/agentdock@main` still works as Issue-with-branch (no selector; wizard short-circuits on both repo and branch).

### Task 8.2: Final commit + release notes prep

- [ ] **Step 1: Release-notes bullet list**

Write a short release-notes snippet (not committed):

```
- New: `@bot ask <question>` — bot answers in Slack without filing an issue.
- New: `@bot review <PR URL>` — bot reviews a GitHub PR with line-level
  comments + summary (requires `pr_review.enabled: true` + the
  `github-pr-review` skill on workers).
- New: `@bot` with no args shows a workflow selector.
- Config: `prompt.goal` / `prompt.output_rules` evolved to
  `prompt.{issue,ask,pr_review}.*`; flat form is aliased into
  `prompt.issue.*` for backwards compat.
- Metrics: `IssueCreated/Rejected/RetryTotal` replaced with
  `WorkflowCompletionsTotal{workflow, status}` and
  `WorkflowRetryTotal{workflow, outcome}`. Grafana dashboards need a rebuild.
```

- [ ] **Step 2: Verify clean tree + final phase tag**

Run: `git status` (expect clean) then:

```bash
git commit --allow-empty -m "chore: phase 8 complete — workflow-types refactor ready for release"
```

---

## Self-review checklist

Before handing this plan off, confirm:

- [ ] Every phase has a build gate (run all module tests).
- [ ] Every TDD step has actual Go code, not a description.
- [ ] Every `Commit` step has the full `git add` path list + a concrete message.
- [ ] No "TBD" / "similar to Task N" / "implement later" anywhere.
- [ ] File paths match the File Structure map at top.
- [ ] Type names and method signatures match across tasks (e.g. `BuildJob(ctx, p) (*Job, string, error)` used consistently).
- [ ] Every spec Goal has a Phase:
  - G1 (three workflow types) — Phases 2, 5, 6
  - G2 (polymorphic dispatch) — Phases 1, 2 (dispatcher), 7 (D-selector)
  - G3 (TaskType app-side only) — Phase 3 (ResultListener dispatches)
  - G4 (backwards-compat triggers) — Phase 7 Task 7.2 test
  - G5 (D-selector) — Phase 7
  - G6 (worker workflow-agnostic) — Phase 4
  - G7 (PR Review via skill) — Phase 6 uses existing `agents/skills/github-pr-review/`
  - G8 (nested config) — Phase 2 Task 2.2
  - G9 (unified metrics) — Phase 3 Task 3.4

---

## Fallback decisions

Track here if any Phase 4 spike fails. Leave untouched if all green.

| Agent runner | Spike result | Fallback applied |
|---|---|---|
| claude    | PASS / FAIL | |
| codex     | PASS / FAIL | |
| gemini    | PASS / FAIL | |
| opencode  | PASS / FAIL | |

---

**Plan complete.** Execute via subagent-driven-development (recommended) or executing-plans.
