package slack

import (
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
)

// EventTypeLabel classifies a socketmode event into one of a small, fixed set
// of business event types for the slack_events_total metric. The set is
// deliberately bounded — no channel IDs, action IDs, or other unbounded
// fields ever become a label (see docs/adr/0005-metrics-prometheus.md).
//
// socketmode connection-lifecycle events (Connecting / Connected / Hello /
// Disconnect / IncomingError / ...) are not business events; they fall through
// to "unknown". A persistent non-trivial "unknown" share is the signal that a
// new business event class arrived and needs its own label here.
func EventTypeLabel(evt socketmode.Event) string {
	switch evt.Type {
	case socketmode.EventTypeEventsAPI:
		ea, ok := evt.Data.(slackevents.EventsAPIEvent)
		if !ok {
			return "unknown"
		}
		switch ea.InnerEvent.Data.(type) {
		case *slackevents.AppMentionEvent:
			return "app_mention"
		case *slackevents.MemberJoinedChannelEvent:
			return "member_joined"
		case *slackevents.MemberLeftChannelEvent:
			return "member_left"
		default:
			return "unknown"
		}
	case socketmode.EventTypeSlashCommand:
		return "slash_command"
	case socketmode.EventTypeInteractive:
		cb, ok := evt.Data.(slack.InteractionCallback)
		if !ok {
			return "unknown"
		}
		switch cb.Type {
		case slack.InteractionTypeBlockSuggestion:
			return "block_suggestion"
		case slack.InteractionTypeBlockActions:
			return "block_action"
		default:
			return "unknown"
		}
	default:
		return "unknown"
	}
}
