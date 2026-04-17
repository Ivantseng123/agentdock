# App-to-Worker Secret Passing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enable the app to send encrypted secrets to workers through Redis, so workers can use centrally-managed tokens for CLI agent execution and repo cloning.

**Architecture:** App encrypts a `map[string]string` of secrets with AES-256-GCM and embeds the ciphertext in the Job struct. Workers decrypt, merge with local overrides, and inject into `cmd.Env` for CLI agents and into `RepoCache` for git operations. Redis-only; inmem mode unchanged.

**Tech Stack:** Go stdlib `crypto/aes`, `crypto/cipher`, `crypto/rand`, `encoding/hex`, `encoding/json`

**Spec:** `docs/superpowers/specs/2026-04-16-app-worker-secrets-design.md`

---

### Task 1: Encryption Module

**Files:**
- Create: `internal/crypto/aes.go`
- Create: `internal/crypto/aes_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// internal/crypto/aes_test.go
package crypto

import (
	"bytes"
	"crypto/rand"
	"testing"
)

func TestEncryptDecrypt_RoundTrip(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	plaintext := []byte(`{"GH_TOKEN":"ghp_xxx","K8S_TOKEN":"eyJhb"}`)

	ciphertext, err := Encrypt(key, plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if bytes.Equal(ciphertext, plaintext) {
		t.Fatal("ciphertext should differ from plaintext")
	}

	decrypted, err := Decrypt(key, ciphertext)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Errorf("got %q, want %q", decrypted, plaintext)
	}
}

func TestDecrypt_WrongKey(t *testing.T) {
	key1 := make([]byte, 32)
	key2 := make([]byte, 32)
	rand.Read(key1)
	rand.Read(key2)

	ciphertext, _ := Encrypt(key1, []byte("secret"))
	_, err := Decrypt(key2, ciphertext)
	if err == nil {
		t.Fatal("expected error decrypting with wrong key")
	}
}

func TestDecrypt_CorruptData(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key)

	_, err := Decrypt(key, []byte("not-valid-ciphertext"))
	if err == nil {
		t.Fatal("expected error decrypting corrupt data")
	}
}

func TestEncrypt_InvalidKeyLength(t *testing.T) {
	_, err := Encrypt([]byte("short"), []byte("data"))
	if err == nil {
		t.Fatal("expected error with invalid key length")
	}
}

func TestEncrypt_EmptyPlaintext(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key)

	ciphertext, err := Encrypt(key, []byte{})
	if err != nil {
		t.Fatalf("Encrypt empty: %v", err)
	}
	decrypted, err := Decrypt(key, ciphertext)
	if err != nil {
		t.Fatalf("Decrypt empty: %v", err)
	}
	if len(decrypted) != 0 {
		t.Errorf("expected empty, got %q", decrypted)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/crypto/ -v`
Expected: compilation error — package does not exist

- [ ] **Step 3: Write the implementation**

```go
// internal/crypto/aes.go
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"io"
)

// Encrypt encrypts plaintext using AES-256-GCM.
// Returns nonce (12 bytes) prepended to ciphertext.
func Encrypt(key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("nonce: %w", err)
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// Decrypt decrypts ciphertext produced by Encrypt.
func Decrypt(key, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("gcm: %w", err)
	}
	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short")
	}
	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
	return gcm.Open(nil, nonce, ciphertext, nil)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/crypto/ -v`
Expected: all 5 tests PASS

- [ ] **Step 5: Commit**

```bash
git add internal/crypto/
git commit -m "feat(crypto): add AES-256-GCM encrypt/decrypt module (#56)"
```

---

### Task 2: Config — Add SecretKey, Secrets, and Env Var Scan

