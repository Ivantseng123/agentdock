package app

import (
	"log/slog"

	"github.com/Ivantseng123/agentdock/app/bot"
	slackclient "github.com/Ivantseng123/agentdock/app/slack"
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

func (a *slackAdapterPort) UpdateMessageWithButton(channelID, messageTS, text, actionID, buttonText, value string) error {
	return a.client.UpdateMessageWithButton(channelID, messageTS, text, actionID, buttonText, value)
}

func (a *slackAdapterPort) PostSelector(channelID, prompt, actionPrefix string, options []string, threadTS string) (string, error) {
	return a.client.PostSelector(channelID, prompt, actionPrefix, options, threadTS)
}

func (a *slackAdapterPort) PostSelectorWithBack(channelID, prompt, actionPrefix string, options []string, threadTS, backActionID, backLabel string) (string, error) {
	return a.client.PostSelectorWithBack(channelID, prompt, actionPrefix, options, threadTS, backActionID, backLabel)
}

func (a *slackAdapterPort) PostExternalSelector(channelID, prompt, actionID, placeholder, threadTS string) (string, error) {
	return a.client.PostExternalSelector(channelID, prompt, actionID, placeholder, threadTS)
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

func (a *slackAdapterPort) DownloadAttachments(messages []slackclient.ThreadRawMessage, tempDir string) []slackclient.AttachmentDownload {
	return a.client.DownloadAttachments(messages, tempDir)
}
