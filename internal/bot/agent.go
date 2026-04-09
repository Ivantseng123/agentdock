package bot

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"

	"slack-issue-bot/internal/config"
)

type AgentRunner struct {
	agents      []config.AgentConfig
	githubToken string
}

func NewAgentRunner(agents []config.AgentConfig) *AgentRunner {
	return &AgentRunner{agents: agents}
}

func NewAgentRunnerFromConfig(cfg *config.Config) *AgentRunner {
	var chain []config.AgentConfig
	if len(cfg.Fallback) > 0 {
		for _, name := range cfg.Fallback {
			if agent, ok := cfg.Agents[name]; ok {
				chain = append(chain, agent)
			} else {
				slog.Warn("fallback agent not found in agents config", "name", name)
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

func (r *AgentRunner) Run(ctx context.Context, workDir, prompt string) (string, error) {
	var errs []string
	for i, agent := range r.agents {
		slog.Info("trying agent", "command", agent.Command, "index", i, "total", len(r.agents), "timeout", agent.Timeout)
		output, err := r.runOne(ctx, agent, workDir, prompt)
		if err != nil {
			slog.Warn("agent failed", "command", agent.Command, "index", i, "error", err)
			errs = append(errs, fmt.Sprintf("%s: %s", agent.Command, err))
			continue
		}
		slog.Info("agent succeeded", "command", agent.Command, "output_len", len(output))
		return output, nil
	}
	slog.Error("all agents exhausted", "errors", strings.Join(errs, "; "))
	return "", fmt.Errorf("all agents failed: %s", strings.Join(errs, "; "))
}

func (r *AgentRunner) runOne(ctx context.Context, agent config.AgentConfig, workDir, prompt string) (string, error) {
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
		slog.Info("prompt too large for args, using stdin", "prompt_len", len(prompt))
	} else {
		args = substitutePrompt(agent.Args, prompt)
	}

	cmd := exec.CommandContext(ctx, agent.Command, args...)
	cmd.Dir = workDir

	// Pass GH_TOKEN so agent can use `gh issue create`
	cmd.Env = append(os.Environ(), fmt.Sprintf("GH_TOKEN=%s", r.githubToken))

	if useStdin {
		cmd.Stdin = strings.NewReader(prompt)
	}

	output, err := cmd.Output()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("timeout after %s", timeout)
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("exit %d: %s", exitErr.ExitCode(), strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func substitutePrompt(args []string, prompt string) []string {
	result := make([]string, 0, len(args))
	for _, a := range args {
		result = append(result, strings.ReplaceAll(a, "{prompt}", prompt))
	}
	return result
}
