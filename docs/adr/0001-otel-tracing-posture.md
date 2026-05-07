# OTel Tracing Posture: Always-Attempt, Never Fatal

## Context

When introducing OpenTelemetry distributed tracing in observability v2 (issue #46), we had to decide how tracing failures should interact with the main Slack→GitHub triage flow. The original spec proposed a `tracing.enabled: bool` knob plus panic-on-missing-endpoint at startup. After grilling, we adopted a simpler and safer posture.

## Decision

Tracing is treated as a baseline capability, not an optional feature, but it is **never allowed to crash app or worker**.

1. **No `tracing.enabled` knob.** The presence or absence of `tracing.otlp_endpoint` (YAML) / `OTEL_EXPORTER_OTLP_ENDPOINT` (env) is the only switch.
2. **Empty endpoint = silent skip.** When neither is set, `BuildTracerProvider` returns a real OTel SDK *without* any OTLP exporter — spans are produced and propagated through ctx (so `trace_id` survives in logs), but nothing is exported. We deliberately do **not** return a `noop.NewTracerProvider()` because that would zero out the SpanContext in ctx and break log↔log correlation across the app/worker boundary.
3. **Endpoint set + collector unreachable at startup = fail-soft.** OTel SDK's default `BatchSpanProcessor` buffers + retries; the process continues. We do not add a `grpc.WithBlock()` startup healthcheck.
4. **Endpoint set + collector drops mid-flight = fail-soft.** Same buffer/drop behavior. A Jaeger blip never crash-loops the bot.
5. **Worker pushes to Jaeger directly, not via app.** Worker's own `BuildTracerProvider` runs in-process, with its own OTLP exporter pointing at its own configured endpoint. We considered "worker → Redis → app → Jaeger" forwarding and rejected it: it ties worker tracing health to app uptime, requires a new wire format for serialised spans on Redis, and undoes the v2 module-split independence between app and worker. Worker-on-laptop scenarios that can't reach the cluster's Jaeger leave `tracing.otlp_endpoint` empty, which gives the silent-skip behavior above (worker spans dropped, but `trace_id` still printed in worker logs because step 2's real-SDK-no-exporter setup keeps SpanContext live in ctx).
6. **`Job.RequestID` carries the OTel trace_id (16-byte hex).** It is generated after the root span starts. The v1 timestamp+rand format is removed; logs only ever print OTel hex `trace_id`. Wire-format compatibility with v1 workers is preserved because the field is still a string — v1 workers just see an opaque hex.

## Consequences

- Operators cannot accidentally turn off tracing by flipping a typo'd boolean — they have to clear the endpoint.
- A misconfigured production deployment that forgets to set `OTEL_EXPORTER_OTLP_ENDPOINT` does not panic; it just silently skips exporting. Operators must rely on dashboards (does Jaeger see this `service.name`?) to detect this, not on app log spam.
- Every log line that flows through any process under v2 carries a `trace_id` (when there is a span in ctx), even when the local OTLP endpoint is empty. Cross-process log grep continues to work.
