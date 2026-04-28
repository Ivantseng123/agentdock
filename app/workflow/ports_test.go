package workflow

import (
	"context"
	"fmt"

	slackclient "github.com/Ivantseng123/agentdock/app/slack"
)

// fakeSlackPort is a test double for SlackPort. All methods are no-ops that
// record what was posted so tests can make assertions.
type fakeSlackPort struct {
	Posted    []string
	Selectors []string
	Modal     bool

	// PriorBotAnswer is the canned response for FetchPriorBotAnswer. Tests
	// that exercise the multi-turn opt-in path set this to a non-nil
	// ThreadRawMessage so the workflow renders the opt-in button.
	PriorBotAnswer      *slackclient.ThreadRawMessage
	PriorBotAnswerErr   error
	PriorBotAnswerCalls int
}

func newFakeSlackPort() *fakeSlackPort { return &fakeSlackPort{} }

func (f *fakeSlackPort) PostMessage(ch, text, ts string) error {
	f.Posted = append(f.Posted, text)
	return nil
}

func (f *fakeSlackPort) PostMessageWithTS(ch, text, ts string) (string, error) {
	f.Posted = append(f.Posted, text)
	return "ts", nil
}

func (f *fakeSlackPort) PostMessageWithButton(ch, text, ts, aid, bt, val string) (string, error) {
	f.Posted = append(f.Posted, text)
	return "ts", nil
}

func (f *fakeSlackPort) UpdateMessage(ch, mts, text string) error {
	f.Posted = append(f.Posted, text)
	return nil
}

func (f *fakeSlackPort) DeleteMessage(ch, mts string) error { return nil }

func (f *fakeSlackPort) UpdateMessageWithButton(ch, mts, text, aid, bt, val string) error {
	f.Posted = append(f.Posted, text)
	return nil
}

func (f *fakeSlackPort) PostSmartSelector(ch, ts string, spec SelectorSpec) (string, error) {
	f.Selectors = append(f.Selectors, spec.Prompt)
	return "sel-ts", nil
}

func (f *fakeSlackPort) OpenTextInputModal(tid, title, label, name, metadata string) error {
	f.Modal = true
	return nil
}

func (f *fakeSlackPort) ResolveUser(uid string) string       { return "user-" + uid }
func (f *fakeSlackPort) GetChannelName(cid string) string    { return "ch-" + cid }

func (f *fakeSlackPort) FetchThreadContext(c, ts, tts string, lim int) ([]slackclient.ThreadRawMessage, error) {
	return nil, nil
}

// PriorBotAnswer is the canned response for FetchPriorBotAnswer calls. nil
// (the default) models "no prior answer in thread" so existing tests behave
// as before. PriorBotAnswerCalls counts invocations so tests can verify
// fetch-once semantics in the Ask workflow.
func (f *fakeSlackPort) FetchPriorBotAnswer(c, ts, tts string, lim int) (*slackclient.ThreadRawMessage, error) {
	f.PriorBotAnswerCalls++
	if f.PriorBotAnswerErr != nil {
		return nil, f.PriorBotAnswerErr
	}
	return f.PriorBotAnswer, nil
}

func (f *fakeSlackPort) DownloadAttachments(msgs []slackclient.ThreadRawMessage, dir string) []slackclient.AttachmentDownload {
	return nil
}

func (f *fakeSlackPort) UploadFile(channelID, threadTS, filename, title, content, initialComment string) error {
	// Record the file body in Posted so tests that verify "the answer reached
	// Slack" don't care whether the answer went inline or into a file.
	f.Posted = append(f.Posted, content)
	if initialComment != "" {
		f.Posted = append(f.Posted, initialComment)
	}
	return nil
}

// fakeIssueCreator is a test double for IssueCreator.
type fakeIssueCreator struct {
	URL     string
	LastArg string
	// Captured fields from the most recent CreateIssue call. Tests that need
	// to assert on exact title/body/label content after redaction inspect
	// these directly instead of parsing LastArg.
	LastTitle  string
	LastBody   string
	LastLabels []string
	// err, when non-nil, is returned by CreateIssue instead of a URL.
	err error
}

func (f *fakeIssueCreator) CreateIssue(ctx context.Context, owner, repo, title, body string, labels []string) (string, error) {
	f.LastArg = fmt.Sprintf("%s/%s %s", owner, repo, title)
	f.LastTitle = title
	f.LastBody = body
	f.LastLabels = append([]string(nil), labels...)
	if f.err != nil {
		return "", f.err
	}
	if f.URL == "" {
		f.URL = fmt.Sprintf("https://github.com/%s/%s/issues/1", owner, repo)
	}
	return f.URL, nil
}
