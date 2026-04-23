package bot

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Ivantseng123/agentdock/app/config"
	slackclient "github.com/Ivantseng123/agentdock/app/slack"
	"github.com/Ivantseng123/agentdock/app/workflow"
	"github.com/Ivantseng123/agentdock/shared/queue"
)

// ── fake SlackPort for shim tests ──────────────────────────────────────────

type shimSlack struct {
	posted []string
}

func (s *shimSlack) PostMessage(ch, text, ts string) error {
	s.posted = append(s.posted, text)
	return nil
}
func (s *shimSlack) PostMessageWithTS(ch, text, ts string) (string, error) { return "ts", nil }
func (s *shimSlack) PostMessageWithButton(ch, text, ts, aid, bt, val string) (string, error) {
	return "ts", nil
}
func (s *shimSlack) UpdateMessage(ch, mts, text string) error                         { return nil }
func (s *shimSlack) UpdateMessageWithButton(ch, mts, text, aid, bt, val string) error { return nil }
func (s *shimSlack) PostSelector(ch, prompt, prefix string, labels, values []string, ts string) (string, error) {
	return "sel-ts", nil
}
func (s *shimSlack) PostSelectorWithBack(ch, prompt, prefix string, labels, values []string, ts, back, bl string) (string, error) {
	return "sel-ts", nil
}
func (s *shimSlack) PostExternalSelector(ch, prompt, aid, ph, ts, cancelAID, cancelLabel string) (string, error) {
	return "ext-ts", nil
}
func (s *shimSlack) OpenTextInputModal(tid, title, label, name, metadata string) error { return nil }
func (s *shimSlack) ResolveUser(uid string) string                                     { return uid }
func (s *shimSlack) GetChannelName(cid string) string                                  { return cid }
func (s *shimSlack) FetchThreadContext(c, ts, tts string, lim int) ([]slackclient.ThreadRawMessage, error) {
	return nil, nil
}
func (s *shimSlack) DownloadAttachments(msgs []slackclient.ThreadRawMessage, dir string) []slackclient.AttachmentDownload {
	return nil
}
func (s *shimSlack) UploadFile(channelID, threadTS, filename, title, content, initialComment string) error {
	return nil
}

// ── tests ──────────────────────────────────────────────────────────────────

// TestHandleTrigger_NoThread_Posts a warning and does not call dispatcher.
func TestHandleTrigger_NoThread_PostsWarning(t *testing.T) {
	slack := &shimSlack{}
	cfg := &config.Config{
		Channels:        map[string]config.ChannelConfig{"C1": {}},
		ChannelDefaults: config.ChannelConfig{},
	}
	// Build a real dispatcher with a fakeWorkflow so Dispatch works.
	reg := workflow.NewRegistry()
	reg.Register(&fakeIssueWorkflow{})
	disp := workflow.NewDispatcher(reg, slack, nil)

	wf := NewWorkflow(cfg, disp, slack, nil, nil, nil)

	wf.HandleTrigger(slackclient.TriggerEvent{
		ChannelID: "C1",
		ThreadTS:  "", // no thread
		UserID:    "U1",
		Text:      "issue foo/bar",
	})

	if len(slack.posted) == 0 {
		t.Fatal("expected warning message to be posted")
	}
	found := false
	for _, m := range slack.posted {
		if len(m) > 0 {
			found = true
		}
	}
	if !found {
		t.Error("expected at least one Slack post for no-thread trigger")
	}
}

