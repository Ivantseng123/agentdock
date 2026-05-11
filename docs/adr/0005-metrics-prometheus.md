# Metrics: Prometheus client_golang (pull), not OTel metrics

## Context

Observability v3 (issue [#47](https://github.com/Ivantseng123/agentdock/issues/47)) closes the metrics gap: logs answer "what happened" and traces show "how it flowed", but neither answers aggregate questions like "triage success rate this hour" or "P95 agent execution time". The issue framed the library choice as either/or — Prometheus `client_golang` (pull) vs. OTel metrics (OTLP push, consistent with the v2 tracing pipeline).

By the time #47 was picked up, a substantial Prometheus surface had already landed: `shared/metrics/metrics.go` defined ~23 counters/histograms/gauges under the `agentdock` namespace, the app already mounted `/metrics` via `promhttp.Handler()`, and the four oncall questions in #47 were already answerable from existing metrics. So #47 became a gap-closure exercise rather than a fresh build, and the "library choice" was effectively already made — this ADR records *why that choice stands* and the decisions taken around it.

## Decision

### 1. Prometheus `client_golang` (pull model), not OTel metrics

We keep `github.com/prometheus/client_golang` and the `/metrics` pull endpoint. Reasons:

- **Zero new dependency.** `client_golang` is already a direct dependency of `shared`; the metric set, registration, and `/metrics` handler already exist and work. Switching to OTel metrics would mean adding `go.opentelemetry.io/otel/sdk/metric` + `otlp/otlpmetric/otlpmetricgrpc`, rewiring every observation site, and migrating any existing dashboards/alerts off the `agentdock_*` names.
- **Pull fits the deployment shape.** The bot runs in Kubernetes; a `ServiceMonitor`/`PodMonitor` scraping `/metrics` is the path of least resistance for the operators. Push-to-collector adds a collector to run and monitor.
- **Maturity for this use case.** `client_golang` histograms, `GaugeFunc` (scrape-time computation for queue depth / worker counts), and the exposition format are all battle-tested.

### 2. Dual stack: OTel traces + Prometheus metrics

The two signals use two pipelines on purpose. Traces go out via OTLP gRPC (see ADR-0001); metrics are scraped from `/metrics`. We do **not** introduce a second metrics pipeline (OTel metrics push) "for consistency" — consistency of telemetry pipelines is not worth the migration cost when both halves already work. If a future deployment standardises on an OTel collector for everything, that's a separate, deliberate migration with its own ADR.

### 3. `metrics.enabled` defaults to `true`

New `metrics.enabled` knob (`*bool` under top-level `metrics:` in `app.yaml`), default **true** — i.e. the key being absent means enabled. This preserves pre-v3 behaviour, where `/metrics` was mounted whenever `server.port > 0` and operators had no way to turn it off (other than not configuring a port).

`metrics.enabled: false` skips `metrics.Register()` *and* does not mount `/metrics`, so the default Prometheus registry stays empty. The `.Inc()` / `.Observe()` call sites in app/worker code are *not* guarded — they're harmless no-ops on an unregistered metric, and guarding every one would be churn for no benefit.

We chose `true` over #47's literal "default false" because (a) nobody is currently relying on metrics being off, (b) silently dropping the `/metrics` endpoint on an existing deployment after a version bump would be a regression, and (c) "absent = on" matches how `pr_review.enabled` already behaves.

### 4. `metrics.listen_addr` deferred

#47 mentioned a `metrics.listen_addr` (a dedicated scrape port, separate from `server.port`, so `/metrics` can bind to an internal-only interface while `/healthz` and `/jobs` stay on the public port). Not implemented in v3 — it adds a second `http.Server` and a config field for a need no operator has voiced yet. Until then, operators restrict `/metrics` exposure at the ingress / NetworkPolicy layer (documented in `docs/configuration-app.md`). Revisit as a small follow-up if a concrete need shows up.

### 5. Label cardinality is enforced by test, with one justified exception

`shared/metrics/metrics_test.go:TestLabelCardinality` walks the `staticCollectors` slice (the single registration list) and fails on a banlist of unbounded-cardinality label keys: `channel_id`, `user_id`, `thread_ts`, `repo`, `pr_number`, `issue_number`. These belong in logs / span attributes, never in a Prometheus label.

One exception is allowlisted: `agentdock_ref_write_violations_total{repo}`. The `repo` here is a *ref repo* — one of the fixed set chosen in channel config, not a user-supplied URL — so its value set is bounded. Any new `(metric, label)` exception requires a PR-visible justification; the reviewer gates on the allowlist map, not on the test passing.

### 6. Worker emits no metrics directly; the app observes on its behalf

The worker has no HTTP server and does not import `shared/metrics`. "Worker behaviour" metrics (agent execution time, prepare time, exit code, ...) are derived by the app's result listener from data the worker reports back in `JobResult` / `StatusReport`. v3 added `JobResult.ExitCode` (sentinel `-1` for "no process / not waited") for exactly this — see Decision #7 below. Giving the worker its own `/metrics` endpoint was considered and rejected: it's a larger change than the gap warrants, and it cuts against the product positioning (a structuring tool, not a diagnosis platform).

### 7. `JobResult.ExitCode` uses a `-1` sentinel and is not `omitempty`

`int`'s zero value `0` collides with a successful exit, so a sentinel is needed to mean "no process / not waited" (pre-runner failures, cancellation before exec). We use `-1`. The field is deliberately **not** `omitempty` — `omitempty` would drop the `-1` on the wire and resurrect the ambiguity. The app gates observation on `ExitCode >= 0`.

**Known limitation:** a version-skewed older worker that doesn't set the field serialises it as `0`, which the app reads as "exited successfully". The app/worker pair is expected to deploy together, so this is a transient-during-rollout concern, not a steady-state one.

## Consequences

- The `agentdock_*` metric names and labels are a stable contract; renaming any of them breaks operator dashboards/alerts, so it's an "ask first" change (see `SPEC.md` Boundaries).
- New metrics must be appended to `staticCollectors` — that slice is both the registration list and the cardinality-audit input, so a metric that skips it is silently absent from both `/metrics` and the audit.
- Operators detect "metrics accidentally disabled" the same way they detect "tracing endpoint not set" — by the data not showing up in Grafana, not by log spam. The startup log carries a `metrics_enabled` field, and an enabled-but-`server.port == 0` config produces one warning at startup.
- `client_golang`'s `promhttp.Handler()` registers its own `promhttp_metric_handler_*` self-metrics on the default registry; those continue to appear in `/metrics`. The httptest-based unit test uses `promhttp.HandlerFor(<test registry>, ...)` instead so it doesn't touch the global registry.

## Appendix: #47 metric list → reality

#47's MVP list named seven metrics. Five are already covered (some under different names — renaming is out of scope, see Decision #1); two were genuinely missing and added in v3.

| #47 name | Status | Maps to |
|---|---|---|
| `agentdock_jobs_total{status, agent}` | covered (superset) | `agentdock_agent_executions_total{provider, workflow, status}` — same intent, plus a `workflow` dimension |
| `agentdock_job_duration_seconds{phase, agent}` | covered (split into 3) | `agentdock_queue_wait_seconds` + `agentdock_agent_prepare_seconds` + `agentdock_agent_execution_seconds{provider}` — three independent histograms, one per phase; a unified `{phase}` histogram would re-observe the same intervals (see `docs/operations.md` for the PromQL that stitches them) |
| `agentdock_queue_depth` | covered (exact) | `agentdock_queue_depth` (GaugeFunc, computed on scrape) |
| `agentdock_queue_wait_seconds` | covered (exact) | `agentdock_queue_wait_seconds` (observed in the app result listener) |
| `agentdock_agent_exit_code_total{agent, exit_code}` | **added in v3** | `agentdock_agent_exit_code_total{provider, exit_code}` — raw exit code, so OOM (137) / timeout (124) / self-abort (1) are separable |
| `agentdock_slack_events_total{type}` | **added in v3** | `agentdock_slack_events_total{type}` — `app_mention` / `member_joined` / `member_left` / `slash_command` / `block_suggestion` / `block_action` / `unknown` |
| `agentdock_github_api_errors_total{endpoint}` | covered (generalised) | `agentdock_external_errors_total{service, operation}` — covers GitHub *and* Slack, labelled by `service="github"` + `operation` (`list_repos`, `create_issue`, ...) |
