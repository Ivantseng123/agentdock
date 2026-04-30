package queue

import (
	"strings"
	"testing"
)

func TestReadStreamJSONClaude_ResultEvent(t *testing.T) {
	input := `{"type":"assistant","message":{"content":[{"type":"text","text":"Looking at code..."}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/src/main.go"}}]}}
{"type":"assistant","message":{"content":[{"type":"text","text":"Found the issue..."}]}}
{"type":"result","result":"Final answer text here","total_cost_usd":0.042,"usage":{"input_tokens":8500,"output_tokens":1200}}`

	r := strings.NewReader(input)
	eventCh := make(chan StreamEvent, 100)
	text := ReadStreamJSONClaude(r, eventCh)
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

func TestReadStreamJSONClaude_FallbackToReassembly(t *testing.T) {
	input := `{"type":"assistant","message":{"content":[{"type":"text","text":"Hello "}]}}
{"type":"assistant","message":{"content":[{"type":"text","text":"World"}]}}`

	r := strings.NewReader(input)
	eventCh := make(chan StreamEvent, 100)
	text := ReadStreamJSONClaude(r, eventCh)
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

func TestReadStreamJSONClaude_ToolInputFirstArg(t *testing.T) {
	// Verify the tool_use event surfaces input.file_path through the parser
	// into StreamEvent.ToolInputFirstArg. Other key recognition is unit-tested
	// in TestExtractFirstArg_*; this end-to-end path is the integration check.
	input := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"src/foo/bar.go"}}]}}`

	r := strings.NewReader(input)
	eventCh := make(chan StreamEvent, 10)
	ReadStreamJSONClaude(r, eventCh)
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

// TestReadStreamJSONOpencode_ReadTool fixture is a minimized capture from
// `opencode 1.14.29 run --pure --format json` reading a small file. Confirms
// the opencode parser surfaces tool_use events with title-cased tool name and
// the camelCase filePath argument.
func TestReadStreamJSONOpencode_ReadTool(t *testing.T) {
	input := `{"type":"step_start","sessionID":"ses_x","part":{"type":"step-start"}}
{"type":"tool_use","sessionID":"ses_x","part":{"type":"tool","tool":"read","callID":"call_a","state":{"status":"completed","input":{"filePath":"/tmp/test.txt"}}}}
{"type":"text","sessionID":"ses_x","part":{"type":"text","text":"The magic number is 42."}}
{"type":"step_finish","sessionID":"ses_x","part":{"type":"step-finish","tokens":{"input":18000,"output":120},"cost":0.0123}}`

	r := strings.NewReader(input)
	eventCh := make(chan StreamEvent, 100)
	text := ReadStreamJSONOpencode(r, eventCh)
	close(eventCh)

	if text != "The magic number is 42." {
		t.Errorf("text = %q, want 'The magic number is 42.'", text)
	}

	var events []StreamEvent
	for e := range eventCh {
		events = append(events, e)
	}

	var toolUse, result StreamEvent
	for _, e := range events {
		switch e.Type {
		case "tool_use":
			toolUse = e
		case "result":
			result = e
		}
	}
	if toolUse.ToolName != "Read" {
		t.Errorf("ToolName = %q, want Read (title-cased)", toolUse.ToolName)
	}
	if toolUse.ToolInputFirstArg != "/tmp/test.txt" {
		t.Errorf("ToolInputFirstArg = %q, want /tmp/test.txt", toolUse.ToolInputFirstArg)
	}
	if result.CostUSD != 0.0123 {
		t.Errorf("cost = %f, want 0.0123", result.CostUSD)
	}
	if result.InputTokens != 18000 {
		t.Errorf("input_tokens = %d, want 18000", result.InputTokens)
	}
	if result.OutputTokens != 120 {
		t.Errorf("output_tokens = %d, want 120", result.OutputTokens)
	}
}

// TestReadStreamJSONOpencode_BashTool confirms `bash` tool's `command` input
// key is recognized by extractFirstArg (shared with claude convention).
func TestReadStreamJSONOpencode_BashTool(t *testing.T) {
	input := `{"type":"tool_use","sessionID":"ses_x","part":{"type":"tool","tool":"bash","callID":"call_b","state":{"status":"completed","input":{"command":"echo hello-bash","description":"Print hello"}}}}`

	r := strings.NewReader(input)
	eventCh := make(chan StreamEvent, 10)
	ReadStreamJSONOpencode(r, eventCh)
	close(eventCh)

	var got StreamEvent
	for e := range eventCh {
		if e.Type == "tool_use" {
			got = e
			break
		}
	}
	if got.ToolName != "Bash" {
		t.Errorf("ToolName = %q, want Bash", got.ToolName)
	}
	if got.ToolInputFirstArg != "echo hello-bash" {
		t.Errorf("ToolInputFirstArg = %q, want 'echo hello-bash'", got.ToolInputFirstArg)
	}
}

// TestReadStreamJSONOpencode_GrepTool confirms `grep` tool's `pattern` key
// (shared with claude convention).
func TestReadStreamJSONOpencode_GrepTool(t *testing.T) {
	input := `{"type":"tool_use","sessionID":"ses_x","part":{"type":"tool","tool":"grep","callID":"call_c","state":{"status":"completed","input":{"pattern":"TODO"}}}}`

	r := strings.NewReader(input)
	eventCh := make(chan StreamEvent, 10)
	ReadStreamJSONOpencode(r, eventCh)
	close(eventCh)

	var got StreamEvent
	for e := range eventCh {
		if e.Type == "tool_use" {
			got = e
			break
		}
	}
	if got.ToolName != "Grep" {
		t.Errorf("ToolName = %q, want Grep", got.ToolName)
	}
	if got.ToolInputFirstArg != "TODO" {
		t.Errorf("ToolInputFirstArg = %q, want TODO", got.ToolInputFirstArg)
	}
}

// TestReadStreamJSONOpencode_TokenAccumulation confirms tokens summed across
// multiple step_finish events (opencode emits one per step, not one terminal
// total like claude).
func TestReadStreamJSONOpencode_TokenAccumulation(t *testing.T) {
	input := `{"type":"step_finish","part":{"tokens":{"input":1000,"output":50},"cost":0.01}}
{"type":"step_finish","part":{"tokens":{"input":200,"output":30},"cost":0.005}}`

	r := strings.NewReader(input)
	eventCh := make(chan StreamEvent, 10)
	ReadStreamJSONOpencode(r, eventCh)
	close(eventCh)

	var result StreamEvent
	for e := range eventCh {
		if e.Type == "result" {
			result = e
		}
	}
	if result.InputTokens != 1200 {
		t.Errorf("input_tokens = %d, want 1200 (1000+200)", result.InputTokens)
	}
	if result.OutputTokens != 80 {
		t.Errorf("output_tokens = %d, want 80 (50+30)", result.OutputTokens)
	}
	if result.CostUSD != 0.015 {
		t.Errorf("cost = %f, want 0.015 (0.01+0.005)", result.CostUSD)
	}
}

// TestReadStreamJSONOpencode_TextReassembly confirms output text is built
// from `text` events (opencode has no terminal result.result field).
func TestReadStreamJSONOpencode_TextReassembly(t *testing.T) {
	input := `{"type":"text","part":{"type":"text","text":"Hello "}}
{"type":"text","part":{"type":"text","text":"World"}}`

	r := strings.NewReader(input)
	eventCh := make(chan StreamEvent, 10)
	text := ReadStreamJSONOpencode(r, eventCh)
	close(eventCh)

	if text != "Hello World" {
		t.Errorf("text = %q, want 'Hello World'", text)
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
		{"file_path (claude Read)", map[string]any{"file_path": "src/foo.go"}, "src/foo.go"},
		{"filePath (opencode read)", map[string]any{"filePath": "src/foo.go"}, "src/foo.go"},
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
	// file_path takes priority over command and others. Real tool_use
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

// TestExtractFirstArg_SnakeBeatsCamel guards the snake-vs-camel priority. If
// a tool input ever carries BOTH file_path and filePath (extremely
// unlikely), claude's snake form wins because claude is the more
// widely-tested upstream.
func TestExtractFirstArg_SnakeBeatsCamel(t *testing.T) {
	in := map[string]any{
		"filePath":  "camel.go",
		"file_path": "snake.go",
	}
	if got := extractFirstArg(in); got != "snake.go" {
		t.Errorf("got %q, want snake.go (file_path wins)", got)
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

func TestTitleCaseTool(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"read", "Read"},
		{"bash", "Bash"},
		{"grep", "Grep"},
		{"webfetch", "Webfetch"}, // simple cap; not "WebFetch" — that's a known cosmetic loss
		{"Read", "Read"},         // already title-cased
		{"", ""},                 // empty pass-through
		{"中文", "中文"},             // non-ASCII pass-through
	}
	for _, c := range cases {
		if got := titleCaseTool(c.in); got != c.want {
			t.Errorf("titleCaseTool(%q) = %q, want %q", c.in, got, c.want)
		}
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
