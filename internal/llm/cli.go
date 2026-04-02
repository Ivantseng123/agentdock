package llm

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// CLIProvider calls a local CLI tool (e.g. claude, opencode) that is already
// authenticated via the user's own subscription. No API key needed.
type CLIProvider struct {
	name    string
	command string
	args    []string
	timeout time.Duration
}

func NewCLIProvider(name, command string, args []string, timeout time.Duration) *CLIProvider {
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	return &CLIProvider{
		name:    name,
		command: command,
		args:    args,
		timeout: timeout,
	}
}

func (c *CLIProvider) Name() string { return c.name }

func (c *CLIProvider) Diagnose(ctx context.Context, req DiagnoseRequest) (DiagnoseResponse, error) {
	if _, err := exec.LookPath(c.command); err != nil {
		return DiagnoseResponse{}, fmt.Errorf("%s not found in PATH: %w", c.command, err)
	}

	systemMsg := SystemPrompt(req.Type, req.Prompt)
	userMsg := BuildPrompt(req.Type, req.Message, req.RepoFiles)
	fullPrompt := systemMsg + "\n\n" + userMsg

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	args := c.buildArgs(fullPrompt)
	cmd := exec.CommandContext(ctx, c.command, args...)

	// Always pipe prompt via stdin (args may contain flags but not the full prompt)
	cmd.Stdin = strings.NewReader(fullPrompt)

	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			stderr := strings.TrimSpace(string(exitErr.Stderr))
			return DiagnoseResponse{}, fmt.Errorf("%s failed (exit %d): %s", c.command, exitErr.ExitCode(), stderr)
		}
		return DiagnoseResponse{}, fmt.Errorf("%s failed: %w", c.command, err)
	}

	text := strings.TrimSpace(string(output))
	if text == "" {
		return DiagnoseResponse{}, fmt.Errorf("empty response from %s", c.command)
	}

	return ParseLLMTextResponse(text)
}

// buildArgs constructs CLI arguments.
// {prompt} placeholders are ONLY replaced if the prompt is short enough for a command arg (< 32KB).
// For large prompts, {prompt} is removed and the prompt goes via stdin instead.
func (c *CLIProvider) buildArgs(prompt string) []string {
	if len(c.args) == 0 {
		return nil
	}

	const maxArgLen = 32 * 1024 // 32KB safe limit for command args

	var args []string
	for _, a := range c.args {
		if strings.Contains(a, "{prompt}") {
			if len(prompt) < maxArgLen {
				args = append(args, strings.ReplaceAll(a, "{prompt}", prompt))
			}
			// else: skip this arg entirely, prompt goes via stdin
		} else {
			args = append(args, a)
		}
	}
	return args
}
