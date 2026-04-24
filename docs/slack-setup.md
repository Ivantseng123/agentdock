# Slack App 設定

[English](slack-setup.en.md)

## Scopes 與事件

Bot Token Scopes：
- `chat:write`, `channels:read`, `channels:history`, `users:read`, `commands`
- 私人頻道：`groups:history`, `groups:read`
- 附件下載：`files:read`
- 附件上傳：`files:write`（`@bot ask` 長答案以 `answer.md` 附檔回覆）

Event Subscriptions：
- `app_mention`
- auto-bind：`member_joined_channel`, `member_left_channel`

Interactivity：
- 啟用（用於 repo/branch 選擇按鈕 + 取消按鈕 + 補充說明 modal）

Slash Command：
- `/triage`（只是 fallback：Slack 不讓 slash command 看到 thread context，所以實際用 `@bot <verb>` 觸發，見下）

Socket Mode 啟用，App-Level Token scope `connections:write`。

## 觸發方式

在 thread 裡 mention bot 並挑動詞：

| 指令 | 動作 |
|------|------|
| `@bot` | 跳出 issue / ask / review 三顆按鈕 |
| `@bot issue` · `@bot owner/repo` | 走 IssueWorkflow（建 GitHub issue） |
| `@bot ask <問題>` | 走 AskWorkflow（直接 thread 內回答） |
| `@bot review <PR URL>` | 走 PRReviewWorkflow（在 PR 上留 review） |

`/triage` 如果被呼叫，bot 會回一條提示訊息叫使用者改用 `@bot`。

## 用 Manifest 快速建立 App

https://api.slack.com/apps → **Create New App** → **From a manifest**，貼以下 YAML：

```yaml
display_information:
  name: AgentDock
  description: Turn Slack threads into GitHub issues, thread-grounded answers, or PR reviews
  background_color: "#1f2937"

features:
  bot_user:
    display_name: AgentDock
    always_online: true
  slash_commands:
    - command: /triage
      # 保留只為 backward compat — bot 收到 /triage 會回覆改用 @bot 的提示
      description: (legacy) see `@bot`
      usage_hint: "use @bot in a thread instead"
      should_escape: false

oauth_config:
  scopes:
    bot:
      - app_mentions:read
      - channels:history
      - channels:read
      - groups:history
      - groups:read
      - chat:write
      - commands
      - files:read
      - files:write
      - users:read

settings:
  event_subscriptions:
    bot_events:
      - app_mention
      - member_joined_channel
      - member_left_channel
  interactivity:
    is_enabled: true
  socket_mode_enabled: true
  token_rotation_enabled: false
```

建立後：
1. **Basic Information** → **App-Level Tokens** → **Generate** → 加 `connections:write` scope → 複製 `xapp-…` 作為 `SLACK_APP_TOKEN`
2. **Install App** → 複製 `xoxb-…` 作為 `SLACK_BOT_TOKEN`
