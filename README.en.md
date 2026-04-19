# AgentDock

[繁體中文](README.md)

> **Upgrading from v1?** See [docs/MIGRATION-v2.en.md](docs/MIGRATION-v2.en.md) (`config.yaml` splits into `app.yaml` + `worker.yaml`, flat worker schema). v0 → v1 is [docs/MIGRATION-v1.en.md](docs/MIGRATION-v1.en.md).

AI agent dispatch platform — receives requests from any source, dispatches to CLI agents (claude/codex/opencode) for execution, returns structured results. Currently supports Slack → codebase triage → GitHub Issue workflow.

Single Go binary backed by three independent modules:

- [`app/`](app/README.en.md) — Slack orchestrator
- [`worker/`](worker/README.en.md) — agent CLI executor
- `shared/` — queue / logging / crypto / GitHub helpers

## Documentation

| Topic | File |
|-------|------|
| Slack App setup (incl. Manifest) | [docs/slack-setup.en.md](docs/slack-setup.en.md) |
| App config (`app.yaml`) | [docs/configuration-app.en.md](docs/configuration-app.en.md) |
| Worker config (`worker.yaml`) | [docs/configuration-worker.en.md](docs/configuration-worker.en.md) |
| Config overview / quick start | [docs/configuration.en.md](docs/configuration.en.md) |
| Deployment (Local / Redis / Docker / K8s / CI) | [docs/deployment.en.md](docs/deployment.en.md) |
| Monitoring, agent behavior, HTTP endpoints | [docs/operations.en.md](docs/operations.en.md) |
| v1 → v2 migration | [docs/MIGRATION-v2.en.md](docs/MIGRATION-v2.en.md) |

## Quick Start

**macOS / Linux (Homebrew):**

```bash
brew tap Ivantseng123/tap
brew install agentdock
agentdock init app -i       # create ~/.config/agentdock/app.yaml
agentdock init worker -i    # create ~/.config/agentdock/worker.yaml
agentdock app               # reads ~/.config/agentdock/app.yaml by default
```

Inmem mode (single-host) starts a local worker pool automatically, reading the sibling `worker.yaml`.

**From source:**

```bash
go build -o agentdock ./cmd/agentdock/
./agentdock init app -i
./agentdock init worker -i
./run.sh
```

> Homebrew installs only the binary; the worker side still needs agent CLIs (`claude`, `opencode`, `codex`) installed. For production deployment, use the Docker image `ghcr.io/ivantseng123/agentdock`.

Haven't created the Slack App yet? See [docs/slack-setup.en.md](docs/slack-setup.en.md).

## Flow

```
@bot (in thread)
  → app: dedup + rate limit → read thread messages
  → repo/branch selection (buttons) → optional description
  → build prompt → Submit to Queue (immediate reply with queue status + cancel button)
  → worker pulls job from Queue
    → clone repo → mount skills → spawn agent CLI
    → agent explores codebase + judges confidence → returns JSON result
  → app receives result → create GitHub issue → post URL to Slack thread
```

## Architecture

```
┌──────────┐     ┌──────────────┐     ┌─────────────┐     ┌──────────────┐
│ app      │────→│ Priority     │────→│ worker Pool │────→│ Result       │
│ (Slack)  │     │ Queue        │     │ (N workers) │     │ Listener     │
│          │     │              │     │             │     │              │
│ dedup +  │     │ channel      │     │ clone repo  │     │ create issue │
│ rate     │     │ priority     │     │ mount skill │     │ post Slack   │
│ limit    │     │              │     │ run agent   │     │              │
└──────────┘     └──────────────┘     └─────────────┘     └──────────────┘
                       ↑ Submit              ↑ Kill              ↑ Status
                       │                     │                   │
                 ┌─────┴─────────────────────┴───────────────────┘
                 │         CommandBus / StatusBus
                 └─────────────────────────────────────────────────
```

### Deployment modes

| Mode | Transport | Description |
|------|-----------|-------------|
| In-Memory | `queue.transport: inmem` | Everything in one process via Go channels (default) |
| Redis Worker | `queue.transport: redis` | App + worker as separate deployments over Redis streams |
| External Worker | Redis + `agentdock worker` | Run the worker on another machine pointing at the same Redis |

All three share the same `JobQueue` / `ResultBus` / `StatusBus` / `CommandBus` / `AttachmentStore` interfaces; only the transport layer swaps. Switching is a config change, not code.

Detailed deployment steps in [docs/deployment.en.md](docs/deployment.en.md).

## Tests

```bash
go test ./... -race                # root module
(cd shared && go test ./... -race)
(cd app && go test ./... -race)
(cd worker && go test ./... -race)
```

Redis integration tests auto-skip when port 6379 is unreachable.

## Log levels

Two independent knobs:

- `log_level` (top-level in app.yaml / worker.yaml): console / stderr, default **info**
- `logging.level` (under `logging:` block): rotated file `logs/YYYY-MM-DD.jsonl`, default **debug**

Accepts `debug` / `info` / `warn` / `error`. See [docs/configuration-app.en.md](docs/configuration-app.en.md) and [docs/configuration-worker.en.md](docs/configuration-worker.en.md).

## License

MIT
