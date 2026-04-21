package workflow

import (
	"context"

	slackclient "github.com/Ivantseng123/agentdock/app/slack"
)

// SlackPort is the narrow Slack surface each workflow + the dispatcher need.
// Mirrors the app/bot.slackAPI surface but is owned here so the workflow
// package does not import app/bot.
type SlackPort interface {
	PostMessage(channelID, text, threadTS string) error
	PostMessageWithTS(channelID, text, threadTS string) (string, error)
	PostMessageWithButton(channelID, text, threadTS, actionID, buttonText, value string) (string, error)
	UpdateMessage(channelID, messageTS, text string) error
	UpdateMessageWithButton(channelID, messageTS, text, actionID, buttonText, value string) error
	PostSelector(channelID, prompt, actionPrefix string, options []string, threadTS string) (string, error)
	PostSelectorWithBack(channelID, prompt, actionPrefix string, options []string, threadTS, backActionID, backLabel string) (string, error)
	PostExternalSelector(channelID, prompt, actionID, placeholder, threadTS string) (string, error)
	OpenTextInputModal(triggerID, title, label, inputName, metadata string) error
	ResolveUser(userID string) string
	GetChannelName(channelID string) string
	FetchThreadContext(channelID, threadTS, triggerTS, botUserID string, limit int) ([]slackclient.ThreadRawMessage, error)
	DownloadAttachments(messages []slackclient.ThreadRawMessage, tempDir string) []slackclient.AttachmentDownload
}

// IssueCreator abstracts GitHub issue creation. Only IssueWorkflow consumes
// this; the interface lives in the workflow package because that is where
// its single consumer lives.
type IssueCreator interface {
	CreateIssue(ctx context.Context, owner, repo, title, body string, labels []string) (string, error)
}

// GitHubPR abstracts the PR endpoints PR Review needs for URL validation.
// PRReviewWorkflow uses this to verify a URL references a real, accessible PR
// before submitting work.
type GitHubPR interface {
	GetPullRequest(ctx context.Context, owner, repo string, number int) (*PullRequest, error)
}

// PullRequest is the subset of the GitHub PR payload we care about. Field
// names match the GitHub REST response so shared/github can populate this
// from its httpGet directly.
type PullRequest struct {
	Number  int    `json:"number"`
	State   string `json:"state"` // "open" / "closed"
	Draft   bool   `json:"draft"`
	Merged  bool   `json:"merged"`
	Title   string `json:"title"`
	HTMLURL string `json:"html_url"`
	Head    struct {
		Ref  string `json:"ref"`
		SHA  string `json:"sha"`
		Repo struct {
			FullName string `json:"full_name"`
			CloneURL string `json:"clone_url"`
		} `json:"repo"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}
