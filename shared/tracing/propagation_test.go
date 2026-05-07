package tracing

import (
	"context"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/trace"
)

func TestInjectFromContext_NoSpanReturnsEmpty(t *testing.T) {
	got := InjectFromContext(context.Background())
	if got != "" {
		t.Errorf("InjectFromContext with no span = %q, want empty", got)
	}
}

func TestInjectExtractRoundTrip(t *testing.T) {
	ctx := context.Background()
	tp, shutdown := BuildTracerProvider(ctx, Config{
		ServiceName:    "test",
		ServiceVersion: "0.0.0",
	}, nil)
	defer func() { _ = shutdown(ctx) }()

	tracer := tp.Tracer("test")
	spanCtx, span := tracer.Start(ctx, "round-trip")
	defer span.End()

	traceparent := InjectFromContext(spanCtx)
	if traceparent == "" {
		t.Fatal("InjectFromContext returned empty for ctx with span")
	}
	parts := strings.Split(traceparent, "-")
	if len(parts) != 4 {
		t.Errorf("traceparent has %d dash-separated parts, want 4: %q", len(parts), traceparent)
	}

	extracted := ExtractToContext(context.Background(), traceparent)
	sc := trace.SpanContextFromContext(extracted)
	if !sc.IsValid() {
		t.Fatal("Extract produced invalid SpanContext")
	}
	if got := sc.TraceID().String(); got != span.SpanContext().TraceID().String() {
		t.Errorf("round-trip TraceID = %q, want %q", got, span.SpanContext().TraceID().String())
	}
}

type marker struct{}

func TestExtractToContext_EmptyReturnsOriginal(t *testing.T) {
	ctx := context.WithValue(context.Background(), marker{}, "preserved")
	got := ExtractToContext(ctx, "")
	if got.Value(marker{}) != "preserved" {
		t.Error("ExtractToContext(\"\") did not preserve original ctx value")
	}
}

func TestExtractToContext_MalformedDoesNotPanic(t *testing.T) {
	ctx := context.Background()
	cases := []string{
		"not-a-traceparent",
		"00",
		"00-tooshort",
		"garbage-garbage-garbage-garbage",
	}
	for _, tp := range cases {
		got := ExtractToContext(ctx, tp)
		sc := trace.SpanContextFromContext(got)
		if sc.IsValid() {
			t.Errorf("malformed traceparent %q produced valid SpanContext", tp)
		}
	}
}
