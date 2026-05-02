# GitHub App Auth Support (Single Installation, Coexists with PAT)

**Date:** 2026-05-02 (updated 2026-05-02 post-grilling)
**Status:** Design ratified, ready for impl
**Issue:** [#212](https://github.com/Ivantseng123/agentdock/issues/212)
**Open Questions resolution:** Issue [#212 comment](https://github.com/Ivantseng123/agentdock/issues/212#issuecomment-4362634608)
**Branch:** `feat/212-github-app-auth`

---

## 1. Premise / Decision Summary

| Item | Decision |
|------|----------|
| 認證形態 | Single GitHub App + single installation；與 PAT 並存，App 優先 |
| 私鑰位置 | App 端，path-only：`github.app.private_key_path`；env override `GITHUB_APP_PRIVATE_KEY_PATH` |
| 私鑰邊界 | 永遠不過 app/worker boundary，worker 只看到鑄好的 string token |
| Token rotation | App 端 `TokenSource` 介面有 `Get()`（cache）+ `MintFresh()`（bypass cache） |
| Dispatch path | 用 `MintFresh()` 鑄全新 token 注入 `secrets["GH_TOKEN"]`，每 job 滿 60min TTL |
| App 內部 client | 用 `Get()`，共享 cache（剩餘 ≥ 50min 重用，否則 mint） |
| GitHub App 權限 | `Issues:rw` + `Contents:r` + `Metadata:r` + `Pull requests:rw` |
| Cross-installation repo | 走 PAT fallback；無 PAT 時 fail loudly |
| init flow | 不加 App 分支；PAT prompt 結束後加一行 hint 指向 migration doc |
| Token TTL 邊界 | agent timeout > ~50min 屬 user 自訂風險；fail loudly + migration doc 警語；不為此加 worker→app 通道 |
| Out of scope | inline PEM env、webhook、multi-installation routing、worker 持有私鑰 |

## 2. Problem

`app/config/config.go:59-61` 的 `GitHubConfig` 只認一把 `Token string`，全 codebase 從 4 個 client 建構（`app/app.go:78,79,195,196`）到 `worker/worker.go:71` 的 `RepoCache`、`worker/agent/runner.go:152` 注入 agent 的 `GH_TOKEN`，全吃個人 PAT。PAT 模式有三個 PAT 解不掉的痛點：

1. **Bot 身份/審計** — Issue 是 `Bob` 開的，不是 `agentdock[bot]` 開的；離職換人就斷。
2. **細粒度 repo 範圍** — PAT 通常拿到帳號 _所有_ repo 的權限；GitHub App 安裝時可指定 repo + 限定權限。
3. **Org 集中管理** — admin 從 GitHub Settings 一處撤銷/重裝即可。

短期權杖輪替（installation token 1h TTL）**不是**主要驅動力，是吞下的副作用。

## 3. Goals / Non-Goals

### Goals

1. App 模式可獨立於 PAT 上線，使用 GitHub App 認證向 GitHub API + git fetch 操作。
2. 私鑰永不離開 app 進程；worker 端及 agent CLI 路徑零變動。
3. PAT 模式部署 byte-for-byte 不變（regression-free）。
4. 雙模式並存時 App 優先；App 沒安裝在某 owner 時走 PAT fallback。
5. Preflight 在 app 啟動時驗證 App config 可正確 mint installation token + 權限齊全。
6. Migration doc（雙語）完整覆蓋 GitHub Settings 操作步驟與 timeout 約束警語。

### Non-Goals (本 spec 不做)

- Multi-installation routing（同一 App 跨多 org 動態查表）— 單 installation 範圍內無人使用的複雜度。
- Webhook 接收（`InstallationCreated` / `Deleted`）— 偏離產品定位（Slack-driven 結構化工具）。
- Worker 端持有 App 私鑰 — 違反 worker-on-laptop landmine。
- 把 `TokenSource` 推進 `shared/github/` — Worker 永遠只看到字串，shared 不需要這個抽象。
- 背景 token refresh goroutine — Lazy mint with TTL cache 同樣安全且更簡單。
- `github.prefer: app|pat` 開關 — 「兩者皆設則 App 優先」是唯一行為，避免狀態空間膨脹。
- inline PEM via env var — 對齊既有 `github.token` env override 慣例（path 字串 only）；未來真有 K8s 強需求再加。
- `init` 加 GitHub App 分支 — Init 不跑 preflight，收 input 不驗證是負 UX；80% 工作量在 GitHub.com 網頁，init 幫不上。

## 4. Design

### 4.1 Config Schema

`app/config/config.go` 的 `GitHubConfig` 改：

```go
type GitHubConfig struct {
    Token string          `yaml:"token"`
    App   GitHubAppConfig `yaml:"app"`
}

type GitHubAppConfig struct {
    AppID          int64  `yaml:"app_id"`
    InstallationID int64  `yaml:"installation_id"`
    PrivateKeyPath string `yaml:"private_key_path"`
}

func (c GitHubAppConfig) IsConfigured() bool {
    return c.AppID != 0 && c.InstallationID != 0 && c.PrivateKeyPath != ""
}
```

YAML：

```yaml
github:
  token: ghp_xxx                              # 仍支援；兩者皆設則 App 優先
  app:                                        # 新增；App 模式
    app_id: 123456
    installation_id: 7890123
    private_key_path: /etc/agentdock/app-key.pem
```

部分填寫（如只填 `app_id`）→ preflight 報錯，**不啟用 App 模式也不沉默回退**。

`worker/config/config.go` 的 `GitHubConfig` **不**動 — worker 不認 App。

### 4.2 Env Override

新增三條 env override（`app/config/env.go` `EnvOverrideMap()`），對齊既有 `GITHUB_TOKEN → github.token` 慣例：

| ENV var | YAML path |
|---------|-----------|
| `GITHUB_APP_APP_ID` | `github.app.app_id` |
| `GITHUB_APP_INSTALLATION_ID` | `github.app.installation_id` |
| `GITHUB_APP_PRIVATE_KEY_PATH` | `github.app.private_key_path` |

Worker 端 `worker/config/env.go` 不加。

### 4.3 TokenSource 介面

新套件 `app/githubapp/`（subpackage of `app/` module，不另開 `go.mod`）：

```go
package githubapp

type TokenSource interface {
    // Get returns a token, possibly cached. Caller accepts that the
    // returned token may have as little as 50 minutes remaining TTL.
    // Used by app-internal GitHub clients (long-lived).
    Get() (string, error)

    // MintFresh always mints a new installation token, bypassing any
    // cache, and updates the cache with the new value. Used at job
    // dispatch time so workers receive a token close to the full 60min TTL.
    MintFresh() (string, error)
}
```

兩個方法、不用 bool 參數，呼叫端意圖自明。

### 4.4 staticPATSource (PAT 模式)

```go
type staticPATSource struct{ token string }

func (s *staticPATSource) Get() (string, error)        { return s.token, nil }
func (s *staticPATSource) MintFresh() (string, error)  { return s.token, nil }
```

兩方法行為相同；存在的目的是讓 PAT 與 App 兩條路徑共用 4 個 client 建構碼。

> **TokenSource 與 client 的綁定方式**：4 個 app-side client（`IssueClient` / `RepoDiscovery` / `pr.Client` / `RepoCache`）的 underlying gh client 不能在構造時固化 token——否則 cache 失效後 client 仍用舊 token。實作策略：在 `shared/github/` 新增 `tokenTransport`（`http.RoundTripper`），構造時傳入 `tokenFn func() (string, error)`，每個 outgoing HTTP request 由 RoundTripper 即時呼叫 `tokenFn()` 注入 `Authorization` header。`tokenFn` 通常是 `source.Get`（cache-friendly）。`tokenTransport` 住在 `shared/github/` 但不引用 `app/githubapp/`，符合 import direction。

### 4.5 appInstallationSource (App 模式)

```go
type appInstallationSource struct {
    appID          int64
    installationID int64
    privateKey     *rsa.PrivateKey  // loaded from path at construction
    httpClient     *http.Client
    logger         *slog.Logger
    now            func() time.Time // injectable for tests

    mu        sync.Mutex
    cached    string
    expiresAt time.Time             // from GitHub mint response
}

func (s *appInstallationSource) Get() (string, error) {
    s.mu.Lock()
    defer s.mu.Unlock()
    if s.cached != "" && s.expiresAt.Sub(s.now()) >= 50*time.Minute {
        return s.cached, nil
    }
    return s.mintLocked()
}

func (s *appInstallationSource) MintFresh() (string, error) {
    s.mu.Lock()
    defer s.mu.Unlock()
    return s.mintLocked()
}

// mintLocked must be called with s.mu held.
func (s *appInstallationSource) mintLocked() (string, error) {
    jwtStr, err := s.signJWT()
    if err != nil {
        return "", err
    }
    token, expiresAt, err := s.postInstallationToken(jwtStr)
    if err != nil {
        return "", err
    }
    s.cached = token
    s.expiresAt = expiresAt
    return token, nil
}
```

- Mutex 共享：`Get()` 與 `MintFresh()` 都進同一個臨界區，避免 race。
- Cache 在 `MintFresh()` 也會更新 — fresh-mint 順帶讓下一個 `Get()` 受惠。
- 50min 閾值 = 60min TTL − 10min 安全緩衝。
- `now func() time.Time` 注入點為了單測 mock 時間。

### 4.6 JWT 簽章

`app/githubapp/jwt.go`：

| 欄位 | 值 |
|------|-----|
| Algorithm | RS256 |
| `iss` claim | App ID |
| `iat` | now − 60s（GitHub 容忍時鐘漂移） |
| `exp` | now + 10min（GitHub 上限） |

依賴 `github.com/golang-jwt/jwt/v5`（檢查 `app/go.mod` 已有則沿用，否則新增）。

### 4.7 Mint API

`POST https://api.github.com/app/installations/{installation_id}/access_tokens`

| Header | 值 |
|--------|-----|
| `Authorization` | `Bearer <jwt>` |
| `Accept` | `application/vnd.github+json` |
| `X-GitHub-Api-Version` | `2022-11-28` |

Response 200：

```json
{
  "token": "ghs_...",
  "expires_at": "2026-05-02T13:00:00Z"
}
```

`expires_at` parse 為 `time.Time` 後存進 `s.expiresAt`。

### 4.8 Factory

`app/githubapp/factory.go`：

```go
// NewFromConfig returns the appropriate TokenSource for the given config.
// Returns appInstallationSource when App is configured (App 優先);
// otherwise staticPATSource.
// Returns error if neither PAT nor full App config is set.
func NewFromConfig(cfg config.GitHubConfig, logger *slog.Logger) (TokenSource, error)
```

行為：

1. `cfg.App.IsConfigured()` → load private key, parse PEM, return `appInstallationSource`
2. 否則 `cfg.Token != ""` → return `staticPATSource`
3. 否則 → error: `"github auth not configured: set github.token or github.app.*"`

部分填寫的 `github.app.*` 不會掉到 `staticPATSource`；preflight 會先擋住。

### 4.9 App-side Wiring (4 個 Client)

`app/app.go` 4 個建構點全部改吃 `TokenSource`。在 `app.go` 啟動序列建立一個 `source` instance，全 app 進程共用：

```go
source, err := githubapp.NewFromConfig(cfg.GitHub, githubAppLogger)
if err != nil { return err }
```

| 行 | Before | After |
|---|--------|-------|
| 78 | `NewRepoCache(dir, maxAge, cfg.GitHub.Token, log)` | `NewRepoCacheWithTokenFn(dir, maxAge, source.Get, log)` |
| 79 | `NewRepoDiscovery(cfg.GitHub.Token, log)` | `NewRepoDiscovery(source.Get, log)` |
| 195 | `NewIssueClient(cfg.GitHub.Token, log)` | `NewIssueClient(source.Get, log)` |
| 196 | `NewClient(cfg.GitHub.Token)` | `NewClient(source.Get)` |

**Constructor signature 變動**：

- `RepoCache`：保留舊 constructor `NewRepoCache(..., string, ...)`（worker 用）；**新增** `NewRepoCacheWithTokenFn(..., func() (string, error), ...)`。
- `RepoDiscovery`、`IssueClient`、`pr.Client`：app-only，**就地改** signature 從 `string` → `func() (string, error)`。內部用 `tokenTransport` 包 underlying http client，token 經 RoundTripper 每 request 即時注入。所有 caller 在 `app/app.go`，一處改完。

> 為何 RepoCache 雙留：worker 也建構 RepoCache（`worker/worker.go:71`），用 string token 是合理的（無 rotation 需求），兩者並存避免 worker 端被迫包一個無意義 closure。

> **Worker-side `RepoCache` 並非完全零變動**：`AddWorktree` (`shared/github/repo.go:294`) 與 `Checkout` (`shared/github/repo.go:262`) 內部 fallback 使用 `rc.githubPAT`。在 App-only 部署（`worker.yaml` 的 `github.token` 為空）下，這條 fallback 為空字串會對 private repo 401。因此 §4.10 加上：`AddWorktree` 與 `Checkout` signature 加 `token string` 參數，由 caller (`worker/pool/adapters.go`) 從 job 解出 secrets 後 plumb 進來。`worker/agent/runner.go` 不變。

### 4.10 RepoCache token-fn 變體

`shared/github/repo.go`：

```go
// existing — kept for worker (zero change)
func NewRepoCache(dir string, maxAge time.Duration, githubPAT string, logger *slog.Logger) *RepoCache

// new — for app side, supports per-call token rotation
func NewRepoCacheWithTokenFn(dir string, maxAge time.Duration, tokenFn func() (string, error), logger *slog.Logger) *RepoCache
```

內部 `RepoCache` 多一個 `tokenFn func() (string, error)` 欄位；舊建構子內部設 `tokenFn = func() (string, error) { return githubPAT, nil }`。所有 git fetch / clone 操作改從 `tokenFn()` 取最新 token，不再從欄位字串讀。

**`AddWorktree` / `Checkout` signature 擴充**（worker side correctness）：

```go
// before
func (rc *RepoCache) AddWorktree(barePath, branch, worktreePath string) error
func (rc *RepoCache) Checkout(repoPath, branch string) error

// after — caller plumbs per-call token from job secrets
func (rc *RepoCache) AddWorktree(barePath, branch, worktreePath, token string) error
func (rc *RepoCache) Checkout(repoPath, branch, token string) error
```

呼叫端 (`worker/pool/adapters.go`) 從 `mergedSecrets["GH_TOKEN"]` 取 token plumb 進來。`worker/agent/runner.go` 完全不變。

`StripsTokenFromGitConfig` / `HealsLegacyTokenInConfig` healing 邏輯（`shared/github/repo_test.go:466,512`）對新路徑同樣關鍵 — token 是 per-call 取，每次 fetch token 都可能不同，`.git/config` 殘留風險更高，healing 必須通過。

新測試：`HealsRotatedInstallationTokenInConfig`，mock `tokenFn` 模擬 token rotation 場景，驗證跨呼叫 healing 仍然乾淨。

**Workflow caller 不再讀 `cfg.Secrets["GH_TOKEN"]`**：`app/workflow/issue.go:701-704,1042-1045`、`app/workflow/ask.go:348-352,580-584` 四處原本顯式讀取 `cfg.Secrets["GH_TOKEN"]` 並傳入 `EnsureRepo` 作為 perCall 參數，改為傳空字串，由 `RepoCache` 內部 `tokenFn` fallback 處理（PAT 模式 `tokenFn` 回 PAT；App 模式 `tokenFn = source.Get`）。否則 App-only 部署互動 branch picker 會 silently 401。

### 4.11 Dispatch Path：MintFresh

`app/config/defaults.go:359-362` 既有：

```go
if cfg.GitHub.Token != "" {
    if _, exists := cfg.Secrets["GH_TOKEN"]; !exists {
        cfg.Secrets["GH_TOKEN"] = cfg.GitHub.Token
    }
}
```

這段 config-load 階段的 auto-merge 對 App 模式不適用（dispatch 才知道要 mint fresh）。`queue.Job` 沒有 `Secrets map[string]string` 欄位——secrets 流向是 `cfg.Secrets` → `json.Marshal` → `crypto.Encrypt` → `job.EncryptedSecrets`（見 `app/app.go:350-368`）。直接 mutate `cfg.Secrets["GH_TOKEN"]` 在 multi-goroutine dispatch 下會 race。

新流程：

- **Config 階段（不變）**：PAT 模式仍走原 auto-merge（給 inmem 模式 fallback；雖然 inmem 已 retire，本 spec 不動，屬 orthogonal cleanup）。
- **Dispatch 階段（新增）**：在 `app/app.go` `submitJob` closure 內，於既有的「encrypt secrets」block (line 350-368) 之前 fork 一份 per-job secrets map，於該 copy 上覆寫 `GH_TOKEN`，再 marshal/encrypt：

  ```go
  // 抽 helper：buildEncryptedSecrets(cfg, source, secretKey) ([]byte, error)
  perJobSecrets := make(map[string]string, len(cfg.Secrets)+1)
  for k, v := range cfg.Secrets { perJobSecrets[k] = v }
  token, err := source.MintFresh()
  if err != nil {
      // fail loud：post slack error，不 submit
      return err
  }
  perJobSecrets["GH_TOKEN"] = token
  secretsJSON, _ := json.Marshal(perJobSecrets)
  encrypted, _ := crypto.Encrypt(secretKey, secretsJSON)
  job.EncryptedSecrets = encrypted
  ```

  兩種模式都吃 `MintFresh()`；PAT 模式下 `MintFresh()` 直接回 PAT 字串，行為等價於原本的 auto-merge。`buildEncryptedSecrets` helper 由 `submitJob` 與 retry path（§4.11.1）共用。

### 4.11.1 Retry Path：也走 MintFresh

`app/bot/retry_handler.go:68` 目前直接複用 `original.EncryptedSecrets`：

```go
EncryptedSecrets: original.EncryptedSecrets,
```

App 模式下 installation token 60min TTL，原 job 失敗後若用戶 50min+ 才按 retry，retry 帶的 token 已過期 → worker 401。

修法：retry handler 改呼叫 `buildEncryptedSecrets(cfg, source, secretKey)` 重新 mint 並加密，**不**複用原 bundle。retry mint 失敗時走既有 retry-failed slack post 路徑。

### 4.12 Cross-installation Fallback

App 沒安裝在 repo 的 owner 時，mint 出的 token 對該 repo 無權限。Fallback 設計：

**機制：Preflight + dispatch-time 檢查**

1. Preflight 階段呼叫 `GET /installation/repositories`（**full pagination**，per_page=100），取得 App 可存取的 `owner/repo` 集合，cache 在 `appInstallationSource.accessibleRepos`（by-repo，非 by-owner，避免 owner 部分 selected 時的 false positive）。
2. 每次 mint 後（`mintLocked()` 結束時）刷新此 cache（成本：1+ 次 HTTP，與 mint 同 batch）。Cache 自然每 50–60min 刷新一次，不加背景 goroutine。
3. **Dispatch-time 早攔**：在 `app/app.go` `submitJob` closure 內，於 `buildEncryptedSecrets` 之前先查 `source.IsAccessible(owner/repo)`：
   - 若 primary 在 set 內：呼叫 `source.MintFresh()` 作為 token；
   - 若 primary 不在 set 且 `cfg.GitHub.Token != ""`：用 PAT 作為該 job 的 `GH_TOKEN`，log warn 「app not installed at owner=X, falling back to PAT」；
   - 若 primary 不在 set 且無 PAT：dispatch 失敗（不 submit），slack post 「App not installed at owner=X, install at the org or set github.token」。

**多 repo job（`Job.RefRepos`）的 token 政策**：

同 job 內 primary + ref repos 共用一個 `GH_TOKEN`。Fallback 決策由 primary repo 觸發（primary 不在 set → 整 job 走 PAT；primary 在 set → 整 job 用 App token，個別 ref 無權限的話 git fetch 自然失敗）。當 ref repo 在 App set 外且 primary 在 set 內時，worker fetch 失敗的錯訊應明確指向 cross-installation 場景（建議用戶將 primary 也轉到 PAT-涵蓋的 owner，或將 App 也安裝至 ref repo 的 owner）。

> 為何不做 per-repo token：`Job.Secrets` 是 `map[string]string`，要表達 per-repo 需擴成 nested map 或多 secret keys，blast radius 過大；且實務上 ref repos 常與 primary 同 org，共用 token 命中率高。

### 4.13 Preflight 驗證

`app/config/preflight.go:134-174` 既有 `preflightGitHub` 改：

```go
func preflightGitHub(cfg *Config, interactive bool, prompted map[string]any) error {
    if cfg.GitHub.App.IsConfigured() {
        if err := preflightGitHubApp(cfg.GitHub.App); err != nil {
            return err
        }
    }
    if cfg.GitHub.Token != "" {
        // existing PAT validation (unchanged)
    }
    if !cfg.GitHub.App.IsConfigured() && cfg.GitHub.Token == "" {
        return errors.New("github auth not configured: set github.token or github.app.*")
    }
    return nil
}

func preflightGitHubApp(app GitHubAppConfig) error {
    // 1. Read private_key_path → parse PEM → check is RSA private key
    // 2. Sign JWT with RS256
    // 3. POST /app/installations/{id}/access_tokens (retry per §7 policy on 5xx)
    // 4. GET /app/installations/{id} (auth: JWT) — 驗 permissions map vs expected 4
    // 5. GET /installation/repositories (auth: installation token, full pagination) — 填 accessibleRepos cache
    return nil
}
```

**Retry policy 對齊**：步驟 3、4、5 對 5xx / dial timeout 套用 §7 mint retry policy（3 次 / 500ms / 1s / 2s）；對 4xx 立即 fail（配置錯誤無從 retry）。

**權限檢查走 `GET /app/installations/{id}`**：response 含 `permissions` map，可比對 4 個 expected key（`issues:write`, `contents:read`, `metadata:read`, `pull_requests:write`）。`/installation/repositories` 只證明 metadata read，不夠。

四種錯誤分流（mapping issue body 「待驗證的假設」第 5 條）：

| 故障 | 訊息 |
|------|-------|
| `private_key_path` 不存在 / 不是 RSA PEM | `"github app private key invalid: <path>: <err>"` |
| App ID 錯（mint 端 401） | `"github app credentials rejected: check github.app.app_id and private_key_path match"` |
| Installation ID 錯（mint 端 404） | `"github app installation not found: id=<X>; verify github.app.installation_id"` |
| Installation 缺權限之一 | `"github app installation missing required permissions: missing=[X, Y]; expected: Issues:rw, Contents:r, Metadata:r, Pull requests:rw"` |
| Mint / metadata / list_repos 5xx 連續 3 次 fail | `"github api unavailable during preflight (after 3 retries): <err>; this is an infrastructure issue, not a config issue"` |

部分填寫（缺欄位）的偵測在 `preflightGitHub` 入口先檢查並回明確訊息。

**`secret_key` 隱性需求**：App 模式下 installation token 透過 `EncryptedSecrets` 跨 app/worker boundary，沒 `cfg.SecretKey` 就過不去。Preflight 加：

```go
if cfg.GitHub.App.IsConfigured() && cfg.SecretKey == "" {
    return errors.New("github app mode requires secret_key (token cannot cross app/worker boundary unencrypted)")
}
```

**Agent timeout 邊界 warn**（不阻擋）：

```go
if cfg.GitHub.App.IsConfigured() && cfg.Queue.JobTimeout > 50*time.Minute {
    prompt.Warn("queue.job_timeout=%s exceeds GitHub App installation token TTL boundary (50min); long jobs may hit 401 mid-run. See docs/MIGRATION-github-app.md.",
        cfg.Queue.JobTimeout)
}
```

### 4.14 init Flow Hint

`cmd/agentdock/init.go:240` 後加一行 hint（不加 App prompt 分支）：

```go
fmt.Fprintln(prompt.Stderr, "  GitHub token (ghp_... or github_pat_...):")
fmt.Fprintln(prompt.Stderr, "  Tip: 改用 GitHub App auth → 見 docs/MIGRATION-github-app.md")
tok := prompt.Hidden("Token: ")
```

兩行，零複雜度。Init 不跑 preflight，所以 hint 純文字導引，不嘗試驗證 App config。

## 5. Data Flow

```
config.yaml
  github.token              (PAT fallback)
  github.app.app_id
  github.app.installation_id
  github.app.private_key_path
                          │
                          ▼
              ┌──────────────────────────────────┐
              │ TokenSource (app/githubapp/)     │
              │                                  │
              │ Get()        → cached (≥ 50min)  │
              │ MintFresh()  → no cache, fresh   │
              └────────┬──────────────┬──────────┘
                       │              │
                 Get() │              │ MintFresh()
                       ▼              ▼
              4 app-internal     secrets["GH_TOKEN"]
              clients            (job dispatch)
              (RepoCache,               │
               IssueClient,             ▼
               RepoDiscovery,      Redis queue
               generic Client)          │
                                        │
                         ── app / worker boundary ──
                                        │
                                        ▼
                                  Worker (zero change)
                                  reads secrets
                                  → agent CLI env
                                    GH_TOKEN=string
```

## 6. Backward Compatibility

| Scenario | Behavior |
|----------|----------|
| 只設 `github.token`，沒設 `github.app.*` | 全部用 PAT；行為與當前 byte-for-byte 一致 |
| 沒設 `github.token`，設了 `github.app.*` | 全部用 App；cross-installation repo 無 fallback → fail loudly |
| 兩者都設 | App 優先；cross-installation repo 走 PAT fallback |
| 兩者都沒設 | preflight 啟動失敗 |
| `github.app.*` 部分填寫（缺欄位） | preflight 啟動失敗，錯訊指明缺哪個欄位 |
| Worker `cfg.GitHub.Token` 仍可設（worker git fetch 用） | 不變；worker 不認 `github.app.*` |
| 既有 `init` 流程 | 加一行 hint，PAT prompt 流程不變 |

## 7. Error Handling

| 故障點 | 處理 |
|--------|-------|
| Mint API 5xx | Retry up to 3 times with exponential backoff（500ms / 1s / 2s）；fail-loud 後傳遞。**同 policy 套用至 preflight 的 mint / `GET /app/installations/{id}` / `GET /installation/repositories`** |
| Mint API 4xx | 不 retry；映射到分流訊息（見 §4.13） |
| Private key parse 失敗 | Fatal at startup（preflight） |
| Token 在 in-flight 過期（agent 跑超過 ~50min） | Worker GitHub API 回 401 → job 失敗 → migration doc 警語：建議 agent timeout ≤ 50min |
| Cross-installation repo 無 PAT fallback | Fail at dispatch（不投 job）；錯訊「App 未安裝於 owner=X，請 admin 安裝至該 org 或設定 github.token」 |
| Concurrent `Get()` + `MintFresh()` | mutex 序列化，無 race |

## 8. Logging / Observability

對齊 `shared/logging/GUIDE.md` component 慣例：

```go
logger := logging.ComponentLogger(slog.Default(), logging.CompGitHubApp)
```

新 `logging.CompGitHubApp = "githubapp"` 加進 `shared/logging/components.go`。

關鍵 log 點：

| Phase | Level | 訊息 |
|-------|-------|------|
| `mint` | Info | `installation token minted, expires_at=<T>, source=fresh|cache_miss` |
| `cache_hit` | Debug | `installation token cache hit, remaining=<min>m` |
| `fallback` | Warn | `app not installed at owner=<X>, falling back to PAT` |
| `fallback_no_pat` | Error | `app not installed at owner=<X> and no PAT configured` |
| `preflight_pass` | Info | `github app preflight passed, installation_id=<X>, accessible_repos=<N>` |

無新 metric（YAGNI；現有 prometheus 框架可在後續 PR 加 mint count / cache hit rate / fallback count，本 spec 不做）。

## 9. Testing

| Test | 範圍 | File |
|------|------|------|
| JWT 簽章 round-trip（sign + verify） | unit | `app/githubapp/jwt_test.go` |
| Mint API 200 / 401 / 404 / 5xx 分流 | unit (httptest) | `app/githubapp/mint_test.go` |
| `appInstallationSource.Get()` cache hit (≥ 50min) 回 cached | unit | `app/githubapp/source_test.go` |
| `appInstallationSource.Get()` cache 過期觸發 mint | unit | `app/githubapp/source_test.go` |
| `appInstallationSource.MintFresh()` bypass cache 並更新 cache | unit | `app/githubapp/source_test.go` |
| Mutex：併發 `Get()` + `MintFresh()` 不 race | unit (`-race`) | `app/githubapp/source_test.go` |
| `staticPATSource` Get/MintFresh 都回同字串 | unit | `app/githubapp/source_test.go` |
| `NewFromConfig` 模式判斷（App 優先 / PAT-only / both empty / partial app） | unit | `app/githubapp/factory_test.go` |
| `RepoCache` 新建構子（tokenFn）每次 fetch 用最新 token | unit | `shared/github/repo_test.go` |
| `RepoCache` healing 對 rotation 場景仍通過（新測試） | unit | `shared/github/repo_test.go` |
| Preflight 4 種 App 錯誤分流 | unit (httptest) | `app/config/preflight_test.go` |
| Preflight 部分填寫（缺欄位）錯訊明確 | unit | `app/config/preflight_test.go` |
| Cross-installation fallback：App 失敗時用 PAT | integration | `app/workflow/.../*_test.go` |
| Cross-installation 無 PAT 時 fail loudly | integration | 同上 |
| Dispatch 確實呼叫 `MintFresh()`（每 job mint 一次） | integration | `app/workflow/.../*_test.go` |
| Retry path 也呼叫 `MintFresh()`（retry job token 為 fresh） | integration | `app/bot/retry_handler_test.go` |
| App-only 模式互動 branch picker 不靠 `cfg.Secrets["GH_TOKEN"]` | integration | `app/workflow/issue_test.go` / `ask_test.go` |
| Preflight `secret_key` 缺漏 → fail | unit | `app/config/preflight_test.go` |
| Preflight `JobTimeout > 50min` → log warn 不阻擋 | unit | 同上 |
| Preflight 5xx infra error 訊息明確區分基礎設施 vs 配置 | unit (httptest) | 同上 |
| `tokenTransport` 每 request 從 tokenFn 注入 Authorization | unit | `shared/github/transport_test.go` |
| Worker 端零變動 regression：PAT 模式 e2e（既有 e2e suite） | e2e | 沿用 |
| `init` PAT prompt 仍然只 prompt PAT，hint 字串出現 | unit | `cmd/agentdock/init_test.go` |
| Import direction 不破壞（worker 不引 `app/githubapp/`） | static | `test/import_direction_test.go` |

## 10. Files Changed

| File | Change |
|------|--------|
| `app/githubapp/source.go` | **New** — TokenSource interface + appInstallationSource |
| `app/githubapp/static.go` | **New** — staticPATSource |
| `app/githubapp/jwt.go` | **New** — RS256 signing |
| `app/githubapp/mint.go` | **New** — POST /access_tokens client |
| `app/githubapp/factory.go` | **New** — NewFromConfig dispatcher |
| `app/githubapp/*_test.go` | **New** — 見 §9 |
| `app/go.mod` | 加 `github.com/golang-jwt/jwt/v5`（若 absent） |
| `app/config/config.go` | 加 `GitHubAppConfig` + `IsConfigured()`；`GitHubConfig.App` 子欄位 |
| `app/config/env.go` | 加 3 條 env override |
| `app/config/preflight.go` | `preflightGitHub` 加 App 分支；新 `preflightGitHubApp` |
| `app/app.go` | 4 個 client 建構改吃 `source.Get`；啟動序列建 `source`；`submitJob` closure 改用 `buildEncryptedSecrets(cfg, source, secretKey)` helper（per-job secrets fork + dispatch-time mint + cross-installation 早攔） |
| `app/bot/retry_handler.go` | retry path 改用 `buildEncryptedSecrets`，**不**複用 `original.EncryptedSecrets` |
| `app/workflow/issue.go` | 四處 `cfg.Secrets["GH_TOKEN"]` 讀取改傳空字串給 `EnsureRepo`，由 `RepoCache` 內部 `tokenFn` fallback 處理 |
| `app/workflow/ask.go` | 同上 |
| `shared/github/repo.go` | 加 `NewRepoCacheWithTokenFn`；內部 `tokenFn` 欄位；`AddWorktree` 與 `Checkout` signature 加 `token string` 參數 |
| `shared/github/repo_test.go` | 加 token rotation 場景的 healing 測試 |
| `shared/github/transport.go` | **New** — `tokenTransport`（http.RoundTripper）每 request 從 tokenFn 注入 Authorization header |
| `shared/github/issue.go` | constructor signature 改 `string` → `func() (string, error)`；內部用 `tokenTransport` 包 http client |
| `shared/github/discovery.go` | constructor signature 改 `string` → `func() (string, error)`；內部用 `tokenTransport` 包 http client |
| `shared/github/pr.go` | constructor signature 改 `string` → `func() (string, error)`；內部用 `tokenTransport` 包 http client |
| `worker/pool/adapters.go` | 從 `mergedSecrets["GH_TOKEN"]` plumb token 進 `AddWorktree` / `Checkout`（worker/agent/runner.go 不變） |
| `shared/logging/components.go` | 加 `CompGitHubApp` 常數 |
| `cmd/agentdock/init.go` | PAT prompt 後加一行 hint（line 240 附近） |
| `docs/MIGRATION-github-app.md` | **New** — 中文 migration |
| `docs/MIGRATION-github-app.en.md` | **New** — 英文 migration |
| `worker/**` | 零變動（驗證點：git diff 必須無 worker/ 修改） |

## 11. Migration Doc Outline (`docs/MIGRATION-github-app.md`)

本 spec 不展開內容，僅約束 migration doc 必須涵蓋以下 11 個主題：

1. 為何切到 GitHub App（PAT 三痛點 recap）
2. 在 GitHub.com 建立 GitHub App 的步驟（含螢幕截圖位置 placeholder）
3. 設定 4 個 repository permissions：`Issues:rw` / `Contents:r` / `Metadata:r` / `Pull requests:rw`
4. 安裝 App 到 org / 個人帳號
5. 產生並下載 private key（PEM）
6. 從 installation URL 抄下 `installation_id`
7. 寫進 config（YAML 與 env vars 兩種範例）
8. 驗證：跑 `agentdock app` 看 preflight 通過
9. PAT 並存 vs 純 App 部署的決策（cross-installation repo 場景）
10. **約束警語：agent timeout 建議 ≤ 50min（避免 token TTL 邊界）**
11. 從 PAT 切換到 App 的步驟：
    - **先確認 `secret_key` 已設**（App 模式硬性要求；否則 preflight fail）
    - 警告：**不要在 `worker.yaml` `secrets.GH_TOKEN` 設值**——會 overwrite App 鑄造的 token（`worker/pool/executor.go` workerSecrets overlay 是「worker wins」）
    - 設 App、preflight 過、staging smoke：手動觸發一次 `@bot triage`，到 GitHub UI 確認 issue author 為 `<app-name>[bot]`
    - 確認後再考慮拿掉 PAT

英文版 `.en.md` 為直譯，主題對應 1:1。

## 12. Acceptance Criteria

實作完成需通過下列檢核（依賴關係：AC-1 是 regression baseline，AC-2~AC-7 是 happy paths，AC-8~AC-11 是邊界）：

- [ ] **AC-1** PAT-only 部署（`github.app.*` 全空）：所有 e2e 測試通過，行為 byte-for-byte 一致
- [ ] **AC-2** App-only 部署（`github.token` 空）：preflight 過、issue 建立成功、PR review 提交成功
- [ ] **AC-3** App + PAT 並存：preflight 過、App 為主、cross-installation repo 走 PAT
- [ ] **AC-4** 任一個 App config 欄位缺漏 → preflight 啟動失敗，錯訊指明缺哪個欄位
- [ ] **AC-5** 私鑰路徑無效 / PEM 格式錯誤 → preflight 失敗，錯訊指明
- [ ] **AC-6** App ID / Installation ID / 權限不足 → preflight 失敗，錯訊分流（4 種 4xx + 1 種 5xx infra error；5xx 訊息明確區分「基礎設施 vs 配置」）
- [ ] **AC-7** Dispatch 觸發後 `IssueClient.CreateIssue` 帶的 token 來自 `appInstallationSource.MintFresh()`（installation token `ghs_...`），非 `cfg.GitHub.Token`（automated-verifiable surrogate；GitHub UI `[bot]` author 由 staging smoke 手動驗）
- [ ] **AC-8** Dispatch path 確實呼叫 `MintFresh()`（每 job 拿到新 token）— 透過 mint API 計次驗證；**retry path 也呼叫 `MintFresh()`**，retry job token 為 fresh 而非複用原 `EncryptedSecrets`
- [ ] **AC-9** App 內部 4 個 client 共享 cache：app 啟動後連續操作數秒不應重複 mint；client 內部用 `tokenTransport` 每 request 從 `source.Get` 取 token，cache 命中時零額外 mint
- [ ] **AC-10** 模擬 token 過期 + 併發（`Get()` + `MintFresh()` race）`-race` 測試通過
- [ ] **AC-11** RepoCache token rotation 場景下 `.git/config` 不殘留舊 token（healing 通過）
- [ ] **AC-12** `worker/agent/runner.go` 完全零變動（git diff 驗證）；`worker/pool/adapters.go` 變動限於從 `mergedSecrets` plumb token 進 `AddWorktree` / `Checkout`；`shared/github/repo.go` 變動限於 `AddWorktree` / `Checkout` signature 擴充
- [ ] **AC-13** Migration doc 雙語（中/英）齊全；涵蓋 §11 11 條主題
- [ ] **AC-14** `init` 互動式流程仍 prompt PAT；輸出含 hint 字串指向 migration doc
- [ ] **AC-15** 既有 token healing 測試（`StripsTokenFromGitConfig`、`HealsLegacyTokenInConfig`）全綠
- [ ] **AC-16** `test/import_direction_test.go` 通過：worker / shared 不引 `app/githubapp/`
- [ ] **AC-17** Cross-installation：primary 在 App set 內、ref 在 set 外時，worker fetch 失敗錯訊明確指向「App 未涵蓋 ref repo」+ 建議（轉 primary 至 PAT-涵蓋 owner / 將 App 安裝至 ref owner）
- [ ] **AC-18** App configured + `cfg.SecretKey == ""` → preflight fail，錯訊明確指明 token 無法跨 boundary
- [ ] **AC-19** App configured + `cfg.Queue.JobTimeout > 50*time.Minute` → preflight log warn（不阻擋啟動），訊息指向 migration doc
- [ ] **AC-20** App-only 模式互動 branch picker 不靠 `cfg.Secrets["GH_TOKEN"]`；workflow 傳空字串給 `EnsureRepo`，由 `RepoCache` `tokenFn` fallback 取得 App token

## 13. References

- Issue: [#212](https://github.com/Ivantseng123/agentdock/issues/212)
- Open Questions resolution comment: [issuecomment-4362634608](https://github.com/Ivantseng123/agentdock/issues/212#issuecomment-4362634608)
- Related spec: [Secret passing (2026-04-16)](./2026-04-16-app-worker-secrets-design.md) — 既有 `secrets["GH_TOKEN"]` injection 機制，本 spec 在其上 layer dispatch-time mint
- GitHub docs: [Generating an installation access token](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-an-installation-access-token-for-a-github-app)
- GitHub docs: [Permissions required for GitHub Apps](https://docs.github.com/en/rest/overview/permissions-required-for-github-apps)
- CLAUDE.md landmines (worker-on-laptop / module split / `internal/` retired)
