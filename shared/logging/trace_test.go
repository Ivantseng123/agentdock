package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/trace"
)

// ctxWithTraceID returns a ctx whose SpanContext carries the given trace ID.
// We build a SpanContext directly rather than starting a real span so the
// test stays decoupled from the SDK setup path in shared/tracing.
func ctxWithTraceID(t *testing.T, hex string) context.Context {
	t.Helper()
	tid, err := trace.TraceIDFromHex(hex)
	if err != nil {
		t.Fatalf("invalid trace ID hex %q: %v", hex, err)
	}
	sid, _ := trace.SpanIDFromHex("00f067aa0ba902b7")
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    tid,
		SpanID:     sid,
		TraceFlags: trace.FlagsSampled,
		Remote:     true,
	})
	return trace.ContextWithSpanContext(context.Background(), sc)
}

func TestTraceIDHandler_InjectsAttrFromOTelSpanContext(t *testing.T) {
	var buf bytes.Buffer
	h := NewTraceIDHandler(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	logger := slog.New(h)

	const traceHex = "4bf92f3577b34da6a3ce929d0e0e4736"
	logger.InfoContext(ctxWithTraceID(t, traceHex), "hello")

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("invalid JSON: %v\noutput: %s", err, buf.String())
	}
	if entry[KeyTraceID] != traceHex {
		t.Errorf("trace_id = %v, want %q", entry[KeyTraceID], traceHex)
	}
}

func TestTraceIDHandler_NoSpanOnCtxNoAttr(t *testing.T) {
	var buf bytes.Buffer
	h := NewTraceIDHandler(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	logger := slog.New(h)

	// No OTel span on ctx — handler should emit no trace_id attr.
	logger.InfoContext(context.Background(), "no span")

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("invalid JSON: %v\noutput: %s", err, buf.String())
	}
	if _, ok := entry[KeyTraceID]; ok {
		t.Errorf("trace_id should be absent when ctx has no span, got %v", entry[KeyTraceID])
	}
}

// TestTraceIDHandler_NonContextCallNoAttr asserts that Info/Warn/Error
// (the non-Context variants) do not surface a trace_id even when set on
// ambient ctx — slog only forwards ctx through *Context variants. Per
// ADR-0004 this is acceptable; call sites that want trace correlation
// must use InfoContext / WarnContext / ErrorContext.
func TestTraceIDHandler_NonContextCallNoAttr(t *testing.T) {
	var buf bytes.Buffer
	h := NewTraceIDHandler(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	logger := slog.New(h)

	logger.Info("non-context call") // never plumbs ctx

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("invalid JSON: %v\noutput: %s", err, buf.String())
	}
	if _, ok := entry[KeyTraceID]; ok {
		t.Errorf("non-context call must not emit trace_id, got %v", entry[KeyTraceID])
	}
}

func TestTraceIDHandler_PassesThroughMultiHandler(t *testing.T) {
	const traceHex = "00112233445566778899aabbccddeeff"

	var stderr, file bytes.Buffer
	multi := NewMultiHandler(
		slog.NewTextHandler(&stderr, &slog.HandlerOptions{Level: slog.LevelInfo}),
		slog.NewJSONHandler(&file, &slog.HandlerOptions{Level: slog.LevelInfo}),
	)
	logger := slog.New(NewTraceIDHandler(multi))
	logger.InfoContext(ctxWithTraceID(t, traceHex), "msg")

	if !strings.Contains(stderr.String(), "trace_id="+traceHex) {
		t.Errorf("stderr missing trace_id: %s", stderr.String())
	}
	var entry map[string]any
	if err := json.Unmarshal(file.Bytes(), &entry); err != nil {
		t.Fatalf("invalid file JSON: %v", err)
	}
	if entry[KeyTraceID] != traceHex {
		t.Errorf("file trace_id = %v, want %q", entry[KeyTraceID], traceHex)
	}
}

func TestTraceIDHandler_WithAttrs_PreservesWrap(t *testing.T) {
	const traceHex = "11223344556677889900aabbccddeeff"

	var buf bytes.Buffer
	base := NewTraceIDHandler(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	logger := slog.New(base).With("component", "Test")
	logger.InfoContext(ctxWithTraceID(t, traceHex), "after-with")

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if entry[KeyTraceID] != traceHex {
		t.Errorf("trace_id lost after WithAttrs: %v", entry[KeyTraceID])
	}
	if entry["component"] != "Test" {
		t.Errorf("component lost: %v", entry["component"])
	}
}
