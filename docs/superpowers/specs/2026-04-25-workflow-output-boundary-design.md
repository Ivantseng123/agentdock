# Workflow Output Boundary and Fallback Policy

**Date:** 2026-04-25
**Status:** Draft (revised after design grill)
**Issue:** TBD
**Reversed in part by:** [`2026-04-26-ask-fallback-extension-design.md`](./2026-04-26-ask-fallback-extension-design.md) — §Ask Fallback Policy → Trigger Condition #2, §Acceptance Criteria #3, and §Design Decisions Resolved #2 are no longer in force. Ask fallback now covers all parse-failure shapes, with categorised metric labels for observability.

## Problem

AgentDock currently treats workflow output parsing and user-visible failure
messaging as mostly local decisions inside each workflow. That has produced a
useful but inconsistent boundary:

- `ask` fails closed on missing `===ASK_RESULT===`, even when the model already
  produced a correct final answer in plain Slack mrkdwn.
- `issue` and `pr_review` do not expose raw agent output on parse failure,
  which is good for safety. Their failure messaging today still varies — for
  example `app/workflow/issue.go` posts `errMsg` directly while
  `app/workflow/pr_review.go` runs errors through `mapGitHubErrorToSlack` —
  but that inconsistency is not in scope for this spec.
- The system lacks a shared policy for when raw agent output is a valid
  user-facing deliverable versus when it is only an operator diagnostic.

The missing policy matters because the three workflows do not have the same
product contract:

1. `ask` produces an answer for the user to read.
2. `issue` produces a GitHub issue as an external side effect.
3. `pr_review` produces comments / a review on GitHub as an external side
   effect.

Those differences should drive what we are willing to show to the user when the
agent, parser, or GitHub integration degrades.

## Incident Trigger

On 2026-04-25, Ask job `20260425-130213-49a87529` exited successfully with a
correct markdown answer, but omitted the trailing `===ASK_RESULT===` JSON block.
`app/workflow/ask_parser.go` therefore returned `===ASK_RESULT=== marker not
found`, and Slack showed a parse failure instead of the answer the model had
already produced.

The same class of incident does not automatically justify looser exposure for
`issue` or `pr_review`, because those workflows are only successful when their
GitHub side effects land.

## Goals

1. Define a single policy for what kinds of agent output may be shown directly
   to Slack users for each workflow.
2. Allow `ask` to recover from schema-packaging failures when the raw stdout is
   clearly the final answer.
3. Keep `issue` and `pr_review` strict: no raw agent drafts should be shown to
   users when the intended GitHub side effect did not happen.
4. Preserve the failure taxonomy as an operator-facing observability primitive
   (metric labels, structured logs) without dictating user-facing wording.
5. Keep the design compatible with workers running on local machines or in k8s;
   no solution may depend on relaxing host sandbox rules.
6. Keep the implementation surface small. Worker code stays unchanged; all
   fallback logic lives in `app/workflow`.

## Non-Goals

- Replacing the existing marker-based schemas (`===ASK_RESULT===`,
  `===TRIAGE_RESULT===`, `===REVIEW_RESULT===`) with a new transport format.
- Guaranteeing that weak models will always follow output schemas through prompt
  changes alone.
- Adding a second LLM invocation as the default repair path for malformed
  output.
- Exposing raw stderr, raw tool transcripts, or environment-specific diagnostics
  directly to Slack users.
- Centralizing user-facing failure wording across workflows. Each workflow owns
  its own Slack wording because the deliverables differ.
- Building a generic stdout sanitizer beyond the existing
  `shared/logging.Redact` (secrets-only). Sanitization beyond secrets is
  treated as a logging convention, not an enforced mechanism.
- Sanitizing or restructuring `issue` / `pr_review` failure messages. Any UX
  improvement to those (e.g. `issue.go:316,323` exposing raw `errMsg`) is
  out-of-scope for this spec and tracked as a follow-up task.
- Changing the success criteria of any workflow:
  `ask` still returns an answer, `issue` still creates an issue, and
  `pr_review` still posts a review.

## Core Principle

Raw agent output may be shown to the user only when that raw output is itself
the workflow's intended final deliverable.

