---
date: 2026-04-22
status: approved
owners: Ivantseng123
---

# Mantis 擷取交給 Agent Skill

## Problem

現況：

- `app/mantis/` package 與 `app/bot/enrich.go` 在 backend 實作 Mantis REST 擷取。流程是 app 在排入佇列前同步打 Mantis，把 title + description 拼接到 `ThreadMessage.Text`，再送給 worker。
- 使用者通報「Mantis 連結沒有方式擷取內容」— 其實功能從未被啟用（`mantis.*` config 空著）。
- 公司已有一份成熟的 Mantis agent skill（管理於 `softleader/agent-skills`），能力遠超目前 backend 實作：`get-issue` / `list-issues` / `list-attachments` / `download-attachment` / `add-note` / `stats` 等，還有結構化的 `status` 錯誤 taxonomy。

結論：既然 skill 已經存在且更強，backend 這層是多餘且方向錯誤的中介。保留它只是讓 app 知道一件它不該知道的事（Mantis 存在與否）、讓 worker 收到「已熟成」的文字脈絡（剝奪 agent 自主擷取能力）。

## Root Reasoning

- **方向**：agent-first。triage 任務本質是「agent 探查資料並推論」，把 Mantis 擷取放在 backend 等於預先替 agent 做決策（一律 fetch、一律塞全 description）。agent 其實能更聰明地判斷「這 URL 要不要抓 / 抓完整 description 還是抓附件截圖 / 要不要抓相關 issue」。
- **既有基礎**：`worker/agent/runner.go:137-145` 已經把 `job.EncryptedSecrets` 解密後的 key/value 注入 agent process env。`executor.go:127-135` 已經把 `job.Skills` 掛載到 agent 的 skill 目錄。兩條 infra 都已存在，本 refactor 幾乎零新 code on the wire。
- **架構衛生**：Mantis 留在 app 代表 app 有 Mantis import、有 Mantis 相關 validate、有 Mantis lifecycle log — 跟 `App/Worker 獨立` 原則背道而馳。整包拔掉就是「app 只做 Slack 事件編排 + 排入佇列」的最小化形態。

## Goals

- 砍 `app/mantis/` 整包與 `app/bot/enrich.go`。App 完全不 import Mantis 相關程式碼。
- Mantis 設定（`base_url` + `api_token`）保留在 `app.yaml`，但用途改為**轉譯為 env var 推給 agent**（`MANTIS_API_URL` / `MANTIS_API_TOKEN`），而非後端自擷取。
- Mantis agent skill（`softleader/agent-skills` 裡的 mantis）bundle 進本 repo 的 `agents/skills/mantis/`，走既有 baked-in skill loader，自動隨 job 投遞給 worker。
- 更新 `agents/skills/triage-issue/SKILL.md`，指引 agent「遇到 Mantis URL 用 mantis skill 擷取」。
- `agentdock init app --interactive` 新增 optional Mantis 段，引導使用者啟用。

## Non-Goals

- **不保留 basic auth**（`username` + `password`）：skill 只支援 API token 認證，config 欄位直接砍。pre-launch 沒使用者受影響。
- **不做 runtime Mantis preflight connectivity check**：Mantis 是選用功能，server 掛不該擋 app 啟動。結構性驗證足以 catch 設定錯誤；init 時的互動驗證足以 catch 一次性 token 錯填。
- **不動 worker 任何 Mantis 相關 code**：worker 本來就沒 Mantis package，continue 保持零認知。Worker 只需透過既有 secret bus → env var 機制把 `MANTIS_API_URL` / `MANTIS_API_TOKEN` 餵給 agent process。
- **不改 Mantis skill 本身**：skill 維護在 `softleader/agent-skills`，本 repo 只是複製 bundle。升級 skill 走另一條路（複製新版本 + PR）。
- **不做 inline URL expansion**：prompt builder 不會自動把 thread 內的 Mantis URL 展開成 issue 內容。完全交由 agent 在執行時動態擷取。

## Design

