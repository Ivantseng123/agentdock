# Logging 重新設計

**日期**: 2026-04-15
**狀態**: 設計完成，待實作
**目標**: 讓 debug 時能直覺追蹤問題，不用在 65 條英文 log 裡大海撈針

---

## 問題摘要

1. Log 全英文，debug 不直覺
2. 沒有 component/phase 分類，看到一條 log 不知道是哪個模組、哪個階段
3. Attribute 命名不一致（`userID` vs `user_id`、`"err"` vs `"error"`）
4. Debug level log 只有 3 條，幾乎沒有細粒度追蹤能力
5. 沒有耗時記錄，無法判斷效能瓶頸

---

## 設計決策

| 決策 | 選擇 | 理由 |
|------|------|------|
| 實作方式 | 自訂 slog Handler | 改動封裝在 logging 層，呼叫端只需 `slog.With()` |
| Log 語言 | message 中文，attribute key 英文 | 中文直覺閱讀，英文 key 相容 log 工具 |
| 分類維度 | Component + Phase 雙維度 | 正交不衝突，兩個維度同時提供 |
| Terminal 格式 | 結構化前綴（無顏色） | `[Component][Phase] 中文訊息 key=value` |
| Debug log | 全面補充（+22 條） | 開 Debug level 時能看到完整資料流 |

---

## 架構設計

### 1. StyledTextHandler

新增 `internal/logging/styled_handler.go`，實作 `slog.Handler` 介面。

**職責**：攔截 `component` 和 `phase` 兩個 attribute，拉到 message 前綴位置。剩餘 attribute 照常 `key=value` 排列。

**輸出格式**：

```
15:03:22 INFO  [Slack][接收] 收到觸發事件 channel_id=C0123 thread_ts=1710000000.000
15:03:22 DEBUG [Slack][接收] 訊息串內容已讀取 channel_id=C0123 message_count=5
15:03:23 INFO  [Agent][處理中] 啟動 CLI agent job_id=20240315-150322-a1b2 provider=claude
15:03:45 WARN  [Agent][降級] 信心度不足，跳過 triage job_id=20240315-150322-a1b2 confidence=low
15:03:46 INFO  [GitHub][完成] Issue 建立成功 job_id=20240315-150322-a1b2 url=https://github.com/...
```

**向後相容**：如果 `component` 或 `phase` 缺失，該段前綴省略，不會炸掉未遷移的 log。

**Handler 鏈路**（MultiHandler 不需改架構）：
- stderr → `StyledTextHandler`（取代現有 `slog.NewTextHandler`）
- file → `slog.NewJSONHandler`（不動，component/phase 自動成為 JSON 欄位）

### 2. Constants

新增 `internal/logging/constants.go`。

**Component 常數**：

```go
const (
    CompSlack   = "Slack"
    CompGitHub  = "GitHub"
    CompAgent   = "Agent"
    CompQueue   = "Queue"
    CompWorker  = "Worker"
    CompSkill   = "Skill"
    CompConfig  = "Config"
    CompMantis  = "Mantis"
    CompApp     = "App"      // cmd/agentdock/app.go 啟動相關
)
```

**Phase 常數**：

```go
const (
    Phase接收   = "接收"
    Phase處理中 = "處理中"
    Phase等待中 = "等待中"
    Phase完成   = "完成"
    Phase降級   = "降級"
    Phase失敗   = "失敗"
    Phase重試   = "重試"
)
```

### 3. Attribute Key 常數

新增 `internal/logging/attributes.go`。

```go
const (
    KeyRequestID  = "request_id"
    KeyJobID      = "job_id"
    KeyWorkerID   = "worker_id"
    KeyChannelID  = "channel_id"
    KeyThreadTS   = "thread_ts"
    KeyUserID     = "user_id"
    KeyRepo       = "repo"
    KeyProvider   = "provider"
    KeyStatus     = "status"
    KeyError      = "error"
    KeyURL        = "url"
    KeyDuration   = "duration_ms"
    KeyActionID   = "action_id"
    KeyVersion    = "version"
    KeyAddr       = "addr"
    KeyPath       = "path"
    KeyName       = "name"
    KeyCount      = "count"
)
```

