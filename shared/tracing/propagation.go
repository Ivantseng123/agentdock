package tracing

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// W3C TraceContext propagator used internally by Inject/Extract. We do not
// rely on the global otel.GetTextMapPropagator() so callers don't have to
// configure a global before this package works.
var traceContextPropagator = propagation.TraceContext{}

// InjectFromContext returns the W3C `traceparent` string for the active
// span on ctx. Returns "" when no active span is present — callers should
// store the empty string verbatim on the carrier (e.g. Job.Traceparent)
// so the empty case is distinguishable from a malformed value downstream.
func InjectFromContext(ctx context.Context) string {
	carrier := propagation.MapCarrier{}
	traceContextPropagator.Inject(ctx, carrier)
	return carrier.Get("traceparent")
}

// ExtractToContext parses a W3C `traceparent` string and returns ctx with
// the SpanContext set. Empty input returns ctx unchanged. Malformed input
// is silently dropped — the OTel TraceContext propagator does not surface
// parse errors, and per ADR-0001 we never want a stray bad header to
// disturb the caller's flow. A debug-level log is emitted when a non-empty
// value fails to produce a valid SpanContext, so a split trace tree can
// still be diagnosed without leaking the bad value (length only).
func ExtractToContext(ctx context.Context, traceparent string) context.Context {
	if traceparent == "" {
		return ctx
	}
	carrier := propagation.MapCarrier{"traceparent": traceparent}
	extracted := traceContextPropagator.Extract(ctx, carrier)
	if !trace.SpanContextFromContext(extracted).IsValid() {
		slog.DebugContext(ctx, "malformed traceparent dropped, trace tree will split",
			"phase", "處理中",
			"traceparent_len", len(traceparent),
		)
	}
	return extracted
}