### 1. 移除 backend Mantis

刪除：

- `app/mantis/` 整個 package（`client.go`）。
- `app/bot/enrich.go`（URL 擷取 orchestration）。

改動：

- `app/bot/workflow.go`：
  - 移除 `mantisClient *mantis.Client` 欄位。
  - 移除 `NewWorkflow` 簽章中的 `mantisClient *mantis.Client` 參數。
  - 移除第 411-415 行的 `if w.mantisClient != nil { text = enrichMessage(text, w.mantisClient) }`。
  - 移除 `import mantis`。

- `app/app.go`：
  - 移除第 104-112 行的 `mantis.NewClient(...)` 建構與「Mantis 整合已啟用」log。
  - 移除 `import mantis`。
  - 移除 `NewWorkflow` 呼叫中的 `mantisClient` 引數。

### 2. Config 瘦身

**`app/config/config.go`**：`MantisConfig` 砍剩兩欄位：

```go
type MantisConfig struct {
    BaseURL  string `yaml:"base_url"`
    APIToken string `yaml:"api_token"`
}
```

**`app/config/flags.go`**：

- 移除 `mantis-username` / `mantis-password` flag 定義與 `FlagYAMLMap` 對應。
- 保留 `mantis-base-url` / `mantis-api-token`。

**`app/config/env.go`**：移除 `MANTIS_USERNAME` / `MANTIS_PASSWORD` env mapping（如有）；`MANTIS_API_TOKEN` 映射現況保留。

**舊 YAML 相容性**：koanf 載入對 unknown keys 不 fail，只透過 `warnUnknownKeys` 印 warn。pre-launch 不需 migration tool；若 dev 環境有 `mantis.username` / `mantis.password`，啟動會印一行 warn。

### 3. Secret bus 注入

**`app/config/defaults.go:resolveSecrets`** 在既有 GitHub token 段落之後新增：

```go
if cfg.Mantis.BaseURL != "" && cfg.Mantis.APIToken != "" {
    if _, exists := cfg.Secrets["MANTIS_API_URL"]; !exists {
        cfg.Secrets["MANTIS_API_URL"] = strings.TrimRight(cfg.Mantis.BaseURL, "/") + "/api/rest"
    }
    if _, exists := cfg.Secrets["MANTIS_API_TOKEN"]; !exists {
        cfg.Secrets["MANTIS_API_TOKEN"] = cfg.Mantis.APIToken
    }
}
```

語意：

- `base_url` 在 config 存 host 根（`https://mantis.cloud.softleader.com.tw`），符合使用者貼 view URL 時的心智模型。
- `resolveSecrets` 自動附 `/api/rest` 後存入 `Secrets["MANTIS_API_URL"]`，對齊 skill 期望的 env 格式。
- `Secrets` 已由 `workflow.go:496-509` encrypt 進 `Job.EncryptedSecrets`；worker `executor.go:60-73` decrypt 為 `mergedSecrets`；`agent/runner.go:137-145` 將 key/value 注入 `cmd.Env`。完全複用既有 secret bus，零新 wire。

### 4. 結構性驗證

**`app/config/validate.go`** 新增：

```go
if (cfg.Mantis.BaseURL != "") != (cfg.Mantis.APIToken != "") {
    errs = append(errs, "mantis.base_url and mantis.api_token must both be set or both be empty")
}
```

語意：partial config（只填一半）→ fail-fast 拒啟動，與既有 validate 一致。完全空或完全填是合法態。

### 5. Bundle Mantis skill

把 `softleader/agent-skills` 中的 mantis skill 整包複製到 `agents/skills/mantis/`，預期結構：

```
agents/skills/mantis/
├── SKILL.md
├── scripts/
│   ├── mantis.js
│   └── maintenance/
│       ├── bootstrap-project-metadata.mjs
│       └── refresh-project-metadata.mjs
└── references/
    ├── issue-workflow.md
    └── metadata-maintenance.md
```

