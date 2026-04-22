package bot

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/Ivantseng123/agentdock/app/config"
	slackclient "github.com/Ivantseng123/agentdock/app/slack"
	"github.com/Ivantseng123/agentdock/app/workflow"
	ghclient "github.com/Ivantseng123/agentdock/shared/github"
	"github.com/Ivantseng123/agentdock/shared/queue"
)

const pendingTimeout = 1 * time.Minute

// Workflow is the thin Slack-side handler. Real triage logic lives in
// app/workflow workflows reached through the dispatcher.
type Workflow struct {
	cfg           *config.Config
	dispatcher    *workflow.Dispatcher
	slack         workflow.SlackPort
	handler       *slackclient.Handler
	repoDiscovery *ghclient.RepoDiscovery
	logger        *slog.Logger
	availability  queue.WorkerAvailability

	mu        sync.Mutex
	pending   map[string]*workflow.Pending
	autoBound map[string]bool

	// onSubmit is called when a workflow returns NextStepSubmit.
	// Filled by app/app.go via SetSubmitHook.
	onSubmit func(ctx context.Context, p *workflow.Pending)
}

// NewWorkflow constructs the thin shim. cfg is used for channel-binding
// checks; repoDiscovery is kept for HandleRepoSuggestion.
func NewWorkflow(
	cfg *config.Config,
	dispatcher *workflow.Dispatcher,
	slack workflow.SlackPort,
	repoDiscovery *ghclient.RepoDiscovery,
	logger *slog.Logger,
	availability queue.WorkerAvailability,
) *Workflow {
	return &Workflow{
		cfg:           cfg,
		dispatcher:    dispatcher,
		slack:         slack,
		repoDiscovery: repoDiscovery,
		logger:        logger,
		availability:  availability,
		pending:       make(map[string]*workflow.Pending),
		autoBound:     make(map[string]bool),
	}
}

// SetHandler registers the socketmode Handler so ClearThreadDedup can be
// called when the workflow finishes.
func (w *Workflow) SetHandler(h *slackclient.Handler) { w.handler = h }

// SetSubmitHook installs the callback invoked when a workflow signals
// NextStepSubmit. app/app.go wires this to the queue-submission closure.
func (w *Workflow) SetSubmitHook(f func(ctx context.Context, p *workflow.Pending)) {
	w.onSubmit = f
}

// RegisterChannel marks a channel as auto-bound (bot joined via MemberJoined).
func (w *Workflow) RegisterChannel(channelID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.autoBound[channelID] = true
}

// UnregisterChannel removes a channel from the auto-bound set.
func (w *Workflow) UnregisterChannel(channelID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.autoBound, channelID)
}

