# AgentDock

Slack → GitHub issue triage bot. Spawns external CLI agents (`claude`, `opencode`, `codex`, `gemini`) to explore a cloned repo and draft the issue body; this bot only orchestrates.

Overview, architecture, build/run, tests, and release flow live in `README.md` and `docs/`. Do not duplicate them here.

## Landmines

- **Binary is `agentdock`, not `bot`.** Entry is `cmd/agentdock/`, and it requires a subcommand (`app`, `worker`, `init`, ...). Any instruction saying `./bot -config ...` is pre-v1 and wrong.
- **App, worker, and shared are separate Go modules.** `app/`, `worker/`, and `shared/` each have their own `go.mod`. `internal/` no longer exists — do not recreate it. Any advice referencing `internal/<anything>` is pre-v2 and stale.
- **Import direction is enforced by `test/import_direction_test.go`**: `app ✗ worker`, `worker ✗ app`, `shared ✗ app|worker`. Only the root module (cmd/, test/) may import all three. The test fails the CI Test job on any violation.
- **Config is split into `app.yaml` and `worker.yaml`.** There is no migration tool. Users rebuild via `agentdock init app` and `agentdock init worker`. See `docs/MIGRATION-v2.md`.
- **`worker.yaml` is flat, not nested.** Top-level `count` and `prompt.extra_rules` — NOT `worker.count` / `worker.prompt.extra_rules`.
- **Inmem mode is gone (v2.1+).** Only `queue.transport: redis` is supported. The transport switch in `app/app.go` and `worker/worker.go` is preserved as the extension point; adding a new backend means adding a case there, not removing the field.
- **`/triage` is not a trigger anymore.** It only prints a usage hint because Slack doesn't expose thread context to slash commands. Real triggers are `@bot` mentions inside a thread.
- **Slack `invalid_blocks`:** do not combine `MsgOptionMetadata` with `MsgOptionBlocks` in the same post — they reject silently together.
- **Full clone required for branch listing.** Shallow clones can't enumerate branches. `shared/github/repo.go` uses `fetch --all --prune`; keep it that way.
- **Reporter tag is plain text.** Slack display name ≠ GitHub handle. Never render `@username` into issue bodies.
- **Worker may run on a user's real machine, not an isolated pod.** Do NOT propose flags / settings that disable agent sandboxing on the host (e.g. `opencode --dangerously-skip-permissions`, `claude --dangerously-skip-permissions`, granting blanket write access). Such flags would let the agent touch `$HOME`, `/etc`, SSH keys, etc. on the operator's box. Solutions for permission/sandbox issues must work in BOTH worker-in-pod and worker-on-laptop deployments — prefer fixing skill/prompt instructions or scoping cwd-relative writes over loosening the agent's host permissions.

## Product Positioning

This is a **structuring tool, not a diagnosis tool.** The core value is turning Slack threads into well-formatted GitHub issues. AI triage (file pointers, confidence scoring) is a bonus — do not sacrifice thread-capture reliability to improve diagnostic quality.

## Commit hygiene

CI 跑 `wagoid/commitlint-github-action@v6`（`@commitlint/config-conventional` 預設規則），任何 PR 的 commit 訊息違規會擋下 merge。常踩到的點：

- `subject-case`：subject 不能是 sentence-case / start-case / pascal-case / upper-case。專有名詞如 `GitHub App` 出現在 subject 開頭會中招——用祈使句開頭（`add GitHub App preflight at startup` ✓，`GitHub App preflight at startup` ✗）。
- `footer-leading-blank`：trailer 行（`Closes:`、`Co-Authored-By:` 等）前要空一行。

Push 前 local 驗證（一次性設定 + 後續單行）：

```bash
# 一次性
mkdir -p /tmp/commitlint && cd /tmp/commitlint && npm init -y >/dev/null
npm install @commitlint/cli @commitlint/config-conventional
echo "module.exports = { extends: ['@commitlint/config-conventional'] };" > commitlint.config.js

# 檢查最新一個 commit
git log -1 --format="%B" | (cd /tmp/commitlint && ./node_modules/.bin/commitlint)

# 檢查整條 branch（main..HEAD）
git log main..HEAD --format="%H" | while read sha; do
  git log -1 --format="%B" "$sha" | (cd /tmp/commitlint && ./node_modules/.bin/commitlint) || echo "↑ $sha"
done
```

Exit 0 = 過。violation 修法：要嘛尚未 push 之前 `git commit --amend`，要嘛之後 `git rebase` + force-push（force-push 屬於不可逆，請先確認分支沒被別人 fork）。

## Routing

- Logging conventions (component/phase taxonomy, attribute names, Chinese message format): `shared/logging/GUIDE.md`
- v2 migration (app/worker module split, config rebuild, v2.0→v2.2 follow-ups): `docs/MIGRATION-v2.md`
- Historical specs and plans: `docs/superpowers/`
