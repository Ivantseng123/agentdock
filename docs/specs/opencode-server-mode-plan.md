# Implementation Plan: Opencode Server Mode

References:
- Spec: [`opencode-server-mode.md`](./opencode-server-mode.md)
- ADR: [`../adr/0005-opencode-server-mode.md`](../adr/0005-opencode-server-mode.md)
- Domain context: [`../../CONTEXT.md`](../../CONTEXT.md) (terms: *Agent runtime mode*, *Worker deployment shape*)

## Overview

Two-phase delivery. Phase 3.1 in branch `poc/opencode-server-mode` writes a throwaway Go POC validating P1-P10 hidden assumptions on the user's Mac; only `REPORT.md` is merged back to main. Phase 3.1 → 3.2 ship gate = all P-criteria green under the failure-classification rules below; `provider_retry_transient` counts in P2/P3/P9 are recorded separately and reviewed, not auto-red by themselves. Phase 3.2 in main branch adds server-mode runtime alongside spawn legacy, default `opencode.mode: spawn`, prod observation ≥ 2 weeks before flipping to `server`. Code changes are 100% inside `worker/` + a small worker-init template touch in `cmd/agentdock/init.go`; `app/` is untouched and the worker → app `RawOutput` Redis contract is preserved.

## Architecture decisions

See ADR-0005 for full rationale. Locked-in choices that drive this plan:

- **Mapping**: 1 `opencode serve` subprocess per worker process; pool's N goroutines share via per-request `x-opencode-directory` header.
- **Lifecycle**: lazy. Server spawns on first job, stops after `opencode.idle_timeout` of pool inactivity, gracefully torn down on worker SIGTERM.
- **Storage isolation**: subprocess receives `XDG_DATA_HOME=$worker_owned_dir`. `XDG_CONFIG_HOME` and `XDG_CACHE_HOME` inherited unchanged so user auth carries over.
- **Rollout**: top-level `opencode:` config block; first release default `mode: spawn`. ≥ 2 weeks of green prod metrics → flip default to `server`. ≥ 2 more weeks → spawn path deletion can be discussed.
- **No auto-fallback** between modes. Failed server-mode jobs fail loudly.
- **Bug A user-facing message**: `「LLM 回應為空，請稍後再試或改用 @bot issue」` (Slack mrkdwn) on `finish=other && tokens.output=0` outcome.
- **PR split**: Phase 3.2 ships as 4 PRs aligned with Stages 1-4 below. Each PR leaves `mode: spawn` default working, lets the user pull and ask-test.

---

## Phase 3.1 — POC (branch `poc/opencode-server-mode`)

POC code is throwaway. Acceptance is per P-criterion in spec Success Criteria. Tasks below describe how each P-criterion gets verified; the POC main returns a non-zero exit on any red.

### Task 3.1-1: Fixture extraction

**Description:** Promote the original captured export if it is available locally; otherwise supplement it with a sanitized synthetic multimodal fixture that preserves the same transport shape (one prompt + 4 PNG attachments + stable `===ASK_RESULT===` contract). The committed fixture must be safe to ship on the POC branch.

**Acceptance criteria:**
- [ ] `testdata/harbor-4images/prompt.txt` contains a sanitized multimodal prompt with a stable structured-output contract
- [ ] `testdata/harbor-4images/image-{1,2,3,4}.png` exist and are readable PNG fixtures committed in-repo
- [ ] `testdata/harbor-4images/expected-marker.txt` documents what a successful answer must contain (e.g. `===ASK_RESULT===` and JSON `answer` key non-empty)

**Verification:**
- [ ] `file testdata/harbor-4images/image-1.png` reports a PNG, all four readable
- [ ] `wc -c testdata/harbor-4images/prompt.txt` > 0
- [ ] Code review: no PII / company-internal handles left in `prompt.txt`

**Dependencies:** None.

**Files likely touched:**
- `testdata/harbor-4images/prompt.txt`, `image-{1..4}.png`, `expected-marker.txt`
- `cmd/dev/poc-opencode-server/fixture.go` (loader)

**Estimated scope:** S

---

### Task 3.1-2: Server supervisor (POC always-on)

**Description:** A minimal Go supervisor that spawns `opencode serve --port=0 --hostname=127.0.0.1` as a child process, polls `/health` until ready, exposes the resolved port, and supports graceful shutdown. Lazy lifecycle is deferred to Task 3.1-9; this is the always-on baseline.

**Acceptance criteria:**
- [ ] `Start(ctx, opencodePath, opts)` returns a `Handle` with `BaseURL()` and `Stop()` methods
- [ ] `/health` polled with backoff up to 10s; failure returns descriptive error
- [ ] `Stop()` sends SIGTERM, waits up to 5s, falls back to SIGKILL; returns no orphan child processes (verified by `ps` snapshot pre/post)

**Verification:**
- [ ] Manual: run a POC main that calls `Start` → `Stop` 5 times in a row; `ps -ef | grep opencode` shows zero leftover after each cycle
- [ ] `go vet ./cmd/dev/poc-opencode-server/...` clean

**Dependencies:** None.

**Files likely touched:**
- `cmd/dev/poc-opencode-server/server.go`

**Estimated scope:** S

---

### Task 3.1-3: HTTP/SSE client (single happy-path session)

