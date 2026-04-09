package llm

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCLIProvider_Name(t *testing.T) {
	p := NewCLIProvider("test-cli", "echo", nil, 10*time.Second)
	if p.Name() != "test-cli" {
		t.Errorf("expected name test-cli, got %s", p.Name())
	}
}

func TestCLIProvider_BuildArgs_PromptPlaceholder(t *testing.T) {
	p := NewCLIProvider("test", "tool", []string{"--query", "{prompt}", "--format", "json"}, 10*time.Second)
	args, useStdin := p.buildArgs("hello world")
	expected := []string{"--query", "hello world", "--format", "json"}
	if len(args) != len(expected) {
		t.Fatalf("expected %d args, got %d: %v", len(expected), len(args), args)
	}
	for i, e := range expected {
		if args[i] != e {
			t.Errorf("args[%d]: expected %q, got %q", i, e, args[i])
		}
	}
	if useStdin {
		t.Error("expected useStdin=false when prompt fits in args")
	}
}

func TestCLIProvider_BuildArgs_NoArgs(t *testing.T) {
	p := NewCLIProvider("test", "tool", nil, 10*time.Second)
	args, useStdin := p.buildArgs("hello")
	if args != nil {
		t.Errorf("expected nil args for stdin mode, got %v", args)
	}
	if !useStdin {
		t.Error("expected useStdin=true when no args configured")
	}
}

func TestCLIProvider_BuildArgs_ArgsWithoutPlaceholder(t *testing.T) {
	p := NewCLIProvider("test", "tool", []string{"--verbose"}, 10*time.Second)
	args, useStdin := p.buildArgs("hello")
	if len(args) != 1 || args[0] != "--verbose" {
		t.Errorf("expected [--verbose], got %v", args)
	}
	if !useStdin {
		t.Error("expected useStdin=true when no placeholder in args")
	}
}

func TestParseJSONInTextResponse(t *testing.T) {
	t.Run("tool call", func(t *testing.T) {
		resp, err := parseJSONInTextResponse(`{"tool": "grep", "args": {"pattern": "Login"}}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.StopReason != StopReasonToolUse {
			t.Errorf("expected stop_reason=%q, got %q", StopReasonToolUse, resp.StopReason)
		}
		if len(resp.ToolCalls) != 1 {
			t.Fatalf("expected 1 tool call, got %d", len(resp.ToolCalls))
		}
		tc := resp.ToolCalls[0]
		if tc.Name != "grep" {
			t.Errorf("expected tool name 'grep', got %q", tc.Name)
		}
		if tc.ID == "" {
			t.Error("expected non-empty tool call ID")
		}
		var args map[string]string
		if err := json.Unmarshal(tc.Args, &args); err != nil {
			t.Fatalf("failed to unmarshal args: %v", err)
		}
		if args["pattern"] != "Login" {
			t.Errorf("expected pattern=Login, got %q", args["pattern"])
		}
	})

	t.Run("finish", func(t *testing.T) {
		resp, err := parseJSONInTextResponse(`{"tool": "finish", "result": {"summary": "found it"}}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.StopReason != StopReasonFinish {
			t.Errorf("expected stop_reason=%q, got %q", StopReasonFinish, resp.StopReason)
		}
		if len(resp.ToolCalls) != 0 {
			t.Errorf("expected no tool calls, got %d", len(resp.ToolCalls))
		}
		// Content should be the result JSON.
		var result map[string]string
		if err := json.Unmarshal([]byte(resp.Content), &result); err != nil {
			t.Fatalf("content is not valid JSON: %v (content=%q)", err, resp.Content)
		}
		if result["summary"] != "found it" {
			t.Errorf("expected summary='found it', got %q", result["summary"])
		}
	})

	t.Run("plain text", func(t *testing.T) {
		resp, err := parseJSONInTextResponse(`Just some thinking text`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.StopReason != StopReasonFinish {
			t.Errorf("expected stop_reason=%q, got %q", StopReasonFinish, resp.StopReason)
		}
		if resp.Content != "Just some thinking text" {
			t.Errorf("expected content to be plain text, got %q", resp.Content)
		}
		if len(resp.ToolCalls) != 0 {
			t.Errorf("expected no tool calls, got %d", len(resp.ToolCalls))
		}
	})

	t.Run("tool call in code block", func(t *testing.T) {
		input := "Here is my action:\n```json\n{\"tool\": \"read_file\", \"args\": {\"path\": \"main.go\"}}\n```"
		resp, err := parseJSONInTextResponse(input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.StopReason != StopReasonToolUse {
			t.Errorf("expected stop_reason=%q, got %q", StopReasonToolUse, resp.StopReason)
		}
		if len(resp.ToolCalls) != 1 {
			t.Fatalf("expected 1 tool call, got %d", len(resp.ToolCalls))
		}
		if resp.ToolCalls[0].Name != "read_file" {
			t.Errorf("expected tool name 'read_file', got %q", resp.ToolCalls[0].Name)
		}
	})

	t.Run("json without tool field", func(t *testing.T) {
		resp, err := parseJSONInTextResponse(`{"summary": "just a JSON object"}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.StopReason != StopReasonFinish {
			t.Errorf("expected stop_reason=%q, got %q", StopReasonFinish, resp.StopReason)
		}
		if len(resp.ToolCalls) != 0 {
			t.Errorf("expected no tool calls for JSON without tool field")
		}
	})
}

func TestCLIProvider_PrepareImageFiles_Claude(t *testing.T) {
	p := NewCLIProvider("test", "claude", []string{"--print", "{prompt}"}, 10*time.Second)

	images := []ImageContent{
		{Name: "a.png", MimeType: "image/png", Data: []byte("fakepng")},
		{Name: "b.jpg", MimeType: "image/jpeg", Data: []byte("fakejpg")},
	}

	tmpFiles, fileArgs := p.prepareImageFiles(images)
	defer func() {
		for _, f := range tmpFiles {
			os.Remove(f)
		}
	}()

	if len(tmpFiles) != 2 {
		t.Fatalf("expected 2 temp files, got %d", len(tmpFiles))
	}
	if len(fileArgs) != 4 {
		t.Fatalf("expected 4 file args (--file x2), got %d: %v", len(fileArgs), fileArgs)
	}

	for _, f := range tmpFiles {
		info, err := os.Stat(f)
		if err != nil {
			t.Errorf("temp file %s not found: %v", f, err)
		}
		if info.Size() == 0 {
			t.Errorf("temp file %s is empty", f)
		}
	}

	if fileArgs[0] != "--file" || fileArgs[2] != "--file" {
		t.Errorf("expected --file flags, got %v", fileArgs)
	}
}

func TestCLIProvider_PrepareImageFiles_NonClaude(t *testing.T) {
	p := NewCLIProvider("test", "some-other-tool", []string{"{prompt}"}, 10*time.Second)

	images := []ImageContent{
		{Name: "a.png", MimeType: "image/png", Data: []byte("fakepng")},
	}

	tmpFiles, fileArgs := p.prepareImageFiles(images)
	if len(tmpFiles) != 0 {
		t.Errorf("non-claude tool should not create temp files, got %d", len(tmpFiles))
	}
	if len(fileArgs) != 0 {
		t.Errorf("non-claude tool should not produce file args, got %v", fileArgs)
	}
}

func TestImageFallbackText(t *testing.T) {
	images := []ImageContent{
		{Name: "screen.png", MimeType: "image/png", Data: []byte("data")},
	}
	text := imageFallbackText(images)
	if !strings.Contains(text, "[圖片: screen.png]") {
		t.Errorf("expected fallback text, got %q", text)
	}
}