That principle yields the following rule:

- `ask`: raw fallback is sometimes acceptable.
- `issue`: raw fallback is not acceptable.
- `pr_review`: raw fallback is not acceptable.

Reason: a free-form answer can still satisfy Ask's product contract even when
its machine wrapper is missing. A triage draft or review draft does not satisfy
Issue / PR Review unless the GitHub side effect actually happened.

## Output Classes

This spec standardizes three output classes:

1. **User deliverable**
   The artifact the user actually asked for.
   Examples:
   - Ask answer text
   - Created issue URL
   - Posted PR review summary

2. **Side-effect status**
   Whether the workflow's external action succeeded.
   Examples:
   - Issue creation succeeded / failed
   - PR review comments posted / skipped / failed

3. **Operator diagnostics**
   Internal details useful for logs, metrics, or debugging, but not required in
   the Slack thread.
   Examples:
   - raw stdout / stderr
   - `permission denied`
   - `403 Resource not accessible by integration`
   - sandbox wording
   - helper exit codes
   - local file paths

Slack user messaging must prefer user deliverables first, then user-safe
side-effect status. Operator diagnostics stay in logs unless explicitly
sanitized for user display.

## Workflow Policy Matrix

This matrix provides the rationale for why Ask is the only workflow that
relaxes its parsing contract.

### Ask

**Success contract:** user receives a readable answer in Slack.

**Allowed degraded success path:**
- If the parser fails only because the result marker is missing, and the raw
  stdout passes a minimal syntactic check, the app may treat the stdout as the
  answer and post it with a transparency banner.

**Not allowed as degraded success:**
- Empty / whitespace-only stdout.
- Malformed-JSON cases (marker present but JSON unparseable). These continue
  to fail closed; see §Ask Fallback Policy.

### Issue

**Success contract:** a GitHub issue is actually created.

**Allowed degraded success path:**
- None. A triage draft without issue creation is not a success.

**Not allowed as degraded success:**
- Raw triage markdown
- Raw `===TRIAGE_RESULT===` JSON
- Draft issue title/body when `gh` or GitHub API failed

**Current behavior preserved (no change in this spec):**
- `IssueWorkflow.HandleResult` parses first, then calls GitHub. Parse failures
  short-circuit before any GitHub call (`app/workflow/issue.go:263–273`),
  which already satisfies strictness without further work in this spec.

### PR Review

**Success contract:** the review summary/comments are actually posted or the
workflow deterministically decides to skip.

**Allowed degraded success path:**
- Structured `SKIPPED` remains a valid terminal outcome because the workflow
  intentionally chose not to post comments.

**Not allowed as degraded success:**
- Raw review draft when review posting failed
- Raw helper output when permission/auth/API errors prevented posting

**Current behavior preserved (no change in this spec):**
- `mapGitHubErrorToSlack` (`app/workflow/pr_review.go:184`) already maps
  GitHub errors into short user-safe text. This spec does not modify that
  helper.

## Failure Taxonomy (Operator-Facing Only)

Workflows classify failures into a small taxonomy for **metrics labels and
structured logs only**. This taxonomy does not appear in user-facing Slack
text. Each workflow chooses its own Slack wording (see §User-Facing Wording).

Categories:

- `parse_failed`
- `access_denied`
- `not_found`
- `sandbox_blocked`
- `transient_remote_error`
- `invalid_state`
- `internal_error`

These categories are reused as `WorkflowCompletionsTotal` label values. They
do not constrain Slack wording, and there is no shared `Category → Slack text`
mapper in this spec.

## User-Facing Wording (Workflow-Local)

Each workflow owns the Chinese strings shown to its users. Those strings are
shaped by the workflow's deliverable, not by a shared taxonomy:

- Ask describes whether an answer was produced.
- Issue describes whether an issue was created.
- PR Review describes whether comments were posted.

Examples (illustrative, not normative):

- Ask parse-failure (after fallback rejected): `:x: 回答產生失敗`
- Issue create failure: `:x: 分析失敗,尚未建立 issue`
- PR Review post failure: `:x: Review 失敗,尚未送出結果`

