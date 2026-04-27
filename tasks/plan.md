# Implementation Plan: Ask Fallback Extension (Categorised Failure Recovery)

**Spec:** `docs/superpowers/specs/2026-04-26-ask-fallback-extension-design.md`
**Date:** 2026-04-26
**Branch suggestion:** `feat/ask-fallback-extension` (NOT continued from `fix/ask-parser-marker-in-string`)

## Overview

Extend Ask parser fallback to cover **all** parse-failure shapes (not just missing-marker), with categorised metric labels (`fallback_marker_missing` / `fallback_segments_no_json` / `fallback_unmarshal` / `fallback_empty_answer`) for operator observability. Reverses §Design Decisions #2 + §Acceptance Criteria #3 of `2026-04-25-workflow-output-boundary-design.md`.

User-visible outcome: `:x: 解析失敗` no longer surfaces when stdout has substantive content. Only truly-empty stdout produces `:x: Agent 沒有產生任何答案`.

## Architecture Decisions

1. **Branch from `main`, not from `fix/ask-parser-marker-in-string`.** The pending branch's string-aware `markerPositions` patch becomes redundant — its in-string marker case now falls through to `fallback_unmarshal` gracefully under the new contract. Keeping the patch contradicts spec §What This Spec Refuses to Do (per-case parser patches).
2. **Single banner, not per-category.** Banner is a transparency signal; users read the answer + warning, operators read metric breakdown. Spec §Banner.
3. **Keep `success` label for schema path.** Backwards-compat for the existing personal Grafana dashboard. Spec §Open Questions.
4. **Drop `fallback_raw` label entirely.** Replaced by the four `fallback_*` labels. Pre-launch project, no production dashboard migration burden.
5. **`fix/ask-parser-marker-in-string` is closed without merge** per spec §Acceptance Criteria #5. Local-only branch (no remote, no PR), so deletion is `git branch -D` — destructive, requires user confirmation.

## Dependency Graph

```
Task 1 (parser: ResultSource enum + ParseAskOutput contract)
    └─ Task 2 (handler: simplify HandleResult + route ResultSource → metric label)
            └─ Task 3 (cleanup: annotate old spec + CHANGELOG + close stale branch)
```

Task 2 depends on Task 1 because the new label values must exist before HandleResult routes them. Task 3 is documentation + housekeeping; depends on Task 2 only because CHANGELOG references the merged behavior.

## Task List

### Phase 1: Parser

#### Task 1 — Extend ResultSource enum + ParseAskOutput contract change

**Description:** Replace `ResultSourceRawFallback` with four categorised constants. Refactor `ParseAskOutput` so every parse-failure shape (marker missing, segments no JSON, unmarshal failed, empty answer) routes through a single `fallbackOrFail` helper that gates on `looksLikePlainAnswer`. Only true-empty stdout returns an error.

**Acceptance criteria:**
- [ ] Constants: `ResultSourceSchema`, `ResultSourceFallbackMarkerMissing`, `ResultSourceFallbackSegmentsNoJSON`, `ResultSourceFallbackUnmarshal`, `ResultSourceFallbackEmptyAnswer`. Old `ResultSourceRawFallback` removed.
- [ ] `ParseAskOutput` returns `error == nil` whenever `looksLikePlainAnswer(output) == true`, regardless of which parsing path failed.
- [ ] `ParseAskOutput` returns `error != nil` only when `looksLikePlainAnswer` rejects (truly empty / below min length).
- [ ] Each fallback path sets the correct `ResultSource` value (the four `fallback_*` constants).
- [ ] Schema-path success continues to set `ResultSource = ResultSourceSchema`.

**Verification:**
- [ ] `go test ./app/workflow -run TestParseAskOutput -v` green
- [ ] `go vet ./app/...` clean
- [ ] Test cases (added or updated):
  - schema happy path → `ResultSourceSchema`
  - marker missing + plain text → `ResultSourceFallbackMarkerMissing`
  - marker present + body has no `{` segment (2026-04-26 case) → `ResultSourceFallbackSegmentsNoJSON`
  - marker present + JSON-shaped segment but unmarshal fails (in-string marker / fence / doubled marker) → `ResultSourceFallbackUnmarshal`
  - marker present + JSON valid + `answer` empty → `ResultSourceFallbackEmptyAnswer` (rest of body fills the fallback Answer)
  - empty / whitespace-only / sub-min-length stdout → `error != nil`
- [ ] Existing `TestParseAskOutput_MalformedJSON` semantic flips: now expects fallback success (rename test accordingly).
- [ ] Existing `TestParseAskOutput_EmptyAnswer` semantic flips: now expects fallback success.
- [ ] Existing `TestParseAskOutput_MarkerInsideAnswer` (from `fix/ask-parser-marker-in-string`) is **not** ported — that branch is closed; behavior is covered by the new `fallback_unmarshal` test.

**Dependencies:** None.

**Files likely touched:**
- `app/workflow/ask_parser.go`
- `app/workflow/ask_parser_test.go`

**Estimated scope:** S (2 files)

---

### Checkpoint: After Task 1

- [ ] Parser tests green; `ResultSource` enum extended; `ParseAskOutput` contract is "error iff syntactic gate fails".
- [ ] Diff to `ask_parser.go` is contained (one helper added, one constant block edited, one function refactored). No drive-by edits.
- [ ] Confirm with human: are the four `fallback_*` label spellings final? They become metric label values that are awkward to rename later.

---

### Phase 2: Handler

#### Task 2 — HandleResult: drop `:x: 解析失敗`, route ResultSource to metric label

