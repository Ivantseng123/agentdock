# AgentDock

[繁體中文](README.md)

> **Upgrading from v0.x?** See [docs/MIGRATION-v1.md](docs/MIGRATION-v1.md) (binary renamed to `agentdock`, subcommand required, default config path moved).

AI agent dispatch platform — receives requests from any source, dispatches to CLI agents (claude/codex/opencode) for execution, returns structured results. Currently supports Slack → codebase triage → GitHub Issue workflow.

Single Go binary, supports both in-memory and Redis transports. Workers can run in the same process, separate pods, or on a teammate's machine.

## Documentation

| Topic | File |
|-------|------|
| Slack App setup (incl. Manifest) | [docs/slack-setup.en.md](docs/slack-setup.en.md) |
| Configuration (config.yaml, Skills, NPX) | [docs/configuration.en.md](docs/configuration.en.md) |
| Deployment (Local / Redis / Docker / K8s / CI) | [docs/deployment.en.md](docs/deployment.en.md) |
| Monitoring, agent behavior, HTTP endpoints | [docs/operations.en.md](docs/operations.en.md) |

## Quick Start

**macOS / Linux (Homebrew, recommended for team developers):**

```bash
brew tap Ivantseng123/tap
brew install agentdock
agentdock init -i   # interactive prompts for Slack / GitHub tokens
agentdock app -c config.yaml
```

Upgrade: `brew upgrade agentdock`.

> Homebrew installs only the binary. The `app`/`worker` subcommands still need a config and external CLIs (`claude`, `opencode`, `codex`, `gemini`). For production deployment, use the Docker image `ghcr.io/ivantseng123/agentdock`.

**From source:**

```bash
go build -o agentdock ./cmd/agentdock/
./agentdock init -i   # interactive prompts for Slack / GitHub tokens
./run.sh
```

Or directly:

```bash
./run.sh
# equivalent to: go build -o agentdock ./cmd/agentdock/ && ./agentdock app -c config.yaml
```

`run.sh` automatically sets up agent skills → build → start the app.

Haven't created the Slack App yet? See [docs/slack-setup.en.md](docs/slack-setup.en.md).

## Flow

```
@bot or /triage (in thread)
  → dedup + rate limit → read all thread messages
  → repo/branch selection (buttons in thread) → optional description
  → build prompt → Submit to Priority Queue (immediate reply with queue status + cancel button)
  → Worker consumes job from Queue
    → clone repo → mount skills → spawn CLI agent
    → agent explores codebase + evaluates confidence → returns JSON result
  → App receives result → create GitHub issue → post URL to Slack thread
```

## Queue Architecture

Bot uses a producer/consumer queue to decouple Slack event handling from agent execution:

```
┌──────────┐     ┌──────────────┐     ┌─────────────┐     ┌──────────────┐
│ Slack    │────→│ Priority     │────→│ Worker Pool │────→│ Result       │
│ Handler  │     │ Queue        │     │ (N workers) │     │ Listener     │
│          │     │ (channel     │     │             │     │              │
│ dedup +  │     │  priority)   │     │ clone repo  │     │ create issue │
│ rate     │     │              │     │ mount skill │     │ post Slack   │
│ limit    │     │ capacity: 50 │     │ run agent   │     │ cleanup      │
└──────────┘     └──────────────┘     └─────────────┘     └──────────────┘
                       ↑ Submit              ↑ Kill              ↑ Status
                       │                     │                   │
                 ┌─────┴─────────────────────┴───────────────────┘
                 │              CommandBus / StatusBus
                 │        (kill signals, agent status reports)
                 └───────────────────────────────────────────────┘
```

- **Priority Queue**: channel-based priority + FIFO, bounded capacity
- **Worker Pool**: N goroutines consuming jobs, each job has an independent context (cancellable)
- **StatusBus**: workers periodically report agent status (PID, tool calls, files read, cost)
- **CommandBus**: app → worker kill signal channel

### Deployment Modes

| Mode | Transport | Description |
|------|-----------|-------------|
| In-Memory | `queue.transport: inmem` | Everything in one process, Go channel communication (default) |
| Redis Worker | `queue.transport: redis` | App and Worker deployed separately, Redis Stream/Pub/Sub |
| External Worker | Redis + runner binary | External machines run `agentdock worker`, same Redis |

All three modes use the same interfaces (`JobQueue`, `ResultBus`, `StatusBus`, `CommandBus`, `AttachmentStore`), only the transport layer changes. Switch by changing config only.

Redis architecture diagram, external worker dependencies, Docker/K8s deployment steps: see [docs/deployment.en.md](docs/deployment.en.md).

## Triggers

| Method | Example | Description |
|--------|---------|-------------|
| `@bot` mention | `@bot` in thread | Reads all preceding thread messages (primary trigger) |
| `/triage` | `/triage` | Shows usage hint (Slack limitation: cannot detect thread context) |

Bot only operates in **threads**. Triggering directly in a channel prompts "please use in a thread".

## Testing

```bash
go test ./...   # 215 tests (Redis tests auto-skip if no Redis)
```

## Log Levels

Two independent levels:

- `log_level` (top-level): console / stderr output, default **info**
- `logging.level` (inside `logging:` block): file `logs/YYYY-MM-DD.jsonl`, default **debug**

Accepted: `debug` / `info` / `warn` / `error`. Set via:

```yaml
log_level: debug     # in config.yaml
```

Or CLI: `./agentdock app --log-level debug`. See [docs/configuration.en.md#log-levels](docs/configuration.en.md) for details.

## License

MIT
