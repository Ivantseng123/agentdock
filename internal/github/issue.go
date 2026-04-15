package github

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"agentdock/internal/metrics"

	gh "github.com/google/go-github/v60/github"
)

// IssueClient creates GitHub issues.
type IssueClient struct {
	client *gh.Client
	logger *slog.Logger
}

// NewIssueClient creates a new GitHub issue client.
func NewIssueClient(token string, logger *slog.Logger) *IssueClient {
	return &IssueClient{
		client: gh.NewClient(nil).WithAuthToken(token),
		logger: logger,
	}
}

// CreateIssue creates a GitHub issue with the given title, body, and labels.
// Returns the issue HTML URL.
func (ic *IssueClient) CreateIssue(ctx context.Context, owner, repo, title, body string, labels []string) (string, error) {
	start := time.Now()
	req := &gh.IssueRequest{
		Title:  gh.String(title),
		Body:   gh.String(body),
		Labels: &labels,
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