// TestHandleTrigger_UnboundChannel_Silent when AutoBind is false.
func TestHandleTrigger_UnboundChannel_Silent(t *testing.T) {
	slack := &shimSlack{}
	cfg := &config.Config{
		Channels:        map[string]config.ChannelConfig{},
		ChannelDefaults: config.ChannelConfig{},
		AutoBind:        false,
	}
	reg := workflow.NewRegistry()
	reg.Register(&fakeIssueWorkflow{})
	disp := workflow.NewDispatcher(reg, slack, nil)

	wf := NewWorkflow(cfg, disp, slack, nil, nil, nil)

	wf.HandleTrigger(slackclient.TriggerEvent{
		ChannelID: "C_UNBOUND",
		ThreadTS:  "T1",
		UserID:    "U1",
		Text:      "issue foo/bar",
	})

	if len(slack.posted) != 0 {
		t.Errorf("expected silence for unbound channel, got %v", slack.posted)
	}
}

// TestExecuteStep_Submit_CallsHook verifies onSubmit is called for NextStepSubmit.
func TestExecuteStep_Submit_CallsHook(t *testing.T) {
	slack := &shimSlack{}
	cfg := &config.Config{Channels: map[string]config.ChannelConfig{}}
	reg := workflow.NewRegistry()
	reg.Register(&fakeIssueWorkflow{})
	disp := workflow.NewDispatcher(reg, slack, nil)
	wf := NewWorkflow(cfg, disp, slack, nil, nil, nil)

	called := false
	wf.SetSubmitHook(func(ctx context.Context, p *workflow.Pending) {
		called = true
	})

	p := &workflow.Pending{ChannelID: "C1", ThreadTS: "T1"}
	step := workflow.NextStep{Kind: workflow.NextStepSubmit, Pending: p}
	wf.executeStep(context.Background(), p, step, "")

	if !called {
		t.Error("expected onSubmit to be called for NextStepSubmit")
	}
}

// TestExecuteStep_Error_PostsMessage verifies error step posts to Slack.
func TestExecuteStep_Error_PostsMessage(t *testing.T) {
	slack := &shimSlack{}
	cfg := &config.Config{Channels: map[string]config.ChannelConfig{}}
	reg := workflow.NewRegistry()
	reg.Register(&fakeIssueWorkflow{})
	disp := workflow.NewDispatcher(reg, slack, nil)
	wf := NewWorkflow(cfg, disp, slack, nil, nil, nil)

	p := &workflow.Pending{ChannelID: "C1", ThreadTS: "T1"}
	step := workflow.NextStep{Kind: workflow.NextStepError, ErrorText: "boom"}
	wf.executeStep(context.Background(), p, step, "")

	if len(slack.posted) == 0 {
		t.Fatal("expected error message to be posted")
	}
}

// ── shimSlack variant that fails OpenTextInputModal ────────────────────────

type shimSlackModalFail struct {
	shimSlack
}

func (s *shimSlackModalFail) OpenTextInputModal(tid, title, label, name, metadata string) error {
	return errors.New("modal open failed")
}

// TestHandleSelection_DSelector_DispatchesWorkflow verifies that a D-selector
// click (Phase == "d_selector", value == "issue") is forwarded to the issue
// workflow's Trigger and ultimately calls onSubmit.
func TestHandleSelection_DSelector_DispatchesWorkflow(t *testing.T) {
	sl := &shimSlack{}
	cfg := &config.Config{Channels: map[string]config.ChannelConfig{}}
	reg := workflow.NewRegistry()
	reg.Register(&fakeIssueWorkflow{})
	disp := workflow.NewDispatcher(reg, sl, nil)
	wf := NewWorkflow(cfg, disp, sl, nil, nil, nil)

	submitted := false
	wf.SetSubmitHook(func(ctx context.Context, p *workflow.Pending) {
		submitted = true
	})

	// Manually insert a pending entry with Phase="d_selector", mirroring what
	// postDSelector places in the map after storePending.
	const selectorTS = "sel-123"
	p := &workflow.Pending{
		ChannelID:  "C1",
		ThreadTS:   "T1",
		Phase:      "d_selector",
		SelectorTS: selectorTS,
	}
	wf.mu.Lock()
	wf.pending[selectorTS] = p
	wf.mu.Unlock()

	wf.HandleSelection("C1", "d_selector", "issue", selectorTS, "")

	if !submitted {
		t.Error("expected onSubmit to be called after d_selector click → issue workflow")
	}

	// Pending must have been consumed.
	wf.mu.Lock()
	_, stillPending := wf.pending[selectorTS]
	wf.mu.Unlock()
	if stillPending {
		t.Error("expected pending entry to be removed after HandleSelection")
	}
}

