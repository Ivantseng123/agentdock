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

func TestBaseAttrs_StyledIsEmpty(t *testing.T) {
	if attrs := BaseAttrs("styled", "agentdock", "1.0.0", "abc"); attrs != nil {
		t.Errorf("styled mode should not emit base attrs, got %v", attrs)
	}
}

func TestBaseAttrs_JSONHasMappedKeys(t *testing.T) {
	t.Setenv("POD_NAME", "agentdock-app-0")
	attrs := BaseAttrs("json", "agentdock", "1.2.3", "deadbeef")
	want := map[string]string{
		KeyAppName:    "agentdock",
		KeyAppVersion: "1.2.3",
		KeyAppCommit:  "deadbeef",
		KeyPodName:    "agentdock-app-0",
	}
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

func TestBaseAttrs_OmitsBlanks(t *testing.T) {
	t.Setenv("POD_NAME", "")
	attrs := BaseAttrs("json", "agentdock", "", "")
	if len(attrs) != 1 || attrs[0].Key != KeyAppName {
		t.Errorf("blank version/commit/pod should be omitted, got %v", attrs)
	}
}
