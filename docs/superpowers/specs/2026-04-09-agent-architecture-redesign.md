# Agent Architecture Redesign

## Overview

Replace the custom agent loop and LLM provider abstraction with direct CLI agent invocation. The Go service becomes a thin runtime that handles Slack interaction and GitHub issue creation, while delegating all codebase analysis to external CLI agents (claude, opencode, codex, gemini).

This is a **v2 breaking change**. The trigger model changes from emoji reactions to `@bot` mentions and `/triage` slash commands. There is no backward-compatible transition period — v2 fully replaces v1.

### Slack App Manifest Changes Required
- **Remove**: `reaction_added` event subscription
- **Add**: `app_mention` event subscription
- **Add**: `/triage` slash command registration
- **Add OAuth scope**: `commands` (for slash commands)
- **Keep**: `chat:write`, `channels:read`, `channels:history`, `groups:history`, `im:history`, `users:read`

## Motivation

The current architecture reimplements what CLI agents already do natively:
- Agent loop with turn management (`diagnosis/loop.go`)
- 6 custom tools: grep, read_file, list_files, read_context, search_code, git_log (`diagnosis/tools.go`)
- 4 LLM providers with different tool-use parsing (`llm/claude.go`, `llm/openai.go`, `llm/ollama.go`, `llm/cli.go`)
- JSON-in-text simulation for CLI/Ollama providers
- Token budget management

CLI agents like Claude Code already have superior built-in tools (Bash, Read, Grep, Glob) and handle their own agent loop, token management, and tool execution. The Go service should delegate to them rather than reimplement.

## Architecture

```
+-----------------------------------------------------+
|                    Slack                              |
|  @bot / /triage  <->  Socket Mode                    |
+------------------------+----------------------------+
                         |
                         v
+-----------------------------------------------------+
|              Go Thin Runtime                         |
|                                                      |
|  1. Receive trigger -> read thread history           |
|  2. Enrich: expand Mantis URLs in thread messages    |
|  3. Interactive flow: repo -> branch -> description  |
|  4. AgentRunner.Run(workDir, prompt)                 |
|     - Read active_agent from config                  |
|     - Fallback chain: claude -> opencode -> ...      |
|     - Bounded by max_concurrent semaphore            |
|  5. Post-process: sanitize agent output              |
|  6. Parse: markdown body + JSON metadata             |
|  7. Reject/Degrade decision (based on metadata)      |
|  8. GitHub issue creation (API call)                 |
|     - Inject controlled header (channel, reporter)   |
|  9. Reply to Slack thread                            |
+------------------------+----------------------------+
                         |
              +----------+----------+
              v          v          v
         +--------+ +--------+ +--------+
         | claude | |opencode| | codex  | ...
         | --print| |        | |        |
         +--------+ +--------+ +--------+
              |          |          |
              +----------+----------+
                         v
              Same prompt template
              Same output format
              (markdown + ===TRIAGE_METADATA=== + JSON)
```

## Trigger Model

### Trigger Methods
Both methods are handled through **Socket Mode** (no separate HTTP endpoint needed):
- **App Mention**: `@bot` in a thread — via `app_mention` event
- **Slash Command**: `/triage` in a thread — Socket Mode handles slash commands natively

### Thread-Only Constraint
The bot only operates within threads. If triggered outside a thread (channel-level message), it replies with a prompt to use it in a thread and takes no further action.

### Slash Command Thread Detection

Slack slash commands in Socket Mode include a `channel_id` but thread context varies:
- When `/triage` is typed **in a thread reply**, the payload does **not** include a native `thread_ts`. Instead, use the Slack API to check if the triggering message is a reply by looking at the command's context.
- **Workaround**: After receiving the slash command, call `chat.postMessage` with `thread_ts` set to the slash command's own `message_ts` to anchor the bot's reply. For thread detection, the recommended approach is to use `conversations.history` to find the parent message, or require users to use `@bot` mentions instead of `/triage` for thread-based triggers.
- **Simplification option**: If slash command thread detection proves unreliable, limit `/triage` to `@bot` mention only and keep `/triage` for channel-level help/status commands.