// TestExecuteStep_OpenModal_FirstStepStoresPending verifies the modal-first
// path (PR Review D-path with no URL in thread): ModalMetadata is empty and
// no prior selector stored the pending, so executeStep must synthesise a key
// from pending.RequestID and store pending itself — otherwise the subsequent
// modal submit (HandleDescriptionSubmit) looks up "" and silently drops.
func TestExecuteStep_OpenModal_FirstStepStoresPending(t *testing.T) {
	sl := &shimSlack{}
	cfg := &config.Config{Channels: map[string]config.ChannelConfig{}}
	reg := workflow.NewRegistry()
	reg.Register(&fakeIssueWorkflow{})
	disp := workflow.NewDispatcher(reg, sl, nil)
	wf := NewWorkflow(cfg, disp, sl, nil, slog.Default(), nil)

	p := &workflow.Pending{
		ChannelID: "C1", ThreadTS: "T1",
		RequestID: "req-xyz",
		TaskType:  "pr_review",
	}
	step := workflow.NextStep{
		Kind:           workflow.NextStepOpenModal,
		ModalTitle:     "PR Review",
		ModalLabel:     "貼上 PR URL",
		ModalInputName: "pr_url",
		// ModalMetadata intentionally empty — modal-first path.
		Pending: p,
	}
	wf.executeStep(context.Background(), p, step, "trigger-abc")

	wf.mu.Lock()
	stored, ok := wf.pending["modal-req-xyz"]
	wf.mu.Unlock()
	if !ok {
		t.Fatal("expected pending stored under synthesised 'modal-<reqID>' key")
	}
	if stored != p {
		t.Error("stored pending differs from supplied pending")
	}
	if p.SelectorTS != "modal-req-xyz" {
		t.Errorf("pending.SelectorTS = %q, want modal-req-xyz", p.SelectorTS)
	}
}

// TestHandleDescriptionAction_ModalFail_ConsumesPending verifies that when
// OpenTextInputModal returns an error the pending entry is removed so the
// timeout goroutine cannot fire a spurious ":hourglass: 選擇已超時" message.
func TestHandleDescriptionAction_ModalFail_ConsumesPending(t *testing.T) {
	sl := &shimSlackModalFail{}
	cfg := &config.Config{Channels: map[string]config.ChannelConfig{}}
	reg := workflow.NewRegistry()
	reg.Register(&fakeIssueWorkflow{})
	disp := workflow.NewDispatcher(reg, sl, nil)
	logger := slog.Default()
	wf := NewWorkflow(cfg, disp, sl, nil, logger, nil)

	submitted := false
	wf.SetSubmitHook(func(ctx context.Context, p *workflow.Pending) {
		submitted = true
	})

	// Simulate a pending left by the description-prompt selector.
	const selectorTS = "desc-sel-456"
	p := &workflow.Pending{
		ChannelID:  "C1",
		ThreadTS:   "T1",
		TaskType:   "issue",
		Phase:      "description",
		SelectorTS: selectorTS,
	}
	wf.mu.Lock()
	wf.pending[selectorTS] = p
	wf.mu.Unlock()

	// The dispatcher's HandleSelection for "description" phase calls
	// IssueWorkflow.Selection which returns NextStepOpenModal.
	// We cannot wire that here without a real IssueWorkflow, so we test via
	// executeStep directly instead — feed it NextStepOpenModal with a valid
	// triggerID so it tries (and fails) to open the modal.
	step := workflow.NextStep{
		Kind:           workflow.NextStepOpenModal,
		ModalTitle:     "補充說明",
		ModalLabel:     "說明",
		ModalInputName: "description_input",
		ModalMetadata:  selectorTS,
	}
	wf.executeStep(context.Background(), p, step, "fake-trigger-id")

	// After the fallback, pending must be gone.
	wf.mu.Lock()
	_, stillPending := wf.pending[selectorTS]
	wf.mu.Unlock()
	if stillPending {
		t.Error("expected pending entry to be consumed when OpenTextInputModal fails")
	}

	if !submitted {
		t.Error("expected onSubmit to be called as fallback when OpenTextInputModal fails")
	}
}

