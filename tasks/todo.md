# Todo: Workflow Output Boundary — Ask Raw Fallback

Plan: `tasks/plan.md`
Spec: `docs/superpowers/specs/2026-04-25-workflow-output-boundary-design.md`

## Task 1 — Parser fallback (`app/workflow/ask_parser.go`, `_test.go`)

- [ ] Add `ResultSource string` field to `AskResult` (`json:"-"`)
- [ ] Schema-path success sets `ResultSource = "schema"`
- [ ] Marker-not-found path runs syntactic check, returns `"raw_fallback"` on pass
- [ ] Syntactic check: non-empty after `TrimSpace`, meets min-length (≈10 chars)
- [ ] Test: schema path → `"schema"`
- [ ] Test: missing-marker + plain text → `"raw_fallback"`
- [ ] Test: missing-marker + empty / whitespace / short stdout → error
- [ ] Test: marker present + malformed JSON → unchanged error
- [ ] `go test ./app/workflow -run TestParseAskOutput -v` green
- [ ] `go vet ./...` clean

## Task 2 — Handler banner + metric (`app/workflow/ask.go`, `_test.go`)

- [ ] Branch on `parsed.ResultSource` in `HandleResult`
- [ ] Prepend `:warning: 請驗證輸出答案,AGENT 並未遵守輸出格式\n\n` on fallback path
- [ ] Increment `WorkflowCompletionsTotal{status="fallback_raw"}` on fallback path
- [ ] Banner prepended before `askMaxChars` truncation
- [ ] Test: schema path → no banner, `success` metric
- [ ] Test: fallback path → banner present, `fallback_raw` metric
- [ ] Test: long fallback → banner survives truncation
- [ ] `go test ./app/workflow -run TestAskWorkflow_HandleResult -v` green
- [ ] `go build ./...` clean

## Task 3 — End-to-end verification

- [ ] `go test ./...` green
- [ ] `go test ./test/...` green (import direction)
- [ ] `go build ./cmd/agentdock` succeeds
- [ ] Manual sanity check on synthesised missing-marker `JobResult`

## Checkpoints

- [ ] After Task 1: confirm threshold value and `ResultSource` constant naming
- [ ] After Task 2: confirm banner wording with stakeholders
- [ ] After Task 3: open PR
