package config

import (
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func loadFromString(t *testing.T, yamlContent string) *Config {
	t.Helper()
	var cfg Config
	if err := yaml.Unmarshal([]byte(yamlContent), &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	ApplyDefaults(&cfg)
	return &cfg
}

func TestLoadConfig_FlatSchema(t *testing.T) {
	cfg := loadFromString(t, `
count: 7
prompt:
  extra_rules:
    - "no guessing"
    - "only real files"
agents:
  claude:
    command: claude
    args: ["--print", "-p", "{prompt}"]
providers: [claude]
`)
	if cfg.Count != 7 {
		t.Errorf("Count = %d, want 7 (flat schema)", cfg.Count)
	}
	if len(cfg.Prompt.ExtraRules) != 2 {
		t.Errorf("ExtraRules len = %d, want 2", len(cfg.Prompt.ExtraRules))
	}
	if cfg.Prompt.ExtraRules[0] != "no guessing" {
		t.Errorf("ExtraRules[0] = %q", cfg.Prompt.ExtraRules[0])
	}
	if len(cfg.Providers) != 1 || cfg.Providers[0] != "claude" {
		t.Errorf("providers = %v", cfg.Providers)
	}
}

// TestLoadConfig_LegacyActiveAgentIgnored verifies that a yaml file containing
// the removed active_agent key does not panic and leaves Providers empty (the
// caller is responsible for handling empty providers as a config error).
func TestLoadConfig_LegacyActiveAgentIgnored(t *testing.T) {
	cfg := loadFromString(t, `
agents:
  claude:
    command: claude
    args: ["--print", "-p", "{prompt}"]
active_agent: claude
`)
	// active_agent is an unknown key after removal; Providers must remain empty.
	if len(cfg.Providers) != 0 {
		t.Errorf("Providers = %v, want empty (legacy active_agent must not populate providers)", cfg.Providers)
	}
}

func TestApplyDefaults_Count(t *testing.T) {
	cfg := loadFromString(t, ``)
	if cfg.Count != 3 {
		t.Errorf("default count = %d, want 3", cfg.Count)
	}
}

func TestApplyDefaults_AgentTimeout(t *testing.T) {
	cfg := loadFromString(t, `
agents:
  claude:
    command: claude
`)
	claude := cfg.Agents["claude"]
	if claude.Timeout != 5*time.Minute {
		t.Errorf("default agent timeout = %v, want 5m", claude.Timeout)
	}
}

func TestResolveSecrets_MergesGitHubToken(t *testing.T) {
	cfg := loadFromString(t, `
github:
  token: ghp-worker
`)
	if cfg.Secrets["GH_TOKEN"] != "ghp-worker" {
		t.Errorf("GH_TOKEN = %q", cfg.Secrets["GH_TOKEN"])
	}
}

func TestLoadConfig_AgentRequiredSecretsOverride(t *testing.T) {
	cfg := loadFromString(t, `
agents:
  custom-agent:
    command: /usr/bin/custom-agent
    args: ["{prompt}"]
    required_secrets: [GH_TOKEN, OPENAI_API_KEY]
`)
	agent, ok := cfg.Agents["custom-agent"]
	if !ok {
		t.Fatal("custom-agent not found in Agents map")
	}
	if len(agent.RequiredSecrets) != 2 {
		t.Fatalf("RequiredSecrets len = %d, want 2; got %v", len(agent.RequiredSecrets), agent.RequiredSecrets)
	}
	if agent.RequiredSecrets[0] != "GH_TOKEN" {
		t.Errorf("RequiredSecrets[0] = %q, want GH_TOKEN", agent.RequiredSecrets[0])
	}
	if agent.RequiredSecrets[1] != "OPENAI_API_KEY" {
		t.Errorf("RequiredSecrets[1] = %q, want OPENAI_API_KEY", agent.RequiredSecrets[1])
	}
}

func TestLoadConfig_AgentRequiredSecretsEmptyList(t *testing.T) {
	cfg := loadFromString(t, `
agents:
  zero-trust:
    command: /usr/bin/zero-trust
    args: ["{prompt}"]
    required_secrets: []
`)
	agent, ok := cfg.Agents["zero-trust"]
	if !ok {
		t.Fatal("zero-trust not found in Agents map")
	}
	// Explicit empty list must unmarshal as []string{}, not nil.
	if agent.RequiredSecrets == nil {
		t.Error("RequiredSecrets must be []string{} (non-nil) for explicit empty list, got nil")
	}
	if len(agent.RequiredSecrets) != 0 {
		t.Errorf("RequiredSecrets len = %d, want 0; got %v", len(agent.RequiredSecrets), agent.RequiredSecrets)
	}
}

func TestLoadConfig_AgentRequiredSecretsAbsentIsNil(t *testing.T) {
	cfg := loadFromString(t, `
agents:
  legacy-agent:
    command: /usr/bin/legacy
    args: ["{prompt}"]
`)
	agent, ok := cfg.Agents["legacy-agent"]
	if !ok {
		t.Fatal("legacy-agent not found in Agents map")
	}
	// Field absent from yaml must unmarshal as nil (not empty slice).
	if agent.RequiredSecrets != nil {
		t.Errorf("RequiredSecrets must be nil when absent from yaml, got %v", agent.RequiredSecrets)
	}
}
