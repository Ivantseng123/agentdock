# Opencode Server Mode POC Report

> **Methodology & criteria definitions** — see [`opencode-server-mode.md` § Phase 3.1](./opencode-server-mode.md) for the formal P1–P10 criterion text. POC source code lives on the `poc/opencode-server-mode` branch under `cmd/dev/poc-opencode-server/`.
>
> **Failure classification** (applies to P2/P3/P9 replay attempts):
> - `provider_retry_transient` — upstream provider noise (rate limits, API retries, `session.status=retry`, `session.error` with `IsRetryable=true`, scan-timeout before assistant text). Spawn-mode also sees these; not a server-mode gate.
> - `server_mode_regression` — worker ↔ `opencode serve` transport / contract layer broken (SSE channel closed without termination event, mid-flight cut after meaningful text, HTTP transport failure, unparseable answer, Bug A pattern). This is what gates ship.
>
> **Run configuration for this report:**
> - opencode provider: `opencode-go/deepseek-v4-flash` (paid OpenCode Zen) — chosen to avoid free-tier rate-limit bursts that contaminate the `provider_retry_transient` counter on earlier `opencode/minimax-m2.5-free` runs
> - `-replay-count`: 100 (P2 / P3 / P7 false-positive batch)
> - `-run-timeout`: 120s
> - fixture: `testdata/harbor-4images` (sanitized multimodal: 1 prompt + 4 PNG)
> - isolated XDG: `/tmp/poc-opencode-xdg-data`

Generated at: 2026-05-14T02:20:38+08:00

Baseline opencode: `1.14.41`

HEAD opencode: `1.14.41`

| Criterion | Status | Time | Version |
| --- | --- | --- | --- |
| P1 | ✅ | 2026-05-14T01:38:16+08:00 | 1.14.41 |
| P2 | ✅ | 2026-05-14T01:38:20+08:00 | 1.14.41 |
| P3 | ✅ | 2026-05-14T01:48:07+08:00 | 1.14.41 |
| P4 | ✅ | 2026-05-14T01:57:43+08:00 | 1.14.41 |
| P5 | ✅ | 2026-05-14T01:58:38+08:00 | 1.14.41 |
| P6 | ✅ | 2026-05-14T02:00:19+08:00 | 1.14.41 |
| P7 | ✅ | 2026-05-14T02:00:27+08:00 | 1.14.41 |
| P8 | ✅ | 2026-05-14T02:19:08+08:00 | 1.14.41 -> 1.14.41 |
| P9 | ✅ | 2026-05-14T02:19:11+08:00 | 1.14.41 |
| P10 | ✅ | 2026-05-14T02:20:20+08:00 | 1.14.41 |

## P1

- Status: ✅ green
- Time: `2026-05-14T01:38:16+08:00`
- Version: `1.14.41`
- Raw measurement:

```text
P1 latencies: <2s=5 2-5s=0 5-10s=0 >=10s=0 p50=718ms p95=718ms max=719ms
```

## P2

- Status: ✅ green
- Time: `2026-05-14T01:38:20+08:00`
- Version: `1.14.41`
- Raw measurement:

```text
P2 summary: version=1.14.41 success=100/100 transient=0 regression=0
P2 latencies: <2s=0 2-5s=30 5-10s=67 >=10s=3 p50=5414ms p95=8022ms max=18585ms
```

## P3

- Status: ✅ green
- Time: `2026-05-14T01:48:07+08:00`
- Version: `1.14.41`
- Raw measurement:

```text
P3 summary: version=1.14.41 success=100/100 transient=0 regression=0
P3 latencies: <2s=0 2-5s=39 5-10s=56 >=10s=5 p50=5191ms p95=9028ms max=29195ms
```

## P4

- Status: ✅ green
- Time: `2026-05-14T01:57:43+08:00`
- Version: `1.14.41`
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
- Time: `2026-05-14T01:58:38+08:00`
- Version: `1.14.41`
- Raw measurement:

```text
P5 matrix 1: OK OK OK OK OK OK OK OK
P5 summary: success=8/8
```

## P6

- Status: ✅ green
- Time: `2026-05-14T02:00:19+08:00`
- Version: `1.14.41`
- Raw measurement:

```text
P6 summary: plugin_loaded=false answer="P6-SAFE-RESULT"
```

## P7

- Status: ✅ green
- Time: `2026-05-14T02:00:27+08:00`
- Version: `1.14.41`
- Raw measurement:

