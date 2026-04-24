package slack

import (
	"fmt"
	"strings"
	"time"

	"github.com/Ivantseng123/agentdock/shared/metrics"
	"github.com/slack-go/slack"
)

// botAnswerMinChars is the minimum text length (in runes) for a bot post
// to be treated as a substantive answer. Shorter messages are typically
// acks, status lines, or one-liners that add no signal to multi-turn
// continuity. 50 is a judgment call — tune via feedback.
const botAnswerMinChars = 50

// botNonAnswerEmojiPrefixes is the blocklist of leading Slack emoji codes
// that identify bot posts as UI scaffolding rather than substantive
// answers. Any bot message whose (trimmed) text starts with one of these
// is excluded from PriorAnswer.
//
// The categories:
//   - selector prompts: :question: :point_right: :pencil2:
//   - user-pick acks:   :white_check_mark:
//   - skip breadcrumbs: :fast_forward: :leftwards_arrow_with_hook:
//   - status lines:     :mag: :thinking_face:
//   - error lines:      :warning: :x:
//   - user-echo quote:  :memo: (Ask's "補充說明" is the user's own words, not
//                       a bot answer — the modal-derived extra_description
//                       path already carries that signal)
var botNonAnswerEmojiPrefixes = []string{
	":question:",
	":point_right:",
	":pencil2:",
	":white_check_mark:",
	":fast_forward:",
	":leftwards_arrow_with_hook:",
	":mag:",
	":thinking_face:",
	":memo:",
	":warning:",
	":x:",
}

// isBotAnswerMessage reports whether a bot-authored Slack message text
// qualifies as a substantive answer worth replaying to the next turn.
//
// Rules:
//  1. Trim whitespace.
//  2. Reject if empty.
//  3. Reject if the text begins with any blocklisted emoji prefix.
//  4. Reject if shorter than botAnswerMinChars runes (UI noise guard).
//
// The predicate is deliberately conservative — false negatives (dropping
// an ambiguous "好的，馬上處理") are cheaper than false positives
// (feeding a status line back as "previous answer" and confusing the
// worker).
func isBotAnswerMessage(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	for _, prefix := range botNonAnswerEmojiPrefixes {
		if strings.HasPrefix(trimmed, prefix) {
			return false
		}
	}
	if runeLen(trimmed) < botAnswerMinChars {
		return false
	}
	return true
}

// runeLen counts Unicode code points. len(s) counts bytes, which
// underestimates English character count for CJK text that most Slack
// answers in this workspace are written in.
func runeLen(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}

// filterPriorBotAnswers returns bot-authored messages from the thread
// that pass isBotAnswerMessage, in the order Slack returned them (usually
// chronological). Messages at or after triggerTS are excluded. Bot
// identity is matched against botUserID or botID; at least one must be
// non-empty for any match to succeed — passing both empty returns nil.
//
// Unlike filterThreadMessages (which drops the bot's own posts), this
// helper is the inverse: it keeps only the bot's posts. Other-bot
// messages (e.g., GitHub, CI) are not considered — PriorAnswer is
// about this bot's own conversational memory.
func filterPriorBotAnswers(messages []slack.Message, triggerTS, botUserID, botID string) []ThreadRawMessage {
	if botUserID == "" && botID == "" {
		return nil
	}
	var result []ThreadRawMessage
	for _, m := range messages {
		if m.Timestamp >= triggerTS {
			continue
		}
		// Identify self-posts. Match either axis because integration quirks
		// can leave User mismatched while BotID matches, or vice versa.
		isSelf := false
		if botUserID != "" && m.User == botUserID {
			isSelf = true
		}
		if botID != "" && m.BotID == botID {
			isSelf = true
		}
		if !isSelf {
			continue
		}
		text := extractMessageText(m)
		if !isBotAnswerMessage(text) {
			continue
		}
		user := m.User
		if m.BotID != "" {
			if name := resolveBotDisplayName(m); name != "" {
				user = "bot:" + name
			}
		}
		result = append(result, ThreadRawMessage{
			User:      user,
			Text:      text,
			Timestamp: m.Timestamp,
			Files:     m.Files,
		})
	}
	return result
}

// FetchPriorBotAnswer reads the thread and returns the most recent
// qualifying bot-authored answer (by Slack timestamp), or nil when
// none is found. limit defaults to 50 when <=0 — matching
// FetchThreadContext's default so a cap-misconfig doesn't diverge
// silently between the two paths.
//
// This exists so the Ask workflow can enable multi-turn continuity
// without re-fetching thread pages on every job submit: the Ask
// description-prompt step calls this once to decide whether to show
// the "帶上次回覆" opt-in button, and caches the result for BuildJob.
func (c *Client) FetchPriorBotAnswer(channelID, threadTS, triggerTS, botUserID, botID string, limit int) (*ThreadRawMessage, error) {
	start := time.Now()
	if limit <= 0 {
		limit = 50
	}

	var allMessages []slack.Message
	cursor := ""

	for {
		params := &slack.GetConversationRepliesParameters{
			ChannelID: channelID,
			Timestamp: threadTS,
			Cursor:    cursor,
			Limit:     200,
		}

		msgs, hasMore, nextCursor, err := c.api.GetConversationReplies(params)
		if err != nil {
			metrics.ExternalErrorsTotal.WithLabelValues("slack", "fetch_prior_bot_answer").Inc()
			return nil, fmt.Errorf("conversations.replies: %w", err)
		}

		allMessages = append(allMessages, msgs...)

		if !hasMore || len(allMessages) >= limit {
			break
		}
		cursor = nextCursor
	}

	filtered := filterPriorBotAnswers(allMessages, triggerTS, botUserID, botID)
	metrics.ExternalDuration.WithLabelValues("slack", "fetch_prior_bot_answer").Observe(time.Since(start).Seconds())

	if len(filtered) == 0 {
		return nil, nil
	}
	// Most recent by thread order. Slack returns messages oldest-first,
	// so the last element is freshest.
	latest := filtered[len(filtered)-1]
	return &latest, nil
}
