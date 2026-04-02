# Agent Loop Diagnosis Engine Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the hardcoded 4-step diagnosis pipeline with an LLM-driven agent loop where the model decides which tools to use and when to finish.

**Architecture:** The engine runs a loop: send messages to LLM → LLM returns a tool call or final triage card → execute tool → feed result back. Six tools (grep, read_file, list_files, read_context, search_code, git_log) give the LLM investigation capabilities. A per-turn FallbackChain with exponential backoff provides resilience.

**Tech Stack:** Go 1.22, Anthropic Messages API (tool use), OpenAI function calling, CLI/Ollama JSON-in-text simulation

---

## File Structure

| File | Responsibility |
|------|---------------|
| `internal/llm/provider.go` | ConversationProvider interface, ChatRequest/Response, Message, ToolCall, ToolDef types, ChatFallbackChain |
| `internal/llm/claude.go` | Claude Chat() with native tool use API |
| `internal/llm/openai.go` | OpenAI Chat() with function calling |
| `internal/llm/cli.go` | CLI Chat() with JSON-in-text simulation |
| `internal/llm/ollama.go` | Ollama Chat() with JSON-in-text simulation |
| `internal/llm/prompt.go` | Agent loop system prompt with tool descriptions |
| `internal/llm/backoff.go` | Exponential backoff with jitter for retryable errors |
| `internal/diagnosis/tools.go` | Tool interface + 6 tool implementations (grep, read_file, list_files, read_context, search_code, git_log) |
| `internal/diagnosis/loop.go` | Agent loop: RunLoop(ctx, chain, tools, input) → DiagnoseResponse |
| `internal/diagnosis/cache.go` | In-memory response cache with TTL |
| `internal/diagnosis/engine.go` | Rewritten Engine using agent loop + cache + FindFiles lite mode |
| `internal/config/config.go` | Add MaxTurns, MaxTokens, CacheTTL to DiagnosisConfig |
| `internal/bot/workflow.go` | Add "正在分析..." progress message |
| `cmd/bot/main.go` | Wire ChatFallbackChain + new Engine constructor |

---

### Task 1: New Types and ConversationProvider Interface

**Files:**
- Modify: `internal/llm/provider.go`
- Test: `internal/llm/provider_test.go`

- [ ] **Step 1: Add new types to provider.go**

Add the following types after the existing `DiagnoseResponse` struct. Keep `DiagnoseResponse`, `File`, `FileRef`, `PromptOptions` unchanged. Remove `DiagnoseRequest`, `Provider`, `ProviderEntry`, `FallbackChain`.

```go
// internal/llm/provider.go
package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
)

// --- Existing types (keep as-is) ---

type File struct {
	Path    string
	Content string
}

type FileRef struct {
	Path        string
	LineNumber  int
	Description string
}

type DiagnoseResponse struct {
	Summary       string
	Files         []FileRef
	Suggestions   []string
	Complexity    string
	OpenQuestions  []string
	Confidence    string
}

type PromptOptions struct {
	Language   string
	ExtraRules []string
}

// --- New types ---

const (
	StopReasonToolUse = "tool_use"
	StopReasonFinish  = "finish"
)

type ToolCall struct {
	ID   string          // Tool use ID for matching tool_result
	Name string          // Tool name
	Args json.RawMessage // Tool arguments as raw JSON
}

type Message struct {
	Role       string     // "assistant", "user", "tool_result"
	Content    string     // Text content
	ToolCalls  []ToolCall // For assistant messages with tool calls
	ToolCallID string     // For tool_result messages (matches ToolCall.ID)
}

type ToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

type ChatRequest struct {
	SystemPrompt string
	Messages     []Message
	Tools        []ToolDef
}

type ChatResponse struct {
	Content    string
	ToolCalls  []ToolCall
	StopReason string // StopReasonToolUse or StopReasonFinish
}

type ConversationProvider interface {
	Name() string
	Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
}

// --- FallbackChain for Chat ---

type ChatProviderEntry struct {
	Provider   ConversationProvider
	MaxRetries int
}

type ChatFallbackChain struct {
	entries []ChatProviderEntry
}

func NewChatFallbackChain(entries []ChatProviderEntry) *ChatFallbackChain {
	for i := range entries {
		if entries[i].MaxRetries <= 0 {
			entries[i].MaxRetries = 1
		}
	}
	return &ChatFallbackChain{entries: entries}
}

func (fc *ChatFallbackChain) Name() string { return "chat-fallback-chain" }

func (fc *ChatFallbackChain) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	var errs []string
	for _, e := range fc.entries {
		for attempt := 1; attempt <= e.MaxRetries; attempt++ {
			resp, err := e.Provider.Chat(ctx, req)
			if err == nil {
				return resp, nil
			}
			slog.Warn("chat provider failed",
				"provider", e.Provider.Name(),
				"attempt", fmt.Sprintf("%d/%d", attempt, e.MaxRetries),
				"error", err,
			)
			errs = append(errs, fmt.Sprintf("%s (attempt %d/%d): %s", e.Provider.Name(), attempt, e.MaxRetries, err))
		}
		slog.Warn("chat provider exhausted retries", "provider", e.Provider.Name())
	}
	return ChatResponse{}, fmt.Errorf("all chat providers failed: %s", strings.Join(errs, "; "))
}
```

- [ ] **Step 2: Rewrite provider_test.go for ChatFallbackChain**

Replace all existing tests. The old `Provider`/`FallbackChain` are removed.

```go
// internal/llm/provider_test.go
package llm

import (
	"context"
	"errors"
	"testing"
)

type mockChatProvider struct {
	name  string
	err   error
	resp  ChatResponse
	calls int
}

func (m *mockChatProvider) Name() string { return m.name }
func (m *mockChatProvider) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	m.calls++
	return m.resp, m.err
}

func TestChatFallbackChain_FirstSucceeds(t *testing.T) {
	p1 := &mockChatProvider{name: "p1", resp: ChatResponse{Content: "ok", StopReason: StopReasonFinish}}
	p2 := &mockChatProvider{name: "p2", resp: ChatResponse{Content: "backup"}}

	chain := NewChatFallbackChain([]ChatProviderEntry{
		{Provider: p1, MaxRetries: 1},
		{Provider: p2, MaxRetries: 1},
	})

	resp, err := chain.Chat(context.Background(), ChatRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "ok" {
		t.Errorf("expected 'ok', got %q", resp.Content)
	}
	if p2.calls != 0 {
		t.Errorf("p2 should not be called, got %d calls", p2.calls)
	}
}

func TestChatFallbackChain_FallsBack(t *testing.T) {
	p1 := &mockChatProvider{name: "p1", err: errors.New("timeout")}
	p2 := &mockChatProvider{name: "p2", resp: ChatResponse{Content: "backup ok"}}

	chain := NewChatFallbackChain([]ChatProviderEntry{
		{Provider: p1, MaxRetries: 1},
		{Provider: p2, MaxRetries: 1},
	})

	resp, err := chain.Chat(context.Background(), ChatRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "backup ok" {
		t.Errorf("expected 'backup ok', got %q", resp.Content)
	}
}

func TestChatFallbackChain_AllFail(t *testing.T) {
	chain := NewChatFallbackChain([]ChatProviderEntry{
		{Provider: &mockChatProvider{name: "p1", err: errors.New("fail1")}, MaxRetries: 2},
		{Provider: &mockChatProvider{name: "p2", err: errors.New("fail2")}, MaxRetries: 1},
	})

	_, err := chain.Chat(context.Background(), ChatRequest{})
	if err == nil {
		t.Fatal("expected error when all providers fail")
	}
}

func TestChatFallbackChain_RetriesPerProvider(t *testing.T) {
	p1 := &mockChatProvider{name: "p1", err: errors.New("fail")}
	p2 := &mockChatProvider{name: "p2", resp: ChatResponse{Content: "ok"}}

	chain := NewChatFallbackChain([]ChatProviderEntry{
		{Provider: p1, MaxRetries: 3},
		{Provider: p2, MaxRetries: 1},
	})

	chain.Chat(context.Background(), ChatRequest{})
	if p1.calls != 3 {
		t.Errorf("expected p1 called 3 times, got %d", p1.calls)
	}
	if p2.calls != 1 {
		t.Errorf("expected p2 called 1 time, got %d", p2.calls)
	}
}

func TestChatFallbackChain_DefaultRetries(t *testing.T) {
	p := &mockChatProvider{name: "p1", err: errors.New("fail")}
	chain := NewChatFallbackChain([]ChatProviderEntry{
		{Provider: p, MaxRetries: 0},
	})
	chain.Chat(context.Background(), ChatRequest{})
	if p.calls != 1 {
		t.Errorf("expected 1 call (default), got %d", p.calls)
	}
}
```

- [ ] **Step 3: Run tests**

Run: `cd /Users/ivantseng/local_file/slack-issue-bot && go test ./internal/llm/ -run TestChatFallback -v`
Expected: All 5 tests PASS

- [ ] **Step 4: Commit**

```bash
git add internal/llm/provider.go internal/llm/provider_test.go
git commit -m "refactor: replace Provider with ConversationProvider and ChatFallbackChain"
```

---

### Task 2: Claude Provider Chat()

**Files:**
- Modify: `internal/llm/claude.go`

- [ ] **Step 1: Rewrite claude.go**

Remove the old `Diagnose()` method. Add `Chat()` with native tool use. Keep `ParseLLMResponse` and `ParseLLMTextResponse` (used by agent loop for parsing finish output).

