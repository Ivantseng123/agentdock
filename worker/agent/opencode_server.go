package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
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
// Stage 3 Task 3.2-9 retry-once: when an attempt fails with a
// recoverable transport-layer error (supervisor crashed, connection
// refused, EOF mid-stream) the function transparently retries against a
// fresh session and (after Acquire) a fresh server subprocess. A second
// failure within retry surfaces as a hard error — no third attempt, no
// fallback to spawn mode (spec C4).
//
// Stage 3 Task 3.2-11 Bug A: when the POST response carries `finish=
// other` AND `tokens.output=0` AND the SSE consumer saw no text part,
// the run hits the silent-drop signature and we mark the job failed
// with the user-facing copy `LLM 回應為空，請稍後再試或改用 @bot issue`.
// The three-condition AND-gate matches POC P7 (false-positive 0/100 on
// the P2 success corpus). Bug A does not trigger retry — the silent
// drop is an application-level outcome, not transport.
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
		// with nil err. Stage 2 only emits exitCode ∈ {-1, 0}; Stage 3
		// Task 3.2-11's Bug A detector will emit positive exit codes,
		// and Jaeger filters by `error=true` need this branch to keep
		// working.
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
	// onStartedFired tracks whether OnStarted has been delivered to
	// the pool's status registry. Cross-review pr finding: the older
	// `attempt == 0` gate skipped OnStarted when attempt 0 failed
	// recoverably and attempt 1 succeeded, leaving status reporting /
	// the kill registry without a PID for the job. Track the flip
	// across attempts so the first SUCCESSFUL Acquire fires OnStarted,
	// not the first ATTEMPT.
	onStartedFired := &atomic.Bool{}
	const maxAttempts = 2
	for attempt := 0; attempt < maxAttempts; attempt++ {
		attemptOutput, attemptErr := r.runOneServerAttempt(ctx, logger, sup, agent, workDir, prompt, opts, attempt, onStartedFired)
		if attemptErr == nil {
			exitCode = 0
			return attemptOutput, nil
		}
		// Bug A: silent-drop signature. Mark span as application failure
		// (exitCode=1, distinct from transport-layer -1) and surface
		// immediately — retry can't fix a server that already returned
		// `finish=other` + 0 output tokens.
		if errors.Is(attemptErr, errOpencodeEmptyStream) {
			exitCode = 1
			return "", attemptErr
		}
		// Last attempt: surface error as-is (caller's runner.Run loops
		// the agent chain). Don't double-wrap.
		if attempt+1 >= maxAttempts {
			return "", attemptErr
		}
		// Decide whether to retry. Only crashes / transport failures
		// recover. 4xx, business-logic errors, and ctx cancellations
		// surface immediately — retry can't fix them.
		if !isRecoverableSupervisorCrash(attemptErr, sup) {
			return "", attemptErr
		}
		logger.Warn("opencode server-mode retry-once 觸發",
			"phase", "處理中",
			"command", agent.Command,
			"attempt", attempt+1,
			"error", attemptErr,
		)
	}
	// Loop above always returns explicitly; this is unreachable.
	return "", errors.New("opencode server-mode retry-once exhausted (logic bug)")
}