// ── fake workflow for tests ────────────────────────────────────────────────

type fakeIssueWorkflow struct{}

func (f *fakeIssueWorkflow) Type() string { return "issue" }
func (f *fakeIssueWorkflow) Trigger(ctx context.Context, ev workflow.TriggerEvent, args string) (workflow.NextStep, error) {
	return workflow.NextStep{Kind: workflow.NextStepSubmit, Pending: &workflow.Pending{
		ChannelID: ev.ChannelID,
		ThreadTS:  ev.ThreadTS,
		TaskType:  "issue",
	}}, nil
}
func (f *fakeIssueWorkflow) Selection(ctx context.Context, p *workflow.Pending, value string) (workflow.NextStep, error) {
	return workflow.NextStep{Kind: workflow.NextStepSubmit, Pending: p}, nil
}
func (f *fakeIssueWorkflow) BuildJob(ctx context.Context, p *workflow.Pending) (*queue.Job, string, error) {
	return &queue.Job{TaskType: "issue"}, "status", nil
}
func (f *fakeIssueWorkflow) HandleResult(ctx context.Context, state *queue.JobState, result *queue.JobResult) error {
	return nil
}

// stubAvailability lets tests pre-program verdicts.
type stubAvailability struct {
	mu          sync.Mutex
	SoftVerdict queue.Verdict
	HardVerdict queue.Verdict
	SoftCalls   int
	HardCalls   int
}

func (s *stubAvailability) CheckSoft(ctx context.Context) queue.Verdict {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.SoftCalls++
	return s.SoftVerdict
}
func (s *stubAvailability) CheckHard(ctx context.Context) queue.Verdict {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.HardCalls++
	return s.HardVerdict
}

func TestSubmit_NoWorkers_HardRejects(t *testing.T) {
	sl := &shimSlack{}
	avail := &stubAvailability{HardVerdict: queue.Verdict{Kind: queue.VerdictNoWorkers}}
	cfg := &config.Config{Channels: map[string]config.ChannelConfig{}}
	reg := workflow.NewRegistry()
	reg.Register(&fakeIssueWorkflow{})
	disp := workflow.NewDispatcher(reg, sl, nil)
	wf := NewWorkflow(cfg, disp, sl, nil, slog.Default(), avail)

	onSubmitCalled := false
	wf.SetSubmitHook(func(ctx context.Context, p *workflow.Pending) {
		onSubmitCalled = true
	})

	p := &workflow.Pending{ChannelID: "C1", ThreadTS: "T1", TaskType: "issue"}
	step := workflow.NextStep{Kind: workflow.NextStepSubmit, Pending: p}
	wf.executeStep(context.Background(), p, step, "")

	if avail.HardCalls != 1 {
		t.Errorf("HardCalls = %d, want 1", avail.HardCalls)
	}
	if onSubmitCalled {
		t.Error("onSubmit must NOT be called when verdict is NoWorkers")
	}

	foundReject := false
	for _, m := range sl.posted {
		if strings.Contains(m, ":x:") && strings.Contains(m, "沒有 worker") {
			foundReject = true
		}
	}
	if !foundReject {
		t.Errorf("expected :x: hard reject message; got posts: %+v", sl.posted)
	}
}

