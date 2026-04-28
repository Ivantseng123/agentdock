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

Interactive mode prompts for GitHub token, Redis address, secret_key (must match the app's), and providers. Built-in agents (claude / codex / opencode) are filled at startup by `mergeBuiltinAgents` — they are no longer frozen into the `agents:` block (see the v2.6 → v2.7 section below).

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

### v2.2 → v2.3: `workflows:` promoted to top level, `prompt_defaults:` split out (additive, aliased)

**Motivation** (issue #126): the old schema had two layering problems — `prompt:` doubled as both the prompt container and the workflow container (inverted layering), and a single workflow's properties were split across `prompt.pr_review` and the top-level `pr_review` block. The new schema promotes workflows to the top level, nests prompt beneath each workflow, and pulls cross-workflow `language` / `allow_worker_rules` into `prompt_defaults:`.

**New shape (recommended)**:

```yaml
workflows:
  issue:
    prompt:
      goal: "..."
      response_schema: "..."
      output_rules: []
  ask:
    prompt:
      goal: "..."
      output_rules: [...]
  pr_review:
    enabled: false          # PR Review feature flag now lives here
    prompt:
      goal: "..."
      output_rules: [...]

prompt_defaults:
  language: English
  allow_worker_rules: true
```

**Legacy aliases (handled automatically — old yaml still loads)**:

| Old path | Mapped to | Notes |
|---|---|---|
| `prompt.goal` / `prompt.response_schema` / `prompt.output_rules` (Old-A, pre-v2.1 flat form) | `workflows.issue.prompt.*` | Only copied when the new field is empty |
| `prompt.issue.*` / `prompt.ask.*` / `prompt.pr_review.*` (Old-B, v2.2 interim) | `workflows.issue.prompt.*` / `workflows.ask.prompt.*` / `workflows.pr_review.prompt.*` | Only copied when the new field is empty |
| `prompt.language` / `prompt.allow_worker_rules` | `prompt_defaults.language` / `prompt_defaults.allow_worker_rules` | Works for both legacy variants |
| Top-level `pr_review.enabled` (Old-B) | `workflows.pr_review.enabled` | |

**Mixed yaml rule**: if both the new shape and a legacy block are present in the same file, the new shape wins; the app logs a `component=config phase=載入` warning at startup suggesting the legacy block be removed.

**Suggested migration**:

1. `prompt.issue.*` → `workflows.issue.prompt.*` (ditto ask / pr_review).
2. Top-level `pr_review.enabled` → `workflows.pr_review.enabled`.
3. `prompt.language` / `prompt.allow_worker_rules` → `prompt_defaults.*`.
4. Drop the legacy `prompt:` / top-level `pr_review:` blocks.

Shortcut: `agentdock init app -c app-new.yaml --force` produces the new shape directly — port your custom goal / output_rules into it.

### v2.5 → v2.6: `queue.store` field added (**defaults to `redis` — upgrade changes behaviour**)

v2.6 wires `RedisJobStore` (#145 / PR #147) into the app (#146) and defaults `queue.store` to `redis` so production deployments get the #123 fix (in-flight jobs resuming after app restart) without having to opt in.

**⚠️ Upgrade impact (when existing yaml doesn't set `queue.store`)**

- **Behaviour change**: app restart switches from "all in-flight state lost (`:hourglass:` stuck forever)" to "state rehydrated from Redis, Slack threads complete correctly". For production, this means the bug is fixed.
- **Extra Redis load**: ~0.6 × N QPS per active worker (from ResultListener / StatusListener `store.Get` + occasional `UpdateStatus` per StatusReport cycle). Size Redis accordingly; negligible for typical deployments.
- **TTL defaults to 1h**: refreshed on every write. If a job runs longer than the TTL, records risk eviction mid-run — set `queue.store_ttl` comfortably larger than your longest expected job runtime.

**To keep v2.5's in-process behaviour** (local dev / single-pod tests that don't want Redis persistence), set `mem` explicitly in `app.yaml`:

```yaml
queue:
  # ... other fields unchanged ...
  store: mem            # explicit opt-out, reverts to v2.5 behaviour: in-flight state is lost on restart
```

App-only change; `worker.yaml` stays as is (workers don't read JobStore — they only publish JobResult / StatusReport).

**Startup behaviour**: on the `redis` path the app calls `ListAll()` once and logs `rehydrated in-flight jobs from previous instance` with the count of non-terminal records. Terminal-state records are left to TTL — the app never deletes them proactively.

Background: [#123](https://github.com/Ivantseng123/agentdock/issues/123) — on app restart, in-flight Slack threads were orphaned (the `:hourglass:` status never cleared) because even when the worker published the final result, the app's fresh MemJobStore had no record to correlate against.

### v2.6 → v2.7: `init worker` no longer freezes a BuiltinAgents snapshot

**Behavior change**: `agentdock init worker` no longer writes an `agents:` block into the generated `worker.yaml`. Instead, `mergeBuiltinAgents` fills all built-in entries (claude / codex / opencode) from the current binary at every startup.

**Existing users with an `agents:` block (non-breaking)**: your yaml keeps working unchanged. `mergeBuiltinAgents` only fills missing entries — it never overwrites entries that already exist. If your yaml contains a stale opencode entry (e.g. missing `--pure`), the worker will continue using that stale config after upgrade.

**To pick up refreshed built-in defaults**:

```bash
# Delete the agents: block (or the specific agent entry you want refreshed)
# and restart the worker. No other steps required.
```

Concrete example: PR #108 added `--pure` to opencode's args. Operators who ran `init` before that PR and who still have `agents.opencode` in their yaml will not get `--pure` automatically — they need to delete that entry (or the whole `agents:` block) to have `mergeBuiltinAgents` fill in the latest value.

**Overriding specific fields**: write only the fields you want to change; unspecified fields are filled from BuiltinAgents:

```yaml
agents:
  opencode:
    timeout: 30m  # override timeout only; command/args/skill_dir come from built-ins
```
