package workflow

import (
	"context"

	slackclient "github.com/Ivantseng123/agentdock/app/slack"
	ghclient "github.com/Ivantseng123/agentdock/shared/github"
)

// SlackPort is the narrow Slack surface each workflow + the dispatcher need.
// Mirrors the app/bot.slackAPI surface but is owned here so the workflow
// package does not import app/bot.
type SlackPort interface {
	PostMessage(channelID, text, threadTS string) error
	PostMessageWithTS(channelID, text, threadTS string) (string, error)
	PostMessageWithButton(channelID, text, threadTS, actionID, buttonText, value string) (string, error)
	UpdateMessage(channelID, messageTS, text string) error
	DeleteMessage(channelID, messageTS string) error
	UpdateMessageWithButton(channelID, messageTS, text, actionID, buttonText, value string) error
	PostSmartSelector(channelID, threadTS string, spec SelectorSpec) (string, error)
	OpenTextInputModal(triggerID, title, label, inputName, metadata string) error
	ResolveUser(userID string) string
	GetChannelName(channelID string) string
	FetchThreadContext(channelID, threadTS, triggerTS string, limit int) ([]slackclient.ThreadRawMessage, error)
	DownloadAttachments(messages []slackclient.ThreadRawMessage, tempDir string) []slackclient.AttachmentDownload
	UploadFile(channelID, threadTS, filename, title, content, initialComment string) error
}

// IssueCreator abstracts GitHub issue creation. Only IssueWorkflow consumes
// this; the interface lives in the workflow package because that is where
// its single consumer lives.
type IssueCreator interface {
	CreateIssue(ctx context.Context, owner, repo, title, body string, labels []string) (string, error)
}

// GitHubPR abstracts the PR endpoints PR Review needs for URL validation.
// PRReviewWorkflow uses this to verify a URL references a real, accessible PR
// before submitting work. The concrete type (shared/github.Client) lives in
// shared/github so the module-import direction (shared cannot import app) is
// preserved; the struct moved along with it to shared/github/pr_types.go.
type GitHubPR interface {
	GetPullRequest(ctx context.Context, owner, repo string, number int) (*ghclient.PullRequest, error)
}
