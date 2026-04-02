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

// buildArgs constructs CLI arguments by replacing {prompt} placeholders.
// If no args are configured, prompt is passed via stdin only (cmd.Stdin is set by caller).
// If args contain {prompt}, it is replaced with the actual prompt text.
// If args are set but contain no {prompt}, they are used as-is and prompt goes via stdin.
func (c *CLIProvider) buildArgs(prompt string) []string {
	if len(c.args) == 0 {
		return nil
	}
	var args []string
	for _, a := range c.args {
		args = append(args, strings.ReplaceAll(a, "{prompt}", prompt))
	}
	return args
}
