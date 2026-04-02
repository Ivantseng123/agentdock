# Slack Issue Bot

[繁體中文](README.md)

Slack emoji reactions trigger automatic GitHub issue creation with AI-powered codebase triage.

## How It Works

1. Someone posts a bug report or feature request in a Slack channel
2. A team member reacts with a configured emoji (e.g., `:bug:` or `:rocket:`)
3. The bot replies **in the message thread**:
   - Shows repo selector (searchable dropdown or buttons)
   - Shows branch selector (if enabled)
   - Clones/pulls the GitHub repo
   - Runs AI diagnosis engine to analyze the codebase
   - **Rejection check** — if the report is too vague, replies asking for clarification instead of creating an issue
   - Creates a GitHub issue with clickable file links and posts the URL in thread

## Features

- **Thread-based interaction** — all bot messages stay in the original message's thread
- **Multi-repo support** — one channel can map to multiple repos via button or searchable dropdown
- **Branch selection** — optionally pick which branch to analyze
- **Cross-repo awareness** — reads README/CLAUDE.md/agent.md to understand repo context and relationships
- **Rejection mechanism** — refuses to create issues when the report is too vague (no related files, too many unknowns, or low confidence)
- **GitHub file links** — file references in issues are clickable links to the actual source
- **LLM fallback chain** — multiple providers with per-provider retry and timeout
- **CLI provider** — integrate any CLI tool (claude, opencode, etc.) with your own subscription, zero API cost
- **Lite mode** — grep-only triage with zero LLM cost
- **Rate limiting** — per-user and per-channel throttling
- **Auto-bind** — bot auto-registers when joining a channel, no manual config needed
- **Response caching** — identical messages within TTL return cached results

## Prerequisites

