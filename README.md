# AgentDock

[English](README.en.md)

> **從 v0.x 升級到 v1.0？** 請見 [docs/MIGRATION-v1.md](docs/MIGRATION-v1.md)（binary 改名為 `agentdock`、需帶 subcommand、預設 config 路徑變動）。

AI agent 調度平台 — 從任何來源接收請求，分派給 CLI agent（claude/codex/opencode）執行，回傳結構化結果。目前支援 Slack → codebase triage → GitHub Issue 流程。

Go 單一 binary，支援 in-memory 和 Redis 兩種 transport，worker 可在同一 process、獨立 pod、或同事的電腦上執行。

## 文件

| 主題 | 檔案 |
|------|------|
| Slack App 設定（含 Manifest） | [docs/slack-setup.md](docs/slack-setup.md) |
| 設定（config.yaml、Skills、NPX） | [docs/configuration.md](docs/configuration.md) |
| 部署（Local / Redis / Docker / K8s / CI） | [docs/deployment.md](docs/deployment.md) |
| 監控與管理、Agent 行為、HTTP endpoints | [docs/operations.md](docs/operations.md) |

## Quick Start

**macOS / Linux（Homebrew，推薦團隊開發者）：**

```bash
brew tap Ivantseng123/tap
brew install agentdock
agentdock init -i   # 互動式填入 Slack / GitHub token
agentdock app -c config.yaml
```

升級：`brew upgrade agentdock`。

> brew 只裝 binary；`app`/`worker` 子指令仍需配置 config 與外部 CLI（`claude`、`opencode`、`codex`、`gemini`）。正式部署請使用 Docker 映像 `ghcr.io/ivantseng123/agentdock`。

**從源碼：**

```bash
go build -o agentdock ./cmd/agentdock/
./agentdock init -i   # 互動式填入 Slack / GitHub token
./run.sh
```

或直接：

```bash
./run.sh
# 等同：go build -o agentdock ./cmd/agentdock/ && ./agentdock app -c config.yaml
```

`run.sh` 會自動設定 agent skills → build → 啟動 app。

Slack App 還沒建立？見 [docs/slack-setup.md](docs/slack-setup.md)。

## 流程

```
@bot 或 /triage（thread 中）
  → dedup + rate limit → 讀取 thread 所有訊息
  → repo/branch 選擇（thread 內按鈕）→ 可選補充說明
  → 組 prompt → Submit 到 Priority Queue（立即回覆排隊狀態 + 取消按鈕）
  → Worker 從 Queue 取 job
    → clone repo → mount skills → spawn CLI agent
    → agent 探索 codebase + 判斷 confidence → 回傳 JSON 結果
  → App 收到結果 → 建 GitHub issue → post URL 到 Slack thread
```

## Queue 架構

Bot 使用 producer/consumer queue 解耦 Slack 事件處理和 agent 執行：

```
┌──────────┐     ┌──────────────┐     ┌─────────────┐     ┌──────────────┐
│ Slack    │────→│ Priority     │────→│ Worker Pool │────→│ Result       │
│ Handler  │     │ Queue        │     │ (N workers) │     │ Listener     │
│          │     │ (channel     │     │             │     │              │
│ dedup +  │     │  priority)   │     │ clone repo  │     │ create issue │
│ rate     │     │              │     │ mount skill │     │ post Slack   │
│ limit    │     │ capacity: 50 │     │ run agent   │     │ cleanup      │
└──────────┘     └──────────────┘     └─────────────┘     └──────────────┘
                       ↑ Submit              ↑ Kill              ↑ Status
                       │                     │                   │
                 ┌─────┴─────────────────────┴───────────────────┘
                 │              CommandBus / StatusBus
                 │        (kill signals, agent status reports)
                 └───────────────────────────────────────────────┘
```

- **Priority Queue**：channel-based 優先級 + FIFO，bounded capacity
- **Worker Pool**：N 個 goroutine 消費 job，每個 job 有獨立 context（可 cancel）
- **StatusBus**：worker 定期回報 agent 狀態（PID, tool calls, files read, cost）
- **CommandBus**：app → worker 的 kill 指令通道

### 部署模式

| 模式 | Transport | 說明 |
|------|-----------|------|
| In-Memory | `queue.transport: inmem` | 全部在同一個 process，Go channel 通訊（預設） |
| Redis Worker | `queue.transport: redis` | App 和 Worker 分開部署，Redis Stream/Pub/Sub 通訊 |
| External Worker | Redis + runner binary | 外部機器跑 `agentdock worker`，連同一個 Redis |

三種模式用同一套 interface（`JobQueue`, `ResultBus`, `StatusBus`, `CommandBus`, `AttachmentStore`），只換 transport 層。切換只改 config，不改代碼。

Redis 架構圖、External Worker 依賴、Docker/K8s 部署步驟見 [docs/deployment.md](docs/deployment.md)。

## 觸發方式

| 方式 | 範例 | 說明 |
|------|------|------|
| `@bot` 提及 | 在 thread 中 `@bot` | 讀取 thread 所有前序訊息，主要觸發方式 |
| `/triage` | `/triage` | 顯示使用提示（因 Slack 限制無法偵測 thread context） |

Bot 只在 **thread 中** 運作。在 channel 直接觸發會提示「請在對話串中使用」。

## 測試

```bash
go test ./...   # 215 tests (Redis tests auto-skip if no Redis)
```

## License

MIT