If two workflows happen to share wording for an underlying category, that is
fine but not required.

## Ask Fallback Policy

### Trigger Condition

Ask fallback applies only when all of the following are true:

1. Worker status is `completed`, not `failed`.
2. `ParseAskOutput` fails because the `===ASK_RESULT===` marker is **missing
   entirely**. Malformed-JSON cases (marker present, JSON unparseable) are
   not covered.
3. The raw stdout passes a minimal syntactic check (see below).

### Syntactic Check (Replaces the Old Plausibility Check)

To avoid false-positive language detection that would not survive non-English
locales or macOS path strings, this spec deliberately does not use a keyword
blocklist. The check is purely syntactic:

- raw stdout has at least some non-whitespace content
- raw stdout exceeds a small minimum length threshold (exact value chosen at
  implementation time; intent is to reject obvious empties, not to classify
  content)

Anything passing this check is treated as the answer. The classifier does not
attempt to detect "this looks like a stack trace" or "this looks like a
sandbox error" — those judgments belong to the human reading the Slack
message, who is now warned that the output skipped the schema (see §Slack
Rendering).

### AskResult Metadata

Add a new `ResultSource` field to `AskResult`:

```go
type AskResult struct {
    Answer       string `json:"answer"`
    Confidence   string `json:"confidence,omitempty"`
    ResultSource string `json:"-"` // "schema" | "raw_fallback"
}
```

- `ResultSource = "schema"` — parser found the marker and decoded valid JSON.
- `ResultSource = "raw_fallback"` — parser took the missing-marker fallback
  path.

`Confidence` continues to carry the model's self-reported confidence (from
the JSON payload) and is not overloaded for transport quality. The two fields
answer different questions: `Confidence` answers "how sure does the model
claim to be?", `ResultSource` answers "did the transport contract hold?".

### Slack Rendering

Fallback answers are rendered with a prepended warning banner so the user can
make their own call about whether to trust the answer. The banner is:

```
:warning: 請驗證輸出答案,AGENT 並未遵守輸出格式

<answer body>
```

Schema-path answers continue to render unchanged. The banner is not a quality
judgment — it is a transparency signal that the machine wrapper was missing,
and the user should validate before acting.

## Issue and PR Review Strictness

Issue and PR Review remain strict. This spec does not introduce new behavior
for either workflow; it merely documents that their existing strictness is the
intended contract.

### Issue

If triage parsing or `gh issue create` fails:

- Slack must not expose the generated issue body or title.
- The retry button (when available) and operator logs remain the recovery
  channel.
- Existing wording in `IssueWorkflow.handleFailure` is preserved.

### PR Review

If analysis completes but posting the review fails:

- Slack must not expose the draft review summary/comments.
- The user should re-trigger after permissions, PR state, or remote issues are
  resolved.
- Existing wording in `PRReviewWorkflow.HandleResult` is preserved.

Reason: once raw drafts are shown in Slack, users can easily mistake them for
completed side effects.

## Sanitization Policy (Minimal)

This spec does not introduce a generic stdout sanitizer. The only enforced
sanitization is the existing `shared/logging.Redact`, which strips secrets
listed in `cfg.Secrets`.

Stack traces, file paths, sandbox wording, and helper command lines are not
machine-stripped. They may leak into Slack on the failure paths that don't
already have a workflow-local mapper. The team accepts that tradeoff because:

- Adding a generic stripper would require a heuristic regex set with no
  evaluation corpus, which is exactly the kind of fuzzy classifier this spec
  rejects in §Syntactic Check.
- The categories in §Failure Taxonomy give operators enough signal via metrics
  and logs.
- Workflows that want cleaner Slack wording can add their own local mapper
  (PR Review already has one) without depending on a shared helper.

The "Must Stay in Logs" list (raw stderr / helper command lines / local
filesystem paths / API response bodies / full stack traces) remains a logging
convention for engineers writing log statements, not an enforced runtime
filter.

## Proposed Implementation Shape

Implementation lives entirely in `app/workflow`. Worker code is unchanged.

