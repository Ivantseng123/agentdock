package agent

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/Ivantseng123/agentdock/worker/config"
)

// TestDispatchTarget_Matrix covers the 4 agents × 3 mode states = 12
// dispatch decisions: only opencode×server routes to the server-mode
// runner. Every other combination falls through to spawn. The empty
// mode (zero value, e.g. Runner constructed via NewRunner without a
// follow-up NewRunnerFromConfig) also routes to spawn, so a partially
// constructed Runner doesn't silently end up in server mode.
func TestDispatchTarget_Matrix(t *testing.T) {
	cases := []struct {
		name    string
		command string
		mode    string
		want    string
	}{
		{"claude/spawn", "claude", config.OpencodeModeSpawn, "spawn"},
		{"claude/server", "claude", config.OpencodeModeServer, "spawn"},
		{"claude/zero", "claude", "", "spawn"},
		{"codex/spawn", "codex", config.OpencodeModeSpawn, "spawn"},
		{"codex/server", "codex", config.OpencodeModeServer, "spawn"},
		{"codex/zero", "codex", "", "spawn"},
		{"opencode/spawn", "opencode", config.OpencodeModeSpawn, "spawn"},
		{"opencode/server", "opencode", config.OpencodeModeServer, "server"},
		{"opencode/zero", "opencode", "", "spawn"},
		{"gemini/spawn", "gemini", config.OpencodeModeSpawn, "spawn"},
		{"gemini/server", "gemini", config.OpencodeModeServer, "spawn"},
		{"gemini/zero", "gemini", "", "spawn"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &Runner{
				opencodeCfg: config.OpencodeConfig{Mode: tc.mode},
			}
			got := r.dispatchTarget(config.AgentConfig{Command: tc.command})
			if got != tc.want {
				t.Errorf("dispatchTarget(%q, mode=%q) = %q, want %q", tc.command, tc.mode, got, tc.want)
			}
		})
	}
}

// TestRunOne_ServerStub_ReturnsExplicitError walks the dispatcher and the
// stub end-to-end: when opencode+server is configured, runOne must call
// runOneServer, which (in Stage 1) returns an error mentioning the
// not-implemented state. This proves the dispatcher actually reaches the
// stub rather than silently falling through to spawn.
func TestRunOne_ServerStub_ReturnsExplicitError(t *testing.T) {
	r := &Runner{
		opencodeCfg: config.OpencodeConfig{Mode: config.OpencodeModeServer},
	}
	_, err := r.runOne(
		context.Background(),
		slog.Default(),
		config.AgentConfig{Command: "opencode"},
		"/tmp",
		"unused prompt",
		RunOptions{},
	)
	if err == nil {
		t.Fatal("expected stub error, got nil")
	}
	if !strings.Contains(err.Error(), "server mode not implemented") {
		t.Errorf("error %q does not mention 'server mode not implemented'", err.Error())
	}
}

// TestNewRunnerFromConfig_PopulatesOpencodeCfg verifies the dispatcher
// field is wired through the Config constructor — without this, a worker
// loaded via NewRunnerFromConfig with `opencode.mode: server` in yaml
// would still route to spawn, hiding the configuration error.
func TestNewRunnerFromConfig_PopulatesOpencodeCfg(t *testing.T) {
	cfg := &config.Config{
		Providers: []string{"opencode"},
		Agents: map[string]config.AgentConfig{
			"opencode": {Command: "opencode"},
		},
		Opencode: config.OpencodeConfig{Mode: config.OpencodeModeServer},
	}
	runner := NewRunnerFromConfig(cfg)
	if runner.opencodeCfg.Mode != config.OpencodeModeServer {
		t.Errorf("opencodeCfg.Mode = %q, want %q", runner.opencodeCfg.Mode, config.OpencodeModeServer)
	}
}