**Description:** Simplify `HandleResult` so the `if err != nil` branch only handles truly-empty stdout (post `:x: Agent 沒有產生任何答案`). On parser success, prepend the existing `askFallbackBanner` whenever `parsed.ResultSource != ResultSourceSchema`, and emit `WorkflowCompletionsTotal{workflow="ask", status=parsed.ResultSource}` (or `"success"` when schema). Banner does not vary by category.

**Acceptance criteria:**
- [ ] `:x: 解析失敗：%v` post-and-return removed from `app/workflow/ask.go`.
- [ ] New error-branch wording: `:x: Agent 沒有產生任何答案` (no leak of internal error string).
- [ ] Schema path: no banner, metric label `success`.
- [ ] Any `fallback_*` path: banner prepended, metric label = the `ResultSource` value.
- [ ] Banner prepended **before** the `askMaxChars` truncation check (preserves PR #205 ordering).
- [ ] `r.Status="completed"` left unchanged on the empty-output error path (Ask still has no retry lane; preserve PR #205 comment intent).

**Verification:**
- [ ] `go test ./app/workflow -run TestAskWorkflow_HandleResult -v` green
- [ ] `go build ./cmd/agentdock` succeeds
- [ ] Test cases (updated):
  - schema path → no banner, `success` metric +1
  - each of 4 fallback paths → banner present, correct `fallback_*` metric +1
  - true-empty stdout → posts `:x: Agent 沒有產生任何答案`, `parse_failed` metric +1
- [ ] Existing `TestAskWorkflow_HandleResult_FallbackPrependsBannerAndIncMetric` is renamed/split into per-category tests; the old `fallback_raw` label is no longer asserted.

**Dependencies:** Task 1.

**Files likely touched:**
- `app/workflow/ask.go`
- `app/workflow/ask_test.go`

**Estimated scope:** S (2 files)

---

### Checkpoint: After Task 2

- [ ] Handler tests green; metric labels exactly match the four `fallback_*` constants + `success` + `parse_failed`.
- [ ] `go test ./...` green across modules.
- [ ] `go test ./test/...` green (import direction).
- [ ] Confirm with human: empty-stdout wording (`:x: Agent 沒有產生任何答案`) is acceptable, or revise.

---

### Phase 3: Cleanup

#### Task 3 — Annotate old spec, update CHANGELOG, close stale branch

**Description:** Mark the reversed sections in `2026-04-25-workflow-output-boundary-design.md` so future readers find the reversal. Add a CHANGELOG entry for the metric label rename (`fallback_raw` removed, four `fallback_*` added). Confirm with user before deleting local branch `fix/ask-parser-marker-in-string` (destructive; no remote, no PR, but commit `2c4a2db` lives only there).

**Acceptance criteria:**
- [ ] `2026-04-25-workflow-output-boundary-design.md` gains a top-of-file note: `**Reversed in part by:** docs/superpowers/specs/2026-04-26-ask-fallback-extension-design.md (§Design Decisions #2, §Acceptance Criteria #3, §Ask Fallback Policy → Trigger Condition #2)`.
- [ ] CHANGELOG entry under appropriate section: `Ask metric label fallback_raw removed; replaced by fallback_marker_missing, fallback_segments_no_json, fallback_unmarshal, fallback_empty_answer.`
- [ ] User confirms before any `git branch -D fix/ask-parser-marker-in-string` is run. If user prefers to keep the branch locally for reference, that's fine — close-by-deletion is not strictly required for the spec acceptance, only that the branch is not merged.

**Verification:**
- [ ] `grep -n "Reversed" docs/superpowers/specs/2026-04-25-workflow-output-boundary-design.md` finds the new note.
- [ ] CHANGELOG diff shows the metric label migration entry.
- [ ] `git branch --list fix/ask-parser-marker-in-string` outcome matches user's decision (deleted or retained).

**Dependencies:** Task 2.

**Files likely touched:**
- `docs/superpowers/specs/2026-04-25-workflow-output-boundary-design.md`
- `CHANGELOG.md` (path TBD — confirm existing CHANGELOG location)

**Estimated scope:** XS (2 files + git op)

---

### Checkpoint: After Task 3 (Complete)

- [ ] All acceptance criteria from spec §Acceptance Criteria met.
- [ ] PR opened on `feat/ask-fallback-extension` from `main`. Per memory `feedback_merge_authorization`, stop after opening PR; user verifies CI before merge.
- [ ] Image rebuild + k8s redeploy is downstream and is **not** part of this plan — covered by existing release-please flow.

## Risks and Mitigations

| Risk | Impact | Mitigation |
|------|--------|------------|
| Renaming metric labels breaks user's personal Grafana panels | Low (pre-launch, single dashboard) | Explicit CHANGELOG note. User updates dashboard at deploy time. |
| `looksLikePlainAnswer` minimum-length (10 runes) lets through obviously-broken stdout (e.g. `null null null`) | Low | Banner warns user. If false-positive rate is noisy in practice, revisit threshold separately — out of scope for this plan. |
| Closing `fix/ask-parser-marker-in-string` loses commit `2c4a2db` | Low | Commit lives in reflog for 90 days; the test it added is documented in spec §Failure Categories as the `fallback_unmarshal` reference case. Branch is local-only with no PR, so no one else depends on it. |
| Future incident proposes "add another `markerPositions` rule" | Medium | Spec §What This Spec Refuses to Do is explicit. Reject and reinforce prompt instead. |

## Open Questions

- **Banner per category?** Default: no. Single banner `:warning: 請驗證輸出答案,AGENT 並未遵守輸出格式`. Revisit if user feedback says it's too vague.
- **Rename `success` label to `schema`?** Default: keep `success` for backwards-compat with existing dashboard.
- **CHANGELOG file path?** Confirm at Task 3 — release-please usually maintains it; if managed by automation, the entry may go in a different file (e.g. release-notes draft).
