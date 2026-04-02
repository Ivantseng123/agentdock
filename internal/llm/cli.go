package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	"github.com/google/uuid"
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
		timeout = 5 * time.Minute // CLI tools need more time (startup + API round-trip)
	}
	return &CLIProvider{
		name:    name,
		command: command,
		args:    args,
		timeout: timeout,
	}
}

func (c *CLIProvider) Name() string { return c.name }

func (c *CLIProvider) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	if _, err := exec.LookPath(c.command); err != nil {
		return ChatResponse{}, fmt.Errorf("%s not found in PATH: %w", c.command, err)
	}

	// Flatten system prompt + messages into a single text prompt.
	var sb strings.Builder
	if req.SystemPrompt != "" {
		sb.WriteString(req.SystemPrompt)
		// CLI/Ollama don't support native tool use — embed tool schemas in the prompt text
		if len(req.Tools) > 0 {
			sb.WriteString(CLIToolPromptSuffix(req.Tools))
		}
		sb.WriteString("\n\n")
	}
	for _, m := range req.Messages {
		switch m.Role {
		case "user":
			sb.WriteString("User:\n")
			sb.WriteString(m.Content)
			sb.WriteString("\n\n")
		case "assistant":
			sb.WriteString("Assistant:\n")
			sb.WriteString(m.Content)
			sb.WriteString("\n\n")
		case "tool_result":
			sb.WriteString("Tool Result:\n")
			sb.WriteString(m.Content)
			sb.WriteString("\n\n")
		}
	}
	fullPrompt := sb.String()

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	args, useStdin := c.buildArgs(fullPrompt)
	cmd := exec.CommandContext(ctx, c.command, args...)

	if useStdin {
		cmd.Stdin = strings.NewReader(fullPrompt)
	}

	output, err := cmd.Output()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return ChatResponse{}, fmt.Errorf("%s timed out after %s", c.command, c.timeout)
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			stderr := strings.TrimSpace(string(exitErr.Stderr))
			return ChatResponse{}, fmt.Errorf("%s failed (exit %d): %s", c.command, exitErr.ExitCode(), stderr)
		}
		return ChatResponse{}, fmt.Errorf("%s failed: %w", c.command, err)
	}

	text := strings.TrimSpace(string(output))
	if text == "" {
		return ChatResponse{}, fmt.Errorf("empty response from %s", c.command)
	}

	head := text
	if len(head) > 500 {
		head = head[:500] + "..."
	}
	slog.Info("cli raw response", "command", c.command, "response_len", len(text), "response_head", head)

	return parseJSONInTextResponse(text)
}

// buildArgs constructs CLI arguments and reports whether stdin should be used.
// If {prompt} fits in args (< 32KB), it's placed in args and stdin is NOT used.
// If {prompt} is too large, the placeholder arg is dropped and stdin is used instead.
// If no {prompt} placeholder exists in args, stdin is always used.
func (c *CLIProvider) buildArgs(prompt string) (args []string, useStdin bool) {
	if len(c.args) == 0 {
		return nil, true
	}

	const maxArgLen = 32 * 1024 // 32KB safe limit for command args

	hasPlaceholder := false
	promptInArgs := false

	for _, a := range c.args {
		if strings.Contains(a, "{prompt}") {
			hasPlaceholder = true
			if len(prompt) < maxArgLen {
				args = append(args, strings.ReplaceAll(a, "{prompt}", prompt))
				promptInArgs = true
			}
			// else: skip this arg entirely, prompt goes via stdin
		} else {
			args = append(args, a)
		}
	}

	// Use stdin only when prompt was NOT placed in args
	useStdin = !promptInArgs || !hasPlaceholder
	return args, useStdin
}

// parseJSONInTextResponse parses LLM text output that may contain JSON tool
// calls. Used by CLI and Ollama providers which simulate tool use via JSON.
//
// Patterns recognized:
//   - {"tool": "<name>", "args": {...}}  -> ToolCall
//   - {"tool": "finish", "result": {...}} -> Content = result JSON, StopReason = finish
//   - plain text -> Content = text, StopReason = finish
func parseJSONInTextResponse(text string) (ChatResponse, error) {
	jsonStr := extractJSON(text)
	if jsonStr == "" {
		// Plain text, no JSON found.
		return ChatResponse{
			Content:    text,
			StopReason: StopReasonFinish,
		}, nil
	}

	// Try parsing as a tool call.
	var toolMsg struct {
		Tool   string          `json:"tool"`
		Args   json.RawMessage `json:"args"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &toolMsg); err != nil {
		// JSON present but not a tool call -- treat as plain text.
		return ChatResponse{
			Content:    text,
			StopReason: StopReasonFinish,
		}, nil
	}

	if toolMsg.Tool == "" {
		// JSON without "tool" field — might be a raw triage card (LLM skipped the finish wrapper).
		// Check if it looks like a triage card (has "summary" field).
		var triageCheck struct {
			Summary string `json:"summary"`
		}
		if json.Unmarshal([]byte(jsonStr), &triageCheck) == nil && triageCheck.Summary != "" {
			slog.Info("cli: detected raw triage card (no finish wrapper)")
			return ChatResponse{
				Content:    jsonStr,
				StopReason: StopReasonFinish,
			}, nil
		}
		// Not a triage card either — treat as plain text.
		return ChatResponse{
			Content:    text,
			StopReason: StopReasonFinish,
		}, nil
	}

	if toolMsg.Tool == "finish" {
		resultStr := string(toolMsg.Result)
		if resultStr == "" || resultStr == "null" {
			resultStr = "{}"
		}
		return ChatResponse{
			Content:    resultStr,
			StopReason: StopReasonFinish,
		}, nil
	}

	// Regular tool call.
	args := toolMsg.Args
	if args == nil {
		args = json.RawMessage("{}")
	}
	return ChatResponse{
		ToolCalls: []ToolCall{
			{
				ID:   uuid.New().String(),
				Name: toolMsg.Tool,
				Args: args,
			},
		},
		StopReason: StopReasonToolUse,
	}, nil
}

// extractJSON finds the first JSON object in text, handling optional code blocks.
func extractJSON(text string) string {
	// Try code block first: ```json ... ``` or ``` ... ```
	if idx := strings.Index(text, "```json"); idx != -1 {
		start := idx + 7
		end := strings.Index(text[start:], "```")
		if end != -1 {
			candidate := strings.TrimSpace(text[start : start+end])
			if len(candidate) > 0 && candidate[0] == '{' {
				return candidate
			}
		}
	}
	if idx := strings.Index(text, "```"); idx != -1 {
		start := idx + 3
		// Skip to newline after opening backticks
		if nl := strings.Index(text[start:], "\n"); nl != -1 {
			start += nl + 1
		}
		end := strings.Index(text[start:], "```")
		if end != -1 {
			candidate := strings.TrimSpace(text[start : start+end])
			if len(candidate) > 0 && candidate[0] == '{' {
				return candidate
			}
		}
	}

	// Find matching braces.
	braceStart := strings.Index(text, "{")
	if braceStart == -1 {
		return ""
	}

	depth := 0
	inString := false
	escape := false
	for i := braceStart; i < len(text); i++ {
		ch := text[i]
		if escape {
			escape = false
			continue
		}
		if ch == '\\' && inString {
			escape = true
			continue
		}
		if ch == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		if ch == '{' {
			depth++
		} else if ch == '}' {
			depth--
			if depth == 0 {
				return text[braceStart : i+1]
			}
		}
	}
	return ""
}
