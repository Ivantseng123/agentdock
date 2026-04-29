package bot

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/Ivantseng123/agentdock/app/config"
	slackclient "github.com/Ivantseng123/agentdock/app/slack"
	"github.com/Ivantseng123/agentdock/app/workflow"
	ghclient "github.com/Ivantseng123/agentdock/shared/github"
	"github.com/Ivantseng123/agentdock/shared/queue"
)

// defaultPendingTimeout is the idle window before a wizard pending is
// evicted and the thread is condensed to the "selection timed out"
// breadcrumb. Per-Workflow field so tests can shorten it without racing on
// package state.
const defaultPendingTimeout = 1 * time.Minute

// Workflow is the thin Slack-side handler. Real triage logic lives in
// app/workflow workflows reached through the dispatcher.
type Workflow struct {
	cfg           *config.Config
	dispatcher    *workflow.Dispatcher
	slack         workflow.SlackPort
	handler       *slackclient.Handler
	repoDiscovery *ghclient.RepoDiscovery
	repoCache     *ghclient.RepoCache
	logger        *slog.Logger
	availability  queue.WorkerAvailability

	mu        sync.Mutex
	pending   map[string]*workflow.Pending
	autoBound map[string]bool

	// pendingTimeout is how long storePending keeps a pending alive before
	// the cleanup goroutine condenses the thread. Exposed as a field so
	// tests can override it without racing on package-level state.
	pendingTimeout time.Duration

	// onSubmit is called when a workflow returns NextStepSubmit.
	// Filled by app/app.go via SetSubmitHook.
	onSubmit func(ctx context.Context, p *workflow.Pending)
}

