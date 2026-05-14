package agent

import (
	"strings"
	"testing"
)

func TestDecodeOpencodeBusEvent_NoOps(t *testing.T) {
	cases := []string{
		`{"type":"server.connected","properties":{}}`,
		`{"type":"server.heartbeat","properties":{}}`,
		`{"type":"message.updated","properties":{"info":{"id":"m1","role":"assistant"}}}`,
		`{"type":"session.status","properties":{"sessionID":"s1","status":{"type":"idle"}}}`,
		`{"type":"session.error","properties":{"sessionID":"s1","error":{"name":"X"}}}`,
		`{"type":"message.part.delta","properties":{"field":"text","delta":"abc"}}`,
		`{"type":"unknown.future.event","properties":{}}`,
	}
	for _, in := range cases {
		t.Run(in[:min(40, len(in))], func(t *testing.T) {
			ev, ok, err := decodeOpencodeBusEvent([]byte(in))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ok {
				t.Errorf("expected ok=false for %q, got ev=%+v", in, ev)
			}
		})
	}
}

func TestDecodeOpencodeBusEvent_TextPartUpdate(t *testing.T) {
	in := `{"type":"message.part.updated","properties":{"part":{"type":"text","text":"hello world"}}}`
	ev, ok, err := decodeOpencodeBusEvent([]byte(in))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true for text part update")
	}
	if ev.Type != "message_delta" {
		t.Errorf("Type = %q, want message_delta", ev.Type)
	}
	if ev.TextBytes != len("hello world") {
		t.Errorf("TextBytes = %d, want %d", ev.TextBytes, len("hello world"))
	}
}

func TestDecodeOpencodeBusEvent_ToolPartUpdate(t *testing.T) {
	in := `{"type":"message.part.updated","properties":{"part":{"type":"tool","tool":"read","state":{"input":{"file_path":"/etc/hostname"}}}}}`
	ev, ok, err := decodeOpencodeBusEvent([]byte(in))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true for tool part update")
	}
	if ev.Type != "tool_use" {
		t.Errorf("Type = %q, want tool_use", ev.Type)
	}
	if ev.ToolName != "Read" {
		t.Errorf("ToolName = %q, want Read (Pascal-cased)", ev.ToolName)
	}
	if ev.ToolInputFirstArg != "/etc/hostname" {
		t.Errorf("ToolInputFirstArg = %q, want /etc/hostname", ev.ToolInputFirstArg)
	}
}

func TestDecodeOpencodeBusEvent_ToolWithoutState(t *testing.T) {
	in := `{"type":"message.part.updated","properties":{"part":{"type":"tool","tool":"bash"}}}`
	ev, ok, err := decodeOpencodeBusEvent([]byte(in))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true for tool part update without state")
	}
	if ev.Type != "tool_use" {
		t.Errorf("Type = %q, want tool_use", ev.Type)
	}
	if ev.ToolName != "Bash" {
		t.Errorf("ToolName = %q, want Bash", ev.ToolName)
	}
	if ev.ToolInputFirstArg != "" {
		t.Errorf("ToolInputFirstArg = %q, want empty (no input)", ev.ToolInputFirstArg)
	}
}

func TestDecodeOpencodeBusEvent_StepFinish(t *testing.T) {
	in := `{"type":"message.part.updated","properties":{"part":{"type":"step-finish","tokens":{"input":120,"output":80},"cost":0.0023}}}`
	ev, ok, err := decodeOpencodeBusEvent([]byte(in))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true for step-finish")
	}
	if ev.Type != "result" {
		t.Errorf("Type = %q, want result", ev.Type)
	}
	if ev.InputTokens != 120 || ev.OutputTokens != 80 {
		t.Errorf("tokens = (%d,%d), want (120,80)", ev.InputTokens, ev.OutputTokens)
	}
	if ev.CostUSD != 0.0023 {
		t.Errorf("CostUSD = %v, want 0.0023", ev.CostUSD)
	}
}

func TestDecodeOpencodeBusEvent_ReasoningAndStepStartIgnored(t *testing.T) {
	cases := []string{
		`{"type":"message.part.updated","properties":{"part":{"type":"reasoning","text":"thinking..."}}}`,
		`{"type":"message.part.updated","properties":{"part":{"type":"step-start"}}}`,
	}
	for _, in := range cases {
		t.Run(in[:min(60, len(in))], func(t *testing.T) {
			_, ok, err := decodeOpencodeBusEvent([]byte(in))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if ok {
				t.Errorf("expected ok=false for unsupported part type")
			}
		})
	}
}

func TestDecodeOpencodeBusEvent_InvalidJSON(t *testing.T) {
	_, _, err := decodeOpencodeBusEvent([]byte(`{not-json}`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestFirstArgFromInput_PriorityAndTruncation(t *testing.T) {
	t.Run("priority-order-file-path", func(t *testing.T) {
		got := firstArgFromInput(map[string]any{"command": "ls", "file_path": "/a/b"})
		if got != "/a/b" {
			t.Errorf("got %q, want /a/b (file_path beats command)", got)
		}
	})
	t.Run("filePath-camel-case", func(t *testing.T) {
		got := firstArgFromInput(map[string]any{"filePath": "/c/d"})
		if got != "/c/d" {
			t.Errorf("got %q, want /c/d", got)
		}
	})
	t.Run("nil-input-empty", func(t *testing.T) {
		if firstArgFromInput(nil) != "" {
			t.Error("nil input should return empty string")
		}
	})
	t.Run("truncation-to-100-runes", func(t *testing.T) {
		long := strings.Repeat("a", 200)
		got := firstArgFromInput(map[string]any{"path": long})
		runes := []rune(got)
		if len(runes) != 100 {
			t.Errorf("got %d runes, want 100", len(runes))
		}
		if runes[99] != '…' {
			t.Errorf("expected last rune '…', got %q", string(runes[99]))
		}
	})
}

func TestTitleCaseFirstASCII(t *testing.T) {
	cases := map[string]string{
		"":      "",
		"read":  "Read",
		"bash":  "Bash",
		"Read":  "Read", // already cap; unchanged
		"123":   "123",
		"αlpha": "αlpha", // non-ASCII first byte; unchanged
	}
	for in, want := range cases {
		if got := titleCaseFirstASCII(in); got != want {
			t.Errorf("titleCaseFirstASCII(%q) = %q, want %q", in, got, want)
		}
	}
}