`app/skill/loader.go:loadBakedInSkills` 在啟動時自動 scan `agents/skills/` 下每個子目錄（要求包含 `SKILL.md`），用 `readDirRecursive` 抓所有檔案 bytes 進記憶體。`Loader.LoadAll` 產出的 skill payload 被 `workflow.loadSkills` 塞進 `Job.Skills`，worker `executor.mountSkills` 寫入 agent workdir 的 skill dir，權限 0644（`node script.js` 不需 execute bit）。

**Runtime 依賴**：worker host 需有 `node` runtime（Node 18+）。非本 spec 範圍，但須在 `docs/deployment.md` 加一行 prerequisite。

**升級路徑**（非本 spec 範圍但登記）：skill 更新時從 `softleader/agent-skills` 複製新版本到 `agents/skills/mantis/` 並開 PR。未來若改走 npm internal registry，改 `app.yaml:skills_config` 指 remote package 即可，baked-in 路徑可以之後再撤。

### 6. 指引 agent 使用 mantis skill

**`agents/skills/triage-issue/SKILL.md`** 在 "Process" 章節的 "1. Understand the problem" 下新增子段：

```markdown
### 1a. Fetch Mantis issue context (if applicable)

If the thread context contains Mantis issue URLs (patterns like
`view.php?id=<N>` or `/issues/<N>`), invoke the `mantis` skill to
fetch full issue details before proceeding with code investigation.

```bash
node <skill-path>/mantis/scripts/mantis.js status     # sanity check
node <skill-path>/mantis/scripts/mantis.js get-issue <N> --full
node <skill-path>/mantis/scripts/mantis.js list-attachments <N>
```

Incorporate issue description, severity, handler, and any relevant
attachment content (use `download-attachment` for screenshots and
then Read them) into your root-cause analysis.

If `MANTIS_API_URL` or `MANTIS_API_TOKEN` are not set, the `status`
command reports missing configuration — in that case, keep the URL
in your output as-is and note that Mantis enrichment is unavailable.
```

此改動純文字（markdown），無程式碼影響。agent 讀到這份 SKILL.md 後在執行 triage 時會自動照走。

### 7. Init 互動流程

**`shared/prompt/prompt.go`** 新增 helper：

```go
// YesNoDefault prompts for Y/N with explicit default. Pressing Enter
// returns defaultYes. Existing YesNo (default-yes) delegates to this.
func YesNoDefault(prompt string, defaultYes bool) bool {
    suffix := "[y/N]"
    if defaultYes {
        suffix = "[Y/n]"
    }
    answer := Line(fmt.Sprintf("%s %s: ", prompt, suffix))
    lower := strings.ToLower(answer)
    if answer == "" {
        return defaultYes
    }
    return lower == "y" || lower == "yes"
}

func YesNo(prompt string) bool {
    return YesNoDefault(prompt, true)
}
```

既有 `YesNo` 呼叫點（`init.go:262` 自動生成 secret key）行為不變。

**`cmd/agentdock/init.go:promptAppInit`** 在 secret key 段之後新增 optional Mantis 段：

```go
fmt.Fprintln(prompt.Stderr)
fmt.Fprintln(prompt.Stderr, "  Mantis enrichment (optional) — lets the agent fetch Mantis issue details.")
if prompt.YesNoDefault("  Enable Mantis?", false) {
    if err := promptMantis(cfg); err != nil {
        return err
    }
}
```

`promptMantis` 子函式流程：

1. 問 `base_url`（`prompt.Line`），trim trailing slash。
2. 問 `api_token`（`prompt.Hidden`）。
3. 呼叫 `connectivity.CheckMantis(baseURL, apiToken)` 驗證；成功印 `Mantis connected ({N} projects accessible)`，失敗印錯誤並重試，最多 3 次。
4. 3 次失敗後 prompt `Skip Mantis setup? [Y/n]`（預設 Y，即跳過），Y 就清回零值並 return。
5. 成功則 `cfg.Mantis.BaseURL` / `cfg.Mantis.APIToken` 寫入。

