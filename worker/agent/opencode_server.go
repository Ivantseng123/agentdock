package agent

import (
	"context"
	"errors"
	"log/slog"

	"github.com/Ivantseng123/agentdock/worker/config"
)

// runOneServer is the entry point for opencode's server-mode run path
// (long-running `opencode serve` subprocess shared by the worker pool over
// HTTP/SSE). Stage 1 ships this as a stub so operators who flip
// `opencode.mode: server` before the Phase 3.2 supervisor lands receive a
// loud, explicit error from the existing agent-chain failure path rather
// than a silent degradation.
//
// Implementation lands in Stage 2:
//   - Task 3.2-5 wires the supervisor (Start/Stop/BaseURL/Password).
//   - Task 3.2-6 wires the HTTP/SSE client (CreateSession, SendPrompt).
//   - Task 3.2-7 fills this function with the supervisor + client glue.
//
// Until those land, `Run`'s agent-chain fallback at runner.go's Run()
// catches the error, logs the failure, and tries the next provider — so
// the worker stays alive and ask jobs degrade visibly rather than wedge.
func (r *Runner) runOneServer(ctx context.Context, logger *slog.Logger, agent config.AgentConfig, workDir, prompt string, opts RunOptions) (string, error) {
	logger.Error(
		"opencode server-mode 尚未實作 (Stage 2+)",
		"phase", "失敗",
		"command", agent.Command,
		"mode", r.opencodeCfg.Mode,
		"hint", "Stage 1 PR only ships the dispatcher; flip opencode.mode back to spawn until Stage 2 (Task 3.2-5..3.2-7) lands.",
	)
	return "", errors.New("opencode server mode not implemented yet (Stage 2+; flip opencode.mode to spawn or wait for Tasks 3.2-5..3.2-7)")
}
