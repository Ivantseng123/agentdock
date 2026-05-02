# AgentDock

[English](README.en.md)

> **從 v1 升級到 v2？** 請見 [docs/MIGRATION-v2.md](docs/MIGRATION-v2.md)（`config.yaml` 拆成 `app.yaml` + `worker.yaml`，worker schema 扁平化；底部有 v2.0 → v2.2 的後續變更）。

AI agent 調度平台 — 從 Slack 接收請求，分派給 CLI agent（claude/codex/opencode）執行，回傳結構化結果。目前支援三個工作流，全部由 `@bot <verb>` 觸發：

| Verb | 範例 | 產出 |
|------|------|------|
| `issue` | `@bot issue` · `@bot owner/repo`（legacy bare-repo 走 issue） | Agent triage codebase → app 建立 GitHub issue → URL 回貼 thread |
| `ask` | `@bot ask 這段 retry 邏輯在哪？` | Agent 讀 thread（可選附 repo）→ 直接在 thread 內回答，不開 issue |
| `review` | `@bot review <PR URL>` | Agent clone PR head → 直接在 PR 上留 line-level comments + summary，回 thread 報狀態 |

不帶動詞（`@bot`）→ 跳出三顆按鈕讓你選 issue / ask / review。`review` 預設開啟，要關就設 `pr_review.enabled: false`。

Go 單一 binary（`agentdock` with `app` / `worker` 兩個子命令）。Transport 以 Redis 為主，`queue.transport` 是保留的擴充點供未來新增 backend。Repo 內有 3 個獨立 Go module：

- [`app/`](app/README.md) — Slack orchestrator
- [`worker/`](worker/README.md) — agent CLI executor
- `shared/` — queue / logging / crypto / GitHub helpers

## 文件

| 主題 | 檔案 |
|------|------|
| Slack App 設定（含 Manifest） | [docs/slack-setup.md](docs/slack-setup.md) |
| GitHub App 設定（單一 installation） | [docs/github-app-setup.md](docs/github-app-setup.md) |
| App 設定（`app.yaml`） | [docs/configuration-app.md](docs/configuration-app.md) |
| Worker 設定（`worker.yaml`） | [docs/configuration-worker.md](docs/configuration-worker.md) |
| 設定總覽 / Quick Start | [docs/configuration.md](docs/configuration.md) |
| 部署（Local / Redis / Docker / K8s / CI） | [docs/deployment.md](docs/deployment.md) |
| 監控與管理、Agent 行為、HTTP endpoints | [docs/operations.md](docs/operations.md) |
| v1 → v2 Migration | [docs/MIGRATION-v2.md](docs/MIGRATION-v2.md) |
| PAT → GitHub App Migration | [docs/MIGRATION-github-app.md](docs/MIGRATION-github-app.md) |

## Quick Start

**macOS / Linux（Homebrew）：**

```bash
brew tap Ivantseng123/tap
brew install agentdock
agentdock init app -i      # 建立 ~/.config/agentdock/app.yaml
agentdock init worker -i   # 建立 ~/.config/agentdock/worker.yaml
agentdock app              # 另起一個 terminal 跑 `agentdock worker`
```

App 和 worker 必須分別跑（`queue.transport: redis`，兩側的 Redis 位址和 `secret_key` 要一致）。

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
@bot <verb>（thread 中）
  → app: dedup + rate limit → workflow dispatcher
    · 不認得的動詞或沒帶動詞 → 跳出 issue / ask / review 三選一
    · 每個 selector / modal 都有「取消」按鈕可清掉 dedup
  → 走對應 workflow 的 UX：
    · issue   → repo / branch 選擇 → 可選補充說明
    · ask     → 可選附 repo（同時決定要不要 branch）
    · review  → thread 內找 PR URL；找不到 → 開 modal 輸問 URL
  → 組 prompt（含 <bot> handle、thread、skill）→ submit 到 Queue
  → worker 從 Queue 取 job
    → 準備 workdir（有 repo 就 clone，沒 repo 就空目錄）
    → mount 對應 skill（triage-issue / ask-assistant / github-pr-review）
    → spawn CLI agent，回傳該 workflow 定義的 JSON（===TRIAGE_RESULT=== / ===ASK_RESULT=== / ===REVIEW_RESULT===）
  → app 依 workflow 收尾：建 issue / 回答 thread / 報 PR review 狀態
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
| Redis Worker | `queue.transport: redis` | App 和 Worker 各自是獨立 process，透過 Redis Stream/Pub/Sub 通訊 |
| External Worker | Redis + 遠端 runner | 另一台機器跑 `agentdock worker`，連同一個 Redis |

兩種部署都走同一套 interface（`JobQueue`, `ResultBus`, `StatusBus`, `CommandBus`, `AttachmentStore`）。`queue.transport` 是擴充點，未來新增 backend 在這裡切換。

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
