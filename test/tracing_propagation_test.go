package importtest

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/Ivantseng123/agentdock/shared/queue"
	"github.com/Ivantseng123/agentdock/shared/tracing"
)

// TestCrossProcessTraceContinuity exercises the W3C carrier round-trip the
// way app→queue→worker uses it, without spinning up real Redis or workers.
// Asserts every span produced (app root, worker umbrella, agent leaf) ends
// up under the same trace ID — the contract the live deployment relies on.
func TestCrossProcessTraceContinuity(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	appTracer := otel.Tracer("agentdock/app")
	workerTracer := otel.Tracer("agentdock/worker/pool")
	agentTracer := otel.Tracer("agentdock/worker/agent")

	// App side: start root, inject carrier onto Job.
	rootCtx, rootSpan := appTracer.Start(context.Background(), tracing.SpanBotHandleEvent)
	job := &queue.Job{ID: "j-cross-001"}
	job.Traceparent = tracing.InjectFromContext(rootCtx)
	rootSpan.End()

	if job.Traceparent == "" {
		t.Fatal("Traceparent empty after Inject — app side did not emit a carrier")
	}

	// Worker side: simulate dequeue + extract + handle_job + agent.execute.
	workerCtx := tracing.ExtractToContext(context.Background(), job.Traceparent)
	workerCtx, umbrella := workerTracer.Start(workerCtx, tracing.SpanWorkerHandleJob)
	_, leaf := agentTracer.Start(workerCtx, tracing.SpanAgentExecute)
	leaf.End()
	umbrella.End()

	rootTID := rootSpan.SpanContext().TraceID().String()
	if rootTID == "" {
		t.Fatal("root TraceID is empty")
	}

	spans := sr.Ended()
	if len(spans) != 3 {
		t.Fatalf("expected 3 spans (root + umbrella + leaf), got %d", len(spans))
	}
	for _, s := range spans {
		if got := s.SpanContext().TraceID().String(); got != rootTID {
			t.Errorf("span %q has TraceID %q, want shared %q", s.Name(), got, rootTID)
		}
	}

	// Parent/child topology: leaf → umbrella → root.
	byName := map[string]sdktrace.ReadOnlySpan{}
	for _, s := range spans {
		byName[s.Name()] = s
	}
	if got := byName[tracing.SpanWorkerHandleJob].Parent().SpanID(); got != rootSpan.SpanContext().SpanID() {
		t.Errorf("worker.handle_job parent SpanID = %s, want app root %s", got, rootSpan.SpanContext().SpanID())
	}
	if got := byName[tracing.SpanAgentExecute].Parent().SpanID(); got != byName[tracing.SpanWorkerHandleJob].SpanContext().SpanID() {
		t.Errorf("agent.execute parent SpanID = %s, want worker.handle_job %s", got, byName[tracing.SpanWorkerHandleJob].SpanContext().SpanID())
	}
}

// TestCrossProcessTraceContinuity_LegacyV1Producer covers the rolling-deploy
// case where a v1 app emits jobs without Traceparent. The worker must
// synthesize a fresh root span — no panic, no fallback to a sentinel.
func TestCrossProcessTraceContinuity_LegacyV1Producer(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	workerTracer := otel.Tracer("agentdock/worker/pool")

	job := &queue.Job{ID: "j-legacy-v1", Traceparent: ""}

	workerCtx := tracing.ExtractToContext(context.Background(), job.Traceparent)
	_, span := workerTracer.Start(workerCtx, tracing.SpanWorkerHandleJob)
	span.End()

	spans := sr.Ended()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span (worker root), got %d", len(spans))
	}
	if !spans[0].SpanContext().TraceID().IsValid() {
		t.Error("worker root span has invalid TraceID")
	}
	if spans[0].Parent().SpanID().IsValid() {
		t.Errorf("worker root must have empty Parent SpanID, got %s", spans[0].Parent().SpanID())
	}
}