func TestSubmit_BusyEnqueueOK_SetsBusyHint(t *testing.T) {
	sl := &shimSlack{}
	avail := &stubAvailability{
		HardVerdict: queue.Verdict{
			Kind:          queue.VerdictBusyEnqueueOK,
			EstimatedWait: 6 * time.Minute,
		},
	}
	cfg := &config.Config{Channels: map[string]config.ChannelConfig{}}
	reg := workflow.NewRegistry()
	reg.Register(&fakeIssueWorkflow{})
	disp := workflow.NewDispatcher(reg, sl, nil)
	wf := NewWorkflow(cfg, disp, sl, nil, slog.Default(), avail)

	var gotPending *workflow.Pending
	wf.SetSubmitHook(func(ctx context.Context, p *workflow.Pending) {
		gotPending = p
	})

	p := &workflow.Pending{ChannelID: "C1", ThreadTS: "T1", TaskType: "issue"}
	step := workflow.NextStep{Kind: workflow.NextStepSubmit, Pending: p}
	wf.executeStep(context.Background(), p, step, "")

	if gotPending == nil {
		t.Fatal("onSubmit was not called; expected BusyEnqueueOK to pass through")
	}
	if gotPending.BusyHint == "" {
		t.Errorf("BusyHint should be set; got empty")
	}
	if !strings.Contains(gotPending.BusyHint, "預估等候") {
		t.Errorf("BusyHint should contain 預估等候; got %q", gotPending.BusyHint)
	}
}

func TestHandleTrigger_NoWorkers_PostsSoftWarnButContinues(t *testing.T) {
	sl := &shimSlack{}
	avail := &stubAvailability{SoftVerdict: queue.Verdict{Kind: queue.VerdictNoWorkers}}
	cfg := &config.Config{
		Channels: map[string]config.ChannelConfig{"C1": {}},
	}
	reg := workflow.NewRegistry()
	reg.Register(&fakeIssueWorkflow{})
	disp := workflow.NewDispatcher(reg, sl, nil)
	wf := NewWorkflow(cfg, disp, sl, nil, slog.Default(), avail)

	onSubmitCalled := false
	wf.SetSubmitHook(func(ctx context.Context, p *workflow.Pending) {
		onSubmitCalled = true
	})

	wf.HandleTrigger(slackclient.TriggerEvent{
		ChannelID: "C1",
		ThreadTS:  "T1",
		TriggerTS: "T1",
		UserID:    "U1",
		Text:      "issue foo/bar",
	})

	if avail.SoftCalls != 1 {
		t.Errorf("SoftCalls = %d, want 1", avail.SoftCalls)
	}

	foundWarn := false
	for _, m := range sl.posted {
		if strings.Contains(m, ":warning:") && strings.Contains(m, "沒有 worker") {
			foundWarn = true
		}
	}
	if !foundWarn {
		t.Errorf("expected :warning: soft warn; got posts: %+v", sl.posted)
	}

	// fakeIssueWorkflow.Trigger returns NextStepSubmit → submit() runs. The stub's
	// hard verdict is zero-valued (VerdictKind("")) which matches none of the switch
	// cases, so submit() falls through to onSubmit. That's the assertion that soft
	// warn doesn't short-circuit dispatch.
	if !onSubmitCalled {
		t.Error("soft warn must not block dispatch; expected onSubmit to still be called")
	}
}

// ── #141 pending invalidation + in-flight dedup ────────────────────────────

// countingSelectionWorkflow counts Selection calls. Used by the
// rapid-double-click test to assert only one click ever reaches dispatch.
type countingSelectionWorkflow struct {
	mu        sync.Mutex
	selCalls  int
	returnFn  func(p *workflow.Pending, value string) workflow.NextStep
	signalCh  chan struct{} // closed by the first Selection caller if non-nil
	waitUntil chan struct{} // if non-nil, Selection blocks until this closes
}