### Slash Command Parameters (optional)
```
/triage                    -> interactive repo selection
/triage owner/repo         -> skip repo selection, show branch selection
/triage owner/repo@branch  -> skip repo + branch, start analysis directly
```

### Thread Context Reading

```go
// FetchThreadContext reads all messages in a thread up to the trigger point.
// Returns messages in chronological order, excluding bot's own messages.
//
// Uses conversations.replies with cursor-based pagination (Slack returns
// max 1000 per call). Pagination continues until all replies are fetched
// or max_thread_messages limit is reached.
func (c *Client) FetchThreadContext(channelID, threadTS, triggerTS string, limit int) ([]ThreadMessage, error)

type ThreadMessage struct {
    User      string    // resolved display name
    Timestamp string    // human-readable time
    Text      string    // message content (with Mantis URLs enriched)
    Files     []string  // downloaded attachment temp paths
}
```

Implementation notes:
- `conversations.replies` requires the parent message's `ts` (= `threadTS`)
- When trigger is a reply, `threadTS` is the parent; filter messages where `ts < triggerTS`
- Pagination: loop with `cursor` until `has_more` is false or message count exceeds limit
- Resolve user IDs to display names via existing `ResolveUser()`

### Issue Type
No longer determined by config. The agent decides the issue type (bug, feature, improvement, question) based on thread context.

## Agent Invocation

### Multi-Agent Support

Agents are configured in YAML with a unified interface:

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

active_agent: claude
fallback: [claude, opencode]
```

### AgentRunner

A thin wrapper (~50 lines) that:
1. Substitutes `{prompt}` into the configured args
2. Executes the command with `workDir` set to the repo path
3. Captures stdout
4. Returns raw output string

```go
type AgentRunner struct {
    Command string
    Args    []string
    Timeout time.Duration
}

func (a *AgentRunner) Run(ctx context.Context, workDir, prompt string) (string, error)
```

### Execution Model
- Stateless: each triage spawns a new process, discarded after use
- Fallback: on failure (timeout, command not found, non-zero exit), try next agent in fallback chain
- All agents exhausted: notify Slack, clear dedup for retry
- Concurrency: bounded by `max_concurrent` semaphore (same pattern as current `handler.go`)

### Rate Limiting & Concurrency

Preserved from current architecture:
- **Semaphore**: `max_concurrent` limits simultaneous agent processes (default 3). Agent spawns block on semaphore acquisition with a timeout of `semaphore_timeout` (default 30s). If timeout expires, notify Slack "system busy" and clear dedup.
- **Per-user rate limit**: preserved from current architecture. Configurable via `rate_limit.per_user` (default 5 per minute). Prevents a single user from flooding the system.
- **Per-channel rate limit**: optional, configurable via `rate_limit.per_channel`. Prevents a single channel from monopolizing agent capacity.
- **Dedup**: prevents duplicate triggers on the same thread (see Dedup section below).

## Prompt Design

The prompt is a minimal user prompt (not a system prompt) to avoid constraining the agent's natural capabilities. The agent already knows how to explore codebases.

### Prompt Structure

```
## Task

Analyze the following thread conversation and triage against the specified
codebase. Produce a report suitable as a GitHub issue body.

## Thread Context

Alice (2026-04-09 10:30):
> Login page spins forever after submit...

Bob (2026-04-09 10:32):
> Same here, is the API down?

## Repository

Path: /repos/owner/repo
Branch: main

## Attachments

The following files have been downloaded to /tmp/triage-abc123/:
- screenshot.png (image — use your file reading tools to view)
- error.log (text — read directly)

## Output Format

Output markdown first (used directly as the issue body),
then a ===TRIAGE_METADATA=== separator, then JSON:

