# app/

[English](README.en.md)

AgentDock 的 Slack 端。`agentdock app` 的程式碼 module，獨立成 Go module（`github.com/Ivantseng123/agentdock/app`）。

## 責任範圍

- 接收 Slack 事件（`@bot <verb>` 在 thread 中觸發）
- Workflow dispatcher：`issue` / `ask` / `review` 三個 verb + 空 `@bot` 時的三按鈕 selector
- Workflow-specific UX：repo / branch picker、PR URL modal、補充說明 modal、取消按鈕
- 讀 thread context、組 prompt、submit 到 job queue
- 依 workflow 收尾：建 GitHub issue（issue）/ 在 thread 回答（ask）/ 回報 PR review 狀態（review）
- Secret 管理（AES-256 加密傳給 worker）
- HTTP endpoints：`/healthz`, `/jobs`, `/metrics`
- Watchdog（偵測卡死的 job）

App **不執行 agent CLI**；那是 worker 的工作。App 與 worker 永遠是獨立 process，透過 `queue.transport` 指定的 backend 通訊。

## 設定

詳細 schema 見 [docs/configuration-app.md](../docs/configuration-app.md)。

快速建立：

```bash
agentdock init app -i -c ~/.config/agentdock/app.yaml
```

啟動：

```bash
agentdock app -c ~/.config/agentdock/app.yaml
```

## 模式切換

`queue.transport` 決定 queue backend：

- `redis`（目前唯一支援值）：Redis Stream/Pub/Sub，app 與 worker 獨立 process
- 未來擴充：新增 backend 就在 `app/app.go` 的 transport switch 加 case

## 測試

```bash
(cd app && go test ./... -race)
```

## 依賴

- `shared/` — queue、logging、crypto、github、configloader、connectivity、prompt
- 不依賴 `worker/`（app ✗ worker 邊界強制）

## 相關文件

- [頂層 README](../README.md)
- [worker/README.md](../worker/README.md)
- [docs/configuration.md](../docs/configuration.md) — 設定總覽
- [docs/MIGRATION-v2.md](../docs/MIGRATION-v2.md) — v1 → v2 升級
