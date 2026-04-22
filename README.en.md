# AgentDock

[繁體中文](README.md)

> **Upgrading from v1?** See [docs/MIGRATION-v2.en.md](docs/MIGRATION-v2.en.md) (`config.yaml` splits into `app.yaml` + `worker.yaml`, flat worker schema). v0 → v1 is [docs/MIGRATION-v1.en.md](docs/MIGRATION-v1.en.md).

AI agent dispatch platform — receives requests from Slack, dispatches to CLI agents (claude/codex/opencode) for execution, returns structured results. Three workflows today, all routed by `@bot <verb>`:

| Verb | Example | Outcome |
|------|---------|---------|
| `issue` | `@bot issue` · `@bot owner/repo` (legacy bare-repo routes to issue) | Agent triages codebase → app creates GitHub issue → URL posted to thread |
| `ask` | `@bot ask where is the retry logic?` | Agent reads thread (repo optional) → answers inline in thread, no issue created |
| `review` | `@bot review <PR URL>` | Agent clones PR head → posts line-level comments + summary to the PR, reports status back to thread |

No verb (`@bot`) → three-button selector lets you pick issue / ask / review. `review` is off by default; enable with `pr_review.enabled: true`.

Single Go binary (`agentdock` with `app` / `worker` subcommands). Redis is the only transport today; `queue.transport` stays as the extension point for future backends. Repo contains three independent Go modules:

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
agentdock app               # in a second terminal, run `agentdock worker`
```

App and worker always run as separate processes (`queue.transport: redis`; both sides must share the same Redis address and `secret_key`).

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
@bot <verb> (in thread)
  → app: dedup + rate limit → workflow dispatcher
    · Unknown verb or bare @bot → three-button selector
    · Every selector / modal has a "Cancel" button that also clears dedup
  → per-workflow UX:
    · issue   → repo / branch selection → optional description
    · ask     → optional repo attach (branch if multiple exist)
    · review  → scan thread for PR URL; if missing → modal prompts for URL
  → build prompt (with <bot> handle, thread, skill) → submit to Queue
  → worker pulls job:
    → prepare workdir (clone repo if provided, else empty dir)
    → mount the workflow's skill (triage-issue / ask-assistant / github-pr-review)
    → spawn CLI agent, returns the workflow-specific JSON
      (===TRIAGE_RESULT=== / ===ASK_RESULT=== / ===REVIEW_RESULT===)
  → app handles result per workflow: create issue / post answer / report PR review status
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
| Redis Worker | `queue.transport: redis` | App and worker are separate processes communicating over Redis streams / pub-sub |
| External Worker | Redis + remote runner | Run `agentdock worker` on another machine pointing at the same Redis |

Both go through the same `JobQueue` / `ResultBus` / `StatusBus` / `CommandBus` / `AttachmentStore` interfaces. `queue.transport` is the extension point — future backends get a new value here.

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
