# Retry Shares trace_id, No App-Side Umbrella Span

## Context

When a user clicks the 重試 button in Slack, `app/bot/retry_handler.go` builds a new `Job` and submits it to the queue. We had to decide how the retry attempt relates to the original attempt in the OTel trace tree. The naive default in many systems is to give each retry its own trace, optionally linked back to the original via OTel `Link`s. After grilling we picked a different model.

## Decision

A retry shares the **same `trace_id`** as the original attempt. There is **no app-side umbrella span** for retry — the retry handler does not call `tracer.Start`. Instead:

1. `retry_handler.Handle` reads `original.Traceparent` and copies it verbatim to `newJob.Traceparent`.
2. `newJob.RequestID = original.RequestID` (sharing the OTel trace_id; cf. ADR-0001 §6).
3. `retry_handler` calls `tracing.ExtractToContext(ctx, original.Traceparent)` to put the original `bot.handle_event`'s SpanContext back into ctx, then submits — the auto-instrumented `queue.enqueue` span that `shared/queue.Submit` opens hangs off the original root.
4. Worker dequeues `newJob`, extracts `newJob.Traceparent` (which still points at the original `bot.handle_event`), and starts a fresh `worker.handle_job` span as another child of that root.

The resulting trace tree:

```
bot.handle_event (app, original)
├── queue.enqueue (attempt 1)
├── worker.handle_job (attempt 1, FAILED)
│   └── ...
├── queue.enqueue (attempt 2 = retry)
└── worker.handle_job (attempt 2, SUCCESS)
    └── ...
```

`retry_count` and `retry_of_job_id` are recorded as span attributes on the retry's `worker.handle_job` (not on `bot.handle_event`, which doesn't know it will be retried).

## Considered Options

- **New trace + OTel Link to original.** Rejected: Jaeger's link rendering varies by version, and we don't pin the cluster Jaeger version. Operators going from a retry's span back to the original would have to grep manually anyway.
- **Same trace_id with an app-side `bot.retry_handler` umbrella span.** Rejected as needless: the retry handler runs in <100ms (store read, secrets re-encrypt, queue submit). Its time is not on any critical path; an umbrella span buys no diagnostic value and adds instrumentation surface to maintain. If retry_handler ever becomes slow, *that* is the moment to add a span.
- **Long retry chain (retry of retry of retry) in one trace.** Accepted as a consequence. Even five retries × 45s = ~4 min, well under typical Jaeger trace-duration caps.

## Consequences

- One Slack thread that requires multiple retries renders as one trace tree in Jaeger UI, with attempt N visible as the (N+1)-th `worker.handle_job` sibling under the original `bot.handle_event`. This matches the operator's mental model ("show me the whole story for this Slack thread").
- `retry_handler.go:108` must change `RequestID: logging.NewRequestID()` to `RequestID: original.RequestID`. This is a behavior change that any future contributor reading the line in isolation might "fix" back — this ADR exists to stop that.
