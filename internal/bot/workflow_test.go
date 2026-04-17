package bot

import (
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"

	"agentdock/internal/config"
	"agentdock/internal/queue"
	slackclient "agentdock/internal/slack"
)

// stubSlack implements the slackAPI interface for workflow tests.
type stubSlack struct {
	mu sync.Mutex

	PostSelectorCalls          []postSelectorCall
	PostSelectorWithBackCalls  []postSelectorWithBackCall
	PostExternalSelectorCalls  []postExternalSelectorCall
	UpdateMessageCalls         []updateMessageCall
	PostMessageCalls           []postMessageCall
	PostMessageWithButtonCalls []postMessageWithButtonCall

	PostSelectorErr     error
	PostSelectorBackErr error
	PostExternalErr     error
	NextSelectorTS      string
}

type postSelectorCall struct {
	ChannelID, Prompt, ActionPrefix, ThreadTS string
	Options                                   []string
}
type postSelectorWithBackCall struct {
	ChannelID, Prompt, ActionPrefix, ThreadTS, BackActionID, BackLabel string
	Options                                                             []string
}
type postExternalSelectorCall struct {
	ChannelID, Prompt, ActionID, Placeholder, ThreadTS string
}
type updateMessageCall struct{ ChannelID, MessageTS, Text string }
type postMessageCall struct{ ChannelID, Text, ThreadTS string }
type postMessageWithButtonCall struct {
	ChannelID, Text, ThreadTS, ActionID, ButtonText, Value string
}

func (s *stubSlack) PostMessage(channelID, text, threadTS string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.PostMessageCalls = append(s.PostMessageCalls, postMessageCall{channelID, text, threadTS})
	return nil
}
func (s *stubSlack) PostMessageWithButton(channelID, text, threadTS, actionID, buttonText, value string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.PostMessageWithButtonCalls = append(s.PostMessageWithButtonCalls,
		postMessageWithButtonCall{channelID, text, threadTS, actionID, buttonText, value})
	return "STATUS_TS", nil
}
func (s *stubSlack) PostSelector(channelID, prompt, actionPrefix string, options []string, threadTS string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.PostSelectorCalls = append(s.PostSelectorCalls,
		postSelectorCall{channelID, prompt, actionPrefix, threadTS, options})
	if s.PostSelectorErr != nil {
		return "", s.PostSelectorErr
	}
	return s.ts("SEL"), nil
}
func (s *stubSlack) PostSelectorWithBack(channelID, prompt, actionPrefix string, options []string, threadTS, backActionID, backLabel string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.PostSelectorWithBackCalls = append(s.PostSelectorWithBackCalls,
		postSelectorWithBackCall{channelID, prompt, actionPrefix, threadTS, backActionID, backLabel, options})
	if s.PostSelectorBackErr != nil {
		return "", s.PostSelectorBackErr
	}
	return s.ts("SELB"), nil
}
func (s *stubSlack) PostExternalSelector(channelID, prompt, actionID, placeholder, threadTS string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.PostExternalSelectorCalls = append(s.PostExternalSelectorCalls,
		postExternalSelectorCall{channelID, prompt, actionID, placeholder, threadTS})
	if s.PostExternalErr != nil {
		return "", s.PostExternalErr
	}
	return s.ts("EXT"), nil
}
func (s *stubSlack) UpdateMessage(channelID, messageTS, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.UpdateMessageCalls = append(s.UpdateMessageCalls,
		updateMessageCall{channelID, messageTS, text})
	return nil
}
func (s *stubSlack) OpenDescriptionModal(triggerID, selectorMsgTS string) error { return nil }
func (s *stubSlack) ResolveUser(userID string) string                           { return userID }
func (s *stubSlack) GetChannelName(channelID string) string                     { return channelID }
func (s *stubSlack) FetchThreadContext(channelID, threadTS, triggerTS, botUserID string, limit int) ([]slackclient.ThreadRawMessage, error) {
	return nil, nil
}
func (s *stubSlack) DownloadAttachments(messages []slackclient.ThreadRawMessage, tempDir string) []slackclient.AttachmentDownload {
	return nil
}
func (s *stubSlack) ts(prefix string) string {
	if s.NextSelectorTS != "" {
		v := s.NextSelectorTS
		s.NextSelectorTS = ""
		return v
	}
	return fmt.Sprintf("%s_%d", prefix, len(s.PostSelectorCalls)+len(s.PostSelectorWithBackCalls)+len(s.PostExternalSelectorCalls))
}

