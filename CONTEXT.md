# AgentDock

AgentDock is a Slack-driven orchestration tool that routes user requests into
workflow-specific GitHub and worker execution paths. This context exists to
keep workflow, auth, and repo-routing terms stable as the product grows.

## Language

**Repo-bound workflow entrypoint**:
An app-side workflow step that already knows the target **Repository** and may touch GitHub before worker execution.
_Avoid_: pre-submit hook, early GitHub call

**Repository**:
A target `owner/repo` that a workflow reads from, files against, or reviews against.
_Avoid_: project, codebase target

**App-covered repository**:
A **Repository** that is included in the configured GitHub App installation's accessible repo set.
_Avoid_: installed repo, App repo

**PAT fallback**:
The explicit use of `github.token` for a **Repository** that is not an **App-covered repository**.
_Avoid_: legacy token path, hidden token dependency

**PR base repository**:
The `owner/repo` that owns the PR URL and receives review comments or review state changes.
_Avoid_: target fork, clone repo

**PR head repository**:
The `owner/repo` that provides the head commit the worker actually clones for PR analysis.
_Avoid_: reviewed repo, base fork

## Relationships

- A **Repo-bound workflow entrypoint** operates on exactly one primary **Repository**
- An **App-covered repository** uses GitHub App auth as the primary path
- A **Repository** outside the App installation may use **PAT fallback** if `github.token` is configured
- A PR review validates access against the **PR base repository** before submit
- A PR review executes against the **PR head repository** during worker clone and analysis

## Example dialogue

> **Dev:** "This PR URL validates before submit, so does it count as a **Repo-bound workflow entrypoint**?"
> **Domain expert:** "Yes. It already knows the target **Repository**, so it must use App auth for an **App-covered repository** and only use **PAT fallback** when the repo is outside the installation."

> **Dev:** "For a forked PR, which repo decides auth?"
> **Domain expert:** "Pre-submit validation is against the **PR base repository**; worker execution routes against the **PR head repository** because that's what gets cloned."

## Flagged ambiguities

- "GitHub App support" was used to mean both "the app can mint installation tokens" and "every **Repo-bound workflow entrypoint** actually uses them" — resolved: those are different claims, and this repo tracks the second separately.
- "the PR repo" was ambiguous between the **PR base repository** and the **PR head repository** — resolved: validation and review-target access use base; clone/execution access use head.
