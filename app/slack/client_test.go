package slack

import (
	"strings"
	"testing"

	"github.com/slack-go/slack"
)

func TestFilterThreadMessages(t *testing.T) {
	messages := []slack.Message{
		{Msg: slack.Msg{User: "U001", Text: "bug report", Timestamp: "1000.0"}},
		{Msg: slack.Msg{User: "UBOT", Text: "analyzing...", Timestamp: "1001.0", BotID: "B123"}},
		{Msg: slack.Msg{User: "U002", Text: "me too", Timestamp: "1002.0"}},
		{Msg: slack.Msg{User: "U001", Text: "@bot", Timestamp: "1003.0"}},
	}

	result := filterThreadMessages(messages, "1003.0", "UBOT")
	if len(result) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result))
	}
	if result[0].Text != "bug report" {
		t.Errorf("msg[0] = %q", result[0].Text)
	}
	if result[1].Text != "me too" {
		t.Errorf("msg[1] = %q", result[1].Text)
	}
}

func TestClassifyAttachment(t *testing.T) {
	tests := []struct {
		filetype string
		mimetype string
		want     string
	}{
		{"png", "image/png", "image"},
		{"jpg", "image/jpeg", "image"},
		{"gif", "image/gif", "image"},
		{"text", "text/plain", "text"},
		{"csv", "text/csv", "text"},
		{"log", "text/plain", "text"},
		{"xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "document"},
		{"pdf", "application/pdf", "document"},
		{"binary", "application/octet-stream", "document"},
	}
	for _, tt := range tests {
		t.Run(tt.filetype, func(t *testing.T) {
			got := classifyAttachment(tt.filetype, tt.mimetype)
			if got != tt.want {
				t.Errorf("classifyAttachment(%q, %q) = %q, want %q", tt.filetype, tt.mimetype, got, tt.want)
			}
		})
	}
}

func TestExtractKeywords(t *testing.T) {
	tests := []struct {
		name     string
		message  string
		minWords int
	}{
		{
			name:     "short message",
			message:  "login page crashes",
			minWords: 2,
		},
		{
			name:     "long message with stop words",
			message:  "The user is unable to login after the latest deploy and the page shows a white screen",
			minWords: 3,
		},
		{
			name:     "empty message",
			message:  "",
			minWords: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kw := ExtractKeywords(tt.message)
			if len(kw) < tt.minWords {
				t.Errorf("expected at least %d keywords, got %d: %v", tt.minWords, len(kw), kw)
			}
			stopWords := map[string]bool{"the": true, "is": true, "a": true, "and": true, "to": true, "after": true}
			for _, w := range kw {
				if stopWords[w] {
					t.Errorf("keyword %q is a stop word", w)
				}
			}
		})
	}
}

func TestExtractMessageText_PrefersText(t *testing.T) {
	msg := slack.Message{Msg: slack.Msg{
		Text:        "primary",
		Attachments: []slack.Attachment{{Fallback: "fallback"}},
	}}
	got := extractMessageText(msg)
	if got != "primary" {
		t.Errorf("got %q, want primary", got)
	}
}

func TestExtractMessageText_BlocksFallback(t *testing.T) {
	blocks := slack.Blocks{BlockSet: []slack.Block{
		slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn", "hello world", false, false),
			nil, nil,
		),
	}}
	msg := slack.Message{Msg: slack.Msg{Text: "", Blocks: blocks}}
	got := extractMessageText(msg)
	if !strings.Contains(got, "hello world") {
		t.Errorf("got %q, want containing 'hello world'", got)
	}
}

func TestExtractMessageText_AttachmentsFallback(t *testing.T) {
	msg := slack.Message{Msg: slack.Msg{
		Text: "",
		Attachments: []slack.Attachment{{
			Pretext: "pre",
			Title:   "title",
			Text:    "body",
			Fields: []slack.AttachmentField{
				{Title: "Env", Value: "prod"},
			},
		}},
	}}
	got := extractMessageText(msg)
	for _, want := range []string{"pre", "title", "body", "Env", "prod"} {
		if !strings.Contains(got, want) {
			t.Errorf("got %q, missing %q", got, want)
		}
	}
}

func TestExtractMessageText_BlocksWinOverAttachments(t *testing.T) {
	blocks := slack.Blocks{BlockSet: []slack.Block{
		slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn", "from-blocks", false, false),
			nil, nil,
		),
	}}
	msg := slack.Message{Msg: slack.Msg{
		Text:        "",
		Blocks:      blocks,
		Attachments: []slack.Attachment{{Fallback: "from-attach"}},
	}}
	got := extractMessageText(msg)
	if !strings.Contains(got, "from-blocks") {
		t.Errorf("got %q, want blocks content preferred", got)
	}
	if strings.Contains(got, "from-attach") {
		t.Errorf("got %q, should not include attachment when blocks present", got)
	}
}

func TestExtractMessageText_AllEmpty(t *testing.T) {
	msg := slack.Message{Msg: slack.Msg{}}
	if got := extractMessageText(msg); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestExtractMessageText_RichTextQuoteAndPreformatted(t *testing.T) {
	msg := slack.Message{Msg: slack.Msg{Blocks: slack.Blocks{BlockSet: []slack.Block{
		&slack.RichTextBlock{
			Type: slack.MBTRichText,
			Elements: []slack.RichTextElement{
				&slack.RichTextQuote{
					Type: slack.RTEQuote,
					Elements: []slack.RichTextSectionElement{
						&slack.RichTextSectionTextElement{Type: slack.RTSEText, Text: "quoted text"},
					},
				},
				&slack.RichTextPreformatted{
					Type: slack.RTEPreformatted,
					Elements: []slack.RichTextSectionElement{
						&slack.RichTextSectionTextElement{Type: slack.RTSEText, Text: "preformatted text"},
					},
				},
			},
		},
	}}}}
	got := extractMessageText(msg)
	for _, want := range []string{"quoted text", "preformatted text"} {
		if !strings.Contains(got, want) {
			t.Errorf("got %q, missing %q", got, want)
		}
	}
}

func TestResolveBotDisplayName_PrefersBotProfileName(t *testing.T) {
	m := slack.Message{Msg: slack.Msg{
		BotProfile: &slack.BotProfile{Name: "GitHub"},
		Username:   "github[bot]",
		BotID:      "B123",
	}}
	if got := resolveBotDisplayName(m); got != "GitHub" {
		t.Errorf("got %q, want GitHub", got)
	}
}

func TestResolveBotDisplayName_FallsBackToUsername(t *testing.T) {
	m := slack.Message{Msg: slack.Msg{
		Username: "my-webhook",
		BotID:    "B123",
	}}
	if got := resolveBotDisplayName(m); got != "my-webhook" {
		t.Errorf("got %q, want my-webhook", got)
	}
}

func TestResolveBotDisplayName_FallsBackToBotID(t *testing.T) {
	m := slack.Message{Msg: slack.Msg{
		BotID: "B123",
	}}
	if got := resolveBotDisplayName(m); got != "B123" {
		t.Errorf("got %q, want B123", got)
	}
}

func TestResolveBotDisplayName_ReturnsEmptyForNonBot(t *testing.T) {
	m := slack.Message{Msg: slack.Msg{}}
	if got := resolveBotDisplayName(m); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}
