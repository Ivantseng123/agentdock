# app/

[繁體中文](README.md)

The Slack side of AgentDock. Holds the code for `agentdock app`, published as its own Go module (`github.com/Ivantseng123/agentdock/app`).

## Responsibilities

- Accept Slack events (`@bot` mentions inside a thread)
- Repo / branch picker + description prompt UX
- Read thread context, build the prompt, submit to the job queue
- Receive job result → create GitHub issue → post URL back to the Slack thread
- Secret management (AES-256 encrypt before handing off to worker)
- HTTP endpoints: `/healthz`, `/jobs`, `/metrics`
- Watchdog for stuck jobs

App **does not run agent CLIs**; that is worker's job. In inmem mode, cmd/agentdock starts a local worker pool against app's buses.

## Configuration

Full schema in [docs/configuration-app.en.md](../docs/configuration-app.en.md).

Scaffold:

```bash
agentdock init app -i -c ~/.config/agentdock/app.yaml
```

Run:

```bash
agentdock app -c ~/.config/agentdock/app.yaml
```

## Mode switching

`queue.transport` decides the runtime:

- `inmem` (default): app starts a worker pool in-process, reading the sibling `worker.yaml` (or the path from `--worker-config`).
- `redis`: app only handles Slack; worker runs in a separate process / pod.

Inmem detail:

```bash
agentdock app -c ~/.config/agentdock/app.yaml \
              --worker-config ~/.config/agentdock/worker.yaml
```

## Tests

```bash
(cd app && go test ./... -race)
```

## Dependencies

- `shared/` — queue, logging, crypto, github, configloader, connectivity, prompt
- Does **not** depend on `worker/` (app ✗ worker boundary is enforced)

## See also

- [Top-level README](../README.en.md)
- [worker/README.en.md](../worker/README.en.md)
- [docs/configuration.en.md](../docs/configuration.en.md) — config overview
- [docs/MIGRATION-v2.en.md](../docs/MIGRATION-v2.en.md) — v1 → v2 migration