func (f *countingSelectionWorkflow) Type() string { return "issue" }
func (f *countingSelectionWorkflow) Trigger(ctx context.Context, ev workflow.TriggerEvent, args string) (workflow.NextStep, error) {
	return workflow.NextStep{Kind: workflow.NextStepSubmit, Pending: &workflow.Pending{ChannelID: ev.ChannelID, ThreadTS: ev.ThreadTS, TaskType: "issue"}}, nil
}
func (f *countingSelectionWorkflow) Selection(ctx context.Context, p *workflow.Pending, value string) (workflow.NextStep, error) {
	f.mu.Lock()
	first := f.selCalls == 0
	f.selCalls++
	f.mu.Unlock()
	if first && f.signalCh != nil {
		close(f.signalCh)
	}
	if f.waitUntil != nil {
		<-f.waitUntil
	}
	if f.returnFn != nil {
		return f.returnFn(p, value), nil
	}
	return workflow.NextStep{Kind: workflow.NextStepSubmit, Pending: p}, nil
}
func (f *countingSelectionWorkflow) BuildJob(ctx context.Context, p *workflow.Pending) (*queue.Job, string, error) {
	return &queue.Job{TaskType: "issue"}, "status", nil
}
func (f *countingSelectionWorkflow) HandleResult(ctx context.Context, state *queue.JobState, result *queue.JobResult) error {
	return nil
}

// TestHandleSelection_RapidDoubleClick_DispatchesOnce simulates two concurrent
// clicks on the same selector message. The workflow's Selection is slow, so
// both clicks reach HandleSelection before either has a chance to finish.
// The atomic lookup-and-delete must ensure only the first click's lookup
// succeeds; the second returns early and never dispatches.
func TestHandleSelection_RapidDoubleClick_DispatchesOnce(t *testing.T) {
	sl := &shimSlack{}
	cfg := &config.Config{Channels: map[string]config.ChannelConfig{}}
	reg := workflow.NewRegistry()
	gate := make(chan struct{})
	fake := &countingSelectionWorkflow{waitUntil: gate}
	reg.Register(fake)
	disp := workflow.NewDispatcher(reg, sl, nil)
	wf := NewWorkflow(cfg, disp, sl, nil, slog.Default(), nil)
	wf.SetSubmitHook(func(ctx context.Context, p *workflow.Pending) {})

	const selectorTS = "sel-double"
	p := &workflow.Pending{
		ChannelID: "C1", ThreadTS: "T1", TaskType: "issue",
		Phase: "issue_confirm", SelectorTS: selectorTS,
	}
	wf.mu.Lock()
	wf.pending[selectorTS] = p
	wf.mu.Unlock()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); wf.HandleSelection("C1", "issue_confirm", "ok", selectorTS, "") }()
	go func() { defer wg.Done(); wf.HandleSelection("C1", "issue_confirm", "ok", selectorTS, "") }()

	// Give both goroutines time to hit the lookup; only one should proceed
	// into Selection (which blocks on gate). Release after a brief pause.
	time.Sleep(20 * time.Millisecond)
	close(gate)
	wg.Wait()

	fake.mu.Lock()
	calls := fake.selCalls
	fake.mu.Unlock()
	if calls != 1 {
		t.Errorf("Selection call count = %d, want 1 (second click should have been deduped)", calls)
	}
}

