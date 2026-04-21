package bot

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/Ivantseng123/agentdock/app/config"
	"github.com/Ivantseng123/agentdock/app/workflow"
	slackclient "github.com/Ivantseng123/agentdock/app/slack"
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
func (s *shimSlack) UpdateMessage(ch, mts, text string) error                  { return nil }
func (s *shimSlack) UpdateMessageWithButton(ch, mts, text, aid, bt, val string) error { return nil }
func (s *shimSlack) PostSelector(ch, prompt, prefix string, opts []string, ts string) (string, error) {
	return "sel-ts", nil
}
func (s *shimSlack) PostSelectorWithBack(ch, prompt, prefix string, opts []string, ts, back, bl string) (string, error) {
	return "sel-ts", nil
}
func (s *shimSlack) PostExternalSelector(ch, prompt, aid, ph, ts string) (string, error) {
	return "ext-ts", nil
}
func (s *shimSlack) OpenTextInputModal(tid, title, label, name, metadata string) error { return nil }
func (s *shimSlack) ResolveUser(uid string) string                                     { return uid }
func (s *shimSlack) GetChannelName(cid string) string                                  { return cid }
func (s *shimSlack) FetchThreadContext(c, ts, tts, bot string, lim int) ([]slackclient.ThreadRawMessage, error) {
	return nil, nil
}
func (s *shimSlack) DownloadAttachments(msgs []slackclient.ThreadRawMessage, dir string) []slackclient.AttachmentDownload {
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

	wf := NewWorkflow(cfg, disp, slack, nil, nil)

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

	wf := NewWorkflow(cfg, disp, slack, nil, nil)

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
	wf := NewWorkflow(cfg, disp, slack, nil, nil)

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
	wf := NewWorkflow(cfg, disp, slack, nil, nil)

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
	wf := NewWorkflow(cfg, disp, sl, nil, nil)

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

	wf.HandleSelection("C1", "d_selector", "issue", selectorTS)

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
	wf := NewWorkflow(cfg, disp, sl, nil, logger)

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
func (f *fakeIssueWorkflow) HandleResult(ctx context.Context, job *queue.Job, result *queue.JobResult) error {
	return nil
}
