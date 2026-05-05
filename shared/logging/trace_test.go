package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestWithTraceID_RoundTrip(t *testing.T) {
	ctx := WithTraceID(context.Background(), "abc123")
	if got := TraceIDFrom(ctx); got != "abc123" {
		t.Errorf("TraceIDFrom = %q, want %q", got, "abc123")
	}
}

func TestWithTraceID_EmptyIsNoop(t *testing.T) {
	ctx := WithTraceID(context.Background(), "")
	if got := TraceIDFrom(ctx); got != "" {
		t.Errorf("empty WithTraceID stored %q, want empty", got)
	}
}

func TestTraceIDFrom_NilCtx(t *testing.T) {
	var ctx context.Context // intentionally nil — guard against SA1012 by routing via variable
	if got := TraceIDFrom(ctx); got != "" {
		t.Errorf("TraceIDFrom(nil) = %q, want empty", got)
	}
}

func TestTraceIDHandler_InjectsAttrFromCtx(t *testing.T) {
	var buf bytes.Buffer
	h := NewTraceIDHandler(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	logger := slog.New(h)

	ctx := WithTraceID(context.Background(), "trace-xyz")
	logger.InfoContext(ctx, "hello")

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("invalid JSON: %v\noutput: %s", err, buf.String())
	}
	if entry[KeyTraceID] != "trace-xyz" {
		t.Errorf("trace_id = %v, want %q", entry[KeyTraceID], "trace-xyz")
	}
}

func TestTraceIDHandler_AbsentCtxNoAttr(t *testing.T) {
	var buf bytes.Buffer
	h := NewTraceIDHandler(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	logger := slog.New(h)

	logger.Info("no ctx")

	var entry map[string]any
	json.Unmarshal(buf.Bytes(), &entry)
	if _, ok := entry[KeyTraceID]; ok {
		t.Errorf("trace_id should be absent when ctx empty, got %v", entry[KeyTraceID])
	}
}

func TestTraceIDHandler_PassesThroughMultiHandler(t *testing.T) {
	// trace_id must reach BOTH inner handlers when wrapped before MultiHandler.
	var stderr, file bytes.Buffer
	multi := NewMultiHandler(
		slog.NewTextHandler(&stderr, &slog.HandlerOptions{Level: slog.LevelInfo}),
		slog.NewJSONHandler(&file, &slog.HandlerOptions{Level: slog.LevelInfo}),
	)
	logger := slog.New(NewTraceIDHandler(multi))

	ctx := WithTraceID(context.Background(), "tid-1")
	logger.InfoContext(ctx, "msg")

	if !strings.Contains(stderr.String(), "trace_id=tid-1") {
		t.Errorf("stderr missing trace_id: %s", stderr.String())
	}
	var entry map[string]any
	json.Unmarshal(file.Bytes(), &entry)
	if entry[KeyTraceID] != "tid-1" {
		t.Errorf("file trace_id = %v, want %q", entry[KeyTraceID], "tid-1")
	}
}

func TestTraceIDHandler_WithAttrs_PreservesWrap(t *testing.T) {
	var buf bytes.Buffer
	base := NewTraceIDHandler(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	logger := slog.New(base).With("component", "Test")

	ctx := WithTraceID(context.Background(), "tid-2")
	logger.InfoContext(ctx, "after-with")

	var entry map[string]any
	json.Unmarshal(buf.Bytes(), &entry)
	if entry[KeyTraceID] != "tid-2" {
		t.Errorf("trace_id lost after WithAttrs: %v", entry[KeyTraceID])
	}
	if entry["component"] != "Test" {
		t.Errorf("component lost: %v", entry["component"])
	}
}
