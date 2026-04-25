# Plan: Workflow Output Boundary — Ask Raw Fallback

**Spec:** `docs/superpowers/specs/2026-04-25-workflow-output-boundary-design.md`
**Date:** 2026-04-25
**Branch suggestion:** `feat/ask-raw-fallback`

## Scope

Implement the Ask missing-marker fallback. Worker code is unchanged. All work
lives in `app/workflow/`.

In scope:
- `AskResult.ResultSource` field
- `ParseAskOutput` missing-marker fallback path with syntactic check
- `HandleResult` prepends warning banner + emits `fallback_raw` metric

Out of scope (rejected during design grill, see spec §Design Decisions Resolved):
- Malformed-JSON fallback
- Worker-side hint
- Generic stdout sanitizer
- Issue / PR Review failure-message UX cleanup (separate follow-up)

## Dependency graph

```
Task 1 (parser) ─→ Task 2 (handler) ─→ Task 3 (e2e verification)
```

Task 2 needs the `ResultSource` field added in Task 1. Task 3 is verification
only — no code changes.

## Tasks

### Task 1 — AskResult.ResultSource + parser fallback path

**Files:**
- `app/workflow/ask_parser.go`
- `app/workflow/ask_parser_test.go`

**Changes:**
1. Add `ResultSource string` to `AskResult` (struct tag `json:"-"` so it
   never round-trips into the agent's JSON contract).
2. In `ParseAskOutput`:
   - Schema-path success → set `ResultSource = "schema"` before return.
   - On marker-not-found error → run syntactic check on the trimmed output.
     Pass: return `AskResult{Answer: trimmed, ResultSource: "raw_fallback"}, nil`.
     Fail: return existing `marker not found` error.
   - Marker present but JSON malformed / missing object / empty answer →
     keep existing error path (deliberately not covered).
3. Syntactic check (private helper, e.g. `looksLikePlainAnswer`):
   - non-empty after `strings.TrimSpace`
   - meets minimum length threshold (suggested ≥ 10 visible chars; finalize
     at code review)
   - intentionally **no** keyword blocklist

**Acceptance criteria:**
- Schema path returns `ResultSource = "schema"` and existing tests pass.
- Missing-marker + valid plain text → `ResultSource = "raw_fallback"`, no error.
- Missing-marker + empty stdout → error.
- Missing-marker + whitespace-only stdout → error.
- Missing-marker + below-threshold-length stdout → error.
- Marker present + malformed JSON → unchanged error (no fallback).
- `Confidence` field semantics unchanged (still model-provided).

**Verification:**
```sh
go test ./app/workflow -run TestParseAskOutput -v
go vet ./...
```

### Task 2 — Handler banner + metric label

**Files:**
- `app/workflow/ask.go`
- `app/workflow/ask_test.go`

**Changes:**
1. In `AskWorkflow.HandleResult`, after `ParseAskOutput` succeeds:
   - If `parsed.ResultSource == "raw_fallback"`:
     - Prepend banner `":warning: 請驗證輸出答案,AGENT 並未遵守輸出格式\n\n"`
       to `answer`.
     - Increment `metrics.WorkflowCompletionsTotal.WithLabelValues("ask", "fallback_raw")`.
   - Else:
     - Behave as today (`success` metric).
2. Banner is prepended **before** the `askMaxChars` truncation so a very long
   fallback answer still ships with the warning intact.

**Acceptance criteria:**
- Schema path: posted text equals `parsed.Answer`, no banner, `success`
  metric increments.
- Fallback path: posted text begins with the banner, `fallback_raw` metric
  increments.
- Existing `parse_failed` path (truly empty / non-fallback-eligible) still
  posts `:x: 解析失敗:%v` and increments `parse_failed` metric.
- Long fallback answers: banner + truncated body, banner is not lost to
  truncation.

**Verification:**
```sh
go test ./app/workflow -run TestAskWorkflow_HandleResult -v
go build ./...
```

### Task 3 — End-to-end verification

**Changes:** none (verification only)

**Acceptance criteria:**
- `go test ./...` green across all modules.
- `go test ./test/...` green (covers `import_direction_test.go`).
- `go build ./cmd/agentdock` succeeds.
- Manual sanity: synthesise a `JobResult` whose `RawOutput` is plain text
  without the marker, run through `AskWorkflow.HandleResult` (or a unit-test
  harness that exercises it) and confirm the banner + answer render.

**Verification:**
```sh
go test ./...
go build ./cmd/agentdock
```

## Checkpoints

- **After Task 1:** parser tests green, handler unchanged. Pause to confirm
  threshold value and the constant naming for `ResultSource` values
  (`"schema"` / `"raw_fallback"`).
- **After Task 2:** handler tests green, metric observable. Pause to decide
  whether the banner wording needs UX review with stakeholders before merge.
- **After Task 3:** ready for PR. CI must pass, no worker code touched.

## Risks

- **Banner wording UX:** team may judge the wording too aggressive or too
  permissive. Mitigation: revisit at Task 2 checkpoint; wording is a
  one-line change.
- **Threshold mis-calibration:** too low leaks garbage stdout, too high
  rejects legitimate one-line answers. Mitigation: observe `fallback_raw`
  rate after rollout; adjust via follow-up PR.
- **Metric label collision:** confirm `fallback_raw` is not already a
  registered status value in `shared/metrics`.

## Follow-ups (not part of this plan)

- Sanitize `issue.go:316,323` raw `errMsg` exposure (separate UX PR).
- Review `===ASK_RESULT===` prompt emphasis, gated by `fallback_raw` metric
  trend (spec §Rollout step 4).
- Revisit model selection if fallback frequency stays operationally noisy
  (spec §Rollout step 5).
