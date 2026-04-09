package bot

import (
	"strings"
	"testing"

	"slack-issue-bot/internal/config"
)

func TestBuildPrompt_Basic(t *testing.T) {
	input := PromptInput{
		ThreadMessages: []ThreadMessage{
			{User: "Alice", Timestamp: "2026-04-09 10:30", Text: "Login page is broken"},
			{User: "Bob", Timestamp: "2026-04-09 10:32", Text: "Same here"},
		},
		RepoPath: "/repos/owner/repo",
		Branch:   "main",
		Prompt: config.PromptConfig{
			Language: "zh-TW",
		},
	}
	result := BuildPrompt(input)
	if !strings.Contains(result, "Alice (2026-04-09 10:30)") {
		t.Error("missing Alice's message")
	}
	if !strings.Contains(result, "Login page is broken") {
		t.Error("missing message text")
	}
	if !strings.Contains(result, "/repos/owner/repo") {
		t.Error("missing repo path")
	}
	if !strings.Contains(result, "main") {
		t.Error("missing branch")
	}
	if !strings.Contains(result, "triage-issue") {
		t.Error("missing skill reference")
	}
	if !strings.Contains(result, "zh-TW") {
		t.Error("missing language")
	}
}

func TestBuildPrompt_WithAttachments(t *testing.T) {
	input := PromptInput{
		ThreadMessages: []ThreadMessage{
			{User: "Alice", Timestamp: "10:30", Text: "see screenshot"},
		},
		Attachments: []AttachmentInfo{
			{Path: "/tmp/triage-abc/screenshot.png", Name: "screenshot.png", Type: "image"},
			{Path: "/tmp/triage-abc/error.log", Name: "error.log", Type: "text"},
		},
		RepoPath: "/repos/owner/repo",
		Prompt:   config.PromptConfig{Language: "en"},
	}
	result := BuildPrompt(input)
	if !strings.Contains(result, "screenshot.png") {
		t.Error("missing image attachment")
	}
	if !strings.Contains(result, "error.log") {
		t.Error("missing text attachment")
	}
}

func TestBuildPrompt_WithExtraRules(t *testing.T) {
	input := PromptInput{
		ThreadMessages: []ThreadMessage{
			{User: "Alice", Timestamp: "10:30", Text: "test"},
		},
		RepoPath: "/repos/owner/repo",
		Prompt: config.PromptConfig{
			Language:   "zh-TW",
			ExtraRules: []string{"no guessing", "only real files"},
		},
	}
	result := BuildPrompt(input)
	if !strings.Contains(result, "no guessing") {
		t.Error("missing extra rule 1")
	}
	if !strings.Contains(result, "only real files") {
		t.Error("missing extra rule 2")
	}
}

func TestBuildPrompt_WithExtraDescription(t *testing.T) {
	input := PromptInput{
		ThreadMessages: []ThreadMessage{
			{User: "Alice", Timestamp: "10:30", Text: "it's broken"},
		},
		ExtraDescription: "It happens on the login page after entering wrong password 3 times",
		RepoPath:         "/repos/owner/repo",
		Prompt:           config.PromptConfig{Language: "en"},
	}
	result := BuildPrompt(input)
	if !strings.Contains(result, "It happens on the login page") {
		t.Error("missing extra description")
	}
}
