# PR #33 Review Fixes Design

**Date**: 2026-04-15
**Status**: Draft
**Branch**: `feat/cobra-cli-migration` (fix before merge)

## Context

Code review of PR #33 (cobra + koanf CLI migration) identified three issues
to fix before merge. All are small, targeted changes with no architectural
impact.

## Fix 1: `atomicWrite` stale tmp file permissions

### Problem

`atomicWrite` calls `os.WriteFile(tmp, data, 0600)`. If the `.tmp` file
already exists from a previous failed write with different permissions,
`WriteFile` does NOT change the mode of an existing file. The config file
(containing secrets) briefly sits at the stale tmp's permissions.

### Solution

Add `os.Remove(tmp)` before `os.WriteFile`. This ensures `WriteFile` always
creates a new file, so the mode parameter is guaranteed to take effect.

### Files

- `cmd/agentdock/init.go`: `atomicWrite` — add `os.Remove(tmp)` before write
- `cmd/agentdock/init_test.go`: new test — pre-create `.tmp` with 0644, call
  `atomicWrite` with 0600, verify final file has 0600

## Fix 2: `warnUnknownKeys` false negative on nested struct keys

### Problem

`warnUnknownKeys` short-circuits on any valid top-level key:
```go
if !valid[topLevel] && !valid[key] { warn }
```
This means `queue.nonexistent_field` is silently accepted because
`valid["queue"]` is true. The check was intended for map-typed fields
(`agents`, `channels`, `channel_priority`) whose sub-keys are dynamic,
but it blanket-allows arbitrary sub-keys under any valid top-level struct.

### Solution

Use reflection to distinguish map-typed top-level fields from struct-typed
ones. Only map-typed fields allow arbitrary sub-keys.

Changes:

1. Add a `mapKeys map[string]bool` output parameter to `walkYAMLPathsKeyOnly`.
   When a field's type is `reflect.Map`, record it in `mapKeys` and return
   (sub-keys are dynamic). No rename — keep the function name to minimize
   diff churn.

2. `validKoanfKeys()` returns two sets: `valid` (all known dotted paths) and
   `mapKeys` (top-level keys whose type is map).

3. `warnUnknownKeys` uses `mapKeys` for the short-circuit instead of `valid`:
   ```go
   if mapKeys[topLevel] { continue }
   if !valid[key] { warn }
   ```

### Files

- `cmd/agentdock/config.go`: `walkYAMLPathsKeyOnly`, `validKoanfKeys`,
  `warnUnknownKeys` — as described above
- `cmd/agentdock/config_test.go`: new test — verify `queue.nonexistent_field`
  triggers warning; verify `agents.myagent.command` does not

## Fix 3: `init -i` non-TTY footgun

### Problem

`agentdock init -i` enters interactive mode unconditionally. If stdin is not
a terminal (CI, Docker, piped input), `term.ReadPassword` fails silently,
`promptHidden` returns `""`, and the retry loop runs 3 times before failing
with a confusing error. Unlike `preflight.go` which guards with
`term.IsTerminal`, `init -i` has no such guard.

### Solution

Add a TTY check at the top of `runInit` when `interactive=true`:
```go
if interactive && !term.IsTerminal(int(syscall.Stdin)) {
    return fmt.Errorf("--interactive requires a terminal (stdin is not a TTY)")
}
```

### Files

- `cmd/agentdock/init.go`: `runInit` — add TTY guard, add `syscall` and
  `golang.org/x/term` imports if not already present
- `cmd/agentdock/init_test.go`: new test — call `runInit(path, true, false)`
  in test environment (non-TTY stdin), expect error containing
  `"requires a terminal"`. Note: this is a negative-path-only test. The
  happy path (interactive on a real TTY) cannot be unit-tested without
  refactoring to inject `isTerminal`; manual smoke test covers it.

## Validation

After all three fixes, run `go test ./cmd/agentdock/...` to verify no
regressions across the full package.

## Implementation order

All three fixes are independent. Can be done in any order or in parallel.
Single commit per fix for clean git history.

## Out of scope

The remaining review findings (#2 koanf `_ =`, #4 mutation chain, #5
duplicate retry logic, #7 version bump coordination, #8 plan file cleanup)
are deferred to follow-up work.
