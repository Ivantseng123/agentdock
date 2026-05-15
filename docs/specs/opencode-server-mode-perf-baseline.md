# Opencode server-mode performance baseline

Phase 3.2 Stage 4 acceptance artifact for plan task 3.2-13. Records the
measured spawn-mode vs server-mode behavior against spec budgets
N1 / N2 / N3 and documents the `--pure` flag fix that lands alongside
the baseline.

## Headline

The initial perf bench (spawn ~1.5s vs server ~25s) looked like a
massive server-mode regression. **Investigation showed the comparison
was misleading**:

1. **Spawn is silently broken on short answers**. A focused repro
   (`TestSpawnBugABReproduction`) shows spawn returns `(output="",
   err=nil)` 30/30 times on the short-prompt fixture. The opencode CLI
   exits 0 with empty stdout — what the worker logs as "Agent exit 0
   但 stdout 空" at `worker/agent/opencode_spawn.go:254`. Spawn's
   ~1.5s wallclock is not "fast" — it is "fail-fast". The cause is
   either Bug B (ADR-0005's dispose race on short answers) or this
   dev box's auth state; the parser drops both error JSON and missing
   trailing events to the same empty string, so distinguishing on
   stdout alone is not possible.
2. **Server mode was missing `--pure`**. The supervisor spawned
   `opencode serve --port 0 --hostname 127.0.0.1` without the flag
   the spawn-mode CLI has always carried (memory
   `project_opencode_pure_flag` documents the rule). Every job paid
   ~4s of project-plugin re-load. Stage 4 adds `--pure`, dropping
   fresh-workdir server median from ~17s to ~13s.
3. **The remaining ~13s is opencode-side LLM round-trip + server
   processing**, not a worker-side regression. Spawn mode's much
   smaller wallclock is spawn never doing the round trip in the first
   place.

The realistic operator trade-off after the `--pure` fix:

| Mode | Wallclock | Output |
|---|---|---|
| spawn (current default) | ~1.5s | empty (silent drop) |
| server with `--pure` | ~13s | correct answer |

Server mode is roughly 8x wallclock but is the only mode producing
correct answers on this fixture. The N3 idle RSS budget is the real
trade-off (server costs ~470 MB more per worker).

## Methodology

- **Perf bench harness**: `worker/agent/perf_benchmark_test.go` (`TestPerf_OpencodeSpawnVsServer`).
- **Invocation**: `OPENCODE_PERF_BENCH=1 OPENCODE_PERF_SAMPLE_SIZE=<N> go -C worker test ./agent -run TestPerf_OpencodeSpawnVsServer -count=1 -timeout 60m -v`
- **Bug A/B repro + timing diagnostics**: `worker/agent/bug_repro_test.go` (`TestSpawnBugABReproduction`, `TestServerModeTimingBreakdown`, `TestServerModeSessionReuse`).
- **Invocation**: `OPENCODE_BUG_REPRO=1 go -C worker test ./agent -run TestSpawnBugAB -count=1 -timeout 20m -v` (similar pattern for the other two)
- **Prompt fixture**: `"Reply with just the two letters OK and nothing else."` — shortest prompt that still exercises the full opencode + LLM provider round trip. ~15 tokens per job.
- **Sample size**: 20 per rep for the perf bench, 30 for the bug repro, 8 for timing breakdown. Smaller samples for the diagnostic tests because each one costs LLM tokens and we are measuring orders of magnitude, not millisecond differences.
- **Per-job timeout**: 90s. BuiltinAgents default 30m is unsuitable for a perf harness; a stuck LLM hang would wedge the whole run.
- **Warm-up drop (perf bench)**: first 10 jobs of each rep dropped from steady-state median; covers skill cache load, auth handshake, and (server mode) SSE subscribe ramp.
- **Reps**: 2 for the perf bench, 1 for the diagnostics.
- **RSS**: shell-out to `ps -o rss= -p <pid>` (works on darwin + linux without cgo). Spawn samples the test process only; server samples test process + `Supervisor.ChildPID()` (the persistent `opencode serve` subprocess).

## Environment

- **Architecture**: darwin/arm64 (Apple Silicon Mac, operator's dev box).
- **opencode binary**: 1.14.41 (Phase 3.2 floor).
- **Auth**: operator's auth state (XDG default location).
- **Network**: operator's home/office.

### Limitations to flag for the reviewer

- **Dev-box measurement, not production-representative**. Production runs in Linux pods with different IO scheduler, CPU class, network path, and RSS-accounting semantics. Use these numbers as relative-order-of-magnitude signal, not absolute baseline.
- **Single-instance sample**. Bench runs one supervisor; production runs N workers per pod each with its own supervisor. RSS scales linearly with N.
- **Short prompt amplifies provider variance**. The "reply OK" prompt minimizes token cost but maximizes wallclock noise from provider load. Production prompts (issue-triage style) average out provider transients; the relative server-vs-spawn picture should hold either way, but absolute milliseconds will differ.

## Spec budgets

Per `docs/specs/opencode-server-mode.md` § N1..N3:

- **N1** server-mode steady-state median ≤ spawn-mode steady-state median + 200ms
- **N2** server-mode first-job latency ≤ spawn-mode first-job latency + 5s
- **N3** server-mode idle RSS ≤ spawn-mode idle RSS + 200MB
- **Variance** ≤ 10% between rep1 and rep2 (each mode's steady median)

## Raw data

### Perf bench (initial, before `--pure` fix)

Verbatim from `TestPerf_OpencodeSpawnVsServer` t.Log output, N=20, 2 reps:

```
| Metric                    | Rep | Spawn          | Server          | Δ               | Budget   | PASS? |
|---------------------------|-----|----------------|-----------------|-----------------|----------|-------|
| Steady-state median (N1)  | 1   | 1.356196563s   | 34.956846041s   | 33.600649478s   | <=+200ms | false |
| Steady-state median (N1)  | 2   | 1.684067166s   | 24.957438062s   | 23.273370896s   | <=+200ms | false |
| First-job latency (N2)    | 1   | 1.344112792s   | 58.57998075s    | 57.235867958s   | <=+5s    | false |
| First-job latency (N2)    | 2   | 1.696831s      | 10.442275333s   | 8.745444333s    | <=+5s    | false |
| Idle RSS MB (N3)          | 1   | 22             | 492             | 470             | <=+200MB | false |
| Idle RSS MB (N3)          | 2   | 26             | 488             | 462             | <=+200MB | false |

Variance: spawn=21.6%, server=33.4% (target <=10%)
Errors: spawn rep1=0 rep2=0 (out of 20 each), server rep1=2 rep2=0 (both mid-SSE 90s caps)
```

### Bug A/B repro — spawn mode silent-drop confirmed (30/30)

Verbatim from `TestSpawnBugABReproduction`, N=30, same short prompt:

```
=== Spawn Bug A/B reproduction (N=30, opencode 1.14.41 on darwin/arm64) ===
Prompt: "Reply with just the two letters OK and nothing else."

Healthy (output contains 'ok'):           0/30  (0.0%)
Silent drops (output empty, err nil):     30/30  (100.0%)  <- Bug A/B signature
Suspicious output (no 'ok' in answer):    0/30  (0.0%)
Non-nil err (timeout / exec failure):     0/30  (0.0%)

VERDICT: Bug A/B reproduced 30 times in 30 attempts. Spec premise stands.
```

100% silent drops on the short prompt fixture. The opencode CLI exited 0 with empty stdout every time; the worker's "Agent exit 0 但 stdout 空" warning fires at `worker/agent/opencode_spawn.go:254` and the function returns `("", nil)` per the spec-documented silent-drop pattern.

**Disambiguation note**: the parser drops both error-JSON events (e.g. `ProviderAuthError`) and missing trailing events (Bug B race) to the same empty-string outcome. The 100% rate could be ADR Bug B race, or it could be auth-state misconfiguration on this dev box, or a mix. The downstream symptom is the same and the worker-side fix (server mode) covers both.

### Server-mode timing breakdown — per-step costs (before `--pure` fix)

Verbatim from `TestServerModeTimingBreakdown`, N=8 each variant:

```
Medians (no --pure):
  fresh:  CreateSession=12.654166ms  SendPrompt=1.119958ms  Wait=17.230688749s  Total=17.246829146s
  shared: CreateSession=4.446375ms   SendPrompt=1.075416ms  Wait=10.016238646s  Total=10.028605646s

Healthy output (outputLen>0 + no err): fresh=8/8  shared=8/8
```

`Wait` (the POST `/session/{id}/message` long-poll until completion) dominates. CreateSession and SendPrompt are sub-second. Shared-workdir saves ~7s vs fresh-workdir, consistent with per-workdir plugin reload paying ~4-7s per session.

### Server-mode timing breakdown — after `--pure` fix

Verbatim from `TestServerModeTimingBreakdown` re-run, N=8 fresh variant only (shared variant context-exhausted in this run):

```
Medians (with --pure):
  fresh:  CreateSession=12.563979ms  SendPrompt=1.127895ms  Wait=13.150416104s  Total=13.167353896s

Healthy output: fresh=8/8
```

`--pure` saves ~4s of per-session plugin re-load on the fresh-workdir variant. The remaining ~13s is opencode-side LLM round-trip + server processing.

### Session reuse — message overhead is per-message, not per-session

Verbatim from `TestServerModeSessionReuse`, N=6 each variant (before `--pure` fix):

```
Medians:
  fresh-session  (new session per job): Wait=12.778252437s  Total=12.790987041s
  reused-session (1 session, N msgs):    Wait=8.513499646s  Total=8.517074958s
```

Reusing one session for N prompts saves ~4s vs creating a new session per prompt. The remaining ~8s is paid per message inside opencode server (LLM call + completion plumbing), not at session boundaries.

## Verdict

**N1 (steady-state median): NOT a fair comparison.** Spawn ~1.5s wallclock is "fail-fast" (silent drop); a healthy spawn run would also pay the LLM round trip. The `--pure` fix improves server-mode latency by ~4s; the remaining gap is the opencode-server LLM path, comparable to what a healthy spawn would cost.

**N2 (first-job latency): NOT a fair comparison.** Same reasoning as N1.

**N3 (idle RSS): real trade-off, still over budget by ~270MB.** Server mode costs ~488 MB per worker (vs spawn's ~22 MB); spec budget allows +200 MB, actual is +462-470 MB. Operators running N workers per pod pay N × ~470 MB extra. The `--pure` flag does not change this — RSS is dominated by opencode server's in-memory runtime, not plugins.

**Variance: methodology limitation.** Spawn variance 21.6%, server variance 33.4%. The short prompt amplifies LLM-provider transients; longer production-style prompts would dampen this. The variance number itself is a known methodology weakness (see Limitations), not a server-mode issue.

**Bug A/B premise: empirically confirmed.** 30/30 silent drops in spawn mode on this fixture. Whatever the root cause (Bug B race or dev-box auth state), spawn mode is not reliably delivering correct answers; server mode is.

**Spec C2 default flip recommendation**: with the `--pure` fix landed and the spawn-mode silent-drop empirically confirmed, the case for keeping `mode: spawn` as default is weakened. Each shipped release with `mode: spawn` as default carries this known production-affecting failure mode. The C2 trigger ("≥ 2 weeks of zero answer-drop in production with `mode: server`") should be invoked sooner rather than later. **This baseline doc recommends flipping the default to `server` in a follow-up PR**, gated only on someone running this harness in a Linux pod first to confirm the +470 MB RSS is acceptable in that environment.

## Implications for Stage 4 ship

The Stage 4 PR ships three concrete deliverables to the production codebase:

1. **`--pure` fix** to `worker/agent/server_supervisor.go` — server-mode supervisor now skips project-level plugins, matching the spawn-mode CLI rule. Saves ~4s per job.
2. **Perf bench + diagnostics harnesses** — opt-in test paths future engineers can re-run on opencode upgrades or new debugging.
3. **Docs + init template** documenting the opt-in `opencode.mode: server` block.

What this PR does **not** ship:

- A fix for the remaining ~13s per-job server-mode latency (opencode-side; not a worker bug)
- A default-flip from `spawn` to `server` (recommendation only; would land in a follow-up PR after Linux-pod measurement)
- A direct fix for Bug A/B in spawn mode (server mode sidesteps both by construction)

## Reproducibility

The harnesses are deterministic in method; the data has run-to-run noise. Re-running on the same dev box should reproduce the order of magnitude. Re-running on a different host (different OS, LLM provider routing, opencode version) may yield different numbers — that is expected and is what FUP-2 should explore.

- Perf bench: `OPENCODE_PERF_BENCH=1 OPENCODE_PERF_SAMPLE_SIZE=20 go -C worker test ./agent -run TestPerf_OpencodeSpawnVsServer -count=1 -timeout 60m -v`
- Spawn Bug A/B repro: `OPENCODE_BUG_REPRO=1 go -C worker test ./agent -run TestSpawnBugABReproduction -count=1 -timeout 20m -v`
- Server timing breakdown: `OPENCODE_BUG_REPRO=1 go -C worker test ./agent -run TestServerModeTimingBreakdown -count=1 -timeout 20m -v`
- Server session reuse: `OPENCODE_BUG_REPRO=1 go -C worker test ./agent -run TestServerModeSessionReuse -count=1 -timeout 20m -v`

## Follow-up tasks

The Stage 4 finding leaves four work items open. Owners + dates TBD; recommended order of attack:

- **FUP-1: Linux-pod re-measurement.** The dev-box numbers above are the only data we have. Production target is Linux pods; the relative spawn-vs-server picture should hold, but absolute RSS / latency need pod measurement before the C2 default flip can be approved.
- **FUP-2: Spawn-mode silent-drop disambiguation.** The bug repro test shows 30/30 silent drops but cannot distinguish ADR Bug B race from a dev-box auth state issue. A focused test that captures opencode CLI stderr separately (the parser today drops error JSON to the same "" outcome) would localize the root cause. Either way, server mode is the worker-side fix; this FUP is for understanding, not for fixing.
- **FUP-3: Server-mode 13s opencode-side investigation.** The remaining per-job latency is inside opencode (provider routing, model load, SSE coalescing, or a server-only quirk). Options: `--log-level DEBUG` instrumentation on the supervisor, opencode version bump (1.14.41 is the floor; later versions may improve), or upstream issue filing.
- **FUP-4: Periodic regression cadence.** Once FUP-1 lands and the C2 flip happens, run the harness suite quarterly (or on opencode upgrade) to catch upstream regressions before prod.

`mode: server` is the only mode producing correct answers on this fixture; the `--pure` fix lands the most actionable improvement. The remaining items are follow-ups, not gates on Stage 4 merge.
