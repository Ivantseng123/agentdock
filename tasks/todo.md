# Todo: Ask Fallback Extension (Categorised Failure Recovery)

Plan: `tasks/plan.md`
Spec: `docs/superpowers/specs/2026-04-26-ask-fallback-extension-design.md`
Branch: `feat/ask-fallback-extension` (from `main`, NOT from `fix/ask-parser-marker-in-string`)

## Pre-flight

- [ ] Confirm with human: spec is approved as-is or with edits
- [ ] Confirm with human: four `fallback_*` constant spellings are final
- [ ] Confirm with human: empty-stdout wording `:x: Agent 沒有產生任何答案`
- [ ] Confirm with human: branch deletion of `fix/ask-parser-marker-in-string` (destructive, requires explicit go-ahead)
- [ ] `git checkout main && git pull && git checkout -b feat/ask-fallback-extension`

## Task 1 — Parser refactor (`app/workflow/ask_parser.go`, `_test.go`)

- [ ] Replace `ResultSourceRawFallback` const with four new consts:
  - [ ] `ResultSourceFallbackMarkerMissing = "fallback_marker_missing"`
  - [ ] `ResultSourceFallbackSegmentsNoJSON = "fallback_segments_no_json"`
  - [ ] `ResultSourceFallbackUnmarshal = "fallback_unmarshal"`
  - [ ] `ResultSourceFallbackEmptyAnswer = "fallback_empty_answer"`
- [ ] Add private helper `fallbackOrFail(output, reason string) (AskResult, error)` per spec sketch
- [ ] Refactor `ParseAskOutput`:
  - [ ] Marker-missing branch routes through `fallbackOrFail(output, ResultSourceFallbackMarkerMissing)`
  - [ ] Segment-not-`{` branch tracks `ResultSourceFallbackSegmentsNoJSON` as `lastReason`
  - [ ] Unmarshal-fail branch tracks `ResultSourceFallbackUnmarshal`
  - [ ] Empty-answer branch tracks `ResultSourceFallbackEmptyAnswer`
  - [ ] Loop fall-through routes through `fallbackOrFail(output, lastReason)`
  - [ ] Schema success unchanged
- [ ] Update doc comment on `ParseAskOutput` to describe the new contract; remove the pointer to old §Design Decisions #2
- [ ] Test updates in `ask_parser_test.go`:
  - [ ] `TestParseAskOutput_Valid` — unchanged (schema path)
  - [ ] `TestParseAskOutput_MarkerMissing_FallbackToRaw` → rename + assert new constant `ResultSourceFallbackMarkerMissing`
  - [ ] `TestParseAskOutput_MalformedJSON` → flip semantic: now expects fallback success with `ResultSourceFallbackUnmarshal`; rename to `TestParseAskOutput_MalformedJSON_FallsBackToUnmarshal`
  - [ ] `TestParseAskOutput_EmptyAnswer` → flip semantic: now expects fallback success with `ResultSourceFallbackEmptyAnswer`; rename to `TestParseAskOutput_EmptyAnswer_FallsBackToEmptyAnswer`
  - [ ] Add `TestParseAskOutput_MarkerInBodyNoJSON` (covers 2026-04-26 incident: marker referenced in markdown body, no JSON object) → expects `ResultSourceFallbackSegmentsNoJSON`
  - [ ] `TestParseAskOutput_MarkerMissing_EmptyFails` — keep (true empty still errors)
  - [ ] `TestParseAskOutput_MarkerMissing_WhitespaceOnlyFails` — keep
  - [ ] `TestParseAskOutput_MarkerMissing_TooShortFails` — keep
  - [ ] `TestParseAskOutput_MultipleMarkers_LastWins` — verify still passes (schema path unaffected)
  - [ ] `TestParseAskOutput_FenceMarkers` — verify still passes (schema path)
- [ ] Verify: `go test ./app/workflow -run TestParseAskOutput -v` green
- [ ] Verify: `go vet ./app/...` clean

### Checkpoint after Task 1

- [ ] Stop and confirm with human: are the four `fallback_*` constant spellings + the empty-stdout wording final before continuing to Task 2?

## Task 2 — Handler simplify (`app/workflow/ask.go`, `_test.go`)

- [ ] Replace the `if err != nil { ... }` body in `HandleResult` (`app/workflow/ask.go:443-455`):
  - [ ] Keep `logging.Redact` + truncation + `WARN` log (operator value)
  - [ ] Replace `metrics ... "parse_failed"` increment — keep label, but it now only fires for true-empty stdout
  - [ ] Replace post text with `:x: Agent 沒有產生任何答案`
- [ ] Replace the `parsed.ResultSource == ResultSourceRawFallback` check (`app/workflow/ask.go:459-462`):
  - [ ] Branch on `parsed.ResultSource != ResultSourceSchema` instead
  - [ ] `status = parsed.ResultSource` (the new `fallback_*` value)
  - [ ] Banner prepend unchanged
- [ ] Banner constant `askFallbackBanner` unchanged
- [ ] Test updates in `ask_test.go`:
  - [ ] Existing `TestAskWorkflow_HandleResult_FallbackPrependsBannerAndIncMetric` (line 722) → split or generalise into per-category tests
  - [ ] Add coverage for `fallback_segments_no_json` metric label increment + banner present
  - [ ] Add coverage for `fallback_unmarshal` metric label increment + banner present
  - [ ] Add coverage for `fallback_empty_answer` metric label increment + banner present
  - [ ] Add coverage for true-empty stdout → posts `:x: Agent 沒有產生任何答案` + `parse_failed` metric
  - [ ] Existing `TestAskWorkflow_HandleResult_SchemaPathHasNoBanner` — verify unchanged
- [ ] Verify: `go test ./app/workflow -run TestAskWorkflow_HandleResult -v` green
- [ ] Verify: `go build ./cmd/agentdock` succeeds

### Checkpoint after Task 2

- [ ] `go test ./...` green across all modules
- [ ] `go test ./test/...` green (import direction)
- [ ] Stop and confirm with human: handler behaviour matches expectation before housekeeping

## Task 3 — Cleanup (spec annotation + CHANGELOG + branch close)

- [ ] Add reversed-by note to top of `docs/superpowers/specs/2026-04-25-workflow-output-boundary-design.md`:
  - [ ] `**Reversed in part by:** docs/superpowers/specs/2026-04-26-ask-fallback-extension-design.md (§Design Decisions #2, §Acceptance Criteria #3, §Ask Fallback Policy → Trigger Condition #2)`
- [ ] Locate CHANGELOG-of-record (release-please managed `CHANGELOG.md` or release-notes draft):
  - [ ] Add entry: `Ask metric label fallback_raw removed; replaced by fallback_marker_missing, fallback_segments_no_json, fallback_unmarshal, fallback_empty_answer.`
- [ ] Confirm with human (per memory `feedback_merge_authorization`): delete local branch `fix/ask-parser-marker-in-string`?
  - [ ] If yes: `git branch -D fix/ask-parser-marker-in-string` (commit `2c4a2db` recoverable from reflog 90 days)
  - [ ] If no: leave branch as-is; spec acceptance only requires "not merged"

### Checkpoint after Task 3 (Complete)

- [ ] Open PR on `feat/ask-fallback-extension`
- [ ] **STOP after PR open** — per memory, do not self-merge; user verifies CI
- [ ] Image rebuild + k8s redeploy is downstream (release-please flow), not part of this plan
