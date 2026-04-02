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
		timeout = 60 * time.Second
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
	// Check if the CLI tool exists
	if _, err := exec.LookPath(c.command); err != nil {
		return DiagnoseResponse{}, fmt.Errorf("%s not found in PATH: %w", c.command, err)
	}

	systemMsg := SystemPrompt(req.Type)
	userMsg := BuildPrompt(req.Type, req.Message, req.RepoFiles)
	fullPrompt := systemMsg + "\n\n" + userMsg

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	args := c.buildArgs(fullPrompt)
	cmd := exec.CommandContext(ctx, c.command, args...)
	cmd.Stdin = strings.NewReader(fullPrompt)

	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return DiagnoseResponse{}, fmt.Errorf("%s failed (exit %d): %s", c.command, exitErr.ExitCode(), string(exitErr.Stderr))
		}
		return DiagnoseResponse{}, fmt.Errorf("%s failed: %w", c.command, err)
	}

	text := strings.TrimSpace(string(output))
	if text == "" {
		return DiagnoseResponse{}, fmt.Errorf("empty response from %s", c.command)
	}

	return ParseLLMTextResponse(text)
}

func (c *CLIProvider) buildArgs(prompt string) []string {
	// Known CLI tools and their flags for non-interactive single-prompt usage
	switch c.command {
	case "claude":
		// claude --print -p "prompt" sends prompt and prints response to stdout
		args := []string{"--print", "-p", prompt}
		return args
	default:
		// For unknown CLIs, use the configured args
		// If args contain "{prompt}", replace it; otherwise append
		var args []string
		replaced := false
		for _, a := range c.args {
			if strings.Contains(a, "{prompt}") {
				args = append(args, strings.ReplaceAll(a, "{prompt}", prompt))
				replaced = true
			} else {
				args = append(args, a)
			}
		}
		if !replaced {
			// Fallback: pipe via stdin (cmd.Stdin is already set)
			args = c.args
		}
		return args
	}
}
