package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/Ivantseng123/agentdock/shared/queue"
	"github.com/Ivantseng123/agentdock/shared/tracing"
	"github.com/Ivantseng123/agentdock/worker/config"
)

// runOneSpawn executes a single agent in the legacy per-job spawn mode:
// each call exec's a fresh CLI process (e.g. `opencode run`, `claude --print`),
// pipes prompt via args or stdin, reads stream output, and waits for exit.
// This is the historical default path and remains in use for:
//
//   - All non-opencode agents (claude / codex / gemini) regardless of
//     `cfg.Opencode.Mode`.
//   - The opencode agent when `cfg.Opencode.Mode == "spawn"` (the worker
//     default).
//
// The dispatcher in runner.go decides between this and runOneServer.
// Helpers used here (readOutput, expandExtraArgs, detectBlockedArgs,
// filterClaudeCodeEnv, tailStderr, blockedArgs, claudeCodeEnvWhitelist,
// stderrTailLen) live in runner.go so they remain available to the
// dispatcher path and any future runOne* variant.
func (r *Runner) runOneSpawn(ctx context.Context, logger *slog.Logger, agent config.AgentConfig, workDir, prompt string, opts RunOptions) (output string, err error) {
	ctx, span := tracer.Start(ctx, tracing.SpanAgentExecute,
		trace.WithAttributes(
			attribute.String("agent_type", filepath.Base(agent.Command)),
		),
	)
	start := time.Now()
	var stderrLen int
	exitCode := -1 // -1 = not run / not waited
	// Closure captures `output`, `err` (named returns), `stderrLen`, and
	// `exitCode` so span attrs AND status reflect whatever values the
	// function ends up returning, regardless of which exit branch fires.
	// Single status-setting site here keeps the cmd.Start / cmd.Wait /
	// blocked-args / output-file paths consistent — Jaeger filters by
	// `error=true` would otherwise miss exit-non-zero (the most common
	// failure mode), since `span.End` alone leaves status Unset.
	defer func() {
		attrs := []attribute.KeyValue{
			attribute.Int64("duration_ms", time.Since(start).Milliseconds()),
			attribute.Int("stdout_len", len(output)),
			attribute.Int("stderr_len", stderrLen),
		}
		if exitCode >= 0 {
			attrs = append(attrs, attribute.Int("exit_code", exitCode))
		}
		span.SetAttributes(attrs...)
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

	const maxArgLen = 32 * 1024 // 32KB safe limit for command args

	hasPromptPlaceholder := false
	hasOutputFilePlaceholder := false
	for _, a := range agent.Args {
		if strings.Contains(a, "{prompt}") {
			hasPromptPlaceholder = true
		}
		if strings.Contains(a, "{output_file}") {
			hasOutputFilePlaceholder = true
		}
	}

	// Some CLIs (e.g. `codex exec -o <file>`) write their final message to a
	// path instead of stdout. Allocate a temp file and let the caller read from
	// it after the process exits; the {output_file} placeholder opts into this.
	var outputFile string
	if hasOutputFilePlaceholder {
		f, err := os.CreateTemp("", "agentdock-output-*.txt")
		if err != nil {
			return "", fmt.Errorf("create output file: %w", err)
		}
		outputFile = f.Name()
		_ = f.Close()
		defer os.Remove(outputFile)
	}

	useStdin := !hasPromptPlaceholder || len(prompt) >= maxArgLen

	// Single splice site for {extra_args}. Both branches below operate on the
	// already-expanded slice, so future placeholders / bug fixes only need to
	// touch one place.
	expanded := expandExtraArgs(agent.Args, agent.ExtraArgs)

	if blocked := detectBlockedArgs(expanded); len(blocked) > 0 {
		return "", fmt.Errorf("blocked args rejected: %s", strings.Join(blocked, ", "))
	}

	var args []string
	if useStdin && hasPromptPlaceholder {
		// Prompt too large for args — drop the prompt arg, use stdin instead.
		// {output_file} still substitutes in place; {prompt}-bearing args are
		// skipped entirely. {extra_args} is already gone (expandExtraArgs ran
		// above), so this loop only handles the remaining string-valued slots.
		for _, a := range expanded {
			if strings.Contains(a, "{prompt}") {
				continue
			}
			args = append(args, strings.ReplaceAll(a, "{output_file}", outputFile))
		}
		logger.Info("Prompt 過大，改用 stdin", "phase", "處理中", "prompt_len", len(prompt))
	} else {
		args = substituteStringPlaceholders(expanded, map[string]string{
			"{prompt}":      prompt,
			"{output_file}": outputFile,
		})
	}

	cmd := exec.CommandContext(ctx, agent.Command, args...)
	cmd.Dir = workDir

	// Graceful termination: SIGTERM first, then force-kill after 10s.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 10 * time.Second

	// Inject secrets as environment variables. Filter inherited env to drop
	// residual CLAUDE_CODE_* vars from the worker host that could pollute
	// agent behavior across deployments.
	env := filterClaudeCodeEnv(os.Environ())
	if len(opts.Secrets) > 0 {
		for k, v := range opts.Secrets {
			env = append(env, fmt.Sprintf("%s=%s", k, v))
		}
	} else if r.githubToken != "" {
		env = append(env, fmt.Sprintf("GH_TOKEN=%s", r.githubToken))
	}
	cmd.Env = env

	if useStdin {
		cmd.Stdin = strings.NewReader(prompt)
	}

	// Use StdoutPipe for streaming reads.
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("stdout pipe: %w", err)
	}

	var stderr strings.Builder
	cmd.Stderr = &stderr

	if startErr := cmd.Start(); startErr != nil {
		// Process couldn't even launch (e.g. binary missing) — not a
		// business failure but a worker-environment problem. Defer above
		// records this on the span via the named-return `err` capture.
		return "", startErr
	}

	// Notify listener of PID.
	if opts.OnStarted != nil {
		opts.OnStarted(cmd.Process.Pid, agent.Command)
	}
	logger.Info("Agent process 已啟動", "phase", "處理中", "command", agent.Command, "pid", cmd.Process.Pid)

	// Inactivity timer fires SIGTERM when no stream event arrives within
	// agent.InactivityTimeout. Streaming agents only — non-stream CLIs emit
	// no events and would be killed prematurely. Disabled when timeout <= 0.
	var inactivityKilled atomic.Bool
	eventCallback := opts.OnEvent
	if agent.InactivityTimeout > 0 && agent.StreamFormat != "" {
		inactivityTimer := time.AfterFunc(agent.InactivityTimeout, func() {
			inactivityKilled.Store(true)
			logger.Warn("Inactivity timeout 觸發，發送 SIGTERM",
				"phase", "失敗",
				"command", agent.Command,
				"timeout", agent.InactivityTimeout)
			_ = cmd.Process.Signal(syscall.SIGTERM)
		})
		defer inactivityTimer.Stop()
		eventCallback = func(evt queue.StreamEvent) {
			// Don't re-arm the timer once ctx is cancelled/expired: readOutput
			// now forwards buffered events through this callback during its
			// post-cancel drain (#253), and resetting here would schedule a
			// pointless SIGTERM against the already-dying process — and could
			// mislabel the failure as "inactivity timeout". The deferred
			// Stop() finishes teardown.
			if ctx.Err() == nil {
				inactivityTimer.Reset(agent.InactivityTimeout)
			}
			if opts.OnEvent != nil {
				opts.OnEvent(evt)
			}
		}
	}

	// Read stdout in a goroutine; wait for it before cmd.Wait().
	// `output` is the named return value; goroutine writes through closure.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		output = readOutput(ctx, stdoutPipe, agent.StreamFormat, eventCallback)
	}()
	wg.Wait()

	err = cmd.Wait()
	stderrLen = stderr.Len()
	if exitErr, ok := err.(*exec.ExitError); ok {
		exitCode = exitErr.ExitCode()
	} else if err == nil {
		exitCode = 0
	}
	if err != nil {
		if inactivityKilled.Load() {
			return "", fmt.Errorf("inactivity timeout after %s (no stream events)", agent.InactivityTimeout)
		}
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("timeout after %s", timeout)
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("exit %d: %s", exitErr.ExitCode(), tailStderr(stderr.String()))
		}
		return "", err
	}
	if outputFile != "" {
		data, readErr := os.ReadFile(outputFile)
		if readErr != nil {
			return "", fmt.Errorf("read output file: %w", readErr)
		}
		return strings.TrimSpace(string(data)), nil
	}
	// Exit 0 + empty stdout is silent failure (e.g. opencode run auto-rejecting
	// a permission ask and cascade-collapsing the session). Surface stderr tail
	// so the next time this happens, log alone is enough to diagnose without
	// kubectl exec'ing into the worker.
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		logger.Warn("Agent exit 0 但 stdout 空", "phase", "失敗", "command", agent.Command, "stderr_tail", tailStderr(stderr.String()))
	}
	return trimmed, nil
}
