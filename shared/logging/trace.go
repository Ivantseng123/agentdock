package logging

import (
	"context"
	"log/slog"
)

type traceIDKey struct{}

// WithTraceID returns a copy of ctx tagged with the given trace ID. Empty IDs
// are ignored — the returned ctx is the input unchanged. The value flows to
// log records emitted via Info/Warn/Error/DebugContext through TraceIDHandler.
func WithTraceID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, traceIDKey{}, id)
}

// TraceIDFrom returns the trace ID stored on ctx, or "" if absent.
func TraceIDFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(traceIDKey{}).(string)
	return v
}

// TraceIDHandler wraps a slog.Handler so every record carries a trace_id
// attribute pulled from ctx (set via WithTraceID). Records emitted through
// non-Context slog calls (Info/Warn/Error/Debug) flow through with no
// trace_id — those entry-point sites need to migrate to *Context variants
// for trace_id to surface.
type TraceIDHandler struct {
	inner slog.Handler
}

// NewTraceIDHandler wraps inner so emitted records gain a trace_id attribute
// when ctx carries one.
func NewTraceIDHandler(inner slog.Handler) *TraceIDHandler {
	return &TraceIDHandler{inner: inner}
}

func (h *TraceIDHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *TraceIDHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := TraceIDFrom(ctx); id != "" {
		r.AddAttrs(slog.String(KeyTraceID, id))
	}
	return h.inner.Handle(ctx, r)
}

func (h *TraceIDHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &TraceIDHandler{inner: h.inner.WithAttrs(attrs)}
}

func (h *TraceIDHandler) WithGroup(name string) slog.Handler {
	return &TraceIDHandler{inner: h.inner.WithGroup(name)}
}
