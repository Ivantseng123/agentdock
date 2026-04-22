# Slack App Setup

[繁體中文](slack-setup.md)

## Scopes & Events

Bot Token Scopes:
- `chat:write`, `channels:read`, `channels:history`, `users:read`, `commands`
- Private channels: `groups:history`, `groups:read`
- Attachment downloads: `files:read`

Event Subscriptions:
- `app_mention`
- auto-bind: `member_joined_channel`, `member_left_channel`

Interactivity:
- Enabled (for repo/branch selection buttons + cancel button + description modal)

Slash Command:
- `/triage` (fallback only — Slack doesn't expose thread context to slash commands, so the real trigger is `@bot <verb>`, see below)

Socket Mode enabled, App-Level Token scope `connections:write`.

## How to trigger

Mention the bot inside a thread and pick a verb:

| Command | Action |
|---------|--------|
| `@bot` | Posts a three-button selector (issue / ask / review) |
| `@bot issue` · `@bot owner/repo` | IssueWorkflow (creates GitHub issue) |
| `@bot ask <question>` | AskWorkflow (answers inline in the thread) |
| `@bot review <PR URL>` | PRReviewWorkflow (posts review comments on the PR) |

If `/triage` is invoked the bot replies with a hint telling the user to switch to `@bot`.

## Create App from Manifest

https://api.slack.com/apps → **Create New App** → **From a manifest**, paste this YAML:

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
      # Kept for backward compat only — bot responds to /triage with a hint to use @bot.
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

After creation:
1. **Basic Information** → **App-Level Tokens** → **Generate** → add `connections:write` scope → copy `xapp-…` as `SLACK_APP_TOKEN`
2. **Install App** → copy `xoxb-…` as `SLACK_BOT_TOKEN`