// newTestWorkflow builds a Workflow with stubs sufficient for selector tests.
func newTestWorkflow(t *testing.T, slack *stubSlack, cfg *config.Config) *Workflow {
	t.Helper()
	if cfg == nil {
		cfg = &config.Config{
			Channels:        map[string]config.ChannelConfig{},
			ChannelDefaults: config.ChannelConfig{},
		}
	}
	w := &Workflow{
		cfg:       cfg,
		slack:     slack,
		pending:   make(map[string]*pendingTriage),
		autoBound: make(map[string]bool),
	}
	return w
}

func testPending(ch, thread string, repoWasPicked bool, phase string) *pendingTriage {
	return &pendingTriage{
		ChannelID:     ch,
		ThreadTS:      thread,
		TriggerTS:     thread,
		UserID:        "U1",
		Reporter:      "U1",
		ChannelName:   "#test",
		Phase:         phase,
		RepoWasPicked: repoWasPicked,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// Satisfy queue import (used by stubSlack indirectly through interface).
var _ queue.JobQueue = nil

func truePtr() *bool { v := true; return &v }

func TestAfterRepoSelected_BackButton_WhenRepoWasPicked(t *testing.T) {
	slack := &stubSlack{}
	cfg := &config.Config{
		ChannelDefaults: config.ChannelConfig{
			Branches:     []string{"main", "develop"},
			BranchSelect: truePtr(),
		},
	}
	w := newTestWorkflow(t, slack, cfg)

	pt := testPending("C1", "T1", true, "repo_search")
	pt.SelectedRepo = "o/r"
	w.afterRepoSelected(pt, cfg.ChannelDefaults)

	if len(slack.PostSelectorWithBackCalls) != 1 {
		t.Fatalf("expected 1 PostSelectorWithBack call, got %d", len(slack.PostSelectorWithBackCalls))
	}
	c := slack.PostSelectorWithBackCalls[0]
	if c.ActionPrefix != "branch_select" {
		t.Errorf("ActionPrefix = %q, want branch_select", c.ActionPrefix)
	}
	if c.BackActionID != "back_to_repo" {
		t.Errorf("BackActionID = %q, want back_to_repo (RepoWasPicked=true)", c.BackActionID)
	}
}

func TestAfterRepoSelected_NoBackButton_WhenShortcut(t *testing.T) {
	slack := &stubSlack{}
	cfg := &config.Config{
		ChannelDefaults: config.ChannelConfig{
			Branches:     []string{"main", "develop"},
			BranchSelect: truePtr(),
		},
	}
	w := newTestWorkflow(t, slack, cfg)

	pt := testPending("C1", "T1", false, "")
	pt.SelectedRepo = "o/r"
	w.afterRepoSelected(pt, cfg.ChannelDefaults)

	if len(slack.PostSelectorWithBackCalls) != 1 {
		t.Fatalf("expected 1 PostSelectorWithBack call")
	}
	if slack.PostSelectorWithBackCalls[0].BackActionID != "" {
		t.Errorf("should not include back button for shortcut path; got %q",
			slack.PostSelectorWithBackCalls[0].BackActionID)
	}
}

func TestShowDescriptionPrompt_BackButton_WhenRepoWasPicked(t *testing.T) {
	slack := &stubSlack{}
	w := newTestWorkflow(t, slack, nil)

	pt := testPending("C1", "T1", true, "branch")
	pt.SelectedRepo = "o/r"
	pt.SelectedBranch = "main"
	w.showDescriptionPrompt(pt)

	if len(slack.PostSelectorWithBackCalls) != 1 {
		t.Fatalf("expected 1 PostSelectorWithBack call")
	}
	c := slack.PostSelectorWithBackCalls[0]
	if c.ActionPrefix != "description_action" {
		t.Errorf("ActionPrefix = %q, want description_action", c.ActionPrefix)
	}
	if c.BackActionID != "back_to_repo" {
		t.Errorf("BackActionID = %q, want back_to_repo", c.BackActionID)
	}
}

func TestShowDescriptionPrompt_NoBackButton_WhenShortcut(t *testing.T) {
	slack := &stubSlack{}
	w := newTestWorkflow(t, slack, nil)

	pt := testPending("C1", "T1", false, "")
	pt.SelectedRepo = "o/r"
	w.showDescriptionPrompt(pt)

	if len(slack.PostSelectorWithBackCalls) != 1 {
		t.Fatalf("expected 1 PostSelectorWithBack call")
	}
	if slack.PostSelectorWithBackCalls[0].BackActionID != "" {
		t.Errorf("should not include back button for shortcut path")
	}
}
