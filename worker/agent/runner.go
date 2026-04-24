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
	var args []string
	if useStdin && hasPromptPlaceholder {
		// Prompt too large for args — drop the prompt arg, use stdin instead.
		for _, a := range agent.Args {
			if strings.Contains(a, "{prompt}") {
				continue
			}
			args = append(args, strings.ReplaceAll(a, "{output_file}", outputFile))
		}
		logger.Info("Prompt 過大，改用 stdin", "phase", "處理中", "prompt_len", len(prompt))
	} else {
		args = substitutePlaceholders(agent.Args, map[string]string{
			"{prompt}":      prompt,
			"{output_file}": outputFile,
		})
	}

	cmd := exec.CommandContext(ctx, agent.Command, args...)
	cmd.Dir = workDir

	// Graceful termination: SIGTERM first, then force-kill after 10s.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 10 * time.Second

	// Inject secrets as environment variables.
	// Nil RequiredSecrets (absent from yaml) falls back to ["GH_TOKEN"] for
	// back-compat; an explicit empty list means zero secrets forwarded.
	secrets := filterSecrets(opts.Secrets, agent.RequiredSecrets)
	env := os.Environ()
	if len(secrets) > 0 {
		for k, v := range secrets {
			env = append(env, fmt.Sprintf("%s=%s", k, v))
		}
	} else if r.githubToken != "" && agent.RequiredSecrets == nil {
		// Legacy fallback: no secrets map at all and RequiredSecrets not
		// declared — inject GH_TOKEN from the runner's static field.
		// Coupling: the `RequiredSecrets == nil` check here must stay in sync
		// with filterSecrets' own nil-semantic (nil → default ["GH_TOKEN"],
		// empty slice → zero-trust). If that contract changes, update both.
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
			return "", fmt.Errorf("exit %d: %s", exitErr.ExitCode(), strings.TrimSpace(stderr.String()))
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
		stderrTail := strings.TrimSpace(stderr.String())
		if len(stderrTail) > 2000 {
			stderrTail = "…" + stderrTail[len(stderrTail)-2000:]
		}
		logger.Warn("Agent exit 0 但 stdout 空", "phase", "失敗", "command", agent.Command, "stderr_tail", stderrTail)
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

func substitutePlaceholders(args []string, values map[string]string) []string {
	result := make([]string, 0, len(args))
	for _, a := range args {
		for k, v := range values {
			a = strings.ReplaceAll(a, k, v)
		}
		result = append(result, a)
	}
	return result
}

// filterSecrets returns the subset of all that matches the keys in whitelist.
//
// Semantics:
//   - whitelist == nil  → default to ["GH_TOKEN"] for back-compat (custom
//     agents in worker.yaml that predate this field keep their prior behavior)
//   - whitelist == []   → return empty map (zero-trust provider)
//   - otherwise         → return only keys in whitelist that exist in all
func filterSecrets(all map[string]string, whitelist []string) map[string]string {
	if whitelist == nil {
		// Back-compat default: treat as if the user wrote required_secrets: [GH_TOKEN].
		whitelist = []string{"GH_TOKEN"}
	}
	// Explicit empty list → zero secrets.
	if len(whitelist) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(whitelist))
	for _, k := range whitelist {
		if v, ok := all[k]; ok {
			out[k] = v
		}
	}
	return out
}
