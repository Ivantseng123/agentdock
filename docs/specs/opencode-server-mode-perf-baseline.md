# Opencode server-mode performance baseline

Phase 3.2 Stage 4 acceptance artifact for plan task 3.2-13. Records the
measured spawn-mode vs server-mode latencies and idle RSS against spec
budgets N1 / N2 / N3.

## Methodology

- **Harness**: `worker/agent/perf_benchmark_test.go` (`TestPerf_OpencodeSpawnVsServer`).
- **Invocation**: `OPENCODE_PERF_BENCH=1 OPENCODE_PERF_SAMPLE_SIZE=<N> go -C worker test ./agent -run TestPerf_OpencodeSpawnVsServer -count=1 -timeout 60m -v`
- **Prompt fixture**: `"Reply with just the two letters OK and nothing else."` — shortest prompt that still exercises the full opencode CLI + LLM provider round trip. Approx. 15 tokens per job.
- **Sample size (N)**: 20 per rep, per mode (cost-bounded). Spec acceptance nominal is 100; the dev-box smoke run uses N=20 because LLM tokens are the operator's expense AND a higher N exposes more opencode upstream hangs (see below). Variance ≤ 10% (target) gates whether N=20 was sufficient or a re-run with N=100 is warranted.
- **Per-job timeout**: 90s. The BuiltinAgents default (30m) is unreasonable for a perf harness — a single LLM hang would wedge the whole run. 90s is well above the typical 1–5s healthy job and short enough that a tail-end hang fails one job without burning a quarter of the bench.
- **Warm-up drop**: first 10 jobs of each rep are dropped from the steady-state median; covers skill cache load, auth handshake, and (server mode) SSE subscribe ramp.
- **Reps**: 2 independent invocations of each mode. Variance = `|rep1_median - rep2_median| / mean_median`.
- **RSS measurement**: shell-out to `ps -o rss= -p <pid>`. Spawn mode samples the test process only (no persistent child). Server mode sums the test process + `Supervisor.ChildPID()` (the long-running `opencode serve` subprocess).
- **Idle RSS approximation**: sampled at the end of each rep's loop while the `opencode serve` child is still alive (before `Drain` kills it). Strict idle would require triggering the 5m `IdleTimeout`, which isn't worth the wall clock — server mode's busy/idle delta is small because the child is persistent.

## Environment

