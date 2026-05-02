# GitHub App Auth — Implementation Plan

> **For agentic workers:** Use `agent-skills:incremental-implementation` slice-by-slice with `agent-skills:test-driven-development` per AC. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship single-installation GitHub App auth alongside PAT, with App-priority dispatch, per-job MintFresh, cross-installation PAT fallback, and zero worker host changes.

**Spec:** `docs/superpowers/specs/2026-05-02-github-app-auth-design.md` (commit `91f0be4`)

**Branch:** `feat/212-github-app-auth`

**Issue:** [#212](https://github.com/Ivantseng123/agentdock/issues/212)

---

## Architecture Decisions

- **Vertical slicing.** Each task ends with the build green and tests passing. No long-lived broken state.
- **Foundation first (Phase 1) is purely additive.** New files only — no signature changes — so the rest of the codebase keeps building.
- **Signature breaks bundled with caller fixes.** When a constructor signature flips (Phase 2), the same task updates `app/app.go` so `go build ./...` stays green.
- **App-only logic stays in `app/githubapp/`.** `shared/` and `worker/` never import it; the `tokenTransport` lives in `shared/github/` but accepts a `tokenFn`, so it has no `app/githubapp/` dep. Verified by `test/import_direction_test.go` (AC-16).
- **PAT-mode equivalence.** `staticPATSource.MintFresh()` returns the PAT verbatim, so dispatch-path unification (T10) does not change PAT behavior byte-for-byte (AC-1).

---

## Task List

### Phase 1: Foundation (additive — no signature changes)

#### Task 1: `tokenTransport` (http.RoundTripper) in `shared/github/`

**Description:** Add a per-request `Authorization` header injector that reads the token from a `tokenFn func() (string, error)` on every outbound HTTP call. This is the seam that lets app-side gh clients rotate tokens without rebuilding the client.

**Acceptance criteria:**
- [ ] `tokenTransport` implements `http.RoundTripper`; constructor takes `tokenFn` + delegate `http.RoundTripper` (defaults to `http.DefaultTransport` when nil)
- [ ] Each `RoundTrip` call invokes `tokenFn()` and sets `Authorization: Bearer <token>`; if `tokenFn` errors, the request fails before going on the wire
- [ ] Empty token → no `Authorization` header set (lets caller decide; matches existing `gh.Client.WithAuthToken("")` semantics)
- [ ] Does **not** mutate the input `*http.Request` (clones before setting header — std lib RoundTripper contract)

**Verification:**
- [ ] `go test ./shared/github/... -run TokenTransport`
- [ ] Test: every request gets fresh token (rotate `tokenFn` between calls, assert header changes)
- [ ] Test: `tokenFn` error propagates as `RoundTrip` error
- [ ] `go build ./...` passes

**Dependencies:** None

**Files likely touched:**
- `shared/github/transport.go` (new)
- `shared/github/transport_test.go` (new)

**Estimated scope:** S

**Mapped ACs:** AC-9 (per-request token injection)

---

#### Task 2: Config schema, env overrides, logging component

**Description:** Add the App config struct to `app/config`, the three env overrides, and the new logging component constant. Pure additions; nothing reads these yet.

**Acceptance criteria:**
- [ ] `GitHubAppConfig{AppID, InstallationID, PrivateKeyPath}` added to `app/config/config.go`; `GitHubConfig` gains `App GitHubAppConfig \`yaml:"app"\``
- [ ] `IsConfigured()` returns true iff all three fields set; partial → false (preflight will catch in T13)
- [ ] `EnvOverrideMap()` in `app/config/env.go` includes `GITHUB_APP_APP_ID`, `GITHUB_APP_INSTALLATION_ID`, `GITHUB_APP_PRIVATE_KEY_PATH`
- [ ] `worker/config/` not touched
- [ ] `shared/logging/components.go` adds `CompGitHubApp = "githubapp"`

**Verification:**
- [ ] `go test ./app/config/...` passes (existing + new IsConfigured table)
- [ ] `go build ./...` passes
- [ ] `grep -r "GITHUB_APP" worker/` returns nothing

**Dependencies:** None

**Files likely touched:**
- `app/config/config.go`
- `app/config/env.go`
- `app/config/config_test.go` (existing — extend)
- `shared/logging/components.go`

**Estimated scope:** S

**Mapped ACs:** AC-4 (preflight surface; struct is the prerequisite)

---

#### Task 3: `app/githubapp/` — JWT signing + mint client

**Description:** New `app/githubapp/` package with RS256 JWT signing and the `POST /app/installations/{id}/access_tokens` HTTP client. Exposes `signJWT(privateKey, appID, now) (string, error)` and `postInstallationToken(httpClient, jwt, installationID) (token, expiresAt, error)`. No public TokenSource yet.

**Acceptance criteria:**
- [ ] `jwt.go`: RS256, `iss=appID`, `iat=now-60s`, `exp=now+10min`; injects `now func() time.Time` for tests
- [ ] `mint.go`: posts to `https://api.github.com/app/installations/{id}/access_tokens` with `Authorization: Bearer <jwt>` + `Accept: application/vnd.github+json` + `X-GitHub-Api-Version: 2022-11-28`; parses `{token, expires_at}` (RFC3339)
- [ ] 200 → returns token + parsed `time.Time`
- [ ] 401 → typed error `errInvalidAppCredentials`
- [ ] 404 → typed error `errInstallationNotFound`
- [ ] 5xx → typed error `errMintTransient` (T13 retry policy hooks here)
- [ ] Other 4xx → wrapped error with status + body excerpt
- [ ] Uses `jwt-go` v5 if absent (`app/go.mod` add)
- [ ] No exported symbols outside `githubapp` yet

**Verification:**
- [ ] `go test ./app/githubapp/...` JWT round-trip (sign with priv, verify with pub)
- [ ] `httptest` server covers 200/401/404/5xx/malformed-JSON branches
- [ ] `go build ./app/...` passes

**Dependencies:** Task 2 (no — JWT/mint don't read config; package can be standalone)

**Files likely touched:**
- `app/githubapp/jwt.go` (new)
- `app/githubapp/jwt_test.go` (new)
- `app/githubapp/mint.go` (new)
- `app/githubapp/mint_test.go` (new)
- `app/go.mod` (maybe + go.sum)

**Estimated scope:** M

**Mapped ACs:** prerequisite for AC-7 / AC-8

---

#### Task 4: `TokenSource` + `staticPATSource` + `appInstallationSource`

**Description:** Add the public `TokenSource` interface, the trivial `staticPATSource`, and the cache-aware `appInstallationSource` with mutex-serialized `Get()` / `MintFresh()`. Exposes `now func() time.Time` injection point.

**Acceptance criteria:**
- [ ] `TokenSource` interface: `Get() (string, error)`, `MintFresh() (string, error)`
- [ ] `staticPATSource{token string}` — both methods return same token
- [ ] `appInstallationSource` with `appID, installationID, privateKey, httpClient, logger, now, mu, cached, expiresAt`
- [ ] `Get()` returns cached when `expiresAt - now() >= 50min`; otherwise calls `mintLocked`
- [ ] `MintFresh()` always calls `mintLocked` and updates cache
- [ ] `mintLocked` reuses `signJWT` + `postInstallationToken` from T3
- [ ] Concurrent `Get()` + `MintFresh()` mutex-serialized — `go test -race` passes

**Verification:**
- [ ] `go test ./app/githubapp/... -race -run Source` covers: cache hit, cache expiry, MintFresh updates cache, mutex serialization
- [ ] `go build ./...` passes

**Dependencies:** Task 3

**Files likely touched:**
- `app/githubapp/source.go` (new — TokenSource + staticPATSource + appInstallationSource)
- `app/githubapp/source_test.go` (new)

**Estimated scope:** M

**Mapped ACs:** AC-9, AC-10

---

#### Task 5: `NewFromConfig` factory

**Description:** Dispatcher that picks `appInstallationSource` (App-priority) or `staticPATSource` based on config. Errors when neither is configured.

**Acceptance criteria:**
- [ ] `NewFromConfig(cfg config.GitHubConfig, logger *slog.Logger) (TokenSource, error)`
- [ ] `cfg.App.IsConfigured()` → load `PrivateKeyPath`, parse PEM, build `appInstallationSource`; cfg parse errors wrapped
- [ ] Else `cfg.Token != ""` → `staticPATSource`
- [ ] Else → error `"github auth not configured: set github.token or github.app.*"`
- [ ] Partial App config does NOT silently fall to PAT (preflight T13 catches; factory simply uses `IsConfigured()`)

**Verification:**
- [ ] Table test: 4 cases (App full, PAT only, both, neither, partial App)
- [ ] PEM parse error path tested with bad fixture
- [ ] `go test ./app/githubapp/... -run Factory` passes

**Dependencies:** Tasks 2, 4

**Files likely touched:**
- `app/githubapp/factory.go` (new)
- `app/githubapp/factory_test.go` (new)
- `app/githubapp/testdata/test_key.pem` (new fixture)

**Estimated scope:** S

**Mapped ACs:** AC-1, AC-2, AC-3 (mode selection)

---

### Checkpoint: Phase 1 (Foundation)

- [ ] `go build ./...` passes
- [ ] `go test ./app/githubapp/... ./shared/github/... ./app/config/... -race` passes
- [ ] `app/githubapp/` is a complete unit-tested package; no caller wires it yet
- [ ] `test/import_direction_test.go` passes (no new violations)

---

### Phase 2: Wire-up (signature changes bundled with caller fixes)

#### Task 6: Three app-only clients switch to `tokenFn` + `tokenTransport`

**Description:** Flip `IssueClient`, `RepoDiscovery`, and `pr.Client` constructor signatures from `string` → `func() (string, error)`, and rebuild internal `gh.Client` with `tokenTransport` so each request rotates. Update the three `app/app.go` call sites in the same task to keep build green.

**Acceptance criteria:**
- [ ] `shared/github/issue.go`: `NewIssueClient(tokenFn func() (string, error), logger *slog.Logger) *IssueClient`
- [ ] `shared/github/discovery.go`: `NewRepoDiscovery(tokenFn func() (string, error), logger *slog.Logger) *RepoDiscovery`
- [ ] `shared/github/pr.go`: `NewClient(tokenFn func() (string, error)) *Client`
- [ ] All three internally wrap `http.Client.Transport = tokenTransport{tokenFn, http.DefaultTransport}`; the underlying `gh.Client` is built without `WithAuthToken` (token comes via transport, not constructor)
- [ ] `app/app.go:79,195,196` pass `source.Get` (`source` built once at startup via T5)
- [ ] Existing tests in `shared/github/issue_test.go` adapted to pass `func() (string, error) { return "tok", nil }`
- [ ] `worker/` builds unchanged (these clients are app-only, not used in worker)

**Verification:**
- [ ] `go build ./...` passes
- [ ] `go test ./shared/github/... ./app/...` passes
- [ ] `go test ./test/... -run TestImportDirection` passes
- [ ] Test (in `issue_test.go` or similar): rotating `tokenFn` between two `httptest` requests changes `Authorization` header

**Dependencies:** Tasks 1, 5

**Files likely touched:**
- `shared/github/issue.go`, `shared/github/discovery.go`, `shared/github/pr.go`
- `shared/github/issue_test.go` (signature update)
- `app/app.go` (3 call sites + add `source := githubapp.NewFromConfig(...)` near top)

**Estimated scope:** M

**Mapped ACs:** AC-9 (cache shared via `source.Get`)

---

#### Task 7: `RepoCache` `tokenFn` variant + `app/app.go` caller

**Description:** Add `NewRepoCacheWithTokenFn` alongside the existing `NewRepoCache`. Old constructor wraps its `string` arg in a closure; internals call `tokenFn()` per fetch/clone. Worker stays on the old constructor; app-side switches to the new one. Add the rotation healing test.

**Acceptance criteria:**
- [ ] `shared/github/repo.go`:
  - new field `tokenFn func() (string, error)` on `RepoCache`
  - `NewRepoCache(dir, maxAge, githubPAT, logger)` unchanged signature; sets `tokenFn = func() (string, error) { return githubPAT, nil }`
  - new `NewRepoCacheWithTokenFn(dir, maxAge, tokenFn, logger)`
  - all internal call sites that previously read `rc.githubPAT` now call `rc.tokenFn()`
- [ ] `app/app.go:78` switches to `NewRepoCacheWithTokenFn(..., source.Get, githubLogger)`
- [ ] `worker/worker.go:71` unchanged — still uses old ctor with PAT
- [ ] New test `HealsRotatedInstallationTokenInConfig`: mock `tokenFn` returns different tokens across calls, assert `.git/config` does not retain stale token

**Verification:**
- [ ] `go test ./shared/github/...` — existing `StripsTokenFromGitConfig` + `HealsLegacyTokenInConfig` still green
- [ ] New healing test passes
- [ ] `go build ./...` passes

**Dependencies:** Task 6 (so `source` is available in `app.go`)

**Files likely touched:**
- `shared/github/repo.go`
- `shared/github/repo_test.go` (new test)
- `app/app.go` (RepoCache call site)

**Estimated scope:** M

**Mapped ACs:** AC-9, AC-11, AC-15

---

#### Task 8: `AddWorktree` / `Checkout` `token` parameter + worker plumbing

**Description:** Extend `RepoCache.AddWorktree` and `RepoCache.Checkout` with a `token string` parameter so worker-side callers can plumb the per-job token. Update `worker/pool/adapters.go` to pass `mergedSecrets["GH_TOKEN"]`. `worker/agent/runner.go` stays unchanged (AC-12 hard requirement).

**Acceptance criteria:**
- [ ] `AddWorktree(barePath, branch, worktreePath, token string) error`
- [ ] `Checkout(repoPath, branch, token string) error`
- [ ] Internal logic: prefer `token` param when non-empty, else fall back to `rc.tokenFn()` (so app-side callers passing `""` still work; T9 leans on this)
- [ ] `worker/pool/adapters.go` extracts `GH_TOKEN` from merged secrets and plumbs to both calls
- [ ] `worker/agent/runner.go` git diff is empty (AC-12)
- [ ] `app/workflow/...` callers: temporarily pass `""` token to keep build green (T9 finalizes)

**Verification:**
- [ ] `go build ./...` passes
- [ ] `go test ./worker/... ./shared/github/...` passes
- [ ] `git diff worker/agent/runner.go` is empty

**Dependencies:** Task 7

**Files likely touched:**
- `shared/github/repo.go`
- `shared/github/repo_test.go` (signature in tests)
- `worker/pool/adapters.go`
- `app/workflow/issue.go`, `app/workflow/ask.go` (signature compile fix only — T9 finishes)

**Estimated scope:** M

**Mapped ACs:** AC-12

---

#### Task 9: Drop workflow `cfg.Secrets["GH_TOKEN"]` reads

**Description:** Four sites in `app/workflow/issue.go` (lines 701-704, 1042-1045) and `app/workflow/ask.go` (lines 348-352, 580-584) currently read `cfg.Secrets["GH_TOKEN"]` and pass it to `EnsureRepo`. Replace with empty string so `RepoCache` falls through to its `tokenFn` (PAT mode → PAT; App mode → fresh App token). Without this, App-only deployments silently 401 in interactive branch picker.

**Acceptance criteria:**
- [ ] All four sites pass `""` as token to `EnsureRepo` (or whichever helper plumbs it down)
- [ ] No remaining `cfg.Secrets["GH_TOKEN"]` reads in `app/workflow/`
- [ ] In PAT mode integration test: behavior unchanged (RepoCache `tokenFn` returns PAT → same outcome as before)
- [ ] In App-only integration test: branch picker successfully fetches private repo

**Verification:**
- [ ] `grep -n 'cfg.Secrets\["GH_TOKEN"\]' app/workflow/` returns nothing
- [ ] `go test ./app/workflow/...` passes
- [ ] Add integration-style test verifying App-only branch picker uses `tokenFn` path

**Dependencies:** Task 8

**Files likely touched:**
- `app/workflow/issue.go`
- `app/workflow/ask.go`
- `app/workflow/issue_test.go` / `ask_test.go` (extend)

**Estimated scope:** S

**Mapped ACs:** AC-20

---

### Checkpoint: Phase 2 (Wire-up)

- [ ] `go build ./...` passes
- [ ] `go test ./...` passes (all modules)
- [ ] PAT-only e2e (existing suite) byte-for-byte same behavior — AC-1
- [ ] Import direction test passes — AC-16

---

### Phase 3: Dispatch (App mode actually mints per job)

#### Task 10: `buildEncryptedSecrets` helper + `submitJob` per-job fork

**Description:** Extract a helper that forks `cfg.Secrets`, calls `source.MintFresh()`, overlays `GH_TOKEN`, marshals + AES-encrypts, and returns the ciphertext. Replace the inline encrypt block in `submitJob` (lines ~350-368) with a call to it. PAT-mode equivalence: `staticPATSource.MintFresh()` returns the PAT, so the encrypted bundle is identical to today's auto-merge.

**Acceptance criteria:**
- [ ] `buildEncryptedSecrets(cfg *config.Config, source githubapp.TokenSource, secretKey []byte) ([]byte, error)` (location: `app/app.go` or new `app/dispatch.go`)
- [ ] Forks `cfg.Secrets` into a per-job map (size = `len(cfg.Secrets)+1`); never mutates `cfg.Secrets`
- [ ] `MintFresh` failure → returns error; `submitJob` posts slack error and does NOT submit
- [ ] `submitJob` closure replaces inline block with helper call
- [ ] Test: two concurrent `submitJob` invocations with mocked `source` — no shared-map race (`go test -race`)

**Verification:**
- [ ] `go test ./app/... -race` passes
- [ ] Test (table or integration): MintFresh called exactly once per submitJob invocation
- [ ] PAT-mode integration: encrypted bundle byte-equivalent to pre-T10 behavior

**Dependencies:** Task 9 (so wire-up is complete)

**Files likely touched:**
- `app/app.go` (extract helper + call from submitJob)
- `app/app_test.go` or new `app/dispatch_test.go`

**Estimated scope:** M

**Mapped ACs:** AC-7 (token from MintFresh), AC-8 (per-job mint)

---

#### Task 11: Cross-installation `accessibleRepos` set + dispatch-time intercept

**Description:** Extend `appInstallationSource` with an `accessibleRepos map[string]struct{}` populated by `GET /installation/repositories` (full pagination), refreshed at the end of each `mintLocked`. Add `IsAccessible(ownerRepo string) bool`. In `submitJob`, before calling `buildEncryptedSecrets`, check primary-repo accessibility: in-set → mint; not in set + PAT configured → use PAT for that job (warn log); not in set + no PAT → fail dispatch + slack error.

**Acceptance criteria:**
- [ ] `appInstallationSource.accessibleRepos` field + mutex-protected updates
- [ ] `mintLocked` end: paginated `GET /installation/repositories` fetch (per_page=100), populates set as `owner/repo` strings
- [ ] `IsAccessible(ownerRepo)` reads from set under same mutex
- [ ] `staticPATSource.IsAccessible` always returns true (PAT has whatever scope user gave)
- [ ] `submitJob` early-intercept logic:
  - primary in set → call `buildEncryptedSecrets` (which uses `MintFresh`)
  - primary not in set + `cfg.GitHub.Token != ""` → build encrypted secrets with PAT for `GH_TOKEN`; log warn `app not installed at owner=<X>, falling back to PAT`
  - primary not in set + no PAT → fail dispatch + slack post `"App not installed at owner=<X>, install at the org or set github.token"`
- [ ] Ref repo failures during worker fetch produce error message pointing at cross-installation (AC-17)
- [ ] Test: 3 dispatch paths (mint, PAT fallback, fail-loud)

**Verification:**
- [ ] `go test ./app/githubapp/... -run Accessible` covers pagination
- [ ] `go test ./app/...` covers 3 dispatch branches
- [ ] Integration: ref repo not in set produces clear error

**Dependencies:** Task 10

**Files likely touched:**
- `app/githubapp/source.go`
- `app/githubapp/source_test.go`
- `app/app.go` (submitJob early intercept)
- worker-side error message tweak in `worker/pool/adapters.go` if needed for AC-17 specificity

**Estimated scope:** M

**Mapped ACs:** AC-3, AC-17

---

#### Task 12: Retry path uses `buildEncryptedSecrets`

**Description:** `app/bot/retry_handler.go:68` currently reuses `original.EncryptedSecrets`, which carries a token that may be 50min+ stale by retry time. Replace with a call to `buildEncryptedSecrets(cfg, source, secretKey)` so retry jobs get fresh tokens. Mint failure → existing retry-failed slack post path.

**Acceptance criteria:**
- [ ] `retry_handler` no longer assigns `original.EncryptedSecrets` to `newJob.EncryptedSecrets`; calls helper instead
- [ ] Helper failure path: posts existing `:x: 重試失敗: <err>` slack message, does NOT enqueue
- [ ] Test: retry mint counter > 0 (mocked source records calls)

**Verification:**
- [ ] `go test ./app/bot/...` passes including new retry mint test
- [ ] Existing retry behavioral tests still green

**Dependencies:** Task 10

**Files likely touched:**
- `app/bot/retry_handler.go`
- `app/bot/retry_handler_test.go`

**Estimated scope:** S

**Mapped ACs:** AC-8 (retry path also mints)

---

### Checkpoint: Phase 3 (Dispatch)

- [ ] `go build ./...` + `go test ./...` passes
- [ ] AC-7, AC-8, AC-9, AC-10, AC-17 verifiable via tests
- [ ] PAT-mode integration suite still byte-for-byte (AC-1)

---

### Phase 4: Preflight + Polish

#### Task 13: `preflightGitHubApp` (4-way error split + 5xx retry + secret_key + JobTimeout)

**Description:** Extend `app/config/preflight.go:preflightGitHub` with App branch. New `preflightGitHubApp` reads PEM, signs JWT, mints (with §7 retry policy), checks `permissions` from `GET /app/installations/{id}`, populates `accessibleRepos` from `GET /installation/repositories`. Adds `secret_key` requirement when App configured (AC-18) and `JobTimeout > 50min` warn (AC-19).

**Acceptance criteria:**
- [ ] `preflightGitHub` dispatcher checks App branch then PAT branch; both empty → error
- [ ] `preflightGitHubApp`:
  - PEM read/parse error → `"github app private key invalid: <path>: <err>"`
  - mint 401 → `"github app credentials rejected: check github.app.app_id and private_key_path match"`
  - mint 404 → `"github app installation not found: id=<X>; verify github.app.installation_id"`
  - permissions missing → `"github app installation missing required permissions: missing=[X, Y]; expected: Issues:rw, Contents:r, Metadata:r, Pull requests:rw"`
  - 5xx ×3 retries fail → `"github api unavailable during preflight (after 3 retries): <err>; this is an infrastructure issue, not a config issue"`
- [ ] Retry policy on 5xx: 500ms / 1s / 2s; 4xx fail-fast; covers mint + `GET /app/installations/{id}` + `GET /installation/repositories`
- [ ] Permissions validated via `GET /app/installations/{id}` response `permissions` map (4 expected keys: `issues=write`, `contents=read`, `metadata=read`, `pull_requests=write`)
- [ ] `cfg.GitHub.App.IsConfigured() && cfg.SecretKey == ""` → fail with `"github app mode requires secret_key (token cannot cross app/worker boundary unencrypted)"` (AC-18)
- [ ] `cfg.GitHub.App.IsConfigured() && cfg.Queue.JobTimeout > 50*time.Minute` → log warn (don't block) (AC-19)
- [ ] Partial-config (e.g., only `app_id`) → fail with field-specific message (AC-4)
- [ ] On success: log `github app preflight passed, installation_id=<X>, accessible_repos=<N>`
- [ ] Pump the populated `accessibleRepos` into the `source` (preflight runs before dispatch, primes the cache)

**Verification:**
- [ ] Table test in `preflight_test.go` covers all 5 error branches via httptest
- [ ] 5xx retry test: mock returns 503 thrice → 200, asserts 4 calls + retry timing (use deterministic clock)
- [ ] secret_key check tested
- [ ] JobTimeout warn tested (capture log output)
- [ ] Partial-config tested (3 sub-cases: missing app_id, installation_id, key path)

**Dependencies:** Tasks 5, 11 (source + accessibleRepos)

**Files likely touched:**
- `app/config/preflight.go`
- `app/config/preflight_test.go`

**Estimated scope:** M

**Mapped ACs:** AC-2 (preflight pass), AC-4, AC-5, AC-6, AC-18, AC-19

---

#### Task 14: `init` command hint line

**Description:** Add a single non-interactive hint line in `cmd/agentdock/init.go` after the PAT prompt header, pointing at the migration doc. No new prompts, no preflight in init.

**Acceptance criteria:**
- [ ] Hint string: `"  Tip: 改用 GitHub App auth → 見 docs/MIGRATION-github-app.md"` (or similar — aligned with existing init doc style)
- [ ] PAT prompt flow byte-for-byte unchanged (same prompts, same order)
- [ ] Test asserting hint appears in output stream

**Verification:**
- [ ] `go test ./cmd/agentdock/... -run Init` passes
- [ ] `agentdock init app` interactive run prints hint

**Dependencies:** None

**Files likely touched:**
- `cmd/agentdock/init.go`
- `cmd/agentdock/init_test.go`

**Estimated scope:** XS

**Mapped ACs:** AC-14

---

#### Task 15: Migration doc (zh + en)

**Description:** Write `docs/MIGRATION-github-app.md` (Traditional Chinese) and `docs/MIGRATION-github-app.en.md` (English direct translation) covering the 11 topics in spec §11.

**Acceptance criteria:**
- [ ] Both files exist; topic sections 1:1 mapped
- [ ] Topic 10 explicitly states `agent timeout ≤ 50min` recommendation
- [ ] Topic 11 contains:
  - secret_key prerequisite
  - warning about `worker.yaml secrets.GH_TOKEN` overwriting (executor.go:85-91 worker-wins overlay)
  - staging-smoke step: trigger `@bot triage`, verify GitHub UI issue author shows `<app-name>[bot]`
- [ ] Permissions list: `Issues:rw`, `Contents:r`, `Metadata:r`, `Pull requests:rw`
- [ ] Cross-references spec section 4.x for technical detail; doesn't duplicate

**Verification:**
- [ ] Manual review against spec §11 checklist
- [ ] Optional markdown lint (if `markdownlint-cli` config exists)

**Dependencies:** Final task — informed by impl reality

**Files likely touched:**
- `docs/MIGRATION-github-app.md` (new)
- `docs/MIGRATION-github-app.en.md` (new)

**Estimated scope:** M

**Mapped ACs:** AC-13

---

### Checkpoint: Final (Phase 4 + AC sweep)

- [ ] `go build ./...` passes
- [ ] `go test ./... -race` passes
- [ ] `go test ./test/... -run TestImportDirection` passes (AC-16)
- [ ] `git diff worker/agent/runner.go` is empty (AC-12)
- [ ] `git diff worker/pool/adapters.go` is bounded to plumbing only (AC-12)
- [ ] All 20 ACs in spec §12 mapped to a passing test or manual verification step
- [ ] Manual smoke (staging): App-only deployment creates an issue with `<app-name>[bot]` author (AC-7 GitHub UI confirmation)
- [ ] Migration doc reviewed by human

---

## AC Coverage Map

| AC | Tasks |
|----|-------|
| AC-1 (PAT regression) | T6, T7, T9, T10, T12 (each preserves PAT path) + Phase 2/3 checkpoints |
| AC-2 (App-only happy path) | T13 + Phase 4 smoke |
| AC-3 (App + PAT priority) | T11 |
| AC-4 (partial App config) | T13 |
| AC-5 (private key invalid) | T13 |
| AC-6 (mint errors split) | T13 |
| AC-7 (CreateIssue token from MintFresh) | T10 |
| AC-8 (per-job mint + retry mint) | T10, T12 |
| AC-9 (cache shared via tokenFn) | T1, T6, T7 |
| AC-10 (race-free) | T4 |
| AC-11 (rotation healing) | T7 |
| AC-12 (worker zero/bounded change) | T8 + final checkpoint |
| AC-13 (migration doc) | T15 |
| AC-14 (init hint) | T14 |
| AC-15 (existing healing) | T7 (regression) |
| AC-16 (import direction) | Phase 1 + final checkpoint |
| AC-17 (cross-installation ref repo) | T11 |
| AC-18 (App + no secret_key) | T13 |
| AC-19 (JobTimeout warn) | T13 |
| AC-20 (App-only branch picker) | T9 |

---

## Risks and Mitigations

| Risk | Impact | Mitigation |
|------|--------|------------|
| `tokenTransport` breaks gh client behavior (e.g., User-Agent stripping) | Med | T1: clone request, only mutate `Authorization` header; cover with test against real `gh.Client` round-trip |
| `gh.Client.WithAuthToken("")` in T6 + transport path unexpectedly skip auth | Med | Build a request without `WithAuthToken` and verify transport-injected header reaches the wire (httptest assertion) |
| `accessibleRepos` cache stale (50–60min) misses just-installed repo | Low | Acceptable per spec §4.12; user can restart app to force re-mint. Document in migration doc topic 9 |
| PAT-mode `MintFresh()` allocates per-call (returns same string) — perf regression | Low | `staticPATSource.MintFresh` is a single field read; benchmark if visible in flight, but not expected |
| Retry path in T12 doesn't have `cfg` / `source` / `secretKey` in scope | Med | Inject via existing retry handler struct; T12 spec'd to thread these in |
| Migration doc topic 11 too vague for ops handoff | Low | Pair with human reviewer post-T15 before merge |

---

## Open Questions

(none — spec ratified post-grilling, all 11 deltas resolved)

---

## Parallelization

- **Sequential (must):** T1 → T3 → T4 → T5 → T6 → T7 → T8 → T9 → T10 → T11 → T12 → T13
- **Independent of impl chain:** T2 (config schema), T14 (init hint), T15 (migration doc)
- **T2 should run early** — T3/T5 reference `config.GitHubAppConfig`
- **T14 + T15 can be parallel agents** with the impl chain after T13 completes

---

## Verification Summary

Before merge:

- [ ] All 15 tasks ✅
- [ ] All 20 ACs mapped to a passing test or signed manual step
- [ ] All four checkpoints passed
- [ ] `go test ./... -race` clean
- [ ] `go test ./test/... -run TestImportDirection` clean
- [ ] PAT-mode e2e regression suite green (AC-1)
- [ ] Staging smoke: App-only created issue shows `[bot]` author
- [ ] Migration doc reviewed
