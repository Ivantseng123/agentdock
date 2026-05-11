<!--
AI authoring rules:
1. Follow the section order; do not change headings.
2. "Verification" lists only commands actually run and their output. If a command was not run, write "not run — reason: …". Do not paste the pre-filled commands back as if they had been executed.
3. "Module touched": mark `[x]` for modules touched, `[ ]` for untouched. Do not delete any item.
4. "Release note" defaults to NONE. Only fill in content when operators need to change config, rebuild the environment, or change usage (see #214).
5. Delete inapplicable sections entirely (including the heading).
-->

## What this PR does / why

<!-- Explain the problem and the solution; go further than the commit message. -->

## Module touched

- [ ] `app/`
- [ ] `worker/`
- [ ] `shared/`
- [ ] `cmd/`
- [ ] `docs/` / `.github/`

## Related issues

<!-- At least one entry; write N/A if unrelated. -->

- Fixes #
- Refs #
- Part of #

## Verification

<!-- Commands actually run + a summary of output. Write "not run — reason: …" if not executed. -->

```bash
go build ./... && (cd app && go build ./...) && (cd worker && go build ./...) && (cd shared && go build ./...)
go test ./... -race && (cd app && go test ./... -race) && (cd worker && go test ./... -race) && (cd shared && go test ./... -race)
go test ./test/... -run TestImportDirection
git log -1 --pretty=%B | npx --yes commitlint --extends @commitlint/config-conventional
```

## Release note

<!--
release-please consumes this block to generate the CHANGELOG and bump the version.
Metric label renames, internal refactors, and test cleanup → NONE.
Use BREAKING CHANGE only when operators must change config, rebuild the environment, or change usage.
-->

```release-note
NONE
```

## Notes for reviewer

<!-- Optional. Risk points, files needing extra attention, follow-up plans. Delete the section if not needed. -->