// TestHandleSelection_OpenModal_ReinsertsPending verifies the OpenModal
// regression guard: when the workflow's Selection returns OpenModal with
// ModalMetadata == selectorMsgTS (the lookup key), HandleSelection must
// re-insert the pending under that key so the subsequent modal submit can
// still resolve it via HandleDescriptionSubmit.
func TestHandleSelection_OpenModal_ReinsertsPending(t *testing.T) {
	sl := &shimSlack{}
	cfg := &config.Config{Channels: map[string]config.ChannelConfig{}}
	reg := workflow.NewRegistry()
	const selectorTS = "sel-modal-reinsert"
	fake := &countingSelectionWorkflow{
		returnFn: func(p *workflow.Pending, value string) workflow.NextStep {
			return workflow.NextStep{
				Kind:           workflow.NextStepOpenModal,
				ModalTitle:     "補充說明",
				ModalLabel:     "說明",
				ModalInputName: "description_input",
				ModalMetadata:  selectorTS,
				Pending:        p,
			}
		},
	}
	reg.Register(fake)
	disp := workflow.NewDispatcher(reg, sl, nil)
	wf := NewWorkflow(cfg, disp, sl, nil, slog.Default(), nil)

	p := &workflow.Pending{
		ChannelID: "C1", ThreadTS: "T1", TaskType: "issue",
		Phase: "description", SelectorTS: selectorTS,
	}
	wf.mu.Lock()
	wf.pending[selectorTS] = p
	wf.mu.Unlock()

	wf.HandleSelection("C1", "description_action", "補充", selectorTS, "trig-1")

	wf.mu.Lock()
	stored, ok := wf.pending[selectorTS]
	wf.mu.Unlock()
	if !ok {
		t.Fatal("pending must be re-inserted so HandleDescriptionSubmit can resolve it")
	}
	if stored != p {
		t.Error("re-inserted pending is not the same pointer")
	}
}

// TestExecuteStep_InvalidatedPending_DoesNotPostSelector: if the pending was
// invalidated between dispatch and executeStep, NextStepPostSelector must
// bail out before touching Slack.
func TestExecuteStep_InvalidatedPending_DoesNotPostSelector(t *testing.T) {
	sl := &shimSlack{}
	cfg := &config.Config{Channels: map[string]config.ChannelConfig{}}
	reg := workflow.NewRegistry()
	reg.Register(&fakeIssueWorkflow{})
	disp := workflow.NewDispatcher(reg, sl, nil)
	wf := NewWorkflow(cfg, disp, sl, nil, slog.Default(), nil)

	p := &workflow.Pending{ChannelID: "C1", ThreadTS: "T1"}
	p.Invalidate()

	step := workflow.NextStep{
		Kind:            workflow.NextStepPostSelector,
		SelectorPrompt:  "pick",
		SelectorActions: []workflow.SelectorAction{{ActionID: "a", Label: "L", Value: "V"}},
		Pending:         p,
	}
	wf.executeStep(context.Background(), p, step, "")

	wf.mu.Lock()
	n := len(wf.pending)
	wf.mu.Unlock()
	if n != 0 {
		t.Errorf("invalidated pending should not have been stored; got %d entries", n)
	}
}

// TestExecuteStep_InvalidatedPending_DoesNotSubmit: NextStepSubmit on an
// invalidated pending must NOT fire onSubmit.
func TestExecuteStep_InvalidatedPending_DoesNotSubmit(t *testing.T) {
	sl := &shimSlack{}
	cfg := &config.Config{Channels: map[string]config.ChannelConfig{}}
	reg := workflow.NewRegistry()
	reg.Register(&fakeIssueWorkflow{})
	disp := workflow.NewDispatcher(reg, sl, nil)
	wf := NewWorkflow(cfg, disp, sl, nil, slog.Default(), nil)

	submitted := false
	wf.SetSubmitHook(func(ctx context.Context, p *workflow.Pending) { submitted = true })

	p := &workflow.Pending{ChannelID: "C1", ThreadTS: "T1"}
	p.Invalidate()
	step := workflow.NextStep{Kind: workflow.NextStepSubmit, Pending: p}
	wf.executeStep(context.Background(), p, step, "")

	if submitted {
		t.Error("invalidated pending must not trigger onSubmit")
	}
}