{
  "issue_type": "bug|feature|improvement|question",
  "confidence": "low|medium|high",
  "files": [{"path": "...", "line": 0, "relevance": "..."}],
  "open_questions": [],
  "suggested_title": "..."
}

Response language: zh-TW
```

### Attachment Handling

Attachments are downloaded to a temp dir by the Go runtime before agent invocation:

| Type | Handling |
|------|----------|
| Images (png, jpg, gif) | Download to temp dir. Prompt includes path with hint to use file reading tools. Agent vision support varies by CLI — works when available, gracefully ignored when not. |
| Text files (txt, log, csv) | Download to temp dir. Agent reads directly. |
| Documents (xlsx, pdf, docx) | Download to temp dir. Agent reads if capable, otherwise noted as "unsupported format" in the issue. |
| Download failure | Skip. Prompt notes "download failed" for that attachment. |

### Config-Driven Customization
- `prompt.language`: agent response language
- `prompt.extra_rules`: appended to the prompt tail

## Output Format

Agent output is split by `===TRIAGE_METADATA===` into two parts. The parser uses the **last** occurrence of the separator to avoid false positives if the agent mentions the format in its markdown body.

### Part 1: Markdown Body (agent-produced)

The agent writes the full issue content. The Go runtime then wraps it with a controlled header:

```markdown
<!-- Injected by Go runtime — not from agent output -->
**Channel**: #general
**Reporter**: alice
**Branch**: main

---

<!-- Agent-produced content below -->
## Summary
...

## Related Code
...

## Analysis
...
```

This ensures channel/reporter attribution is always accurate (not influenced by agent or repository content).

### Part 2: JSON Metadata
```go
type AgentMetadata struct {
    IssueType      string    `json:"issue_type"`
    Confidence     string    `json:"confidence"`
    Files          []FileRef `json:"files"`
    OpenQuestions  []string  `json:"open_questions"`
    SuggestedTitle string    `json:"suggested_title"`
}

type FileRef struct {
    Path      string `json:"path"`
    Line      int    `json:"line"`
    Relevance string `json:"relevance"`
}
```

### Output Sanitization

Before using the agent's markdown body as the issue body:
1. Strip HTML tags (prevent XSS in non-GitHub renderers)
2. Enforce max body length (default 65000 chars, GitHub's limit)
3. Channel/reporter header is injected by Go runtime (never from agent output)

### Issue Title

Title resolution chain:
1. `metadata.suggested_title` (if present and non-empty)
2. First line of the agent's markdown body
3. First line of the thread's first message
4. "Untitled issue"

Truncated to 80 characters.

### Issue Labels

The `issue_type` from metadata maps to labels:

| `issue_type` | Label |
|---|---|
| `bug` | `bug` |
| `feature` | `enhancement` |
| `improvement` | `enhancement` |
| `question` | `question` |
| unknown/missing | no type label added |

Plus `default_labels` from channel config (e.g., `from-slack`).

### Parsing Fallbacks

| Scenario | Handling |
|----------|----------|
| Valid `===TRIAGE_METADATA===` + valid JSON | Normal flow |
| Valid `===TRIAGE_METADATA===` + invalid JSON | Degrade: use markdown body, default metadata (confidence=medium) |
| No `===TRIAGE_METADATA===` | Degrade: entire stdout as issue body, no metadata |
| Empty stdout | Agent failure, try fallback |
| Stdout under 50 chars | Agent failure, try fallback (likely garbage output) |

## Interactive Flow

```
Thread: @bot or /triage
    |
    v
Validate: is this in a thread?
    +-- No -> reply "please use in a thread", end
    |
    v
Dedup check (see Dedup section)
    +-- Duplicate -> ignore
    |
    v
Read thread history + download attachments to temp dir
Enrich: expand Mantis URLs in thread messages
    |
    v
Determine repo source
    +-- /triage owner/repo@branch -> skip to "start analysis"
    +-- /triage owner/repo        -> skip to "branch selection"
    +-- config has 1 repo         -> auto-select, go to "branch selection"
    |
    v
