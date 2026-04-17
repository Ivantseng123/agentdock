# App-to-Worker Secret Passing

**Issue**: [#56](https://github.com/Ivantseng123/agentdock/issues/56)
**Date**: 2026-04-16
**Updated**: 2026-04-17 (post-grill review)

## Problem

Secrets (GitHub token, K8s token, NPM token, etc.) are currently hardcoded as a single `GH_TOKEN` environment variable in the worker. There is no mechanism for the app to centrally manage and distribute multiple secrets to workers. Workers cannot be guaranteed to have the correct or up-to-date tokens. Additionally, `Job.CloneURL` embeds the GitHub token in plaintext through Redis.

## Decision Summary

| Item | Decision |
|------|----------|
| Secret source | App centralized (config plaintext + env var via dynamic scan) |
| Transport | Job struct → Redis (AES-256-GCM encrypted) |
| Worker injection | Decrypted → `cmd.Env` (never in prompt) |
| Worker override | Worker config `secrets` map overrides app-provided values |
| Encryption key | Symmetric AES-256, shared `secret_key` in config |
| Scope | **Redis transport only**; inmem mode unchanged |
| Backward compat | No `secret_key` → no encryption; `github.token` kept (not deprecated) |
| CloneURL | No longer contains token; worker assembles authenticated URL from job secrets |

## Config Structure

### App config

```yaml
secret_key: "64-char-hex-encoded-32-byte-aes-key"
secrets:
  GH_TOKEN: "ghp_xxx"
  K8S_TOKEN: "hardcoded-or-set-via-env"
  NPM_TOKEN: "npm_xxx"
```

### Worker config (optional override)

```yaml
secret_key: "same-key-as-app"
secrets:
  GH_TOKEN: "ghp_worker_specific"  # overrides app-provided value
```

- `secrets` is `map[string]string`; keys become environment variable names
- `secret_key` is hex-encoded 32 bytes (AES-256); config string is 64 hex characters
- **Environment variable injection**:
  - `SECRET_KEY` env var → `secret_key` config path (static mapping in `EnvOverrideMap()`)
  - `AGENTDOCK_SECRET_<NAME>` env vars → dynamic scan at config load time. E.g., `AGENTDOCK_SECRET_K8S_TOKEN=xxx` → `cfg.Secrets["K8S_TOKEN"] = "xxx"`. The `AGENTDOCK_` prefix avoids collision with K8s or other system env vars.
  - Config-file values and env var values merge; env var wins on conflict.

## Encryption Module

New package: `internal/crypto/`

```go
// Encrypt encrypts plaintext using AES-256-GCM.
// Returns nonce (12 bytes) prepended to ciphertext.
func Encrypt(key, plaintext []byte) ([]byte, error)

// Decrypt decrypts ciphertext produced by Encrypt.
func Decrypt(key, ciphertext []byte) ([]byte, error)
```

- Uses Go stdlib `crypto/aes` + `crypto/cipher` — zero external dependencies
- Random nonce per encryption via `crypto/rand`
- GCM provides authentication (tamper detection) for free

## Job Struct Change

```go
type Job struct {
    // ... existing fields
    EncryptedSecrets []byte `json:"encrypted_secrets,omitempty"`
}
```

- `EncryptedSecrets` is **always** AES-GCM ciphertext when present; there is no unencrypted fallback through this field.
- If `secret_key` is not configured, `EncryptedSecrets` is left empty (nil). Secrets are not sent through the Job at all — only worker-local config secrets apply.
- This eliminates ambiguity: if the field is non-empty, it is encrypted. Period.

### CloneURL Change

`Job.CloneURL` no longer contains the GitHub token. App submits a clean URL (e.g., `https://github.com/owner/repo.git`). Worker assembles the authenticated URL using `GH_TOKEN` from the resolved secrets map.

Before:
```go
// workflow.go — app side
CloneURL: w.repoCache.ResolveURL(pt.SelectedRepo)  // https://ghp_xxx@github.com/...
```

After:
```go
// workflow.go — app side
CloneURL: fmt.Sprintf("https://github.com/%s.git", pt.SelectedRepo)  // no token
```

This eliminates plaintext token leakage through Redis in `Job.CloneURL`.

## Secret Passing Interface

Secrets flow from `executor.go` (decryption) to `AgentRunner` (env injection) via `RunOptions`:

```go
type RunOptions struct {
    OnStarted func(pid int, command string)
    OnEvent   func(event queue.StreamEvent)
    Secrets   map[string]string  // NEW: injected as cmd.Env
}
```

- `executor.go` decrypts job secrets, merges with worker config secrets, sets `opts.Secrets`
- `AgentRunner.runOne()` reads `opts.Secrets` and injects into `cmd.Env`
- **`AgentRunner.githubToken` field is kept** as fallback for inmem mode. Injection logic:
  ```go
  if len(opts.Secrets) > 0 {
      for k, v := range opts.Secrets { env = append(env, k+"="+v) }
  } else if r.githubToken != "" {
      env = append(env, "GH_TOKEN="+r.githubToken)
  }
  ```
- The `Runner` interface signature does not change (it already accepts `RunOptions`)

## Worker Execution Dependencies

`executionDeps` gains two fields for secret handling:

```go
type executionDeps struct {
    // ... existing fields
    secretKey     []byte            // decoded AES key, nil if not configured
    workerSecrets map[string]string // worker config overrides
}
```

`Pool` passes these from config at construction time.

## RepoCache Per-Call Token

`RepoCache.EnsureRepo` gains a `token` parameter so the worker can pass the job's `GH_TOKEN`:

```go
// Before
func (rc *RepoCache) EnsureRepo(repoRef string) (string, error)

// After
func (rc *RepoCache) EnsureRepo(repoRef string, token string) (string, error)
```

- If `token` is non-empty, use it; otherwise fallback to `rc.githubPAT` (backward compat for inmem mode)
- On cache hit (repo already cloned), only run `git remote set-url origin <url-with-token>` **if the token differs** from what's currently in the remote URL. Same token → skip `set-url`, go straight to `git fetch`.
- On cache miss, clone with the provided token.

### RepoProvider Interface Change

```go
type RepoProvider interface {
    Prepare(cloneURL, branch, token string) (string, error)  // token added
    RemoveWorktree(worktreePath string) error
    CleanAll() error
    PurgeStale() error
}
```

`repoCacheAdapter.Prepare()` passes token to `EnsureRepo`. `executor.go` extracts `GH_TOKEN` from the resolved secrets map and passes it to `Prepare`.

`NewRepoCache` signature unchanged — in Redis mode, pass `""` as `githubPAT` at startup (token comes per-call from jobs). In inmem mode, pass `cfg.GitHub.Token` as before.

## Scope: Redis Transport Only

This feature is **only active in Redis transport mode** (`queue.transport: "redis"`).

In inmem mode:
- `AgentRunner.githubToken` fallback handles `GH_TOKEN` injection (same as today)
- `RepoCache` uses `rc.githubPAT` from startup config (same as today)
- `opts.Secrets` is empty; no encryption/decryption occurs
- No behavioral changes whatsoever

## Data Flow (Redis Mode)

```
┌─────────── App ───────────┐
│                            │
│  config.yaml               │
│  ├ secret_key: "aes-key"   │
│  ├ secrets:                │
│  │   GH_TOKEN: "ghp_xxx"  │
│  │   K8S_TOKEN: "from-cfg" │  ← or via AGENTDOCK_SECRET_K8S_TOKEN env
│  └ github.token: "ghp_x"  │  ← auto-merge → secrets["GH_TOKEN"]
│                            │
│  Submit Job:               │
│  1. Resolve secrets map    │
│  2. JSON marshal secrets   │
│  3. AES-GCM encrypt        │
│  4. Job.EncryptedSecrets   │
│  5. Job.CloneURL (no token)│
└──────────┬─────────────────┘
           │ Redis (ciphertext + clean URL)
┌──────────▼─────────────────┐
│                            │
│  Worker                    │
│  config.yaml               │
│  ├ secret_key: "same-key"  │
│  └ secrets:                │  ← optional override
│      GH_TOKEN: "ghp_ovr"  │
│                            │
│  Receive Job:              │
│  1. AES-GCM decrypt        │
│  2. Merge (worker wins)    │
│  3. RepoCache.EnsureRepo   │
│     with GH_TOKEN from     │
│     merged secrets         │
│  4. cmd.Env inject all     │
│                            │
│  exec claude --print ...   │
│  env: GH_TOKEN=ghp_ovr    │
│  env: K8S_TOKEN=eyJhb...   │
│  env: NPM_TOKEN=npm_xxx    │
└────────────────────────────┘
```

## Merge Order

1. Start with app-provided secrets (decrypted from Job)
2. Overlay worker config `secrets` (worker wins on conflict)
3. Result is the final `map[string]string` — used for both `cmd.Env` injection and `RepoCache` token

## Backward Compatibility

| Scenario | Behavior |
|----------|----------|
| No `secret_key`, no `secrets` | Same as today; `github.token` → `GH_TOKEN` env var via `AgentRunner.githubToken` fallback |
| `secrets` set, no `secret_key` | Secrets are NOT sent through Job; only worker-local config secrets apply |
| `secret_key` set, `secrets` set | Full encryption flow |
| Worker has no `secret_key` but receives `EncryptedSecrets` | Job fails with clear error |
| `github.token` set alongside `secrets` | `github.token` auto-merged as `secrets["GH_TOKEN"]`; explicit `secrets.GH_TOKEN` wins |
| inmem transport | Completely unchanged; secrets feature not active |

## `github.token` Handling

`github.token` is **not deprecated**. Both `github.token` and `secrets.GH_TOKEN` are valid config paths.

The auto-merge happens at config post-processing time (in `applyDefaults` or a new `resolveSecrets` step):

1. If `cfg.GitHub.Token` is set and `cfg.Secrets["GH_TOKEN"]` is not → copy `cfg.GitHub.Token` into `cfg.Secrets["GH_TOKEN"]`
2. If both are set → `cfg.Secrets["GH_TOKEN"]` wins (explicit beats implicit)
3. After this step, `cfg.Secrets` is the single source of truth for all secrets
4. `RepoCache` in inmem mode still reads `cfg.GitHub.Token` directly for its own `githubPAT` field

## Error Handling

- `secret_key` is not valid 64-character hex or does not decode to 32 bytes → **fatal at startup** (fail fast)
- Decryption failure (wrong key, corrupt data) → **job fails**, no retry
- Env var referenced by `EnvOverrideMap` is unset → value simply not overridden (consistent with existing behavior)

## `agentdock init` Changes

Add optional step in the init wizard:

1. Ask if user wants to enable secret encryption
2. If yes, auto-generate 32 bytes via `crypto/rand`, hex-encode, write to config
3. Prompt for secrets (key-value pairs) — or tell user to add manually later

## Environment Variable Composition

Secrets override host environment variables of the same name. The composition is:

```go
env := os.Environ()
for k, v := range mergedSecrets {
    env = append(env, fmt.Sprintf("%s=%s", k, v))
}
cmd.Env = env
```

On Linux/macOS, later entries override earlier ones with the same key, so appending secrets after `os.Environ()` guarantees the secret value wins.

## Testing

| Test | Scope |
|------|-------|
| AES-GCM round-trip (encrypt → decrypt) | `internal/crypto/aes_test.go` |
| Decrypt with wrong key fails | `internal/crypto/aes_test.go` |
| Decrypt with corrupt data fails | `internal/crypto/aes_test.go` |
| Merge logic: app secrets + worker override | `internal/worker/executor_test.go` |
| `github.token` auto-merge into `secrets["GH_TOKEN"]` | `internal/config/config_test.go` |
| No `secret_key` → `EncryptedSecrets` is nil | `internal/bot/workflow_test.go` |
| Worker receives `EncryptedSecrets` without `secret_key` → job fails | `internal/worker/executor_test.go` |
| `RunOptions.Secrets` injected into `cmd.Env` | `internal/bot/agent_test.go` |
| `AGENTDOCK_SECRET_*` env var scan | `internal/config/config_test.go` |
| `RepoCache.EnsureRepo` with per-call token | `internal/github/repo_test.go` |
| `RepoCache` skips `set-url` when token unchanged | `internal/github/repo_test.go` |
| `Job.CloneURL` does not contain token | `internal/bot/workflow_test.go` |

## Files Changed

| File | Change |
|------|--------|
| `internal/crypto/aes.go` | **New** — Encrypt / Decrypt functions |
| `internal/crypto/aes_test.go` | **New** — encryption round-trip tests |
| `internal/config/config.go` | Add `SecretKey`, `Secrets` fields + `AGENTDOCK_SECRET_*` dynamic scan + `SECRET_KEY` in `EnvOverrideMap` |
| `internal/queue/job.go` | Add `EncryptedSecrets []byte` field |
| `internal/bot/agent.go` | Generic secret injection via `opts.Secrets` with `githubToken` fallback, remove hardcoded `GH_TOKEN` |
| `internal/bot/workflow.go` | Encrypt secrets when submitting job; `CloneURL` without token |
| `internal/worker/executor.go` | Decrypt + merge worker secrets; pass `GH_TOKEN` to `RepoCache.Prepare`; add `secretKey`/`workerSecrets` to `executionDeps` |
| `internal/github/repo.go` | `EnsureRepo` per-call token param; conditional `git remote set-url` |
| `internal/worker/executor.go` | `RepoProvider.Prepare` adds `token` param |
| `cmd/agentdock/adapters.go` | `repoCacheAdapter.Prepare` passes token; `agentRunnerAdapter` unchanged |
| `cmd/agentdock/worker.go` | Pass `secretKey`/`workerSecrets` to Pool config |
| `cmd/agentdock/init.go` / `prompts.go` | Init wizard: secret_key generation step |

## Out of Scope

- Asymmetric encryption (future upgrade path if shared key management becomes painful)
- Per-channel secret overrides (all jobs get the same secrets from app config)
- Secret rotation mechanism (manual: update config, restart)
- Prompt-level secret injection (secrets are env vars only, never in prompt text)
- inmem transport support for secrets (inmem mode unchanged)
