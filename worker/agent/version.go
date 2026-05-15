package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"
)

// versionDetectTimeout caps how long worker startup waits for one agent's
// --version to respond. Slow CLIs delay startup; missing CLIs fail fast.
const versionDetectTimeout = 5 * time.Second

// LogVersions runs `<command> --version` for each configured agent and
// records the result. Failures (missing binary, non-zero exit, no --version
// support) emit warn-level logs but never block startup — agents older than
// the --version convention must not crash workers.
func (r *Runner) LogVersions(ctx context.Context, logger *slog.Logger) {
	for _, ag := range r.agents {
		version, err := detectVersion(ctx, ag.Command)
		if err != nil {
			logger.Warn("Agent --version 探測失敗", "phase", "處理中", "command", ag.Command, "error", err)
			continue
		}
		logger.Info("Agent 版本", "phase", "處理中", "command", ag.Command, "version", version)
	}
}

// detectVersion runs `command --version` with a per-agent timeout and
// returns the trimmed first line. The first line is the convention for CLIs
// that print a banner before the version (claude, codex, opencode all
// conform). Caller decides how to surface failures.
func detectVersion(ctx context.Context, command string) (string, error) {
	versionCtx, cancel := context.WithTimeout(ctx, versionDetectTimeout)
	defer cancel()

	out, err := exec.CommandContext(versionCtx, command, "--version").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("exec %s --version: %w", command, err)
	}
	line := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	if line == "" {
		return "", fmt.Errorf("%s --version returned empty output", command)
	}
	return line, nil
}
