package config

import "time"

// BuiltinAgents is the canonical registry of agent CLI configurations shipped
// with AgentDock. Config files may override individual entries by defining a
// same-named entry under `agents:`; missing names fall back to these defaults.
//
// Adding a new built-in agent: just add an entry here. Existing users get it
// automatically on next startup; no `agentdock init` rerun needed.
var BuiltinAgents = map[string]AgentConfig{
	"claude": {
		Command: "claude",
		// {extra_args} sits after --output-format stream-json and before -p
		// because the claude CLI requires all option flags to appear before the
		// -p/--print positional. Any extra flags (e.g. --model, --effort,
		// --dangerously-skip-permissions) must therefore land here.
		Args:     []string{"--print", "--output-format", "stream-json", "{extra_args}", "-p", "{prompt}"},
		Timeout:  30 * time.Minute,
		SkillDir: ".claude/skills",
		Stream:   true,
	},
	"codex": {
		Command: "codex",
		// {extra_args} sits between the fixed flags and the positional prompt
		// argument. The codex exec sub-command accepts option flags anywhere
		// before the positional, so extra flags (e.g. --sandbox) go here.
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
		// {extra_args} sits between --pure and the positional prompt. Flags such
		// as -m/--model, --agent, --variant, -c/--config, and --session are all
		// accepted before the positional, making this the safe injection point.
		Args:     []string{"run", "--pure", "{extra_args}", "{prompt}"},
		Timeout:  30 * time.Minute,
		SkillDir: ".opencode/skills",
	},
}
