package llm

import (
	"bytes"
	"context"
	"encoding/base64"
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

func buildOpenAIUserContent(m Message) any {
	if len(m.Images) == 0 {
		return m.Content
	}

	var blocks []map[string]any
	for _, img := range m.Images {
		dataURI := fmt.Sprintf("data:%s;base64,%s", img.MimeType, base64.StdEncoding.EncodeToString(img.Data))
		blocks = append(blocks, map[string]any{
			"type": "image_url",
			"image_url": map[string]string{
				"url": dataURI,
			},
		})
	}
	blocks = append(blocks, map[string]any{
		"type": "text",
		"text": m.Content,
	})
	return blocks
}

func (o *OpenAIProvider) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	// Build messages array for OpenAI Chat Completions API.
	var msgs []map[string]any

	// System prompt goes as a system message.
	if req.SystemPrompt != "" {
		msgs = append(msgs, map[string]any{
			"role":    "system",
			"content": req.SystemPrompt,
		})
	}

	for _, m := range req.Messages {
		switch m.Role {
		case "user":
			msgs = append(msgs, map[string]any{
				"role":    "user",
				"content": buildOpenAIUserContent(m),
			})
		case "assistant":
			msg := map[string]any{
				"role": "assistant",
			}
			// OpenAI content can be null when only tool_calls are present.
			if m.Content != "" {
				msg["content"] = m.Content
			}
			if len(m.ToolCalls) > 0 {
				var tcs []map[string]any
				for _, tc := range m.ToolCalls {
					tcs = append(tcs, map[string]any{
						"id":   tc.ID,
						"type": "function",
						"function": map[string]any{
							"name":      tc.Name,
							"arguments": string(tc.Args),
						},
					})
				}
				msg["tool_calls"] = tcs
			}
			msgs = append(msgs, msg)
		case "tool_result":
			msgs = append(msgs, map[string]any{
				"role":         "tool",
				"tool_call_id": m.ToolCallID,
				"content":      m.Content,
			})
		}
	}

	// Build tools array.
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

	body := map[string]any{
		"model":      o.model,
		"messages":   msgs,
		"max_tokens": 4096,
	}
	if len(tools) > 0 {
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

	// Parse response.
	var raw struct {
		Choices []struct {
			Message struct {
				Content   *string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
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
	var result ChatResponse

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

	if choice.FinishReason == "tool_calls" {
		result.StopReason = StopReasonToolUse
	} else {
		result.StopReason = StopReasonFinish
	}

	return result, nil
}
