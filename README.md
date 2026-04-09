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
    agent 探索 codebase + 判斷 confidence + 建 GitHub issue（或拒絕）
  → 回傳 issue URL → post 到 Slack thread
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

Skills 以目錄形式集中管理在 `agents/skills/`，透過 symlink 發布到各 agent 的全域設定：

```
agents/
  skills/
    triage-issue/
      SKILL.md           # skill 內容
  setup.sh               # local 開發：建 symlink（run.sh 自動呼叫）
```

新增 skill：在 `agents/skills/` 下建目錄 + `SKILL.md`，`setup.sh` 會自動 link 到 Claude Code 和 OpenCode 的全域目錄。

### Prompt 自訂

```yaml
prompt:
  language: "繁體中文"              # agent 回覆語言
  extra_rules:                      # 附加規則
    - "列出所有相關的檔案名稱與完整路徑"
    - "如果涉及資料庫變更，請提醒需要 migration"
```

## Agent 行為

Agent 收到 prompt 後：
1. 載入 triage-issue skill（從全域 `~/.claude/skills/`）
2. 探索 codebase（用自己的內建工具）
3. 評估 confidence（low → 拒絕建 issue）
4. 用 `gh issue create` 建立 GitHub issue
5. 回傳結果標記：

```
===TRIAGE_RESULT===
CREATED: https://github.com/owner/repo/issues/42
```

或拒絕：

```
===TRIAGE_RESULT===
REJECTED: 問題與此 repo 的程式碼關聯性不足
```

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

## 部署

### Local

```bash
./run.sh
# 或
go build -o bot ./cmd/bot/ && ./bot -config config.yaml
```

### Docker

```bash
docker build -t react2issue .
docker run -e SLACK_BOT_TOKEN=xoxb-... \
           -e SLACK_APP_TOKEN=xapp-... \
           -e GH_TOKEN=ghp_... \
           -e CLAUDE_AUTH_TOKEN=... \
           react2issue
```

### Kubernetes

使用 Kustomize：

```
deploy/
  base/                          # 通用 deployment（進 repo）
    kustomization.yaml
    deployment.yaml
  overlays/
    example/                     # 範本（進 repo）
      kustomization.yaml.example
      secret.yaml.example
    <your-env>/                  # 實際設定（gitignored）
      kustomization.yaml
      secret.yaml
```

```bash
# 建立 overlay
cp deploy/overlays/example/*.example deploy/overlays/my-env/
# 編輯設定
vi deploy/overlays/my-env/kustomization.yaml
vi deploy/overlays/my-env/secret.yaml
# 部署
kubectl apply -k deploy/overlays/my-env/
```

Claude CLI 認證：本地跑 `claude setup-token` 取得 token，存入 k8s Secret 的 `CLAUDE_AUTH_TOKEN`。

### CI/CD (Jenkins)

| Pipeline | 說明 |
|----------|------|
| `Jenkinsfile-bump-version` | semver 檢查 → go test → 自動 PR merge 更新版號 |
| `Jenkinsfile-release` | docker build/push → GitHub Release |

Registry、image name、credential ID 透過 Jenkins parameters 注入，不寫死在 repo。

## 架構

```
cmd/bot/main.go              # entry point, Socket Mode event loop
internal/
  config/config.go           # YAML config: agents, channels, prompt, rate limits
  bot/
    workflow.go              # trigger → interact → spawn agent → parse result
    agent.go                 # AgentRunner: spawn CLI agent with fallback chain
    parser.go                # parse ===TRIAGE_RESULT=== (CREATED/REJECTED/ERROR)
    prompt.go                # build minimal user prompt for CLI agent
    enrich.go                # expand Mantis URLs in messages
  slack/
    client.go                # PostMessage/PostSelector/FetchThreadContext/DownloadAttachments
    handler.go               # TriggerEvent dedup, rate limiting, bounded concurrency
  github/
    issue.go                 # CreateIssue (backup, agent handles issue creation)
    repo.go                  # RepoCache: clone, fetch, branch list, checkout
    discovery.go             # GitHub API repo discovery with cache
  mantis/                    # Mantis bug tracker URL enrichment
agents/
  skills/
    triage-issue/SKILL.md    # Agent skill: triage + gh issue create
  setup.sh                   # Setup symlinks for local dev
deploy/
  base/                      # Kustomize base (deployment)
  overlays/example/          # Overlay template (secret.yaml.example)
```

## 測試

```bash
go test ./...
```

## License

MIT
