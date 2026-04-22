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
