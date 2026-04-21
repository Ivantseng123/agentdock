# `github-pr-review` Skill — Design

**Date:** 2026-04-21
**Status:** Revised after grill-me review (2026-04-21)
**Parent spec:** [2026-04-20-workflow-types-design.md](./2026-04-20-workflow-types-design.md) — PR Review workflow

## Problem

The PR Review workflow defined in the parent spec needs a skill that teaches the agent two things:

1. **How to understand the repository it's about to review** — language, framework, test runner, style conventions, and which of those apply to the files the PR actually touches.
2. **How to post a code review back to GitHub** — line-level inline comments (including multi-line ranges and suggestion blocks) plus a summary, with the correct REST API payload, without accidentally commenting on lines outside the PR's diff, and without creating duplicate reviews.

The parent spec sketches this skill as `github-pr-review` and treats it as a separate deliverable. This document is that deliverable's design, revised after the grill-me review that surfaced hard constraints missed in the first pass.

The grill revealed a key constraint: **workers can run on any device, including colleagues' laptops**. This invalidates any design that depends on non-ubiquitous tools (`gh`, `jq`, Python) being present.

## Goals

1. **Zero new external dependency.** The skill ships helper logic as an `agentdock pr-review-helper` subcommand, which is necessarily present on any worker (the worker itself is `agentdock worker`). No `gh`, `jq`, or Python required.
2. **Fingerprint before reviewing.** The skill always runs a repo fingerprint step (language, framework, style sources, PR-touched languages and subprojects) before reading the diff. This grounds the review in the project's actual conventions and in what the PR touches.
3. **Validate before posting.** Every inline comment's target line (or line range, for multi-line) is checked against the PR's actual diff hunks before the review is submitted. Comments that don't map to a changed line are dropped, not posted. This is the skill's core correctness mechanism.
4. **Always `COMMENT`, never auto-approve or auto-request-changes.** The bot behaves like Copilot / CodeRabbit / IntelliJ AI reviewer: it comments. Approving or requesting changes is a human decision. `severity_summary` (`clean | minor | major`) in the result JSON carries the bot's assessment without touching GitHub's merge-gating flow.
5. **Single round-trip review.** The review is posted as one `POST /pulls/{n}/reviews` call (which carries both summary and inline comments). This keeps the PR timeline clean and matches the parent spec's "no retry" contract.
6. **Deterministic orchestration, subjective judgement.** The agent handles review quality (what to flag, how to explain it, when to use suggestion blocks). The Go helper handles diff parsing, line validation, API payload construction, HTTP transport, and rate-limit handling.
7. **Canonical skill anatomy.** SKILL.md + optional `references/`, matching the superpowers skill-creator conventions and the existing `triage-issue` precedent. No `scripts/` — SKILL.md invokes `agentdock pr-review-helper` directly.
8. **Pluggable via existing skill loader.** Ships as a baked-in skill at `agents/skills/github-pr-review/`, loaded by `app/skill/loader.go` with no loader changes. Follows the existing `triage-issue` dual-mount pattern (Dockerfile symlink + runtime mountSkills).

## Non-goals

