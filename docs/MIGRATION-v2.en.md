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
| `active_agent` | **removed in v2.5** — use `providers: [<name>]` instead; existing yaml with `active_agent:` is silently ignored by the loader (unknown key warning logged) | worker.yaml |
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

Worker deployment:

```yaml
args: ["worker", "-c", "/etc/agentdock/worker.yaml"]
```

> Since v2.1, the inmem transport is gone, so the app pod no longer has to mount `worker.yaml` and the worker always runs as its own deployment. See the **v2.0 → v2.2 follow-up changes** section below.

## Worker machine (local launch)

```bash
brew upgrade agentdock         # or your preferred upgrade path
agentdock init worker -i
agentdock worker               # reads ~/.config/agentdock/worker.yaml by default
```

## FAQ

- **Q: startup logs `config file not found: ~/.config/agentdock/app.yaml`** → run `agentdock init app -i` to create it.
- **Q: worker preflight reports `secret_key 與 app 不匹配`** → the secret_key differs from the app's. Copy the value from the app config into worker.yaml.
- **Q: log warns `未知設定鍵 key=worker.count`** → the schema is flat now. Rename `worker.count` to `count` and `worker.prompt.extra_rules` to `prompt.extra_rules`.

## v2.0 → v2.2 follow-up changes

v2.0 was the big config-split release. The 2.x point releases after that are mostly additive — only one real breaking change:

### v2.0 → v2.1: inmem transport removed (**breaking**)

v2.0 still supported `queue.transport: inmem` (app and worker in one process). v2.1 removes it entirely; only `redis` is accepted. Steps:

1. Set `queue.transport: redis` in both `app.yaml` and `worker.yaml`.
2. Both sides need `redis.addr` and a matching `secret_key`.
3. Run the worker as its own deployment / process.

Leaving `queue.transport: inmem` in place yields an `unsupported queue.transport` startup error.

Other v2.1 changes (additive — existing yaml keeps working):

- `nickname_pool` (worker.yaml) — random display nicknames.
- `worker.github.token` is now optional — if the app ships `secrets["GH_TOKEN"]`, the worker inherits it.

### v2.1 → v2.2: no breaking changes

Adds the `github-pr-review` skill and the `agentdock pr-review-helper` subcommand. At v2.2, the PR Review workflow itself required `pr_review.enabled: true`; from v2.3.x onward it defaults to enabled (flip `enabled: false` to opt out).

### v2.2 + workflow-types (PR #124): prompt schema reshape + PR Review feature flag

Three verbs (`issue` / `ask` / `review`) each with their own prompt. All changes are additive, so an unchanged v2.0-style `app.yaml` still runs — but migrating is recommended.

1. **Prompt moves to per-workflow nesting**:

   Old (flat, supported as a legacy alias):

   ```yaml
   prompt:
     goal: "Use the /triage-issue skill ..."
     output_rules: [...]
   ```

   New:

   ```yaml
   prompt:
     language: English
     allow_worker_rules: true
     issue:
       goal: "..."
       output_rules: []
     ask:
       goal: "..."
       output_rules: [...]
     pr_review:
       goal: "..."
       output_rules: [...]
   ```

   At load time the flat `prompt.goal` / `prompt.output_rules` fields are copied into `prompt.issue.*` (only if `prompt.issue.*` is empty). Any workflow field left blank falls back to the hardcoded defaults in `app/config/defaults.go`, so leaving the whole `prompt:` block empty is valid too.

2. **PR Review feature flag (off by default)**:

   ```yaml
   pr_review:
     enabled: false
   ```

   Before enabling: confirm the worker has the `github-pr-review` skill mounted, `agentdock pr-review-helper` is available on the worker host, and `secrets.GH_TOKEN` has permission to post review comments on the target PR.

3. **Slack `/triage` slash command is now a fallback**: real triggers are `@bot <verb>` (`issue` / `ask` / `review`). The old `/triage` command is still registered — when invoked, the bot replies with a hint to switch to `@bot`. No manifest change is required for this to keep working, but updating the slash-command `description` to mark it legacy is recommended.

Full field reference: [configuration-app.en.md](configuration-app.en.md#workflow-specific-prompts) and [configuration-app.en.md](configuration-app.en.md#enabling-pr-review).
