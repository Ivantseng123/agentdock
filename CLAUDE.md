# react2issue v2

Go service that turns Slack conversations into structured GitHub issues with AI-powered codebase triage. Triggered by `@bot` mentions or `/triage` slash commands in threads. Core value: lowering the barrier for non-engineers to create useful issues from Slack conversations.

## Architecture

```
@bot or /triage in thread → Socket Mode → Handler (dedup + rate limit + semaphore)
  → Workflow (thread context read → repo/branch selection via buttons)
    → CLI Agent (claude/opencode/codex/gemini):
        Spawn agent with prompt (thread context + repo path)
        Agent explores codebase using its own tools
        Returns markdown issue body + JSON metadata
    → Rejection/Degradation:
        confidence=low → reject (wrong repo)
        files=0 or questions>=5 → create issue WITHOUT triage section
        otherwise → create issue WITH full triage
    → GitHub Issue (Go-injected header + agent markdown) → Post URL in thread
```

## Project Structure

```
cmd/bot/main.go              # Entry point, wires deps, Socket Mode event loop
internal/
  config/config.go           # YAML config: agents, channels, prompt, rate limits
  bot/
    workflow.go              # Orchestrator: trigger → interact → spawn agent → parse → issue
    agent.go                 # AgentRunner: spawn CLI agent with fallback chain
    parser.go                # Parse agent output (===TRIAGE_RESULT=== CREATED/REJECTED/ERROR)
    prompt.go                # Build minimal user prompt for CLI agent
    enrich.go                # Expand Mantis URLs in messages
  slack/
    client.go                # PostMessage/PostSelector/FetchThreadContext/DownloadAttachments
    handler.go               # TriggerEvent dedup, rate limiting, bounded concurrency
  github/
    issue.go                 # CreateIssue(ctx, owner, repo, title, body, labels)
    repo.go                  # RepoCache: full clone, fetch, branch list, checkout
    discovery.go             # GitHub API repo discovery with cache (auto-bind)
  mantis/                    # Mantis bug tracker URL enrichment
```

## Key Design Decisions

### Tool positioning
This is a **structuring tool**, not a diagnosis tool. The core value is turning Slack conversations into formatted issues — AI triage is a bonus.

### CLI Agent Delegation (v2)
Instead of implementing a custom agent loop with tools, the bot spawns external CLI agents (claude, opencode, codex, gemini) that use their own built-in tools to explore the codebase. The bot sends a minimal prompt with thread context and repo path, and parses the structured output.

### Multi-Agent Fallback
Agents are configured in YAML. If the active agent fails (timeout, not found, error), the bot tries the next agent in the fallback chain.

### Output Format
Agent creates the GitHub issue directly via `gh issue create` and outputs a result marker. The parser looks for `===TRIAGE_RESULT===` followed by `CREATED: {url}`, `REJECTED: {reason}`, or `ERROR: {message}`. If the marker is missing, the parser falls back to extracting a GitHub issue URL from the output.

### Rejection vs Degradation
- `confidence=low` → reject (likely wrong repo)
- `files=0` or `open_questions>=5` → create issue, skip triage
- Normal → create issue with full triage

### Thread-Only, Thread-Context
Bot only operates in threads. Reads all thread messages before the trigger to build context. Agent decides issue type (bug/feature/improvement/question) from context.

### Auto-bind
Bot auto-registers when joining a channel. No static channel config needed.

## Lessons Learned

- **Slack `invalid_blocks`**: Don't combine `MsgOptionMetadata` with `MsgOptionBlocks`.
- **Full clone required**: Shallow clone can't list branches. Use `fetch --all --prune`.
- **Reporter tag**: Don't use `@username` — Slack/GitHub names differ. Plain text only.

## Testing

```bash
go test ./...   # 69 tests
```

## Build & Run

```bash
./run.sh
# or
go build -o bot ./cmd/bot/ && ./bot -config config.yaml
```
