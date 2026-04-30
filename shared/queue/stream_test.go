package queue

import (
	"strings"
	"testing"
)

func TestReadStreamJSON_ResultEvent(t *testing.T) {
	input := `{"type":"assistant","message":{"content":[{"type":"text","text":"Looking at code..."}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/src/main.go"}}]}}
{"type":"assistant","message":{"content":[{"type":"text","text":"Found the issue..."}]}}
{"type":"result","result":"Final answer text here","total_cost_usd":0.042,"usage":{"input_tokens":8500,"output_tokens":1200}}`

	r := strings.NewReader(input)
	eventCh := make(chan StreamEvent, 100)
	text := ReadStreamJSON(r, eventCh)
	close(eventCh)

	if text != "Final answer text here" {
		t.Errorf("text = %q, want 'Final answer text here'", text)
	}

	var events []StreamEvent
	for e := range eventCh {
		events = append(events, e)
	}
	if len(events) < 3 {
		t.Fatalf("expected at least 3 events, got %d", len(events))
	}

	found := false
	for _, e := range events {
		if e.Type == "tool_use" && e.ToolName == "Read" {
			found = true
		}
	}
	if !found {
		t.Error("missing tool_use:Read event")
	}

	var resultEvent StreamEvent
	for _, e := range events {
		if e.Type == "result" {
			resultEvent = e
		}
	}
	if resultEvent.CostUSD != 0.042 {
		t.Errorf("cost = %f, want 0.042", resultEvent.CostUSD)
	}
	if resultEvent.InputTokens != 8500 {
		t.Errorf("input_tokens = %d, want 8500", resultEvent.InputTokens)
	}
}

func TestReadStreamJSON_FallbackToReassembly(t *testing.T) {
	input := `{"type":"assistant","message":{"content":[{"type":"text","text":"Hello "}]}}
{"type":"assistant","message":{"content":[{"type":"text","text":"World"}]}}`

	r := strings.NewReader(input)
	eventCh := make(chan StreamEvent, 100)
	text := ReadStreamJSON(r, eventCh)
	close(eventCh)

	if text != "Hello World" {
		t.Errorf("reassembled text = %q, want 'Hello World'", text)
	}
}

func TestReadRawOutput(t *testing.T) {
	input := "line1\nline2\nline3"
	r := strings.NewReader(input)
	text := ReadRawOutput(r)
	if !strings.Contains(text, "line1") || !strings.Contains(text, "line3") {
		t.Errorf("raw output = %q", text)
	}
}

func TestReadStreamJSON_ToolInputFirstArg(t *testing.T) {
	// Verify the tool_use event surfaces input.file_path through the parser
	// into StreamEvent.ToolInputFirstArg. Other key recognition is unit-tested
	// in TestExtractFirstArg_*; this end-to-end path is the integration check.
	input := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"src/foo/bar.go"}}]}}`

	r := strings.NewReader(input)
	eventCh := make(chan StreamEvent, 10)
	ReadStreamJSON(r, eventCh)
	close(eventCh)

	var got StreamEvent
	for e := range eventCh {
		if e.Type == "tool_use" {
			got = e
			break
		}
	}
	if got.ToolName != "Read" {
		t.Errorf("ToolName = %q, want Read", got.ToolName)
	}
	if got.ToolInputFirstArg != "src/foo/bar.go" {
		t.Errorf("ToolInputFirstArg = %q, want src/foo/bar.go", got.ToolInputFirstArg)
	}
}

func TestExtractFirstArg_Empty(t *testing.T) {
	if got := extractFirstArg(nil); got != "" {
		t.Errorf("nil input: got %q, want empty", got)
	}
	if got := extractFirstArg(map[string]any{}); got != "" {
		t.Errorf("empty input: got %q, want empty", got)
	}
}

func TestExtractFirstArg_RecognizedKeys(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]any
		want string
	}{
		{"file_path (Read)", map[string]any{"file_path": "src/foo.go"}, "src/foo.go"},
		{"command (Bash)", map[string]any{"command": "git status"}, "git status"},
		{"pattern (Grep)", map[string]any{"pattern": "TODO"}, "TODO"},
		{"path (LS)", map[string]any{"path": "/tmp"}, "/tmp"},
		{"url (WebFetch)", map[string]any{"url": "https://example.com"}, "https://example.com"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := extractFirstArg(c.in); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestExtractFirstArg_PriorityOrder(t *testing.T) {
	// file_path takes priority over command and others. Real claude tool_use
	// objects only carry one of these at a time, but ordering guards future
	// shape drift.
	in := map[string]any{
		"file_path": "src/foo.go",
		"command":   "git status",
		"pattern":   "TODO",
	}
	if got := extractFirstArg(in); got != "src/foo.go" {
		t.Errorf("got %q, want file_path winner", got)
	}
}

func TestExtractFirstArg_UnknownKey(t *testing.T) {
	in := map[string]any{"unknown_field": "value"}
	if got := extractFirstArg(in); got != "" {
		t.Errorf("unknown key should yield empty: got %q", got)
	}
}

func TestExtractFirstArg_NonStringValue(t *testing.T) {
	// Type-assert to string fails silently when value is some other type
	// (e.g. nested object). Must not panic; returns empty.
	in := map[string]any{"file_path": 123}
	if got := extractFirstArg(in); got != "" {
		t.Errorf("non-string value should yield empty: got %q", got)
	}
}

func TestExtractFirstArg_Truncated(t *testing.T) {
	long := strings.Repeat("a", toolInputArgMaxLen+50)
	in := map[string]any{"command": long}
	got := extractFirstArg(in)
	gotRunes := []rune(got)
	if len(gotRunes) != toolInputArgMaxLen {
		t.Errorf("len = %d runes, want %d", len(gotRunes), toolInputArgMaxLen)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncated value should end with marker: got %q", got)
	}
}

func TestTruncateRunes(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want string
	}{
		{"short", 100, "short"},
		{"abcdefghij", 5, "abcd…"},
		{"中文太長要截斷", 5, "中文太長…"},
		{"exactly5", 8, "exactly5"},
	}
	for _, c := range cases {
		if got := truncateRunes(c.in, c.max); got != c.want {
			t.Errorf("truncateRunes(%q, %d) = %q, want %q", c.in, c.max, got, c.want)
		}
	}
}
