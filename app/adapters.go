package app

import (
	"log/slog"

	slackclient "github.com/Ivantseng123/agentdock/app/slack"
)

// slackPosterAdapter wraps slackclient.Client to satisfy bot.SlackPoster interface.
// SlackPoster.PostMessage has no return value, but Client.PostMessage returns error.
type slackPosterAdapter struct {
	client *slackclient.Client
	logger *slog.Logger
}

func (a *slackPosterAdapter) PostMessage(channelID, text, threadTS string) {
	if err := a.client.PostMessage(channelID, text, threadTS); err != nil {
		a.logger.Warn("發送訊息失敗", "phase", "失敗", "channel_id", channelID, "error", err)
	}
}

func (a *slackPosterAdapter) UpdateMessage(channelID, messageTS, text string) {
	if err := a.client.UpdateMessage(channelID, messageTS, text); err != nil {
		a.logger.Warn("更新訊息失敗", "phase", "失敗", "channel_id", channelID, "error", err)
	}
}

func (a *slackPosterAdapter) PostMessageWithButton(channelID, text, threadTS, actionID, buttonText, value string) (string, error) {
	return a.client.PostMessageWithButton(channelID, text, threadTS, actionID, buttonText, value)
}
