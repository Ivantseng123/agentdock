package slack

import (
	"testing"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
)

func eventsAPI(inner any) socketmode.Event {
	return socketmode.Event{
		Type: socketmode.EventTypeEventsAPI,
		Data: slackevents.EventsAPIEvent{
			InnerEvent: slackevents.EventsAPIInnerEvent{Data: inner},
		},
	}
}

func interactive(t slack.InteractionType) socketmode.Event {
	return socketmode.Event{
		Type: socketmode.EventTypeInteractive,
		Data: slack.InteractionCallback{Type: t},
	}
}

func TestEventTypeLabel(t *testing.T) {
	cases := []struct {
		name string
		evt  socketmode.Event
		want string
	}{
		// --- the 7 business event types ---
		{"app_mention", eventsAPI(&slackevents.AppMentionEvent{}), "app_mention"},
		{"member_joined", eventsAPI(&slackevents.MemberJoinedChannelEvent{}), "member_joined"},
		{"member_left", eventsAPI(&slackevents.MemberLeftChannelEvent{}), "member_left"},
		{"slash_command", socketmode.Event{Type: socketmode.EventTypeSlashCommand}, "slash_command"},
		{"block_suggestion", interactive(slack.InteractionTypeBlockSuggestion), "block_suggestion"},
		{"block_action", interactive(slack.InteractionTypeBlockActions), "block_action"},

		// --- unknown fallbacks ---
		{"connection_lifecycle_connecting", socketmode.Event{Type: socketmode.EventTypeConnecting}, "unknown"},
		{"connection_lifecycle_hello", socketmode.Event{Type: socketmode.EventTypeHello}, "unknown"},
		{"events_api_wrong_data_shape", socketmode.Event{Type: socketmode.EventTypeEventsAPI, Data: "not an EventsAPIEvent"}, "unknown"},
		{"events_api_unrecognised_inner", eventsAPI(&slackevents.MessageEvent{}), "unknown"},
		{"interactive_wrong_data_shape", socketmode.Event{Type: socketmode.EventTypeInteractive, Data: 42}, "unknown"},
		{"interactive_other_type", interactive(slack.InteractionTypeShortcut), "unknown"},
		{"zero_value", socketmode.Event{}, "unknown"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := EventTypeLabel(c.evt); got != c.want {
				t.Errorf("EventTypeLabel(%s) = %q, want %q", c.name, got, c.want)
			}
		})
	}
}
