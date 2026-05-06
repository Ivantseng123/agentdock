package tracing

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel/trace"
)

func TestBuildTracerProvider_EmptyEndpointReturnsUsableProvider(t *testing.T) {
	ctx := context.Background()
	tp, shutdown := BuildTracerProvider(ctx, Config{
		OTLPEndpoint:   "",
		ServiceName:    "agentdock-test",
		ServiceVersion: "0.0.0",
	}, nil)
	defer func() { _ = shutdown(ctx) }()

	if tp == nil {
		t.Fatal("BuildTracerProvider returned nil for empty endpoint")
	}

	tracer := tp.Tracer("test")
	spanCtx, span := tracer.Start(ctx, "test-span")
	defer span.End()

	sc := trace.SpanContextFromContext(spanCtx)
	if !sc.IsValid() {
		t.Error("SpanContext from empty-endpoint provider is invalid")
	}
	if !sc.TraceID().IsValid() {
		t.Error("SpanContext has invalid TraceID")
	}
}

func TestBuildTracerProvider_SetEndpointReturnsUsableProvider(t *testing.T) {
	ctx := context.Background()
	// Use a deliberately unreachable endpoint. otlptracegrpc.New does a
	// non-blocking dial, so init succeeds; export attempts at runtime will
	// fail but the BatchSpanProcessor handles that fail-soft.
	tp, shutdown := BuildTracerProvider(ctx, Config{
		OTLPEndpoint:   "127.0.0.1:1", // closed port
		ServiceName:    "agentdock-test",
		ServiceVersion: "0.0.0",
	}, nil)
	if tp == nil {
		t.Fatal("BuildTracerProvider returned nil with endpoint set")
	}

	// Cap shutdown so the test doesn't wait the full 5s flush window.
	shortCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	_ = shutdown(shortCtx) // err is acceptable per fail-soft posture
}

func TestBuildTracerProvider_NilLoggerDoesNotPanic(t *testing.T) {
	ctx := context.Background()
	tp, shutdown := BuildTracerProvider(ctx, Config{
		OTLPEndpoint:   "",
		ServiceName:    "test",
		ServiceVersion: "0.0.0",
	}, nil)
	defer func() { _ = shutdown(ctx) }()

	if tp == nil {
		t.Fatal("nil logger should not block provider construction")
	}
}
