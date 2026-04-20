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
		Command:  "claude",
		Args:     []string{"--print", "--output-format", "stream-json", "-p", "{prompt}"},
		Timeout:  15 * time.Minute,
		SkillDir: ".claude/skills",
		Stream:   true,
	},
	"codex": {
		Command: "codex",
		Args:    []string{"exec", "--skip-git-repo-check", "--color", "never", "{prompt}"},
		Timeout: 15 * time.Minute,
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
		Args:     []string{"run", "--pure", "{prompt}"},
		Timeout:  15 * time.Minute,
		SkillDir: ".opencode/skills",
	},
}
