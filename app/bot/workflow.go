package bot

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/Ivantseng123/agentdock/app/config"
	ghclient "github.com/Ivantseng123/agentdock/shared/github"
	slackclient "github.com/Ivantseng123/agentdock/app/slack"
	"github.com/Ivantseng123/agentdock/app/workflow"
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
) *Workflow {
	return &Workflow{
		cfg:           cfg,
		dispatcher:    dispatcher,
		slack:         slack,
		repoDiscovery: repoDiscovery,
		logger:        logger,
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

// HandleSelection handles a button click on a selector message. It looks up
// the pending state by selectorMsgTS, removes it from the map, and
// calls dispatcher.HandleSelection.
func (w *Workflow) HandleSelection(channelID, actionID, value, selectorMsgTS string) {
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
	// Update the selector message to show the selection.
	_ = w.slack.UpdateMessage(channelID, selectorMsgTS,
		":white_check_mark: "+value)
	step, err := w.dispatcher.HandleSelection(ctx, pending, value)
	if err != nil {
		w.logger.Error("HandleSelection dispatch failed", "phase", "失敗", "error", err)
		return
	}
	w.executeStep(ctx, pending, step, "")
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

	// Set phase to description_modal so IssueWorkflow.Selection routes
	// the value through the extra-desc branch.
	pending.Phase = "description_modal"

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
func (w *Workflow) executeStep(ctx context.Context, pending *workflow.Pending, step workflow.NextStep, triggerID string) {
	if pending == nil {
		return
	}
	switch step.Kind {
	case workflow.NextStepPostSelector:
		labels := make([]string, len(step.SelectorActions))
		for i, a := range step.SelectorActions {
			labels[i] = a.Label
		}
		var selectorTS string
		var err error
		if step.SelectorBack != "" {
			selectorTS, err = w.slack.PostSelectorWithBack(
				pending.ChannelID,
				step.SelectorPrompt,
				actionPrefix(step.SelectorActions),
				labels,
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
			if w.onSubmit != nil {
				w.onSubmit(ctx, pending)
			}
			return
		}
		if err := w.slack.OpenTextInputModal(
			tid,
			step.ModalTitle,
			step.ModalLabel,
			step.ModalInputName,
			step.ModalMetadata,
		); err != nil {
			w.logger.Error("OpenTextInputModal failed", "phase", "失敗", "error", err)
			// Fall back: submit without extra description.
			if w.onSubmit != nil {
				w.onSubmit(ctx, pending)
			}
		}
		// Pending is already in the map under SelectorTS (set by HandleDescriptionAction's
		// caller); the modal submit carries selectorMsgTS as private_metadata so we find it.

	case workflow.NextStepSubmit:
		p := step.Pending
		if p == nil {
			p = pending
		}
		if w.onSubmit != nil {
			w.onSubmit(ctx, p)
		} else {
			w.logger.Warn("NextStepSubmit but no onSubmit hook set", "phase", "失敗")
		}

	case workflow.NextStepError:
		_ = w.slack.PostMessage(pending.ChannelID, ":x: "+step.ErrorText, pending.ThreadTS)
		if w.handler != nil {
			w.handler.ClearThreadDedup(pending.ChannelID, pending.ThreadTS)
		}

	case workflow.NextStepNoop:
		// Nothing to do.

	default:
		w.logger.Warn("executeStep: unknown NextStepKind", "phase", "失敗", "kind", step.Kind)
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