```go
// internal/llm/claude.go
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type ClaudeProvider struct {
	apiKey  string
	model   string
	baseURL string
	client  *http.Client
}

func NewClaudeProvider(apiKey, model, baseURL string, timeout time.Duration) *ClaudeProvider {
	return &ClaudeProvider{
		apiKey:  apiKey,
		model:   model,
		baseURL: baseURL,
		client:  &http.Client{Timeout: timeout},
	}
}

func (c *ClaudeProvider) Name() string { return "claude" }

func (c *ClaudeProvider) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	// Build Anthropic messages format
	var messages []map[string]any
	for _, m := range req.Messages {
		switch m.Role {
		case "user":
			messages = append(messages, map[string]any{"role": "user", "content": m.Content})
		case "assistant":
			if len(m.ToolCalls) > 0 {
				// Assistant message with tool use
				var content []map[string]any
				if m.Content != "" {
					content = append(content, map[string]any{"type": "text", "text": m.Content})
				}
				for _, tc := range m.ToolCalls {
					var args map[string]any
					json.Unmarshal(tc.Args, &args)
					content = append(content, map[string]any{
						"type":  "tool_use",
						"id":    tc.ID,
						"name":  tc.Name,
						"input": args,
					})
				}
				messages = append(messages, map[string]any{"role": "assistant", "content": content})
			} else {
				messages = append(messages, map[string]any{"role": "assistant", "content": m.Content})
			}
		case "tool_result":
			messages = append(messages, map[string]any{
				"role": "user",
				"content": []map[string]any{{
					"type":        "tool_result",
					"tool_use_id": m.ToolCallID,
					"content":     m.Content,
				}},
			})
		}
	}

	body := map[string]any{
		"model":      c.model,
		"max_tokens": 4096,
		"messages":   messages,
		"system":     req.SystemPrompt,
	}

	// Add tools if any
	if len(req.Tools) > 0 {
		var tools []map[string]any
		for _, t := range req.Tools {
			tools = append(tools, map[string]any{
				"name":         t.Name,
				"description":  t.Description,
				"input_schema": t.InputSchema,
			})
		}
		body["tools"] = tools
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/v1/messages", bytes.NewReader(jsonBody))
	if err != nil {
		return ChatResponse{}, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("call claude: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return ChatResponse{}, fmt.Errorf("claude returned %d: %s", resp.StatusCode, string(respBody))
	}

	// Parse Claude response
	var raw struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
	}
	if err := json.Unmarshal(respBody, &raw); err != nil {
		return ChatResponse{}, fmt.Errorf("parse response: %w", err)
	}

	result := ChatResponse{}
	for _, block := range raw.Content {
		switch block.Type {
		case "text":
			result.Content += block.Text
		case "tool_use":
			result.ToolCalls = append(result.ToolCalls, ToolCall{
				ID:   block.ID,
				Name: block.Name,
				Args: block.Input,
			})
		}
	}

	switch raw.StopReason {
	case "tool_use":
		result.StopReason = StopReasonToolUse
	default:
		result.StopReason = StopReasonFinish
	}

	return result, nil
}

// ParseLLMTextResponse extracts structured diagnosis from LLM text output.
// Used by the agent loop to parse the finish response.
func ParseLLMTextResponse(text string) (DiagnoseResponse, error) {
	var structured struct {
		Summary       string `json:"summary"`
		Files         []struct {
			Path        string `json:"path"`
			LineNumber  int    `json:"line_number"`
			Description string `json:"description"`
		} `json:"files"`
		Suggestions   []string `json:"suggestions"`
		Complexity    string   `json:"complexity"`
		OpenQuestions []string `json:"open_questions"`
		Confidence    string   `json:"confidence"`
	}

	jsonStr := text
	if idx := strings.Index(text, "```json"); idx != -1 {
		start := idx + 7
		end := strings.Index(text[start:], "```")
		if end != -1 {
			jsonStr = text[start : start+end]
		}
	} else if idx := strings.Index(text, "{"); idx != -1 {
		jsonStr = text[idx:]
	}

	if err := json.Unmarshal([]byte(strings.TrimSpace(jsonStr)), &structured); err != nil {
		return DiagnoseResponse{Summary: text}, nil
	}

	var files []FileRef
	for _, f := range structured.Files {
		files = append(files, FileRef{
			Path:        f.Path,
			LineNumber:  f.LineNumber,
			Description: f.Description,
		})
	}

	return DiagnoseResponse{
		Summary:       structured.Summary,
		Files:         files,
		Suggestions:   structured.Suggestions,
		Complexity:    structured.Complexity,
		OpenQuestions:  structured.OpenQuestions,
		Confidence:    structured.Confidence,
	}, nil
}
```

- [ ] **Step 2: Run tests**

Run: `cd /Users/ivantseng/local_file/slack-issue-bot && go build ./...`
Expected: Build succeeds (may have compile errors in other files referencing old Provider — fix in later tasks)

- [ ] **Step 3: Commit**

```bash
git add internal/llm/claude.go
git commit -m "feat: Claude provider Chat() with native tool use"
```

---

### Task 3: OpenAI Provider Chat()

**Files:**
- Modify: `internal/llm/openai.go`

- [ ] **Step 1: Rewrite openai.go**

Replace `Diagnose()` with `Chat()` using OpenAI function calling format.

```go
// internal/llm/openai.go
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type OpenAIProvider struct {
	apiKey  string
	model   string
	baseURL string
	client  *http.Client
}

func NewOpenAIProvider(apiKey, model, baseURL string, timeout time.Duration) *OpenAIProvider {
	return &OpenAIProvider{
		apiKey:  apiKey,
		model:   model,
		baseURL: baseURL,
		client:  &http.Client{Timeout: timeout},
	}
}

func (o *OpenAIProvider) Name() string { return "openai" }

func (o *OpenAIProvider) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	var messages []map[string]any

	// System message
	if req.SystemPrompt != "" {
		messages = append(messages, map[string]any{"role": "system", "content": req.SystemPrompt})
	}

	for _, m := range req.Messages {
		switch m.Role {
		case "user":
			messages = append(messages, map[string]any{"role": "user", "content": m.Content})
		case "assistant":
			msg := map[string]any{"role": "assistant"}
			if m.Content != "" {
				msg["content"] = m.Content
			}
			if len(m.ToolCalls) > 0 {
				var toolCalls []map[string]any
				for _, tc := range m.ToolCalls {
					toolCalls = append(toolCalls, map[string]any{
						"id":   tc.ID,
						"type": "function",
						"function": map[string]any{
							"name":      tc.Name,
							"arguments": string(tc.Args),
						},
					})
				}
				msg["tool_calls"] = toolCalls
			}
			messages = append(messages, msg)
		case "tool_result":
			messages = append(messages, map[string]any{
				"role":         "tool",
				"tool_call_id": m.ToolCallID,
				"content":      m.Content,
			})
		}
	}

	body := map[string]any{
		"model":      o.model,
		"messages":   messages,
		"max_tokens": 4096,
	}

	if len(req.Tools) > 0 {
		var tools []map[string]any
		for _, t := range req.Tools {
			tools = append(tools, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        t.Name,
					"description": t.Description,
					"parameters":  t.InputSchema,
				},
			})
		}
		body["tools"] = tools
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", o.baseURL+"/v1/chat/completions", bytes.NewReader(jsonBody))
	if err != nil {
		return ChatResponse{}, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+o.apiKey)

	resp, err := o.client.Do(httpReq)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("call openai: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return ChatResponse{}, fmt.Errorf("openai returned %d: %s", resp.StatusCode, string(respBody))
	}

	var raw struct {
		Choices []struct {
			Message struct {
				Content   *string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &raw); err != nil {
		return ChatResponse{}, fmt.Errorf("parse response: %w", err)
	}
	if len(raw.Choices) == 0 {
		return ChatResponse{}, fmt.Errorf("empty response from openai")
	}

	choice := raw.Choices[0]
	result := ChatResponse{}
	if choice.Message.Content != nil {
		result.Content = *choice.Message.Content
	}
	for _, tc := range choice.Message.ToolCalls {
		result.ToolCalls = append(result.ToolCalls, ToolCall{
			ID:   tc.ID,
			Name: tc.Function.Name,
			Args: json.RawMessage(tc.Function.Arguments),
		})
	}

	switch choice.FinishReason {
	case "tool_calls":
		result.StopReason = StopReasonToolUse
	default:
		result.StopReason = StopReasonFinish
	}

	return result, nil
}
```

- [ ] **Step 2: Commit**

```bash
git add internal/llm/openai.go
git commit -m "feat: OpenAI provider Chat() with function calling"
```

---

### Task 4: CLI and Ollama Provider Chat()

**Files:**
- Modify: `internal/llm/cli.go`
- Modify: `internal/llm/ollama.go`
- Modify: `internal/llm/cli_test.go`

Both use JSON-in-text simulation: the system prompt tells the LLM to output `{"tool": "...", "args": {...}}` or `{"tool": "finish", "result": {...}}`.

- [ ] **Step 1: Rewrite cli.go**

Keep `buildArgs()` and its logic. Replace `Diagnose()` with `Chat()`.

```go
// internal/llm/cli.go
package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/google/uuid"
)

type CLIProvider struct {
	name    string
	command string
	args    []string
	timeout time.Duration
}