// runOneServerAttempt is one Acquire/CreateSession/SendPrompt/Wait
// cycle. Acquire is per-attempt so a crashed supervisor gets a fresh
// spawn on retry (Stage 3 Task 3.2-9 spec line "fresh session against
// the recovered server"). Returns the trimmed assistant text or a
// transport / request error that the outer retry loop classifies.
func (r *Runner) runOneServerAttempt(ctx context.Context, logger *slog.Logger, sup *Supervisor, agent config.AgentConfig, workDir, prompt string, opts RunOptions, attempt int, onStartedFired *atomic.Bool) (string, error) {
	if err := sup.Acquire(ctx); err != nil {
		return "", fmt.Errorf("acquire opencode supervisor: %w", err)
	}
	defer sup.Release()

	client := NewClient(sup.BaseURL(), sup.Password(), nil)

	// OnStarted fires once per runOneServer call, at the first
	// successful Acquire. CompareAndSwap so the second attempt's
	// Acquire-after-retry still fires it if attempt 0's Acquire
	// failed before this point (the pool's status registry needs
	// a PID; missing one means /kill can't find the job).
	if opts.OnStarted != nil && onStartedFired != nil && onStartedFired.CompareAndSwap(false, true) {
		opts.OnStarted(sup.ChildPID(), agent.Command)
	}
	logger.Info("opencode server-mode session 啟動",
		"phase", "處理中",
		"command", agent.Command,
		"supervisor_pid", sup.ChildPID(),
		"base_url", sup.BaseURL(),
		"attempt", attempt,
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

	output, err := run.Wait()
	<-drainDone

	if err != nil {
		return "", err
	}

	// Bug A AND-gate (Stage 3 Task 3.2-11): finish=other AND tokens.output=0
	// AND no text part received via SSE. All three must hold — POC P7
	// false-positive rate 0/100 on the P2 success corpus. Two-of-three
	// is explicitly rejected by spec rationale.
	if run.Finish() == "other" && run.TokensOut() == 0 && !run.SawTextPart() {
		logger.Warn("Agent 執行失敗 (空 stream)",
			"phase", "失敗",
			"command", agent.Command,
			"session_id", sessionID,
			"finish", run.Finish(),
			"tokens_output", run.TokensOut(),
			"saw_text_part", run.SawTextPart(),
		)
		return "", errOpencodeEmptyStream
	}

	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		// Defensive: above gate is strict, but a trimmed-empty answer
		// that didn't trip Bug A still warrants a warn so the operator
		// can investigate. Job is not failed in this branch — the
		// answer just happens to be whitespace.
		logger.Warn("opencode 答案為空 (未命中 Bug A 三條件)",
			"phase", "處理中",
			"command", agent.Command,
			"session_id", sessionID,
			"finish", run.Finish(),
			"tokens_output", run.TokensOut(),
			"saw_text_part", run.SawTextPart(),
		)
	}
	return trimmed, nil
}

// errOpencodeEmptyStream is the sentinel surfaced by Bug A detection.
// Its message doubles as the Slack-visible failure copy spec mandates:
// the worker error string flows verbatim into `r.Error` and is
// rendered by app/workflow/ask.go's failed-path as
// `:x: 思考失敗：<r.Error>`. The wrapping prefix from runner.go
// ("all agents failed: opencode:") makes this less clean than a
// direct app-side render special-case, but the spec § Boundaries
// Ask first guard explicitly says do not widen scope to app/ without
// approval. Stage 3 leaves the Slack copy slightly noisy; a follow-
// up PR can clean it up by special-casing this sentinel in app/.
var errOpencodeEmptyStream = errors.New("LLM 回應為空，請稍後再試或改用 @bot issue")

// isRecoverableSupervisorCrash returns true when the error chain looks
// like a transport-layer failure that retry-once can fix: either the
// supervisor's state has already flipped to `crashed` (cmd.Wait
// observed unexpected exit), or the error contains classic transport
// signatures (connection refused / EOF / ErrUnexpectedEOF). Errors
// that don't recover — 4xx from a healthy server, ctx cancellation,
// Bug A (Stage 3 Task 3.2-11) — return false and surface immediately
// without burning the retry budget.
//
// The state check covers the clean case where cmd.Wait already
// observed the dead child by the time the HTTP request returned; the
// error-signature fallback covers the race window where the child is
// dead but the wait goroutine hasn't flipped state yet (TCP RST
// arrives ahead of waitpid).
func isRecoverableSupervisorCrash(err error, sup *Supervisor) bool {
	if err == nil {
		return false
	}
	if sup != nil && sup.State() == stateCrashed {
		return true
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	msg := err.Error()
	if strings.Contains(msg, "connection refused") {
		return true
	}
	if strings.Contains(msg, ": EOF") || strings.Contains(msg, "unexpected EOF") {
		// Anchored matches: Go's HTTP client wraps post-EOF as
		// `Post "url": EOF`, scanner errors as `... unexpected EOF`.
		// The leading `: ` and `unexpected ` anchors avoid false
		// positives like a future "session reached EOF state" message
		// (cross-review pr finding).
		return true
	}
	if strings.Contains(msg, "broken pipe") {
		return true
	}
	return false
}