### 8. Connectivity helper

**`shared/connectivity/mantis.go`** 新檔：

```go
// CheckMantis probes the Mantis REST API with the given credentials by
// listing one project. Returns the number of accessible projects on
// success.
func CheckMantis(baseURL, apiToken string) (int, error) {
    if baseURL == "" {
        return 0, errors.New("base URL is empty")
    }
    if apiToken == "" {
        return 0, errors.New("API token is empty")
    }

    url := strings.TrimRight(baseURL, "/") + "/api/rest/projects?page_size=1"
    httpClient := &http.Client{Timeout: 10 * time.Second}
    req, _ := http.NewRequest(http.MethodGet, url, nil)
    req.Header.Set("Authorization", apiToken)

    resp, err := httpClient.Do(req)
    if err != nil {
        return 0, fmt.Errorf("connect %s: %w", baseURL, err)
    }
    defer resp.Body.Close()

    switch resp.StatusCode {
    case http.StatusOK:
        var body struct {
            Projects []struct{} `json:"projects"`
        }
        if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
            return 0, fmt.Errorf("decode response: %w", err)
        }
        return len(body.Projects), nil
    case http.StatusUnauthorized, http.StatusForbidden:
        return 0, errors.New("invalid credentials")
    case http.StatusNotFound:
        return 0, fmt.Errorf("REST API not found at %s; confirm URL or REST plugin enabled", baseURL)
    default:
        return 0, fmt.Errorf("Mantis returned HTTP %d", resp.StatusCode)
    }
}
```

### 9. 文件

- **`docs/configuration-app.md`** 的 `mantis:` 段落改寫：
  - 說明「設定轉譯為 env var 推給 agent skill，不在 backend 擷取」
  - 列出 env var 名稱 `MANTIS_API_URL` / `MANTIS_API_TOKEN` 與其 value 來源（`base_url + "/api/rest"` / `api_token`）
  - 註明 basic auth 已移除（skill 不支援）
  - 一段「agent 自動擷取」行為示範
- **`docs/configuration-app.en.md`** 同步英文版。
- **`README.md` / `README.en.md`** Features 段加：`- **Mantis issue lookup**: bundled mantis skill allows the agent to fetch issue details, attachments, and stats (requires config)`。
- **`docs/deployment.md`** 補一行 `node` (18+) prerequisite（mantis skill 依賴）。

## Testing

### 刪除測試

無現有測試引用 `enrichMessage` / `mantis.Client`（grep 確認），直接 delete source 無測試遺毒。

### 新增測試

- `app/config/validate_test.go`:
  - `TestValidate_Mantis_PartialConfig` — base_url 有 / api_token 空 → error
  - `TestValidate_Mantis_PartialConfig_reverse` — api_token 有 / base_url 空 → error
  - `TestValidate_Mantis_BothEmpty_OK` → 合法
  - `TestValidate_Mantis_BothSet_OK` → 合法
- `app/config/defaults_test.go`:
  - `TestResolveSecrets_MantisInjected` — 給 Mantis config，verify `Secrets["MANTIS_API_URL"]` 有 `/api/rest` 後綴、`Secrets["MANTIS_API_TOKEN"]` 為 token 原值。
  - `TestResolveSecrets_MantisEmpty_NoInjection` — Mantis 空，verify Secrets 不被寫入 Mantis key。
  - `TestResolveSecrets_MantisExistingSecretOverride` — 若 user 在 `Secrets` 自行填 `MANTIS_API_URL`，不被 `cfg.Mantis.BaseURL` 覆寫。
- `shared/connectivity/mantis_test.go`:
  - `TestCheckMantis_Success` — httptest mock 200 回 projects list
  - `TestCheckMantis_Unauthorized` — 401，error 含 `invalid credentials`
  - `TestCheckMantis_NotFound` — 404，error 含 `REST API not found`
  - `TestCheckMantis_EmptyBaseURL` — 直接 error，無 HTTP call
  - `TestCheckMantis_EmptyToken` — 直接 error，無 HTTP call
  - （Timeout 不測；`http.Client.Timeout` 由 `net/http` 保證。）
