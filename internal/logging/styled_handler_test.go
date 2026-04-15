package logging

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func newTestStyledHandler(buf *bytes.Buffer) *StyledTextHandler {
	return NewStyledTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
}

// TestStyledHandler_ComponentAndPhase verifies that [Component][Phase] prefix is rendered,
// the message and extra attrs appear, and "component"/"phase" are NOT in the key=value list.
func TestStyledHandler_ComponentAndPhase(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(newTestStyledHandler(&buf)).With("component", CompSlack)

	logger.Info("hello world", "phase", PhaseReceive, "key", "val")

	out := buf.String()
	if !strings.Contains(out, "[Slack][接收]") {
		t.Errorf("expected [Slack][接收] in output, got: %s", out)
	}
	if !strings.Contains(out, "hello world") {
		t.Errorf("expected message in output, got: %s", out)
	}
	if !strings.Contains(out, "key=val") {
		t.Errorf("expected key=val in output, got: %s", out)
	}
	if strings.Contains(out, "component=") {
		t.Errorf("component should NOT appear as key=val, got: %s", out)
	}
	if strings.Contains(out, "phase=") {
		t.Errorf("phase should NOT appear as key=val, got: %s", out)
	}
}

// TestStyledHandler_ComponentOnly verifies [Component] prefix with no phase bracket.
func TestStyledHandler_ComponentOnly(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(newTestStyledHandler(&buf)).With("component", CompGitHub)

	logger.Info("repo cloned", "repo", "myrepo")

	out := buf.String()
	if !strings.Contains(out, "[GitHub]") {
		t.Errorf("expected [GitHub] in output, got: %s", out)
	}
	// Must NOT have empty [] after [GitHub]
	if strings.Contains(out, "[GitHub][]") {
		t.Errorf("unexpected empty [] after [GitHub], got: %s", out)
	}
	if strings.Contains(out, "component=") {
		t.Errorf("component should NOT appear as key=val, got: %s", out)
	}
}

// TestStyledHandler_PhaseOnly verifies that when phase is in record but component is absent,
// the output contains [Phase] prefix.
func TestStyledHandler_PhaseOnly(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(newTestStyledHandler(&buf))

	logger.Info("done", "phase", PhaseComplete)

	out := buf.String()
	if !strings.Contains(out, "[完成]") {
		t.Errorf("expected [完成] in output, got: %s", out)
	}
	if strings.Contains(out, "phase=") {
		t.Errorf("phase should NOT appear as key=val, got: %s", out)
	}
}

// TestStyledHandler_NoComponentNoPhase verifies plain message with no brackets at all.
func TestStyledHandler_NoComponentNoPhase(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(newTestStyledHandler(&buf))

	logger.Info("plain message", "foo", "bar")

	out := buf.String()
	if !strings.Contains(out, "plain message") {
		t.Errorf("expected message in output, got: %s", out)
	}
	if strings.Contains(out, "[") {
		t.Errorf("expected NO brackets in output, got: %s", out)
	}
	if !strings.Contains(out, "foo=bar") {
		t.Errorf("expected foo=bar in output, got: %s", out)
	}
}

// TestStyledHandler_TimeFormat verifies the time is formatted as HH:MM (no date).
func TestStyledHandler_TimeFormat(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(newTestStyledHandler(&buf))

	now := time.Now()
	logger.Info("time check")

	out := buf.String()
	// Expect current HH:MM prefix
	expectedPrefix := now.Format("15:04")
	if !strings.Contains(out, expectedPrefix) {
		t.Errorf("expected time prefix %q in output, got: %s", expectedPrefix, out)
	}
	// Must NOT contain a date like 2006-01-02
	if strings.Contains(out, now.Format("2006-01-02")) {
		t.Errorf("output should NOT contain date, got: %s", out)
	}
}

// TestStyledHandler_LevelAlignment verifies INFO and WARN are padded to 5 chars.
func TestStyledHandler_LevelAlignment(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(newTestStyledHandler(&buf))

	logger.Info("info msg")
	logger.Warn("warn msg")

	out := buf.String()
	if !strings.Contains(out, "INFO ") {
		t.Errorf("expected 'INFO ' (5 chars) in output, got: %s", out)
	}
	if !strings.Contains(out, "WARN ") {
		t.Errorf("expected 'WARN ' (5 chars) in output, got: %s", out)
	}
}

// TestStyledHandler_WithGroup verifies that WithGroup prefixes attribute keys
// and the component is still extracted correctly.
func TestStyledHandler_WithGroup(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(newTestStyledHandler(&buf).WithGroup("grp")).With("component", CompAgent)

	logger.Info("agent started", "k", "v")

	out := buf.String()
	if !strings.Contains(out, "[Agent]") {
		t.Errorf("expected [Agent] in output, got: %s", out)
	}
	if !strings.Contains(out, "grp.k=v") {
		t.Errorf("expected grp.k=v in output, got: %s", out)
	}
	if strings.Contains(out, "component=") {
		t.Errorf("component should NOT appear as key=val, got: %s", out)
	}
}
