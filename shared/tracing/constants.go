// Package tracing wires OpenTelemetry distributed tracing for the agentdock
// app and worker processes. It is intentionally fail-soft per ADR-0001:
// empty endpoint produces a real SDK with no exporter (SpanContext still
// flows through ctx so log handlers can pick up trace_id), and runtime
// errors connecting to the collector are logged and suppressed rather than
// propagated. This package never panics, never calls os.Exit, never
// returns a fatal error to the caller.
package tracing

// Span name constants. Convention: lower-snake, <component>.<verb_object>.
// New spans must be added here so cross-package callers reference a single
// source of truth.
const (
	SpanBotHandleEvent    = "bot.handle_event"
	SpanWorkerHandleJob   = "worker.handle_job"
	SpanQueueEnqueue      = "queue.enqueue"
	SpanGithubCloneRepo   = "github.clone_repo"
	SpanAgentExecute      = "agent.execute"
	SpanGithubCreateIssue = "github.create_issue"
)
