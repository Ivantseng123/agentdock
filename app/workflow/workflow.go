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
	"sync/atomic"

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
	BusyHint    string // populated by bot.Workflow.submit() when verdict is BusyEnqueueOK; app.submitJob appends to statusText

	// RepoAckTS is set by the bot layer after the user picks a repo — it
	// points at the "✅ <repo>" acknowledgement message. HandleBackToRepo
	// uses it to delete the abandoned pick so clicking "重新選 repo" leaves
	// a single breadcrumb ("已返回 repo 選擇") instead of a growing stack of
	// rejected repo choices. Zero value = no pending repo ack.
	RepoAckTS string

	// invalidated is set by HandleBackToRepo when the user abandons this pending
	// generation. An in-flight repo-prep goroutine that completes after the
	// back-button was clicked must observe this flag and skip posting the
	// selector/modal/submit — otherwise a stale selector races with the fresh
	// repo-picker posted by back-to-repo. Accessed concurrently by the bot
	// goroutine (Invalidate) and the executeStep goroutine (IsInvalidated), so
	// use sync/atomic.Bool — a bare bool would be a data race.
	invalidated atomic.Bool
}

// Invalidate marks this pending generation as abandoned. Subsequent
// IsInvalidated calls return true. Safe to call from any goroutine.
func (p *Pending) Invalidate() {
	if p == nil {
		return
	}
	p.invalidated.Store(true)
}

// IsInvalidated reports whether Invalidate has been called on this pending.
// Safe to call from any goroutine.
func (p *Pending) IsInvalidated() bool {
	if p == nil {
		return false
	}
	return p.invalidated.Load()
}

// NextStepKind enumerates the actions a workflow's Trigger/Selection can
// request from the dispatcher. The dispatcher executes these against the
// SlackPort so workflows stay Slack-agnostic.
type NextStepKind int

const (
	NextStepSelector  NextStepKind = iota // unified selector (button / static_select / external)
	NextStepOpenModal                     // open a text-input modal
	NextStepSubmit                        // submit the completed Pending to the queue
	NextStepError                         // post an error message
	NextStepNoop                          // workflow handled everything in-place (rare)
	NextStepCancel                        // user aborted mid-flow; dispatcher clears dedup and stops
)

// NextStep is a discriminated union of what the dispatcher should do next.
// Only the field matching Kind is read.
type NextStep struct {
	Kind NextStepKind

	// Selector — Kind == NextStepSelector. The adapter chooses the Slack
	// rendering (button row, static_select, external search) based on the
	// number of options so callers never have to worry about the 25-button
	// cap on Slack actions blocks.
	Selector *SelectorSpec

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

// SelectorOption is one choice in a selector.
type SelectorOption struct {
	Label string
	Value string
}

// SelectorSpec describes what the user should pick from. The adapter decides
// the concrete Slack rendering:
//   - Searchable=true                          → external_select (type-ahead)
//   - Searchable=false, few options            → button row
//   - Searchable=false, many options           → static_select dropdown
//
// ActionID is the Slack action_id shared by every option (the rendered
// payload delivers the pick via action.Value for buttons or
// action.SelectedOption.Value for dropdowns — app.go's router already
// accepts either).
//
// BackActionID / CancelActionID describe optional extra buttons (e.g.
// "← 重新選 repo", "取消") that render alongside the options regardless of
// the chosen mode.
type SelectorSpec struct {
	Prompt   string
	ActionID string
	Options  []SelectorOption

	// External-search mode. When true, Options may be empty — the user
	// types a query and app/app.go's BlockSuggestion handler supplies
	// live results.
	Searchable  bool
	Placeholder string

	// Optional back/cancel buttons. Empty ActionID = button not rendered.
	BackActionID   string
	BackLabel      string
	CancelActionID string
	CancelLabel    string
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
	// decisions, and dedup-clear. state.Job provides the dispatched Job; state
	// also carries WorkerID/AgentStatus for diagnostics.
	HandleResult(ctx context.Context, state *queue.JobState, result *queue.JobResult) error
}
