package skill

import (
	"os"
	"testing"
	"time"
)

func TestLoadSkillsConfig_Full(t *testing.T) {
	yaml := `
skills:
  triage-issue:
    type: local
    path: app/agents/skills/triage-issue
  code-review:
    type: remote
    package: "@someone/skill-code-review"
    version: "latest"
  security-audit:
    type: remote
    package: "@team/security-skills"
    version: "^2.0.0"
    timeout: 60s
cache:
  ttl: 5m
`
	f, _ := os.CreateTemp("", "skills-*.yaml")
	f.WriteString(yaml)
	f.Close()
	defer os.Remove(f.Name())

	cfg, err := LoadSkillsConfig(f.Name())
	if err != nil {
		t.Fatalf("LoadSkillsConfig: %v", err)
	}

	if len(cfg.Skills) != 3 {
		t.Fatalf("skills count = %d, want 3", len(cfg.Skills))
	}

	local := cfg.Skills["triage-issue"]
	if local.Type != "local" {
		t.Errorf("triage-issue type = %q", local.Type)
	}
	if local.Path != "app/agents/skills/triage-issue" {
		t.Errorf("triage-issue path = %q", local.Path)
	}

	remote := cfg.Skills["code-review"]
	if remote.Type != "remote" {
		t.Errorf("code-review type = %q", remote.Type)
	}
	if remote.Package != "@someone/skill-code-review" {
		t.Errorf("code-review package = %q", remote.Package)
	}
	if remote.Version != "latest" {
		t.Errorf("code-review version = %q", remote.Version)
	}

	audit := cfg.Skills["security-audit"]
	if audit.Timeout != 60*time.Second {
		t.Errorf("security-audit timeout = %v", audit.Timeout)
	}

	if cfg.Cache.TTL != 5*time.Minute {
		t.Errorf("cache ttl = %v", cfg.Cache.TTL)
	}
}

func TestLoadSkillsConfig_Defaults(t *testing.T) {
	yaml := `
skills:
  review:
    type: remote
    package: "@team/review"
`
	f, _ := os.CreateTemp("", "skills-*.yaml")
	f.WriteString(yaml)
	f.Close()
	defer os.Remove(f.Name())

	cfg, err := LoadSkillsConfig(f.Name())
	if err != nil {
		t.Fatalf("LoadSkillsConfig: %v", err)
	}

	s := cfg.Skills["review"]
	if s.Version != "latest" {
		t.Errorf("default version = %q, want latest", s.Version)
	}
	if s.Timeout != 30*time.Second {
		t.Errorf("default timeout = %v, want 30s", s.Timeout)
	}
	if cfg.Cache.TTL != 5*time.Minute {
		t.Errorf("default cache TTL = %v, want 5m", cfg.Cache.TTL)
	}
}

func TestLoadSkillsConfig_FileNotFound(t *testing.T) {
	_, err := LoadSkillsConfig("/nonexistent/skills.yaml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}
