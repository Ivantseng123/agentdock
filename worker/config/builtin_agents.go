package config

import "time"

// BuiltinAgents is the canonical registry of agent CLI configurations shipped
// with AgentDock. Config files may override individual entries by defining a
// same-named entry under `agents:`; missing names fall back to these defaults.
//
// Adding a new built-in agent: just add an entry here. Existing users get it
// automatically on next startup; no `agentdock init` rerun needed.
//
// Placement study for `{extra_args}` — per CLI flag-order rules:
//
//   - claude: flags MUST precede `-p`. `--print`/`--output-format stream-json`
//     are flags; `-p "{prompt}"` is the positional-ish pair. `{extra_args}`
//     goes after `stream-json` and BEFORE `-p`.
//   - codex: `codex exec <flags> <prompt>`. `--skip-git-repo-check`/`--color
//     never` are flags; prompt is positional. `{extra_args}` goes right before
//     `{prompt}`.
//   - opencode: `opencode run <flags> <prompt>`. `--pure` and `--format json`
//     are worker-managed flags; `-m`, `--agent`, `--variant`, `-c`,
//     `--session`, `-f` all live in this same pre-prompt flag slot.
//     `{extra_args}` goes between `--format json` and `{prompt}`.
//
// `--format json` is required for the StreamFormatOpencode parser to receive
// NDJSON. Operators who override args via `extra_args` and accidentally
// re-set `--format default` will silently lose tool-name visibility — the
// runner falls back to a counter line, no regression in stdout capture.
//
// Runtime: nil/empty `extra_args` → the `{extra_args}` slot is dropped
// entirely (NO empty string element). See runner.go expandExtraArgs.
var BuiltinAgents = map[string]AgentConfig{
	"claude": {
		Command:      "claude",
		Args:         []string{"--print", "--output-format", "stream-json", "{extra_args}", "-p", "{prompt}"},
		Timeout:      30 * time.Minute,
		SkillDir:     ".claude/skills",
		StreamFormat: StreamFormatClaude,
	},
	"codex": {
		Command: "codex",
		Args:    []string{"exec", "--skip-git-repo-check", "--color", "never", "{extra_args}", "{prompt}"},
		Timeout: 30 * time.Minute,
		// Codex CLI discovers skills from .agents/skills (repo/CWD scope),
		// NOT .codex/skills. See https://developers.openai.com/codex/skills.
		SkillDir: ".agents/skills",
	},
	"opencode": {
		Command: "opencode",
		// --pure skips external plugins (oh-my-openagent et al). Without it,
		// project-level plugins that dispatch sub-agents via an async
		// BackgroundManager cause `opencode run` to exit on the main agent's
		// first "I dispatched" text — before the sub-agents return a parseable
		// TRIAGE_RESULT. Internal auth plugins still load, so credentials stay intact.
		//
		// --format json switches stdout to NDJSON so the worker can surface
		// tool-name activity into Slack progress. opencode 1.14.x and later;
		// older versions silently fall through (parser sees no events,
		// LastTool stays empty, Slack shows the counter line).
		Args:         []string{"run", "--pure", "--format", "json", "{extra_args}", "{prompt}"},
		Timeout:      30 * time.Minute,
		SkillDir:     ".opencode/skills",
		StreamFormat: StreamFormatOpencode,
	},
}
