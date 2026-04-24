---
name: github-pr-review
description: Review a GitHub pull request — fingerprint the repository's language and style conventions, analyze the diff, and post line-level comments plus a summary review back to the PR via `agentdock pr-review-helper`. Invoked by the PR Review workflow when a user asks `@bot review <PR URL>`.
---

# GitHub PR Review

Review a pull request by fingerprinting the repo, analyzing the diff, and
posting a `COMMENT`-event review back to GitHub. This is a mostly hands-off
workflow — minimize questions to the user.

## Input

You will receive a prompt containing:

- **Thread Context**: Slack messages leading to this review request
- **PR URL**: full `https://github.com/{owner}/{repo}/pull/{number}` URL
- **PR Number**: numeric PR number
- **Repository**: already cloned at the PR's HEAD on disk (cwd)
- **Environment**: `GITHUB_TOKEN` set; `PR_URL` set to the full URL

The helper `agentdock pr-review-helper` is on PATH (it is this worker's binary).

## Process

### 1. Fingerprint the repo

```bash
agentdock pr-review-helper fingerprint --pr-url "$PR_URL"
```

The helper prints the fingerprint JSON directly to stdout; parse it from
there. It contains `language`, `confidence`, `style_sources`, `test_runner`,
`framework`, `pr_touched_languages`, `pr_subprojects`.

Do NOT redirect to `/tmp/fp.json` or any path outside the current working
directory — opencode's sandbox treats cwd-external writes as
`external_directory` asks, and headless `opencode run` auto-rejects them,
cascade-failing the whole session. If you need the output in a file, write
it inside cwd (e.g. `./fp.json`).

Why: reviewing generic code without knowing the project's conventions produces
boilerplate feedback. The fingerprint tells you what rules to apply.

If `style_sources` is empty, note in the review summary that "no project style
file found; reviewing against general language conventions." If `pr_subprojects`
is non-empty, read those subdirectories' manifests / linter configs too — a
monorepo may have per-service rules.

### 2. Decide whether to short-circuit

Before looking at the diff, check whether detailed review makes sense:

- Pure lockfile (`yarn.lock`, `pnpm-lock.yaml`, `go.sum`, `Cargo.lock`, …)
- Generated files (anything in `vendor/`, `third_party/`, `node_modules/`, or
  files with `Code generated` header)
- Pure docs (`docs/`, `*.md`, `README*`)
- Pure CI config (`.github/workflows/*.yml`) — use judgement; workflows can
  have subtle bugs

If the diff is fully in one of these categories, skip to step 7 and emit
`===REVIEW_RESULT===` with `status: SKIPPED`, a short `reason`, and a summary
explaining why.

Why: line-level review on a 30K-line generated file is noise; honest skip is
better than fake feedback.

### 3. Analyze the diff

Fetch the diff:

```bash
# The helper also fetches this internally; you can inspect it directly for planning.
gh_api_url="https://api.github.com/repos/{owner}/{repo}/pulls/{number}/files"
```

Or just read files on disk (they're already checked out at PR head). Use
`git diff origin/<base>..HEAD` if you need to see the exact hunk shape.

For each concerning change, prepare a comment candidate:
- `path`: repo-relative file path
- `line`: the line number on the appropriate side
- `side`: `RIGHT` for added/context lines, `LEFT` for removed lines
- `start_line` + `start_side` (optional): for multi-line comments spanning 2+ lines
- `body`: markdown explanation; may include a ```suggestion block (see step 4)
- `severity`: `blocker`, `suggestion`, or `nit`

Severity guidance:
- **blocker**: correctness / security / data loss; reviewer would hard-block merge
- **suggestion**: a clearly better way to do it; reviewer would push back in a meeting
- **nit**: style / taste; reviewer would mention in passing

Why: the skill's downstream logic (summary severity + Slack display) depends
on honest severity calls. Don't call everything a blocker.

### 4. Use `suggestion` blocks when the fix is mechanical

GitHub renders ````suggestion ```` fenced blocks as "Commit suggestion"
buttons. When you know the exact replacement, include one:

````markdown
This should null-check `result`:

```suggestion
if result == nil {
    return ErrNotFound
}
return result.Value
```
````

The suggestion block must be a compilable/runnable replacement for the exact
lines the comment covers. Single-line comments replace one line; multi-line
comments (with `start_line`) replace the full range. Fuzzy hints stay in prose.

Why: suggestions turn review from passive to actionable. A reviewer who tells
you "add null-check here" is useful; one who also gives the code to paste is
*valuable*.

### 5. Compute the severity summary

- `clean` if zero findings
- `minor` if only `nit` comments
- `major` if any `blocker` or any `suggestion`

Why: the workflow's Slack result message and dashboards read this field. Keep
it honest; inflated severity trains reviewers to ignore bot output.

### 6. Assemble and post the review

Build the review JSON:

```json
{
  "summary": "One-sentence overall assessment.\n\n**Issues (N)**: X blockers, Y suggestions, Z nits.\n\n<optional detail sentence>",
  "severity_summary": "minor",
  "comments": [
    {
      "path": "services/api/handler.go",
      "line": 45,
      "side": "RIGHT",
      "body": "Null-check needed…",
      "severity": "blocker"
    }
  ]
}
```

Summary length ≤ 2000 chars. Individual comment bodies ≤ 4000 chars. The
helper truncates with `(truncated)` markers if you exceed either, but aim to
stay under.

Pipe to the helper:

```bash
cat review.json | agentdock pr-review-helper validate-and-post --pr-url "$PR_URL"
```

Read the helper's stdout JSON:
- `posted`: how many comments landed
- `skipped`: how many dropped (typically because their line wasn't in the diff)
- `skip_reasons`: list of `{path, line, reason}` for operators
- `review_id`: the GitHub review ID

Exit codes:
- `0`: all comments posted
- `1`: review posted, some comments skipped — still a success
- `2`: fatal (auth / 422 / rate limit exhaustion); nothing posted

Why pipe through the helper: it validates every comment's line against the
actual diff before posting. If you hallucinate a line number, the helper drops
that comment rather than posting garbage to the PR.

### 7. Emit the result marker

Output `===REVIEW_RESULT===` followed by a JSON object. Three shapes per the
status:

**POSTED** (review landed):

```json
{
  "status": "POSTED",
  "summary": "<same text posted to GitHub>",
  "comments_posted": 12,
  "comments_skipped": 3,
  "severity_summary": "minor"
}
```

**SKIPPED** (short-circuited in step 2):

```json
{
  "status": "SKIPPED",
  "summary": "Diff is vendored/generated; detailed review skipped.",
  "reason": "lockfile_only"
}
```

Valid `reason`: `lockfile_only`, `vendored`, `generated`, `pure_docs`,
`pure_config`.

**ERROR** (helper exit 2):

```json
{
  "status": "ERROR",
  "error": "PR head moved during review (422); please re-trigger",
  "summary": "<what you would have posted, for operators to see>"
}
```

Do not retry; the user re-mentions `@bot review <URL>` manually if they want.

## Special cases

- **Closed / merged PR**: review anyway. Note in summary that review is
  historical ("Reviewing a merged PR — suggestions are for learning only").
- **Force-push mid-review**: helper exits 2 with 422. Emit `status: ERROR`.
- **Empty diff after filtering** (every comment's line was outside the diff):
  helper returns `posted: 0`, `skipped: N`. Still emit `status: POSTED` — the
  summary alone posts fine, and `comments_skipped` reflects the quality problem.
