# app/

[English](README.en.md)

AgentDock 的 Slack 端。`agentdock app` 的程式碼 module，獨立成 Go module（`github.com/Ivantseng123/agentdock/app`）。

## 責任範圍

- 接收 Slack 事件（`@bot` 在 thread 中觸發 triage）
- Repo / branch 選擇 + 補充說明互動
- 讀 thread context、組 prompt、submit 到 job queue
- 接 job 結果 → 建 GitHub issue → 回 post 到 Slack thread
- Secret 管理（AES-256 加密傳給 worker）
- HTTP endpoints：`/healthz`, `/jobs`, `/metrics`
- Watchdog（偵測卡死的 job）

App **不執行 agent CLI**；那是 worker 的工作。在 inmem 模式下 cmd/agentdock 會另外起本地 worker pool 共用 app 的 buses。

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

`queue.transport` 決定 runtime：

- `inmem`（預設）：app 在同 process 啟動 worker pool，讀 sibling `worker.yaml`（或 `--worker-config` 指定的路徑）
- `redis`：app 只處理 Slack，worker 在另一個 process / pod 跑

Inmem 模式細節：

```bash
agentdock app -c ~/.config/agentdock/app.yaml \
              --worker-config ~/.config/agentdock/worker.yaml
```

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
