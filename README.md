# react2issue

[English](README.en.md)

Slack reaction → AI codebase triage → GitHub Issue。Go 單一 binary，Socket Mode（不需公開 URL）。

支援 Slack 附件：xlsx（解析成文字表格）、jpg/png（透過 vision API 讓 AI 看到截圖）。

## Quick Start

```bash
cp config.example.yaml config.yaml
# 填入 Slack / GitHub / LLM token
go run ./cmd/bot/
```

## 流程

```
reaction event → dedup + rate limit → repo/branch 選擇（thread 內）
  → 抓取訊息 + 附件（text/xlsx inline, jpg/png 下載）
  → pre-grep（原文關鍵字）→ agent loop（LLM 呼叫工具 + 看圖片）→ triage card
  → confidence=low? 拒絕 : files=0? 建 issue 但跳過 triage : 建完整 issue
  → issue title 取 AI summary → post issue URL in thread
```

## 設定

完整選項見 `config.example.yaml`。

```yaml
auto_bind: true                       # bot 加入頻道自動綁定

channel_defaults:
  branch_select: true
  default_labels: ["from-slack"]

channels:                             # 靜態綁定（可選，auto_bind 時可不設）
  C05XXXXXX:
    repos: ["org/backend", "org/frontend"]
    branch_select: true

reactions:
  bug:    { type: "bug",     issue_labels: ["bug", "triage"], issue_title_prefix: "[Bug]" }
  rocket: { type: "feature", issue_labels: ["enhancement"],   issue_title_prefix: "[Feature]" }

llm:
  timeout: 60s                        # 全域預設
  providers:
    - name: "cli"                     # CLI provider：任何支援 --print 或 stdin 的工具
      command: "claude"
      args: ["--print", "{prompt}"]
      timeout: 5m
      max_retries: 3
    - name: "claude"                  # Anthropic API
      api_key: "sk-ant-..."
      model: "claude-sonnet-4-20250514"
      base_url: "https://api.anthropic.com"
      max_retries: 3
    - name: "openai"                  # OpenAI-compatible API
      api_key: "sk-..."
      model: "gpt-4o"
      base_url: "https://api.openai.com"
    - name: "ollama"                  # Local LLM
      model: "llama3"
      base_url: "http://localhost:11434"

diagnosis:
  mode: "full"                        # "full" | "lite"（grep only, 0 token）
  max_turns: 5                        # agent loop 回合上限
  max_tokens: 100000                  # token budget
  cache_ttl: 10m                      # 相同訊息快取（0 = 不快取）
  prompt:
    language: "繁體中文"              # 輸出語言（留空 = English）
    extra_rules: []                   # 附加到 system prompt 尾端的自訂規則
```

### extra_rules

字串陣列，原封不動附加到 system prompt。用來客製 AI 行為：

```yaml
extra_rules:
  - "列出所有相關的檔案名稱與完整路徑"
  - "如果涉及資料庫變更，請在 Direction 中提醒需要 migration"
  - "若找到相關的單元測試檔案，也要列出"
```

### CLI Provider

`{prompt}` 為 placeholder — prompt < 32KB 嵌入 args，否則走 stdin。無 `{prompt}` 時一律 stdin。

```yaml
# 範例：任何支援 stdin 的工具都可以
- name: "cli"
  command: "my-ai-tool"
  args: []
  timeout: 3m
```

### 環境變數

```bash
SLACK_BOT_TOKEN=xoxb-...
SLACK_APP_TOKEN=xapp-...
GITHUB_TOKEN=ghp_...
LLM_CLAUDE_API_KEY=sk-ant-...    # 格式：LLM_{NAME}_API_KEY
```

## Rejection / Degradation

| 情況 | 行為 |
|------|------|
| triage 正常 | issue + AI triage section |
| `files=0` 或 `questions>=5`，confidence 非 low | issue，跳過 triage section |
| `confidence=low` | 拒絕（可能選錯 repo） |

## 診斷引擎

Agent loop — LLM 自行決定使用哪些工具：

