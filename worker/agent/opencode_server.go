package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/Ivantseng123/agentdock/shared/tracing"
	"github.com/Ivantseng123/agentdock/worker/config"
)

// runOneServer is the server-mode counterpart to runOneSpawn. It drives
// one ask job through the worker-process-scoped Supervisor (long-running
// `opencode serve` child) plus a per-job HTTP/SSE Client. See plan Task
// 3.2-7 for the acceptance contract.
//
// Contract parity with runOneSpawn (Stage 1 cross-review pre-mortems
// 6.1 + 6.3):
//
//   - OnStarted fires with supervisor.ChildPID() and agent.Command so
//     the pool's status registry has a valid PID to track. Server mode
//     has no per-job exec'd process — the supervisor's child is the
//     only OS process running the work, so reusing its PID across
//     every job is the honest signal.
//   - OnEvent fires for every queue.StreamEvent the BusEvent decoder
//     produces (message_delta / tool_use / result), same taxonomy as
//     ReadStreamJSONOpencode populates in spawn mode.
//   - OnExit fires in a deferred closure with a synthetic exit code:
//     0 on a clean answer, -1 when transport / request errors surface
//     before Wait returns successfully. Bug A discrimination
//     (finish=other + tokens.output=0) is plan Task 3.2-11 (Stage 3);
//     promptResponse already carries those fields so the discriminator
//     can lift in without altering this function's signature.
//   - OTel span attributes mirror runOneSpawn's set (agent_type,
//     duration_ms, stdout_len, exit_code) so Jaeger filters by
//     `error=true` keep working across modes.
//
// Supervisor is owned by the worker's Runner; calling runOneServer
// with r.supervisor == nil is a programming error (the dispatcher only
// routes here when Mode == server, which is mutually exclusive with a
// nil supervisor in production boot). A defensive guard surfaces an
// explicit error rather than panicking on dereference.
func (r *Runner) runOneServer(ctx context.Context, logger *slog.Logger, agent config.AgentConfig, workDir, prompt string, opts RunOptions) (output string, err error) {
	if r.supervisor == nil {
		return "", errors.New("opencode supervisor not initialized (runOneServer reached with nil supervisor — Runner construction bug)")
	}

	ctx, span := tracer.Start(ctx, tracing.SpanAgentExecute,
		trace.WithAttributes(
			attribute.String("agent_type", filepath.Base(agent.Command)),
		),
	)
	start := time.Now()
	exitCode := -1
	defer func() {
		attrs := []attribute.KeyValue{
			attribute.Int64("duration_ms", time.Since(start).Milliseconds()),
			attribute.Int("stdout_len", len(output)),
		}
		if exitCode >= 0 {
			attrs = append(attrs, attribute.Int("exit_code", exitCode))
		}
		span.SetAttributes(attrs...)
		// Span status parity with runOneSpawn (cross-review M4): mark
		// span as Error not only on Go err but also when exitCode > 0
		// with nil err. Stage 2 only emits exitCode ∈ {-1, 0}, so the
		// second branch is currently unreachable; Stage 3 Task 3.2-11's
		// Bug A detector will emit positive exit codes, and Jaeger
		// filters by `error=true` need this branch to keep working.
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "agent run failed")
		} else if exitCode > 0 {
			span.SetStatus(codes.Error, fmt.Sprintf("agent exited %d", exitCode))
		}
		if opts.OnExit != nil {
			opts.OnExit(exitCode)
		}
		span.End()
	}()

	timeout := agent.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	sup := r.supervisor
	if err := sup.Acquire(ctx); err != nil {
		return "", fmt.Errorf("acquire opencode supervisor: %w", err)
	}
	defer sup.Release()

	client := NewClient(sup.BaseURL(), sup.Password(), nil)

	if opts.OnStarted != nil {
		opts.OnStarted(sup.ChildPID(), agent.Command)
	}
	logger.Info("opencode server-mode session 啟動",
		"phase", "處理中",
		"command", agent.Command,
		"supervisor_pid", sup.ChildPID(),
		"base_url", sup.BaseURL(),
	)

	sessionID, err := client.CreateSession(ctx, workDir)
	if err != nil {
		return "", fmt.Errorf("create opencode session: %w", err)
	}
	sup.SetActiveSession(sessionID)
	defer sup.ClearActiveSession(sessionID)
	logger.Info("opencode session 已建立",
		"phase", "處理中",
		"command", agent.Command,
		"session_id", sessionID,
	)

	run, err := client.SendPrompt(ctx, sessionID, workDir, prompt)
	if err != nil {
		return "", fmt.Errorf("send opencode prompt: %w", err)
	}

	drainDone := make(chan struct{})
	go func() {
		defer close(drainDone)
		for {
			select {
			case ev, ok := <-run.Events:
				if !ok {
					return
				}
				if opts.OnEvent != nil {
					opts.OnEvent(ev)
				}
			case sseErr, ok := <-run.SSEErrors:
				if !ok {
					continue
				}
				// SSE is telemetry only — POST is authoritative. Warn
				// the operator so an SSE disruption (proxy disconnect,
				// floor+1 upstream regression, etc.) is correlatable
				// with downstream behavior, but never abort the job
				// (cross-review M1 fix).
				logger.Warn("opencode SSE 中斷",
					"phase", "處理中",
					"command", agent.Command,
					"session_id", sessionID,
					"error", sseErr,
				)
			}
		}
	}()

	output, err = run.Wait()
	<-drainDone

	if err != nil {
		return "", err
	}
	exitCode = 0
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		logger.Warn("opencode 答案為空",
			"phase", "失敗",
			"command", agent.Command,
			"session_id", sessionID,
		)
	}
	return trimmed, nil
}
