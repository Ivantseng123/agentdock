package logging

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestBuildStderrHandler_JSONShape(t *testing.T) {
	var buf bytes.Buffer
	h := buildStderrHandler(&buf, "json", slog.LevelInfo)
	logger := slog.New(h)
	logger.Info("hello")

	out := buf.String()
	if !strings.Contains(out, `"msg":"hello"`) {
		t.Errorf("json mode should emit JSON, got: %s", out)
	}
}

func TestBuildStderrHandler_StyledFallback(t *testing.T) {
	var buf bytes.Buffer
	for _, format := range []string{"styled", "", "anything-unknown"} {
		buf.Reset()
		h := buildStderrHandler(&buf, format, slog.LevelInfo)
		slog.New(h).Info("hi", "k", "v")
		if !strings.Contains(buf.String(), "k=v") {
			t.Errorf("format %q expected styled output, got %s", format, buf.String())
		}
	}
}

func TestStderrBaseAttrs_StyledIsEmpty(t *testing.T) {
	if attrs := StderrBaseAttrs("styled", "agentdock", "1.0.0", "abc"); attrs != nil {
		t.Errorf("styled mode should not emit base attrs, got %v", attrs)
	}
}

func TestStderrBaseAttrs_JSONHasMappedKeys(t *testing.T) {
	t.Setenv("POD_NAME", "agentdock-app-0")
	assertAttrs(t, StderrBaseAttrs("json", "agentdock", "1.2.3", "deadbeef"), map[string]string{
		KeyAppName:    "agentdock",
		KeyAppVersion: "1.2.3",
		KeyAppCommit:  "deadbeef",
		KeyPodName:    "agentdock-app-0",
	})
}

func TestFileBaseAttrs_AlwaysPopulated(t *testing.T) {
	t.Setenv("POD_NAME", "agentdock-app-0")
	// File handler should not gate on stderr_format — operator running in
	// styled mode still needs build provenance on file records for
	// post-mortem analysis.
	assertAttrs(t, FileBaseAttrs("agentdock", "1.2.3", "deadbeef"), map[string]string{
		KeyAppName:    "agentdock",
		KeyAppVersion: "1.2.3",
		KeyAppCommit:  "deadbeef",
		KeyPodName:    "agentdock-app-0",
	})
}

func TestBaseAttrs_OmitsBlanks(t *testing.T) {
	t.Setenv("POD_NAME", "")
	attrs := FileBaseAttrs("agentdock", "", "")
	if len(attrs) != 1 || attrs[0].Key != KeyAppName {
		t.Errorf("blank version/commit/pod should be omitted, got %v", attrs)
	}
}

func assertAttrs(t *testing.T, attrs []slog.Attr, want map[string]string) {
	t.Helper()
	got := map[string]string{}
	for _, a := range attrs {
		got[a.Key] = a.Value.String()
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("attr %q = %q, want %q", k, got[k], v)
		}
	}
}
