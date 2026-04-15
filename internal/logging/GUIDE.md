# Logging 開發指南

## 概述

本專案使用 Go 標準庫 `log/slog` 搭配自訂 `StyledTextHandler`，提供雙維度分類的結構化 log。

## 輸出格式

### Terminal (stderr)
```
15:03:22 INFO  [Slack][接收] 收到觸發事件 channel_id=C0123 thread_ts=1234.5678
```

### File (JSON)
```json
{"time":"...","level":"INFO","msg":"收到觸發事件","component":"Slack","phase":"接收","channel_id":"C0123"}
```

## Component 注入

每個 struct 在建構時接收一個已綁定 component 的 `*slog.Logger`：

```go
// 建構端 (app.go)
logger := logging.ComponentLogger(slog.Default(), logging.CompSlack)
client := slack.NewClient(token, logger)

// struct 內部
func (c *Client) DoSomething() {
    c.logger.Info("做某件事", "phase", logging.PhaseProcessing, "key", value)
}
```

## Phase 使用方式

Phase 是**逐條帶入**的，不綁定到 logger（避免 slog.With 疊加）：

```go
c.logger.Info("訊息", "phase", logging.PhaseReceive, ...)
c.logger.Warn("錯誤", "phase", logging.PhaseFailed, ...)
```

## Component 清單

| 常數 | 值 | 適用模組 |
|------|---|---------|
| CompSlack | Slack | internal/slack, adapters |
| CompGitHub | GitHub | internal/github |
| CompAgent | Agent | internal/bot (agent, result_listener) |
| CompQueue | Queue | internal/queue (watchdog, status_listener) |
| CompWorker | Worker | internal/worker, retry_handler |
| CompSkill | Skill | internal/skill |
| CompConfig | Config | internal/config |
| CompMantis | Mantis | internal/bot/enrich |
| CompApp | App | cmd/agentdock |

## Phase 清單

| 常數 | 值 | 用途 |
|------|---|------|
| PhaseReceive | 接收 | 收到外部事件 |
| PhaseProcessing | 處理中 | 執行核心邏輯 |
| PhaseWaiting | 等待中 | 等外部回應 |
| PhaseComplete | 完成 | 成功結束 |
| PhaseDegraded | 降級 | 部分成功 |
| PhaseFailed | 失敗 | 出錯 |
| PhaseRetry | 重試 | 重試流程 |

## Attribute 規範

- 全部使用 **snake_case**
- Error key 統一用 `"error"`，不用 `"err"`
- 高頻 key 用 `logging.KeyXxx` 常數
- 一次性 key 直接寫字串

## 新增 Log 的 Checklist

1. 選擇正確的 component（看你在哪個 struct 裡）
2. 選擇正確的 phase（看當前操作的生命週期階段）
3. 中文 message，動詞開頭，不加句號
4. Attribute key 用 snake_case
5. 專有名詞維持英文（GitHub, Redis, agent, clone）
6. 如果是耗時操作，加 `"duration_ms", time.Since(start).Milliseconds()`

## Debug Log 判斷原則

加 Debug log 的時機：
- 操作的輸入/輸出摘要（長度、數量）
- 快取命中/未命中
- 分支選擇的原因（為什麼走這條路）
- 外部 API 呼叫的細節

不需要 Debug log 的時機：
- 每一行程式碼的執行（太 noisy）
- 已經有 Info/Warn 覆蓋的路徑