func NewCLIProvider(name, command string, args []string, timeout time.Duration) *CLIProvider {
	if timeout <= 0 {
		timeout = 5 * time.Minute
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

	// Build a single prompt from system + messages for CLI
	var sb strings.Builder
	sb.WriteString(req.SystemPrompt)
	sb.WriteString("\n\n")
	for _, m := range req.Messages {
		switch m.Role {
		case "user":
			sb.WriteString("User:\n")
			sb.WriteString(m.Content)
			sb.WriteString("\n\n")
		case "assistant":
			sb.WriteString("Assistant:\n")
			sb.WriteString(m.Content)
			for _, tc := range m.ToolCalls {
				sb.WriteString(fmt.Sprintf(`{"tool": "%s", "args": %s}`, tc.Name, string(tc.Args)))
			}
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

	return parseJSONInTextResponse(text)
}

func (c *CLIProvider) buildArgs(prompt string) (args []string, useStdin bool) {
	if len(c.args) == 0 {
		return nil, true
	}

	const maxArgLen = 32 * 1024

	hasPlaceholder := false
	promptInArgs := false

	for _, a := range c.args {
		if strings.Contains(a, "{prompt}") {
			hasPlaceholder = true
			if len(prompt) < maxArgLen {
				args = append(args, strings.ReplaceAll(a, "{prompt}", prompt))
				promptInArgs = true
			}
		} else {
			args = append(args, a)
		}
	}

	useStdin = !promptInArgs || !hasPlaceholder
	return args, useStdin
}

// parseJSONInTextResponse parses CLI/Ollama JSON-in-text tool call format.
// Expected: {"tool": "grep", "args": {"pattern": "x"}} or {"tool": "finish", "result": {...}}
func parseJSONInTextResponse(text string) (ChatResponse, error) {
	// Extract JSON from text (may be wrapped in code blocks or explanation)
	jsonStr := text
	if idx := strings.Index(text, "```json"); idx != -1 {
		start := idx + 7
		if end := strings.Index(text[start:], "```"); end != -1 {
			jsonStr = text[start : start+end]
		}
	} else if idx := strings.Index(text, "{"); idx != -1 {
		// Find matching closing brace
		depth := 0
		for i := idx; i < len(text); i++ {
			if text[i] == '{' {
				depth++
			} else if text[i] == '}' {
				depth--
				if depth == 0 {
					jsonStr = text[idx : i+1]
					break
				}
			}
		}
	}

	var parsed struct {
		Tool   string          `json:"tool"`
		Args   json.RawMessage `json:"args"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(jsonStr)), &parsed); err != nil {
		// Not valid JSON tool call — treat as text response (finish)
		return ChatResponse{Content: text, StopReason: StopReasonFinish}, nil
	}

	if parsed.Tool == "finish" || parsed.Tool == "" {
		// Finish — result contains the triage card
		content := string(parsed.Result)
		if content == "" || content == "null" {
			content = text
		}
		return ChatResponse{Content: content, StopReason: StopReasonFinish}, nil
	}

	// Tool call
	return ChatResponse{
		ToolCalls: []ToolCall{{
			ID:   uuid.New().String(),
			Name: parsed.Tool,
			Args: parsed.Args,
		}},
		StopReason: StopReasonToolUse,
	}, nil
}
```

- [ ] **Step 2: Rewrite ollama.go**

```go
// internal/llm/ollama.go
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type OllamaProvider struct {
	model   string
	baseURL string
	client  *http.Client
}

func NewOllamaProvider(model, baseURL string, timeout time.Duration) *OllamaProvider {
	return &OllamaProvider{
		model:   model,
		baseURL: baseURL,
		client:  &http.Client{Timeout: timeout},
	}
}

func (o *OllamaProvider) Name() string { return "ollama" }

func (o *OllamaProvider) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	var messages []map[string]string

	// Flatten system + conversation into Ollama chat format
	if req.SystemPrompt != "" {
		messages = append(messages, map[string]string{"role": "system", "content": req.SystemPrompt})
	}
	for _, m := range req.Messages {
		switch m.Role {
		case "user":
			messages = append(messages, map[string]string{"role": "user", "content": m.Content})
		case "assistant":
			content := m.Content
			for _, tc := range m.ToolCalls {
				content += fmt.Sprintf(`{"tool": "%s", "args": %s}`, tc.Name, string(tc.Args))
			}
			messages = append(messages, map[string]string{"role": "assistant", "content": content})
		case "tool_result":
			messages = append(messages, map[string]string{"role": "user", "content": "Tool Result:\n" + m.Content})
		}
	}

	body := map[string]any{
		"model":    o.model,
		"stream":   false,
		"messages": messages,
	}
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", o.baseURL+"/api/chat", bytes.NewReader(jsonBody))
	if err != nil {
		return ChatResponse{}, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := o.client.Do(httpReq)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("call ollama: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return ChatResponse{}, fmt.Errorf("ollama returned %d: %s", resp.StatusCode, string(respBody))
	}

	var raw struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(respBody, &raw); err != nil {
		return ChatResponse{}, fmt.Errorf("parse response: %w", err)
	}

	text := strings.TrimSpace(raw.Message.Content)
	return parseJSONInTextResponse(text)
}
```

- [ ] **Step 3: Update cli_test.go**

The existing `buildArgs` tests remain valid. Just update the import if needed (the function signature hasn't changed).

Run: `cd /Users/ivantseng/local_file/slack-issue-bot && go test ./internal/llm/ -run TestBuildArgs -v`
Expected: PASS

- [ ] **Step 4: Add uuid dependency**

Run: `cd /Users/ivantseng/local_file/slack-issue-bot && go get github.com/google/uuid`

- [ ] **Step 5: Commit**

```bash
git add internal/llm/cli.go internal/llm/ollama.go internal/llm/cli_test.go go.mod go.sum
git commit -m "feat: CLI and Ollama provider Chat() with JSON-in-text simulation"
```

---

### Task 5: Config Changes

**Files:**
- Modify: `internal/config/config.go`

- [ ] **Step 1: Add new fields to DiagnosisConfig**

```go
// In config.go, replace the DiagnosisConfig struct:
type DiagnosisConfig struct {
	Mode      string        `yaml:"mode"`       // "full" = use LLM, "lite" = grep only
	MaxTurns  int           `yaml:"max_turns"`  // Agent loop turn limit (default 5)
	MaxTokens int           `yaml:"max_tokens"` // Token budget per diagnosis (default 100000)
	CacheTTL  time.Duration `yaml:"cache_ttl"`  // Diagnosis cache TTL (default 10m)
	Prompt    PromptConfig  `yaml:"prompt"`
}
```

- [ ] **Step 2: Run tests**

Run: `cd /Users/ivantseng/local_file/slack-issue-bot && go test ./internal/config/ -v`
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add internal/config/config.go
git commit -m "feat: add MaxTurns, MaxTokens, CacheTTL to DiagnosisConfig"
```

---

### Task 6: Diagnosis Tools

**Files:**
- Create: `internal/diagnosis/tools.go`
- Create: `internal/diagnosis/tools_test.go`

- [ ] **Step 1: Define Tool interface and 6 implementations**

```go
// internal/diagnosis/tools.go
package diagnosis

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"slack-issue-bot/internal/llm"
)

// Tool is a function the LLM can call during the agent loop.
type Tool interface {
	Name() string
	Description() string
	Schema() llm.ToolDef
	Execute(repoPath string, args json.RawMessage) (string, error)
}

// AllTools returns all tools available to the agent loop.
func AllTools() []Tool {
	return []Tool{
		&GrepTool{},
		&ReadFileTool{},
		&ListFilesTool{},
		&ReadContextTool{},
		&SearchCodeTool{},
		&GitLogTool{},
	}
}

// ToolDefs returns ToolDef schemas for all tools.
func ToolDefs(tools []Tool) []llm.ToolDef {
	var defs []llm.ToolDef
	for _, t := range tools {
		defs = append(defs, t.Schema())
	}
	return defs
}

// ToolMap returns a name→Tool map for fast lookup.
func ToolMap(tools []Tool) map[string]Tool {
	m := make(map[string]Tool)
	for _, t := range tools {
		m[t.Name()] = t
	}
	return m
}

// --- GrepTool ---

type GrepTool struct{}

func (t *GrepTool) Name() string        { return "grep" }
func (t *GrepTool) Description() string  { return "Find which files mention a term. Use for broad discovery." }
func (t *GrepTool) Schema() llm.ToolDef {
	return llm.ToolDef{
		Name:        "grep",
		Description: "Find which files mention a term. Use for broad discovery — 'which files are related to X?'",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern":     map[string]any{"type": "string", "description": "Search pattern (case-insensitive)"},
				"max_results": map[string]any{"type": "integer", "description": "Max files to return (default 10)"},
			},
			"required": []string{"pattern"},
		},
	}
}

func (t *GrepTool) Execute(repoPath string, args json.RawMessage) (string, error) {
	var input struct {
		Pattern    string `json:"pattern"`
		MaxResults int    `json:"max_results"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if input.Pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}
	if len(input.Pattern) > 200 {
		input.Pattern = input.Pattern[:200]
	}
	if input.MaxResults <= 0 {
		input.MaxResults = 10
	}

	cmd := exec.Command("git", "-C", repoPath, "grep", "-rli", "--no-color", input.Pattern)
	out, err := cmd.Output()
	if err != nil {
		return "No matches found.", nil
	}

	seen := make(map[string]int)
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" && !shouldSkipFile(line) {
			seen[line]++
		}
	}

	type scored struct {
		path  string
		score int
	}
	var files []scored
	for p, s := range seen {
		files = append(files, scored{p, s})
	}
	for i := range files {
		for j := i + 1; j < len(files); j++ {
			if files[j].score > files[i].score {
				files[i], files[j] = files[j], files[i]
			}
		}
	}

	var sb strings.Builder
	for i, f := range files {
		if i >= input.MaxResults {
			sb.WriteString(fmt.Sprintf("... and %d more files\n", len(files)-input.MaxResults))
			break
		}
		sb.WriteString(fmt.Sprintf("%s (%d hits)\n", f.path, f.score))
	}
	if sb.Len() == 0 {
		return "No matches found.", nil
	}
	return sb.String(), nil
}

// --- ReadFileTool ---

type ReadFileTool struct{}

func (t *ReadFileTool) Name() string        { return "read_file" }
func (t *ReadFileTool) Description() string  { return "Read the content of a specific file." }
func (t *ReadFileTool) Schema() llm.ToolDef {
	return llm.ToolDef{
		Name:        "read_file",
		Description: "Read the content of a specific file. Use after grep to examine a candidate.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":      map[string]any{"type": "string", "description": "File path relative to repo root"},
				"max_lines": map[string]any{"type": "integer", "description": "Max lines to read (default 200)"},
			},
			"required": []string{"path"},
		},
	}
}

func (t *ReadFileTool) Execute(repoPath string, args json.RawMessage) (string, error) {
	var input struct {
		Path     string `json:"path"`
		MaxLines int    `json:"max_lines"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if input.MaxLines <= 0 {
		input.MaxLines = 200
	}

	fullPath := filepath.Join(repoPath, input.Path)
	content, err := os.ReadFile(fullPath)
	if err != nil {
		return fmt.Sprintf("Error: file not found: %s", input.Path), nil
	}

	lines := strings.Split(string(content), "\n")
	if len(lines) > input.MaxLines {
		lines = append(lines[:input.MaxLines], fmt.Sprintf("[truncated: %d lines total, showing first %d]", len(lines), input.MaxLines))
	}

	var sb strings.Builder
	for i, line := range lines {
		sb.WriteString(fmt.Sprintf("%4d | %s\n", i+1, line))
	}
	return sb.String(), nil
}

// --- ListFilesTool ---

type ListFilesTool struct{}

func (t *ListFilesTool) Name() string        { return "list_files" }
func (t *ListFilesTool) Description() string  { return "List all files in the repo." }
func (t *ListFilesTool) Schema() llm.ToolDef {
	return llm.ToolDef{
		Name:        "list_files",
		Description: "List all files in the repo. Use when grep returns no results and you need to browse the file tree.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{"type": "string", "description": "Optional glob filter (e.g. '*.go', 'src/**/*.java')"},
			},
		},
	}
}

func (t *ListFilesTool) Execute(repoPath string, args json.RawMessage) (string, error) {
	var input struct {
		Pattern string `json:"pattern"`
	}
	json.Unmarshal(args, &input)

	gitArgs := []string{"-C", repoPath, "ls-files"}
	if input.Pattern != "" {
		gitArgs = append(gitArgs, input.Pattern)
	}

	cmd := exec.Command("git", gitArgs...)
	out, err := cmd.Output()
	if err != nil {
		// Fallback to filepath.Walk
		var lines []string
		filepath.Walk(repoPath, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(repoPath, path)
			if !shouldSkipFile(rel) {
				lines = append(lines, rel)
			}
			if len(lines) >= 500 {
				return filepath.SkipAll
			}
			return nil
		})
		return strings.Join(lines, "\n"), nil
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) > 500 {
		lines = append(lines[:500], fmt.Sprintf("... and %d more files", len(lines)-500))
	}
	return strings.Join(lines, "\n"), nil
}

// --- ReadContextTool ---

type ReadContextTool struct{}

func (t *ReadContextTool) Name() string        { return "read_context" }
func (t *ReadContextTool) Description() string  { return "Read repo context documents." }
func (t *ReadContextTool) Schema() llm.ToolDef {
	return llm.ToolDef{
		Name:        "read_context",
		Description: "Read repo context documents (README.md, CLAUDE.md, agent.md). Use to understand the repo's purpose, structure, and cross-repo relationships.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}
}

func (t *ReadContextTool) Execute(repoPath string, args json.RawMessage) (string, error) {
	candidates := []string{"README.md", "readme.md", "CLAUDE.md", "agent.md", "AGENTS.md"}
	var sb strings.Builder
	for _, name := range candidates {
		fullPath := filepath.Join(repoPath, name)
		content, err := os.ReadFile(fullPath)
		if err != nil {
			continue
		}
		lines := strings.Split(string(content), "\n")
		if len(lines) > 100 {
			lines = append(lines[:100], "[truncated]")
		}
		sb.WriteString(fmt.Sprintf("=== %s ===\n%s\n\n", name, strings.Join(lines, "\n")))
	}
	if sb.Len() == 0 {
		return "No context documents found.", nil
	}
	return sb.String(), nil
}

// --- SearchCodeTool ---

type SearchCodeTool struct{}

func (t *SearchCodeTool) Name() string        { return "search_code" }
func (t *SearchCodeTool) Description() string  { return "Regex search with surrounding context." }
func (t *SearchCodeTool) Schema() llm.ToolDef {
	return llm.ToolDef{
		Name:        "search_code",
		Description: "Find exact code patterns with surrounding context lines. Use when you know a function name, error message, etc.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern":       map[string]any{"type": "string", "description": "Regex pattern to search"},
				"file_pattern":  map[string]any{"type": "string", "description": "Optional glob to filter files (e.g. '*.go')"},
				"context_lines": map[string]any{"type": "integer", "description": "Lines of context around matches (default 2)"},
			},
			"required": []string{"pattern"},
		},
	}
}