// TestHandleBackToRepo_InvalidatesOldPending: after clicking "back to repo",
// the old pending carries the invalidated flag. An in-flight goroutine
// observing it must skip the late side effect. We can't easily reach the
// dispatcher's handleBackToRepo here without a real IssueWorkflow, so we
// directly assert the flag transitions via the bot-layer entry.
func TestHandleBackToRepo_InvalidatesOldPending(t *testing.T) {
	sl := &shimSlack{}
	cfg := &config.Config{Channels: map[string]config.ChannelConfig{}}
	reg := workflow.NewRegistry()
	// A workflow whose Selection responds to "back_to_repo" by returning a
	// PostSelector step. The bot layer re-clones, so the old pending stays
	// invalidated while the fresh one is clean.
	fake := &countingSelectionWorkflow{
		returnFn: func(p *workflow.Pending, value string) workflow.NextStep {
			return workflow.NextStep{
				Kind:            workflow.NextStepPostSelector,
				SelectorPrompt:  "pick a repo",
				SelectorActions: []workflow.SelectorAction{{ActionID: "a", Label: "foo/bar", Value: "foo/bar"}},
				Pending:         p,
			}
		},
	}
	reg.Register(fake)
	disp := workflow.NewDispatcher(reg, sl, nil)
	wf := NewWorkflow(cfg, disp, sl, nil, slog.Default(), nil)

	const selectorTS = "sel-back"
	oldPending := &workflow.Pending{
		ChannelID: "C1", ThreadTS: "T1", TaskType: "issue",
		Phase: "branch", SelectorTS: selectorTS, RequestID: "req-back",
	}
	wf.mu.Lock()
	wf.pending[selectorTS] = oldPending
	wf.mu.Unlock()

	wf.HandleBackToRepo("C1", selectorTS)

	if !oldPending.IsInvalidated() {
		t.Error("old pending should be invalidated after HandleBackToRepo")
	}

	// A fresh pending should now be keyed under the new selector TS.
	wf.mu.Lock()
	var fresh *workflow.Pending
	for _, v := range wf.pending {
		fresh = v
	}
	wf.mu.Unlock()
	if fresh == nil {
		t.Fatal("expected a fresh pending to be stored after back-to-repo")
	}
	if fresh == oldPending {
		t.Error("fresh pending must be a different pointer from old pending")
	}
	if fresh.IsInvalidated() {
		t.Error("fresh pending must not inherit invalidated flag")
	}
	if fresh.RequestID != oldPending.RequestID {
		t.Errorf("fresh pending RequestID = %q, want %q (identity fields should carry over)", fresh.RequestID, oldPending.RequestID)
	}
}

// TestInvalidatedPending_ConcurrentAccess stresses the atomic.Bool with
// concurrent Invalidate / IsInvalidated calls. Must pass under `go test -race`
// — if the flag were a bare bool the race detector would fail this test.
func TestInvalidatedPending_ConcurrentAccess(t *testing.T) {
	p := &workflow.Pending{ChannelID: "C1"}
	const iters = 1000
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			p.Invalidate()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			_ = p.IsInvalidated()
		}
	}()
	wg.Wait()
	if !p.IsInvalidated() {
		t.Error("after Invalidate loop, IsInvalidated should return true")
	}
}

func TestHandleTrigger_HealthyOK_NoSoftWarn(t *testing.T) {
	sl := &shimSlack{}
	avail := &stubAvailability{SoftVerdict: queue.Verdict{Kind: queue.VerdictOK}}
	cfg := &config.Config{
		Channels: map[string]config.ChannelConfig{"C1": {}},
	}
	reg := workflow.NewRegistry()
	reg.Register(&fakeIssueWorkflow{})
	disp := workflow.NewDispatcher(reg, sl, nil)
	wf := NewWorkflow(cfg, disp, sl, nil, slog.Default(), avail)

	wf.SetSubmitHook(func(ctx context.Context, p *workflow.Pending) {})

	wf.HandleTrigger(slackclient.TriggerEvent{
		ChannelID: "C1", ThreadTS: "T1", TriggerTS: "T1", UserID: "U1", Text: "issue foo/bar",
	})

	for _, m := range sl.posted {
		if strings.Contains(m, "沒有 worker") {
			t.Errorf("OK verdict should not post soft warn; got %q", m)
		}
	}
}
