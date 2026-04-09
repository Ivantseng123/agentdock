# Agent Architecture Redesign

## Overview

Replace the custom agent loop and LLM provider abstraction with direct CLI agent invocation. The Go service becomes a thin runtime that handles Slack interaction and GitHub issue creation, while delegating all codebase analysis to external CLI agents (claude, opencode, codex, gemini).

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
|  @bot / /triage  <->  Socket Mode / Slash Command    |
+------------------------+----------------------------+
                         |
                         v
+-----------------------------------------------------+
|              Go Thin Runtime                         |
|                                                      |
|  1. Receive trigger -> read thread history           |
|  2. Interactive flow: repo -> branch -> description  |
|  3. AgentRunner.Run(workDir, prompt)                 |
|     - Read active_agent from config                  |
|     - Fallback chain: claude -> opencode -> ...      |
|  4. Parse agent output: markdown + JSON metadata     |
|  5. Reject/Degrade decision (based on metadata)      |
|  6. GitHub issue creation (API call)                 |
|  7. Reply to Slack thread                            |
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
              (markdown + ---METADATA--- + JSON)
```

## Trigger Model

### Trigger Methods
- **App Mention**: `@bot` in a thread (Socket Mode Events API)
- **Slash Command**: `/triage` in a thread (HTTP endpoint)

### Thread-Only Constraint
The bot only operates within threads. If triggered outside a thread (channel-level message), it replies with a prompt to use it in a thread and takes no further action.

### Slash Command Parameters (optional)
```
/triage                    -> interactive repo selection
/triage owner/repo         -> skip repo selection, show branch selection
/triage owner/repo@branch  -> skip repo + branch, start analysis directly
```

### Thread Context Reading
When triggered in a thread:
1. Use `conversations.replies` API to read all messages in the thread
2. Filter: only messages before the trigger, exclude bot's own messages
3. Each message includes: sender name, timestamp, content
4. Attachments: download to temp dir, provide file paths in prompt

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

- /tmp/triage-abc123/screenshot.png

## Output Format

Output markdown first (used directly as the issue body),
then a ---METADATA--- separator, then JSON:

{
  "issue_type": "bug|feature|improvement|question",
  "confidence": "low|medium|high",
  "files": [{"path": "...", "line": 0, "relevance": "..."}],
  "open_questions": [],
  "suggested_title": "..."
}

Response language: zh-TW
```

### Config-Driven Customization
- `prompt.language`: agent response language
- `prompt.extra_rules`: appended to the prompt tail

## Output Format

Agent output is split by `---METADATA---` into two parts:

### Part 1: Markdown Body
Used directly as the GitHub issue body. The agent writes the full issue content including summary, related code, analysis direction, etc.

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

### Parsing Fallbacks

| Scenario | Handling |
|----------|----------|
| Valid `---METADATA---` + valid JSON | Normal flow |
| Valid `---METADATA---` + invalid JSON | Degrade: use markdown body, default metadata |
| No `---METADATA---` | Degrade: entire stdout as issue body, no metadata |
| Empty stdout | Agent failure, try fallback |

## Interactive Flow

```
Thread: @bot or /triage
    |
    v
Validate: is this in a thread?
    +-- No -> reply "please use in a thread", end
    |
    v
Dedup check (thread_ts + channel_id)
    +-- Duplicate -> ignore
    |
    v
Read thread history + download attachments to temp dir
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
Start analysis
    +-- Post "analyzing..." to thread
    +-- EnsureRepo -> checkout branch
    +-- Build prompt
    +-- AgentRunner.Run()
    +-- Parse output
    |
    v
Reject/Degrade decision
    +-- confidence=low -> reject, notify Slack
    +-- files=0 or open_questions>=5 -> degrade: issue without triage
    |
    v
GitHub issue create
    +-- title: metadata.suggested_title
    +-- body: agent's markdown
    +-- labels: config default_labels + issue_type
    |
    v
Reply Slack thread: issue URL
Cleanup temp dir
Clear dedup
```

### State Management

```go
type pendingTriage struct {
    ChannelID      string
    ThreadTS       string
    ThreadContext  string   // serialized thread messages
    Attachments    []string // temp file paths
    SelectedRepo   string
    SelectedBranch string
    Phase          string   // "repo", "branch", "description"
    SelectorTS     string
}
```

Timeout: 1 minute for interactive selections.

## Config Structure

```yaml
# Slack
slack:
  bot_token: xoxb-local-test-token
  app_token: xapp-local-test-token
  signing_secret: local-signing-secret

# GitHub
github:
  token: ghp_local-test-token
  repo_cache_dir: /data/repos

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
```

Default values in YAML for local testing. In production, ConfigMap overrides.

## Error Handling

### Agent Execution Failures

| Failure | Action |
|---------|--------|
| Timeout | Try fallback agent |
| Command not found | Try fallback agent |
| Non-zero exit code | Try fallback agent |
| All fallback exhausted | Notify Slack, clear dedup for retry |

### Resource Cleanup
On every triage completion (success or failure):
1. Remove temp dir (attachments) via defer
2. Clear dedup entry via defer

### Edge Cases

| Scenario | Handling |
|----------|----------|
| Selection timeout (1 min) | Update selector message to "timed out", clear dedup |
| Thread read failure | Notify Slack, clear dedup |
| Thread too large | Truncate to most recent N messages (config, default 50) |
| Attachment download failure | Skip attachment, note "download failed" in prompt |
| Repo clone/fetch failure | Attempt re-clone (existing logic), notify on failure |
| Duplicate trigger during analysis | Blocked by dedup |

## Module Changes

| Module | Action | Notes |
|--------|--------|-------|
| `config/config.go` | Rewrite | agents map, active_agent, fallback; remove reaction config |
| `slack/handler.go` | Rewrite | Handle slash command / app mention instead of reactions |
| `slack/client.go` | Modify | Add thread history reading |
| `bot/workflow.go` | Rewrite | New flow: trigger -> interact -> spawn -> parse -> issue |
| `bot/agent.go` | New | AgentRunner (~50 lines) |
| `bot/parser.go` | New | Parse agent output (markdown + JSON metadata) |
| `github/issue.go` | Simplify | Remove FormatIssueBody, agent produces body |
| `github/repo.go` | Keep | Clone/fetch/checkout still needed |
| `github/discovery.go` | Keep | Repo search still needed |
| `diagnosis/` | Delete | Replaced by CLI agent |
| `llm/` | Delete | No longer needed |
| `cmd/bot/main.go` | Rewrite | Simplified wiring |

### Estimated Code Reduction
- Before: ~2500 lines
- After: ~800 lines
- Removed: `diagnosis/` (~550 lines), `llm/` (~800 lines)
