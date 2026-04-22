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
	NextStepNoop                // used when the workflow handled everything in-place (rare)
	NextStepPostExternalSelector // external searchable selector (no configured repos)
	NextStepCancel               // user aborted mid-flow; dispatcher clears dedup and stops
)

// NextStep is a discriminated union of what the dispatcher should do next.
// Only the field matching Kind is read.
type NextStep struct {
	Kind NextStepKind

	// PostSelector — Kind == NextStepPostSelector
	SelectorPrompt  string
	SelectorActions []SelectorAction
	SelectorBack    string // optional "back" action ID; empty = no back button

	// PostExternalSelector — Kind == NextStepPostExternalSelector
	SelectorActionID       string // Slack action_id for the external select
	SelectorPlaceholder    string // placeholder text shown in the search box
	SelectorCancelActionID string // optional cancel button action_id; empty = no cancel
	SelectorCancelLabel    string // cancel button label (e.g. "取消")

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
	// decisions, and dedup-clear. state.Job provides the dispatched Job; state
	// also carries WorkerID/AgentStatus for diagnostics.
	HandleResult(ctx context.Context, state *queue.JobState, result *queue.JobResult) error
}