| Item | How to Get |
|------|-----------|
| Go 1.22+ | [go.dev/dl](https://go.dev/dl/) |
| Slack App | [api.slack.com/apps](https://api.slack.com/apps) |
| GitHub PAT | GitHub Settings > Developer settings > Personal access tokens |
| LLM Provider | CLI tool / API key (Anthropic/OpenAI) / Ollama (free) |

### Slack App Setup

1. Create a new app at [api.slack.com/apps](https://api.slack.com/apps)
2. **OAuth & Permissions** — add Bot Token Scopes:
   - `reaction_read`, `channels:history`, `chat:write`, `users:read`, `channels:read`
   - For private channels: `groups:history`, `groups:read`
3. **Socket Mode** — enable it
4. **Basic Information** — generate an App-Level Token with scope `connections:write` (gives you the `xapp-` token)
5. **Event Subscriptions** — subscribe to `reaction_added` bot event
   - For auto-bind: also subscribe to `member_joined_channel`, `member_left_channel`
6. **Install to Workspace** — copy the `xoxb-` Bot Token

## Quick Start

```bash
cp config.example.yaml config.yaml
# Edit config.yaml with your tokens

# Run
go run ./cmd/bot/
# or
./run.sh
```

## Configuration

See `config.example.yaml` for all options. Key sections:

```yaml
auto_bind: true                       # Auto-register when bot joins a channel

channel_defaults:                     # Defaults for auto-bound channels
  branch_select: true
  default_labels: ["from-slack"]

channels:                             # Static channel config (optional)
  C05XXXXXX:
    repos:                            # Multiple repos -> button selector in Slack
      - "org/backend"
      - "org/frontend"
    branch_select: true               # Show branch picker
    default_labels: ["from-slack"]

reactions:                            # Emoji mappings
  bug:
    type: "bug"
    issue_labels: ["bug", "triage"]
    issue_title_prefix: "[Bug]"
  rocket:
    type: "feature"
    issue_labels: ["enhancement"]
    issue_title_prefix: "[Feature]"

llm:
  providers:
    - name: "cli"
      command: "claude"               # Any CLI tool with --print support
      args: ["--print", "{prompt}"]
      timeout: 5m                     # CLI needs more time than API
      max_retries: 3

    - name: "claude"                  # API fallback
      api_key: "sk-ant-..."
      model: "claude-sonnet-4-20250514"
      base_url: "https://api.anthropic.com"
      max_retries: 3
  timeout: 60s                        # Global default (per-provider overrides this)

diagnosis:
  mode: "full"                        # "full" (uses LLM) or "lite" (grep only)
  max_turns: 5                        # Max agent loop iterations
  max_tokens: 100000                  # Token budget limit
  cache_ttl: 10m                      # Response cache TTL (0 = no caching)
  prompt:
    language: "English"
    extra_rules: []
```

### Diagnosis Modes

| Mode | LLM Cost | Description |
|------|----------|-------------|
| `full` | tokens per trigger | Agent loop: LLM uses tools (grep, read_file, etc.) in multi-turn conversation until diagnosis complete |
| `lite` | **0 tokens** | Grep only, creates issue with file references for engineer's own AI |

### Rejection & Graceful Degradation

The core value of this tool is **structuring Slack conversations into issues**, lowering the barrier for non-engineers to create useful issues. AI triage is a bonus, not a requirement.

| Situation | Behavior |
|-----------|----------|
| AI analysis succeeds (files found, confidence OK) | Create issue with full AI Triage section |
| AI finds no files / too many open questions, but confidence is not low | Create issue, **skip AI Triage section** (keep original message + Channel + Reporter only) |
| `confidence = low` | **Reject** (likely wrong repo or completely irrelevant) |

When rejected, the bot replies in thread asking the reporter to verify the repo or refine their description.

### CLI Provider

Integrate any CLI tool that supports stdin or `--print` mode, using your own subscription at zero API cost:

```yaml
# Example: Claude Code CLI
- name: "cli"
  command: "claude"
  args: ["--print", "{prompt}"]
  timeout: 5m

# Example: other CLI tools (prompt via stdin)
- name: "cli"
  command: "my-ai-tool"
  args: []
  timeout: 3m
```

`{prompt}` is a placeholder — when prompt < 32KB it's embedded in args, otherwise sent via stdin. If args contain no `{prompt}`, stdin is always used.

### Environment Variable Overrides

```bash
export SLACK_BOT_TOKEN="xoxb-..."
export SLACK_APP_TOKEN="xapp-..."
export GITHUB_TOKEN="ghp_..."
export LLM_CLAUDE_API_KEY="sk-ant-..."
```

## Issue Output Example

```markdown
**Channel:** #dev-general | **Reporter:** Alice

> Login page crashes when clicking submit with empty password

### AI Triage

The login form submission handler lacks empty field validation before calling the auth API

### Related Files

- [`LoginPage.vue`](https://github.com/example/webapp/blob/main/src/pages/LoginPage.vue) — Login page component with form submit logic
- [`auth.api.js`](https://github.com/example/webapp/blob/main/src/api/auth.api.js) — Auth API calls, may need input validation
- [`validation.js`](https://github.com/example/webapp/blob/main/src/utils/validation.js) — Existing form validation utils, can be referenced

### Direction

- Add empty field validation in LoginPage.vue before form submit, reference validation.js for patterns
- Check if auth.api.js has server-side validation as a fallback

### Needs Clarification

- Does the crash happen on all browsers or just specific ones?
- Is there an error message displayed, or does the page just freeze?
```

## Testing

```bash
go test ./...   # 76 tests
```

## Docker

```bash
docker build -t slack-issue-bot .
docker run -v $(pwd)/config.yaml:/config.yaml slack-issue-bot
```

## Architecture

```
Slack reaction -> Socket Mode -> Handler (dedup + rate limit + concurrency)
  -> Workflow (repo/branch selection via thread buttons)
    -> Diagnosis Engine
    -> Rejection check (files=0, questions>=5, confidence=low)
    -> GitHub Issue (clickable file links) -> Post URL in thread
```

### Diagnosis Engine

The engine uses an **agent loop** — an LLM-driven multi-turn conversation where the model decides which tools to use and when it has enough information to produce a triage card.

```
1. Pre-grep (free, no LLM call)
   Extract keywords from the original Slack message and run git grep.
   This catches non-English terms (e.g. Chinese) that the LLM might miss
   when translating to English identifiers.

2. Agent Loop (up to max_turns, default 5)
   The LLM sees the pre-grep results + available tools and decides:
   +----------------------------------------------------+
   |  LLM: "I want to read sectionInfo.vue"             |
   |  Engine: executes read_file, returns content        |
   |  LLM: "I need to grep for unitno"                  |
   |  Engine: executes grep, returns file list           |
   |  LLM: "I have enough info -> triage card"          |
   +----------------------------------------------------+

3. Output: Triage Card (JSON)
   summary, related files, direction, open questions, confidence level
```

**Available tools (6):**

| Tool | Purpose |
|------|---------|
| `grep` | Find which files mention a term (broad discovery) |
| `read_file` | Read file content with line numbers |
| `list_files` | List repo file tree (`git ls-files`) |
| `read_context` | Read README.md, CLAUDE.md, agent.md for repo context |
| `search_code` | Regex search with surrounding context lines |
| `git_log` | View recent commits |

**Why agent loop instead of a fixed pipeline:**
- LLM adapts its strategy per report — a clear report might need 2 turns, a vague one uses all 5
- Non-English messages are handled naturally — the LLM translates as part of its reasoning
- The pre-grep ensures original-language keyword hits are never missed
- Repos with better documentation (README, CLAUDE.md) produce more accurate triage

## License

MIT
