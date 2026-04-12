# agentdock

[繁體中文](README.md)

Slack reaction → AI codebase triage → GitHub Issue. Single Go binary, Socket Mode (no public URL needed).

## Quick Start

```bash
cp config.example.yaml config.yaml
# Fill in Slack / GitHub / LLM tokens
go run ./cmd/bot/
```

## Flow

```
reaction event → dedup + rate limit → repo/branch selection (in thread)
  → pre-grep (original keywords) → agent loop (LLM calls tools) → triage card
  → confidence=low? reject : files=0? issue without triage : full issue
  → post issue URL in thread
```

## Configuration

See `config.example.yaml` for all options.

```yaml
auto_bind: true                       # auto-register when bot joins a channel

channel_defaults:
  branch_select: true
  default_labels: ["from-slack"]

channels:                             # static binding (optional with auto_bind)
  C05XXXXXX:
    repos: ["org/backend", "org/frontend"]
    branch_select: true

reactions:
  bug:    { type: "bug",     issue_labels: ["bug", "triage"], issue_title_prefix: "[Bug]" }
  rocket: { type: "feature", issue_labels: ["enhancement"],   issue_title_prefix: "[Feature]" }

llm:
  timeout: 60s                        # global default
  providers:
    - name: "cli"                     # CLI provider: any tool with --print or stdin
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
  mode: "full"                        # "full" | "lite" (grep only, 0 tokens)
  max_turns: 5                        # agent loop turn limit
  max_tokens: 100000                  # token budget
  cache_ttl: 10m                      # cache identical messages (0 = no cache)
  prompt:
    language: "English"               # output language (empty = English)
    extra_rules: []                   # appended to system prompt verbatim
```

### extra_rules

String array, appended verbatim to the system prompt. Customize AI behavior:

```yaml
extra_rules:
  - "List all related file names with full paths"
  - "If the change involves database updates, mention migration in Direction"
  - "If related unit test files are found, include them"
```

### CLI Provider

`{prompt}` is a placeholder — embedded in args when < 32KB, otherwise piped via stdin. No `{prompt}` in args = always stdin.

```yaml
# Any tool that reads from stdin works
- name: "cli"
  command: "my-ai-tool"
  args: []
  timeout: 3m
```

### Environment Variables

```bash
SLACK_BOT_TOKEN=xoxb-...
SLACK_APP_TOKEN=xapp-...
GITHUB_TOKEN=ghp_...
LLM_CLAUDE_API_KEY=sk-ant-...    # format: LLM_{NAME}_API_KEY
```

## Rejection / Degradation

| Situation | Behavior |
|-----------|----------|
| Triage succeeds | Issue + AI triage section |
| `files=0` or `questions>=5`, confidence not low | Issue, skip triage section |
| `confidence=low` | Reject (likely wrong repo) |

## Diagnosis Engine

Agent loop — LLM decides which tools to call:

```
1. Pre-grep (free)
   Original keyword git grep → catches non-English hits

2. Agent Loop (max_turns rounds)
   LLM sees pre-grep results + 6 tools → calls tools → engine executes → results back
   → until LLM produces triage card or turns exhausted (forced finish)

3. Output: triage card JSON
   summary / files / direction / open_questions / confidence
```

| Tool | Description |
|------|-------------|
| `grep` | `git grep -rli` file search |
| `read_file` | Read file content (cap 200 lines) |
| `list_files` | `git ls-files` (cap 500) |
| `read_context` | Read README.md / CLAUDE.md / agent.md |
| `search_code` | Regex search + context lines |
| `git_log` | Recent commits |

## Issue Output Example

```markdown
**Channel:** #dev-general | **Reporter:** Alice

> Login page crashes when clicking submit with empty password

### AI Triage
Login form submission lacks empty field validation before calling auth API

### Related Files
- [`LoginPage.vue`](https://github.com/example/webapp/blob/main/src/pages/LoginPage.vue) — Login page with form submit logic
- [`auth.api.js`](https://github.com/example/webapp/blob/main/src/api/auth.api.js) — Auth API calls
- [`validation.js`](https://github.com/example/webapp/blob/main/src/utils/validation.js) — Form validation utils, reference for patterns

### Direction
- Add empty field validation in LoginPage.vue before submit, reference validation.js
- Check if auth.api.js has server-side validation as fallback

### Needs Clarification
- All browsers or specific ones?
- Error message displayed or page just freezes?
```

## Slack App Setup

Bot Token Scopes:
- `reaction_read`, `channels:history`, `chat:write`, `users:read`, `channels:read`
- Private channels: `groups:history`, `groups:read`

Event Subscriptions:
- `reaction_added`
- Auto-bind: `member_joined_channel`, `member_left_channel`

Socket Mode enabled, App-Level Token scope `connections:write`.

## Architecture

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
    claude.go              # Anthropic native tool use
    openai.go              # OpenAI function calling
    cli.go                 # JSON-in-text tool simulation
    ollama.go              # JSON-in-text tool simulation
    prompt.go              # system prompt + tool descriptions
  slack/
    client.go              # PostMessage, PostSelector, PostExternalSelector
    handler.go             # dedup, rate limit, concurrency
  github/
    issue.go               # issue body formatter + file permalinks
    repo.go                # clone, fetch, branch, checkout
    discovery.go           # GitHub API repo listing + cache
  config/config.go         # YAML config + env overrides
```

## Testing

```bash
go test ./...   # 76 tests
```

## Build

```bash
./run.sh
# or
go build -o bot ./cmd/bot/ && ./bot -config config.yaml
```

## License

MIT
