# Span Attribute PII Allowlist

## Context

OTel spans flow to the K8s `istio-system/jaeger-collector`, whose access ACL is owned by the platform team and may be readable beyond the agentdock team. `Job` and `PromptContext` carry user-typed Slack content, encrypted GitHub tokens, and free-form prompt text. Without explicit guardrails, an instrumentor adding a new span could trivially leak any of these into a system we don't fully control.

## Decision

Span attributes are restricted to a hard allowlist. Any attribute not on this list must not be set on a span without an explicit ADR amendment.

**Allowed on spans:**

| Attribute | Source | Notes |
|-----------|--------|-------|
| `task_type` | `Job.TaskType` | enum: `issue` / `ask` / `pr-review` |
| `repo` | `Job.Repo` (or per-clone target) | `owner/name` |
| `repo_role` | derived | `primary` / `ref` (multi-repo Ask) |
| `branch` | `Job.Branch` or `RefRepo.Branch` | empty string = default branch |
| `channel_id` | `Job.ChannelID` | Slack ID, opaque |
| `user_id` | `Job.UserID` | Slack ID, opaque |
| `job_id` | `Job.ID` | internal UUID |
| `attachment_count` | `len(Job.Attachments)` | count only, not filenames |
| `ref_repo_count` | `len(Job.RefRepos)` | count only |
| `retry_count` | `Job.RetryCount` | int |
| `retry_of_job_id` | `Job.RetryOfJobID` | only present on retry spans |
| `agent_type` | `agent.Command` | enum: `claude` / `opencode` / `codex` / `gemini` |
| `exit_code` | subprocess return | int |
| `stdout_bytes` / `stderr_bytes` | length only | not the content |
| `duration_ms` | computed | redundant with Jaeger but useful for queries |
| `agent_pid` | subprocess PID | for debugging stuck subprocesses |
| `cancelled` | bool | true when user cancelled mid-flight |
| `error.type` | derived from err on failure spans | e.g. `auth_failed` / `not_found` / `network_timeout` |

**Forbidden on spans (do not add without amending this ADR):**

- `Job.PromptContext.ThreadMessages[].Text` — Slack message text; may contain user-pasted secrets, PII, or private content.
- `Job.PromptContext.ExtraDescription` — free-form user input.
- `Job.PromptContext.Reporter` — Slack display name.
- `Job.PromptContext.PriorAnswer` / agent stdout / agent stderr **content** (length-only is OK).
- `Job.EncryptedSecrets` — even though encrypted; do not surface.
- `Job.Attachments[].Filename` / `MimeType` / `DownloadURL` — filenames may carry PII.
- Subprocess command-line `Args` (only `agent_type` is allowed; do not set the rest of `exec.Cmd.Args`).

## Consequences

- A reviewer's first question on any PR that adds a `span.SetAttributes(...)` call must be "is this attr on the allowlist?" If not, the PR amends this ADR or refactors to derive an allowed value (e.g. lengths, counts, enums).
- Span Status conventions reinforce this: `agent.execute` exit_code != 0 is **Unset** (not Error) so we don't have to write the agent's failure rationale onto the span; that rationale is in the agent's stdout/stderr file in `cfg.Logging.AgentOutputDir`, not in Jaeger.
- This ADR is the source of truth for the spec's `Boundaries — Never do` PII items; the spec links here rather than duplicating the table.
