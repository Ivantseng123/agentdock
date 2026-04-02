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

func (c *ClaudeProvider) Diagnose(ctx context.Context, req DiagnoseRequest) (DiagnoseResponse, error) {
	prompt := BuildPrompt(req.Type, req.Message, req.RepoFiles)

	body := map[string]any{
		"model":      c.model,
		"max_tokens": 2048,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"system": SystemPrompt(req.Type),
	}
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return DiagnoseResponse{}, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/v1/messages", bytes.NewReader(jsonBody))
	if err != nil {
		return DiagnoseResponse{}, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return DiagnoseResponse{}, fmt.Errorf("call claude: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return DiagnoseResponse{}, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return DiagnoseResponse{}, fmt.Errorf("claude returned %d: %s", resp.StatusCode, string(respBody))
	}

	return ParseLLMResponse(respBody)
}

// ParseLLMResponse parses a Claude Messages API response into DiagnoseResponse.
// Exported so OpenAI/Ollama can reuse the JSON extraction logic via ParseLLMTextResponse.
func ParseLLMResponse(body []byte) (DiagnoseResponse, error) {
	var raw struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return DiagnoseResponse{}, fmt.Errorf("parse response: %w", err)
	}
	if len(raw.Content) == 0 {
		return DiagnoseResponse{}, fmt.Errorf("empty response from claude")
	}

	return ParseLLMTextResponse(raw.Content[0].Text)
}

// ParseLLMTextResponse extracts structured diagnosis from LLM text output.
func ParseLLMTextResponse(text string) (DiagnoseResponse, error) {
	var structured struct {
		Summary    string `json:"summary"`
		Files      []struct {
			Path        string `json:"path"`
			LineNumber  int    `json:"line_number"`
			Description string `json:"description"`
		} `json:"files"`
		Suggestions []string `json:"suggestions"`
		Complexity  string   `json:"complexity"`
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
		Summary:     structured.Summary,
		Files:       files,
		Suggestions: structured.Suggestions,
		Complexity:  structured.Complexity,
	}, nil
}