- `shared/prompt/prompt_test.go`:
  - `TestYesNoDefault_Empty_ReturnsDefault` — 兩種 default 各一
  - `TestYesNoDefault_YesInput` / `TestYesNoDefault_NoInput`
- `cmd/agentdock/init_test.go`:
  - `TestRunInitApp_NonInteractive_Mantis_ZeroValue` — 非互動模式後讀回 yaml，assert `mantis.base_url == ""`。
- `agents/skills/mantis/` 的 SKILL.md 與 scripts 不由本 repo 寫測試（skill 本身的測試維護在 `softleader/agent-skills`）。

### 手動驗證

- 跑 `agentdock init app --interactive` 啟用 Mantis，驗錯誤 token 的 retry 與 skip 行為。
- 配置 Mantis 後啟動 app，`/jobs` trigger 一個含 Mantis view URL 的 thread，觀察 agent CLI 是否自動呼叫 `mantis` skill 並把結果納入 triage output。
- 不配置 Mantis，確認 app 正常啟動、正常處理 Mantis URL（agent 看到 URL 但不 fetch）。

## Files Changed

| 檔案 | 動作 |
|------|------|
| `app/mantis/` | **delete package** |
| `app/bot/enrich.go` | **delete** |
| `app/bot/workflow.go` | 砍 `mantisClient` 欄位、`enrichMessage` 呼叫、`NewWorkflow` 簽章、import |
| `app/app.go` | 砍 `mantis.NewClient` 建構與 log；更新 `NewWorkflow` 呼叫 |
| `app/config/config.go` | `MantisConfig` 砍 `Username` / `Password` |
| `app/config/flags.go` | 砍對應 flag |
| `app/config/env.go` | 砍對應 env mapping（若存在） |
| `app/config/defaults.go` | `resolveSecrets` 新增 Mantis → secrets injection |
| `app/config/defaults_test.go` | 新增 Mantis secrets injection 測試 |
| `app/config/validate.go` | Mantis partial config check |
| `app/config/validate_test.go` | Mantis partial config 測試 |
| `cmd/agentdock/init.go` | `promptAppInit` 加 optional Mantis 段 + `promptMantis` 子函式 |
| `cmd/agentdock/init_test.go` | non-interactive zero-value 驗證 |
| `shared/connectivity/mantis.go`（新檔） | `CheckMantis` helper |
| `shared/connectivity/mantis_test.go`（新檔） | httptest 驅動測試 |
| `shared/prompt/prompt.go` | 新增 `YesNoDefault`；`YesNo` 改 delegate |
| `shared/prompt/prompt_test.go` | `YesNoDefault` 測試 |
| `agents/skills/mantis/` | **新增 skill bundle**（從 `softleader/agent-skills` 複製） |
| `agents/skills/triage-issue/SKILL.md` | 新增「Fetch Mantis issue context」子段指引 |
| `docs/configuration-app.md` / `.en.md` | 重寫 Mantis 區塊 |
| `README.md` / `README.en.md` | Features 段加一行 |
| `docs/deployment.md` | `node` runtime prerequisite 備註 |

**無 queue schema 變更**；Mantis 資訊完全走現有 `Secrets` map。
**無 import direction 衝突**；改動均在 `app/`、`cmd/`、`shared/` 內。
**無 MIGRATION 文件新增**；pre-launch 無使用者受影響。

## Out of Scope

- 其他 ticketing 系統（Jira / Linear / Notion）的 skill bundling — 同 pattern 可跟進。
- Mantis skill 自身的測試覆蓋（由 `softleader/agent-skills` 維護）。
- npm internal registry / remote skill 載入方案（未來選項，本次先走 baked-in）。
- Mantis preflight runtime connectivity check（由 init 階段互動驗證負責）。
- 自動同步 `softleader/agent-skills` 到本 repo（目前手動複製）。