高頻 key 用常數擋 typo，一次性 key 直接寫字串。

### 4. Logger 建構 Helper

```go
func ComponentLogger(base *slog.Logger, component string) *slog.Logger {
    return base.With("component", component)
}

func WithPhase(logger *slog.Logger, phase string) *slog.Logger {
    return logger.With("phase", phase)
}
```

---

## Attribute 標準化

### 命名規範

全面統一為 snake_case。

| 現況 | 修正為 | 出現位置 |
|------|--------|---------|
| `userID` | `user_id` | `cmd/agentdock/app.go:233`, `internal/slack/client.go:174` |
| `channelID` | `channel_id` | `internal/slack/client.go:201` |
| `actionID` | `action_id` | `cmd/agentdock/app.go:293,317` |
| `selectorTS` | `selector_ts` | `cmd/agentdock/app.go:317` |
| `"err"` | `"error"` | `internal/skill/loader.go:255,269,330,341` |

### Duration 追蹤

在關鍵操作加上 `duration_ms` attribute：

- Agent 執行耗時（`internal/worker/executor.go`）
- GitHub issue 建立耗時（`internal/bot/result_listener.go`）
- Repo clone/fetch 耗時（`internal/github/repo.go`）
- Thread context 讀取耗時（`internal/slack/client.go`）

---

## 中文化規範

### 原則

- **log message**：中文
- **attribute key**：英文 snake_case
- **attribute value**：維持原值不翻譯
- 專有名詞（Socket Mode、agent、GitHub、clone、Redis）維持英文

### 訊息風格

- 動詞開頭，簡潔描述
- 不加句號
- 範例：

| 現況 | 修正為 |
|------|--------|
| `"cloning repo"` | `"開始 clone repo"` |
| `"fetching repo"` | `"開始 fetch repo"` |
| `"failed to download slack file"` | `"Slack 檔案下載失敗"` |
| `"job completed"` | `"工作完成"` |
| `"watchdog: killing stuck job"` | `"強制終止逾時工作"` |
| `"skill.config_reloaded"` | `"技能設定已重新載入"` |
| `"dropping duplicate result"` | `"重複結果已忽略"` |
| `"starting bot"` | `"啟動 Bot"` |
| `"worker pool started"` | `"Worker pool 已啟動"` |
| `"failed to subscribe to results"` | `"訂閱結果匯流排失敗"` |

---

## Component / Phase 對應表

### App（`cmd/agentdock/app.go`, `worker.go`）

| Phase | Level | 訊息 | 觸發時機 |
|-------|-------|------|---------|
| 處理中 | INFO | `啟動 Bot` | app 開始運行 |
| 處理中 | INFO | `Bot 身份已解析` | 解析 bot user ID |
| 處理中 | INFO | `使用 Redis 傳輸層` | Redis mode 啟動 |
| 處理中 | INFO | `使用記憶體內傳輸層` | In-memory mode |
| 處理中 | INFO | `HTTP 端點已啟動` | health/jobs endpoints |
| 處理中 | INFO | `Mantis 整合已啟用` | mantis config 存在 |
| 處理中 | INFO | `已連線至 Redis` | worker 連上 Redis |
| 完成 | INFO | `Worker 已啟動` | worker 就緒 |
| 完成 | INFO | `正在關閉` | 收到 signal |
| 失敗 | WARN | `Bot 身份解析失敗` | auth error |
| 失敗 | WARN | `Repo 快取預熱失敗` | clone error |
| 失敗 | WARN | `技能設定監視器啟動失敗` | watcher error |

### Slack（`internal/slack/client.go`, `cmd/agentdock/adapters.go`）