**Files:**
- Modify: `internal/config/config.go:13-38` (Config struct) + `EnvOverrideMap` + new `resolveSecrets`
- Modify: `internal/config/config_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// Add to internal/config/config_test.go

func TestResolveSecrets_GitHubTokenAutoMerge(t *testing.T) {
	cfg := &Config{
		GitHub: GitHubConfig{Token: "ghp_from_github"},
	}
	resolveSecrets(cfg)
	if cfg.Secrets["GH_TOKEN"] != "ghp_from_github" {
		t.Errorf("got %q, want ghp_from_github", cfg.Secrets["GH_TOKEN"])
	}
}

func TestResolveSecrets_ExplicitSecretsWin(t *testing.T) {
	cfg := &Config{
		GitHub:  GitHubConfig{Token: "ghp_from_github"},
		Secrets: map[string]string{"GH_TOKEN": "ghp_explicit"},
	}
	resolveSecrets(cfg)
	if cfg.Secrets["GH_TOKEN"] != "ghp_explicit" {
		t.Errorf("got %q, want ghp_explicit", cfg.Secrets["GH_TOKEN"])
	}
}

func TestResolveSecrets_InitializesNilMap(t *testing.T) {
	cfg := &Config{}
	resolveSecrets(cfg)
	if cfg.Secrets == nil {
		t.Error("Secrets should be initialized to non-nil map")
	}
}

func TestEnvOverrideMap_SecretKey(t *testing.T) {
	t.Setenv("SECRET_KEY", "abc123")
	m := EnvOverrideMap()
	if m["secret_key"] != "abc123" {
		t.Errorf("got %q, want abc123", m["secret_key"])
	}
}

func TestScanSecretEnvVars(t *testing.T) {
	t.Setenv("AGENTDOCK_SECRET_K8S_TOKEN", "k8s-val")
	t.Setenv("AGENTDOCK_SECRET_NPM_TOKEN", "npm-val")
	t.Setenv("UNRELATED_VAR", "ignore")

	got := ScanSecretEnvVars()
	if got["K8S_TOKEN"] != "k8s-val" {
		t.Errorf("K8S_TOKEN = %q, want k8s-val", got["K8S_TOKEN"])
	}
	if got["NPM_TOKEN"] != "npm-val" {
		t.Errorf("NPM_TOKEN = %q, want npm-val", got["NPM_TOKEN"])
	}
	if _, exists := got["UNRELATED_VAR"]; exists {
		t.Error("should not include UNRELATED_VAR")
	}
}

func TestValidateSecretKey_Valid(t *testing.T) {
	// 64 hex chars = 32 bytes
	key := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	_, err := DecodeSecretKey(key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateSecretKey_Invalid(t *testing.T) {
	cases := []string{
		"tooshort",
		"not-hex-not-hex-not-hex-not-hex-not-hex-not-hex-not-hex-not-hex",
		"",
	}
	for _, c := range cases {
		if _, err := DecodeSecretKey(c); err == nil {
			t.Errorf("expected error for %q", c)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/config/ -run "TestResolveSecrets|TestEnvOverrideMap_SecretKey|TestScanSecret|TestValidateSecretKey|TestDecodeSecretKey" -v`
Expected: compilation error — `resolveSecrets`, `ScanSecretEnvVars`, `DecodeSecretKey` not defined

- [ ] **Step 3: Write the implementation**

Add fields to `Config` struct (`config.go:13-38`):

```go
// Add after SkillsConfig field (line 37):
SecretKey string            `yaml:"secret_key"`
Secrets   map[string]string `yaml:"secrets"`
```

Add `SECRET_KEY` to `EnvOverrideMap()` (after the `ACTIVE_AGENT` block, around line 265):

```go
if v := os.Getenv("SECRET_KEY"); v != "" {
    out["secret_key"] = v
}
```

Add new functions at the end of `config.go`:

```go
// ScanSecretEnvVars scans environment for AGENTDOCK_SECRET_* variables
// and returns them as a map with the prefix stripped.
// E.g., AGENTDOCK_SECRET_K8S_TOKEN=xxx → {"K8S_TOKEN": "xxx"}
func ScanSecretEnvVars() map[string]string {
	const prefix = "AGENTDOCK_SECRET_"
	out := make(map[string]string)
	for _, env := range os.Environ() {
		if idx := strings.Index(env, "="); idx > 0 {
			key := env[:idx]
			if strings.HasPrefix(key, prefix) {
				name := key[len(prefix):]
				if name != "" {
					out[name] = env[idx+1:]
				}
			}
		}
	}
	return out
}

// resolveSecrets merges github.token into secrets and applies env var overrides.
func resolveSecrets(cfg *Config) {
	if cfg.Secrets == nil {
		cfg.Secrets = make(map[string]string)
	}
	// Auto-merge github.token → secrets["GH_TOKEN"] if not explicitly set
	if cfg.GitHub.Token != "" {
		if _, exists := cfg.Secrets["GH_TOKEN"]; !exists {
			cfg.Secrets["GH_TOKEN"] = cfg.GitHub.Token
		}
	}
	// Overlay env vars (env wins over config file)
	for k, v := range ScanSecretEnvVars() {
		cfg.Secrets[k] = v
	}
}

// DecodeSecretKey validates and decodes a hex-encoded 32-byte AES key.
func DecodeSecretKey(hexKey string) ([]byte, error) {
	decoded, err := hex.DecodeString(hexKey)
	if err != nil {
		return nil, fmt.Errorf("secret_key: invalid hex: %w", err)
	}
	if len(decoded) != 32 {
		return nil, fmt.Errorf("secret_key: must be 32 bytes (64 hex chars), got %d bytes", len(decoded))
	}
	return decoded, nil
}
```

Add `"encoding/hex"` to the imports.

Call `resolveSecrets` from `applyDefaults` — add at the end of `applyDefaults` (after line 219):

```go
resolveSecrets(cfg)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/config/ -run "TestResolveSecrets|TestEnvOverrideMap_SecretKey|TestScanSecret|TestValidateSecretKey|TestDecodeSecretKey" -v`
Expected: all tests PASS

- [ ] **Step 5: Run all config tests to check for regressions**

Run: `go test ./internal/config/ -v`
Expected: all tests PASS

