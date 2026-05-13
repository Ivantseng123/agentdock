# Opencode Server Mode POC Report

POC source: branch [`poc/opencode-server-mode`](https://github.com/Ivantseng123/agentdock/tree/poc/opencode-server-mode). See `docs/specs/opencode-server-mode.md` (specification) and `docs/specs/opencode-server-mode-plan.md` (execution plan) on that branch for criteria definitions and methodology.

Result summary: 9/10 criteria pass on opencode `1.14.29`. P3 (single-job lifecycle replay) is the only red — opencode `1.14.48` introduces a server-side SSE regression where `/event` closes within ~20ms of subscription. POC code stays on the `poc/opencode-server-mode` branch and is not merged to `main`; this document is the report only.

Failure classification uses transport-vs-upstream semantics:

- `provider_retry_transient` — transient upstream/provider hiccup, safe to retry (does not red the criterion).
- `server_mode_regression` — opencode server-mode behavior change, blocks the criterion.

Generated at: 2026-05-13T14:26:09+08:00

Baseline opencode: `1.14.29`

HEAD opencode: `1.14.48`

| Criterion | Status | Time | Version |
| --- | --- | --- | --- |
| P1 | ✅ | 2026-05-13T13:28:00+08:00 | 1.14.29 |
| P2 | ✅ | 2026-05-13T13:28:04+08:00 | 1.14.29 |
| P3 | ❌ | 2026-05-13T13:42:26+08:00 | 1.14.48 |
| P4 | ✅ | 2026-05-13T13:42:28+08:00 | 1.14.29 |
| P5 | ✅ | 2026-05-13T13:44:13+08:00 | 1.14.29 |
| P6 | ✅ | 2026-05-13T13:46:20+08:00 | 1.14.29 |
| P7 | ✅ | 2026-05-13T13:46:28+08:00 | 1.14.29 |
| P8 | ✅ | 2026-05-13T14:24:07+08:00 | 1.14.29 -> 1.14.48 |
| P9 | ✅ | 2026-05-13T14:24:09+08:00 | 1.14.29 |
| P10 | ✅ | 2026-05-13T14:25:50+08:00 | 1.14.29 |

## P1

- Status: ✅ green
- Time: `2026-05-13T13:28:00+08:00`
- Version: `1.14.29`
- Raw measurement:

```text
P1 latencies: <2s=5 2-5s=0 5-10s=0 >=10s=0 p50=722ms p95=724ms max=726ms
```

## P2

- Status: ✅ green
- Time: `2026-05-13T13:28:04+08:00`
- Version: `1.14.29`
- Raw measurement:

```text
P2 summary: version=1.14.29 success=100/100 transient=0 regression=0
P2 latencies: <2s=0 2-5s=24 5-10s=44 >=10s=32 p50=7095ms p95=18040ms max=29137ms
```

## P3

- Status: ❌ red
- Time: `2026-05-13T13:42:26+08:00`
- Version: `1.14.48`
- Error: `P3 failed: server_mode_regression=100 (transient=0 successes=0/100)`
- Raw measurement:

```text
P3 summary: version=1.14.48 success=0/100 transient=0 regression=100
P3 latencies: no successful runs
P3 failure: run=1 class=server_mode_regression session_id=ses_1e0245f84ffecMtMv6NZJsZUU3 elapsed_ms=13 exit_code=0 error=shared event stream closed unexpectedly
P3 failure: run=2 class=server_mode_regression session_id=ses_1e0245f7affe0qQsP64Qo9vbUI elapsed_ms=9 exit_code=0 error=event stream closed unexpectedly
P3 failure: run=3 class=server_mode_regression session_id=ses_1e0245f71ffehgRQH0WdfFV07F elapsed_ms=2 exit_code=0 error=shared event stream closed unexpectedly
P3 failure: run=4 class=server_mode_regression session_id=ses_1e0245f6fffeBRRJHnYTLce91g elapsed_ms=2 exit_code=0 error=shared event stream closed unexpectedly
P3 failure: run=5 class=server_mode_regression session_id=ses_1e0245f6cffeyizwVl0yc11GkM elapsed_ms=2 exit_code=0 error=shared event stream closed unexpectedly
P3 ... (95 more failure(s) suppressed)
```

## P4

- Status: ✅ green
- Time: `2026-05-13T13:42:28+08:00`
- Version: `1.14.29`
- Raw measurement:

```text
P4 matrix 1: OK OK OK OK OK OK OK OK
P4 matrix 2: OK OK OK OK OK OK OK OK
P4 matrix 3: OK OK OK OK OK OK OK OK
P4 matrix 4: OK OK OK OK OK OK OK OK
P4 matrix 5: OK OK OK OK OK OK OK OK
P4 summary: success=40/40 unique_hashes=40 expected=40
```

## P5

- Status: ✅ green
- Time: `2026-05-13T13:44:13+08:00`
- Version: `1.14.29`
- Raw measurement:

```text
P5 matrix 1: OK OK OK OK OK OK OK OK
P5 summary: success=8/8
```

## P6

- Status: ✅ green
- Time: `2026-05-13T13:46:20+08:00`
- Version: `1.14.29`
- Raw measurement:

```text
P6 summary: plugin_loaded=false answer="P6-SAFE-RESULT"
```

## P7

- Status: ✅ green
- Time: `2026-05-13T13:46:28+08:00`
- Version: `1.14.29`
- Raw measurement:

```text
P7 synthetic-empty summary: failed=true reason="empty SSE / finish=other / 0 output tokens"
P7 say-ok summary: failed=false finish="stop" output_tokens=22 text_received=true attempts=1 transient_hits=0
P7 false-positive summary: false_positives=0 successful_samples=100 transient=0 target=100 min_successes=50
```

## P8

- Status: ✅ green
- Time: `2026-05-13T14:24:07+08:00`
- Version: `1.14.29 -> 1.14.48`
- Raw measurement:

```text
P8 POST /session
  request_added: agent, model, model.id, model.providerID, model.variant
  response_added: agent, model, model.id, model.providerID, model.variant
  breaking: false
P8 POST /session/{id}/message
  breaking: false
P8 GET /event
  breaking: false
P8 summary: baseline=1.14.29 head=1.14.48 breaking=false
```

## P9

- Status: ✅ green
- Time: `2026-05-13T14:24:09+08:00`
- Version: `1.14.29`
- Raw measurement:

```text
P9 concurrent-first-job summary: start_count=1 concurrency=8 started_at=2026-05-13T14:24:09+08:00 completed_at=2026-05-13T14:24:45+08:00
P9 pre-idle replay summary: happy_path=true regression=false attempts=1 transient_hits=0 last_class= session_id=ses_1dffda603ffene1w8BxcZ5YdYJ elapsed_ms=21753
P9 idle-stop summary: pid=5876 idle_timeout=30s idle_started_at=2026-05-13T14:25:07+08:00 stopped_at=2026-05-13T14:25:37+08:00 ps_snapshot=""
P9 post-respawn replay summary: happy_path=true regression=false attempts=1 transient_hits=0 last_class= session_id=ses_1dffcd803ffe3A61Ef4V7ZK9gz elapsed_ms=13429
P9 respawn summary: start_count=2 respawned=true happy_path=true started_at=2026-05-13T14:25:37+08:00 completed_at=2026-05-13T14:25:50+08:00
```

## P10

- Status: ✅ green
- Time: `2026-05-13T14:25:50+08:00`
- Version: `1.14.29`
- Raw measurement:

```text
P10 summary: isolated_db=/tmp/poc-opencode-xdg-data/opencode/opencode.db sessions=1 user_db_unchanged=true
```
