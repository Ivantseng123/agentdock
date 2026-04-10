---
name: triage-issue
description: Triage a bug or issue by exploring the codebase to find root cause, then output a structured triage result with a TDD-based fix plan. Use when user reports a bug, wants to file an issue, mentions "triage", or wants to investigate and plan a fix for a problem.
---

# Triage Issue

Investigate a reported problem, find its root cause, and produce a structured triage result with a TDD fix plan. This is a mostly hands-off workflow - minimize questions to the user.

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
{
  "status": "REJECTED",
  "message": "Brief explanation why this problem is unrelated to the repo"
}
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

### 6. Output result

After your investigation, output the result in this exact JSON format inside the sentinel marker.

For a successful triage (confidence is high or medium):
```
===TRIAGE_RESULT===
{
  "status": "CREATED",
  "title": "Concise issue title",
  "body": "Full markdown issue body including:\n\n**Channel**: #{channel}\n**Reporter**: {reporter}\n**Branch**: {branch}\n\n---\n\n## Problem\n...\n\n## Root Cause Analysis\n...\n\n## TDD Fix Plan\n...\n\n## Acceptance Criteria\n...",
  "labels": ["bug"],
  "confidence": "high",
  "files_found": 5,
  "open_questions": 0
}
```

Use this template for the body field:

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

For a rejection (confidence is low):
```
===TRIAGE_RESULT===
{
  "status": "REJECTED",
  "message": "Brief explanation why this problem is unrelated to the repo"
}
```

For an error:
```
===TRIAGE_RESULT===
{
  "status": "ERROR",
  "message": "What went wrong"
}
```

**Important:** The body field should contain the complete issue body as a single string with \n for newlines. Do NOT use `gh issue create` — just output the JSON.
