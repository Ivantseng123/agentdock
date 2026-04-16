# App-to-Worker Secret Passing

**Issue**: [#56](https://github.com/Ivantseng123/agentdock/issues/56)
**Date**: 2026-04-16

## Problem

Secrets (GitHub token, K8s token, NPM token, etc.) are currently hardcoded as a single `GH_TOKEN` environment variable in the worker. There is no mechanism for the app to centrally manage and distribute multiple secrets to workers. Workers cannot be guaranteed to have the correct or up-to-date tokens.

## Decision Summary

| Item | Decision |
|------|----------|
| Secret source | App centralized (config plaintext + env var via `EnvOverrideMap` pattern) |
| Transport | Job struct → Redis (AES-256-GCM encrypted) |
| Worker injection | Decrypted → `cmd.Env` (never in prompt) |
| Worker override | Worker config `secrets` map overrides app-provided values |
| Encryption key | Symmetric AES-256, shared `secret_key` in config |
| Backward compat | No `secret_key` → no encryption; `github.token` auto-merges into `secrets["GH_TOKEN"]` |

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
- **Environment variable injection** uses the existing `EnvOverrideMap()` pattern in `config.go`, not a `${...}` interpolation syntax (which does not exist in this codebase). New env var mappings:
  - `SECRET_KEY` env var → `secret_key` config path
  - `SECRET_<NAME>` env var → `secrets.<name>` config path (e.g., `SECRET_K8S_TOKEN` → `secrets.K8S_TOKEN`)
  - This is consistent with how `GITHUB_TOKEN`, `SLACK_BOT_TOKEN`, etc. already work

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
- `AgentRunner` no longer stores `githubToken` as a field — it comes through `opts.Secrets`
- The `Runner` interface signature does not change (it already accepts `RunOptions`)

## Data Flow

```
┌─────────── App ───────────┐
│                            │
│  config.yaml               │
│  ├ secret_key: "aes-key"   │
│  ├ secrets:                │
│  │   GH_TOKEN: "ghp_xxx"  │
│  │   K8S_TOKEN: "from-cfg" │  ← or via SECRET_K8S_TOKEN env (EnvOverrideMap)
│  └ github.token: "ghp_x"  │  ← backward compat → secrets["GH_TOKEN"]
│                            │
│  Submit Job:               │
│  1. Resolve secrets map    │
│  2. JSON marshal secrets   │
│  3. AES-GCM encrypt        │
│  4. Job.EncryptedSecrets   │
└──────────┬─────────────────┘
           │ Redis (ciphertext)
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
│  3. cmd.Env inject all     │
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
3. Result is the final `map[string]string` injected into `cmd.Env`

## Backward Compatibility

| Scenario | Behavior |
|----------|----------|
| No `secret_key`, no `secrets` | Same as today; `github.token` → `GH_TOKEN` env var |
| `secrets` set, no `secret_key` | Secrets are NOT sent through Job; only worker-local config secrets apply |
| `secret_key` set, `secrets` set | Full encryption flow |
| Worker has no `secret_key` but receives `EncryptedSecrets` | Job fails with clear error |
| `github.token` set alongside `secrets` | `github.token` auto-merged as `secrets["GH_TOKEN"]`; explicit `secrets.GH_TOKEN` wins |

## Error Handling

- `secret_key` is not valid 64-character hex or does not decode to 32 bytes → **fatal at startup** (fail fast)
- Decryption failure (wrong key, corrupt data) → **job fails**, no retry
- Env var referenced by `EnvOverrideMap` is unset → value simply not overridden (consistent with existing behavior)

## `agentdock init` Changes

Add optional step in the init wizard:

1. Ask if user wants to enable secret encryption
2. If yes, auto-generate 32 bytes via `crypto/rand`, hex-encode, write to config
3. Prompt for secrets (key-value pairs) — or tell user to add manually later

## `github.token` Auto-Merge

The merge happens at config post-processing time (in `applyDefaults` or a new `resolveSecrets` step):

1. If `cfg.GitHub.Token` is set and `cfg.Secrets["GH_TOKEN"]` is not → copy `cfg.GitHub.Token` into `cfg.Secrets["GH_TOKEN"]`
2. If both are set → `cfg.Secrets["GH_TOKEN"]` wins (explicit beats implicit)
3. After this step, `cfg.Secrets` is the single source of truth for all secrets
4. `AgentRunner` no longer reads `cfg.GitHub.Token` directly for env injection

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

## Files Changed

| File | Change |
|------|--------|
| `internal/crypto/aes.go` | **New** — Encrypt / Decrypt functions |
| `internal/crypto/aes_test.go` | **New** — encryption round-trip tests |
| `internal/config/config.go` | Add `SecretKey`, `Secrets` fields + env var expansion |
| `internal/queue/job.go` | Add `EncryptedSecrets []byte` field |
| `internal/bot/agent.go` | Generic secret injection via `cmd.Env`, remove hardcoded `GH_TOKEN` |
| `internal/bot/workflow.go` | Encrypt secrets when submitting job |
| `internal/worker/executor.go` | Decrypt + merge worker secrets before agent execution |
| `cmd/agentdock/adapters.go` | Pass secrets through to Runner |
| `cmd/agentdock/init.go` / `prompts.go` | Init wizard: secret_key generation step |

## Out of Scope

- Asymmetric encryption (future upgrade path if shared key management becomes painful)
- Per-channel secret overrides (all jobs get the same secrets from app config)
- Secret rotation mechanism (manual: update config, restart)
- Prompt-level secret injection (secrets are env vars only, never in prompt text)