- **Retry / re-post.** Parent spec forbids automatic retry for PR Review because the agent may have posted partial comments. Helper does internal retry only for transient HTTP errors (429 / 5xx); the skill itself never re-triggers the whole review on failure.
- **Dedup of repeated triggers.** A user re-mentioning `@bot review <URL>` always produces a new review. Matches Copilot / CodeRabbit behaviour. Users are responsible for not spamming.
- **Auto-approve or auto-request-changes.** See Goal 4. LLM judgement is not strong enough to gate merges.
- **Per-language review reference files.** v1 keeps style adaptation in SKILL.md as a generic checklist with a fallback rule. `references/{go,python,js,rust}.md` split is future work once review rules grow large enough to warrant it.
- **Severity threshold configuration.** v1 hardcodes 3 severities (`blocker | suggestion | nit`) and always emits `COMMENT`. Threshold knobs deferred.
- **Asking the PR author questions.** AI reviewers like Copilot / IntelliJ don't bounce questions back to authors. Skill does the same — no "question" severity, no follow-up prompts.
- **Incremental review.** A second `@bot review <URL>` re-runs the full review; it does not read previous review history and try to do a delta.
- **Enterprise GitHub support.** Inherits parent spec's v1 restriction to `github.com`.
- **Line-level review for vendored / generated / lockfile-only PRs.** The skill explicitly emits `status: SKIPPED` with `reason: lockfile_only` (or similar) and posts summary-only, because line-level comments on 30K-line generated files are noise.
- **`gh` CLI dependency.** Worker may run on any device (Docker image, colleague's laptop) and `gh` is not ubiquitously present. Parent spec's Dockerfile happens to install it, but the skill cannot assume it.
- **`jq`, Python, or any other non-ubiquitous tool.** Same reason.
- **npm-distributed skill package.** The grill revealed this is a purely internal skill tightly coupled to `agentdock pr-review-helper` binary version. Shipping as a baked-in skill (following `triage-issue`) is correct; npm packaging would fight the binary coupling.
- **Cross-skill coordination.** This skill runs standalone.

## Design

### Skill anatomy

```
agents/skills/github-pr-review/
├── SKILL.md                 # entry, <300 lines target
├── evals/
│   └── evals.json           # skill-creator eval test cases (3 baseline)
└── references/              # reserved; not populated in v1
```

No `scripts/` directory: the skill invokes `agentdock pr-review-helper` as a CLI directly from SKILL.md instructions. This is possible because `agentdock` is always on the worker's `PATH` (it's the worker binary itself).

Placement follows the existing `agents/skills/triage-issue/` precedent. `app/skill/loader.go` treats it as a `local` skill via one line in `skills.yaml`:

```yaml
skills:
  triage-issue:
    type: local
    path: agents/skills/triage-issue
  github-pr-review:               # <-- new
    type: local
    path: agents/skills/github-pr-review
```

**Dockerfile**: no changes needed. The existing `COPY agents/skills/ /opt/agents/skills/` plus symlink loop picks up the new skill automatically.

**Skill discovery**: the dual-mount redundancy (Dockerfile symlink to `~/.claude/skills/` + runtime `mountSkills` into `<repoPath>/<agent.SkillDir>/`) is treated as a feature, not a bug — it gives claude-code a fallback when runtime mount fails, and codex/opencode rely on the runtime mount only. Behaviour identical to `triage-issue`.

### SKILL.md structure

