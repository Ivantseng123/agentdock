# Use long-running `opencode serve` + HTTP/SSE for ask path

## Context

Production debugging of a Slack ask that returned no answer (job `20260508-090827-13bf08a3`) traced two deterministic bugs in opencode's `run --format json` path, both unfixed in HEAD:

- **Bug A** — `packages/opencode/src/provider/sdk/copilot/chat/openai-compatible-chat-language-model.ts:341-347` initializes `finishReason.unified = "other"` and only overwrites it on parse error or when an upstream chunk carries `choice.finish_reason`. An empty SSE response leaves the default in place; combined with all-`undefined` `usage`, opencode reports the call as completed with 0 tokens. Worker's `agent/runner.go` then logs `Agent 執行成功 output_len=0` — silent answer drop.
- **Bug B** — `packages/opencode/src/cli/cmd/run.ts:733-757` runs `loop(client, events)` as a fire-and-forget promise; `await client.session.prompt(...)` resolves on the POST response (which fires at `message.time.completed`), and `bootstrap.ts`'s finally calls `InstanceRuntime.disposeInstance(...)` immediately, tearing down the in-process Bus and SSE before trailing `message.part.updated` events with `time.end` reach the loop's `for await`. Captured timing on the failed session shows the final bus publish at `+131ms` after `message.completed` — race lost on short answers.

CLI flags do not fix this — the bugs are structural in opencode's run-mode lifecycle. Multica's opencode integration (`server/pkg/agent/opencode.go`) uses the same spawn-per-job pattern and has the same exposure; they have not yet hit the failure mode. Idea-refine session evaluated six options (V1 SQLite lookback, V2 export fallback, V3 long-running serve, V4 vendored fork, V5 swap to claude, V6 validator-only); V3 was selected because it cuts the dependency on CLI exit semantics entirely.

## Decision

Worker switches the opencode integration to a long-running `opencode serve` subprocess accessed via HTTP `POST /session/{id}/message` and SSE `GET /event`. The implementation follows these constraints:

- **Mapping** — exactly one `opencode serve` subprocess per worker process; the pool's N goroutines share it (server is designed for multi-session concurrency via per-request `x-opencode-directory` header, which scopes Instance + skill loading per directory).
- **Lifecycle** — lazy: subprocess spawns on the first job, stops after `opencode.idle_timeout` of pool inactivity, and is gracefully torn down on worker SIGTERM (active sessions get `POST /session/{id}/abort` first, then process SIGTERM with SIGKILL fallback).
- **Storage isolation** — subprocess receives `XDG_DATA_HOME=$worker_owned_dir` so its SQLite session DB never collides with a user's own `~/.local/share/opencode/opencode.db` (worker may run on a user's laptop). `XDG_CONFIG_HOME` and `XDG_CACHE_HOME` are inherited unchanged so user auth tokens carry over.
- **Rollout** — worker.yaml gains a top-level `opencode:` block with `mode: spawn | server`. First release defaults to `spawn` (legacy preserved). After ≥ 2 weeks of production observation with zero answer-drop incidents, default flips to `server`; spawn path stays in code for ≥ 2 additional weeks before any deletion discussion.
- **No auto-fallback** — when `mode: server` fails, the worker fails the job loudly. Falling back to spawn-mode mid-job would mask whichever bug is firing and double user-visible latency without a real recovery.

## Consequences

- Bug A and Bug B are eliminated by construction: there is no per-job CLI exit, so no dispose race; the worker observes the full `finishReason` / `tokens` event stream and treats `finish=other` + `tokens.output=0` as failure rather than success.
- Blast radius for opencode crashes shifts from per-job (spawn-mode process isolation) to N in-flight jobs (single shared server). Accepted because steady-state crash rate is lower than per-job spawn's accumulated startup risk, and `cmd.Wait()`-based supervision restarts the server with one retry per affected job.
- Laptop deployments tolerate the change: lazy lifecycle keeps idle RSS at zero, and `XDG_DATA_HOME` isolation means the worker server and the user's personal `opencode run` no longer contend on a single SQLite WAL.
- Skill mounting is unchanged — `worker/pool/executor.go mountSkills()` continues to write per-job skills into each temp dir, and opencode's per-`directory` Instance scoping loads them as before. No new skill-distribution mechanism.
- Reversibility is at config-level (`opencode.mode: spawn`), code-level (spawn path retained), and rollout-level (≥ 2 weeks of co-existence before any code deletion). The cost of changing our mind later is bounded.
- Considered and rejected: V1 SQLite session DB lookback (schema-fragile, breaks on opencode storage migration), V2 `opencode export` fallback (two spawns per job, still depends on CLI exit semantics), V4 vendored opencode fork (useless on user laptops where worker doesn't control which opencode binary is on PATH), V5 swap ask path to claude/codex/gemini (loses opencode `--pure`'s plugin-isolation guarantee), V6 validator-only (turns silent failure into loud failure but recovers no answers).