// HandleTrigger is the entry point from the socketmode event loop. It
// pre-filters bare @bot (no thread) and unbound channels, then delegates
// to the dispatcher.
func (w *Workflow) HandleTrigger(event slackclient.TriggerEvent) {
	if event.ThreadTS == "" {
		_ = w.slack.PostMessage(event.ChannelID, ":warning: 請在對話串中使用此指令。", "")
		return
	}

	// Channel-binding check (mirrors old behaviour).
	if _, ok := w.cfg.Channels[event.ChannelID]; !ok {
		w.mu.Lock()
		isBound := w.autoBound[event.ChannelID]
		w.mu.Unlock()
		if !isBound && !w.cfg.AutoBind {
			return
		}
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
	w.executeStep(ctx, pending, step, "")
}

// HandleRepoSuggestion returns type-ahead options for the external repo
// search selector. Delegates to repoDiscovery.SearchRepos.
func (w *Workflow) HandleRepoSuggestion(query string) []string {
	if w.repoDiscovery == nil {
		return nil
	}
	repos, err := w.repoDiscovery.SearchRepos(context.Background(), query)
	if err != nil {
		slog.Warn("Repo 搜尋失敗", "phase", "失敗", "error", err)
		return nil
	}
	return repos
}

// dSelectorLabel maps task-type values to friendly display labels used when
// acknowledging a D-selector click.
var dSelectorLabel = map[string]string{
	"issue":     "📝 建 Issue",
	"ask":       "❓ 問問題",
	"pr_review": "🔍 Review PR",
}

// HandleSelection handles a button click or external-selector pick on a
// selector message. It looks up the pending state by selectorMsgTS and
// calls dispatcher.HandleSelection. triggerID is forwarded to executeStep
// so the dispatched workflow can open a modal in response (e.g.
// pr_review_confirm → "改貼 URL"); pass "" when none.
//
// The pending map entry is removed only if the resulting step doesn't
// keep it alive under the same key — NextStepOpenModal carries the key
// forward via private_metadata so the modal submit can find the same
// pending; NextStepPostSelector re-keys it under a new selectorTS inside
// executeStep via storePending.
func (w *Workflow) HandleSelection(channelID, actionID, value, selectorMsgTS, triggerID string) {
	w.mu.Lock()
	pending, ok := w.pending[selectorMsgTS]
	w.mu.Unlock()
	if !ok {
		return
	}

	ctx := context.Background()
	// Update the selector message to show the selection.
	// For D-selector clicks use a friendly label; other selectors show the raw value.
	ackLabel := value
	if pending.Phase == "d_selector" {
		if label, ok := dSelectorLabel[value]; ok {
			ackLabel = label
		}
	}
	_ = w.slack.UpdateMessage(channelID, selectorMsgTS,
		":white_check_mark: "+ackLabel)
	step, err := w.dispatcher.HandleSelection(ctx, pending, value)
	if err != nil {
		w.logger.Error("HandleSelection dispatch failed", "phase", "失敗", "error", err)
		w.mu.Lock()
		delete(w.pending, selectorMsgTS)
		w.mu.Unlock()
		return
	}
	if step.Kind != workflow.NextStepOpenModal {
		w.mu.Lock()
		delete(w.pending, selectorMsgTS)
		w.mu.Unlock()
	}
	w.executeStep(ctx, pending, step, triggerID)
}

// HandleDescriptionAction handles the "補充說明" / "跳過" button click on the
// description-prompt selector.
func (w *Workflow) HandleDescriptionAction(channelID, value, selectorMsgTS, triggerID string) {
	w.mu.Lock()
	pending, ok := w.pending[selectorMsgTS]
	if !ok {
		w.mu.Unlock()
		return
	}
	if value == "跳過" {
		delete(w.pending, selectorMsgTS)
		w.mu.Unlock()
		_ = w.slack.UpdateMessage(channelID, selectorMsgTS, ":fast_forward: 跳過補充說明")
		ctx := context.Background()
		step, err := w.dispatcher.HandleSelection(ctx, pending, value)
		if err != nil {
			w.logger.Error("HandleDescriptionAction dispatch failed", "phase", "失敗", "error", err)
			return
		}
		w.executeStep(ctx, pending, step, triggerID)
		return
	}
	w.mu.Unlock()

	ctx := context.Background()
	step, err := w.dispatcher.HandleSelection(ctx, pending, value)
	if err != nil {
		w.logger.Error("HandleDescriptionAction dispatch failed", "phase", "失敗", "error", err)
		return
	}
	w.executeStep(ctx, pending, step, triggerID)
}

// HandleDescriptionSubmit handles a modal submission (or close) carrying
// the extra description text.
func (w *Workflow) HandleDescriptionSubmit(selectorMsgTS, extraText string) {
	w.mu.Lock()
	pending, ok := w.pending[selectorMsgTS]
	if ok {
		delete(w.pending, selectorMsgTS)
	}
	w.mu.Unlock()
	if !ok {
		return
	}

	if extraText != "" {
		_ = w.slack.UpdateMessage(pending.ChannelID, selectorMsgTS,
			":memo: 補充說明: "+extraText)
	}

	// Phase is now owned by the workflow itself (issue → "description_modal",
	// pr_review → "pr_review_modal"). We just forward the submitted text.
	ctx := context.Background()
	step, err := w.dispatcher.HandleSelection(ctx, pending, extraText)
	if err != nil {
		w.logger.Error("HandleDescriptionSubmit dispatch failed", "phase", "失敗", "error", err)
		return
	}
	w.executeStep(ctx, pending, step, "")
}

// HandleBackToRepo handles the "← 重新選 repo" back button.
func (w *Workflow) HandleBackToRepo(channelID, selectorMsgTS string) {
	w.mu.Lock()
	pending, ok := w.pending[selectorMsgTS]
	if ok {
		delete(w.pending, selectorMsgTS)
	}
	w.mu.Unlock()
	if !ok {
		return
	}

	ctx := context.Background()
	step, err := w.dispatcher.HandleSelection(ctx, pending, "back_to_repo")
	if err != nil {
		w.logger.Error("HandleBackToRepo dispatch failed", "phase", "失敗", "error", err)
		return
	}
	// Update old selector to show we navigated back.
	_ = w.slack.UpdateMessage(channelID, selectorMsgTS, ":leftwards_arrow_with_hook: 已返回 repo 選擇")
	w.executeStep(ctx, pending, step, "")
}

// executeStep applies a NextStep from a workflow: posts a selector, opens a
// modal, triggers job submission, or renders an error. The triggerID is needed
// for NextStepOpenModal; pass "" when not available.
//
// When step.Pending is set the workflow is signalling "use this pending for
// the next round" — always prefer it over the caller's pending, otherwise a
// fresh workflow created inside a d_selector click (e.g. AskWorkflow.Trigger)
// has its new state thrown away and subsequent clicks route to the stale
// d_selector pending.
func (w *Workflow) executeStep(ctx context.Context, pending *workflow.Pending, step workflow.NextStep, triggerID string) {
	if step.Pending != nil {
		pending = step.Pending
	}
	if pending == nil {
		return
	}
	switch step.Kind {
	case workflow.NextStepPostSelector:
		labels := make([]string, len(step.SelectorActions))
		values := make([]string, len(step.SelectorActions))
		for i, a := range step.SelectorActions {
			labels[i] = a.Label
			values[i] = a.Value
		}
		var selectorTS string
		var err error
		if step.SelectorBack != "" {
			selectorTS, err = w.slack.PostSelectorWithBack(
				pending.ChannelID,
				step.SelectorPrompt,
				actionPrefix(step.SelectorActions),
				labels,
				values,
				pending.ThreadTS,
				step.SelectorBack,
				"← 重新選 repo",
			)
		} else {
			selectorTS, err = w.slack.PostSelector(
				pending.ChannelID,
				step.SelectorPrompt,
				actionPrefix(step.SelectorActions),
				labels,
				values,
				pending.ThreadTS,
			)
		}
		if err != nil {
			w.logger.Error("PostSelector failed", "phase", "失敗", "error", err)
			_ = w.slack.PostMessage(pending.ChannelID, ":x: 無法顯示選單，請重試", pending.ThreadTS)
			if w.handler != nil {
				w.handler.ClearThreadDedup(pending.ChannelID, pending.ThreadTS)
			}
			return
		}
		w.storePending(selectorTS, pending)

	case workflow.NextStepPostExternalSelector:
		selectorTS, err := w.slack.PostExternalSelector(
			pending.ChannelID,
			step.SelectorPrompt,
			step.SelectorActionID,
			step.SelectorPlaceholder,
			pending.ThreadTS,
			step.SelectorCancelActionID,
			step.SelectorCancelLabel,
		)
		if err != nil {
			w.logger.Error("PostExternalSelector failed", "phase", "失敗", "error", err)
			_ = w.slack.PostMessage(pending.ChannelID, ":x: 無法顯示搜尋選單，請重試", pending.ThreadTS)
			if w.handler != nil {
				w.handler.ClearThreadDedup(pending.ChannelID, pending.ThreadTS)
			}
			return
		}
		w.storePending(selectorTS, pending)

	case workflow.NextStepOpenModal:
		tid := triggerID
		if tid == "" {
			tid = step.ModalTriggerID
		}
		if tid == "" {
			// No trigger ID available — fall through to submit without description.
			w.logger.Warn("OpenModal requested but no triggerID", "phase", "失敗")
			w.submit(ctx, pending)
			return
		}
		// Modal-first flows (PR Review D-path when thread has no URL) reach
		// this case with no prior selector, so ModalMetadata is empty and
		// pending was never stored. Synthesize a key from RequestID and store
		// pending so HandleDescriptionSubmit can resolve it from PrivateMetadata.
		meta := step.ModalMetadata
		if meta == "" {
			meta = "modal-" + pending.RequestID
			w.storePending(meta, pending)
		}
		if err := w.slack.OpenTextInputModal(
			tid,
			step.ModalTitle,
			step.ModalLabel,
			step.ModalInputName,
			meta,
		); err != nil {
			w.logger.Error("OpenTextInputModal failed", "phase", "失敗", "error", err)
			// Fall back: submit without extra description.
			// Consume the pending entry so the timeout goroutine doesn't fire
			// a spurious ":hourglass: 選擇已超時" after the job is already running.
			selectorTS := pending.SelectorTS
			if selectorTS != "" {
				w.mu.Lock()
				delete(w.pending, selectorTS)
				w.mu.Unlock()
			}
			w.submit(ctx, pending)
		}
		// Pending is now in the map under meta (either SelectorTS from a
		// prior selector, or the synthesised "modal-<reqID>" key above); the
		// modal submit carries meta as private_metadata so we find it.

	case workflow.NextStepSubmit:
		p := step.Pending
		if p == nil {
			p = pending
		}
		w.submit(ctx, p)

	case workflow.NextStepError:
		_ = w.slack.PostMessage(pending.ChannelID, ":x: "+step.ErrorText, pending.ThreadTS)
		if w.handler != nil {
			w.handler.ClearThreadDedup(pending.ChannelID, pending.ThreadTS)
		}

	case workflow.NextStepNoop:
		// Nothing to do.

	case workflow.NextStepCancel:
		// User aborted mid-flow. The selector ack (":white_check_mark: 取消")
		// has already been posted by HandleSelection; just clear dedup so the
		// same thread can accept a fresh @bot trigger.
		if w.handler != nil {
			w.handler.ClearThreadDedup(pending.ChannelID, pending.ThreadTS)
		}

	default:
		w.logger.Warn("executeStep: unknown NextStepKind", "phase", "失敗", "kind", step.Kind)
	}
}

// submit is the single chokepoint for sending a Pending to the queue-submission
// closure. Consolidates the three former `if w.onSubmit != nil { w.onSubmit(...) }`
// call sites in executeStep so pre-submit checks (like the worker-availability
// hard check below) only need to land in one place.
func (w *Workflow) submit(ctx context.Context, p *workflow.Pending) {
	if w.availability != nil {
		verdict := w.availability.CheckHard(ctx)
		switch verdict.Kind {
		case queue.VerdictNoWorkers:
			_ = w.slack.PostMessage(p.ChannelID,
				RenderHardReject(verdict), p.ThreadTS)
			if w.handler != nil {
				w.handler.ClearThreadDedup(p.ChannelID, p.ThreadTS)
			}
			return
		case queue.VerdictBusyEnqueueOK:
			p.BusyHint = RenderBusyHint(verdict)
		case queue.VerdictOK:
			// continue
		}
	}
	if w.onSubmit != nil {
		w.onSubmit(ctx, p)
	} else {
		w.logger.Warn("submit but no onSubmit hook set", "phase", "失敗")
	}
}

// storePending registers a pending workflow state under selectorTS and starts
// a goroutine that evicts the entry after pendingTimeout.
func (w *Workflow) storePending(selectorTS string, p *workflow.Pending) {
	p.SelectorTS = selectorTS
	w.mu.Lock()
	w.pending[selectorTS] = p
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
			_ = w.slack.UpdateMessage(p.ChannelID, selectorTS, ":hourglass: 已超時")
			_ = w.slack.PostMessage(p.ChannelID, ":hourglass: 選擇已超時，請重新觸發。", p.ThreadTS)
			if w.handler != nil {
				w.handler.ClearThreadDedup(p.ChannelID, p.ThreadTS)
			}
		}
	}()
}

// actionPrefix returns the ActionID of the first action, used as the Slack
// action prefix when posting a selector. Returns "" for empty slices.
func actionPrefix(actions []workflow.SelectorAction) string {
	if len(actions) == 0 {
		return ""
	}
	return actions[0].ActionID
}
