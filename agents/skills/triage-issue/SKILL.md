---
name: triage-issue
description: Triage a bug or issue by exploring the codebase to find root cause, then create a GitHub issue with a TDD-based fix plan. Use when user reports a bug, wants to file an issue, mentions "triage", or wants to investigate and plan a fix for a problem.
---

# Triage Issue

Investigate a reported problem, find its root cause, and create a GitHub issue with a TDD fix plan. This is a mostly hands-off workflow - minimize questions to the user.

## Input

You will receive a prompt containing:
- **Thread Context**: Slack conversation messages describing the problem
- **Repository**: local path and branch to investigate
- **Issue Metadata**: GitHub repo (owner/repo), channel, reporter, labels
- **Attachments**: downloaded files (if any)

## Process

### 1. Understand the problem

Read the thread context carefully. Do NOT ask follow-up questions. Start investigating immediately based on the conversation.

### 2. Explore and diagnose

Deeply investigate the codebase. Your goal is to find:

- **Where** the bug manifests (entry points, UI, API responses)
- **What** code path is involved (trace the flow)
- **Why** it fails (the root cause, not just the symptom)
- **What** related code exists (similar patterns, tests, adjacent modules)

Look at:
- Related source files and their dependencies
- Existing tests (what's tested, what's missing)
- Recent changes to affected files (`git log` on relevant files)
- Error handling in the code path
- Similar patterns elsewhere in the codebase that work correctly

### 3. Assess confidence

After investigation, assess your confidence:

- **high**: Clear root cause found, code path traced, fix approach identified
- **medium**: Likely root cause found, but some uncertainty remains
- **low**: Could not find relevant code, problem likely unrelated to this repo

**If confidence is low**: Do NOT create an issue. Instead, output ONLY:
```
===TRIAGE_RESULT===
REJECTED: [brief explanation why this problem is unrelated to the repo]
```
Then stop.

### 4. Identify the fix approach

Based on your investigation, determine:

- The minimal change needed to fix the root cause
- Which modules/interfaces are affected
- What behaviors need to be verified via tests
- Whether this is a regression, missing feature, or design flaw

### 5. Design TDD fix plan

Create a concrete, ordered list of RED-GREEN cycles. Each cycle is one vertical slice:

- **RED**: Describe a specific test that captures the broken/missing behavior
- **GREEN**: Describe the minimal code change to make that test pass

Rules:
- Tests verify behavior through public interfaces, not implementation details
- One test at a time, vertical slices (NOT all tests first, then all code)
- Each test should survive internal refactors
- Include a final refactor step if needed
- **Durability**: Only suggest fixes that would survive radical codebase changes. Describe behaviors and contracts, not internal structure. Tests assert on observable outcomes (API responses, UI state, user-visible effects), not internal state.

### 6. Create the GitHub issue

Use the metadata from the prompt to create the issue. The prompt will include `github_repo`, `channel`, `reporter`, `branch`, and `labels`.

Create the issue using `gh issue create`:

```bash
gh issue create --repo {github_repo} --title "{title}" --label "{label1}" --label "{label2}" --body "..."
```

Use this template for the issue body:

```
**Channel**: #{channel}
**Reporter**: {reporter}
**Branch**: {branch}

---

## Problem

A clear description of the bug or issue, including:
- What happens (actual behavior)
- What should happen (expected behavior)
- How to reproduce (if applicable)

## Root Cause Analysis

Describe what you found during investigation:
- The code path involved
- Why the current code fails
- Any contributing factors

Do NOT include specific file paths, line numbers, or implementation details that couple to current code layout. Describe modules, behaviors, and contracts instead.

## TDD Fix Plan

1. **RED**: Write a test that [describes expected behavior]
   **GREEN**: [Minimal change to make it pass]

2. **RED**: Write a test that [describes next behavior]
   **GREEN**: [Minimal change to make it pass]

**REFACTOR**: [Any cleanup needed after all tests pass]

## Acceptance Criteria

- [ ] Criterion 1
- [ ] Criterion 2
- [ ] All new tests pass
- [ ] Existing tests still pass
```

### 7. Output result

After creating the issue, output:
```
===TRIAGE_RESULT===
CREATED: {issue_url}
```

If `gh issue create` fails, output:
```
===TRIAGE_RESULT===
ERROR: {error message}
```
