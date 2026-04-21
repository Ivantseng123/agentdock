---
date: 2026-04-21
status: approved
owners: Ivantseng123
---

# Mantis Enrichment 可發現性與啟用流程

## Problem

Mantis 連結自動擷取（title + description）在 `app/mantis/client.go`、`app/bot/enrich.go` 已經實作完成，但使用者回報「Mantis 連結沒有方式擷取內容」。實況是功能從來沒被啟用 —— `mantis.base_url` 空著，`IsConfigured()` 回 `false`，`enrichMessage` 直接 return，一切靜默略過。

痛點來自可發現性：

- `agentdock init app --interactive` 完全沒 prompt Mantis。
- `docs/configuration-app.md` 只有 4 行 yaml 範例，沒說明這功能做什麼、何時啟用、如何拿 token。
- `README` 沒介紹 Mantis enrichment。
- `app/app.go` 啟動時 Mantis 未配置就什麼都不印，使用者看不出「這裡其實有個功能你沒啟用」。
- `enrich.go` 遇到連結時未配置也沒 log 提示。

## Goals

- 讓新加入專案、跑 `agentdock init app --interactive` 的人一眼看到 Mantis enrichment 是個可選功能。
- 透過文件讓人知道功能做什麼、怎麼取得 API token、啟用前後行為差別。
- 啟動時清楚顯示 Mantis 是否啟用，未配置時也會印提示。
- `init` 互動流程內建 connectivity check，避免打錯 URL / token 卻毫不知情。

## Non-Goals

- **不擴充擷取欄位**：不加 notes、status、severity、assignee、custom fields、attachments。先讓既有 title+description 能被實際用到；未來真的需要更多內容再另立 spec。
- **不加 HTML scraping fallback**：所有支援情境皆假設 Mantis REST API 可用。
- **不動 URL regex**：現有 `view.php?id=` 與 `/issues/` 兩個 pattern 維持不變。
- **無 config schema 變更**：`mantis.{base_url, api_token, username, password}` 四個欄位早就存在。

## Design

### 1. 文件強化

**`docs/configuration-app.md` 新增「Mantis Enrichment」子章節**（位置放在既有 `mantis:` yaml 範例下方）：

- **功能說明**：當 thread 訊息含 Mantis issue 連結（`view.php?id=` 或 `/issues/`），app 啟動後會自動打 Mantis REST API 抓 title + description，把內容以區塊形式附加到 prompt 脈絡裡，agent 看得到 issue 完整描述而非只一串 URL。
- **啟用條件**：需要 `base_url` + (`api_token` **或** `username`+`password`)。API token 優先，basic auth 為 fallback（相容沒開 API token 的舊 Mantis 版本）。
- **如何取得 API token**：引導到 Mantis 官方 account preferences 的 API tokens 頁面（只提供路徑指引，不放外部截圖）。
- **未配置行為**：Mantis 連結照原樣留在 prompt，agent 看得到 URL 但看不到內容。
- **Example dump**：前後對照小段落，「前：URL only；後：URL + title + description block」。

**`docs/configuration-app.en.md`** 同步產出英文版。

**`README.md` / `README.en.md`** 的 Features 段落新增一行：

```
- **Mantis enrichment**: 自動擷取 Mantis issue title + description 塞進 prompt（選用）
```

### 2. Init 互動擴充

在 `cmd/agentdock/init.go` 的 `promptAppInit` 函式（目前結尾在第 270 行附近）新增 optional segment：

```go
fmt.Fprintln(prompt.Stderr)
fmt.Fprintln(prompt.Stderr, "  Mantis enrichment (optional) — auto-expand Mantis issue URLs in threads.")
if prompt.YesNo("  Enable Mantis?") {
    promptMantis(cfg)
}
```

`promptMantis` 子函式流程：

1. 詢問 `base_url`（`prompt.Line`），trim trailing slash。
2. 詢問認證方式（預設 api_token）：`prompt.YesNo("  Use API token? (no = username+password)")`。
3. 依選擇收輸入（token 用 `prompt.Hidden`；password 用 `prompt.Hidden`）。
4. 呼叫 `connectivity.CheckMantis(baseURL, token, username, password)` 驗證；成功印 `Mantis connected (user: foo)`，失敗印錯誤並重試。
5. 3 次失敗後 prompt `Skip Mantis setup? (Y/n)` — Y 就清回零值並 return（不卡死 init 流程），N 就再三次。

**Skip 行為**：Mantis 不像 Slack / GitHub / Redis 是必填；skip 後回 zero value，後續 app 啟動時會走「未配置」分支。

