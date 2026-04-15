# Migrating from AgentDock v0.x to v1.0

v1.0 introduces a new CLI based on spf13/cobra, persistent config in
`~/.config/agentdock/`, and an `init` subcommand for bootstrapping.
This is a hard breaking change — there is no `bot` alias.

## Quick reference

| Before (v0.x)                          | After (v1.0)                                            |
|----------------------------------------|---------------------------------------------------------|
| `./bot`                                | `agentdock app`                                         |
| `./bot worker`                         | `agentdock worker`                                      |
| `./bot -config /etc/agentdock.yaml`    | `agentdock app -c /etc/agentdock.yaml`                  |
| `./bot worker -config X`               | `agentdock worker -c X`                                 |
| (none)                                 | `agentdock init` to generate starter config             |
| `./bot -version`                       | `agentdock --version` or `agentdock -v`                 |
| `./bot -help`                          | `agentdock --help` or `agentdock -h`                    |

## Behavior changes

### 1. Env vars no longer override YAML

In v0.x, env vars (`REDIS_ADDR`, `GITHUB_TOKEN`, etc.) overrode YAML config.
In v1.0, the merge order is `flag > env > --config > default`. YAML wins
over env. CLI flags win over both.

If you relied on env-overriding-YAML, change to either:
- Pass via `--redis-addr=...` flag (highest priority)
- Edit the YAML and remove the env var

### 2. Env-derived secrets are NOT persisted

`REDIS_PASSWORD=xxx agentdock worker` works for that session, but the password
is NOT written to the config file. Next launch without env: Redis auth fails.

To persist secrets:
- Use `agentdock init -i` (interactive setup writes secrets to file)
- Pass via `--github-token=ghp_...` flag — flags ARE persisted

### 3. Default config path moved

v0.x defaulted to `./config.yaml` in the current directory.
v1.0 defaults to `~/.config/agentdock/config.yaml` (literal `~/.config`,
not `os.UserConfigDir`, on every platform).

To keep using your existing path: pass `-c /your/old/path/config.yaml`.

### 4. Save-back happens after every successful startup

v1.0 writes back the merged config when:
- A flag overrode any value, OR
- An interactive preflight prompt filled a value, OR
- The config file didn't exist

Pure read-only startups do NOT touch the file. Your manual YAML comments are
preserved across normal launches.

### 5. `config.example.yaml` removed

Use `agentdock init -c /tmp/sample.yaml` to see the schema.

### 6. Built-in agents are runtime fallback

`claude` / `codex` / `opencode` are built into the binary. Your config can
override individual entries by name; missing names fall back to built-in.
After upgrade, new built-in agents added in future versions appear automatically.

## Docker / docker-compose

```diff
# docker-compose.yml
services:
  app:
    image: ghcr.io/ivantseng123/agentdock:v1.0.0
-   command: ["./bot", "-config", "/etc/agentdock/config.yaml"]
+   command: ["agentdock", "app", "-c", "/etc/agentdock/config.yaml"]
  worker:
    image: ghcr.io/ivantseng123/agentdock:v1.0.0
-   command: ["./bot", "worker", "-config", "/etc/agentdock/config.yaml"]
+   command: ["agentdock", "worker", "-c", "/etc/agentdock/config.yaml"]
```

## systemd

```diff
# /etc/systemd/system/agentdock-app.service
[Service]
-ExecStart=/opt/agentdock/bot -config /etc/agentdock/config.yaml
+ExecStart=/opt/agentdock/agentdock app -c /etc/agentdock/config.yaml
```

## Validation behavior change

v1.0 validates config values on startup and lists ALL errors at once.
Previously, invalid values like `workers: 0` were silently auto-fixed
to defaults. Now the startup fails with a clear error message.

If you have config files with intentionally-invalid values that v0.x
silently fixed, update them to valid values before upgrading.

## Need help?

File an issue at https://github.com/Ivantseng123/agentdock/issues with
your old setup and what's broken — we'll add to this guide.
