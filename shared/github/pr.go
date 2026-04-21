package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Client is a thin GitHub REST client for endpoints that need precise HTTP
// error text (e.g. PR fetch, where PRReviewWorkflow routes on substrings like
// "404" / "403" / "dial" / "timeout"). Distinct from IssueClient (which wraps
// go-github for issue creation) because go-github swallows raw status strings.
//
// Extend this by adding endpoint-specific methods in sibling files; keep
// IssueClient for go-github-backed issue flows.
type Client struct {
	token string
	http  *http.Client
}

// NewClient builds a Client. token is the GitHub PAT used for Authorization;
// pass an empty string for unauthenticated requests (rate-limited).
func NewClient(token string) *Client {
	return &Client{
		token: token,
		http:  http.DefaultClient,
	}
}

// GetPullRequest fetches a PR via REST. Returns an error containing the HTTP
// status code string so callers can classify (404/403/dial/timeout) via
// substring matching — intentionally unstructured because PRReviewWorkflow's
// mapGitHubErrorToSlack already routes on those substrings.
func (c *Client) GetPullRequest(ctx context.Context, owner, repo string, number int) (*PullRequest, error) {
	u := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls/%d", owner, repo, number)
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("%d %s", resp.StatusCode, string(body))
	}
	var pr PullRequest
	if err := json.Unmarshal(body, &pr); err != nil {
		return nil, fmt.Errorf("unmarshal pr: %w", err)
	}
	return &pr, nil
}
