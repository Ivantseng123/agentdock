# AgentDock

Slack → GitHub issue triage bot. Spawns external CLI agents (`claude`, `opencode`, `codex`, `gemini`) to explore a cloned repo and draft the issue body; this bot only orchestrates.

Overview, architecture, build/run, tests, and release flow live in `README.md` and `docs/`. Do not duplicate them here.

## Landmines

- **Binary is `agentdock`, not `bot`.** Entry is `cmd/agentdock/`, and it requires a subcommand (`app`, `worker`, `init`, ...). Any instruction saying `./bot -config ...` is pre-v1 and wrong; see `docs/MIGRATION-v1.md`.
- **App, worker, and shared are separate Go modules.** `app/`, `worker/`, and `shared/` each have their own `go.mod`. `internal/` no longer exists — do not recreate it. Any advice referencing `internal/<anything>` is pre-v2 and stale.
- **Import direction is enforced by `test/import_direction_test.go`**: `app ✗ worker`, `worker ✗ app`, `shared ✗ app|worker`. Only the root module (cmd/, test/) may import all three. The test fails the CI Test job on any violation.
- **Config is split into `app.yaml` and `worker.yaml`.** There is no migration tool. Users rebuild via `agentdock init app` and `agentdock init worker`. See `docs/MIGRATION-v2.md`.
- **`worker.yaml` is flat, not nested.** Top-level `count` and `prompt.extra_rules` — NOT `worker.count` / `worker.prompt.extra_rules`.
- **`/triage` is not a trigger anymore.** It only prints a usage hint because Slack doesn't expose thread context to slash commands. Real triggers are `@bot` mentions inside a thread.
- **Slack `invalid_blocks`:** do not combine `MsgOptionMetadata` with `MsgOptionBlocks` in the same post — they reject silently together.
- **Full clone required for branch listing.** Shallow clones can't enumerate branches. `shared/github/repo.go` uses `fetch --all --prune`; keep it that way.
- **Reporter tag is plain text.** Slack display name ≠ GitHub handle. Never render `@username` into issue bodies.

## Product Positioning

This is a **structuring tool, not a diagnosis tool.** The core value is turning Slack threads into well-formatted GitHub issues. AI triage (file pointers, confidence scoring) is a bonus — do not sacrifice thread-capture reliability to improve diagnostic quality.

## Routing

- Logging conventions (component/phase taxonomy, attribute names, Chinese message format): `shared/logging/GUIDE.md`
- v1 migration (binary rename, subcommands, config path): `docs/MIGRATION-v1.md`
- v2 migration (app/worker module split, config rebuild): `docs/MIGRATION-v2.md`
- Historical specs and plans: `docs/superpowers/`