Repo selection (buttons or search dropdown)
    | user clicks
    v
Branch selection (buttons)
    +-- only 1 branch -> auto-skip
    | user clicks
    v
Description prompt (optional)
    +-- "skip" -> start directly
    +-- "add description" -> open modal -> user submits
    |
    v
Acquire semaphore (max_concurrent)
    +-- blocked -> wait (with timeout)
    |
    v
Start analysis
    +-- Post "analyzing..." to thread
    +-- EnsureRepo -> checkout branch
    +-- Build prompt (thread context + repo path + attachments + extra_rules)
    +-- AgentRunner.Run()
    +-- Parse output + sanitize
    |
    v
Release semaphore
    |
    v
Reject/Degrade decision
    +-- confidence=low -> reject, notify Slack
    +-- files=0 or open_questions>=5 -> degrade: issue without triage
    |
    v
GitHub issue create
    +-- title: resolved via title chain
    +-- body: Go header + agent's markdown
    +-- labels: default_labels + issue_type label
    |
    v
Reply Slack thread: issue URL
Cleanup temp dir
Clear dedup
```

### Dedup

| Trigger type | Dedup key | Notes |
|---|---|---|
| `@bot` mention | `channelID:threadTS` | Same thread can only be triaged once at a time |
| `/triage` slash command | `channelID:threadTS` | Same key — slash commands in the same thread dedup the same way |
| After completion/failure/timeout | Clear dedup entry | Allows re-triggering |

### Pending Map

The `pending` map (for interactive selection state) is keyed by `selectorTS` — the timestamp of the selector message the bot posts. This is the same strategy as v1. It is separate from the dedup map:

| Map | Key | Purpose |
|---|---|---|
| `dedup` | `channelID:threadTS` | Prevent duplicate triggers on the same thread |
| `pending` | `selectorTS` | Track interactive selection state (repo/branch/description) |

### State Management

```go
type pendingTriage struct {
    ChannelID      string
    ThreadTS       string
    TriggerTS      string              // the message that triggered the bot
    Attachments    []string            // temp file paths
    SelectedRepo   string
    SelectedBranch string
    Phase          string              // "repo", "branch", "description"
    SelectorTS     string
    Reporter       string              // resolved display name
    ChannelName    string
}
```

Thread context is **not** stored in the struct. It is computed on-demand when building the prompt (after repo/branch selection is complete), to avoid holding large text in memory during interactive selection.

Timeout: 1 minute for interactive selections.

## Mantis Integration

Preserved from current architecture. The `mantis/` package is kept. Mantis URL enrichment runs during thread context reading:

1. `FetchThreadContext()` returns raw message texts
2. Go runtime calls `enrichMessage()` on each message to expand Mantis URLs (fetches title + description)
3. Enriched text is included in the prompt's Thread Context section

This happens before prompt construction, so the agent sees the expanded Mantis context.

## Config Structure

```yaml
# Slack
slack:
  bot_token: xoxb-local-test-token
  app_token: xapp-local-test-token

# GitHub
github:
  token: ghp-local-test-token

# Agents
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

# Prompt
prompt:
  language: "zh-TW"
  extra_rules:
    - "do not guess UI element positions"
    - "only list files actually found"

# Channels (optional, auto-bind if not set)
channels:
  C12345678:
    repos: [owner/repo-a, owner/repo-b]
    branches: [main, develop]
    default_labels: [from-slack]
    branch_select: true

channel_defaults:
  default_labels: [from-slack]
  branch_select: false

auto_bind: true

# Concurrency
max_concurrent: 3
max_thread_messages: 50

# Rate limiting
rate_limit:
  per_user: 5         # max triggers per user per window
  per_channel: 10     # max triggers per channel per window
  window: 1m          # rate limit window duration

semaphore_timeout: 30s

