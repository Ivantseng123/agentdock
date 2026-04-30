# Worker Agent — Hygiene + Tool-Name Visibility (V8)

> Idea-refine output. Replaces the original "per-agent Backend abstraction"
> direction. Backend taxonomy is deferred until issue #227 (session
> continuity) has a validated user need; until then it is disposable
> infrastructure. Origin: issue #228.

## Problem Statement

**How might we** give Slack thread readers enough signal to decide
"wait / cancel / restart", and close the worker's operator-side observation
gaps (stderr context, hung-job detection, env hygiene), **without**
sacrificing thread-capture reliability or pretending to be a diagnosis tool?

The product positioning in `CLAUDE.md` is explicit: AgentDock is a
*structuring* tool, not a diagnosis tool. Any visibility work has to
respect that ceiling.

## Recommended Direction

**Phase 0 hygiene + tool-name visibility (V3) merged into a small bundle
of in-place patches.** No `Backend` interface, no `Message` taxonomy, no
`Result.SessionID`, no fallback-chain rewrite. Estimated 2-3 days of work,
split into three PRs so each ships an independently user-testable change.

The original proposal (mirror multica's per-CLI Backend pattern, 7
unified message types) was rejected after stress-testing because:

- multica is a desktop chat IDE; its 7-type taxonomy fits a UI surface
  that AgentDock does not have.
- Slack updates are throttled to 15s and rendered as single lines —
  `tool_result` and `thinking` content would be noise, not signal, in
  that envelope.
- The "this unblocks session continuity" framing is circular: #227
  itself is unvalidated, so doing a multi-day refactor *for* #227 is
  speculative-on-speculative.

## Key Assumptions to Validate

- [ ] **Within 1 week of V8 shipping, at least one Slack progress update
      changes a thread reader's behavior** (cancel, follow-up ping,
      comment). Verify by sampling 5-10 long-running jobs manually before
      closing the issue. If no behavior shift is observed, revert the
      tool-name half and keep only the hygiene half.
- [ ] **Adding `ToolInputFirstArg string` to `StreamEvent` captures 95%
      of useful tool intent.** Verify against existing claude stream-json
      fixtures: `Read.file_path`, `Bash.command`, `Grep.pattern` should
      all surface readable values when truncated to 100 chars.
- [ ] **`StatusReport.LastTool == ""` for opencode/codex causes no
      visible regression.** Verify `renderStatusMessage` falls back to
      today's "已經敲了 N 次工具" line — must match current behavior
      byte-for-byte for non-streaming agents.

## MVP Scope

PRs are split for independent user verification, per
`feedback_pr_split_user_testability`.

### PR1 — Hygiene bundle (operator-facing, no Slack changes)

1. **`stderr` truncation on non-zero exit.** `runner.go:188-191` returns
   raw stderr; large stderr blobs spam logs. Mirror the 2KB tail already
   used on the stdout-empty path.
2. **`CLAUDE_CODE_*` env strip.** Filter the inherited `os.Environ()`
   slice to drop residual `CLAUDE_CODE_*` vars from the worker host
   that could pollute agent behavior. Whitelist known-good ones (e.g.
   `CLAUDE_CODE_NO_FLICKER` — see `project_cmux_claude_flicker_workaround`).
3. **Blocked args filter.** Reject `--dangerously-skip-permissions` and
   peers at runtime even when injected via `extra_args`. Memory
   `feedback_worker_deployment_unknown` is the rationale; this enforces
   it in code rather than hoping operators remember.
4. **Version detection.** On worker startup, run `<command> --version`
   for each configured agent and log the result. Failure is a `warn`,
   never a startup blocker — older CLIs without `--version` must not
   crash worker.

### PR2 — Inactivity timeout (behavior change, isolated review)

5. **Semantic inactivity timeout.** Add optional
   `agent.inactivity_timeout` in `worker.yaml`. When set and
   `agent.Stream=true`, send SIGTERM if no `StreamEvent` is observed
   for that duration. Default disabled. **Only affects streaming
   agents** so non-stream CLIs (codex today) are not killed mid-think.

### PR3 — Tool-name visibility (Slack-facing)

6. **`StreamEvent.ToolInputFirstArg`.** Extend the parser in
   `shared/queue/stream.go` to extract the first meaningful string from
   `tool_use.input` (`file_path` / `command` / `pattern` / `path`),
   truncated to 100 chars.
7. **`StatusReport.LastTool string`.** New field, populated by
   `statusAccumulator.recordEvent`. Coexists with the existing
   `LastEvent` string; old field stays for backward compat.