- [ ] **Step 6: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): add SecretKey, Secrets fields with env var scan (#56)"
```

---

### Task 3: Job Struct — Add EncryptedSecrets

**Files:**
- Modify: `internal/queue/job.go:22-41` (Job struct)
- Modify: `internal/bot/retry_handler.go:48-64` (propagate field)

- [ ] **Step 1: Add EncryptedSecrets to Job**

In `internal/queue/job.go`, add after `SubmittedAt` field (line 40):

```go
EncryptedSecrets []byte `json:"encrypted_secrets,omitempty"`
```

- [ ] **Step 2: Propagate EncryptedSecrets in retry handler**

In `internal/bot/retry_handler.go`, add to the `newJob` construction (after `SubmittedAt` line):

```go
EncryptedSecrets: original.EncryptedSecrets,
```

- [ ] **Step 3: Run existing tests to check nothing breaks**

Run: `go test ./internal/queue/ ./internal/bot/ -v`
Expected: all tests PASS (new field has `omitempty`, zero value is nil)

- [ ] **Step 4: Commit**

```bash
git add internal/queue/job.go internal/bot/retry_handler.go
git commit -m "feat(queue): add EncryptedSecrets field to Job struct (#56)"
```

---

### Task 4: AgentRunner — Generic Secret Injection

**Files:**
- Modify: `internal/bot/agent.go:20-23` (RunOptions) + `agent.go:111-112` (env injection)
- Modify: `internal/bot/agent_test.go`

- [ ] **Step 1: Write the failing test**

```go
// Add to internal/bot/agent_test.go

func TestAgentRunner_SecretsInjected(t *testing.T) {
	dir := t.TempDir()
	// Script prints all env vars containing "TOKEN"
	script := filepath.Join(dir, "env-agent")
	os.WriteFile(script, []byte(`#!/bin/sh
env | grep TOKEN | sort
echo "padding padding padding padding padding padding padding"
`), 0755)

	runner := NewAgentRunner([]config.AgentConfig{
		{Command: script, Args: []string{"{prompt}"}, Timeout: 5 * time.Second},
	})

	secrets := map[string]string{
		"GH_TOKEN":  "ghp_from_secrets",
		"K8S_TOKEN": "k8s_val",
	}
	output, err := runner.Run(context.Background(), slog.Default(), dir, "test", RunOptions{Secrets: secrets})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if !strings.Contains(output, "GH_TOKEN=ghp_from_secrets") {
		t.Errorf("GH_TOKEN not injected: %q", output)
	}
	if !strings.Contains(output, "K8S_TOKEN=k8s_val") {
		t.Errorf("K8S_TOKEN not injected: %q", output)
	}
}

func TestAgentRunner_GithubTokenFallback(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "env-agent")
	os.WriteFile(script, []byte(`#!/bin/sh
env | grep GH_TOKEN
echo "padding padding padding padding padding padding padding"
`), 0755)

	runner := NewAgentRunner([]config.AgentConfig{
		{Command: script, Args: []string{"{prompt}"}, Timeout: 5 * time.Second},
	})
	runner.githubToken = "ghp_fallback"

	// No secrets in RunOptions → fallback to githubToken
	output, err := runner.Run(context.Background(), slog.Default(), dir, "test", RunOptions{})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if !strings.Contains(output, "GH_TOKEN=ghp_fallback") {
		t.Errorf("githubToken fallback not working: %q", output)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/bot/ -run "TestAgentRunner_Secrets|TestAgentRunner_GithubTokenFallback" -v`
Expected: FAIL — `RunOptions` has no `Secrets` field

- [ ] **Step 3: Write the implementation**

In `internal/bot/agent.go`, add `Secrets` field to `RunOptions` (line 20-23):

```go
type RunOptions struct {
	OnStarted func(pid int, command string)
	OnEvent   func(event queue.StreamEvent)
	Secrets   map[string]string
}
```

Replace the env injection block at line 111-112:

```go
	// Inject secrets as environment variables.
	env := os.Environ()
	if len(opts.Secrets) > 0 {
		for k, v := range opts.Secrets {
			env = append(env, fmt.Sprintf("%s=%s", k, v))
		}
	} else if r.githubToken != "" {
		env = append(env, fmt.Sprintf("GH_TOKEN=%s", r.githubToken))
	}
	cmd.Env = env
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/bot/ -run "TestAgentRunner_Secrets|TestAgentRunner_GithubTokenFallback" -v`
Expected: PASS

- [ ] **Step 5: Run all bot tests for regressions**

Run: `go test ./internal/bot/ -v`
Expected: all tests PASS

- [ ] **Step 6: Commit**

```bash
git add internal/bot/agent.go internal/bot/agent_test.go
git commit -m "feat(agent): generic secret injection via RunOptions.Secrets (#56)"
```

---

### Task 5: RepoCache — Per-Call Token

**Files:**
- Modify: `internal/github/repo.go:34` (`EnsureRepo` signature) + `ResolveURL`
- Modify: `internal/github/repo_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// Add to internal/github/repo_test.go

func TestRepoCache_EnsureRepo_PerCallToken(t *testing.T) {
	dir := t.TempDir()
	logger := slog.Default()
	// Create with empty githubPAT (Redis mode)
	rc := NewRepoCache(dir, 0, "", logger)

	// ResolveURL with per-call token
	url := rc.resolveURLWithToken("owner/repo", "ghp_percall")
	if url != "https://ghp_percall@github.com/owner/repo.git" {
		t.Errorf("got %q", url)
	}
}

func TestRepoCache_ResolveURLWithToken_Fallback(t *testing.T) {
	dir := t.TempDir()
	logger := slog.Default()
	rc := NewRepoCache(dir, 0, "ghp_default", logger)

	// Empty per-call token → fallback to rc.githubPAT
	url := rc.resolveURLWithToken("owner/repo", "")
	if url != "https://ghp_default@github.com/owner/repo.git" {
		t.Errorf("got %q", url)
	}
}

func TestRepoCache_ResolveURLWithToken_NoToken(t *testing.T) {
	dir := t.TempDir()
	logger := slog.Default()
	rc := NewRepoCache(dir, 0, "", logger)

	url := rc.resolveURLWithToken("owner/repo", "")
	if url != "https://github.com/owner/repo.git" {
		t.Errorf("got %q", url)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/github/ -run "TestRepoCache_EnsureRepo_PerCall|TestRepoCache_ResolveURLWithToken" -v`
Expected: FAIL — `resolveURLWithToken` not defined

- [ ] **Step 3: Write the implementation**

In `internal/github/repo.go`, add `resolveURLWithToken` method:

```go
// resolveURLWithToken builds a clone URL. Uses perCallToken if non-empty,
// otherwise falls back to rc.githubPAT.
func (rc *RepoCache) resolveURLWithToken(repoRef, perCallToken string) string {
	if strings.HasPrefix(repoRef, "http") || strings.HasPrefix(repoRef, "git@") || strings.HasPrefix(repoRef, "file://") {
		return repoRef
	}
	token := perCallToken
	if token == "" {
		token = rc.githubPAT
	}
	if token != "" {
		return fmt.Sprintf("https://%s@github.com/%s.git", token, repoRef)
	}
	return fmt.Sprintf("https://github.com/%s.git", repoRef)
}
```

Change `EnsureRepo` signature to accept a token parameter:

```go
func (rc *RepoCache) EnsureRepo(repoRef string, token string) (string, error) {
```

Inside `EnsureRepo`, replace `cloneURL := rc.ResolveURL(repoRef)` with:

```go
cloneURL := rc.resolveURLWithToken(repoRef, token)
```

On cache hit (line 57-59), before `git fetch`, add conditional `set-url`:

```go
if last, ok := rc.lastPull[repoRef]; ok && rc.maxAge > 0 && time.Since(last) < rc.maxAge {
    return localPath, nil
}

// Update remote URL if token changed
currentURL := rc.getRemoteURL(localPath)
if cloneURL != currentURL && cloneURL != "" {
    setCmd := exec.Command("git", "-C", localPath, "remote", "set-url", "origin", cloneURL)
    setCmd.Run() // best-effort
}
```

Add helper:

```go
func (rc *RepoCache) getRemoteURL(repoPath string) string {
	out, err := exec.Command("git", "-C", repoPath, "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
```

Also update the re-clone path (around line 72) to use `cloneURL` (it already does via the local variable — just make sure the new `cloneURL` from `resolveURLWithToken` is used consistently).

Update existing callers that call `EnsureRepo` without the token parameter. There is one in `repoCacheAdapter.Prepare()` — that will be updated in Task 7.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/github/ -run "TestRepoCache_EnsureRepo_PerCall|TestRepoCache_ResolveURLWithToken" -v`
Expected: PASS

- [ ] **Step 5: Fix existing EnsureRepo callers**

The existing tests in `repo_test.go` call `EnsureRepo` with one arg. Add `""` as the second arg to `TestRepoCache_EnsureRepo_ClonesNewRepo` and `TestRepoCache_EnsureRepo_PullsExistingRepo`:

```go
// Change: rc.EnsureRepo("owner/repo")
// To:     rc.EnsureRepo("owner/repo", "")
```

- [ ] **Step 6: Run all github tests for regressions**

Run: `go test ./internal/github/ -v`
Expected: all tests PASS

- [ ] **Step 7: Commit**

```bash
git add internal/github/repo.go internal/github/repo_test.go
git commit -m "feat(repo): add per-call token to EnsureRepo (#56)"
```

---

### Task 6: RepoProvider Interface + Adapter — Add Token Param

**Files:**
- Modify: `internal/worker/executor.go:22-27` (RepoProvider interface)
- Modify: `internal/worker/executor.go:55` (Prepare call)
- Modify: `cmd/agentdock/adapters.go:29-48` (repoCacheAdapter)
- Modify: `internal/worker/pool_test.go:31` (mockRepo.Prepare)

- [ ] **Step 1: Update RepoProvider interface**

In `internal/worker/executor.go`, change line 23:

```go
Prepare(cloneURL, branch, token string) (string, error)
```

- [ ] **Step 2: Update repoCacheAdapter**

In `cmd/agentdock/adapters.go`, change `Prepare` method (line 29):

```go
func (a *repoCacheAdapter) Prepare(cloneURL, branch, token string) (string, error) {
```

Inside, change `EnsureRepo` call to pass token:

```go
barePath, err := a.cache.EnsureRepo(cloneURL, token)
```

- [ ] **Step 3: Update mockRepo in tests**

In `internal/worker/pool_test.go`, change mock (line 31):

```go
func (m *mockRepo) Prepare(cloneURL, branch, token string) (string, error) {
```

- [ ] **Step 4: Update executeJob call site**

In `internal/worker/executor.go`, line 55 — this will be updated in Task 8 when we add secret handling. For now, pass empty string:

```go
repoPath, err := deps.repoCache.Prepare(job.CloneURL, job.Branch, "")
```

- [ ] **Step 5: Compile check**

Run: `go build ./...`
Expected: builds successfully

- [ ] **Step 6: Run all tests**

Run: `go test ./internal/worker/ ./cmd/agentdock/ -v`
Expected: all tests PASS

- [ ] **Step 7: Commit**

```bash
git add internal/worker/executor.go cmd/agentdock/adapters.go internal/worker/pool_test.go
git commit -m "feat(worker): add token param to RepoProvider.Prepare (#56)"
```

---

### Task 7: Workflow — Encrypt Secrets + Clean CloneURL

**Files:**
- Modify: `internal/bot/workflow.go:431` (CloneURL)
- Modify: `internal/bot/workflow.go` (encrypt secrets into Job)

- [ ] **Step 1: Understand the Workflow struct**

Read `internal/bot/workflow.go` to find where the `Workflow` struct stores config and secrets. The Workflow needs access to `cfg.Secrets` and `cfg.SecretKey` to encrypt.

- [ ] **Step 2: Add secrets fields to Workflow**

The Workflow struct needs `secretKey []byte` and `secrets map[string]string`. Add these fields and set them during construction (wherever `NewWorkflow` or equivalent is called).

- [ ] **Step 3: Change CloneURL to not include token**

In `workflow.go:431`, replace:

```go
CloneURL: w.repoCache.ResolveURL(pt.SelectedRepo),
```

With:

```go
CloneURL: fmt.Sprintf("https://github.com/%s.git", pt.SelectedRepo),
```

If `pt.SelectedRepo` is already a full URL (starts with `http` or `git@`), preserve it as-is. Add a helper if needed:

```go
func cleanCloneURL(repoRef string) string {
	if strings.HasPrefix(repoRef, "http") || strings.HasPrefix(repoRef, "git@") || strings.HasPrefix(repoRef, "file://") {
		return repoRef
	}
	return fmt.Sprintf("https://github.com/%s.git", repoRef)
}
```

- [ ] **Step 4: Encrypt secrets into Job**

After Job construction (around line 437), add encryption:

```go
if len(w.secretKey) > 0 && len(w.secrets) > 0 {
    secretsJSON, err := json.Marshal(w.secrets)
    if err != nil {
        return fmt.Errorf("marshal secrets: %w", err)
    }
    encrypted, err := crypto.Encrypt(w.secretKey, secretsJSON)
    if err != nil {
        return fmt.Errorf("encrypt secrets: %w", err)
    }
    job.EncryptedSecrets = encrypted
}
```

Add imports: `"encoding/json"` and `"agentdock/internal/crypto"`.

- [ ] **Step 5: Compile check**

Run: `go build ./...`
Expected: builds successfully

- [ ] **Step 6: Commit**

```bash
git add internal/bot/workflow.go
git commit -m "feat(workflow): encrypt secrets into Job, clean CloneURL (#56)"
```

---

### Task 8: Worker Executor — Decrypt, Merge, Inject

**Files:**
- Modify: `internal/worker/executor.go:29-35` (executionDeps)
- Modify: `internal/worker/executor.go:37-91` (executeJob — decrypt + merge + pass to Prepare and RunOptions)
- Modify: `internal/worker/pool.go:13-27` (Pool Config)
- Modify: `internal/worker/pool.go:163-169` (executionDeps construction)
- Modify: `cmd/agentdock/worker.go:86-100` (pass secrets to Pool)
- Modify: `internal/worker/pool_test.go`

- [ ] **Step 1: Write the failing test**

```go
// Add to internal/worker/pool_test.go

func TestExecuteJob_DecryptsAndMergesSecrets(t *testing.T) {
	dir := t.TempDir()

	// Create a secret key and encrypt some secrets
	secretKey := make([]byte, 32)
	rand.Read(secretKey)

	appSecrets := map[string]string{
		"GH_TOKEN":  "ghp_from_app",
		"K8S_TOKEN": "k8s_from_app",
	}
	secretsJSON, _ := json.Marshal(appSecrets)
	encrypted, _ := crypto.Encrypt(secretKey, secretsJSON)

	workerSecrets := map[string]string{
		"GH_TOKEN": "ghp_worker_override",
	}

	// Track what secrets the runner receives
	var capturedSecrets map[string]string
	runner := &secretCapturingRunner{
		onRun: func(opts bot.RunOptions) {
			capturedSecrets = opts.Secrets
		},
	}

	job := &queue.Job{
		ID:               "test-job",
		CloneURL:         "https://github.com/owner/repo.git",
		EncryptedSecrets: encrypted,
	}

	deps := executionDeps{
		attachments:   queue.NewInMemAttachmentStore(),
		repoCache:     &mockRepo{path: dir},
		runner:        runner,
		store:         queue.NewMemJobStore(),
		skillDirs:     nil,
		secretKey:     secretKey,
		workerSecrets: workerSecrets,
	}

	result := executeJob(context.Background(), job, deps, bot.RunOptions{}, slog.Default())
	if result.Status == "failed" {
		t.Fatalf("job failed: %s", result.Error)
	}

	// Worker override wins
	if capturedSecrets["GH_TOKEN"] != "ghp_worker_override" {
		t.Errorf("GH_TOKEN = %q, want ghp_worker_override", capturedSecrets["GH_TOKEN"])
	}
	// App secret passes through
	if capturedSecrets["K8S_TOKEN"] != "k8s_from_app" {
		t.Errorf("K8S_TOKEN = %q, want k8s_from_app", capturedSecrets["K8S_TOKEN"])
	}
}

func TestExecuteJob_NoSecretKey_EncryptedSecrets_Fails(t *testing.T) {
	dir := t.TempDir()

	job := &queue.Job{
		ID:               "test-job",
		CloneURL:         "https://github.com/owner/repo.git",
		EncryptedSecrets: []byte("some-encrypted-data"),
	}

	deps := executionDeps{
		attachments: queue.NewInMemAttachmentStore(),
		repoCache:   &mockRepo{path: dir},
		runner:      &mockRunner{output: "ok"},
		store:       queue.NewMemJobStore(),
		secretKey:   nil, // no key!
	}

	result := executeJob(context.Background(), job, deps, bot.RunOptions{}, slog.Default())
	if result.Status != "failed" {
		t.Error("expected job to fail when EncryptedSecrets present but no secretKey")
	}
}

// Helper: runner that captures RunOptions
type secretCapturingRunner struct {
	onRun func(opts bot.RunOptions)
}

func (r *secretCapturingRunner) Run(ctx context.Context, workDir, prompt string, opts bot.RunOptions) (string, error) {
	if r.onRun != nil {
		r.onRun(opts)
	}
	return `## Summary

Test

===TRIAGE_METADATA===
{"issue_type":"bug","confidence":"high","files":[],"open_questions":[],"suggested_title":"test"}`, nil
}
```

Add imports to the test file: `"crypto/rand"`, `"encoding/json"`, `"agentdock/internal/crypto"`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/worker/ -run "TestExecuteJob_Decrypts|TestExecuteJob_NoSecretKey" -v`
Expected: FAIL — `executionDeps` has no `secretKey`/`workerSecrets` fields

- [ ] **Step 3: Add fields to executionDeps**

In `internal/worker/executor.go`, update `executionDeps` (line 29-35):

```go
type executionDeps struct {
	attachments   queue.AttachmentStore
	repoCache     RepoProvider
	runner        Runner
	store         queue.JobStore
	skillDirs     []string
	secretKey     []byte
	workerSecrets map[string]string
}
```

- [ ] **Step 4: Add decrypt + merge logic to executeJob**

In `internal/worker/executor.go`, add secret handling after attachment resolution and before repo clone (after line 50, before line 52):

```go
	// Decrypt and merge secrets.
	var mergedSecrets map[string]string
	if len(job.EncryptedSecrets) > 0 {
		if len(deps.secretKey) == 0 {
			return failedResult(job, startedAt, fmt.Errorf("job has encrypted secrets but worker has no secret_key configured"), "")
		}
		decrypted, err := crypto.Decrypt(deps.secretKey, job.EncryptedSecrets)
		if err != nil {
			return failedResult(job, startedAt, fmt.Errorf("decrypt secrets: %w", err), "")
		}
		var appSecrets map[string]string
		if err := json.Unmarshal(decrypted, &appSecrets); err != nil {
			return failedResult(job, startedAt, fmt.Errorf("unmarshal secrets: %w", err), "")
		}
		mergedSecrets = appSecrets
	}
	// Overlay worker secrets (worker wins)
	if len(deps.workerSecrets) > 0 {
		if mergedSecrets == nil {
			mergedSecrets = make(map[string]string)
		}
		for k, v := range deps.workerSecrets {
			mergedSecrets[k] = v
		}
	}
```

Add imports: `"encoding/json"` and `"agentdock/internal/crypto"`.

- [ ] **Step 5: Pass GH_TOKEN to RepoCache.Prepare**

Update the `Prepare` call (line 55) to pass the resolved GH_TOKEN:

```go
	ghToken := ""
	if mergedSecrets != nil {
		ghToken = mergedSecrets["GH_TOKEN"]
	}
	repoPath, err := deps.repoCache.Prepare(job.CloneURL, job.Branch, ghToken)
```

- [ ] **Step 6: Pass secrets to RunOptions**

Before `deps.runner.Run` call (line 91), set secrets on opts:

```go
	opts.Secrets = mergedSecrets
	output, err := deps.runner.Run(ctx, repoPath, prompt, opts)
```

- [ ] **Step 7: Add SecretKey/WorkerSecrets to Pool Config**

In `internal/worker/pool.go`, add to `Config` struct (after `Logger` field):

```go
	SecretKey     []byte
	WorkerSecrets map[string]string
```

In `executeWithTracking` (line 163-169), add to `executionDeps` construction:

```go
deps := executionDeps{
	attachments:   p.cfg.Attachments,
	repoCache:     p.cfg.RepoCache,
	runner:        p.cfg.Runner,
	store:         p.cfg.Store,
	skillDirs:     p.cfg.SkillDirs,
	secretKey:     p.cfg.SecretKey,
	workerSecrets: p.cfg.WorkerSecrets,
}
```

- [ ] **Step 8: Pass secrets from worker.go to Pool**

In `cmd/agentdock/worker.go`, after `agentRunner` construction (line 60), decode the secret key and pass to Pool:

```go
	var secretKey []byte
	if cfg.SecretKey != "" {
		var err error
		secretKey, err = config.DecodeSecretKey(cfg.SecretKey)
		if err != nil {
			return fmt.Errorf("invalid secret_key: %w", err)
		}
		appLogger.Info("Secret key 已載入", "phase", "完成")
	}
```

Then in Pool config construction (line 86-100), add:

```go
	SecretKey:      secretKey,
	WorkerSecrets:  cfg.Secrets,
```

- [ ] **Step 9: Run tests to verify they pass**

Run: `go test ./internal/worker/ -run "TestExecuteJob_Decrypts|TestExecuteJob_NoSecretKey" -v`
Expected: PASS

- [ ] **Step 10: Run all tests**

Run: `go test ./... 2>&1 | tail -30`
Expected: all tests PASS

- [ ] **Step 11: Commit**

```bash
git add internal/worker/executor.go internal/worker/pool.go internal/worker/pool_test.go cmd/agentdock/worker.go
git commit -m "feat(worker): decrypt, merge, and inject secrets per-job (#56)"
```

---

### Task 9: Full Integration Smoke Test

**Files:**
- All modified files (no new files)

- [ ] **Step 1: Run the full test suite**

Run: `go test ./... -count=1 2>&1 | tail -40`
Expected: all packages PASS

- [ ] **Step 2: Build check**

Run: `go build ./cmd/agentdock/`
Expected: builds successfully

- [ ] **Step 3: Verify no secrets in git diff**

Run: `git diff HEAD~8 --stat`
Expected: see all expected files changed, no unexpected files

- [ ] **Step 4: Final commit (if any fixups needed)**

```bash
git add -A
git commit -m "fix: integration fixups for secret passing (#56)"
```
