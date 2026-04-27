# Ask Fallback Extension: Categorised Failure Recovery

**Date:** 2026-04-26
**Status:** Draft
**Issue:** TBD
**Supersedes:** §Ask Fallback Policy, §Acceptance Criteria #3, and §Design Decisions Resolved #2 of `2026-04-25-workflow-output-boundary-design.md`

## Problem

The previous spec ratified a narrow Ask fallback trigger: missing-marker only. Marker-present-but-JSON-broken cases were intentionally left to fail closed.

That decision held until the next failure shape arrived. On 2026-04-26 (~03:33 UTC), an Ask job emitted a markdown answer that included three literal references to `===ASK_RESULT===` in the body and no JSON object. `segmentAfterMarker` produced three segments, none JSON-shaped, parser returned `expected JSON object after marker`, user saw `:x: 解析失敗`.

This is the fourth distinct parser-side incident in two weeks:

| PR / branch | Failure shape | Fix style |
| --- | --- | --- |
| #121 | doubled `===TRIAGE_RESULT===` markers | parser patch |
| #165 | opencode fence pattern around marker | parser patch |
| #205 | marker entirely missing | fallback (narrow) |
| `fix/ask-parser-marker-in-string` (open) | marker referenced inside JSON string value | parser patch |
| 2026-04-26 incident | marker referenced inside markdown body, no JSON | none yet |

Pattern: LLM output is stochastic; a deterministic parser cannot achieve 100% coverage by case-specific patching. Each patch resolves its case; the next case shows up the next week. Engineering cost per patch is days; user cost per uncovered case is `:x: 解析失敗`.

## Premise Reversal

`2026-04-25-workflow-output-boundary-design.md` §Design Decisions #2 closed:

> Ask fallback surface — missing-marker only. Malformed-JSON cases stay strict because the raw stdout in those cases typically contains JSON debris that would confuse the user.

The "JSON debris" rationale was inferred from one incident shape and does not generalise. The 2026-04-26 case has no JSON debris at all — the body is clean markdown. More fundamentally, the original decision optimised for "schema integrity as a contract" and produced "user sees `:x:` while the agent's answer is sitting right there in stdout".

The reversal: every parse failure that pairs with a non-empty syntactic check passes through fallback. The specific failure mode is carried in metrics labels for operator observability, not in user-facing wording.

## Goals

1. Eliminate user-visible `:x: 解析失敗` for the Ask workflow whenever the agent produced substantive stdout.
2. Stop accumulating parser-side patches per new failure shape.
3. Preserve operator observability into agent format-compliance degradation via categorised metrics.
4. Issue and PR Review strictness is untouched. Their success contracts still require successful side effects.

## Non-Goals

- Changing the Ask schema. `===ASK_RESULT===` + JSON is still the contract agents are prompted to follow.
- Removing `looksLikePlainAnswer`. The minimum-length floor still rejects truly empty stdout.
- Changing Issue or PR Review behavior.
- Introducing a second LLM as parser-of-last-resort.

## Design

### Failure Categories

`AskResult.ResultSource` extends from `{schema, raw_fallback}` to a finer set:

| ResultSource | Trigger |
| --- | --- |
| `schema` | marker present, segment is JSON, unmarshals, `answer` non-empty |
| `fallback_marker_missing` | marker not in stdout (PR #205's case) |
| `fallback_segments_no_json` | marker(s) present, no segment starts with `{` (2026-04-26 case) |
| `fallback_unmarshal` | segment starts with `{`, but `json.Unmarshal` failed (covers in-string marker, doubled markers, fence patterns) |
| `fallback_empty_answer` | JSON valid, `answer` field empty/whitespace |

### Parser Contract

`ParseAskOutput` returns `(AskResult, error)` where:

- `error == nil` for the schema path **and** any fallback path that passes `looksLikePlainAnswer`.
- `error != nil` only when stdout fails the syntactic gate (truly empty / near-empty).

Sketch:

```go
func ParseAskOutput(output string) (AskResult, error) {
    output = strings.TrimSpace(output)
    segments := segmentAfterMarker(output, askMarker)
    if len(segments) == 0 {
        return fallbackOrFail(output, ResultSourceFallbackMarkerMissing)
    }
    lastReason := ResultSourceFallbackSegmentsNoJSON
    for _, seg := range segments {
        if !strings.HasPrefix(seg, "{") {
            continue
        }
        var r AskResult
        if err := json.Unmarshal([]byte(extractJSON(seg)), &r); err != nil {
            lastReason = ResultSourceFallbackUnmarshal
            continue
        }
        if strings.TrimSpace(r.Answer) == "" {
            lastReason = ResultSourceFallbackEmptyAnswer
            continue
        }
        r.ResultSource = ResultSourceSchema
        return r, nil
    }
    return fallbackOrFail(output, lastReason)
}

func fallbackOrFail(output, reason string) (AskResult, error) {
    if looksLikePlainAnswer(output) {
        return AskResult{Answer: output, ResultSource: reason}, nil
    }
    return AskResult{}, fmt.Errorf("ask output empty or below min length")
}
```

### HandleResult

`app/workflow/ask.go:443-470` — the `if err != nil` branch shrinks; the `:x: 解析失敗` post-and-return is deleted. Replacement:

```go
parsed, err := ParseAskOutput(r.RawOutput)
if err != nil {
    metrics.WorkflowCompletionsTotal.WithLabelValues("ask", "parse_failed").Inc()
    return w.post(job, ":x: Agent 沒有產生任何答案")
}

answer := parsed.Answer
if parsed.ResultSource != ResultSourceSchema {
    answer = askFallbackBanner + answer
}
metrics.WorkflowCompletionsTotal.WithLabelValues("ask", parsed.ResultSource).Inc()
return w.post(job, answer)
```

### Banner

The existing banner is unchanged and does not vary by `ResultSource`:

```
:warning: 請驗證輸出答案,AGENT 並未遵守輸出格式

<answer body>
```

User only needs "schema violated, verify before acting"; the precise failure mode stays operator-facing.

### Metrics

`WorkflowCompletionsTotal{workflow="ask", status=...}` label values:

- `success` — schema path (kept as-is for dashboard backwards compatibility)
- `fallback_marker_missing` — replaces the previous `fallback_raw` label
- `fallback_segments_no_json` — new
- `fallback_unmarshal` — new
- `fallback_empty_answer` — new
- `parse_failed` — now reserved for true-empty stdout (was the catch-all)

A spike in any specific `fallback_*` label tells ops which failure shape is regressing. Old `fallback_raw` is dropped — the migration is one CHANGELOG line. Pre-launch project, no production dashboards to migrate beyond personal Grafana.

## Acceptance Criteria

1. Ask jobs whose stdout fails any parsing path but passes `looksLikePlainAnswer` post the answer with `askFallbackBanner` prepended. No `:x: 解析失敗` user-visible message.
2. Ask jobs whose stdout fails `looksLikePlainAnswer` post `:x: Agent 沒有產生任何答案` and increment `parse_failed`.
3. Each of the four `fallback_*` metric labels increments exactly under its trigger condition (unit-test enforced).
4. Issue and PR Review behavior is unchanged.
5. Branch `fix/ask-parser-marker-in-string` is closed without merging — its in-string marker case becomes a graceful `fallback_unmarshal` outcome under the new contract, so the parser-side patch is no longer required.

## What This Spec Refuses to Do

Recorded so future-me doesn't re-litigate:

- **Per-case parser patches.** The whole point of this spec is that case-specific patching is unsustainable. Future "add another `markerPositions` rule" proposals should be rejected; reinforce the prompt or accept the fallback.
- **LLM-as-parser.** Second API call, second failure mode, second cost line. The deterministic parser already covers happy path; the new fallback covers everything else cheaply.

## Rollout

1. Land parser + HandleResult changes behind no flag (pre-launch, no users to gate).
2. Update CHANGELOG: `fallback_raw` label removed, `fallback_*` enumeration added.
3. Close `fix/ask-parser-marker-in-string` branch without merging.
4. (Optional) Reinforce prompt to discourage literal marker references in answer body — treated as a quality improvement, not a parser correctness requirement. Fallback absorbs the failure regardless.

## Design Decisions Reversed

Vs. `2026-04-25-workflow-output-boundary-design.md`:

- §Design Decisions #2 (Ask fallback surface — missing-marker only) — **reversed**. All parse failures with non-empty stdout now fall back.
- §Acceptance Criteria #3 (marker present + malformed JSON still fails closed) — **reversed**. Such jobs now fall back with `ResultSource = fallback_unmarshal`.
- §Ask Fallback Policy → Trigger Condition #2 (missing marker only) — **reversed**, see §Failure Categories above.

## Open Questions

- **Banner per category?** Current decision: no. Single banner. User reads answer and decides; operators read metrics. Revisit if user feedback says the warning is too vague.
- **Rename `success` label to `schema`?** Current decision: keep `success` to avoid breaking the one personal dashboard. Trade-off: label name is opinionated. Revisit if it confuses anyone.
