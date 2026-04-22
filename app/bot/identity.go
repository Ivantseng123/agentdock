// Package bot owns the triage workflow: reading Slack threads,
// orchestrating repo selection, enqueuing jobs, and resolving results.
// Identity lives here (not in shared/) because it's only used to
// tell Slack thread messages from our own bot's posts.
package bot

// Identity holds the bot's own user_id, bot_id, and handle as returned by
// Slack's auth.test. UserID/BotID are used by filterThreadMessages to drop
// our own status / selector / result posts from thread context. Username
// (the handle, e.g. "ai_trigger_issue_bot") is plumbed into the worker
// prompt so agents can refer to themselves by their actual Slack name
// instead of inventing persona labels.
type Identity struct {
	UserID   string // e.g. "UBOTxxxxx"
	BotID    string // e.g. "BBOTxxxxx"
	Username string // Slack handle, e.g. "ai_trigger_issue_bot"
}
