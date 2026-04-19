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
		Command:  "opencode",
		Args:     []string{"run", "{prompt}"},
		Timeout:  15 * time.Minute,
		SkillDir: ".opencode/skills",
	},
}
