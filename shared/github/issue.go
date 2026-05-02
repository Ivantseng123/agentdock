package github

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/Ivantseng123/agentdock/shared/metrics"

	gh "github.com/google/go-github/v60/github"
)

// IssueClient creates GitHub issues.
type IssueClient struct {
	client *gh.Client
	logger *slog.Logger
}

// NewIssueClient creates a new GitHub issue client. tokenFn is invoked
// per outbound request via tokenTransport so the underlying gh.Client
// can keep up with installation-token rotation without rebuilding.
func NewIssueClient(tokenFn func() (string, error), logger *slog.Logger) *IssueClient {
	httpClient := &http.Client{Transport: newTokenTransport(tokenFn, nil)}
	return &IssueClient{
		client: gh.NewClient(httpClient),
		logger: logger,
	}
}

// CreateIssue creates a GitHub issue with the given title, body, and labels.
// Returns the issue HTML URL.
func (ic *IssueClient) CreateIssue(ctx context.Context, owner, repo, title, body string, labels []string) (string, error) {
	start := time.Now()
	normalized := normalizeLabels(labels)
	req := &gh.IssueRequest{
		Title:  gh.String(title),
		Body:   gh.String(body),
		Labels: &normalized,
	}

	issue, _, err := ic.client.Issues.Create(ctx, owner, repo, req)
	metrics.ExternalDuration.WithLabelValues("github", "create_issue").Observe(time.Since(start).Seconds())
	if err != nil {
		metrics.ExternalErrorsTotal.WithLabelValues("github", "create_issue").Inc()
		ic.logger.Error("Issue 建立失敗", "phase", "失敗", "owner", owner, "repo", repo, "error", err)
		return "", fmt.Errorf("create issue: %w", err)
	}

	ic.logger.Info("Issue 建立成功", "phase", "完成", "owner", owner, "repo", repo, "url", issue.GetHTMLURL(), "duration_ms", time.Since(start).Milliseconds())
	return issue.GetHTMLURL(), nil
}

// normalizeLabels ensures the value sent to GitHub is a non-nil slice.
// go-github marshals a nil []string into JSON null, which GitHub rejects with
// "For 'properties/labels', nil is not an array." (422).
func normalizeLabels(labels []string) []string {
	if labels == nil {
		return []string{}
	}
	return labels
}