```
1. Pre-grep（免費）
   原文關鍵字 git grep → 捕捉非英文命中

2. Agent Loop（max_turns 回合）
   LLM 看到 pre-grep 結果 + 6 個工具 → 自行呼叫 → engine 執行 → 結果回傳
   → 直到 LLM 產出 triage card 或回合用完（forced finish）

3. Output: triage card JSON
   summary / files / direction / open_questions / confidence
```

| 工具 | 說明 |
|------|------|
附件支援：

| 附件類型 | 處理方式 |
|----------|----------|
| txt, csv, json, xml... | 下載內容 inline（max 500 行） |
| xlsx | excelize 解析成 TSV inline（max 200 行/sheet） |
| jpg/png | 下載 bytes → vision API（Claude/OpenAI/CLI），Ollama fallback 文字 |
| 其他 | 僅顯示 `[附件: name](permalink)` |

限制：單張圖片 max 20MB，每則訊息 max 5 張圖片。

| 工具 | 說明 |
|------|------|
| `grep` | `git grep -rli` 搜檔案 |
| `read_file` | 讀取檔案內容（cap 200 行） |
| `list_files` | `git ls-files`（cap 500） |
| `read_context` | 讀 README.md / CLAUDE.md / agent.md |
| `search_code` | regex search + context lines |
| `git_log` | recent commits |

## Issue 輸出範例

```markdown
**Channel:** #dev-general | **Reporter:** Alice

> 使用者登入頁面，輸入空白密碼按送出後頁面直接當掉

### AI Triage
登入表單的送出處理缺少空欄位驗證，在呼叫 auth API 前未檢查密碼是否為空

### Related Files
- [`LoginPage.vue`](https://github.com/example/webapp/blob/main/src/pages/LoginPage.vue) — 登入頁面，含表單送出邏輯
- [`auth.api.js`](https://github.com/example/webapp/blob/main/src/api/auth.api.js) — 認證 API
- [`validation.js`](https://github.com/example/webapp/blob/main/src/utils/validation.js) — 表單驗證工具，可參考其做法

### Direction
- LoginPage.vue 表單送出前加入空欄位驗證，可參考 validation.js
- 確認 auth.api.js 是否有 server-side 驗證

### Needs Clarification
- 所有瀏覽器都會發生？
- 有錯誤訊息還是直接卡住？
```

## Slack App 設定

Bot Token Scopes：
- `reaction_read`, `channels:history`, `chat:write`, `users:read`, `channels:read`
- 私人頻道：`groups:history`, `groups:read`

Event Subscriptions：
- `reaction_added`
- auto-bind：`member_joined_channel`, `member_left_channel`

Socket Mode 啟用，App-Level Token scope `connections:write`。

## 架構

```
cmd/bot/main.go           # entry point, Socket Mode event loop
internal/
  bot/workflow.go          # reaction → repo/branch selection → rejection → issue
  diagnosis/
    engine.go              # agent loop + cache + lite mode
    loop.go                # pre-grep → LLM tool calls → forced finish
    tools.go               # grep, read_file, list_files, read_context, search_code, git_log
    cache.go               # in-memory TTL cache
  llm/
    provider.go            # ConversationProvider, ChatFallbackChain
    claude.go              # Anthropic native tool use + vision
    openai.go              # OpenAI function calling + vision
    cli.go                 # JSON-in-text tool simulation + --file vision
    ollama.go              # JSON-in-text tool simulation (vision fallback)
    prompt.go              # system prompt + tool descriptions
  slack/
    client.go              # PostMessage, PostSelector, FetchMessage (attachments)
    xlsx.go                # xlsx → TSV parser
    handler.go             # dedup, rate limit, concurrency
  github/
    issue.go               # issue body formatter + file permalinks
    repo.go                # clone, fetch, branch, checkout
    discovery.go           # GitHub API repo listing + cache
  config/config.go         # YAML config + env overrides
```

## 測試

```bash
go test ./...   # 89 tests
```

## Build

```bash
./run.sh
# 或
go build -o bot ./cmd/bot/ && ./bot -config config.yaml
```

## License

MIT
