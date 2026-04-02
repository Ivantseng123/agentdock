package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
)

// Existing types — kept unchanged.

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
	Complexity    string // "low", "medium", "high" — for feature issues
	OpenQuestions []string
	Confidence    string // "low", "medium", "high"
}

// New conversation-based types for agent-loop diagnosis.

const (
	StopReasonToolUse = "tool_use"
	StopReasonFinish  = "finish"
)

type ToolCall struct {
	ID   string
	Name string
	Args json.RawMessage
}

type Message struct {
	Role       string     // "assistant", "user", "tool_result"
	Content    string
	ToolCalls  []ToolCall
	ToolCallID string // For tool_result messages
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

// ChatProviderEntry wraps a ConversationProvider with its per-provider retry count.
type ChatProviderEntry struct {
	Provider   ConversationProvider
	MaxRetries int
}

// ChatFallbackChain tries each provider in order, retrying up to MaxRetries
// before falling back to the next. It also satisfies ConversationProvider.
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
		slog.Warn("chat provider exhausted retries, moving to next", "provider", e.Provider.Name())
	}
	return ChatResponse{}, fmt.Errorf("all chat providers failed: %s", strings.Join(errs, "; "))
}
