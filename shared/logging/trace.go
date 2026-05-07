package logging

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel/trace"
)

// TraceIDHandler wraps a slog.Handler so every record carries a trace_id
// attribute pulled from the active OpenTelemetry SpanContext on ctx.
//
// Per ADR-0004, this handler is the *only* path that injects trace_id —
// the legacy WithTraceID / TraceIDFrom helpers were removed. Records
// emitted through non-Context slog calls (Info/Warn/Error/Debug) still
// flow through with no trace_id, and that is now considered acceptable
// (those call sites must migrate to *Context variants if they want
// trace correlation).
type TraceIDHandler struct {
	inner slog.Handler
}

// NewTraceIDHandler wraps inner so emitted records gain a trace_id
// attribute when ctx carries an OTel SpanContext.
func NewTraceIDHandler(inner slog.Handler) *TraceIDHandler {
	return &TraceIDHandler{inner: inner}
}

func (h *TraceIDHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *TraceIDHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(slog.String(KeyTraceID, sc.TraceID().String()))
	}
	return h.inner.Handle(ctx, r)
}

func (h *TraceIDHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &TraceIDHandler{inner: h.inner.WithAttrs(attrs)}
}

func (h *TraceIDHandler) WithGroup(name string) slog.Handler {
	return &TraceIDHandler{inner: h.inner.WithGroup(name)}
}
