package config

import (
	"strings"
	"testing"
	"time"
)

func TestOpencodeConfig_Defaults_WhenBlockAbsent(t *testing.T) {
	cfg := loadFromString(t, `
count: 1
agents:
  opencode:
    command: opencode
`)
	if cfg.Opencode.Mode != OpencodeModeSpawn {
		t.Errorf("Mode default = %q, want %q", cfg.Opencode.Mode, OpencodeModeSpawn)
	}
	if cfg.Opencode.IdleTimeout != 5*time.Minute {
		t.Errorf("IdleTimeout default = %s, want 5m0s", cfg.Opencode.IdleTimeout)
	}
	if cfg.Opencode.StorageDir != "" {
		t.Errorf("StorageDir default = %q, want empty (runtime-resolved)", cfg.Opencode.StorageDir)
	}
}

func TestOpencodeConfig_CustomValuesFromYAML(t *testing.T) {
	cfg := loadFromString(t, `
count: 1
opencode:
  mode: server
  idle_timeout: 90s
  storage_dir: /var/lib/agentdock/opencode
agents:
  opencode:
    command: opencode
`)
	if cfg.Opencode.Mode != OpencodeModeServer {
		t.Errorf("Mode = %q, want %q", cfg.Opencode.Mode, OpencodeModeServer)
	}
	if cfg.Opencode.IdleTimeout != 90*time.Second {
		t.Errorf("IdleTimeout = %s, want 1m30s", cfg.Opencode.IdleTimeout)
	}
	if cfg.Opencode.StorageDir != "/var/lib/agentdock/opencode" {
		t.Errorf("StorageDir = %q, want /var/lib/agentdock/opencode", cfg.Opencode.StorageDir)
	}
}

func TestValidate_OpencodeMode_InvalidRejected(t *testing.T) {
	cfg := baseValidCfg()
	cfg.Opencode.Mode = "garbage"

	err := Validate(cfg)
	if err == nil {
		t.Fatalf("expected validation error for mode=garbage, got nil")
	}
	if !strings.Contains(err.Error(), "opencode.mode") {
		t.Errorf("error did not mention opencode.mode: %v", err)
	}
}

func TestValidate_OpencodeMode_SpawnAndServerAccepted(t *testing.T) {
	for _, mode := range []string{OpencodeModeSpawn, OpencodeModeServer} {
		t.Run(mode, func(t *testing.T) {
			cfg := baseValidCfg()
			cfg.Opencode.Mode = mode
			if err := Validate(cfg); err != nil {
				t.Errorf("mode=%q should be valid; got %v", mode, err)
			}
		})
	}
}

func TestValidate_OpencodeIdleTimeout_NonPositiveRejected(t *testing.T) {
	cfg := baseValidCfg()
	cfg.Opencode.IdleTimeout = 0

	err := Validate(cfg)
	if err == nil {
		t.Fatalf("expected validation error for idle_timeout=0, got nil")
	}
	if !strings.Contains(err.Error(), "opencode.idle_timeout") {
		t.Errorf("error did not mention opencode.idle_timeout: %v", err)
	}
}
