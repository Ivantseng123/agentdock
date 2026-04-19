# worker/

[繁體中文](README.md)

The agent executor side of AgentDock. Holds the code for `agentdock worker`, published as its own Go module (`github.com/Ivantseng123/agentdock/worker`).

## Responsibilities

- Pull jobs from the queue
- Clone / cache repos, mount skill files into the agent's view
- Invoke the agent CLI (claude / codex / opencode / gemini) with the prompt
- Track PID + heartbeat, publish status
- Accept kill commands via the command bus
- Decrypt secrets that app shipped and inject them as env vars on the child

Worker **does not talk to Slack or GitHub Issues directly**; that's app's job.

## Prerequisites

The host needs at least one agent CLI installed:

- `claude` ([Claude CLI](https://github.com/anthropics/claude-code)) — stream-json support
- `codex` — non-streaming
- `opencode` ([OpenCode](https://github.com/opencode-ai/opencode))
- Anything else that accepts a `{prompt}` placeholder

If the agent binary isn't on PATH, preflight fails immediately.

## Configuration

Full schema in [docs/configuration-worker.en.md](../docs/configuration-worker.en.md).

Scaffold:

```bash
agentdock init worker -i -c ~/.config/agentdock/worker.yaml
```

> `secret_key` must match the app's. Beacon verification failure blocks startup.

Run:

```bash
agentdock worker -c ~/.config/agentdock/worker.yaml
```

## Mode

`agentdock worker` only makes sense in Redis mode — in inmem mode the worker pool is started directly from cmd/agentdock, not through `worker.Run`.

## Tests

```bash
(cd worker && go test ./... -race)
```

## Dependencies

- `shared/` — queue, github, logging, crypto, prompt, connectivity
- Does **not** depend on `app/` (worker ✗ app boundary is enforced)

## See also

- [Top-level README](../README.en.md)
- [app/README.en.md](../app/README.en.md)
- [docs/configuration.en.md](../docs/configuration.en.md)
- [docs/MIGRATION-v2.en.md](../docs/MIGRATION-v2.en.md)
