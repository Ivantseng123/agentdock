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

// ProviderEntry wraps a Provider with its per-provider retry count.
type ProviderEntry struct {
	Provider   Provider
	MaxRetries int
}

type FallbackChain struct {
	entries []ProviderEntry
}

func NewFallbackChain(entries []ProviderEntry) *FallbackChain {
	for i := range entries {
		if entries[i].MaxRetries <= 0 {
			entries[i].MaxRetries = 1
		}
	}
	return &FallbackChain{entries: entries}
}

func (fc *FallbackChain) Name() string { return "fallback-chain" }

func (fc *FallbackChain) Diagnose(ctx context.Context, req DiagnoseRequest) (DiagnoseResponse, error) {
	var errs []string
	for _, e := range fc.entries {
		for attempt := 1; attempt <= e.MaxRetries; attempt++ {
			resp, err := e.Provider.Diagnose(ctx, req)
			if err == nil {
				return resp, nil
			}
			slog.Warn("LLM provider failed",
				"provider", e.Provider.Name(),
				"attempt", fmt.Sprintf("%d/%d", attempt, e.MaxRetries),
				"error", err,
			)
			errs = append(errs, fmt.Sprintf("%s (attempt %d/%d): %s", e.Provider.Name(), attempt, e.MaxRetries, err))
		}
		slog.Warn("provider exhausted retries, moving to next", "provider", e.Provider.Name())
	}
	return DiagnoseResponse{}, fmt.Errorf("all LLM providers failed: %s", strings.Join(errs, "; "))
}