8. **`renderStatusMessage` branch.** When `r.LastTool != ""` and phase
   is `running`, replace the counter line with
   `:wrench: 正在 {tool} · {first_arg}`. Empty string falls back to
   today's behavior — same byte sequence as before.

### Out of scope (and why)

- **`Backend` interface, `Message` taxonomy, `Result.SessionID`,
  `TokenUsage` map** — disposable until #227 has a real user request.
- **`thinking` content in Slack** — 200-char truncation in a 15s
  debounce window converts useful thought into noise.
- **`tool_result` in Slack** — same.
- ~~**opencode `--format json` parsing** — `--pure` interaction is
  unverified, and the V3 fallback (`LastTool == ""`) means opencode
  visibility is a no-op rather than a regression.~~ Reopened and shipped
  in PR4 once `--pure --format json` was empirically verified to emit
  per-tool NDJSON events. The earlier "cascade collapse" concern in
  `worker/agent/runner.go` turned out to be a stdin-not-closed artifact
  on interactive shells; the worker's `exec.Command` defaults already
  close child stdin, so no permission relaxation
  (`OPENCODE_PERMISSION={"*":"allow"}` etc.) was needed. PR4 added a
  flat-shape parser (`shared/queue/stream.go ReadStreamJSONOpencode`),
  generalized the `Stream bool` toggle to a `StreamFormat string`
  selector, and extended `extractFirstArg` with the camelCase `filePath`
  key opencode uses for Read/Edit/Write.
- **codex `app-server` rewrite** — separate 1000+ LOC effort; tracked
  separately if codex stream support ever becomes load-bearing.
- **`JobResult.CostUSD/InputTokens/OutputTokens` propagation** — the
  fields exist in the schema but the runner doesn't populate them. Real
  bug, but unrelated to this issue. Track separately.
- **Slack debounce reduction** — 15s stays.

## Not Doing (and Why)

- **`Backend` abstraction.** Without #227 demand, this is disposable
  infrastructure. `feedback_avoid_disposable_features` is about
  one-shot completeness *after* validating need, not building the
  abstraction first and waiting for need.
- **Mirroring multica's 7 message types.** multica is a desktop IDE.
  AgentDock is a Slack triage bot. Different UI surface, different
  taxonomy.
- **Re-issuing #228.** This document supersedes the original issue
  body in spirit. The issue body itself will be edited (not closed) so
  the issue number remains the public reference.
- **Carrying both this file and the issue body as parallel artifacts.**
  After this lands, the issue body should reference this file as the
  authoritative one-pager rather than duplicate it.

## Open Questions

1. **Coexistence of `StatusReport.LastEvent` (legacy string) and
   `LastTool` (new structured field).** Bias: keep both; new code reads
   `LastTool`, fall-through path keeps `LastEvent` for backward compat.
   Removable later if no consumer reads `LastEvent`.
2. **Default value for `inactivity_timeout`.** Bias: disabled. When
   enabled by operators, suggest 120s as a sensible starting point —
   shorter than `agent.Timeout` (default 5min) but long enough for a
   thinking-heavy claude turn.
3. **`CLAUDE_CODE_*` whitelist composition.** Known keepers:
   `CLAUDE_CODE_NO_FLICKER`. Anything else gets discovered empirically
   in PR1 review when the patch first runs against a real worker.
4. **How to quantify the "people actually look at progress messages"
   assumption.** Bias: manual sample of 5-10 long-running jobs in the
   first week, no instrumentation.

## References

- Issue: <https://github.com/Ivantseng123/agentdock/issues/228>
- Existing pipeline:
  - `shared/queue/stream.go` — `StreamEvent` + claude parser
  - `shared/queue/interface.go:43-63` — `StatusReport`
  - `worker/agent/runner.go` — process management
  - `worker/pool/status.go` — `statusAccumulator`
  - `app/bot/status_listener.go` — Slack render
- Relevant memories:
  - `feedback_avoid_disposable_features` — drove rejection of the
    Backend abstraction in this round
  - `feedback_worker_deployment_unknown` — enforces PR1 #3 (blocked
    args filter)
  - `project_cmux_claude_flicker_workaround` — anchor for PR1 #2
    (env-strip whitelist)
  - `project_deployment_status` — pre-launch, so schema bumps can ship
    without migration shims
  - `feedback_pr_split_user_testability` — drove the 3-PR split
- Reference (kept for context, not adopted):
  - `~/local_file/source-code-library/multica/server/pkg/agent/` —
    14k LOC of per-CLI Backend pattern; useful to know exists, not to
    copy wholesale
