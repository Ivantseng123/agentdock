package bot

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

	"agentdock/internal/config"
	"agentdock/internal/queue"
)

// RunOptions provides per-call callbacks for agent execution.
type RunOptions struct {
	OnStarted func(pid int, command string)
	OnEvent   func(event queue.StreamEvent)
	Secrets   map[string]string
}

type AgentRunner struct {
	agents      []config.AgentConfig
	githubToken string
}

func NewAgentRunner(agents []config.AgentConfig) *AgentRunner {
	return &AgentRunner{agents: agents}
}

func NewAgentRunnerFromConfig(cfg *config.Config) *AgentRunner {
	var chain []config.AgentConfig
	if len(cfg.Providers) > 0 {
		for _, name := range cfg.Providers {
			if agent, ok := cfg.Agents[name]; ok {
				chain = append(chain, agent)
			} else {
				slog.Warn("Provider 未找到", "phase", "失敗", "name", name)
			}
		}
	} else if cfg.ActiveAgent != "" {
		if agent, ok := cfg.Agents[cfg.ActiveAgent]; ok {
			chain = append(chain, agent)
		}
	}
	runner := NewAgentRunner(chain)
	runner.githubToken = cfg.GitHub.Token
	return runner
}

func (r *AgentRunner) Run(ctx context.Context, logger *slog.Logger, workDir, prompt string, opts RunOptions) (string, error) {
	var errs []string
	for i, agent := range r.agents {
		logger.Info("嘗試 agent", "phase", "處理中", "command", agent.Command, "index", i, "total", len(r.agents), "timeout", agent.Timeout)
		output, err := r.runOne(ctx, logger, agent, workDir, prompt, opts)
		if err != nil {
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

func (r *AgentRunner) runOne(ctx context.Context, logger *slog.Logger, agent config.AgentConfig, workDir, prompt string, opts RunOptions) (string, error) {
	timeout := agent.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	const maxArgLen = 32 * 1024 // 32KB safe limit for command args

	// Check if prompt fits in args or needs stdin fallback.
	hasPlaceholder := false
	for _, a := range agent.Args {
		if strings.Contains(a, "{prompt}") {
			hasPlaceholder = true
			break
		}
	}

	useStdin := !hasPlaceholder || len(prompt) >= maxArgLen
	var args []string
	if useStdin && hasPlaceholder {
		// Prompt too large for args — drop the placeholder arg, use stdin instead.
		for _, a := range agent.Args {
			if !strings.Contains(a, "{prompt}") {
				args = append(args, a)
			}
		}
		logger.Info("Prompt 過大，改用 stdin", "phase", "處理中", "prompt_len", len(prompt))
	} else {
		args = substitutePrompt(agent.Args, prompt)
	}

	cmd := exec.CommandContext(ctx, agent.Command, args...)
	cmd.Dir = workDir

	// Graceful termination: SIGTERM first, then force-kill after 10s.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 10 * time.Second

	// Inject secrets as environment variables.
	env := os.Environ()
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
			return "", fmt.Errorf("exit %d: %s", exitErr.ExitCode(), strings.TrimSpace(stderr.String()))
		}
		return "", err
	}
	return strings.TrimSpace(output), nil
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

func substitutePrompt(args []string, prompt string) []string {
	result := make([]string, 0, len(args))
	for _, a := range args {
		result = append(result, strings.ReplaceAll(a, "{prompt}", prompt))
	}
	return result
}