| Phase | Level | 訊息 | 觸發時機 |
|-------|-------|------|---------|
| 接收 | INFO | `收到觸發事件` | mention/slash command |
| 接收 | INFO | `收到按鈕互動` | block action |
| 接收 | INFO | `收到搜尋建議` | block suggestion |
| 處理中 | INFO | `開始讀取訊息串` | FetchThreadContext |
| 處理中 | DEBUG | `訊息串內容已讀取` | 讀取完成 |
| 處理中 | DEBUG | `下載附件` | 檔案下載 |
| 處理中 | DEBUG | `解析使用者名稱` | resolve user |
| 等待中 | INFO | `已發送選擇器` | PostSelector |
| 失敗 | WARN | `Slack 檔案下載失敗` | download error |
| 失敗 | WARN | `XLSX 下載失敗` | xlsx error |
| 失敗 | WARN | `XLSX 解析失敗` | parse error |
| 失敗 | WARN | `圖片下載失敗` | image error |
| 失敗 | WARN | `圖片過大，跳過` | size limit |
| 失敗 | WARN | `使用者名稱解析失敗` | resolve error |
| 失敗 | WARN | `頻道名稱解析失敗` | resolve error |
| 失敗 | WARN | `附件下載失敗` | DownloadAttachments |
| 失敗 | WARN | `附件寫入失敗` | write error |
| 失敗 | WARN | `發送訊息失敗` | PostMessage error |
| 失敗 | WARN | `更新訊息失敗` | UpdateMessage error |

### Agent（`internal/bot/agent.go`, `internal/bot/workflow.go`, `internal/worker/executor.go`）

| Phase | Level | 訊息 | 觸發時機 |
|-------|-------|------|---------|
| 處理中 | DEBUG | `Prompt 已組裝` | prompt 建構完成 |
| 處理中 | INFO | `啟動 CLI agent` | spawn process |
| 處理中 | DEBUG | `Agent 原始輸出` | output 長度/摘要 |
| 處理中 | DEBUG | `解析 agent 輸出` | parser 結果 |
| 完成 | INFO | `Agent 執行完成` | 正常結束 |
| 降級 | WARN | `信心度不足，建議拒絕` | confidence=low |
| 降級 | WARN | `Triage 資訊不足，跳過 triage 區段` | files=0 or questions>=5 |
| 失敗 | ERROR | `Agent 執行失敗` | timeout/crash |
| 失敗 | WARN | `Provider 未找到` | config miss |
| 失敗 | WARN | `Provider 失敗，嘗試下一個` | fallback |

### GitHub（`internal/github/repo.go`, `discovery.go`）

| Phase | Level | 訊息 | 觸發時機 |
|-------|-------|------|---------|
| 處理中 | INFO | `開始 clone repo` | 首次 clone |
| 處理中 | INFO | `開始 fetch repo` | 已有 clone |
| 處理中 | INFO | `移除損壞目錄並重新 clone` | fetch 失敗後 |
| 處理中 | DEBUG | `Git pull fast-forward 失敗（可能在 detached head）` | ff 失敗 |
| 處理中 | DEBUG | `Branch 清單已取得` | ListBranches |
| 完成 | INFO | `探索到 GitHub repos` | discovery 完成 |
| 完成 | INFO | `Issue 建立成功` | CreateIssue |
| 失敗 | WARN | `Git fetch 失敗` | fetch error |
| 失敗 | ERROR | `Issue 建立失敗` | API error |

### Queue（`internal/queue/watchdog.go`, `coordinator.go`）

| Phase | Level | 訊息 | 觸發時機 |
|-------|-------|------|---------|
| 接收 | INFO | `工作已加入佇列` | enqueue |
| 處理中 | INFO | `Watchdog 已啟動` | 啟動時 |
| 處理中 | DEBUG | `Watchdog 掃描中` | 每次檢查週期 |
| 完成 | INFO | `Watchdog 已停止` | 正常關閉 |
| 失敗 | WARN | `Watchdog 列舉工作失敗` | list error |
| 失敗 | WARN | `強制終止逾時工作` | stuck job |

### Worker（`internal/worker/pool.go`, `executor.go`）

| Phase | Level | 訊息 | 觸發時機 |
|-------|-------|------|---------|
| 處理中 | INFO | `Worker pool 已啟動` | pool start |
| 處理中 | DEBUG | `Worker 領取工作` | dequeue |
| 處理中 | DEBUG | `附件解析完成` | attachment resolution |
| 處理中 | DEBUG | `技能掛載完成` | skill mounting |
| 完成 | INFO | `工作完成` | job success |
| 失敗 | ERROR | `接收指令失敗` | receive error |
| 失敗 | WARN | `終止指令失敗` | kill error |
| 失敗 | WARN | `Worker 註冊失敗` | registration error |
| 重試 | INFO | `工作重試中` | retry submit |