**Description:** Go client over the supervisor's `BaseURL`. Implements `CreateSession(directory) → sessionID`, `SendPrompt(sessionID, text) → (chan StreamEvent, error)`, and an SSE consumer that strips `data: ` prefix and feeds the inner JSON line into a parser. Reuses the event taxonomy from `shared/queue/stream.go` shape (text, tool_use, step_start, step_finish, error). For POC we do not import `shared/`; we mirror the JSON shapes locally so the POC stays standalone.

**Acceptance criteria:**
- [ ] Single fixture replay returns a `text` event whose `part.text` ends with the `===ASK_RESULT===` block parseable as JSON with non-empty `answer`
- [ ] SSE heartbeat events (`server.heartbeat`) ignored, do not pollute event channel
- [ ] HTTP errors and SSE disconnects surface as Go errors (not silent empty channel)

**Verification:**
- [ ] POC main runs `Start → CreateSession → SendPrompt → consume → Stop` against opencode 1.14.29; stdout shows the answer JSON
- [ ] Inject `kill -SIGSTOP <opencode-pid>` mid-stream → client returns error within 30s, not hang

**Dependencies:** 3.1-2.

**Files likely touched:**
- `cmd/dev/poc-opencode-server/client.go`
- `cmd/dev/poc-opencode-server/events.go` (event JSON types)

**Estimated scope:** M

---

### Checkpoint A: Plumbing works

- One round-trip fixture replay returns a valid answer with `===ASK_RESULT===` block
- Supervisor + client + fixture all clean; ready for batched P-criteria

### Task 3.1-4: P1 + P2 + P3 — single-session validity & cold start

**Description:** Aggregate runner for P1 (cold start ≤ 10s), P2 (1.14.29 fixture replay × 100 attempts), P3 (HEAD fixture replay × 100 attempts). Records latency, success/fail, raw stderr tail, and classifies any failed attempt as either `server_mode_regression` or `provider_retry_transient`.

**Acceptance criteria:**
- [ ] P1: 5 cold-start cycles measured, max < 10000ms
- [ ] P2: 100 attempts against opencode 1.14.29; `server_mode_regression_count = 0`
- [ ] P3: 100 attempts against opencode HEAD; `server_mode_regression_count = 0`
- [ ] `provider_retry_transient_count` is reported separately for P2 and P3
- [ ] Each successful replay returns `===ASK_RESULT===` JSON with non-empty `answer`
- [ ] Any unclassified failure counts as `server_mode_regression`

**Verification:**
- [ ] POC main `-criteria=p1,p2,p3` prints summary + per-run latency histogram
- [ ] Failed run includes `sessionID`, exit code, stderr tail, and classified error detail in REPORT data

**Dependencies:** 3.1-3.

**Files likely touched:**
- `cmd/dev/poc-opencode-server/main.go`
- `cmd/dev/poc-opencode-server/criteria_p123.go`

**Estimated scope:** M

---

### Task 3.1-5: P4 + P5 — concurrency & cwd isolation