- **Architecture**: darwin/arm64 (Apple Silicon Mac, operator's dev box).
- **opencode binary**: 1.14.41 (Phase 3.2 floor, `~/.opencode/bin/opencode`).
- **Auth**: operator's `~/.opencode/auth.json` (provider-of-record).
- **Network**: operator's home/office network.

### Limitations to flag for the reviewer

- **dev-box measurement, not production-representative**. Production runs in Linux pods with different IO scheduler, CPU class, network path, and RSS-accounting semantics. Use these numbers as relative comparison (spawn vs. server) only — absolute latency or RSS in pod will differ.
- **Single-instance sample**. Bench runs one supervisor; production runs N workers per pod each with its own supervisor. RSS scales linearly with N; the N3 budget (≤ +200MB per worker) is what this test gates.
- **Short prompt bias**. The "reply OK" prompt minimizes LLM token cost but also minimizes LLM service-side latency. Production prompts (issue-triage style) have much larger LLM round trips, so spawn vs. server delta as a *percentage* of wall time will look smaller in prod.

## Spec budgets

Per `docs/specs/opencode-server-mode.md` § N1..N3:

- **N1** server-mode steady-state median ≤ spawn-mode steady-state median + 200ms
- **N2** server-mode first-job latency ≤ spawn-mode first-job latency + 5s
- **N3** server-mode idle RSS ≤ spawn-mode idle RSS + 200MB
- **Variance** ≤ 10% between rep1 and rep2 (each mode's steady median)

## Raw data

Verbatim `t.Log` output from the harness run on 2026-05-14. Sample size
N=20, two reps, warm-up drop 10, per-job timeout 90s. Test exit was FAIL
because the harness asserts the spec budgets; see § Verdict for analysis.

```
=== Perf baseline raw output (arm64/darwin) ===
Sample size: N=20 per rep, 2 reps, warm-up drop=10, prompt="Reply with just the two letters OK and nothing else."

| Metric                    | Rep | Spawn          | Server          | Δ               | Budget   | PASS? |
|---------------------------|-----|----------------|-----------------|-----------------|----------|-------|
| Steady-state median (N1)  | 1   | 1.356196563s   | 34.956846041s   | 33.600649478s   | <=+200ms | false |
| Steady-state median (N1)  | 2   | 1.684067166s   | 24.957438062s   | 23.273370896s   | <=+200ms | false |
| First-job latency (N2)    | 1   | 1.344112792s   | 58.57998075s    | 57.235867958s   | <=+5s    | false |
| First-job latency (N2)    | 2   | 1.696831s      | 10.442275333s   | 8.745444333s    | <=+5s    | false |
| Idle RSS MB (N3)          | 1   | 22             | 492             | 470             | <=+200MB | false |
| Idle RSS MB (N3)          | 2   | 26             | 488             | 462             | <=+200MB | false |

Variance (|rep1-rep2|/mean):
- Spawn steady median: 21.6% (target <=10%)
- Server steady median: 33.4% (target <=10%)

Errors:
- Spawn rep1=0 rep2=0 (out of N=20 each)
- Server rep1=2 rep2=0 (out of N=20 each)  # both rep1 errors hit the 90s per-job cap mid-SSE
```

## Verdict

**N1 (steady-state median): FAIL both reps.** Server-mode median sits in the 25–35s range; spawn-mode is 1.4–1.7s. Budget allows +200ms. Δ is 23–34 seconds — two orders of magnitude over budget.

**N2 (first-job latency): FAIL both reps.** Server-mode first job is 10s in the best case and 58s in the worst; spawn-mode is 1.3–1.7s. Budget allows +5s. The 10s rep2 result is alone over budget; the 58s rep1 result is 12x the budget. Variance between reps is wide — single-shot first-job latency is not stable.

**N3 (idle RSS): FAIL both reps.** Server-mode idle RSS is 488–492 MB; spawn-mode is 22–26 MB. Budget allows +200 MB. Δ is 462–470 MB — over double the budget. The bulk of the gap is the live `opencode serve` subprocess; spawn-mode has no persistent child.

**Variance: FAIL both modes.** Spawn variance 21.6%, server variance 33.4% (target ≤ 10%). Even spawn mode — the simpler path — exceeds the variance budget under dev-box conditions. This is a measurement-environment warning, not exclusively a server-mode issue: short-prompt LLM round trips dominate the wall clock, and that latency varies job-to-job with provider load.

**Per-rep mid-SSE timeouts (server only).** Two of 20 rep1 server jobs hit the 90s per-job cap waiting on SSE. The `subscribeEvents` goroutine was blocked on the chunked-body read with no progress, suggesting an opencode serve completion-event delivery quirk. This is the same shape that drove the first un-capped bench run to wedge for 30 minutes (see commit log).

## Implications for Stage 4 ship

The bench reveals **server mode is not production-viable as measured on the operator's darwin/arm64 dev box with opencode 1.14.41**. Concretely:

1. **Keep `mode: spawn` as the default.** Spec C2 already gates a default flip on ≥2 weeks of zero answer-drop in production; this baseline doc says clearly the flip should NOT happen until N1/N2/N3 are recoverable.
2. **`mode: server` stays opt-in** — operators can still toggle it on for experimentation, but `docs/configuration-worker.md` § Opencode 區塊 already warns that the feature is in C2 observation, not production-recommended.
3. **The Stage 4 perf finding is the deliverable.** Plan task 3.2-13 acceptance reads "Measure ... Compare against N1/N2/N3 budgets" — the harness measures and compares; the comparison verdict (FAIL) is the data the project needed before flipping the default.

## Hypotheses for the regression (deferred to follow-up)

The bench harness itself looks correct: spawn-mode numbers are tight and match expected opencode CLI cold-start cost, and the server-mode wiring is the exact `Runner.runOneServer` path production uses. Speculation on root cause:

- **Per-request skill load on each `x-opencode-directory`.** Every job hands the supervisor a fresh `t.TempDir()` workdir; if the long-running `opencode serve` re-loads `.opencode/skills` each time it sees a new directory, the amortization-across-jobs that server mode was supposed to deliver vanishes. The POC (P2/P3) may have reused a single workdir across attempts and missed this.
- **SSE completion-event coalescing.** The server may emit `session.status idle` after a delay or in a buffered chunk under load; the bench's `PromptRun.Wait` blocks on that signal. Two of 20 jobs in rep1 never received the signal at all within 90s.
- **opencode 1.14.41 vs. newer.** POC report § Historical bisect documents SSE regressions in 1.14.42–48. A different opencode release may behave better; the version-check floor (3.2-4) only enforces a lower bound.

Investigation of these is **out of scope for Stage 4**. Stage 4 ships the harness + finding so the next iteration has data to drive the fix.

## Reproducibility note

The harness is deterministic in its method; the data has run-to-run noise. Re-running on the same dev box should produce similar order-of-magnitude numbers. Re-running on a different host (different OS, different LLM-provider routing, different opencode version) may yield very different numbers — that's expected and is what the next phase of investigation should explore. The reviewer should NOT treat the specific milliseconds in the table above as ground truth; treat the **relative order of magnitude** between spawn and server as the signal.

## Reproducibility

- The harness is checked into `worker/agent/perf_benchmark_test.go`. Reviewer can re-run on a different host (or with `OPENCODE_PERF_SAMPLE_SIZE=100`) to validate.
- The reviewer reproducibility checkpoint is: same prompt, same opencode binary version (1.14.41+), same auth provider; **NOT** same network, OS, or hardware — those are noted as inherent variance.
- If a future opencode release silently regresses server mode (precedent: 1.14.42–1.14.47 SSE-close regression, see `docs/specs/opencode-server-mode-poc-report.md § Historical bisect`), re-running this harness post-upgrade is the gate.

## Future work (not blocking Stage 4 merge)

- Run `OPENCODE_PERF_SAMPLE_SIZE=100` once the spec C2 observation period starts, to validate at the spec's nominal N.
- Repeat measurements in a Linux pod (gha-runner or staging cluster) to surface OS-class deltas. Add as a follow-up task; this baseline doc lands the dev-box numbers as the Phase 3.2 ship gate.
- Establish a periodic perf regression check (e.g. quarterly) to catch upstream opencode regressions before they hit production. Cadence to be decided post-C2.