- `app/workflow/ask_parser.go`
  Add a missing-marker fallback path that returns
  `AskResult{Answer: stdout, ResultSource: "raw_fallback"}` when the syntactic
  check passes. Schema-path returns set `ResultSource = "schema"`.

- `app/workflow/ask.go`
  In `HandleResult`, branch on `ResultSource`:
  - `"schema"` → render as today.
  - `"raw_fallback"` → prepend the `:warning:` banner to the answer body and
    emit `WorkflowCompletionsTotal{workflow="ask",status="fallback_raw"}`.

- `app/workflow/issue.go` / `app/workflow/pr_review.go`
  No changes for this spec.

- `shared/queue.JobResult`
  No schema change. Worker continues to emit raw stdout + `completed` status.

No host-sandbox relaxations are required.

## Observability

Add metrics that let operators see degraded behavior without exposing it to
users.

Counters:

- `WorkflowCompletionsTotal{workflow="ask",status="fallback_raw"}` (new)
- `WorkflowCompletionsTotal{workflow="ask",status="parse_failed"}` (existing)
- `WorkflowCompletionsTotal{workflow="ask",status="success"}` (existing)

Logs:

- Ask raw fallback triggered, including job id and a short redacted output
  head (via `logging.Redact`).
- Ask strict parse failures continue to log as today.

Alert thresholds (e.g. fallback rate paging on > X% over Y minutes) are left
to the ops setup that consumes these metrics; they are not part of this spec.

## Acceptance Criteria

1. Ask jobs whose worker exits `completed` and whose stdout is missing the
   `===ASK_RESULT===` marker but is non-empty post the stdout to Slack with
   the `:warning:` banner prepended.
2. Ask jobs with empty / whitespace-only stdout still fail closed and post the
   existing `:x: 解析失敗` style message.
3. Ask jobs with marker present but malformed JSON still fail closed (this
   spec deliberately does not extend the fallback to that case).
4. Issue and PR Review behavior is unchanged. Raw triage drafts and raw review
   drafts are never posted to Slack on failure paths.
5. `WorkflowCompletionsTotal{workflow="ask",status="fallback_raw"}` increments
   exactly when the missing-marker fallback fires.
6. `AskResult.Confidence` is not overloaded for transport quality. The new
   `ResultSource` field carries that signal.
7. No worker-side code changes ship as part of this spec.

## Rollout Plan

Recommended order:

1. Implement the Ask raw fallback in `ask_parser.go` + `ask.go` (including
   the prepend banner and metric).
2. Add Ask metrics/logging for fallback-vs-strict parsing.
3. ~~Sanitize Issue and PR Review failure messages.~~ Removed: out of scope
   for this spec. If `issue.go:316,323` exposing raw `errMsg` is judged a UX
   problem, file it as a follow-up task.
4. Review whether prompt emphasis on `===ASK_RESULT===` wrapping needs
   strengthening, gated by the `fallback_raw` metric trend exceeding an
   ops-defined threshold.
5. Revisit model selection only if Ask fallback frequency remains
   operationally noisy after step 4.

## Design Decisions Resolved

The following questions were considered and closed during the design grill;
they are recorded here so future readers do not relitigate them.

1. **Sanitization architecture** — workflow-local Chinese wording with a
   shared metric-label taxonomy. No shared `Category → text` mapper, no
   shared stdout sanitizer beyond `logging.Redact`.
2. **Ask fallback surface** — missing-marker only. Malformed-JSON cases stay
   strict because the raw stdout in those cases typically contains JSON
   debris that would confuse the user.
3. **Slack visibility for fallback** — prepend a `:warning:` banner. The
   user is told the schema was violated and decides whether to trust the
   answer; the system does not silently degrade or silently succeed.
4. **app/worker boundary** — fallback logic lives entirely in `app/workflow`.
   Worker stays unchanged and continues to emit raw stdout + `completed`
   status.
5. **Plausibility heuristics** — replaced with a syntactic check
   (non-whitespace + minimum length). Keyword blocklists were rejected as
   English-centric, brittle on macOS path strings, and lacking an
   evaluation corpus.
6. **Spec scope** — three-workflow framing retained for rationale, but
   Issue and PR Review sections describe existing behavior rather than
   prescribing new behavior.
