# Remove the v1 trace_id Fallback Path

## Context

Observability v1 (issue #45, shipped) introduced `logging.WithTraceID(ctx, id)` / `logging.TraceIDFrom(ctx)` and the `TraceIDHandler` slog middleware. v1 stored a timestamp+rand string (`Job.RequestID`) on ctx, and the handler added it as the `trace_id` log attribute.

The original v2 spec wanted to extend this: handler reads OTel SpanContext first, falls back to `WithTraceID`-set RequestID. This kept the v1 API alive as a "defensive fallback for non-`*Context` callers."

After grilling we removed that fallback. With ADR-0001's posture (always-attempt OTel; empty endpoint still runs the SDK without exporter so SpanContext is preserved in ctx), the OTel-first read covers every case the v1 fallback was meant to cover, and keeping a parallel API just creates a "what is this for?" surface for future readers.

## Decision

`logging.WithTraceID` and `logging.TraceIDFrom` are deleted. `TraceIDHandler` reads `trace.SpanContextFromContext(ctx)` only — if the span context is invalid, the handler emits no `trace_id` attribute (no panic, no empty-string attr).

The single existing caller, `app/app.go:273` (`ctx = logging.WithTraceID(ctx, p.RequestID)`), is removed because OTel `tracer.Start(ctx, "bot.handle_event")` already places a valid SpanContext on ctx.

This deliberately overrides the original spec's `Boundaries — Never do` clause that protected this API. That clause was written under the v1 worldview (where `RequestID` was a non-OTel identifier the handler needed to read); ADR-0001's decision to make `Job.RequestID` itself an OTel hex value collapsed the worldview, making the protection obsolete.

## Consequences

- v1 callers that pre-date the OTel rollout and emit logs via non-`*Context` slog variants (`logger.Info(...)` instead of `logger.InfoContext(ctx, ...)`) lose `trace_id` from those records. They were already losing it under v1's `TraceIDHandler` (which only inspected ctx); the change here is that the v1 API which they *could* have switched to as a workaround is gone. The migration path is: use the `*Context` variant or accept missing `trace_id` on that record.
- Tests in `shared/logging/trace_test.go` shed the case (b) ("WithTraceID set, no OTel span"). Two cases remain: (a) span set in ctx → emit OTel hex; (c) no span → emit no attr.
