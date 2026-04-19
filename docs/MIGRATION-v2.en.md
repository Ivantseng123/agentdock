# AgentDock v2 Migration Guide

[繁體中文](MIGRATION-v2.md)

**TL;DR**: the single `config.yaml` splits into `app.yaml` + `worker.yaml`. Rebuild once per node. Existing 3 worker + 1 app pod each upgrade independently.

## Why split

In v2, app and worker become fully independent Go modules. App owns Slack handling + job submission; worker runs agent CLIs. Splitting config lets the two schemas evolve independently (and eventually live in separate repos).

## Rough steps

1. Upgrade the binary to v2.0.0 (K8s image, brew, etc.).
2. Rebuild `app.yaml` and `worker.yaml` manually.
3. Update deployment (K8s ConfigMap, worker-machine launch command).

## Rebuild config

### App

```bash
agentdock init app -c ~/.config/agentdock/app.yaml -i
```

Interactive mode prompts for Slack bot + app tokens, GitHub token, Redis address, and secret_key (if Redis mode, offers to auto-generate a 32-byte key — copy it to worker.yaml).

### Worker

```bash
agentdock init worker -c ~/.config/agentdock/worker.yaml -i
```

Interactive mode prompts for GitHub token, Redis address, secret_key (must match the app's), and providers. Built-in agents (claude / codex / opencode) are pre-populated under `agents:`.

## Field mapping

Old `config.yaml` → new `app.yaml` or `worker.yaml`:

| Old key | New key | File |
|---|---|---|
| `slack.*` | `slack.*` | app.yaml |
| `channels.*` | `channels.*` | app.yaml |
| `channel_defaults.*` | `channel_defaults.*` | app.yaml |
| `auto_bind` | `auto_bind` | app.yaml |
| `max_thread_messages` | `max_thread_messages` | app.yaml |
| `max_concurrent` | **removed** (was already deprecated in favour of `workers`) | — |
| `rate_limit.*` | `rate_limit.*` | app.yaml |
| `mantis.*` | `mantis.*` | app.yaml |
| `channel_priority.*` | `channel_priority.*` | app.yaml |
| `prompt.goal` / `prompt.output_rules` / `prompt.language` / `prompt.allow_worker_rules` | same | app.yaml |
| `skills_config` | `skills_config` | app.yaml |
| `attachments.*` | `attachments.*` | app.yaml |
| `server.port` | `server.port` | app.yaml |
| `agents.*` | `agents.*` | **worker.yaml** |
| `active_agent` | `active_agent` | **worker.yaml** |
| `providers` | `providers` | **worker.yaml** |
| `worker.count` | **`count`** (flat) | worker.yaml |
| `worker.prompt.extra_rules` | **`prompt.extra_rules`** (flat) | worker.yaml |
| `github.token`, `redis.*`, `logging.*`, `repo_cache.*`, `queue.*`, `secret_key`, `secrets.*` | same | **Put in both files** (each module reads its own) |

Highlights:

- **`max_concurrent` is gone.** It was deprecated (replaced by `workers` / `worker.count`) and v2 drops it entirely.
- **Flat worker schema**: `worker.count` → `count`, `worker.prompt.extra_rules` → `prompt.extra_rules`. worker.yaml already lives at worker scope, so the nest was redundant.
- **`github.token` / `redis.*` / `secret_key` go in both files**. v2 has no shared config; each side reads its own.

## K8s ConfigMap split

Old:

```yaml
volumeMounts:
  - name: config
    mountPath: /etc/agentdock/config.yaml
    subPath: config.yaml
args: ["app", "-c", "/etc/agentdock/config.yaml"]
```

New:

```yaml
volumeMounts:
  - name: app-config
    mountPath: /etc/agentdock/app.yaml
    subPath: app.yaml
args: ["app", "-c", "/etc/agentdock/app.yaml"]
```

Inmem-mode app pod also needs worker.yaml:

```yaml
volumeMounts:
  - name: app-config
    mountPath: /etc/agentdock/app.yaml
    subPath: app.yaml
  - name: worker-config
    mountPath: /etc/agentdock/worker.yaml
    subPath: worker.yaml
args:
  - "app"
  - "-c"
  - "/etc/agentdock/app.yaml"
  - "--worker-config"
  - "/etc/agentdock/worker.yaml"
```

Worker deployment (Redis mode):

```yaml
args: ["worker", "-c", "/etc/agentdock/worker.yaml"]
```

## Worker machine (local launch)

```bash
brew upgrade agentdock         # or your preferred upgrade path
agentdock init worker -i
agentdock worker               # reads ~/.config/agentdock/worker.yaml by default
```

## FAQ

- **Q: startup logs `config file not found: ~/.config/agentdock/app.yaml`** → run `agentdock init app -i` to create it.
- **Q: `unsupported queue.transport`** → v2.1 removes inmem mode. `queue.transport` must be `redis` on both app and worker, with `redis.addr` set.
- **Q: worker preflight reports `secret_key 與 app 不匹配`** → the secret_key differs from the app's. Copy the value from the app config into worker.yaml.
- **Q: log warns `未知設定鍵 key=worker.count`** → the schema is flat now. Rename `worker.count` to `count` and `worker.prompt.extra_rules` to `prompt.extra_rules`.
