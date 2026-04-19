# Deployment

[繁體中文](deployment.md)

## Local

App and worker are always two separate processes communicating over Redis.

```bash
# Start Redis
redis-server --daemonize yes

# Scaffold configs (interactive mode walks you through Slack/GitHub/Redis)
agentdock init app -i
agentdock init worker -i

# App (Slack side)
agentdock app -c ~/.config/agentdock/app.yaml

# Worker (agent executor) — run multiple; each consumes jobs independently
agentdock worker -c ~/.config/agentdock/worker.yaml
```

## External Worker (Teammate's Machine)

Teammates don't need any config files, just binary + env vars:

```bash
# Prerequisites: agent CLI installed and logged in (e.g. claude login)
REDIS_ADDR=redis.company.com:6379 GITHUB_TOKEN=ghp_xxx agentdock worker
```

Custom agent:
```bash
REDIS_ADDR=redis.company.com:6379 GITHUB_TOKEN=ghp_xxx PROVIDERS=codex agentdock worker
```

Workers have built-in default configs for three agents (claude/codex/opencode), no YAML needed. Redis address and tokens via env vars.

### External Worker Dependencies

If you download the binary from GitHub Release and run `agentdock worker` on an external machine, the **binary is not self-contained**. Workers `exec` the following CLIs — install them and ensure they're in `PATH`:

- **At least one agent CLI** (whichever is configured):
  - `@anthropic-ai/claude-code` (npm)
  - `@openai/codex` (npm)
  - `opencode` (see [anomalyco/opencode](https://github.com/anomalyco/opencode) releases)
  - `gemini` (if used)
- **`gh` CLI** (for creating GitHub issues)
- **`git`** (for cloning repos)

If you don't want to manage these dependencies, use the Docker image: `ghcr.io/ivantseng123/agentdock:<version>` includes all runtimes.

**Windows note**: native Windows support for the above CLIs is provided by upstream vendors. If you encounter compatibility issues, use the Docker image (requires WSL2 or Linux VM).

## Redis Mode Architecture

```
┌─────────────┐                    ┌─────────────┐
│  App Pod    │                    │ Worker Pod  │
│             │    Redis Streams   │             │
│ Slack ──→   │──── JobQueue ────→│ consume job │
│ Workflow    │                    │ clone repo  │
│             │←── ResultBus ────│ run agent   │
│ create issue│←── StatusBus ────│ report      │
│ post Slack  │──── CommandBus ──→│ kill signal │
└─────────────┘                    └─────────────┘
```

App doesn't run agents. Workers don't need Slack tokens or GitHub write tokens.

## Docker

Image includes three agent CLIs: claude, codex, opencode.

> **Note: Docker containers can only use API key authentication, not OAuth login.** Agent CLI OAuth (e.g. `claude login`) is bound to the local keychain and cannot be transferred to containers. For personal machines using OAuth, use the "External Worker" method above (native binary).

```bash
docker build -t agentdock .

# App (Slack side)
docker run -e SLACK_BOT_TOKEN=xoxb-... \
           -e SLACK_APP_TOKEN=xapp-... \
           -e GITHUB_TOKEN=ghp_... \
           -e REDIS_ADDR=redis:6379 \
           agentdock app

# Worker (standalone job consumer)
docker run -e REDIS_ADDR=redis:6379 \
           -e GITHUB_TOKEN=ghp_... \
           -e PROVIDERS=claude \
           -e ANTHROPIC_API_KEY=sk-ant-... \
           agentdock worker
```

### Agent Authentication Methods

| Execution | Auth Method | Use Case |
|-----------|-------------|----------|
| Native binary (`agentdock worker`) | OAuth login (`claude login` etc.) | Personal machine, own Pro/Max quota |
| Docker / k8s | API key (env var) | Automated deployment, company API quota |

### Agent Selection & API Key

Workers select agents via `PROVIDERS` env var (comma-separated, tried in order), no config file changes needed:

```bash
# Use claude
docker run -e PROVIDERS=claude -e ANTHROPIC_API_KEY=sk-ant-... ...

# Use codex, fallback to claude (tried in order)
docker run -e PROVIDERS=codex,claude -e OPENAI_API_KEY=sk-... -e ANTHROPIC_API_KEY=sk-ant-... ...

# Use opencode
docker run -e PROVIDERS=opencode -e ANTHROPIC_API_KEY=sk-ant-... ...
```

| Agent | API Key Env Var | How to Get |
|-------|----------------|------------|
| claude | `ANTHROPIC_API_KEY` | [console.anthropic.com](https://console.anthropic.com) |
| codex | `OPENAI_API_KEY` | [platform.openai.com](https://platform.openai.com) |
| opencode | `ANTHROPIC_API_KEY` | [console.anthropic.com](https://console.anthropic.com) |

Only the API keys for agents listed in `PROVIDERS` are needed.

### All Environment Variables

| Env Var | Purpose | Required |
|---------|---------|----------|
| `SLACK_BOT_TOKEN` | Slack Bot token | App mode |
| `SLACK_APP_TOKEN` | Slack App-Level token | App mode |
| `GITHUB_TOKEN` | GitHub token (App: read+write, Worker: read) | Yes |
| `REDIS_ADDR` | Redis connection address | Redis mode |
| `REDIS_PASSWORD` | Redis password | If password set |
| `PROVIDERS` | Agent provider order (comma-separated) | No (defaults to config) |
| `ACTIVE_AGENT` | Primary agent | No (defaults to config) |
| `CLAUDE_AUTH_TOKEN` | Claude CLI auth | When using claude |
| `OPENAI_API_KEY` | Codex CLI auth | When using codex |
| `ANTHROPIC_API_KEY` | OpenCode CLI auth | When using opencode |

## Kubernetes

Using Kustomize:

```
deploy/
  base/                          # common deployment (in repo)
    kustomization.yaml
    deployment.yaml
  overlays/
    example/                     # template (in repo)
      kustomization.yaml.example
      secret.yaml.example
    <your-env>/                  # actual config (gitignored)
      kustomization.yaml
      secret.yaml
```

```bash
cp deploy/overlays/example/*.example deploy/overlays/my-env/
vi deploy/overlays/my-env/kustomization.yaml
vi deploy/overlays/my-env/secret.yaml
kubectl apply -k deploy/overlays/my-env/
```

## CI/CD

Automated via [release-please](https://github.com/googleapis/release-please):

1. Write Conventional Commits (`feat:`, `fix:`, `chore:`, etc.)
2. release-please auto-maintains a Release PR (version bump + CHANGELOG)
3. Merge Release PR → auto-creates GitHub Release + tag
4. GHA builds Docker image → push to `ghcr.io`
