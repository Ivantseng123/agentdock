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

func TestAgentConfig_ExtraArgsUnmarshal(t *testing.T) {
	cfg := loadFromString(t, `
agents:
  opencode:
    command: opencode
    extra_args: ["-m", "opencode/claude-opus-4-7"]
`)
	oc, ok := cfg.Agents["opencode"]
	if !ok {
		t.Fatal("agents.opencode missing after unmarshal")
	}
	if len(oc.ExtraArgs) != 2 {
		t.Fatalf("ExtraArgs len = %d, want 2; got %v", len(oc.ExtraArgs), oc.ExtraArgs)
	}
	if oc.ExtraArgs[0] != "-m" {
		t.Errorf("ExtraArgs[0] = %q, want \"-m\"", oc.ExtraArgs[0])
	}
	if oc.ExtraArgs[1] != "opencode/claude-opus-4-7" {
		t.Errorf("ExtraArgs[1] = %q, want \"opencode/claude-opus-4-7\"", oc.ExtraArgs[1])
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
