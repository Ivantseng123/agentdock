# Opencode Server Mode POC Report

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