Frontmatter (neutral description, following `triage-issue` style — not the "pushy" form from skill-creator, because triggering is forced by the PR Review workflow's prompt, not by description matching):

```yaml
---
name: github-pr-review
description: Review a GitHub pull request — fingerprint the repository's language
  and style conventions, analyze the diff, and post line-level comments plus a
  summary review back to the PR via `agentdock pr-review-helper`. Invoked by the
  PR Review workflow when a user asks `@bot review <PR URL>`.
---
```

Body sections, imperative, each step followed by a one-line reason so the agent can extrapolate to edge cases:

1. **Input contract.** Thread context, PR URL, PR number, repo path (already cloned at PR head), `$GITHUB_TOKEN`. Format parallel to `triage-issue`.
2. **Fingerprint** — run `agentdock pr-review-helper fingerprint --pr-url "$PR_URL" > /tmp/fp.json`. Read the JSON. If `style_sources` is empty, note in the review summary that "no project style file found; reviewing against general language conventions." If `pr_subprojects` is non-empty, also inspect those directories for their own manifest / linter configs.
3. **Diff analysis** — the helper already validates line numbers, so the agent can judge changes qualitatively against fingerprint language + style. Produce zero or more comment candidates with `{path, line, side, body, severity}` or `{path, start_line, start_side, line, side, body, severity}` for multi-line ranges. Severity ∈ `blocker | suggestion | nit`.
4. **Suggestion blocks** — when the fix is obvious and mechanical, include a ```suggestion fenced block in the comment `body`. GitHub renders it as a "Commit suggestion" button. Suggestions must be compilable/runnable replacements for the lines the comment covers; fuzzy hints stay in prose.
5. **Severity summary** — compute `severity_summary`: `clean` if zero findings, `minor` if only nits, `major` if any blocker or any suggestion. This is **informational only**; the GitHub review event is always `COMMENT`.
6. **Produce review JSON** — assemble the v1 review JSON (see §Review JSON schema). Summary is markdown, ≤ 2000 chars (helper truncates beyond).
7. **Post** — pipe review.json into `agentdock pr-review-helper validate-and-post --pr-url "$PR_URL"`. Read the helper's stdout (JSON with `posted`, `skipped`, `skip_reasons`, `review_id`) and exit code. If exit code is 2, treat as fatal; do not retry.
8. **Emit result** — output `===REVIEW_RESULT===` followed by the three-state JSON per §Result marker contract.

**Skipping short-circuit** (agent decides before diff analysis):
- Pure lockfile / generated / vendored diff → emit `status: SKIPPED` with `reason: lockfile_only` and a summary explaining why. Helper is not called.
- PR already merged / closed → review still runs; summary notes that review is historical.

**Summary formatting guidance** (example, not required):

```
One-sentence overall assessment.

**Issues (N)**: X blockers, Y suggestions, Z nits.

<optional 1-2 sentences on the most important concern>
```

Agent is free to deviate. Zero-finding summaries can drop the Issues line entirely.

**Writing style guidance baked in:** each rule includes a one-line "why" after the imperative. Matches skill-creator's "explain why, don't stack MUSTs" guidance.

### Helper: `agentdock pr-review-helper`

Cobra subcommand added to `cmd/agentdock/`. Two subcommands:

#### `agentdock pr-review-helper fingerprint --pr-url <URL>`

**Purpose:** Deterministic repo inspection + PR-aware hints.

**Working directory:** cwd is the cloned repo root (worker sets this).

**Flags:**
- `--pr-url <URL>` — required. Used to fetch PR file list and compute PR-aware fields.

**Env:**
- `GITHUB_TOKEN` — required to call `GET /pulls/:n/files`.

**Stdout:** single JSON object.

```json
{
  "language": "go",
  "confidence": "high|medium|low",
  "style_sources": ["CLAUDE.md", ".golangci.yml"],
  "test_runner": "go test",
  "framework": null,
  "pr_touched_languages": ["python", "markdown"],
  "pr_subprojects": ["services/billing"]
}
```

**Probe order:**

1. **Language detection** (repo-level):
   - Lock / manifest: `go.mod` → go, `package.json` → js (then check `typescript` in deps → ts), `pyproject.toml` / `setup.py` → python, `Cargo.toml` → rust, `Gemfile` → ruby, `pom.xml` / `build.gradle` → java.
   - Extension voting (tiebreaker): count `*.go`, `*.py`, `*.ts`, `*.js`, `*.rs`, `*.rb`, `*.java`; largest matching manifest is `high` confidence; mismatch is `medium`; no manifest is `low`.
2. **Style sources**, checked in order (collect all that exist):
   - `CLAUDE.md`, `AGENTS.md`, `CONTRIBUTING.md`
   - `.editorconfig`
   - Language-specific: `.golangci.yml`, `.eslintrc.*`, `ruff.toml` / `pyproject.toml` `[tool.ruff]`, `rustfmt.toml`, `rubocop.yml`, `.prettierrc*`
3. **Test runner** inferred from language + `package.json` `scripts.test` / `Makefile` `test` target / language default.
4. **Framework** best-effort from dependency lists (`react`, `next`, `fastapi`, `django`, `gin`, `echo`, `spring`). Null if not obvious.
5. **PR-aware fields** (require GitHub API call):
   - `pr_touched_languages`: vote on file extensions in `GET /pulls/:n/files`.
   - `pr_subprojects`: for each touched file path, walk upward looking for a manifest (go.mod / package.json / pyproject.toml / etc). If one is found in a subdirectory below repo root, record that subdir. Deduplicate.

**Exit codes:** `0` always (missing style files / unknown language are not errors — they surface in the JSON).

#### `agentdock pr-review-helper validate-and-post --pr-url <URL> [--dry-run]`

**Purpose:** Validate every inline comment against the PR's actual diff, truncate over-long content, then submit the review in one API call (or print what would be sent).

**Stdin:** review JSON (see §Review JSON schema).

**Flags:**
- `--pr-url <URL>` — required.
- `--dry-run` — validate + show would-be payload; do not POST. Also triggered by `DRY_RUN=1` env var; flag wins if both set.

**Env:**
- `GITHUB_TOKEN` — required.

**Workflow:**

1. Read review JSON from stdin. Validate schema (§Review JSON schema). On malformed input → exit 2 with stderr explaining the error.
2. Resolve commit SHA: run `git rev-parse HEAD` inside cwd. **This is the SHA the helper posts against**, not any value from `WorkflowArgs`. Reason: the agent's review is grounded in this commit's code; tying the POST to a different SHA would misalign comments.
3. `GET /repos/{owner}/{repo}/pulls/{n}/files` with `Authorization: Bearer $GITHUB_TOKEN`. Build a map `{path → set of valid (line, side) tuples}` from each file's patch hunks.
   - Valid (line, side) pairs:
     - Added line (`+`) → `(line_number, "RIGHT")`
     - Removed line (`-`) → `(line_number, "LEFT")`
     - Context line → `(line_number, "RIGHT")` (conventional default for comments on unchanged lines)
4. For each comment in review JSON:
   - Truncate `body` to 4096 chars if longer, append `\n\n_…(comment truncated)_`.
   - If `path` not in diff map → skip, record `{path, line, reason: "file not in diff"}`.
   - Single-line: if `(line, side)` not in valid set → skip, record reason.
   - Multi-line: if `(start_line, start_side)` or `(line, side)` not valid, or `start_side != side`, or `start_line > line` → skip.
   - Otherwise include in POST payload.
5. Truncate top-level `summary` to 2048 chars if longer, append `\n\n_(summary truncated; see inline comments)_`.
6. Build POST `/pulls/{n}/reviews` payload:
   ```json
   {
     "commit_id": "<git rev-parse HEAD>",
     "body": "<summary>",
     "event": "COMMENT",
     "comments": [
       {"path": "...", "line": N, "side": "RIGHT", "body": "..."},
       {"path": "...", "start_line": M, "start_side": "RIGHT", "line": N, "side": "RIGHT", "body": "..."}
     ]
   }
   ```
7. POST with rate-limit handling (§Rate limit / retry policy).
   - 2xx → emit stdout; exit 0 if zero comments skipped, 1 if any skipped.
   - 422 (commit not in PR, usually from force-push) → exit 2 with `error: "PR head moved during review (422); please re-trigger"`.
   - 401 / 403 → exit 2 with appropriate message.
   - Rate-limit exhaustion → exit 2.

**Stdout schema (success, exit 0 or 1):**
```json
{
  "posted": 12,
  "skipped": 3,
  "truncated_comments": 1,
  "summary_truncated": false,
  "skip_reasons": [
    {"path": "path/foo.go", "line": 99, "reason": "line not in diff"}
  ],
  "review_id": 123456789,
  "commit_id": "abc123def"
}
```

**Stdout schema (--dry-run, exit 0 or 1):** same as success plus `dry_run: true` and `payload` (the POST body that would have been sent):
```json
{
  "dry_run": true,
  "would_post": 12,
  "skipped": 3,
  "truncated_comments": 1,
  "summary_truncated": false,
  "skip_reasons": [...],
  "commit_id": "abc123def",
  "payload": { /* full POST body */ }
}
```

**Stdout schema (fatal, exit 2):**
```json
{
  "error": "human-readable reason",
  "posted": 0
}
```

Helper's stdout is always JSON — human-friendly output is delegated to shell piping (`| jq`).

### Review JSON schema (stdin to `validate-and-post`)

```json
{
  "summary": "<markdown, ≤2048 chars>",
  "severity_summary": "clean|minor|major",
  "comments": [
    {
      "path": "path/to/file.go",
      "line": 42,
      "side": "RIGHT",
      "body": "<markdown, ≤4096 chars; may contain ```suggestion blocks>",
      "severity": "blocker|suggestion|nit"
    },
    {
      "path": "path/to/other.py",
      "start_line": 10,
      "start_side": "RIGHT",
      "line": 15,
      "side": "RIGHT",
      "body": "...",
      "severity": "suggestion"
    }
  ]
}
```

Validation rules (enforced by helper):
- `summary` required, non-empty string.
- `severity_summary` required, enum.
- `comments` required, array (may be empty).
- Each comment:
  - `path`, `line`, `side`, `body`, `severity` required.
  - `side` ∈ `LEFT | RIGHT`.
  - `severity` ∈ `blocker | suggestion | nit`.
  - `start_line` and `start_side` are both present-or-both-absent (multi-line mode).
  - If multi-line: `start_side == side` and `start_line <= line`.

Oversized `body` / `summary` are truncated, not rejected (§Content length).

### Result marker contract: `===REVIEW_RESULT===`

Three terminal states, each with required fields. HandleResult in `app/workflow/pr_review.go` dispatches on `status`.

**POSTED** (helper exit 0 or 1 — review landed on GitHub):
```json
{
  "status": "POSTED",
  "summary": "<review body, same text posted to GitHub>",
  "comments_posted": 12,
  "comments_skipped": 3,
  "severity_summary": "minor"
}
```

**SKIPPED** (agent short-circuited before calling helper — lockfile-only or generated diff):
```json
{
  "status": "SKIPPED",
  "summary": "Diff is vendored/generated, detailed review skipped.",
  "reason": "lockfile_only"
}
```

Valid `reason` values: `lockfile_only`, `vendored`, `generated`, `pure_docs`, `pure_config`. Additional values acceptable; HandleResult treats unknown values as `"other"` for Slack display.

**ERROR** (helper exit 2 — nothing posted):
```json
{
  "status": "ERROR",
  "error": "human-readable one-liner",
  "summary": "<agent's intended review text (not posted)>"
}
```

`summary` is included so Slack can show what the bot would have said, even though GitHub got nothing.

**Parser error handling** (`app/workflow/pr_review.go` parser):

| Failure | Slack message |
|---------|---------------|
| Marker missing | `:x: Agent 未照 contract 輸出` |
| JSON parse error | `:x: 解析失敗：<err>` |
| `status` missing or not in enum | `:x: Agent status 值不合法` |
| POSTED missing any of `summary / comments_posted / severity_summary` | `:x: Agent 輸出不完整` |
| SKIPPED missing `reason` | `:x: Agent 未說明 skip 原因` |
| ERROR missing `error` | `:x: Agent 失敗但未說明原因` |

### Rate limit / retry policy (helper internals)

GitHub has primary (5000/hour, rarely hit) and secondary (content-based, easily hit on bursty POSTs) rate limits.

**Retry conditions**:
- HTTP 429 → retry
- HTTP 403 + body contains "secondary rate limit" / "abuse detection" → retry
- HTTP 502 / 503 / 504 → retry (transient server)
- Network error (DNS, TCP reset, dial timeout) → retry
- All other 4xx / 5xx → fail immediately

**Wait-time decision** (per retry):
1. `Retry-After` header present → use that (capped at 10s)
2. Absent → fall back to exponential: 2s, 4s, 8s

**Limits**:
- Max 3 attempts total (1 initial + 2 retries)
- Max 30s total wall time per helper invocation — if exceeded, exit 2 with "rate limited; wall time exceeded"

**Error strings** (constants in `shared/prreview/errors.go`, testable):
- Rate limit exhaustion → `"GitHub rate-limited after 3 attempts (max 30s); please re-trigger later"`
- 422 → `"PR head moved during review (422); please re-trigger with current SHA"`
- 401 → `"GitHub token invalid or expired"`
- 403 non-rate-limit → `"Insufficient GitHub token scope (need PR write)"`

### Content length limits

| Field | Max | Beyond limit |
|-------|-----|--------------|
| Comment `body` | 4096 chars | Truncate, append `\n\n_…(comment truncated)_` |
| Summary | 2048 chars | Truncate, append `\n\n_(summary truncated; see inline comments)_` |

Constants: `shared/prreview/types.go` `MaxCommentBody = 4096`, `MaxSummaryBody = 2048`.

Summary limit matches parent spec's Slack output guideline — keeps Slack and GitHub consistent.

### Go package structure

```
shared/prreview/
├── doc.go
├── types.go              # ReviewJSON, CommentJSON, SeveritySummary, status enums, size limits
├── errors.go             # Error-message constants
├── fingerprint.go        # Fingerprint() + probe helpers
├── fingerprint_test.go
├── review.go             # Validate(), diffFiles(), PostReview(), httpCallWithRetry()
└── review_test.go        # httptest.Server-based coverage

cmd/agentdock/
└── pr_review_helper.go   # cobra command wiring; stdin/stdout/exit code glue
```

Rationale: all PR-review logic in one package → high cohesion. Fingerprint is local repo inspection (not GitHub API), and posting is GitHub-specific, but they share types and constants — splitting them into `shared/github/` + elsewhere would fragment the package. The `shared/prreview/` name captures the concern precisely.

Import direction: `shared/prreview/` imports only stdlib + go-modules available to `shared/` (no `app`, no `worker`). `cmd/agentdock/pr_review_helper.go` imports `shared/prreview/`. Root module (`cmd/`) is already allowed to import all submodules by `test/import_direction_test.go`.

### Integration with the rest of the system

- **Skill registration**: one line in `skills.yaml` (see §Skill anatomy).
- **Dockerfile**: no changes. Existing `COPY agents/skills/` + symlink loop picks up the new skill.
- **Prompt wiring**: parent spec's `prompt.pr_review.goal` default references this skill by name. The goal text will be revised (see parent-spec change list below) to drop the `$GITHUB_TOKEN` / REST API phrasing, since the helper abstracts that away.
- **WorkflowArgs**: reduced to `{pr_url, pr_number}` — see parent-spec change list.
- **Token plumbing**: `$GITHUB_TOKEN` is carried by `EncryptedSecrets` to the worker, which makes it available to agent subprocess env. Helper reads `GITHUB_TOKEN` directly.
- **Agent env**: worker must export `PR_URL` to the agent's env (for the skill's `--pr-url` flag). `PR_NUMBER` is helpful for Slack-facing text but not required by helper (it parses URL). Worker-side change in `app/workflow/pr_review.go` BuildJob → propagate WorkflowArgs into agent env.

### Staging / feature flag

The skill and its helper ship as **one implementation plan** (derived from this spec). The plan's final PR:
- `shared/prreview/` unit tests green
- `agentdock pr-review-helper fingerprint` and `validate-and-post --dry-run` manually runnable
- `agents/skills/github-pr-review/SKILL.md` + skills.yaml updated
- `evals/evals.json` baseline captured

After the plan lands, parent spec's PR 6 (PRReviewWorkflow + feature flag) wires the workflow; the skill is already present. Feature flag `pr_review.enabled: false` gates user visibility until operator smoke tests and flips it.

During the intermediate period where the skill is installed but no workflow references it, the skill is mounted into every job's skill dir (~5KB wire bloat). This is accepted.

## Testing

### Layer 1 — Go unit tests (`shared/prreview/*_test.go`)

Using `net/http/httptest.Server` (stdlib, no third-party mock libraries).

`fingerprint_test.go`:
- Fixture repo with `go.mod` + Go files → asserts `language: go`, style_sources includes `.golangci.yml` when present.
- Fixture with `package.json` containing `typescript` in deps → `language: ts`.
- Empty dir → `language: null, confidence: low`.
- PR files response contains files in `services/billing/` → `pr_subprojects` includes that path.
- PR files response contains mixed Python + Markdown → `pr_touched_languages: ["python", "markdown"]`.

`review_test.go`:
- All comments valid → one POST, exit 0, stdout has correct `posted` count.
- One comment on line outside diff → skipped, exit 1, `skip_reasons` non-empty.
- Multi-line comment crossing side boundary → skipped.
- Malformed review JSON on stdin → exit 2, no HTTP call.
- 401 response → exit 2, error string matches constant.
- 422 response → exit 2, error string matches force-push message.
- 429 with `Retry-After: 1` → retry once, succeed.
- 429 repeatedly → exit 2 with rate-limit message.
- Wall-time deadline exceeded mid-retry → exit 2 with "wall time exceeded".
- Comment body > 4KB → truncated, `truncated_comments: 1` in stdout, POST body shows truncation marker.
- Summary > 2KB → truncated similarly.
- `--dry-run` with valid input → no POST made, stdout includes `dry_run: true` and `payload` field.
- `DRY_RUN=1` env var without `--dry-run` flag → treated as dry-run.
- Both `DRY_RUN=1` and `--dry-run` with inconsistent values → flag wins.

### Layer 2 — Skill-level evals (`agents/skills/github-pr-review/evals/`)

Uses the skill-creator eval framework. `evals.json`:

```json
{
  "skill_name": "github-pr-review",
  "evals": [
    {
      "id": 1,
      "prompt": "Review PR <fixture-url-1> — Go PR violating CLAUDE.md import direction rule.",
      "expected_output": "POSTED with severity_summary=major; at least one comment at the violation line; summary mentions CLAUDE.md rule.",
      "files": []
    },
    {
      "id": 2,
      "prompt": "Review PR <fixture-url-2> — Python PR adding untested function in repo with no linter configs.",
      "expected_output": "POSTED with severity_summary=minor or major; summary notes 'no project style file found'; at least one suggestion about adding tests.",
      "files": []
    },
    {
      "id": 3,
      "prompt": "Review PR <fixture-url-3> — pnpm-lock.yaml-only diff.",
      "expected_output": "SKIPPED with reason=lockfile_only; zero inline comments.",
      "files": []
    }
  ]
}
```

**Assertions**:
- Objective (scriptable):
  - `===REVIEW_RESULT===` marker present exactly once.
  - JSON after marker parses and has required fields for its `status`.
  - For POSTED: every inline comment's `(path, line, side)` appears in fixture PR's `files` response.
- Subjective (human review in the eval viewer):
  - Does review reflect the CLAUDE.md rule (case 1)?
  - Senior-engineer tone vs generic boilerplate (case 2)?
  - Case 3's summary explains the skip clearly.

**Fixtures**: small captured GitHub API responses in `testdata/` (3 canned PRs — Go, Python, lockfile). No real GitHub API calls during eval.

Run once at implementation time as baseline. Re-run when SKILL.md or helper behaviour changes.

### Layer 3 — End-to-end smoke

**Deferred to parent spec's PR 8.** v1 does not set up a dedicated staging repo or bot account. Manual smoke test flows:
- Operator installs agentdock, configures `pr_review.enabled: true`
- Mentions `@bot review <some real PR URL>` in staging Slack
- Verifies review appears on GitHub, Slack result message matches

If future volume warrants, a dedicated `agentdock-e2e-fixtures` repo + bot account can be set up. Not in v1.

## Parent spec alignment (applied in this spec's commit)

Applying these decisions cleanly required edits to `docs/superpowers/specs/2026-04-20-workflow-types-design.md`. All six landed in the same commit as this spec's revision (`5c5e458`). Listed here as an audit trail; implementation work derived from the combined specs is handed off to a separate session.

1. **§Non-goals:** Remove the "A `gh` CLI dependency inside the worker container" bullet. Replace with "No new external tool dependency for PR Review beyond the `agentdock` binary itself; the `github-pr-review` skill's helper ships as an `agentdock pr-review-helper` subcommand."
2. **§PR Review — `Job` fields — `PromptContext.Goal` default text:** Remove "via the GitHub REST API with `$GITHUB_TOKEN`". Replace with "Use the `github-pr-review` skill to analyze the diff and post line-level comments and a summary back to the PR. Output `===REVIEW_RESULT===` followed by JSON per the skill's contract."
3. **§PR Review — `Job` fields — `WorkflowArgs`:** Change `{pr_url, pr_number, pr_base_ref}` to `{pr_url, pr_number}`. Note that head SHA is captured by the helper at post time via `git rev-parse HEAD` (not threaded through WorkflowArgs) and that base ref is fetched from GitHub by the helper only if needed.
4. **§PR Review — Result JSON:** Replace the single-state `{summary, comments_posted, verdict}` shape with the three-state `status: POSTED|SKIPPED|ERROR` schema from this spec's §Result marker contract.
5. **§PR Review — HandleResult:** Update to dispatch on `status` (POSTED → success message; SKIPPED → skip-notice message; ERROR → failure message). Update `verdict` references to `severity_summary` throughout.
6. **§Open questions / future work — `github-pr-review` skill package bullet:** Rephrase from "new npm package fetched by the skill loader" to "baked-in skill at `agents/skills/github-pr-review/` plus an `agentdock pr-review-helper` subcommand in `cmd/agentdock/`; ships as one implementation plan tracked by `2026-04-21-github-pr-review-skill-design.md`."

## Open questions / future work

- **Per-language `references/`.** When review rules grow beyond a generic checklist, split into `references/{go,python,js,rust}.md`. SKILL.md will reference only the matching file based on fingerprint language.
- **Severity threshold config.** `pr_review.auto_approve_on_clean: true | false` would let operators opt into bot auto-approve. Current v1 always emits `COMMENT`.
- **Resolving in-thread discussion context.** Slack thread may carry intent ("this is a stopgap, don't suggest alternatives"). Skill currently ignores it; future work is passing thread context into the prompt.
- **Cross-job rate-limit budget.** If the bot is installed on a busy repo, per-invocation retry may not be enough. A shared budget (Redis-backed) across concurrent reviews would prevent stampede.
- **Dry-run mode exposed to Slack.** `@bot review <URL> --preview` that runs the skill with `--dry-run` and posts the would-be review to Slack only. Useful for sensitive PRs.
- **Metrics at the skill level.** Parent spec covers `WorkflowCompletionsTotal{workflow="pr_review", ...}`. Skill-level counters (comments posted / comments skipped by validator / fingerprint confidence distribution / rate-limit hits) would be nice once volume makes them meaningful.
- **Pre-parsed SHA field in WorkflowArgs for audit.** Spec currently drops `pr_head_sha` since helper re-resolves via git. Future may re-add it as an app-captured audit field (not consumed by helper) if debugging force-push races becomes frequent.
- **Enterprise GitHub.** `pr_review.allowed_hosts: [github.com, github.example.com]`. Inherits from parent spec.
