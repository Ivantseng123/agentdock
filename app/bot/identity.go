// Package bot owns the triage workflow: reading Slack threads,
// orchestrating repo selection, enqueuing jobs, and resolving results.
// Identity lives here (not in shared/) because it's only used to
// tell Slack thread messages from our own bot's posts.
package bot

// Identity holds the bot's own user_id and bot_id as returned by
// Slack's auth.test. Used by filterThreadMessages to drop our own
// status / selector / result posts from thread context.
type Identity struct {
	UserID string // e.g. "UBOTxxxxx"
	BotID  string // e.g. "BBOTxxxxx"
}
