# Worker Interactive Preflight Design

## Problem

Local worker startup requires manually passing environment variables with no validation:

```bash
REDIS_ADDR=... GITHUB_TOKEN=... PROVIDERS=claude ./bot worker
```

Wrong Redis address, invalid tokens, or missing agent CLIs are only discovered after startup, wasting time and causing confusion (as seen with the Redis mismatch debugging session).

## Goals

1. Validate all dependencies before starting the worker pool
2. Provide interactive terminal prompts when required values are missing
3. Give clear, actionable error messages on validation failure

## Design

### Startup Flow

```
./bot worker
  ├─ Load config (-config flag or LoadDefaults)
  ├─ applyEnvOverrides (REDIS_ADDR, GITHUB_TOKEN, PROVIDERS)
  │
  ├─ Determine mode: isInteractive = terminal.IsTerminal(stdin)
  │    && any required value is empty
  │
  ├─ Interactive mode? → Prompt missing values one by one
  │    ├─ Redis address (no default, required)
  │    ├─ GitHub token (hidden input, with guidance)
  │    └─ Providers (multi-select from built-in agents)
  │
  ├─ Preflight validation (runs in both modes)
  │    ├─ Redis: PING
  │    ├─ GitHub: GET /user → display username
  │    ├─ GitHub: check token has `repo` scope
  │    └─ Agent CLI: `<cmd> --version` for each selected provider
  │
  ├─ Validation failed?
  │    ├─ Interactive → re-prompt failed item (max 3 retries per item)
  │    └─ Non-interactive → print error, exit 1
  │
  └─ All passed → start worker pool
```

### Interactive Prompts

**Redis address** — no default value, must be explicitly entered:

```
  Redis address: 192.168.1.244:6379
  ✓ Redis connected
```

Empty input shows `Redis address is required`.

**GitHub token** — hidden input with guidance:

```
  GitHub token (ghp_... or github_pat_...):
  Generate at: https://github.com/settings/tokens → "Fine-grained tokens"
  Required permissions: Repository access → Contents (Read), Issues (Write)
  Token: ********
  ✓ Token valid (user: Ivantseng123, scopes: repo)
```

**Providers** — multi-select from built-in agents (`claude`, `codex`, `opencode`):

```
  Select providers:
    [x] claude
    [ ] codex
    [ ] opencode
  ✓ claude v1.0.22
```

### Validation Failure Behavior

**Interactive mode** — re-prompt the failed item only, max 3 retries:

```
  Redis address: 10.0.0.99:6379
  ✗ Redis connect failed: connection refused (attempt 1/3)

  Redis address: 192.168.1.244:6379
  ✓ Redis connected
```

After 3 failed attempts, exit with error:

```
  ✗ Invalid token (attempt 3/3)
  Error: max retries exceeded for GitHub token
  exit status 1
```

**Non-interactive mode** (env vars provided) — report error and exit immediately:

```
  ✗ Redis connect failed: connection refused
  exit status 1
```

### Agent CLI Validation

For each selected provider, run `<command> --version`. If some providers fail:

**Interactive mode** — warn and ask to continue:

```
  ✓ claude v1.0.22
  ⚠ codex: command not found

  Some providers are unavailable. Continue anyway? [Y/n]: y
  Starting worker with: claude
```

**Non-interactive mode** — warn but continue (fallback mechanism handles it):

```
  ✓ claude v1.0.22
  ⚠ codex: command not found
  Starting worker with: claude
```

If ALL providers fail, exit with error in both modes.

### GitHub Token Scope Check

1. Call `GET /user` with the token to verify identity
2. Read the `X-OAuth-Scopes` response header to check for `repo` scope
3. Display username and scopes on success

### Code Changes

All changes in `cmd/bot/worker.go`. New functions:

| Function | Purpose |
|----------|---------|
| `runPreflight(cfg)` | Main flow: determine mode, collect values, validate |
| `promptRedis(cfg)` | Interactive: ask Redis address + ping |
| `promptGitHubToken(cfg)` | Interactive: ask token (hidden) + API validate |
| `promptProviders(cfg)` | Interactive: multi-select + version check |
| `checkRedis(addr)` | Redis PING |
| `checkGitHubToken(token)` | GET /user + scope check |
| `checkAgentCLI(command)` | exec `<cmd> --version` |

No changes to `internal/config/` or `internal/queue/`. Validated values are written back to `cfg` before proceeding to worker pool setup.

### Dependencies

- `golang.org/x/term` — hidden password input for GitHub token
- `bufio.Scanner` (stdlib) — text input
- `os/exec` (stdlib) — agent CLI version check
- `net/http` (stdlib) — GitHub API call
