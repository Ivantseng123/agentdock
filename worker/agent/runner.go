package agent

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Ivantseng123/agentdock/shared/queue"
	"github.com/Ivantseng123/agentdock/worker/config"
)

// RunOptions provides per-call callbacks for agent execution.
type RunOptions struct {
	OnStarted func(pid int, command string)
	OnEvent   func(event queue.StreamEvent)
	Secrets   map[string]string
}

type Runner struct {
	agents      []config.AgentConfig
	githubToken string
}

func NewRunner(agents []config.AgentConfig) *Runner {
	return &Runner{agents: agents}
}

func NewRunnerFromConfig(cfg *config.Config) *Runner {
	var chain []config.AgentConfig
	for _, name := range cfg.Providers {
		if agent, ok := cfg.Agents[name]; ok {
			chain = append(chain, agent)
		} else {
			slog.Warn("Provider 未找到", "phase", "失敗", "name", name)
		}
	}
	runner := NewRunner(chain)
	runner.githubToken = cfg.GitHub.Token
	return runner
}

func (r *Runner) Run(ctx context.Context, logger *slog.Logger, workDir, prompt string, opts RunOptions) (string, error) {
	var errs []string
	for i, agent := range r.agents {
		logger.Info("嘗試 agent", "phase", "處理中", "command", agent.Command, "index", i, "total", len(r.agents), "timeout", agent.Timeout)
		output, err := r.runOne(ctx, logger, agent, workDir, prompt, opts)
		if err != nil {
			if ctx.Err() == context.Canceled {
				logger.Info("Agent 執行已中斷", "phase", "完成", "command", agent.Command, "index", i)
				return "", fmt.Errorf("cancelled")
			}
			logger.Warn("Agent 失敗", "phase", "失敗", "command", agent.Command, "index", i, "error", err)
			errs = append(errs, fmt.Sprintf("%s: %s", agent.Command, err))
			continue
		}
		logger.Info("Agent 執行成功", "phase", "完成", "command", agent.Command, "output_len", len(output))
		return output, nil
	}
	logger.Error("所有 agent 已耗盡", "phase", "失敗", "errors", strings.Join(errs, "; "))
	return "", fmt.Errorf("all agents failed: %s", strings.Join(errs, "; "))
}

func (r *Runner) runOne(ctx context.Context, logger *slog.Logger, agent config.AgentConfig, workDir, prompt string, opts RunOptions) (string, error) {
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

	if err := cmd.Start(); err != nil {
		return "", err
	}

	// Notify listener of PID.
	if opts.OnStarted != nil {
		opts.OnStarted(cmd.Process.Pid, agent.Command)
	}
	logger.Info("Agent process 已啟動", "phase", "處理中", "command", agent.Command, "pid", cmd.Process.Pid)

	// Read stdout in a goroutine; wait for it before cmd.Wait().
	var output string
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		output = readOutput(ctx, stdoutPipe, agent.Stream, opts.OnEvent)
	}()
	wg.Wait()

	err = cmd.Wait()
	if err != nil {
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

// readOutput routes stdout through the appropriate reader based on stream config.
func readOutput(ctx context.Context, r io.Reader, stream bool, onEvent func(queue.StreamEvent)) string {
	if !stream {
		return queue.ReadRawOutput(r)
	}

	eventCh := make(chan queue.StreamEvent, 64)
	var result string
	var wg sync.WaitGroup

	// Forward events to callback in a context-aware goroutine.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case evt, ok := <-eventCh:
				if !ok {
					return
				}
				if onEvent != nil {
					onEvent(evt)
				}
			case <-ctx.Done():
				// Drain remaining events.
				for range eventCh {
				}
				return
			}
		}
	}()

	result = queue.ReadStreamJSON(r, eventCh)
	close(eventCh)
	wg.Wait()
	return result
}

// expandExtraArgs replaces every standalone "{extra_args}" element with zero
// or more entries from extraArgs. nil/empty extraArgs drops the slot entirely
// (no empty-string element leaks into the resulting argv). Substring matches
// inside a larger string are NOT expanded — the token must stand alone as its
// own arg. This is the single splice site for extra_args across both the
// arg-prompt and stdin-prompt paths.
func expandExtraArgs(args []string, extraArgs []string) []string {
	result := make([]string, 0, len(args)+len(extraArgs))
	for _, a := range args {
		if a == config.ExtraArgsToken {
			result = append(result, extraArgs...)
			continue
		}
		result = append(result, a)
	}
	return result
}

// substituteStringPlaceholders applies strings.ReplaceAll for each (key, val)
// pair to every element of args. Used only for string-valued placeholders
// ({prompt}, {output_file}); list-valued {extra_args} must be expanded via
// expandExtraArgs beforehand.
func substituteStringPlaceholders(args []string, values map[string]string) []string {
	result := make([]string, 0, len(args))
	for _, a := range args {
		for k, v := range values {
			a = strings.ReplaceAll(a, k, v)
		}
		result = append(result, a)
	}
	return result
}

// blockedArgs is the set of CLI flags that bypass the agent's host sandbox.
// Memory feedback_worker_deployment_unknown rationale: workers may run on a
// user's real machine, not an isolated pod, so allowing these would let the
// agent touch $HOME, /etc, SSH keys, etc.
var blockedArgs = []string{
	"--dangerously-skip-permissions",
}

// claudeCodeEnvWhitelist is the set of CLAUDE_CODE_* env vars that pass
// through to agent processes. Anything else with the CLAUDE_CODE_ prefix
// inherited from the worker host is stripped to keep agent behavior
// deterministic across deployment environments. Add entries when a new var
// becomes load-bearing.
var claudeCodeEnvWhitelist = map[string]bool{
	"CLAUDE_CODE_NO_FLICKER": true, // see project_cmux_claude_flicker_workaround
}

// stderrTailLen caps how much of an agent's stderr survives into error
// messages and warn logs. Large stderr blobs (e.g. claude SDK's every-event
// JSON dumps) otherwise spam the logger and crowd out other signal.
const stderrTailLen = 2000

// tailStderr returns the trailing stderrTailLen bytes of s, prefixed with a
// "…" marker when truncation occurred. Single truncation site so tail size
// stays consistent across error and log surfaces.
func tailStderr(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= stderrTailLen {
		return s
	}
	return "…" + s[len(s)-stderrTailLen:]
}

// detectBlockedArgs returns any blockedArgs entries present in args. The flag
// must stand alone or appear as `--flag=value`; substring matches inside
// other args are NOT detected. Caller surfaces the result; this function has
// no side effects.
func detectBlockedArgs(args []string) []string {
	var found []string
	for _, a := range args {
		for _, blocked := range blockedArgs {
			if a == blocked || strings.HasPrefix(a, blocked+"=") {
				found = append(found, a)
			}
		}
	}
	return found
}

// filterClaudeCodeEnv strips CLAUDE_CODE_* vars from env unless the key is
// in claudeCodeEnvWhitelist. Non-CLAUDE_CODE_* entries pass through unchanged.
// Output preserves input ordering for deterministic env substitution under
// exec.Command.
func filterClaudeCodeEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		if !strings.HasPrefix(e, "CLAUDE_CODE_") {
			out = append(out, e)
			continue
		}
		i := strings.IndexByte(e, '=')
		if i < 0 {
			continue
		}
		if claudeCodeEnvWhitelist[e[:i]] {
			out = append(out, e)
		}
	}
	return out
}