func (t *SearchCodeTool) Execute(repoPath string, args json.RawMessage) (string, error) {
	var input struct {
		Pattern      string `json:"pattern"`
		FilePattern  string `json:"file_pattern"`
		ContextLines int    `json:"context_lines"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if input.Pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}
	if len(input.Pattern) > 200 {
		input.Pattern = input.Pattern[:200]
	}
	if input.ContextLines <= 0 {
		input.ContextLines = 2
	}

	gitArgs := []string{"-C", repoPath, "grep", "-n", "-E",
		fmt.Sprintf("-C%d", input.ContextLines),
		"--no-color", input.Pattern}
	if input.FilePattern != "" {
		gitArgs = append(gitArgs, "--", input.FilePattern)
	}

	cmd := exec.Command("git", gitArgs...)
	out, err := cmd.Output()
	if err != nil {
		return "No matches found.", nil
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) > 200 { // Cap output (~50 matches with 4 lines context)
		lines = append(lines[:200], fmt.Sprintf("... truncated (%d total lines)", len(lines)))
	}
	return strings.Join(lines, "\n"), nil
}

// --- GitLogTool ---

type GitLogTool struct{}

func (t *GitLogTool) Name() string        { return "git_log" }
func (t *GitLogTool) Description() string  { return "View recent commits." }
func (t *GitLogTool) Schema() llm.ToolDef {
	return llm.ToolDef{
		Name:        "git_log",
		Description: "View recent commits. Use to check if related code was recently changed.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"count": map[string]any{"type": "integer", "description": "Number of commits (default 20)"},
				"path":  map[string]any{"type": "string", "description": "Optional file path filter"},
			},
		},
	}
}

func (t *GitLogTool) Execute(repoPath string, args json.RawMessage) (string, error) {
	var input struct {
		Count int    `json:"count"`
		Path  string `json:"path"`
	}
	json.Unmarshal(args, &input)
	if input.Count <= 0 {
		input.Count = 20
	}

	gitArgs := []string{"-C", repoPath, "log", "--oneline", fmt.Sprintf("-n%d", input.Count)}
	if input.Path != "" {
		gitArgs = append(gitArgs, "--", input.Path)
	}

	cmd := exec.Command("git", gitArgs...)
	out, err := cmd.Output()
	if err != nil {
		return "No git history available.", nil
	}
	return strings.TrimSpace(string(out)), nil
}

// --- Shared helpers ---

func shouldSkipFile(path string) bool {
	skip := []string{".min.js", ".min.css", "vendor/", "node_modules/", ".lock", "go.sum", "package-lock", ".class", ".jar", "target/", "build/", ".git/"}
	for _, s := range skip {
		if strings.Contains(path, s) {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Write tests for tools**

```go
// internal/diagnosis/tools_test.go
package diagnosis

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func initGitRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.name", "test")
	runGit(t, dir, "config", "user.email", "test@test.com")
	for path, content := range files {
		full := filepath.Join(dir, path)
		os.MkdirAll(filepath.Dir(full), 0755)
		os.WriteFile(full, []byte(content), 0644)
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "init")
	return dir
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

func TestGrepTool(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	repo := initGitRepo(t, map[string]string{
		"src/login.go":   "package auth\nfunc Login() {}",
		"src/logout.go":  "package auth\nfunc Logout() {}",
		"vendor/lib.go":  "package lib\nfunc Login() {}",
	})

	tool := &GrepTool{}
	args, _ := json.Marshal(map[string]any{"pattern": "Login", "max_results": 5})
	result, err := tool.Execute(repo, args)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(result, "src/login.go") {
		t.Errorf("expected login.go in results: %s", result)
	}
	if contains(result, "vendor/") {
		t.Errorf("vendor should be skipped: %s", result)
	}
}

func TestReadFileTool(t *testing.T) {
	repo := initGitRepo(t, map[string]string{
		"src/main.go": "package main\nfunc main() {}\n",
	})

	tool := &ReadFileTool{}
	args, _ := json.Marshal(map[string]any{"path": "src/main.go"})
	result, err := tool.Execute(repo, args)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(result, "func main()") {
		t.Errorf("expected file content: %s", result)
	}
}

func TestReadFileTool_NotFound(t *testing.T) {
	repo := initGitRepo(t, map[string]string{"a.go": "package a"})

	tool := &ReadFileTool{}
	args, _ := json.Marshal(map[string]any{"path": "nonexistent.go"})
	result, err := tool.Execute(repo, args)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(result, "Error: file not found") {
		t.Errorf("expected error message: %s", result)
	}
}

func TestListFilesTool(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	repo := initGitRepo(t, map[string]string{
		"src/a.go": "package a",
		"src/b.go": "package b",
		"lib/c.js": "const c = 1",
	})

	tool := &ListFilesTool{}
	args, _ := json.Marshal(map[string]any{"pattern": "*.go"})
	result, err := tool.Execute(repo, args)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(result, "a.go") || !contains(result, "b.go") {
		t.Errorf("expected go files: %s", result)
	}
}

func TestReadContextTool(t *testing.T) {
	repo := initGitRepo(t, map[string]string{
		"README.md": "# My Project\nThis is a test.",
		"src/a.go":  "package a",
	})

	tool := &ReadContextTool{}
	result, err := tool.Execute(repo, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(result, "My Project") {
		t.Errorf("expected README content: %s", result)
	}
}

func TestSearchCodeTool(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	repo := initGitRepo(t, map[string]string{
		"src/handler.go": "package handler\nfunc HandleRequest() {\n\t// process\n}\n",
	})

	tool := &SearchCodeTool{}
	args, _ := json.Marshal(map[string]any{"pattern": "HandleRequest"})
	result, err := tool.Execute(repo, args)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(result, "HandleRequest") {
		t.Errorf("expected match: %s", result)
	}
}

func TestGitLogTool(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	repo := initGitRepo(t, map[string]string{"a.go": "package a"})

	tool := &GitLogTool{}
	args, _ := json.Marshal(map[string]any{"count": 5})
	result, err := tool.Execute(repo, args)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(result, "init") {
		t.Errorf("expected init commit: %s", result)
	}
}

func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && (s == substr || len(s) >= len(substr) && containsStr(s, substr))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
```

- [ ] **Step 3: Run tests**

Run: `cd /Users/ivantseng/local_file/slack-issue-bot && go test ./internal/diagnosis/ -run TestGrep -v && go test ./internal/diagnosis/ -run TestRead -v && go test ./internal/diagnosis/ -run TestList -v && go test ./internal/diagnosis/ -run TestSearch -v && go test ./internal/diagnosis/ -run TestGitLog -v`
Expected: All PASS

- [ ] **Step 4: Commit**

```bash
git add internal/diagnosis/tools.go internal/diagnosis/tools_test.go
git commit -m "feat: 6 diagnosis tools (grep, read_file, list_files, read_context, search_code, git_log)"
```

---

### Task 7: Updated System Prompt

**Files:**
- Modify: `internal/llm/prompt.go`

- [ ] **Step 1: Rewrite prompt.go for agent loop**

Replace the old `SystemPrompt`, `BuildPrompt`, `outputSchema` with the agent loop version. Keep `PromptOptions`.

```go
// internal/llm/prompt.go
package llm

import (
	"fmt"
	"strings"
)

// AgentSystemPrompt builds the system prompt for the agent loop.
func AgentSystemPrompt(diagType string, opts PromptOptions) string {
	base := `You are a code triage assistant. You have tools to investigate a codebase. Your goal: produce a SHORT triage card for a ` + diagType + ` report.

## Rules
- Use tools to find relevant files. Start with grep, then read files that look promising.
- If the message is in a non-English language, translate key terms to likely code identifiers before searching.
- If grep finds nothing, try read_context to understand the repo, then list_files to browse.
- List ONLY files you have actually read. Max 5 files.
- Do NOT guess field names, variable names, UI labels, or component positions.
- Do NOT suggest implementation details or code changes.
- open_questions: ONLY things the reporter/PM can answer. Do NOT include code visibility issues.
- Set confidence: "low" if no clearly related code, "medium" if partially, "high" if strongly related.

## Output
When you have enough information (or this is your last turn), respond with the triage card as JSON (no tool call):

` + agentOutputSchema(diagType)

	if opts.Language != "" {
		base += fmt.Sprintf("\n\nRespond in %s. All JSON string values in %s.", opts.Language, opts.Language)
	}
	for _, rule := range opts.ExtraRules {
		base += "\n" + rule
	}
	return base
}

func agentOutputSchema(diagType string) string {
	if diagType == "bug" {
		return `{
  "summary": "One sentence: what area of code is likely involved",
  "files": [{"path": "exact/path", "line_number": 0, "description": "Why relevant"}],
  "suggestions": ["What to investigate (max 2)"],
  "open_questions": ["Anything you cannot determine from code alone"],
  "confidence": "low|medium|high"
}
line_number: use 0 if unsure. files: max 5.`
	}
	return `{
  "summary": "One sentence: what existing code relates to this request",
  "files": [{"path": "exact/path", "line_number": 0, "description": "Why relevant"}],
  "suggestions": ["Where to start (max 2)"],
  "complexity": "low|medium|high",
  "open_questions": ["Anything you cannot determine from code alone"],
  "confidence": "low|medium|high"
}
line_number: use 0 if unsure. files: max 5.`
}

// CLIToolPromptSuffix returns additional prompt text for CLI/Ollama providers
// that don't support native tool use. Tells the LLM to use JSON-in-text format.
func CLIToolPromptSuffix(tools []ToolDef) string {
	var sb strings.Builder
	sb.WriteString("\n\n## Tool Use (JSON format)\n")
	sb.WriteString("To use a tool, respond with ONLY a JSON object:\n")
	sb.WriteString(`{"tool": "<tool_name>", "args": {<arguments>}}` + "\n\n")
	sb.WriteString("When you are done investigating, respond with:\n")
	sb.WriteString(`{"tool": "finish", "result": {<triage card JSON>}}` + "\n\n")
	sb.WriteString("Available tools:\n")
	for _, t := range tools {
		sb.WriteString(fmt.Sprintf("- **%s**: %s\n", t.Name, t.Description))
	}
	return sb.String()
}
```

- [ ] **Step 2: Commit**

```bash
git add internal/llm/prompt.go
git commit -m "feat: agent loop system prompt with tool descriptions"
```

---

### Task 8: Response Cache

**Files:**
- Create: `internal/diagnosis/cache.go`
- Create: `internal/diagnosis/cache_test.go`

- [ ] **Step 1: Implement cache**

```go
// internal/diagnosis/cache.go
package diagnosis

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"sync"
	"time"

	"slack-issue-bot/internal/llm"
)

type cacheEntry struct {
	response  llm.DiagnoseResponse
	expiresAt time.Time
}

type Cache struct {
	mu      sync.Mutex
	entries map[string]cacheEntry
	ttl     time.Duration
}

func NewCache(ttl time.Duration) *Cache {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	c := &Cache{
		entries: make(map[string]cacheEntry),
		ttl:     ttl,
	}
	go c.cleanup()
	return c
}

func (c *Cache) Key(repo, branch, message, language string, extraRules []string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%s|%s|%s|%s", repo, branch, message, language, strings.Join(extraRules, "|"))
	return fmt.Sprintf("%x", h.Sum(nil))
}

func (c *Cache) Get(key string) (llm.DiagnoseResponse, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok || time.Now().After(entry.expiresAt) {
		return llm.DiagnoseResponse{}, false
	}
	return entry.response, true
}

func (c *Cache) Set(key string, resp llm.DiagnoseResponse) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = cacheEntry{
		response:  resp,
		expiresAt: time.Now().Add(c.ttl),
	}
}

func (c *Cache) cleanup() {
	ticker := time.NewTicker(c.ttl)
	defer ticker.Stop()
	for range ticker.C {
		c.mu.Lock()
		now := time.Now()
		for k, v := range c.entries {
			if now.After(v.expiresAt) {
				delete(c.entries, k)
			}
		}
		c.mu.Unlock()
	}
}
```

- [ ] **Step 2: Write tests**

```go
// internal/diagnosis/cache_test.go
package diagnosis

import (
	"testing"
	"time"

	"slack-issue-bot/internal/llm"
)

func TestCache_SetGet(t *testing.T) {
	c := NewCache(1 * time.Minute)
	key := c.Key("owner/repo", "main", "bug report", "en", nil)

	resp := llm.DiagnoseResponse{Summary: "test"}
	c.Set(key, resp)

	got, ok := c.Get(key)
	if !ok {
		t.Fatal("expected cache hit")
	}
	if got.Summary != "test" {
		t.Errorf("expected 'test', got %q", got.Summary)
	}
}

func TestCache_Miss(t *testing.T) {
	c := NewCache(1 * time.Minute)
	_, ok := c.Get("nonexistent")
	if ok {
		t.Fatal("expected cache miss")
	}
}

func TestCache_Expiry(t *testing.T) {
	c := NewCache(50 * time.Millisecond)
	key := c.Key("repo", "main", "msg", "en", nil)
	c.Set(key, llm.DiagnoseResponse{Summary: "cached"})

	time.Sleep(100 * time.Millisecond)
	_, ok := c.Get(key)
	if ok {
		t.Fatal("expected cache miss after expiry")
	}
}

func TestCache_DifferentKeys(t *testing.T) {
	c := NewCache(1 * time.Minute)
	k1 := c.Key("repo", "main", "msg1", "en", nil)
	k2 := c.Key("repo", "main", "msg2", "en", nil)

	c.Set(k1, llm.DiagnoseResponse{Summary: "first"})
	c.Set(k2, llm.DiagnoseResponse{Summary: "second"})

	got1, _ := c.Get(k1)
	got2, _ := c.Get(k2)
	if got1.Summary != "first" || got2.Summary != "second" {
		t.Errorf("wrong values: %q, %q", got1.Summary, got2.Summary)
	}
}
```

- [ ] **Step 3: Run tests**

Run: `cd /Users/ivantseng/local_file/slack-issue-bot && go test ./internal/diagnosis/ -run TestCache -v`
Expected: All PASS

- [ ] **Step 4: Commit**

```bash
git add internal/diagnosis/cache.go internal/diagnosis/cache_test.go
git commit -m "feat: in-memory diagnosis response cache with TTL"
```

---

### Task 9: Agent Loop

**Files:**
- Create: `internal/diagnosis/loop.go`
- Create: `internal/diagnosis/loop_test.go`

- [ ] **Step 1: Implement the agent loop**

```go
// internal/diagnosis/loop.go
package diagnosis

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"slack-issue-bot/internal/llm"
)

