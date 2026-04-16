# App-to-Worker Secret Passing

**Issue**: [#56](https://github.com/Ivantseng123/agentdock/issues/56)
**Date**: 2026-04-16

## Problem

Secrets (GitHub token, K8s token, NPM token, etc.) are currently hardcoded as a single `GH_TOKEN` environment variable in the worker. There is no mechanism for the app to centrally manage and distribute multiple secrets to workers. Workers cannot be guaranteed to have the correct or up-to-date tokens.

## Decision Summary

| Item | Decision |
|------|----------|
| Secret source | App centralized (config plaintext + K8s env var via `${...}`) |
| Transport | Job struct → Redis (AES-256-GCM encrypted) |
| Worker injection | Decrypted → `cmd.Env` (never in prompt) |
| Worker override | Worker config `secrets` map overrides app-provided values |
| Encryption key | Symmetric AES-256, shared `secret_key` in config |
| Backward compat | No `secret_key` → no encryption; `github.token` auto-merges into `secrets["GH_TOKEN"]` |

## Config Structure

### App config

```yaml
secret_key: "64-char-hex-encoded-32-byte-aes-key"  # or ${SECRET_KEY}
secrets:
  GH_TOKEN: "ghp_xxx"
  K8S_TOKEN: "${K8S_TOKEN}"     # expanded from environment variable
  NPM_TOKEN: "npm_xxx"
```

### Worker config (optional override)

```yaml
secret_key: "same-key-as-app"
secrets:
  GH_TOKEN: "ghp_worker_specific"  # overrides app-provided value
```

- `secrets` is `map[string]string`; keys become environment variable names
- `${VAR}` syntax is expanded at config load time
- `secret_key` is hex-encoded 32 bytes (AES-256)
- Both `secret_key` and `secrets` values support `${...}` env var expansion

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

## Data Flow

```
┌─────────── App ───────────┐
│                            │
│  config.yaml               │
│  ├ secret_key: "aes-key"   │
│  ├ secrets:                │
│  │   GH_TOKEN: "ghp_xxx"  │
│  │   K8S_TOKEN: "${K8S_T}" │  ← env var expansion
│  └ github.token: "ghp_x"  │  ← backward compat → secrets["GH_TOKEN"]
│                            │
│  Submit Job:               │
│  1. Expand ${...}          │
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
| `secrets` set, no `secret_key` | Secrets travel unencrypted (not recommended, but works) |
| `secret_key` set, `secrets` set | Full encryption flow |
| Worker has no `secret_key` but receives `EncryptedSecrets` | Job fails with clear error |
| `github.token` set alongside `secrets` | `github.token` auto-merged as `secrets["GH_TOKEN"]`; explicit `secrets.GH_TOKEN` wins |

## Error Handling

- `secret_key` not 32 bytes → **fatal at startup** (fail fast)
- Decryption failure (wrong key, corrupt data) → **job fails**, no retry
- `${ENV_VAR}` references nonexistent var → **fatal at startup**

## `agentdock init` Changes

Add optional step in the init wizard:

1. Ask if user wants to enable secret encryption
2. If yes, auto-generate 32 bytes via `crypto/rand`, hex-encode, write to config
3. Prompt for secrets (key-value pairs) — or tell user to add manually later

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
