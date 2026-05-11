---
name: Refactor / Tech Debt
about: Internal cleanup, structural changes, renames, and other non-functional changes
title: 'refactor(<scope>): '
labels: enhancement
---

<!--
AI authoring rules:
1. Short template; no RCA or multi-option comparison needed.
2. If the change crosses the app/worker/shared import boundary (enforced by `test/import_direction_test.go`), state the expected impact under "Proposal".
3. `<scope>` must be one of: `worker`, `queue`, `config`, `workflow`, `shared`, `app`.
4. Delete inapplicable sections entirely.
-->

## Current state

<!-- What it looks like today + why you want to change it. -->

## Proposal

<!-- Files / packages expected to change; call out any import-boundary impact. -->

## Verification

```bash
go build ./... && (cd app && go build ./...) && (cd worker && go build ./...) && (cd shared && go build ./...)
go test ./... -race && (cd app && go test ./... -race) && (cd worker && go test ./... -race) && (cd shared && go test ./... -race)
go test ./test/... -run TestImportDirection
```

## Related

<!-- Refs #N / Part of #N; "N/A" if none. -->