### Skill（`internal/skill/loader.go`, `watcher.go`）

| Phase | Level | 訊息 | 觸發時機 |
|-------|-------|------|---------|
| 處理中 | DEBUG | `載入技能檔案` | load/cache |
| 完成 | INFO | `技能設定已重新載入` | hot reload |
| 失敗 | ERROR | `技能設定重新載入失敗` | reload error |
| 失敗 | ERROR | `技能監視器錯誤` | watcher error |
| 失敗 | WARN | `未知技能類型` | unknown type |
| 失敗 | WARN | `本地技能未找到` | baked-in miss |
| 失敗 | WARN | `技能下載失敗，記錄負向快取` | fetch error |
| 失敗 | WARN | `技能驗證失敗，記錄負向快取` | validation error |
| 失敗 | WARN | `無法讀取內建技能目錄` | dir error |
| 失敗 | WARN | `跳過內建技能` | skip error |

### Config（`internal/config/config.go`, `cmd/agentdock/config.go`）

| Phase | Level | 訊息 | 觸發時機 |
|-------|-------|------|---------|
| 處理中 | INFO | `載入設定檔` | 啟動時 |
| 完成 | INFO | `設定載入完成` | 成功 |
| 失敗 | WARN | `設定儲存失敗` | save error |
| 失敗 | WARN | `未知設定鍵` | unknown key |
| 失敗 | WARN | `max_concurrent 已棄用，請改用 workers.count` | deprecation |

### Mantis（`internal/bot/enrich.go`）

| Phase | Level | 訊息 | 觸發時機 |
|-------|-------|------|---------|
| 完成 | INFO | `Mantis issue 已擴充` | 成功 |
| 失敗 | WARN | `Mantis issue 擴充失敗` | fetch error |

### Result Listener（`internal/bot/result_listener.go` — 歸類為 Agent component）

| Phase | Level | 訊息 | 觸發時機 |
|-------|-------|------|---------|
| 完成 | INFO | `工作完成` | 正常結束 |
| 降級 | WARN | `工作失敗` | agent 失敗 |
| 失敗 | ERROR | `訂閱結果匯流排失敗` | subscribe error |
| 失敗 | ERROR | `找不到工作結果對應的工作` | job not found |
| 處理中 | DEBUG | `重複結果已忽略` | dedup |

### Status Listener（`internal/bot/status_listener.go` — 歸類為 Queue component）

| Phase | Level | 訊息 | 觸發時機 |
|-------|-------|------|---------|
| 失敗 | ERROR | `訂閱狀態匯流排失敗` | subscribe error |

### Retry Handler（`internal/bot/retry_handler.go` — 歸類為 Worker component）

| Phase | Level | 訊息 | 觸發時機 |
|-------|-------|------|---------|
| 重試 | INFO | `重試工作已提交` | 成功 submit |
| 重試 | WARN | `重試：找不到工作` | job not found |
| 重試 | INFO | `重試：工作非失敗狀態，忽略` | wrong state |
| 重試 | ERROR | `重試：提交失敗` | submit error |

---

## Debug Log 補充計畫

目標：從 3 條 → 25 條，佔總量約 25%。

### 新增 Debug Log 清單

**Slack（+5 條）**
- 訊息串讀取結果（訊息數量、有無附件）
- 檔案下載細節（檔名、大小）
- 使用者名稱解析結果
- 頻道名稱解析結果
- 按鈕互動 payload 細節

**Agent（+6 條）**
- Prompt 長度/摘要
- 啟動指令與參數
- Agent 原始輸出長度
- 解析結果的 raw marker
- Provider fallback 觸發原因
- Agent timeout 剩餘時間

**GitHub（+4 條）**
- Clone/fetch 指令細節
- Branch 清單結果
- Issue 建立的 request body 摘要
- API rate limit 剩餘

**Queue/Worker（+4 條）**
- Job enqueue/dequeue 細節
- Worker 領取工作的分配
- Watchdog 掃描結果
- 附件解析/技能掛載完成

**Skill（+3 條）**
- 技能檔案快取命中/未命中
- NPX 掃描結果
- Hot reload 偵測到的變更內容

