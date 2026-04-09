package github

import (
	"context"
	"fmt"

	gh "github.com/google/go-github/v60/github"
)

// IssueClient creates GitHub issues.
type IssueClient struct {
	client *gh.Client
}

// NewIssueClient creates a new GitHub issue client.
func NewIssueClient(token string) *IssueClient {
	return &IssueClient{
		client: gh.NewClient(nil).WithAuthToken(token),
	}
}

// CreateIssue creates a GitHub issue with the given title, body, and labels.
// Returns the issue HTML URL.
func (ic *IssueClient) CreateIssue(ctx context.Context, owner, repo, title, body string, labels []string) (string, error) {
	req := &gh.IssueRequest{
		Title:  gh.String(title),
		Body:   gh.String(body),
		Labels: &labels,
	}

	issue, _, err := ic.client.Issues.Create(ctx, owner, repo, req)
	if err != nil {
		return "", fmt.Errorf("create issue: %w", err)
	}

	return issue.GetHTMLURL(), nil
}
