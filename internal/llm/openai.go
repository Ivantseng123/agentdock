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

func (o *OpenAIProvider) Diagnose(ctx context.Context, req DiagnoseRequest) (DiagnoseResponse, error) {
	prompt := BuildPrompt(req.Type, req.Message, req.RepoFiles)

	body := map[string]any{
		"model": o.model,
		"messages": []map[string]string{
			{"role": "system", "content": SystemPrompt(req.Type)},
			{"role": "user", "content": prompt},
		},
		"max_tokens": 2048,
	}
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return DiagnoseResponse{}, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", o.baseURL+"/v1/chat/completions", bytes.NewReader(jsonBody))
	if err != nil {
		return DiagnoseResponse{}, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+o.apiKey)

	resp, err := o.client.Do(httpReq)
	if err != nil {
		return DiagnoseResponse{}, fmt.Errorf("call openai: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return DiagnoseResponse{}, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return DiagnoseResponse{}, fmt.Errorf("openai returned %d: %s", resp.StatusCode, string(respBody))
	}

	var raw struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &raw); err != nil {
		return DiagnoseResponse{}, fmt.Errorf("parse response: %w", err)
	}
	if len(raw.Choices) == 0 {
		return DiagnoseResponse{}, fmt.Errorf("empty response from openai")
	}

	return ParseLLMTextResponse(raw.Choices[0].Message.Content)
}
