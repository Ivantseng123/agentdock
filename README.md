# Slack Issue Bot

Slack emoji reaction triggers automatic GitHub issue creation with AI-powered codebase diagnosis.

## How It Works

1. Someone posts a bug report or feature request in a Slack channel
2. A team member reacts with a configured emoji (e.g., `:bug:` or `:rocket:`)
3. The bot automatically:
   - Fetches the Slack message
   - Clones/pulls the mapped GitHub repo
   - Greps the codebase for relevant files
   - Sends context to an LLM for diagnosis
   - Creates a structured GitHub issue with the AI analysis
   - Posts the issue URL back to the Slack channel

## Prerequisites

| Item | How to Get |
|------|-----------|
| Go 1.22+ | [go.dev/dl](https://go.dev/dl/) |
| Slack App | [api.slack.com/apps](https://api.slack.com/apps) |
| GitHub PAT | GitHub Settings > Developer settings > Personal access tokens |
| LLM API Key | At least one of: Anthropic / OpenAI / local Ollama |

### Slack App Setup

1. Create a new app at [api.slack.com/apps](https://api.slack.com/apps)
2. **OAuth & Permissions** - add Bot Token Scopes:
   - `reaction_read`
   - `channels:history`
   - `chat:write`
   - `users:read`
3. **Socket Mode** - enable it
4. **Basic Information** - generate an App-Level Token with scope `connections:write` (this gives you the `xapp-` token)
5. **Event Subscriptions** - enable and subscribe to `reaction_added` bot event
6. **Install to Workspace** - install and copy the `xoxb-` Bot Token

### GitHub Token

Create a Personal Access Token with:
- `repo` scope (for private repos)
- or `public_repo` (for public repos only)

## Quick Start

```bash
# Clone
git clone https://github.com/your-org/slack-issue-bot.git
cd slack-issue-bot

# Copy and edit config
cp config.yaml config.local.yaml
# Edit config.local.yaml with your tokens (see Configuration below)

# Run
go run ./cmd/bot/ -config config.local.yaml
```

## Configuration

```yaml
server:
  port: 8080                    # Health check port

slack:
  bot_token: "xoxb-..."        # Bot User OAuth Token
  signing_secret: "..."         # App Credentials > Signing Secret
  app_token: "xapp-..."        # App-Level Token (Socket Mode)

# Channel ID -> GitHub repo mapping
channels:
  C05XXXXXX:                    # Find channel ID: right-click channel > View channel details
    repo: "your-org/backend"
    default_labels: ["from-slack"]
  C05YYYYYY:
    repo: "your-org/frontend"
    default_labels: ["from-slack", "frontend"]

# Emoji -> workflow type
reactions:
  bug:                          # :bug: emoji
    type: "bug"
    issue_labels: ["bug", "triage"]
    issue_title_prefix: "[Bug]"
  rocket:                       # :rocket: emoji
    type: "feature"
    issue_labels: ["enhancement"]
    issue_title_prefix: "[Feature]"

github:
  token: "ghp_..."              # Personal Access Token

# LLM providers (tried in order, falls back on failure)
llm:
  providers:
    - name: "claude"
      api_key: "sk-ant-..."
      model: "claude-sonnet-4-20250514"
      base_url: "https://api.anthropic.com"
    - name: "openai"
      api_key: "sk-..."
      model: "gpt-4o"
      base_url: "https://api.openai.com"
    - name: "ollama"            # Free, local, no API key needed
      model: "llama3"
      base_url: "http://localhost:11434"
  timeout: 30s
  max_retries: 2

repo_cache:
  dir: "/tmp/slack-issue-bot/repos"
  max_age: 1h                  # Re-pull after this duration

rate_limit:
  per_user: 5                  # Max triggers per user per window
  per_channel: 20              # Max triggers per channel per window
  window: 1h                   # Time window

# Diagnosis mode
diagnosis:
  mode: "lite"                 # "full" or "lite" (see below)
```

### Diagnosis Modes

| Mode | LLM Cost | What Happens |
|------|----------|-------------|
| `full` | ~5K tokens/trigger | Bot calls centralized LLM for diagnosis, writes full analysis in issue |
| `lite` | **0 tokens** | Bot greps repo for related files, writes a **handoff spec** in the issue |

**Lite mode** is the recommended default for shared deployments. The issue includes:
- Related file paths (found by grepping keywords from the Slack message)
- A ready-to-paste prompt that the assigned developer can feed to their own AI (Claude Code, Copilot, etc.)

This means the **pod's centralized LLM cost stays at zero**, and each developer uses their own AI budget to investigate.

**Full mode** falls back to lite automatically if all LLM providers fail.

### Environment Variable Overrides

Sensitive values can be set via environment variables (takes precedence over YAML):

```bash
export SLACK_BOT_TOKEN="xoxb-..."
export SLACK_APP_TOKEN="xapp-..."
export SLACK_SIGNING_SECRET="..."
export GITHUB_TOKEN="ghp_..."
export LLM_CLAUDE_API_KEY="sk-ant-..."
export LLM_OPENAI_API_KEY="sk-..."
```

## Local Testing

### 1. Unit Tests

```bash
go test ./... -v
```

### 2. Test with Ollama (Free, No API Key)

```bash
# Install Ollama
brew install ollama

# Pull a model
ollama pull llama3

# Configure config.local.yaml to use only Ollama:
# llm:
#   providers:
#     - name: "ollama"
#       model: "llama3"
#       base_url: "http://localhost:11434"

# Run the bot
go run ./cmd/bot/ -config config.local.yaml
```

### 3. End-to-End Test

1. Start the bot: `go run ./cmd/bot/ -config config.local.yaml`
2. Verify health check: `curl http://localhost:8080/healthz` -> `ok`
3. Go to a configured Slack channel
4. Post a message like: "Login page crashes after clicking submit button"
5. React to the message with `:bug:`
6. The bot should create a GitHub issue and post the link back

### 4. Test with a Dedicated Slack Channel

Recommended: create a `#bot-testing` channel and map it to a test repo in your config. This avoids noise in real channels.

## Issue Output Examples

### Bug Report (:bug:)

The created GitHub issue will look like:

```
[Bug] Login page crashes after clicking submit button

### Source
- Slack Channel: #backend-bugs
- Reporter: @ivan
- Original Message: Login page crashes after clicking submit button

### AI Diagnosis

**Possible Cause:**
JWT decode fails when the token payload is missing the `exp` field.

**Potentially Related Files:**
- `src/auth/jwt.go:45` — DecodeToken lacks nil check for claims
- `src/handlers/login.go:78` — Login handler doesn't validate token

**Suggested Fix Direction:**
1. Add nil check in DecodeToken for missing claims
2. Validate token structure before returning to handler
```

### Feature Request (:rocket:)

```
[Feature] Support batch CSV export

### Source
- Slack Channel: #product
- Requester: @ivan
- Original Message: Need to export multiple records as CSV at once

### AI Analysis

**Existing Related Functionality:**
Single-record export exists in src/export/single.go using encoding/csv.

**Suggested Implementation Location:**
- `src/export/single.go:15` — Existing export logic to extend
- `src/handlers/export.go:32` — Handler needs batch endpoint

**Complexity Assessment:** medium
```

## Docker

```bash
# Build
docker build -t slack-issue-bot .

# Run
docker run -v $(pwd)/config.local.yaml:/config.yaml slack-issue-bot
```

Image size: ~15MB (Alpine + git).

## Architecture

```
Slack (reaction_added via Socket Mode)
  -> Handler (dedup + semaphore, max 5 concurrent)
    -> Workflow Orchestrator
      -> Fetch Slack message
      -> Clone/pull repo (shallow, cached)
      -> Grep for relevant files
      -> LLM diagnosis (fallback chain with retry)
      -> Create GitHub issue
      -> Post issue URL to Slack
```

## License

MIT