```text
P7 synthetic-empty summary: failed=true reason="empty SSE / finish=other / 0 output tokens"
P7 say-ok summary: failed=false finish="stop" output_tokens=2 text_received=true attempts=1 transient_hits=0
P7 false-positive summary: false_positives=0 successful_samples=100 transient=0 target=100 min_successes=50
```

## P8

- Status: ✅ green
- Time: `2026-05-14T02:19:08+08:00`
- Version: `1.14.41 -> 1.14.41`
- Raw measurement:

```text
P8 POST /session
  breaking: false
P8 POST /session/{id}/message
  breaking: false
P8 GET /event
  breaking: false
P8 summary: baseline=1.14.41 head=1.14.41 breaking=false
```

## P9

- Status: ✅ green
- Time: `2026-05-14T02:19:11+08:00`
- Version: `1.14.41`
- Raw measurement:

```text
P9 concurrent-first-job summary: start_count=1 concurrency=8 started_at=2026-05-14T02:19:11+08:00 completed_at=2026-05-14T02:19:36+08:00
P9 pre-idle replay summary: happy_path=true regression=false attempts=1 transient_hits=0 last_class= session_id=ses_1dd6f2da0ffe1HmPrMKELvAmlM elapsed_ms=5839
P9 idle-stop summary: pid=60413 idle_timeout=30s idle_started_at=2026-05-14T02:19:42+08:00 stopped_at=2026-05-14T02:20:12+08:00 ps_snapshot=""
P9 post-respawn replay summary: happy_path=true regression=false attempts=1 transient_hits=0 last_class= session_id=ses_1dd6e9d08ffe3xKhCBCDdN4Rxa elapsed_ms=7941
P9 respawn summary: start_count=2 respawned=true happy_path=true started_at=2026-05-14T02:20:12+08:00 completed_at=2026-05-14T02:20:20+08:00
```

## P10

- Status: ✅ green
- Time: `2026-05-14T02:20:20+08:00`
- Version: `1.14.41`
- Raw measurement:

```text
P10 summary: isolated_db=/tmp/poc-opencode-xdg-data/opencode/opencode.db sessions=1 user_db_unchanged=true
```

## Appendix — Historical bisect: 1.14.41 → 1.14.48 SSE-close regression

The above run sets `Baseline = HEAD = 1.14.41` because no newer opencode version has yet passed P3 against the harbor-4images fixture; every version from 1.14.42 onward exhibits a server-mode SSE-close regression that hard-fails P3. This appendix records that evidence so spec/plan citations to "POC report § Historical bisect" have a substantive anchor.

> This appendix is annotated post-generation (not part of the POC `-all` stdout). The raw bisect data lives in the `poc/opencode-server-mode` branch's REPORT.md @ commit `0e76d3a` (Baseline=1.14.29, HEAD=1.14.48 — P3 ❌ recorded) and in the `-criteria=p3 -replay-count=5 -run-timeout=120s` runs captured for the Dockerfile.release pin bump (PR #256).

### Bisect data

| opencode version | P3 outcome | regression / attempts | First-failure signature |
|---|---|---|---|
| 1.14.29 | ✅ green | 0 / 100 (poc branch REPORT @ 0e76d3a) | — |
| 1.14.41 | ✅ green | 0 / 100 (this report; 0 / 5 in PR #256 bisect on darwin-arm64) | — |
| 1.14.42 | ❌ red | 3 / 3 (PR #256 bisect) | `/event` SSE close at ≤12 ms after subscription; no termination event; no `session.error` |
| 1.14.43 | ❌ red | 3 / 3 (PR #256 bisect) | same SSE-close pattern |
| 1.14.48 | ❌ red | 100 / 100 (poc branch REPORT @ 0e76d3a; HEAD pin) | same SSE-close pattern |

### Implications

1. `Dockerfile.release` pins `OPENCODE_VERSION=1.14.41` (PR #256) so the shipped worker image carries the last known-good version.
2. `MinimumOpencodeVersion` (Phase 3.2 Task 3.2-4) mirrors this pin — `1.14.41` — until a newer opencode version passes a fresh POC `-all -replay-count=100` run.
3. The lower-bound version check defends against "operator installed too-old opencode" but **does not** catch "operator installed too-new but broken opencode". The defense for newer-but-broken is per-job failure detection (Bug A detector + `server_mode_regression` classification + no auto-fallback per spec C4).