**Description:** Two sub-runners. P4: 8 sessions × 5 batches = 40 calls in parallel using the same fixture; assert all 40 success, no cross-session bleed (each answer's hash must match expected). P5: 8 sessions each created with a different `directory`, each cwd contains a unique `marker-N.txt`; prompt asks the model to read `marker.txt` and echo content; assert each session reads only its own marker.

**Acceptance criteria:**
- [ ] P4: 40 / 40 success; 40 unique answer hashes match expected
- [ ] P5: 8 / 8 sessions read only their own marker; no foreign content appears

**Verification:**
- [ ] POC main `-criteria=p4,p5` prints per-batch matrix (success/failure)
- [ ] Force a failure (manually corrupt one marker) → POC reports red

**Dependencies:** 3.1-4.

**Files likely touched:**
- `cmd/dev/poc-opencode-server/criteria_p45.go`
- `testdata/concurrency/{marker-1..8.txt}`

**Estimated scope:** M

---

### Task 3.1-6: P6 + P10 — plugin & storage isolation

**Description:** P6: cwd contains `.opencode/skills/poison-plugin/SKILL.md` whose content would be obvious if loaded (e.g. forces a specific reply pattern); assert the agent's reply does NOT contain that pattern. P10: POC sets `XDG_DATA_HOME=/tmp/poc-opencode-xdg-data` for the spawned server; replay fixture; assert (a) `/tmp/poc-opencode-xdg-data/opencode/opencode.db` has session rows, (b) user's `~/.local/share/opencode/opencode.db` mtime+size unchanged.

**Acceptance criteria:**
- [ ] P6: poison plugin not loaded (reply does not match poison pattern)
- [ ] P10: isolated DB grew; user DB byte-identical (sha256 before vs after)

**Verification:**
- [ ] Run with `XDG_DATA_HOME=/tmp/poc-...`, sha256sum user DB before+after
- [ ] Inspect `sqlite3 /tmp/poc-...//opencode.db "SELECT id FROM sessions"` shows session(s)

**Dependencies:** 3.1-3.

**Files likely touched:**
- `cmd/dev/poc-opencode-server/criteria_p6_p10.go`
- `testdata/poison-plugin/SKILL.md`

**Estimated scope:** S

---

### Task 3.1-7: P7 — Bug A detection

**Description:** Implement the worker-side detection logic in POC: at end of session, if final assistant message has `finish=other` AND `tokens.output=0` AND no `text` part received, treat as failed. Drive the positive case with a synthetic empty-SSE / mock-provider transcript so P7 validates the detector itself without contradicting P2/P3's requirement that `harbor-4images` succeeds in server mode.

**Acceptance criteria:**
- [ ] Synthetic empty-SSE transcript with `finish=other` + `tokens.output=0` → detector reports failure with reason `"empty SSE / finish=other / 0 output tokens"`
- [ ] Replay simple text-only "say OK" prompt → detector reports success
- [ ] False positive rate on P2 successful replays = 0 / 100

**Verification:**
- [ ] POC main `-criteria=p7` runs both positive and negative cases, reports both green
- [ ] Detector code lives in `cmd/dev/poc-opencode-server/detector.go` so production task 3.2-11 can mirror it

**Dependencies:** 3.1-3, 3.1-4 (uses P2 corpus for false-positive check).

**Files likely touched:**
- `cmd/dev/poc-opencode-server/criteria_p7.go`
- `cmd/dev/poc-opencode-server/detector.go`

**Estimated scope:** M

---

### Task 3.1-8: P8 — schema cross-version diff

**Description:** Fetch `/doc` (or equivalent OpenAPI endpoint) on opencode 1.14.29 and HEAD; diff the schemas focused on `/session`, `/session/{id}/message`, `/event`. Assert no required field removed, no endpoint renamed, no breaking type change.

**Acceptance criteria:**
- [ ] Both versions expose an OpenAPI endpoint and POC fetches both
- [ ] Diff report enumerates added / removed / changed fields per endpoint
- [ ] All changes classified non-breaking by criteria documented in REPORT

**Verification:**
- [ ] POC main `-criteria=p8` prints diff summary; manual review of report
- [ ] If endpoint missing, P8 fails with "OpenAPI not exposed" — fail loud

**Dependencies:** 3.1-2 (need supervisor up to fetch).

**Files likely touched:**
- `cmd/dev/poc-opencode-server/criteria_p8.go`

**Estimated scope:** S

---

### Task 3.1-9: P9 — lazy lifecycle

**Description:** Extend supervisor with lazy mode. Worker boot does not spawn server. First job triggers spawn (boot lock prevents concurrent spawn from N goroutines). After 30s of no active sessions, server gracefully stops. Next job re-spawns. Replays harbor-4images fixture before the idle-stop cycle and requires at least one successful post-respawn happy-path replay. Retry-transient noise is reported separately and does not by itself fail the lifecycle verdict.

**Acceptance criteria:**
- [ ] Spawn → busy → 30s idle → auto-stop → re-spawn cycle completes once
- [ ] Concurrent first-job from 8 goroutines spawns exactly 1 server (verified by counting `Start` calls)
- [ ] Fixture replay after re-spawn succeeds at least once with a parseable `===ASK_RESULT===` answer
- [ ] If a post-respawn replay fails only as `provider_retry_transient`, it is reported separately and retried; only `server_mode_regression` fails P9

**Verification:**
- [ ] POC main `-criteria=p9 -idle-timeout=30s` runs full cycle, reports timestamps
- [ ] `ps -ef | grep opencode` snapshot at idle window shows zero opencode processes

**Dependencies:** 3.1-2, 3.1-4 (reuses replay logic).

**Files likely touched:**
- `cmd/dev/poc-opencode-server/server.go` (add lazy mode)
- `cmd/dev/poc-opencode-server/criteria_p9.go`

**Estimated scope:** M

---

### Task 3.1-10: REPORT.md aggregator

**Description:** A single POC entry `cmd/dev/poc-opencode-server/main.go -all` runs P1-P10, captures raw data per criterion, and writes `REPORT.md` with verdict, raw numbers, and next-step recommendation. For P2/P3/P9, the report must distinguish `server_mode_regression` from `provider_retry_transient`.

**Acceptance criteria:**
- [ ] Single command runs all 10 criteria
- [ ] `REPORT.md` lists each P-criterion: status, raw measurement, time-of-run, opencode version, and when applicable failure classification / transient count
- [ ] Exit code 0 only when all criteria pass under their classification rules; `provider_retry_transient` counts alone do not force non-zero

**Verification:**
- [ ] Run on macOS with both opencode 1.14.29 and HEAD installed
- [ ] Manually break P2 (e.g. point at a non-existent fixture) → REPORT marks red, exit non-zero

**Dependencies:** 3.1-4 through 3.1-9.

**Files likely touched:**
- `cmd/dev/poc-opencode-server/main.go`
- `cmd/dev/poc-opencode-server/report.go`
- `REPORT.md` (committed on poc branch)

**Estimated scope:** S

---

### Checkpoint B — Phase 3.1 → 3.2 ship gate

- [ ] All P1-P10 green under the classification rules above; any `provider_retry_transient` counts are documented in `REPORT.md`
- [ ] `REPORT.md` committed on `poc/opencode-server-mode` branch
- [ ] `REPORT.md` cherry-picked / re-saved to main as `docs/specs/opencode-server-mode-poc-report.md` via a docs-only PR (per CLAUDE.md: docs-only PRs may be self-merged)
- [ ] POC code stays on `poc/...` branch, NOT merged to main
- [ ] If any criterion shows `server_mode_regression` red → loop back to design / spec, do NOT enter Phase 3.2

---

## Phase 3.2 — Production swap (main branch, 4 PRs)

PRs map to Stages 1-4 below. Each PR leaves `opencode.mode` defaulting to `spawn`, so legacy ask path is always working between PRs.

### Stage 1 PR — Foundation (no behavior change)

#### Task 3.2-1: Top-level `opencode:` config schema

**Description:** Add `OpencodeConfig` struct to worker config: `Mode (spawn|server)`, `IdleTimeout time.Duration`, `StorageDir string`. Wire into `Config` struct. Defaults: Mode=spawn, IdleTimeout=5m, StorageDir="" (resolved at runtime to `~/.local/share/agentdock-worker/opencode`). Validate Mode is one of the enum at load.

**Acceptance criteria:**
- [ ] `worker/config/config.go` exposes `OpencodeConfig` and `Config.Opencode OpencodeConfig`
- [ ] Default values applied when block is absent from worker.yaml
- [ ] Invalid `Mode` value rejected at config load with descriptive error

**Verification:**
- [ ] `go test ./worker/config/...` green; new test cases cover (a) default values, (b) custom values from YAML, (c) invalid mode rejection
- [ ] Loading current `worker.yaml` (no `opencode:` block) yields default `OpencodeConfig`

**Dependencies:** None.

**Files likely touched:**
- `worker/config/config.go`
- `worker/config/load.go`
- `worker/config/load_test.go` or `worker/config/config_test.go`

**Estimated scope:** S

---

#### Task 3.2-2: Spawn path extraction

**Description:** Move opencode-specific spawn logic from `worker/agent/runner.go runOne()` into a new `worker/agent/opencode_spawn.go runOneSpawn()`. **Zero behavior change**. The dispatcher will be added in 3.2-3.

**Acceptance criteria:**
- [ ] `runOneSpawn` has the same signature and contract as the inline path it replaces
- [ ] All existing `worker/agent/runner_test.go` tests pass without modification
- [ ] No new public symbols leak outside `worker/agent/`

**Verification:**
- [ ] `go test ./worker/...` green
- [ ] `git diff` of behavior-relevant files shows extraction-only changes (no logic edits)

**Dependencies:** None.

**Files likely touched:**
- `worker/agent/runner.go` (move out)
- `worker/agent/opencode_spawn.go` (new)
- `worker/agent/runner_test.go` (no change expected, kept as regression net)

**Estimated scope:** S

---

#### Task 3.2-3: Dispatcher

**Description:** `runOne` checks if the agent is opencode and routes by `cfg.Opencode.Mode`. `spawn` → `runOneSpawn`. `server` → `runOneServer` which is a stub returning `errors.New("server mode not implemented")` for now (filled in 3.2-7). Other agents (claude / codex / gemini) take the unchanged spawn path regardless of mode setting.

**Acceptance criteria:**
- [ ] `runOne` dispatches based on agent name + `Opencode.Mode`
- [ ] `mode: server` returns explicit error; not silent
- [ ] `mode: spawn` (default) behaves identically to pre-change

**Verification:**
- [ ] `go test ./worker/agent/...` green; new test asserts dispatch matrix (4 agents × 2 modes = 8 cases, only opencode×server path returns the new error)
- [ ] `worker.yaml` with no `opencode:` block runs unchanged behavior

**Dependencies:** 3.2-1, 3.2-2.

**Files likely touched:**
- `worker/agent/runner.go`
- `worker/agent/opencode_server.go` (new, stub only)
- `worker/agent/runner_test.go` (add dispatch matrix test)

**Estimated scope:** S

---

### Checkpoint C — Stage 1 ready to merge

- [ ] `go test ./worker/... ./shared/... ./test/...` green (incl. import direction)
- [ ] Default `mode: spawn` behavior unchanged (F2 satisfied)
- [ ] `mode: server` returns explicit error (intentional stub, surfaces if user enables prematurely)
- [ ] Stage 1 PR opened, reviewed, merged

### Stage 2 PR — Server-mode happy path

#### Task 3.2-4: Opencode version capability check

**Description:** Worker reads `opencode --version` at process start and compares against the `MinimumOpencodeVersion` floor constant. Below floor → log clear error and exit non-zero; worker does not boot in `mode: server`. `mode: spawn` is completely unaffected. The new check is opencode-specific, server-mode-only, and uses a **blocking** policy.

**Coexistence with existing `worker/agent/version.go`.** `worker/agent/version.go` already defines `detectVersion(ctx, command)` and `LogVersions` (called at `worker/worker.go:84`), with an explicit warn-and-continue policy ("agents older than the --version convention must not crash workers"). The new check **must not** silently duplicate or override that file:
- Reuse `detectVersion` (or extract a shared helper) for the probe — don't shell out twice.
- The new gating function lives in `worker/agent/opencode_version.go` next to the existing detector; the package comment in the new file must explicitly cite `version.go LogVersions` and explain the policy split (general detector = warn-only / informational; new check = blocking / opencode + server-mode only).
- `LogVersions` continues to run for all agents including opencode (it's logging, not gating). The new check runs **in addition** when `mode: server`.

**Timing.** Check runs at worker process start, same boot phase as `LogVersions` at `worker/worker.go:84` (typically right after it), **not** at lazy supervisor spawn. Rationale: a bad version should produce a clear pod-restart / CrashLoopBackOff signal, not a silent first-job failure window. The lazy supervisor work in Task 3.2-8 does not change this.

**Semver parsing.** `opencode --version` outputs a bare semver line (e.g. `1.14.41\n`) — note this differs from `claude --version` / `codex --version` which print a `<name> <version>` banner that `detectVersion` returns whole. The parser inside `CheckOpencodeVersion` must handle the bare form: `strings.TrimSpace` + manual `strings.Split(s, ".")` numeric compare against the constant's three components. **No new go.mod dep** (spec N4 stands — `golang.org/x/mod/semver` is not stdlib, do not add). Malformed version output → treated as probe failure (same as binary-missing).

**Probe failure policy in `mode: server`:** binary missing / non-zero exit / malformed output → log error and exit non-zero (block boot), same outcome as below-floor. This is **deliberately stricter** than `LogVersions`'s warn-and-continue. Document this difference in the new file's package comment.

**Initial value of `MinimumOpencodeVersion`.** Set when this task lands, by re-running POC `-all -replay-count=100` on the then-current opencode version and publishing a fresh `opencode-server-mode-poc-report.md` alongside the implementing PR. The earlier `Dockerfile.release` image-bump bisect (which used `-criteria=p3 -replay-count=5` on darwin-arm64) is **NOT** a substitute — spec §Version Policy treats the image pin and the floor as two independent evidence pipelines.

**Runtime swap limitation.** Check is one-shot at boot. If the underlying opencode binary is replaced while the worker is alive (e.g. operator swaps `~/.opencode/bin/opencode` on a laptop deployment per [[project-deployment-status]]'s "worker may run on user's real machine" rule), the new binary is **not** re-checked. Acceptable trade-off: pod images are immutable; laptop operators own this risk. Task 3.2-14 documentation must flag this.

**Corner case — `opencode.mode: server` without opencode in `providers`.** If a user sets `opencode.mode: server` but `cfg.Providers` does not include `opencode`, the check still fires (mode-driven, not provider-driven) and fails fast with a clear "opencode provider not configured" error rather than letting boot succeed and discovering the misconfig later.

**Acceptance criteria:**
- [ ] `worker/agent/opencode_version.go` declares `MinimumOpencodeVersion` constant and `CheckOpencodeVersion(ctx, binaryPath) (detected string, err error)`. Internally reuses `worker/agent/version.go`'s probe helper (refactor `detectVersion` to a shared form if needed).
- [ ] Package comment in `opencode_version.go` explicitly references `version.go LogVersions` and explains the policy split (general warn-and-continue vs. opencode-server-mode block-on-failure).
- [ ] Worker boot path calls `CheckOpencodeVersion` exactly once when `cfg.Opencode.Mode == server`, at the same boot phase as `LogVersions` (right after, in `worker/worker.go`).
- [ ] On below-floor mismatch, returns explicit error containing both detected and required versions plus a pointer to `docs/specs/opencode-server-mode-poc-report.md`; worker exits non-zero.
- [ ] On probe failure (binary missing, non-zero exit, malformed semver) in `mode: server`, same outcome as below-floor (block boot). Different from `LogVersions`'s warn-only policy — call this out in code comments.
- [ ] `mode: spawn` boot path is unaffected: the new check never runs in spawn mode regardless of `agents.opencode.command` resolvability.
- [ ] Initial value of `MinimumOpencodeVersion` is backed by a fresh POC `-all -replay-count=100` run; the regenerated `opencode-server-mode-poc-report.md` is committed in the same PR as this task. Short-sample bisect evidence is not accepted as floor backing.
- [ ] Logging follows `shared/logging/GUIDE.md`: Chinese message, English keys, `component=OpencodeVersion`, `phase=失敗`.

**Verification:**
- [ ] `go test ./worker/agent/...` green; `opencode_version_test.go` covers (a) parse matrix on bare semver, (b) comparison matrix (below / equal / above), (c) malformed input, (d) missing binary.
- [ ] Integration: temporarily set `MinimumOpencodeVersion` above the installed binary; worker boot in `mode: server` fails with the expected Chinese error and non-zero exit. Verify the pod-level signal: `kubectl describe pod` shows the boot failure clearly (not silent degradation).
- [ ] `mode: spawn` worker boots unchanged regardless of installed opencode version, or absence of opencode binary from PATH.
- [ ] `worker/agent/version.go LogVersions` still warns-and-continues for all agents in both modes; no behavior change there.
- [ ] Implementer has compared the new file against `worker/agent/version.go` and confirmed there is no silent overlap or contradictory behavior across the two.

**Dependencies:** 3.2-1.

**Files likely touched:**
- `worker/agent/opencode_version.go` (new — must reuse, not duplicate, the probe helper in `version.go`)
- `worker/agent/opencode_version_test.go` (new)
- `worker/agent/version.go` (possibly minor refactor to export / share the probe; **must not** change the warn-and-continue policy for `LogVersions`)
- `worker/worker.go` (call new check at boot after `LogVersions` when `Opencode.Mode == server`)
- `docs/specs/opencode-server-mode-poc-report.md` (refreshed with the POC `-all -replay-count=100` run that backs the initial floor value — landing in the same PR)

**Estimated scope:** S–M. S for the Go code itself; M when you include the POC re-run that backs the floor value.

---

#### Task 3.2-5: Server supervisor (production)

**Description:** Production version of POC's `server.go`. `worker/agent/server_supervisor.go` provides `Supervisor` with `Start(ctx)`, `Stop(ctx)`, `BaseURL()`, `Password()`. Spawns `opencode serve --port=0 --hostname=127.0.0.1` as child; sets `XDG_DATA_HOME=$cfg.StorageDir`; sets random `OPENCODE_SERVER_PASSWORD`; polls `/health` for readiness; SIGTERM + 5s grace + SIGKILL on Stop. NOT lazy yet — always-on at this task; lazy added in 3.2-8.

**Acceptance criteria:**
- [ ] `Start` blocks until `/health` returns 200 within 10s, errors otherwise
- [ ] `Stop` returns no orphan child processes (verified by ps)
- [ ] Logging follows `shared/logging/GUIDE.md`: component=`OpencodeServer`, phase=`處理中|完成|失敗`, Chinese messages, English keys

**Verification:**
- [ ] `go test ./worker/agent/...` green; new `server_supervisor_test.go` covers Start success, Start timeout, Stop cleanliness
- [ ] Manual: spin up + tear down 10 times, ps shows zero leftover

**Dependencies:** 3.2-1.

**Files likely touched:**
- `worker/agent/server_supervisor.go`
- `worker/agent/server_supervisor_test.go`

**Estimated scope:** M

---

#### Task 3.2-6: HTTP/SSE client wrapper

**Description:** `worker/agent/opencode_http.go` exposes `Client` with `CreateSession(ctx, directory) → sessionID`, `SendPrompt(ctx, sessionID, text) → (<-chan queue.StreamEvent, error)`. SSE consumer strips `data: ` prefix and feeds inner JSON to `shared/queue/stream.go ReadStreamJSONOpencode` (used as a parser over an in-memory pipe to reuse the existing event taxonomy). Heartbeats ignored.

**Acceptance criteria:**
- [ ] `CreateSession` returns a session ID for valid directory; errors on 4xx/5xx
- [ ] `SendPrompt` returns a channel that emits `queue.StreamEvent` matching the same taxonomy as spawn-mode `ReadStreamJSONOpencode`
- [ ] Channel closes on session completion or error; never hangs

**Verification:**
- [ ] `go test ./worker/agent/...` green; `opencode_http_test.go` uses an in-process httptest server emitting canned SSE
- [ ] Smoke test against a real `Supervisor`: 5 fixture replays succeed

**Dependencies:** 3.2-5 (smoke test only); can be developed in parallel with 3.2-5.

**Files likely touched:**
- `worker/agent/opencode_http.go`
- `worker/agent/opencode_http_test.go`

**Estimated scope:** M

---

#### Task 3.2-7: Wire `runOneServer` happy path

**Description:** Glue Supervisor + HTTP client into `runOneServer(ctx, logger, agent, workDir, prompt, opts)`. Returns `(string, error)` with the same contract as `runOneSpawn`. For this stage, supervisor is always-on (not lazy). Bug A detection deferred to 3.2-11. Crash recovery deferred to 3.2-9.

**Acceptance criteria:**
- [ ] `mode: server` end-to-end ask job returns the expected `===ASK_RESULT===` JSON to the worker → app `RawOutput`
- [ ] All `OnEvent` callbacks fire with the same event taxonomy as spawn mode
- [ ] Supervisor singleton scoped per worker process (one server, shared by pool)

**Verification:**
- [ ] Local manual test: worker with `opencode.mode: server` processes one Slack ask end-to-end; Slack receives the answer
- [ ] Integration test in `worker/agent/opencode_server_test.go` runs a minimal end-to-end against a real opencode binary if available (skipped on CI without binary)

**Dependencies:** 3.2-3, 3.2-5, 3.2-6.

**Files likely touched:**
- `worker/agent/opencode_server.go` (replace stub)
- `worker/agent/opencode_server_test.go`
- `worker/worker.go` (wire supervisor singleton at worker boot — minimal change)

**Estimated scope:** M

---

### Checkpoint D — Stage 2 ready to merge

- [ ] `mode: server` happy path runs end-to-end (single Slack ask)
- [ ] `mode: spawn` (default) behavior unchanged
- [ ] Replay harbor-4images fixture × 10 in `mode: server`: 0 silent drops
- [ ] Worker in `mode: server` refuses to boot when installed opencode < `MinimumOpencodeVersion` (clear Chinese error, non-zero exit)
- [ ] Stage 2 PR opened, reviewed, merged

### Stage 3 PR — Resilience

#### Task 3.2-8: Lazy lifecycle

**Description:** Make supervisor lazy. Worker boot does not spawn. First job triggers spawn under a `sync.Once` / state-machine boot lock so concurrent goroutines coalesce. After `cfg.Opencode.IdleTimeout` of zero active sessions, supervisor gracefully stops the server. Next job re-spawns.

**Acceptance criteria:**
- [ ] Boot does not spawn server (`ps` snapshot at worker boot shows no opencode child)
- [ ] 8 goroutines pulling jobs simultaneously cause exactly 1 spawn (boot lock works)
- [ ] After IdleTimeout of inactivity, server stops; next job re-spawns and succeeds

**Verification:**
- [ ] Unit tests for boot lock under contention (concurrent `Acquire`)
- [ ] Manual: set `idle_timeout: 30s`, run 1 job, wait 60s, run another job; observe spawn → stop → re-spawn in logs

**Dependencies:** 3.2-5, 3.2-7.

**Files likely touched:**
- `worker/agent/server_supervisor.go` (state machine, idle tracker)
- `worker/agent/server_supervisor_test.go` (concurrency cases)

**Estimated scope:** S

---

#### Task 3.2-9: Crash recovery

**Description:** `cmd.Wait()` goroutine watches the server subprocess; on exit, supervisor marks state as `crashed`. Next job triggers re-spawn. Any in-flight job at crash time receives an error from `runOneServer`; the runner retries the job exactly once on a fresh session against the recovered server. Second failure → fail loudly, no further retry, no fallback to spawn (per spec C4).

**Acceptance criteria:**
- [ ] When server is killed externally (`kill -9`), supervisor detects within 1s
- [ ] In-flight job receives error; retry policy runs once
- [ ] Second crash within retry → job marked failed, returns explicit error to app

**Verification:**
- [ ] Test: spawn supervisor, start a long-running session, `kill -9` server PID, verify retry → success on re-spawned server
- [ ] Test: spawn supervisor, kill twice → verify final error, no infinite retry

**Dependencies:** 3.2-5, 3.2-7.

**Files likely touched:**
- `worker/agent/server_supervisor.go` (Wait goroutine, state)
- `worker/agent/opencode_server.go` (retry orchestration)
- `worker/agent/opencode_server_test.go`

**Estimated scope:** S-M

---

#### Task 3.2-10: Worker SIGTERM teardown

**Description:** On worker shutdown, the server-mode supervisor: (1) issues `POST /session/{id}/abort` to every active session; (2) waits up to 30s for those to drain; (3) SIGTERMs the server; (4) SIGKILLs after 5s if still alive. No orphan child processes. Validate on macOS and Linux.

**Acceptance criteria:**
- [ ] Worker SIGTERM during 3 in-flight asks → all 3 receive abort acks within 30s OR get force-failed; server child exits cleanly
- [ ] `ps` snapshot post-shutdown: zero opencode children
- [ ] No goroutine leaks (verified by `runtime.NumGoroutine` before / after if testable)

**Verification:**
- [ ] Local manual: macOS run worker with 3 concurrent asks, send SIGTERM, ps shows zero children
- [ ] Integration test: spawn supervisor, simulate 3 concurrent active sessions, call `Drain()` → all abort calls fire, then Stop returns clean

**Dependencies:** 3.2-5, 3.2-7.

**Files likely touched:**
- `worker/agent/server_supervisor.go` (Drain method)
- `worker/worker.go` (signal handler integration)

**Estimated scope:** S

---

#### Task 3.2-11: Bug A detection

**Description:** SSE consumer accumulates the final assistant message's `finish` reason and `tokens.output`. After session ends, runner inspects: if `finish == "other" && tokens.output == 0 && no text part received`, mark job failed with reason `"opencode returned empty stream (finish=other, 0 output tokens)"`. The user-facing Slack message should surface `「LLM 回應為空，請稍後再試或改用 @bot issue」` through the existing failure path; if that copy cannot be rendered without touching `app/`, stop and review before widening scope. The worker's "Agent 執行成功 output_len=0" log line is replaced with `「Agent 執行失敗 (空 stream)」`.

**Acceptance criteria:**
- [ ] Detector fires on a canned empty-SSE transcript / test server sequence
- [ ] Does NOT fire on a successful single-image / no-image ask (zero false positives)
- [ ] Worker log entry on Bug A failure does not contain string `「執行成功」`

**Verification:**
- [ ] Test: feed a canned empty-SSE sequence into the detector / SSE consumer → returns the specific reason string
- [ ] Test: replay simple "say OK" prompt → returns success
- [ ] Existing failure path surfaces the expected Slack-visible copy; if not, stop and review before touching `app/`

**Dependencies:** 3.2-6, 3.2-7.

**Files likely touched:**
- `worker/agent/opencode_server.go` (detection logic)
- `worker/agent/opencode_server_test.go`

**Estimated scope:** S

---

#### Task 3.2-12: Multi-worker port collision safety

**Description:** Confirm `opencode serve --port=0` behavior: kernel assigns an unused port, supervisor reads it from server stderr / `/health` response. Two workers on the same host should never collide.

**Acceptance criteria:**
- [ ] Two supervisors started concurrently in the same process get distinct, working ports
- [ ] No retry loop / collision-recovery code needed (kernel handles allocation)

**Verification:**
- [ ] Test: spin up 4 supervisors concurrently in one test process, all `BaseURL()` distinct, all `/health` respond green

**Dependencies:** 3.2-5.

**Files likely touched:**
- `worker/agent/server_supervisor_test.go` (concurrency test, no source changes likely)

**Estimated scope:** XS

---

### Checkpoint E — Stage 3 ready to merge

- [ ] F1, F2, F3, F4, F5, F6, F7 all covered
- [ ] Test suite includes lazy lifecycle, crash recovery, Bug A detection, multi-worker port
- [ ] Stage 3 PR opened, reviewed, merged

### Stage 4 PR — Validation + docs

#### Task 3.2-13: Performance verification

**Description:** Measure spawn-mode vs server-mode for: (a) first-job latency (cold worker, server still booting), (b) steady-state per-job median latency, (c) idle worker RSS, (d) busy worker RSS. Compare against N1, N2, N3 budgets in spec.

**Acceptance criteria:**
- [ ] N1: server-mode steady-state median ≤ spawn median + 200ms
- [ ] N2: server-mode first-job latency ≤ spawn first-job + 5s
- [ ] N3: server-mode idle RSS ≤ spawn idle + 200MB
- [ ] Numbers committed to `docs/specs/opencode-server-mode-perf-baseline.md`

**Verification:**
- [ ] Run benchmark suite on author's Mac, 100 jobs in each mode, measure
- [ ] Numbers reproducible — run twice, ≤ 10% variance

**Dependencies:** 3.2-8..12.

**Files likely touched:**
- `worker/agent/perf_benchmark_test.go` (or standalone benchmark binary)
- `docs/specs/opencode-server-mode-perf-baseline.md` (new)

**Estimated scope:** M

---

#### Task 3.2-14: Documentation

**Description:** Update worker init template (`cmd/agentdock/init.go`) so a freshly-generated `worker.yaml` includes a commented-out `opencode:` block with default values and inline comments explaining each field. Update `docs/configuration-worker.md` and `docs/configuration-worker.en.md` with a new section on `opencode:` block. Add `CHANGELOG.md` entry.

**Acceptance criteria:**
- [ ] `agentdock init worker` produces a `worker.yaml` with the `opencode:` block commented out, defaults documented inline
- [ ] `docs/configuration-worker.md` describes `opencode.mode`, `opencode.idle_timeout`, `opencode.storage_dir`
- [ ] `docs/configuration-worker.en.md` mirrors the Chinese version
- [ ] `CHANGELOG.md` mentions the new opt-in `opencode.mode: server`

**Verification:**
- [ ] Run `agentdock init worker --output /tmp/test-worker.yaml`; inspect output
- [ ] `cat /tmp/test-worker.yaml | grep -A 5 'opencode:'` shows expected block

**Dependencies:** 3.2-1.

**Files likely touched:**
- `cmd/agentdock/init.go` (worker init template)
- `docs/configuration-worker.md`
- `docs/configuration-worker.en.md`
- `CHANGELOG.md`

**Estimated scope:** S

---

### Checkpoint F — Stage 4 ready to merge

- [ ] All F + N + C1 in spec satisfied
- [ ] POC report (`docs/specs/opencode-server-mode-poc-report.md`) and perf baseline docs in main
- [ ] Init template + configuration docs synchronized
- [ ] Stage 4 PR opened, reviewed, merged
- [ ] Spec C2 observation period clock starts: ≥ 2 weeks of `mode: server` running in prod with zero answer-drop incidents before flipping default

---

## Risks and mitigations

| Risk | Impact | Mitigation |
|---|---|---|
| HEAD vs 1.14.29 schema drift between POC and Phase 3.2 ship | High | POC 3.1-8 (P8) is a hard ship gate; CI pins opencode version; spec change-management requires re-running P8 on opencode upgrade |
| Lazy lifecycle boot lock race spawns multiple servers under thunder | High | POC 3.1-9 (P9) explicitly tests 8-goroutine concurrent first-job; production task 3.2-8 reuses `sync.Once` + state machine pattern |
| SSE consumer mishandles `data:` prefix or heartbeat events | Medium | POC 3.1-3..4 100x replay against real SSE flow; reuse `shared/queue/stream.go` parser after thin adapter; unit test heartbeat-only sequences |
| Bug A detector false positives (legitimate `finish=other` cases) | Medium | POC P7 negative-corpus check (P2 successful runs); detector uses AND of three conditions (`finish=other` AND `tokens.output=0` AND no text part), stricter than any single signal |
| Worker laptop deployment: server collides with user's own opencode | High | `XDG_DATA_HOME` isolation enforced by Boundaries Never; POC P10 verifies user DB byte-identical; release note documents the isolated path |
| `import_direction_test.go` fails because server-mode reaches into shared/ | Medium | server-mode code stays in `worker/agent/`; only public API of `shared/queue/stream.go` is consumed; pre-merge run of import-direction test in each PR |
| Spawn extraction (3.2-2) accidentally changes behavior | Medium | Extract-only PR; existing `runner_test.go` is the regression net; reviewer asked to verify diff is move-only |
| Stage 3 PR pulls an `app/` change for Bug A copy mapping | Low-Medium | Flagged in 3.2-11 task notes; if needed, surface explicitly to reviewer; keep change minimal (one switch case in result_listener) |
| Newer opencode silently breaks server-mode (e.g. P3 SSE regression on 1.14.48) | High | Version check (3.2-4) only enforces a lower bound, so newer-but-broken versions are NOT caught at boot; defense is per-job failure detection (Bug A detector + `server_mode_regression` classification + no auto-fallback per spec C4) — failure surfaces loudly, operator investigates, floor bumps only after fresh POC pass |

## Open questions (deferred to implementation)

- **OQ-1**: Where exactly does the app-side result_listener live, and does it already render failure-reason text to Slack? (Discover during 3.2-11; may pull small app change into Stage 3 PR.)
- **OQ-2**: Does opencode HEAD's `/event` SSE reliably emit `session.status idle`? POC P2/P3 will surface this; if not, completion signal must use `message.part.updated` + `time.end` correlation (production task 3.2-6 SSE consumer needs to handle either).
- **OQ-3**: What's the right `opencode.idle_timeout` default for production? POC uses 30s for fast iteration; spec suggests 5m. Validate in benchmark task 3.2-13.