# Mantis (optional, moved from integrations.mantis to top-level)
mantis:
  base_url: https://mantis.example.com
  api_token: mantis-local-token
  # Alternative: username/password basic auth (kept from v1)
  # username: user
  # password: pass

# Repo cache (same structure as v1)
repo_cache:
  dir: /data/repos
  max_age: 24h
```

### Config Migration

When loading config, if the `reactions` or `integrations` key is present, log a warning: "v1 config detected — reactions, llm, diagnosis, and integrations sections are no longer used in v2. Note: integrations.mantis has moved to top-level mantis." This provides a helpful error for users upgrading from v1.

Default values in YAML for local testing. In production, ConfigMap overrides.

## Error Handling

### Agent Execution Failures

| Failure | Action |
|---------|--------|
| Timeout | Try fallback agent |
| Command not found | Try fallback agent |
| Non-zero exit code | Try fallback agent |
| Exit 0 but output < 50 chars | Try fallback agent |
| All fallback exhausted | Notify Slack, clear dedup for retry |

### Resource Cleanup
On every triage completion (success or failure):
1. Remove temp dir (attachments) via defer
2. Clear dedup entry via defer
3. Release semaphore via defer

### Edge Cases

| Scenario | Handling |
|----------|----------|
| Selection timeout (1 min) | Update selector message to "timed out", clear dedup |
| Thread read failure | Notify Slack, clear dedup |
| Thread too large | Truncate to most recent N messages (max_thread_messages, default 50) |
| Attachment download failure | Skip attachment, note "download failed" in prompt |
| Repo clone/fetch failure | Attempt re-clone (existing logic), notify on failure |
| Duplicate trigger during analysis | Blocked by dedup |
| Semaphore wait timeout | Notify Slack "system busy, try later", clear dedup |

## Module Changes

| Module | Action | Notes |
|--------|--------|-------|
| `config/config.go` | Rewrite | agents map, active_agent, fallback; remove reaction/provider/diagnosis config. **Preserve**: ChannelConfig struct (repos, branches, branch_select, default_labels), MantisConfig, RepoCacheConfig |
| `slack/handler.go` | Rewrite | Handle slash command / app mention; preserve semaphore + dedup |
| `slack/client.go` | Modify | Add `FetchThreadContext()` with pagination |
| `bot/workflow.go` | Rewrite | New flow: trigger -> enrich -> interact -> spawn -> parse -> issue |
| `bot/agent.go` | New | AgentRunner (~50 lines) |
| `bot/parser.go` | New | Parse agent output, sanitize, resolve title |
| `github/issue.go` | Simplify | Remove FormatIssueBody; keep CreateIssue with body param |
| `github/repo.go` | Keep | Clone/fetch/checkout still needed |
| `github/discovery.go` | Keep | Repo search still needed |
| `mantis/` | Keep | Mantis URL enrichment preserved |
| `diagnosis/` | Delete | Replaced by CLI agent |
| `llm/` | Delete | No longer needed |
| `cmd/bot/main.go` | Rewrite | Simplified wiring |

### Estimated Code Reduction
- Before: ~2500 lines
- After: ~800 lines
- Removed: `diagnosis/` (~550 lines), `llm/` (~800 lines)

## Testing Strategy

### Unit Tests

| Component | Mock strategy |
|---|---|
| `AgentRunner` | Mock `exec.Command` via interface or test helper that replaces the binary with a script returning canned output |
| `parser.go` | Pure function tests: feed various markdown+metadata combinations, verify parsing |
| `FetchThreadContext` | Mock Slack API responses |
| `workflow.go` | Mock AgentRunner + Slack client + GitHub client |

### Integration Tests
- End-to-end: trigger -> thread read -> agent spawn (with a test script that echoes canned output) -> parse -> issue creation (mock GitHub API)
- Agent output format: run actual agent against a test repo, verify `===TRIAGE_METADATA===` format compliance

### Test Repo
Keep a small test fixture repo for integration tests (or use the bot's own repo).
