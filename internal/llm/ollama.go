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

func (o *OllamaProvider) Diagnose(ctx context.Context, req DiagnoseRequest) (DiagnoseResponse, error) {
	prompt := BuildPrompt(req.Type, req.Message, req.RepoFiles)

	body := map[string]any{
		"model":  o.model,
		"stream": false,
		"messages": []map[string]string{
			{"role": "system", "content": SystemPrompt(req.Type)},
			{"role": "user", "content": prompt},
		},
	}
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return DiagnoseResponse{}, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", o.baseURL+"/api/chat", bytes.NewReader(jsonBody))
	if err != nil {
		return DiagnoseResponse{}, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := o.client.Do(httpReq)
	if err != nil {
		return DiagnoseResponse{}, fmt.Errorf("call ollama: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return DiagnoseResponse{}, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return DiagnoseResponse{}, fmt.Errorf("ollama returned %d: %s", resp.StatusCode, string(respBody))
	}

	var raw struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(respBody, &raw); err != nil {
		return DiagnoseResponse{}, fmt.Errorf("parse response: %w", err)
	}

	return ParseLLMTextResponse(raw.Message.Content)
}