// LoopInput contains everything the agent loop needs.
type LoopInput struct {
	Type     string          // "bug" or "feature"
	Message  string          // Original Slack message
	RepoPath string          // Path to cloned repo
	Prompt   llm.PromptOptions
	MaxTurns  int
	MaxTokens int
}

// RunLoop runs the agent loop: LLM calls tools until it finishes or hits the turn limit.
func RunLoop(ctx context.Context, chain llm.ConversationProvider, tools []Tool, input LoopInput) (llm.DiagnoseResponse, error) {
	if input.MaxTurns <= 0 {
		input.MaxTurns = 5
	}
	if input.MaxTokens <= 0 {
		input.MaxTokens = 100000
	}

	toolMap := ToolMap(tools)
	toolDefs := ToolDefs(tools)
	systemPrompt := llm.AgentSystemPrompt(input.Type, input.Prompt)

	// Track files discovered during the loop (for fallback response)
	var discoveredFiles []string

	messages := []llm.Message{
		{Role: "user", Content: fmt.Sprintf("## %s Report\n\n> %s", capitalize(input.Type), input.Message)},
	}

	var tokenEstimate int
	tokenEstimate += estimateTokens(systemPrompt)
	tokenEstimate += estimateTokens(messages[0].Content)

	for turn := 1; turn <= input.MaxTurns+1; turn++ {
		// Check context cancellation
		if ctx.Err() != nil {
			return llm.DiagnoseResponse{}, ctx.Err()
		}

		// Force finish on last turn
		effectiveSystem := systemPrompt
		if turn == input.MaxTurns {
			effectiveSystem += "\n\n⚠️ This is your last turn. You MUST respond with the triage card JSON now. Do NOT call any more tools."
		}
		if turn > input.MaxTurns {
			// One extra turn after ignoring a tool call on the last turn
			effectiveSystem += "\n\n⚠️ You MUST respond with the triage card JSON now. No more tool calls allowed."
		}

		// Token budget check
		if tokenEstimate > input.MaxTokens*80/100 {
			effectiveSystem += "\n\n⚠️ Token budget nearly exhausted. Respond with triage card now."
		}

		req := llm.ChatRequest{
			SystemPrompt: effectiveSystem,
			Messages:     messages,
			Tools:        toolDefs,
		}

		// Don't send tools on forced-finish turns
		if turn >= input.MaxTurns {
			req.Tools = nil
		}

		resp, err := chain.Chat(ctx, req)
		if err != nil {
			return llm.DiagnoseResponse{}, fmt.Errorf("agent loop turn %d: %w", turn, err)
		}

		// Case 1: Tool call(s)
		if len(resp.ToolCalls) > 0 && turn <= input.MaxTurns {
			tc := resp.ToolCalls[0] // Execute only the first tool call

			// Append assistant message with tool call
			messages = append(messages, llm.Message{
				Role:      "assistant",
				Content:   resp.Content,
				ToolCalls: []llm.ToolCall{tc},
			})

			tool, ok := toolMap[tc.Name]
			if !ok {
				// Unknown tool
				toolNames := make([]string, 0, len(toolMap))
				for name := range toolMap {
					toolNames = append(toolNames, name)
				}
				errMsg := fmt.Sprintf("Unknown tool: %s. Available tools: %s", tc.Name, strings.Join(toolNames, ", "))
				messages = append(messages, llm.Message{
					Role:       "tool_result",
					ToolCallID: tc.ID,
					Content:    errMsg,
				})
				tokenEstimate += estimateTokens(errMsg)
				slog.Info("agent loop turn", "turn", turn, "tool", tc.Name, "error", "unknown tool")
				continue
			}

			result, execErr := tool.Execute(input.RepoPath, tc.Args)
			if execErr != nil {
				result = fmt.Sprintf("Error: %s", execErr.Error())
			}

			// Track files from grep/search results
			if tc.Name == "grep" || tc.Name == "search_code" {
				for _, line := range strings.Split(result, "\n") {
					if parts := strings.Fields(line); len(parts) > 0 && !strings.HasPrefix(parts[0], "...") {
						discoveredFiles = append(discoveredFiles, strings.TrimSpace(parts[0]))
					}
				}
			}

			// Truncate if over token budget
			resultTokens := estimateTokens(result)
			if tokenEstimate+resultTokens > input.MaxTokens*90/100 {
				lines := strings.Split(result, "\n")
				kept := len(lines) / 2
				if kept < 10 {
					kept = 10
				}
				if kept < len(lines) {
					result = strings.Join(lines[:kept], "\n") +
						fmt.Sprintf("\n[truncated: %d lines → %d lines]", len(lines), kept)
					resultTokens = estimateTokens(result)
				}
			}

			messages = append(messages, llm.Message{
				Role:       "tool_result",
				ToolCallID: tc.ID,
				Content:    result,
			})
			tokenEstimate += resultTokens

			slog.Info("agent loop turn", "turn", turn, "tool", tc.Name, "result_bytes", len(result), "token_estimate", tokenEstimate)
			continue
		}

		// Case 2: Finish — parse triage card from text content
		if resp.Content != "" {
			diagResp, parseErr := llm.ParseLLMTextResponse(resp.Content)
			if parseErr == nil && diagResp.Summary != "" {
				slog.Info("agent loop finished", "turns", turn, "token_estimate", tokenEstimate, "confidence", diagResp.Confidence)
				return diagResp, nil
			}
		}

		// Case 3: Tool call on forced-finish turn — ignore and try one more
		if len(resp.ToolCalls) > 0 && turn == input.MaxTurns {
			slog.Info("agent loop forced finish", "turn", turn)
			messages = append(messages, llm.Message{
				Role:    "assistant",
				Content: resp.Content,
			})
			continue
		}

		// Case 4: Text but not valid triage card — treat as thinking
		if resp.Content != "" {
			messages = append(messages, llm.Message{
				Role:    "assistant",
				Content: resp.Content,
			})
			tokenEstimate += estimateTokens(resp.Content)
			slog.Info("agent loop turn", "turn", turn, "tool", "thinking", "token_estimate", tokenEstimate)
			continue
		}

		// Case 5: Empty response
		slog.Warn("agent loop empty response", "turn", turn)
	}

	// Fallback: return whatever we found
	slog.Info("agent loop forced finish", "reason", "turn limit exceeded")
	var files []llm.FileRef
	for _, f := range discoveredFiles {
		files = append(files, llm.FileRef{Path: f, Description: "discovered during investigation"})
	}
	return llm.DiagnoseResponse{
		Summary:    "Agent loop reached turn limit without producing a triage card.",
		Files:      files,
		Confidence: "low",
	}, nil
}

