package llm

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

type File struct {
	Path    string
	Content string
}

type FileRef struct {
	Path        string
	LineNumber  int
	Description string
}

type DiagnoseRequest struct {
	Type      string // "bug" or "feature"
	Message   string // Original Slack message
	RepoFiles []File // Relevant code files
}

type DiagnoseResponse struct {
	Summary     string
	Files       []FileRef
	Suggestions []string
	Complexity  string // "low", "medium", "high" — for feature issues
}

type Provider interface {
	Name() string
	Diagnose(ctx context.Context, req DiagnoseRequest) (DiagnoseResponse, error)
}

type FallbackChain struct {
	providers  []Provider
	maxRetries int
}

func NewFallbackChain(providers []Provider, maxRetries int) *FallbackChain {
	if maxRetries <= 0 {
		maxRetries = 1
	}
	return &FallbackChain{providers: providers, maxRetries: maxRetries}
}

func (fc *FallbackChain) Diagnose(ctx context.Context, req DiagnoseRequest) (DiagnoseResponse, error) {
	var errs []string
	for _, p := range fc.providers {
		for attempt := 1; attempt <= fc.maxRetries; attempt++ {
			resp, err := p.Diagnose(ctx, req)
			if err == nil {
				return resp, nil
			}
			slog.Warn("LLM provider failed", "provider", p.Name(), "attempt", attempt, "error", err)
			errs = append(errs, fmt.Sprintf("%s (attempt %d): %s", p.Name(), attempt, err))
		}
	}
	return DiagnoseResponse{}, fmt.Errorf("all LLM providers failed: %s", strings.Join(errs, "; "))
}