// NewWorkflow constructs the thin shim. cfg is used for channel-binding
// checks; repoDiscovery is kept for HandleRepoSuggestion; repoCache is kept
// for HandleBranchSuggestion (the type-ahead that fires when a repo has
// more than selectorStaticSelectMax branches — see issue #153).
func NewWorkflow(
	cfg *config.Config,
	dispatcher *workflow.Dispatcher,
	slack workflow.SlackPort,
	repoDiscovery *ghclient.RepoDiscovery,
	repoCache *ghclient.RepoCache,
	logger *slog.Logger,
	availability queue.WorkerAvailability,
) *Workflow {
	return &Workflow{
		cfg:            cfg,
		dispatcher:     dispatcher,
		slack:          slack,
		repoDiscovery:  repoDiscovery,
		repoCache:      repoCache,
		logger:         logger,
		availability:   availability,
		pending:        make(map[string]*workflow.Pending),
		autoBound:      make(map[string]bool),
		pendingTimeout: defaultPendingTimeout,
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

	// Soft availability check — informational only; do NOT block dispatch.
	// The hard check inside submit() gates actual queue submission.
	if w.availability != nil {
		verdict := w.availability.CheckSoft(context.Background())
		if msg := RenderSoftWarn(verdict); msg != "" {
			_ = w.slack.PostMessage(event.ChannelID, msg, event.ThreadTS)
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

// HandleRefRepoSuggestion is the ref-pick variant of HandleRepoSuggestion:
// runs the same repoDiscovery search, then filters out repos returned by
// the pending state's RefExclusionReader (primary + already-picked refs).
// When the state doesn't implement RefExclusionReader, falls through with
// no filtering — defensive, but workflows that use ask_ref should always
// implement the interface.
func (w *Workflow) HandleRefRepoSuggestion(selectorMsgTS, query string) []string {
	if w.repoDiscovery == nil {
		return nil
	}
	repos, err := w.repoDiscovery.SearchRepos(context.Background(), query)
	if err != nil {
		slog.Warn("Ref repo 搜尋失敗", "phase", "失敗", "error", err)
		return nil
	}
	w.mu.Lock()
	pending, ok := w.pending[selectorMsgTS]
	w.mu.Unlock()
	if !ok || pending == nil {
		return repos
	}
	excluder, ok := pending.State.(workflow.RefExclusionReader)
	if !ok {
		return repos
	}
	excluded := make(map[string]bool)
	for _, r := range excluder.RefExclusions() {
		excluded[r] = true
	}
	out := repos[:0:len(repos)]
	for _, r := range repos {
		if !excluded[r] {
			out = append(out, r)
		}
	}
	return out
}

// maxBranchSuggestions caps the type-ahead result list. External_select is
// also bounded at 100 options per Slack's own API limit.
const maxBranchSuggestions = 100

// HandleBranchSuggestion returns type-ahead branch options for the branch
// selector, which auto-upgrades to external_select when a repo has more
// than selectorStaticSelectMax branches (issue #153).
//
// selectorMsgTS is used to look up the pending Issue/Ask state; the
// SelectedRepo on that state determines which repo's branches to filter.
// The filter is a case-insensitive substring match; an empty query returns
// the first maxBranchSuggestions branches.
func (w *Workflow) HandleBranchSuggestion(selectorMsgTS, query string) []string {
	if w.repoCache == nil {
		return nil
	}
	w.mu.Lock()
	pending, ok := w.pending[selectorMsgTS]
	w.mu.Unlock()
	if !ok || pending == nil {
		return nil
	}
	repo := selectedRepoFromState(pending.State)
	if repo == "" {
		w.logger.Warn("HandleBranchSuggestion: no SelectedRepo on pending", "phase", "跳過", "request_id", pending.RequestID)
		return nil
	}
	// Use LocalPath (not EnsureRepo) on this hot path. EnsureRepo takes
	// rc.mu and — past the maxAge window — runs `git fetch --all --prune`
	// synchronously; both blow Slack's ~3s BlockSuggestion deadline and
	// serialise concurrent flows. The branch selector only renders AFTER
	// the workflow prep step already called EnsureRepo, so by the time
	// type-ahead fires, a bare clone is on disk and fresh enough. The
	// (rare) path-missing case is treated as a race: nil + warn.
	repoPath, exists := w.repoCache.LocalPath(repo)
	if !exists {
		w.logger.Warn("Branch 搜尋: 本地 clone 不存在，跳過建議", "phase", "跳過", "repo", repo, "path", repoPath)
		return nil
	}
	branches, err := w.repoCache.ListBranches(repoPath)
	if err != nil {
		w.logger.Warn("Branch 搜尋: ListBranches 失敗", "phase", "失敗", "error", err, "repo", repo)
		return nil
	}
	return filterBranches(branches, query, maxBranchSuggestions)
}

// selectedRepoFromState extracts SelectedRepo from a Pending.State via the
// workflow.BranchStateReader interface (implemented by *issueState and
// *askState). Unrecognised states return "". Using the narrow interface
// keeps app/bot free of per-state type assertions — adding a new branch-
// bearing workflow just requires implementing BranchSelectedRepo().
func selectedRepoFromState(state any) string {
	if bs, ok := state.(workflow.BranchStateReader); ok {
		return bs.BranchSelectedRepo()
	}
	return ""
}

// filterBranches returns up to limit branches whose names contain query
// (case-insensitive substring). An empty query means "no filter" — the
// first limit branches are returned in their existing order.
func filterBranches(branches []string, query string, limit int) []string {
	q := strings.ToLower(strings.TrimSpace(query))
	out := make([]string, 0, limit)
	for _, b := range branches {
		if q != "" && !strings.Contains(strings.ToLower(b), q) {
			continue
		}
		out = append(out, b)
		if len(out) >= limit {
			break
		}
	}
	return out
}

// dSelectorLabel maps task-type values to friendly display labels used when
// acknowledging a D-selector click.
var dSelectorLabel = map[string]string{
	"issue":     "📝 建 Issue",
	"ask":       "❓ 問問題",
	"pr_review": "🔍 Review PR",
}

// transitionalValues are selector picks whose ack would just clutter the
// thread — the user skipped a step or backed out, and the next rendered
// step (a new selector, a modal, or a status message) is the real signal
// that the pick was accepted. Dropping the ack stops the thread from
// growing by one message per no-op click.
var transitionalValues = map[string]bool{
	"skip":           true, // ask attach prompt: "不用"
	"跳過":             true, // description prompt: "跳過"
	"back_to_repo":   true, // issue/ask back button to repo picker
	"back_to_attach": true, // ask back from repo picker to attach prompt
}

// isTransitionalValue reports whether a selection value represents a pure
// navigation step (skip / back) rather than a substantive choice the user
// might want a thread record of.
func isTransitionalValue(value string) bool {
	return transitionalValues[value]
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
	// Atomic lookup-and-delete: a rapid double-click arrives as two goroutines,
	// both racing on selectorMsgTS. Whichever grabs the lock first consumes the
	// entry; the second sees ok==false and returns early — no duplicate dispatch.
	//
	// Caveat: OpenModal flows persist the pending under the same selectorMsgTS
	// via ModalMetadata (see issue.go / ask.go / pr_review.go: ModalMetadata =
	// p.SelectorTS), so when the resulting step is OpenModal we re-insert below
	// to preserve the lookup path HandleDescriptionSubmit depends on.
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
	// Transitional picks (skip / back_*) just navigate; the next step's own
	// message conveys acceptance, so drop the selector entirely instead of
	// leaving a trail of ":white_check_mark: skip" acks.
	if isTransitionalValue(value) {
		_ = w.slack.DeleteMessage(channelID, selectorMsgTS)
		pending.SessionMsgTSs = removeTS(pending.SessionMsgTSs, selectorMsgTS)
	} else if pending.Phase == "d_selector" {
		ackLabel := value
		if label, ok := dSelectorLabel[value]; ok {
			ackLabel = label
		}
		_ = w.slack.UpdateMessage(channelID, selectorMsgTS, ":white_check_mark: "+ackLabel)
		// Promote this TS to the session-wide "breadcrumb" slot. Timeout
		// cleanup keeps this one, deletes everything else.
		pending.DSelectorAckTS = selectorMsgTS
		pending.SessionMsgTSs = removeTS(pending.SessionMsgTSs, selectorMsgTS)
	} else {
		_ = w.slack.UpdateMessage(channelID, selectorMsgTS, ":white_check_mark: "+value)
		// Remember the repo ack so a later "重新選 repo" click can delete it —
		// otherwise every rejected repo pick leaves behind a ":white_check_mark:
		// owner/repo" line the user already disowned.
		if pending.Phase == "repo" || pending.Phase == "repo_search" {
			pending.RepoAckTS = selectorMsgTS
		}
	}
	step, err := w.dispatcher.HandleSelection(ctx, pending, value)
	if err != nil {
		w.logger.Error("HandleSelection dispatch failed", "phase", "失敗", "error", err)
		return
	}
	// OpenModal with an explicit ModalMetadata keyed by the original selectorTS
	// needs the entry back so HandleDescriptionSubmit can resolve it. The
	// modal-first path (ModalMetadata == "") is handled by executeStep's
	// storePending under a synthesised key, so no re-insert needed here.
	if step.Kind == workflow.NextStepOpenModal && step.ModalMetadata == selectorMsgTS {
		w.mu.Lock()
		w.pending[selectorMsgTS] = pending
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
	// Defensive rewind: a button click here means the user is back on the
	// selector. If the phase is still *_modal (the workflow set it before
	// OpenModal and Slack never delivered a ViewClosed/ViewSubmission to
	// reset it — a very real case when the user dismisses the modal with
	// escape or an outside tap), treating "補充說明"/"跳過" as the modal's
	// submitted text would append it to the description and fire the job.
	// Rewind first so the downstream Selection re-enters the prompt branch.
	if prev, rewind := rewindModalPhase(pending.Phase); rewind {
		pending.Phase = prev
	}
	// Submit-path values: the click dispatches a Submit directly (no modal
	// follows) so the selector message and pending entry must be cleaned up
	// here — nothing downstream will. The only non-submit value is
	// "補充說明", which falls through to OpenModal (modal cleanup owns that
	// path). Keep this list in sync with the description-prompt options in
	// the workflows that use description_action.
	if value == "跳過" {
		delete(w.pending, selectorMsgTS)
		w.mu.Unlock()
		// Drop the prompt rather than leaving an ack — the status message
		// that follows ("分析 codebase 中..." / "思考中...") is the real
		// user-visible confirmation.
		_ = w.slack.DeleteMessage(channelID, selectorMsgTS)
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

// rewindModalPhase maps a *_modal phase (the state a workflow sets right
// before OpenModal) back to its prompt phase. Returns ok=true when the
// cancelled modal can be rewound to a selector the user can re-click;
// returns ok=false when the modal has no reversible prompt (e.g. PR Review's
// URL modal — closing that one should surface the usual empty-URL error).
func rewindModalPhase(phase string) (string, bool) {
	switch phase {
	case "description_modal":
		return "description", true
	case "ask_description_modal":
		return "ask_description_prompt", true
	}
	return "", false
}

// HandleModalClosed handles the ViewClosed event — the user dismissed the
// modal via the ✕ button / 取消 / escape without submitting.
//
// Three outcomes depending on the workflow's phase:
//
//  1. Description modals (description_modal / ask_description_modal): the
//     pending stays alive and the phase rewinds to the prompt state so a
//     subsequent click on the still-visible selector button re-opens the
//     modal. Without this, the stuck *_modal phase would make the next
//     button click feed "補充說明" into the workflow as the modal's
//     submitted text.
//
//  2. PR Review URL modal (pr_review_modal): no reversible prompt exists
//     (the modal was the entry point). Drop the pending, post a cancel
//     acknowledgement, and clear dedup so the user can @bot again.
//
//  3. Any other phase falls through to HandleDescriptionSubmit("") — keeps
//     the old empty-submit semantics for paths this handler doesn't know
//     about, so new workflows don't silently break.
func (w *Workflow) HandleModalClosed(selectorMsgTS string) {
	w.mu.Lock()
	pending, ok := w.pending[selectorMsgTS]
	w.mu.Unlock()
	if !ok {
		return
	}
	if prev, canRewind := rewindModalPhase(pending.Phase); canRewind {
		pending.Phase = prev
		return
	}
	if pending.Phase == "pr_review_modal" {
		w.mu.Lock()
		delete(w.pending, selectorMsgTS)
		w.mu.Unlock()
		_ = w.slack.PostMessage(pending.ChannelID, ":white_check_mark: 已取消 PR Review", pending.ThreadTS)
		if w.handler != nil {
			w.handler.ClearThreadDedup(pending.ChannelID, pending.ThreadTS)
		}
		return
	}
	w.HandleDescriptionSubmit(selectorMsgTS, "")
}

// HandleDescriptionSubmit handles a modal submission carrying the extra
// description text. ViewClosed events are routed through HandleModalClosed
// so description modals can be rewound; this function handles the real
// submit path.
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
//
// Invalidates the old pending under w.mu so any in-flight repo-prep goroutine
// that completes after the user clicks "back" bails out in executeStep instead
// of posting a stale branch selector (or submitting) on top of the fresh repo
// picker we're about to render. The pending returned by dispatcher.handleBackToRepo
// is the SAME pointer we just invalidated (the workflow mutates in place), so
// we clone into a fresh *Pending — otherwise the invalidated flag follows the
// pointer into the new generation and immediately suppresses its own selector.
func (w *Workflow) HandleBackToRepo(channelID, selectorMsgTS string) {
	w.mu.Lock()
	oldPending, ok := w.pending[selectorMsgTS]
	if ok {
		delete(w.pending, selectorMsgTS)
		oldPending.Invalidate()
	}
	w.mu.Unlock()
	if !ok {
		return
	}

	// Drop the stale "✅ <repo>" ack so backing out doesn't leave abandoned
	// picks piling up in the thread. The "已返回 repo 選擇" breadcrumb below
	// is the one message we want to retain.
	if oldPending.RepoAckTS != "" {
		_ = w.slack.DeleteMessage(channelID, oldPending.RepoAckTS)
	}

	ctx := context.Background()
	step, err := w.dispatcher.HandleSelection(ctx, oldPending, "back_to_repo")
	if err != nil {
		w.logger.Error("HandleBackToRepo dispatch failed", "phase", "失敗", "error", err)
		return
	}
	// Produce a fresh Pending generation. We can't reuse the pointer the
	// dispatcher returns — workflows mutate the old pending in place — so we
	// copy identity fields + State + Phase into a brand-new struct. The new
	// struct has its own zero-valued `invalidated` flag. The original pending
	// stays invalidated so any lingering goroutine still skips.
	src := step.Pending
	if src == nil {
		src = oldPending
	}
	fresh := clonePendingForRegeneration(src)
	step.Pending = fresh

	// Update old selector to show we navigated back.
	_ = w.slack.UpdateMessage(channelID, selectorMsgTS, ":leftwards_arrow_with_hook: 已返回 repo 選擇")
	w.executeStep(ctx, fresh, step, "")
}

// clonePendingForRegeneration builds a fresh *Pending carrying the same
// identity/state/phase as src but with a zero-valued invalidation flag. Used
// by HandleBackToRepo so the new generation isn't poisoned by the old one's
// invalidated state. SelectorTS is cleared because the caller (executeStep →
// storePending) will set the fresh selector TS after posting.
func clonePendingForRegeneration(src *workflow.Pending) *workflow.Pending {
	if src == nil {
		return nil
	}
	return &workflow.Pending{
		ChannelID:      src.ChannelID,
		ThreadTS:       src.ThreadTS,
		TriggerTS:      src.TriggerTS,
		UserID:         src.UserID,
		Reporter:       src.Reporter,
		ChannelName:    src.ChannelName,
		RequestID:      src.RequestID,
		SelectorTS:     "",
		Phase:          src.Phase,
		TaskType:       src.TaskType,
		State:          src.State,
		BusyHint:       src.BusyHint,
		DSelectorAckTS: src.DSelectorAckTS,
		SessionMsgTSs:  append([]string(nil), src.SessionMsgTSs...),
		// RepoAckTS intentionally left zero — back-to-repo starts a fresh
		// repo pick, and the old pick's ack has already been deleted by
		// HandleBackToRepo.
	}
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
	// If the pending was invalidated between dispatch and here (e.g. the user
	// clicked "back to repo" while an in-flight repo-prep goroutine was still
	// fetching branches), skip any user-visible side effect: posting a stale
	// selector, opening a late modal, or submitting a job the user abandoned.
	// Only applies to the "act on the user's flow" steps — Error/Cancel/Noop
	// still clear dedup and are safe to run.
	switch step.Kind {
	case workflow.NextStepSelector,
		workflow.NextStepOpenModal,
		workflow.NextStepSubmit:
		if pending.IsInvalidated() {
			w.logger.Info("executeStep: pending invalidated, skipping", "phase", "跳過", "kind", step.Kind, "request_id", pending.RequestID)
			return
		}
	}
	switch step.Kind {
	case workflow.NextStepSelector:
		if step.Selector == nil {
			w.logger.Error("NextStepSelector missing spec", "phase", "失敗", "request_id", pending.RequestID)
			_ = w.slack.PostMessage(pending.ChannelID, ":x: 內部錯誤：selector spec 未設置", pending.ThreadTS)
			if w.handler != nil {
				w.handler.ClearThreadDedup(pending.ChannelID, pending.ThreadTS)
			}
			return
		}
		selectorTS, err := w.slack.PostSmartSelector(pending.ChannelID, pending.ThreadTS, *step.Selector)
		if err != nil {
			w.logger.Error("PostSmartSelector failed", "phase", "失敗", "error", err, "action_id", step.Selector.ActionID)
			_ = w.slack.PostMessage(pending.ChannelID, ":x: 無法顯示選單，請重試", pending.ThreadTS)
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
			if err := w.slack.PostMessage(p.ChannelID,
				RenderHardReject(verdict), p.ThreadTS); err != nil {
				w.logger.Error("可用性檢查: 硬性拒絕訊息發送失敗", "phase", "失敗", "error", err)
			}
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
// a goroutine that evicts the entry after pendingTimeout. The selector TS is
// also tracked on pending.SessionMsgTSs so timeout cleanup can find every
// bot-posted message in this flow.
func (w *Workflow) storePending(selectorTS string, p *workflow.Pending) {
	p.SelectorTS = selectorTS
	w.mu.Lock()
	w.pending[selectorTS] = p
	// Register this message on the session trail (deduped — executeStep may
	// re-key an existing pending under a new TS after a modal flow).
	if !containsTS(p.SessionMsgTSs, selectorTS) {
		p.SessionMsgTSs = append(p.SessionMsgTSs, selectorTS)
	}
	w.mu.Unlock()

	go func() {
		time.Sleep(w.pendingTimeout)
		w.mu.Lock()
		_, stillPending := w.pending[selectorTS]
		var sessionTSs []string
		var dsTS string
		if stillPending {
			delete(w.pending, selectorTS)
			dsTS = p.DSelectorAckTS
			sessionTSs = append([]string(nil), p.SessionMsgTSs...)
		}
		w.mu.Unlock()

		if !stillPending {
			return
		}
		// Condense the thread: delete every session message (including the
		// current selector) except the D-selector breadcrumb, then post the
		// single timeout notice.
		for _, ts := range sessionTSs {
			if ts == dsTS {
				continue
			}
			_ = w.slack.DeleteMessage(p.ChannelID, ts)
		}
		_ = w.slack.PostMessage(p.ChannelID, ":hourglass: 選擇已超時，請重新觸發。", p.ThreadTS)
		if w.handler != nil {
			w.handler.ClearThreadDedup(p.ChannelID, p.ThreadTS)
		}
	}()
}

// containsTS reports whether tsList contains ts.
func containsTS(tsList []string, ts string) bool {
	for _, v := range tsList {
		if v == ts {
			return true
		}
	}
	return false
}

// removeTS returns tsList with the first occurrence of ts removed. Returns
// the input unchanged when ts isn't present. Used when a TS transitions
// from the session trail into a more specific slot (DSelectorAckTS) or gets
// deleted outright, so timeout cleanup doesn't double-delete.
func removeTS(tsList []string, ts string) []string {
	for i, v := range tsList {
		if v == ts {
			return append(tsList[:i], tsList[i+1:]...)
		}
	}
	return tsList
}

