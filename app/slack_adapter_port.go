package app

import (
	"log/slog"

	"github.com/Ivantseng123/agentdock/app/bot"
	slackclient "github.com/Ivantseng123/agentdock/app/slack"
	"github.com/Ivantseng123/agentdock/app/workflow"
)

// slackAdapterPort wraps *slackclient.Client to satisfy workflow.SlackPort.
// All methods delegate directly. The adapter holds the bot identity so
// FetchThreadContext can filter our own posts without every caller having
// to thread those IDs through.
type slackAdapterPort struct {
	client   *slackclient.Client
	logger   *slog.Logger
	identity bot.Identity
}

func (a *slackAdapterPort) PostMessage(channelID, text, threadTS string) error {
	return a.client.PostMessage(channelID, text, threadTS)
}

func (a *slackAdapterPort) PostMessageWithTS(channelID, text, threadTS string) (string, error) {
	return a.client.PostMessageWithTS(channelID, text, threadTS)
}

func (a *slackAdapterPort) PostMessageWithButton(channelID, text, threadTS, actionID, buttonText, value string) (string, error) {
	return a.client.PostMessageWithButton(channelID, text, threadTS, actionID, buttonText, value)
}

func (a *slackAdapterPort) UpdateMessage(channelID, messageTS, text string) error {
	return a.client.UpdateMessage(channelID, messageTS, text)
}

func (a *slackAdapterPort) DeleteMessage(channelID, messageTS string) error {
	return a.client.DeleteMessage(channelID, messageTS)
}

func (a *slackAdapterPort) UpdateMessageWithButton(channelID, messageTS, text, actionID, buttonText, value string) error {
	return a.client.UpdateMessageWithButton(channelID, messageTS, text, actionID, buttonText, value)
}

func (a *slackAdapterPort) PostSmartSelector(channelID, threadTS string, spec workflow.SelectorSpec) (string, error) {
	return a.client.PostSmartSelector(channelID, threadTS, toSlackSelectorSpec(spec))
}

// toSlackSelectorSpec copies the workflow-layer SelectorSpec into the
// slack-layer SmartSelectorSpec field-for-field. The two types stay in sync
// so the workflow package never imports the slack client; this helper is the
// only translation point.
func toSlackSelectorSpec(spec workflow.SelectorSpec) slackclient.SmartSelectorSpec {
	opts := make([]slackclient.SmartSelectorOption, len(spec.Options))
	for i, o := range spec.Options {
		opts[i] = slackclient.SmartSelectorOption{Label: o.Label, Value: o.Value}
	}
	return slackclient.SmartSelectorSpec{
		Prompt:         spec.Prompt,
		ActionID:       spec.ActionID,
		Options:        opts,
		Searchable:     spec.Searchable,
		Placeholder:    spec.Placeholder,
		BackActionID:   spec.BackActionID,
		BackLabel:      spec.BackLabel,
		CancelActionID: spec.CancelActionID,
		CancelLabel:    spec.CancelLabel,
	}
}

func (a *slackAdapterPort) OpenTextInputModal(triggerID, title, label, inputName, metadata string) error {
	return a.client.OpenTextInputModal(triggerID, title, label, inputName, metadata)
}

func (a *slackAdapterPort) ResolveUser(userID string) string {
	return a.client.ResolveUser(userID)
}

func (a *slackAdapterPort) GetChannelName(channelID string) string {
	return a.client.GetChannelName(channelID)
}

func (a *slackAdapterPort) FetchThreadContext(channelID, threadTS, triggerTS string, limit int) ([]slackclient.ThreadRawMessage, error) {
	return a.client.FetchThreadContext(channelID, threadTS, triggerTS, a.identity.UserID, a.identity.BotID, limit)
}

func (a *slackAdapterPort) FetchPriorBotAnswer(channelID, threadTS, triggerTS string, limit int) (*slackclient.ThreadRawMessage, error) {
	return a.client.FetchPriorBotAnswer(channelID, threadTS, triggerTS, a.identity.UserID, a.identity.BotID, limit)
}

func (a *slackAdapterPort) DownloadAttachments(messages []slackclient.ThreadRawMessage, tempDir string) []slackclient.AttachmentDownload {
	return a.client.DownloadAttachments(messages, tempDir)
}

func (a *slackAdapterPort) UploadFile(channelID, threadTS, filename, title, content, initialComment string) error {
	return a.client.UploadFile(channelID, threadTS, filename, title, content, initialComment)
}