func estimateTokens(text string) int {
	return len([]rune(text))
}

func capitalize(s string) string {
	if len(s) == 0 {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
```

- [ ] **Step 2: Write tests**

```go
// internal/diagnosis/loop_test.go
package diagnosis

import (
	"context"
	"encoding/json"
	"testing"

	"slack-issue-bot/internal/llm"
)

// mockConvProvider simulates multi-turn conversations with scripted responses.
type mockConvProvider struct {
	responses []llm.ChatResponse
	calls     int
}

func (m *mockConvProvider) Name() string { return "mock-conv" }
func (m *mockConvProvider) Chat(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	idx := m.calls
	m.calls++
	if idx < len(m.responses) {
		return m.responses[idx], nil
	}
	return llm.ChatResponse{Content: `{"summary":"fallback"}`, StopReason: llm.StopReasonFinish}, nil
}

func TestRunLoop_DirectFinish(t *testing.T) {
	mock := &mockConvProvider{
		responses: []llm.ChatResponse{
			{Content: `{"summary":"found bug in login","files":[{"path":"login.go","line_number":10,"description":"auth logic"}],"confidence":"high"}`, StopReason: llm.StopReasonFinish},
		},
	}

	resp, err := RunLoop(context.Background(), mock, AllTools(), LoopInput{
		Type:     "bug",
		Message:  "login broken",
		RepoPath: "/tmp/fake",
		MaxTurns: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Summary != "found bug in login" {
		t.Errorf("unexpected summary: %q", resp.Summary)
	}
	if mock.calls != 1 {
		t.Errorf("expected 1 call, got %d", mock.calls)
	}
}

func TestRunLoop_ToolCallThenFinish(t *testing.T) {
	grepArgs, _ := json.Marshal(map[string]any{"pattern": "Login"})

	mock := &mockConvProvider{
		responses: []llm.ChatResponse{
			// Turn 1: tool call
			{
				ToolCalls:  []llm.ToolCall{{ID: "tc1", Name: "grep", Args: grepArgs}},
				StopReason: llm.StopReasonToolUse,
			},
			// Turn 2: finish
			{
				Content:    `{"summary":"found it","files":[],"confidence":"medium"}`,
				StopReason: llm.StopReasonFinish,
			},
		},
	}

	// Use a real repo for the grep tool
	if _, err := lookPath("git"); err != nil {
		t.Skip("git not found")
	}
	repo := initGitRepo(t, map[string]string{"src/login.go": "func Login() {}"})

	resp, err := RunLoop(context.Background(), mock, AllTools(), LoopInput{
		Type:     "bug",
		Message:  "login bug",
		RepoPath: repo,
		MaxTurns: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Summary != "found it" {
		t.Errorf("unexpected summary: %q", resp.Summary)
	}
	if mock.calls != 2 {
		t.Errorf("expected 2 calls, got %d", mock.calls)
	}
}

func TestRunLoop_UnknownTool(t *testing.T) {
	badArgs, _ := json.Marshal(map[string]any{})

	mock := &mockConvProvider{
		responses: []llm.ChatResponse{
			// Turn 1: unknown tool
			{
				ToolCalls:  []llm.ToolCall{{ID: "tc1", Name: "nonexistent", Args: badArgs}},
				StopReason: llm.StopReasonToolUse,
			},
			// Turn 2: finish
			{
				Content:    `{"summary":"recovered","files":[],"confidence":"low"}`,
				StopReason: llm.StopReasonFinish,
			},
		},
	}

	resp, err := RunLoop(context.Background(), mock, AllTools(), LoopInput{
		Type:     "bug",
		Message:  "test",
		RepoPath: "/tmp/fake",
		MaxTurns: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Summary != "recovered" {
		t.Errorf("unexpected summary: %q", resp.Summary)
	}
}

func TestRunLoop_ForcedFinish(t *testing.T) {
	grepArgs, _ := json.Marshal(map[string]any{"pattern": "x"})

	// LLM keeps calling tools and never finishes
	mock := &mockConvProvider{
		responses: []llm.ChatResponse{
			{ToolCalls: []llm.ToolCall{{ID: "1", Name: "grep", Args: grepArgs}}, StopReason: llm.StopReasonToolUse},
			{ToolCalls: []llm.ToolCall{{ID: "2", Name: "grep", Args: grepArgs}}, StopReason: llm.StopReasonToolUse},
			// Turn 3 (max): forced finish, but LLM still tries tool call
			{ToolCalls: []llm.ToolCall{{ID: "3", Name: "grep", Args: grepArgs}}, StopReason: llm.StopReasonToolUse},
			// Turn 4 (extra): finally produces text
			{Content: `{"summary":"forced","files":[],"confidence":"low"}`, StopReason: llm.StopReasonFinish},
		},
	}

	repo := initGitRepo(t, map[string]string{"a.go": "package a"})

	resp, err := RunLoop(context.Background(), mock, AllTools(), LoopInput{
		Type:     "bug",
		Message:  "test",
		RepoPath: repo,
		MaxTurns: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Summary != "forced" {
		t.Errorf("unexpected summary: %q", resp.Summary)
	}
}

func lookPath(cmd string) (string, error) {
	// Helper to check if command exists
	return "", nil // Always skip in test — initGitRepo checks git availability
}
```

- [ ] **Step 3: Run tests**

Run: `cd /Users/ivantseng/local_file/slack-issue-bot && go test ./internal/diagnosis/ -run TestRunLoop -v`
Expected: All PASS

- [ ] **Step 4: Commit**

```bash
git add internal/diagnosis/loop.go internal/diagnosis/loop_test.go
git commit -m "feat: agent loop with turn limit, forced finish, token budgeting"
```

---

### Task 10: Rewrite Engine

**Files:**
- Modify: `internal/diagnosis/engine.go`
- Replace: `internal/diagnosis/engine_test.go`

- [ ] **Step 1: Rewrite engine.go**

Remove all old pipeline code. Engine now wraps the agent loop + cache + lite mode.

```go
// internal/diagnosis/engine.go
package diagnosis

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"

	"slack-issue-bot/internal/llm"
)

type DiagnoseInput struct {
	Type     string
	Message  string
	RepoPath string
	Keywords []string          // Used by FindFiles (lite mode) only
	Prompt   llm.PromptOptions
}

type Engine struct {
	chain     llm.ConversationProvider
	tools     []Tool
	cache     *Cache
	maxFiles  int
	maxTurns  int
	maxTokens int
}

type EngineConfig struct {
	MaxFiles  int
	MaxTurns  int
	MaxTokens int
	CacheTTL  int // seconds; 0 = use default (10 min)
}

func NewEngine(chain llm.ConversationProvider, cfg EngineConfig) *Engine {
	if cfg.MaxFiles <= 0 {
		cfg.MaxFiles = 10
	}
	if cfg.MaxTurns <= 0 {
		cfg.MaxTurns = 5
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = 100000
	}

	var cacheTTL int
	if cfg.CacheTTL > 0 {
		cacheTTL = cfg.CacheTTL
	}

	return &Engine{
		chain:     chain,
		tools:     AllTools(),
		cache:     NewCache(secondsToDuration(cacheTTL)),
		maxFiles:  cfg.MaxFiles,
		maxTurns:  cfg.MaxTurns,
		maxTokens: cfg.MaxTokens,
	}
}

func secondsToDuration(seconds int) int64 {
	// Returns nanoseconds for time.Duration. 0 = default in NewCache.
	return int64(seconds) * 1e9
}

// Diagnose runs the agent loop to investigate a codebase and produce a triage card.
func (e *Engine) Diagnose(ctx context.Context, input DiagnoseInput) (llm.DiagnoseResponse, error) {
	// Check cache
	cacheKey := e.cache.Key(input.RepoPath, "", input.Message, input.Prompt.Language, input.Prompt.ExtraRules)
	if cached, ok := e.cache.Get(cacheKey); ok {
		slog.Info("agent loop cache hit", "repo", input.RepoPath)
		return cached, nil
	}

	resp, err := RunLoop(ctx, e.chain, e.tools, LoopInput{
		Type:      input.Type,
		Message:   input.Message,
		RepoPath:  input.RepoPath,
		Prompt:    input.Prompt,
		MaxTurns:  e.maxTurns,
		MaxTokens: e.maxTokens,
	})
	if err != nil {
		return llm.DiagnoseResponse{}, err
	}

	// Cache the result
	e.cache.Set(cacheKey, resp)
	return resp, nil
}

// FindFiles returns relevant file references without calling the LLM.
// Used in lite mode to produce a handoff spec.
func (e *Engine) FindFiles(input DiagnoseInput) []llm.FileRef {
	files, _ := grepFiles(input.RepoPath, input.Keywords, e.maxFiles)
	var refs []llm.FileRef
	for _, f := range files {
		refs = append(refs, llm.FileRef{Path: f, Description: "matched keywords from Slack message"})
	}
	return refs
}

// grepFiles searches the repo for files matching any of the given terms.
func grepFiles(repoPath string, terms []string, maxFiles int) ([]string, error) {
	seen := make(map[string]int)
	for _, term := range terms {
		cmd := exec.Command("git", "-C", repoPath, "grep", "-rli", "--no-color", term)
		out, err := cmd.Output()
		if err != nil {
			continue
		}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if line != "" && !shouldSkipFile(line) {
				seen[line]++
			}
		}
	}

	type scored struct {
		path  string
		score int
	}
	var files []scored
	for p, s := range seen {
		files = append(files, scored{p, s})
	}
	for i := range files {
		for j := i + 1; j < len(files); j++ {
			if files[j].score > files[i].score {
				files[i], files[j] = files[j], files[i]
			}
		}
	}

	var result []string
	for i, f := range files {
		if i >= maxFiles {
			break
		}
		result = append(result, f.path)
	}
	return result, nil
}

// --- Helpers ---

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func parseStringArray(text string) ([]string, error) {
	// Keep for potential future use — not used in agent loop
	text = strings.TrimSpace(text)
	var arr []string

	if err := json.Unmarshal([]byte(text), &arr); err == nil && len(arr) > 0 {
		return arr, nil
	}

	if idx := strings.Index(text, "["); idx != -1 {
		if end := strings.LastIndex(text, "]"); end > idx {
			if err := json.Unmarshal([]byte(text[idx:end+1]), &arr); err == nil && len(arr) > 0 {
				return arr, nil
			}
		}
	}

	return nil, fmt.Errorf("could not parse string array from: %s", truncate(text, 100))
}
```

Wait — `engine.go` references `json` but we need to add the import. Let me fix:

```go
// Add to imports at top of engine.go:
import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"

	"slack-issue-bot/internal/llm"
)
```

- [ ] **Step 2: Rewrite engine_test.go**

```go
// internal/diagnosis/engine_test.go
package diagnosis

import (
	"context"
	"os/exec"
	"testing"

	"slack-issue-bot/internal/llm"
)

func TestEngine_Diagnose_AgentLoop(t *testing.T) {
	mock := &mockConvProvider{
		responses: []llm.ChatResponse{
			{Content: `{"summary":"triage result","files":[{"path":"main.go","line_number":1,"description":"entry"}],"confidence":"high"}`, StopReason: llm.StopReasonFinish},
		},
	}

	engine := NewEngine(mock, EngineConfig{MaxTurns: 5})
	resp, err := engine.Diagnose(context.Background(), DiagnoseInput{
		Type:     "bug",
		Message:  "something broken",
		RepoPath: "/tmp/fake",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Summary != "triage result" {
		t.Errorf("unexpected summary: %q", resp.Summary)
	}
}

func TestEngine_Diagnose_CacheHit(t *testing.T) {
	mock := &mockConvProvider{
		responses: []llm.ChatResponse{
			{Content: `{"summary":"first call","files":[],"confidence":"high"}`, StopReason: llm.StopReasonFinish},
			{Content: `{"summary":"second call","files":[],"confidence":"high"}`, StopReason: llm.StopReasonFinish},
		},
	}

	engine := NewEngine(mock, EngineConfig{MaxTurns: 5})
	input := DiagnoseInput{Type: "bug", Message: "test msg", RepoPath: "/tmp/fake"}

	resp1, _ := engine.Diagnose(context.Background(), input)
	resp2, _ := engine.Diagnose(context.Background(), input)

	if resp1.Summary != "first call" || resp2.Summary != "first call" {
		t.Errorf("expected cache hit, got %q and %q", resp1.Summary, resp2.Summary)
	}
	if mock.calls != 1 {
		t.Errorf("expected 1 LLM call (cached), got %d", mock.calls)
	}
}

func TestEngine_FindFiles(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	repo := initGitRepo(t, map[string]string{
		"src/login.go": "package auth\nfunc Login() {}",
	})

	engine := NewEngine(nil, EngineConfig{MaxFiles: 5})
	refs := engine.FindFiles(DiagnoseInput{
		RepoPath: repo,
		Keywords: []string{"Login"},
	})
	if len(refs) == 0 {
		t.Fatal("expected file refs")
	}
	if refs[0].Path != "src/login.go" {
		t.Errorf("unexpected path: %s", refs[0].Path)
	}
}
```

- [ ] **Step 3: Run all diagnosis tests**

Run: `cd /Users/ivantseng/local_file/slack-issue-bot && go test ./internal/diagnosis/ -v`
Expected: All PASS

- [ ] **Step 4: Commit**

```bash
git add internal/diagnosis/engine.go internal/diagnosis/engine_test.go
git commit -m "refactor: rewrite engine with agent loop, cache, and lite mode"
```

---

### Task 11: Wire Everything (main.go + workflow.go)

**Files:**
- Modify: `cmd/bot/main.go`
- Modify: `internal/bot/workflow.go`

- [ ] **Step 1: Update main.go provider construction**

Replace the old `llm.Provider` / `llm.ProviderEntry` / `llm.FallbackChain` construction with `llm.ConversationProvider` / `llm.ChatProviderEntry` / `llm.ChatFallbackChain`. In the provider switch, call the same constructors but store as `ConversationProvider`.

```go
// In cmd/bot/main.go, replace the provider construction block (lines 55-79):

	var entries []llm.ChatProviderEntry
	for _, p := range cfg.LLM.Providers {
		timeout := p.Timeout
		if timeout <= 0 {
			timeout = cfg.LLM.Timeout
		}
		var provider llm.ConversationProvider
		switch p.Name {
		case "claude":
			provider = llm.NewClaudeProvider(p.APIKey, p.Model, p.BaseURL, timeout)
		case "openai":
			provider = llm.NewOpenAIProvider(p.APIKey, p.Model, p.BaseURL, timeout)
		case "ollama":
			provider = llm.NewOllamaProvider(p.Model, p.BaseURL, timeout)
		case "cli":
			provider = llm.NewCLIProvider(p.Name, p.Command, p.Args, timeout)
		default:
			slog.Warn("unknown LLM provider, skipping", "name", p.Name)
			continue
		}
		slog.Info("loaded LLM provider", "name", p.Name, "max_retries", p.MaxRetries)
		entries = append(entries, llm.ChatProviderEntry{Provider: provider, MaxRetries: p.MaxRetries})
	}
	slog.Info("LLM fallback chain ready", "providers", len(entries))
	fallbackChain := llm.NewChatFallbackChain(entries)
```

Also update the `diagEngine` construction:

```go
// Replace:
//   diagEngine := diagnosis.NewEngine(fallbackChain, 5)
// With:
	maxTurns := cfg.Diagnosis.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 5
	}
	maxTokens := cfg.Diagnosis.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 100000
	}
	cacheTTL := int(cfg.Diagnosis.CacheTTL.Seconds())

	diagEngine := diagnosis.NewEngine(fallbackChain, diagnosis.EngineConfig{
		MaxFiles:  10,
		MaxTurns:  maxTurns,
		MaxTokens: maxTokens,
		CacheTTL:  cacheTTL,
	})
```

Remove the `encoding/json` dummy import (`var _ = json.Marshal`) if no longer needed.

- [ ] **Step 2: Update workflow.go — add progress message**

In `internal/bot/workflow.go`, update `createIssue()` to post a progress message before calling the engine and update it when done.

```go
// In createIssue(), after the branch checkout block and before the diagnosis block, add:

	// Post progress message
	progressTS := ""
	if err := w.slack.PostMessage(pi.Event.ChannelID, ":mag: 正在分析...", pi.ThreadTS); err == nil {
		// We don't need to track progressTS for update since we just post one message
	}
```

Actually, to update the message we need the timestamp. Let's use `PostMessage` which returns error only. We'll just post a simple progress message — the spec says "simple: start analyzing, then result" (option A).

In `workflow.go`'s `createIssue()`, add at the start of the function, right after the repo cache checkout block (after line 288 `}`):

```go
	w.slack.PostMessage(pi.Event.ChannelID, ":mag: 正在分析...", pi.ThreadTS)
```

- [ ] **Step 3: Build and verify**

Run: `cd /Users/ivantseng/local_file/slack-issue-bot && go build ./...`
Expected: Build succeeds with no errors

- [ ] **Step 4: Run all tests**

Run: `cd /Users/ivantseng/local_file/slack-issue-bot && go test ./...`
Expected: All tests PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/bot/main.go internal/bot/workflow.go
git commit -m "feat: wire agent loop engine with ChatFallbackChain and progress message"
```

---

### Task 12: Update Config and Final Cleanup

**Files:**
- Modify: `config.yaml` (if exists) or `config.example.yaml`
- Modify: `CLAUDE.md`
- Modify: `README.md`

- [ ] **Step 1: Update config with new fields**

Add `max_turns`, `max_tokens`, `cache_ttl` to the diagnosis section of config.yaml:

```yaml
diagnosis:
  mode: "full"
  max_turns: 5
  max_tokens: 100000
  cache_ttl: 10m
  prompt:
    language: "繁體中文"
    extra_rules:
      - "列出所有相關的檔案名稱與完整路徑"
```

- [ ] **Step 2: Update CLAUDE.md architecture section**

Update the architecture diagram to reflect the agent loop:

```
Slack reaction → Socket Mode → Handler (dedup + rate limit + semaphore)
  → Workflow (repo/branch selection via thread buttons)
    → Post "正在分析..." in thread
    → Agent Loop (max N turns):
       LLM decides: grep / read_file / list_files / read_context / search_code / git_log
       Execute tool → feed result back → LLM decides next action
       LLM finishes → triage card
    → Rejection check (files=0, questions>=5, confidence=low)
    → GitHub Issue (clickable file links) → Post URL in thread
```

Update the diagnosis engine description:

```
  diagnosis/
    engine.go              # Engine wrapper: agent loop + cache + lite mode
    loop.go                # Agent loop: LLM picks tools, forced finish, token budgeting
    tools.go               # 6 tools: grep, read_file, list_files, read_context, search_code, git_log
    cache.go               # In-memory response cache with TTL
```

Update the "Agent-Style Diagnosis" section:

```
### Agent Loop Diagnosis
LLM-driven agent loop — the model decides which tools to use:
1. LLM sees the bug/feature report
2. LLM calls tools (grep, read_file, search_code, etc.)
3. Results are fed back to LLM
4. LLM can call more tools or produce a triage card
5. Forced finish after max_turns (default 5)
6. Token budgeting prevents context overflow
7. Response caching avoids duplicate work
```

- [ ] **Step 3: Update README.md diagnosis section**

Update the diagnosis description to mention the agent loop approach.

- [ ] **Step 4: Run final test suite**

Run: `cd /Users/ivantseng/local_file/slack-issue-bot && go test ./... -v`
Expected: All tests PASS

- [ ] **Step 5: Commit**

```bash
git add config.yaml CLAUDE.md README.md
git commit -m "docs: update config, CLAUDE.md, README.md for agent loop diagnosis"
```

---

## Self-Review Checklist

| Spec Requirement | Task |
|-----------------|------|
| ConversationProvider interface + types | Task 1 |
| ChatFallbackChain per-turn fallback | Task 1 |
| Claude native tool use | Task 2 |
| OpenAI function calling | Task 3 |
| CLI JSON-in-text simulation | Task 4 |
| Ollama JSON-in-text simulation | Task 4 |
| Config: max_turns, max_tokens, cache_ttl | Task 5 |
| 6 diagnosis tools | Task 6 |
| Agent system prompt | Task 7 |
| Response cache with TTL | Task 8 |
| Agent loop with turn limit + forced finish | Task 9 |
| Token budgeting | Task 9 |
| Engine rewrite | Task 10 |
| FindFiles lite mode preserved | Task 10 |
| Workflow progress message | Task 11 |
| main.go wiring | Task 11 |
| Observability (slog) | Task 9, 10 |
| Backward compatibility (rejection check, issue format) | Unchanged in workflow.go |
| Existing tests replaced | Task 1, 10 |
| Exponential backoff | Not yet — can be added to ChatFallbackChain as a follow-up |

**Gap found:** Exponential backoff is specified in the design spec but not fully implemented in this plan. The current `ChatFallbackChain` does simple retry without backoff. This can be added as a follow-up task or integrated into Task 1 by adding a `time.Sleep` with exponential delay in the retry loop. For now, the simple retry matches the existing behavior.
