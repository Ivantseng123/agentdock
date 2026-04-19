package slack

import (
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
