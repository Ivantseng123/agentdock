# AgentDock

[English](README.en.md)

> **從 v1 升級到 v2？** 請見 [docs/MIGRATION-v2.md](docs/MIGRATION-v2.md)（`config.yaml` 拆成 `app.yaml` + `worker.yaml`，worker schema 扁平化）。早期 v0 → v1 請見 [docs/MIGRATION-v1.md](docs/MIGRATION-v1.md)。

AI agent 調度平台 — 從任何來源接收請求，分派給 CLI agent（claude/codex/opencode）執行，回傳結構化結果。目前支援 Slack → codebase triage → GitHub Issue 流程。

Go 單一 binary，支援 in-memory 和 Redis 兩種 transport。Repo 內有 3 個獨立 module：

- [`app/`](app/README.md) — Slack orchestrator
- [`worker/`](worker/README.md) — agent CLI executor
- `shared/` — queue / logging / crypto / GitHub helpers

## 文件

| 主題 | 檔案 |
|------|------|
| Slack App 設定（含 Manifest） | [docs/slack-setup.md](docs/slack-setup.md) |
| App 設定（`app.yaml`） | [docs/configuration-app.md](docs/configuration-app.md) |
| Worker 設定（`worker.yaml`） | [docs/configuration-worker.md](docs/configuration-worker.md) |
| 設定總覽 / Quick Start | [docs/configuration.md](docs/configuration.md) |
| 部署（Local / Redis / Docker / K8s / CI） | [docs/deployment.md](docs/deployment.md) |
| 監控與管理、Agent 行為、HTTP endpoints | [docs/operations.md](docs/operations.md) |
| v1 → v2 Migration | [docs/MIGRATION-v2.md](docs/MIGRATION-v2.md) |

## Quick Start

**macOS / Linux（Homebrew）：**

```bash
brew tap Ivantseng123/tap
brew install agentdock
agentdock init app -i      # 建立 ~/.config/agentdock/app.yaml
agentdock init worker -i   # 建立 ~/.config/agentdock/worker.yaml
agentdock app              # 預設讀 ~/.config/agentdock/app.yaml
```

Inmem 模式（單機）會自動啟動本地 worker pool，同時讀 sibling `worker.yaml`。

**從源碼：**

```bash
go build -o agentdock ./cmd/agentdock/
./agentdock init app -i
./agentdock init worker -i
./run.sh
```

> brew 只裝 binary；`worker` 那側還需要 agent CLI（`claude`、`opencode`、`codex`）。正式部署用 Docker 映像 `ghcr.io/ivantseng123/agentdock`。

Slack App 還沒建立？見 [docs/slack-setup.md](docs/slack-setup.md)。

## 流程

```
@bot（thread 中）
  → app: dedup + rate limit → 讀 thread 訊息
  → repo/branch 選擇（thread 內按鈕）→ 可選補充說明
  → 組 prompt → Submit 到 Queue（立即回覆排隊狀態 + 取消按鈕）
  → worker 從 Queue 取 job
    → clone repo → mount skills → spawn CLI agent
    → agent 探索 codebase + 判斷 confidence → 回傳 JSON 結果
  → app 收到結果 → 建 GitHub issue → post URL 到 Slack thread
```

## 架構

```
┌──────────┐     ┌──────────────┐     ┌─────────────┐     ┌──────────────┐
│ app      │────→│ Priority     │────→│ worker Pool │────→│ Result       │
│ (Slack)  │     │ Queue        │     │ (N workers) │     │ Listener     │
│          │     │              │     │             │     │              │
│ dedup +  │     │ channel      │     │ clone repo  │     │ create issue │
│ rate     │     │ priority     │     │ mount skill │     │ post Slack   │
│ limit    │     │              │     │ run agent   │     │              │
└──────────┘     └──────────────┘     └─────────────┘     └──────────────┘
                       ↑ Submit              ↑ Kill              ↑ Status
                       │                     │                   │
                 ┌─────┴─────────────────────┴───────────────────┘
                 │         CommandBus / StatusBus
                 └─────────────────────────────────────────────────
```

### 部署模式

| 模式 | Transport | 說明 |
|------|-----------|------|
| In-Memory | `queue.transport: inmem` | 全部在同一個 process，Go channel 通訊（預設） |
| Redis Worker | `queue.transport: redis` | App 和 Worker 分開部署，Redis Stream/Pub/Sub 通訊 |
| External Worker | Redis + runner binary | 外部機器跑 `agentdock worker`，連同一個 Redis |

三種模式用同一套 interface（`JobQueue`, `ResultBus`, `StatusBus`, `CommandBus`, `AttachmentStore`），只換 transport 層。切換只改 config，不改代碼。

詳細部署步驟見 [docs/deployment.md](docs/deployment.md)。

## 測試

```bash
go test ./... -race                # root module
(cd shared && go test ./... -race)
(cd app && go test ./... -race)
(cd worker && go test ./... -race)
```

Redis integration tests auto-skip when 6379 unreachable。

## Log 層級

兩個獨立層級：

- `log_level`（app.yaml / worker.yaml 頂層）：console / stderr，預設 **info**
- `logging.level`（`logging:` 區塊）：`logs/YYYY-MM-DD.jsonl`，預設 **debug**

支援值：`debug` / `info` / `warn` / `error`。細節見 [docs/configuration-app.md](docs/configuration-app.md) 和 [docs/configuration-worker.md](docs/configuration-worker.md)。

## License

MIT