### 3. Connectivity check

新增 `shared/connectivity/mantis.go`：

```go
// CheckMantis probes {baseURL}/api/rest/users/me with the provided
// credentials. Returns the authenticated user's name on success.
func CheckMantis(baseURL, apiToken, username, password string) (string, error)
```

實作策略：

- 10 秒 timeout（與現有 checks 一致）。
- 打 `GET {baseURL}/api/rest/users/me`。
- 認證 header：`apiToken` 非空用 `Authorization: {apiToken}`（Mantis API 不加 `Bearer` 前綴，與 `app/mantis/client.go:56` 一致）；否則 `SetBasicAuth(username, password)`。
- 回傳對應：
  - 200：decode JSON 取 `user.name`，正常 return。
  - 401 / 403：`invalid credentials`。
  - 404：`REST API not found at {baseURL}; confirm URL or REST plugin enabled`。
  - 其他：`Mantis returned HTTP {code}`。
  - 網路錯誤：`connect {baseURL}: {err}`。

### 4. 啟動 log 補 else branch

`app/app.go:110-112` 現況：

```go
if mantisClient.IsConfigured() {
    appLogger.Info("Mantis 整合已啟用", "phase", "處理中", "url", cfg.Mantis.BaseURL)
}
```

調整為：

```go
if mantisClient.IsConfigured() {
    appLogger.Info("Mantis 整合已啟用", "phase", "處理中", "url", cfg.Mantis.BaseURL)
} else {
    appLogger.Info("Mantis 未配置，thread 中的 Mantis 連結不會展開內容", "phase", "處理中")
}
```

INFO level（非 Warn）：Mantis 是選用功能，未配置屬於正常狀態，只是資訊提示。

## Testing

### `shared/connectivity/mantis_test.go`

用 `httptest.NewServer` 架 mock 伺服器：

- `CheckMantis_APIToken_Success` → 200 response，assert 回 user name。
- `CheckMantis_BasicAuth_Success` → 200 response，assert 回 user name。
- `CheckMantis_Unauthorized` → 401，assert error message 含 `invalid credentials`。
- `CheckMantis_NotFound` → 404，assert error message 含 `REST API not found`。
- `CheckMantis_EmptyBaseURL` → 直接 return error，無 HTTP call。

Timeout 行為不在單測裡驗（硬等 10 秒會拖慢 CI）。`http.Client.Timeout` 由 `net/http` 保證，手動驗證即可。

### `cmd/agentdock/init_test.go`

- Mantis skip 路徑：非互動模式 `runInitApp(path, false, force)` 跑完後讀回 yaml，assert `mantis.base_url == ""`（確認 zero value 不被誤動）。
- 互動模式在現有 `init_test.go` 若沒 coverage 就維持一致；不硬塞一個很難 mock TTY 的測試。

### 手動驗證

- 本地跑 `agentdock init app --interactive`，走 Mantis yes 路徑 + 輸入錯 token 驗 retry；走 Mantis skip 路徑驗中斷退出。
- 本地啟動 app（Mantis 未配置），檢查 log 印出「Mantis 未配置...」。
- 本地啟動 app（Mantis 配置好），檢查 log 印出「Mantis 整合已啟用...」且發個含 Mantis 連結的 thread trigger 測 enrichment 實際生效。

## Files Changed

| 檔案 | 動作 |
|------|------|
| `docs/configuration-app.md` | 新增 Mantis Enrichment 子章節 |
| `docs/configuration-app.en.md` | 英文版同步 |
| `README.md` / `README.en.md` | Features 段加一行 |
| `shared/connectivity/mantis.go` | 新檔 `CheckMantis` |
| `shared/connectivity/mantis_test.go` | `httptest` 驅動的測試 |
| `cmd/agentdock/init.go` | `promptAppInit` 加 optional Mantis 段 + `promptMantis` 子函式 |
| `cmd/agentdock/init_test.go` | skip 路徑驗證 |
| `app/app.go` | Mantis 未配置分支 log |

**無 schema 變更、無 import direction 衝突**（新 connectivity check 在 `shared/`，`cmd/` 呼叫合法）。預估 **300 行上下**，單 PR。

## Out of Scope

- 擷取欄位擴充（notes / status / severity / assignee / custom fields / attachments）。
- HTML scraping fallback（REST API 不可用時的替代路徑）。
- URL regex 擴充（目前兩個 pattern 足夠）。
- Bot thread filter 修復（另一份 spec：`2026-04-21-bot-message-thread-filter-design.md`）。