---

## 檔案結構

### 新增/修改

```
internal/logging/
  handler.go              # 既有 MultiHandler（微調 stderr handler）
  styled_handler.go       # 新增：StyledTextHandler
  styled_handler_test.go  # 新增：測試
  constants.go            # 新增：Component + Phase 常數
  attributes.go           # 新增：Attribute Key 常數
  helpers.go              # 新增：ComponentLogger, WithPhase
  helpers_test.go         # 新增：測試
  request_id.go           # 既有（不動）
  rotator.go              # 既有（不動）
  agent.go                # 既有（不動）
  README.md               # 新增：logging 使用指南 + component/phase 對應表
```

### 遷移範圍（按模組）

| 模組 | 檔案 | 現有 log 數 | 預估改動 |
|------|------|------------|---------|
| App | `cmd/agentdock/app.go` | 14 | message 中文化 + component/phase + attribute 修正 |
| App | `cmd/agentdock/worker.go` | 3 | message 中文化 + component/phase |
| App | `cmd/agentdock/adapters.go` | 2 | message 中文化 + component/phase |
| App | `cmd/agentdock/config.go` | 2 | message 中文化 + component/phase |
| Slack | `internal/slack/client.go` | 9 | message 中文化 + component/phase + attribute 修正 |
| Bot | `internal/bot/workflow.go` | 2 | message 中文化 + component/phase |
| Bot | `internal/bot/agent.go` | 1 | message 中文化 + component/phase |
| Bot | `internal/bot/result_listener.go` | 4 | message 中文化 + component/phase |
| Bot | `internal/bot/status_listener.go` | 1 | message 中文化 + component/phase |
| Bot | `internal/bot/retry_handler.go` | 4 | message 中文化 + component/phase |
| Bot | `internal/bot/enrich.go` | 2 | message 中文化 + component/phase |
| GitHub | `internal/github/repo.go` | 5 | message 中文化 + component/phase |
| GitHub | `internal/github/discovery.go` | 1 | message 中文化 + component/phase |
| Queue | `internal/queue/watchdog.go` | 4 | message 中文化 + component/phase |
| Worker | `internal/worker/pool.go` | 4 | message 中文化 + component/phase |
| Skill | `internal/skill/loader.go` | 6 | message 中文化 + component/phase + `"err"` → `"error"` |
| Skill | `internal/skill/watcher.go` | 3 | message 中文化 + component/phase |
| Config | `internal/config/config.go` | 1 | message 中文化 + component/phase |

---

## 遷移策略

不搞 big bang，分三波：

### 第一波：基礎設施
1. 建 `styled_handler.go`、`constants.go`、`attributes.go`、`helpers.go`
2. 加測試
3. `cmd/agentdock/app.go` 裡把 stderr handler 換成 `StyledTextHandler`
4. 此時所有現有 log 照常運作，只是沒有 `[Component][Phase]` 前綴

### 第二波：逐模組遷移
按模組逐一改動，每個模組：
1. 在進入點建 `ComponentLogger`
2. 各 log 點加 `phase`
3. Message 改中文
4. Attribute key 統一 snake_case
5. 補 Debug log
6. 跑 `go test ./...` 確認沒炸

建議順序：`internal/logging/` → `internal/bot/` → `internal/slack/` → `internal/github/` → `internal/queue/` → `internal/worker/` → `internal/skill/` → `internal/config/` → `cmd/agentdock/`

### 第三波：收尾
1. 寫 `internal/logging/README.md`
2. 掃描全專案確認無殘留英文 message 或 camelCase key
3. 更新 `CLAUDE.md` 的 Lessons Learned 加入 logging 規範

---

## 向後相容

- `StyledTextHandler` 遇到沒有 component/phase 的 log 正常輸出，只是沒前綴
- JSON 輸出格式不變，只是多了 `component`、`phase`、`duration_ms` 欄位
- 不會有中間狀態導致 log 解析爆炸

---

## 產出文件

- `internal/logging/README.md`：logging 使用指南
  - 如何建立 component logger
  - Phase 對應表（本文件的對應表精簡版）
  - Attribute 命名規範
  - 新增 log 的 checklist
  - Debug log 何時該加的判斷原則
