# react2issue v2

[English](README.en.md)

Slack 對話 → AI codebase triage → GitHub Issue。Go 單一 binary，Socket Mode（不需公開 URL）。

在 Slack thread 中 `@bot` 或 `/triage`，bot 會讀取整段對話、spawn CLI agent（claude/opencode/codex/gemini）探索 codebase，然後建立結構化的 GitHub issue。

## Quick Start

```bash
cp config.example.yaml config.yaml
# 填入 Slack / GitHub token
./run.sh
```

`run.sh` 會自動設定 agent skills → build → 啟動。

## 流程

```
@bot 或 /triage（thread 中）
  → dedup + rate limit → 讀取 thread 所有訊息 + 下載附件
  → repo/branch 選擇（thread 內按鈕）→ 可選補充說明
  → spawn CLI agent（claude/opencode/codex/gemini）
    agent 用自己的工具探索 codebase → 回傳 markdown + JSON metadata
  → confidence=low? 拒絕 : files=0? 建 issue 但跳過 triage : 建完整 issue
  → Go 注入 header（channel/reporter）→ 建 GitHub issue → post URL in thread
```

## 觸發方式

| 方式 | 範例 | 說明 |
|------|------|------|
| `@bot` 提及 | 在 thread 中 `@bot` | 讀取 thread 所有前序訊息 |
| `/triage` | `/triage` | 互動選 repo |
| `/triage` + repo | `/triage owner/repo` | 跳過 repo 選擇 |
| `/triage` + repo + branch | `/triage owner/repo@main` | 直接開始分析 |

Bot 只在 **thread 中** 運作。在 channel 直接觸發會提示「請在對話串中使用」。

## 設定

完整選項見 `config.example.yaml`。

```yaml
auto_bind: true                       # bot 加入頻道自動綁定

channel_defaults:
  branch_select: true
  default_labels: ["from-slack"]

# Agent 設定：CLI agents 依 fallback 順序嘗試
agents:
  claude:
    command: claude
    args: ["--print", "-p", "{prompt}"]
    timeout: 5m
  opencode:
    command: opencode
    args: ["--prompt", "{prompt}"]
    timeout: 5m

active_agent: claude
fallback: [claude, opencode]

prompt:
  language: "繁體中文"
  extra_rules:
    - "列出所有相關的檔案名稱與完整路徑"
```

### Agent 設定

每個 agent 是一個 CLI 工具。`{prompt}` 為 placeholder，bot 會替換為實際 prompt。沒有 `{prompt}` 時走 stdin。

```yaml
agents:
  claude:
    command: claude
    args: ["--print", "-p", "{prompt}"]
    timeout: 5m
  opencode:
    command: opencode
    args: ["--prompt", "{prompt}"]
    timeout: 5m
  codex:
    command: codex
    args: ["{prompt}"]
    timeout: 5m
  gemini:
    command: gemini
    args: ["--prompt", "{prompt}"]
    timeout: 5m
```

### Agent Skills

Skills 集中管理在 `agents/skills/` 目錄，透過 symlink 發布到各 agent 的全域設定：

```
agents/
  skills/
    triage-issue.md    # 唯一來源，所有 agent 共用
  setup.sh             # local 開發：建 symlink（run.sh 自動呼叫）
```

新增 skill：在 `agents/skills/` 放 `.md` 檔即可，`setup.sh` 會自動 link 到 Claude Code 和 OpenCode 的全域目錄。

### Prompt 自訂

```yaml
prompt:
  language: "繁體中文"              # agent 回覆語言
  extra_rules:                      # 附加規則
    - "列出所有相關的檔案名稱與完整路徑"
    - "如果涉及資料庫變更，請提醒需要 migration"
```

## Agent 輸出格式

Agent 輸出兩段，用 `===TRIAGE_METADATA===` 分隔：

1. **Markdown body** — 直接作為 issue 內容
2. **JSON metadata** — bot 用來決定 reject/degrade、issue title、labels

```
## 問題摘要
登入頁面按送出後一直轉圈圈...

## 相關程式碼
- src/api/auth/login.ts:45

===TRIAGE_METADATA===
{
  "issue_type": "bug",
  "confidence": "high",
  "files": [{"path": "src/api/auth/login.ts", "line": 45, "relevance": "login handler"}],
  "open_questions": ["是否所有使用者都受影響？"],
  "suggested_title": "登入頁面送出後無限 loading"
}
```

## Rejection / Degradation

| 情況 | 行為 |
|------|------|
| 正常 triage | 建 issue（Go header + agent markdown） |
| `files=0` 或 `questions>=5`，confidence 非 low | 建 issue，跳過 triage section |
| `confidence=low` | 拒絕（可能選錯 repo） |

## Slack App 設定

Bot Token Scopes：
- `chat:write`, `channels:read`, `channels:history`, `users:read`, `commands`
- 私人頻道：`groups:history`, `groups:read`

Event Subscriptions：
- `app_mention`
- auto-bind：`member_joined_channel`, `member_left_channel`

Slash Command：
- `/triage`

Socket Mode 啟用，App-Level Token scope `connections:write`。

## 架構

```
cmd/bot/main.go              # entry point, Socket Mode event loop
internal/
  config/config.go           # YAML config: agents, channels, prompt, rate limits
  bot/
    workflow.go              # trigger → interact → spawn agent → parse → issue
    agent.go                 # AgentRunner: spawn CLI agent with fallback chain
    parser.go                # parse markdown + ===TRIAGE_METADATA=== + JSON
    prompt.go                # build minimal user prompt for CLI agent
    enrich.go                # expand Mantis URLs in messages
  slack/
    client.go                # PostMessage/PostSelector/FetchThreadContext/DownloadAttachments
    handler.go               # TriggerEvent dedup, rate limiting, bounded concurrency
  github/
    issue.go                 # CreateIssue(ctx, owner, repo, title, body, labels)
    repo.go                  # RepoCache: clone, fetch, branch list, checkout
    discovery.go             # GitHub API repo discovery with cache
  mantis/                    # Mantis bug tracker URL enrichment
agents/
  skills/                    # Agent skills (symlinked to global dirs)
    triage-issue.md
  setup.sh                   # Setup symlinks for local dev
```

## 測試

```bash
go test ./...   # 69 tests
```

## Build

```bash
./run.sh
# 或
go build -o bot ./cmd/bot/ && ./bot -config config.yaml
```

## License

MIT
