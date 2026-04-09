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
	// Build messages array for Ollama chat API.
	var msgs []map[string]string

	// System prompt as system role message.
	// Ollama doesn't support native tool use — embed tool schemas in the system prompt.
	if req.SystemPrompt != "" {
		systemContent := req.SystemPrompt
		if len(req.Tools) > 0 {
			systemContent += CLIToolPromptSuffix(req.Tools)
		}
		msgs = append(msgs, map[string]string{
			"role":    "system",
			"content": systemContent,
		})
	}

	for _, m := range req.Messages {
		switch m.Role {
		case "user":
			content := m.Content
			if len(m.Images) > 0 {
				content += imageFallbackText(m.Images)
			}
			msgs = append(msgs, map[string]string{
				"role":    "user",
				"content": content,
			})
		case "assistant":
			// Flatten assistant tool calls into text content.
			content := m.Content
			if len(m.ToolCalls) > 0 {
				for _, tc := range m.ToolCalls {
					tcJSON, _ := json.Marshal(map[string]any{
						"tool": tc.Name,
						"args": json.RawMessage(tc.Args),
					})
					if content != "" {
						content += "\n"
					}
					content += string(tcJSON)
				}
			}
			msgs = append(msgs, map[string]string{
				"role":    "assistant",
				"content": content,
			})
		case "tool_result":
			// Tool results as user role messages with prefix.
			msgs = append(msgs, map[string]string{
				"role":    "user",
				"content": "Tool Result:\n" + m.Content,
			})
		}
	}

	body := map[string]any{
		"model":    o.model,
		"stream":   false,
		"messages": msgs,
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

	return parseJSONInTextResponse(raw.Message.Content)
}
