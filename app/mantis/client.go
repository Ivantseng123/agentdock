package mantis

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Client fetches issue summary from a Mantis instance.
type Client struct {
	baseURL  string
	apiToken string
	username string
	password string
	http     *http.Client
}

func NewClient(baseURL, apiToken, username, password string) *Client {
	return &Client{
		baseURL:  strings.TrimRight(baseURL, "/"),
		apiToken: apiToken,
		username: username,
		password: password,
		http:     &http.Client{Timeout: 10 * time.Second},
	}
}

// IsConfigured returns true if the client has enough config to make requests.
func (c *Client) IsConfigured() bool {
	return c.baseURL != "" && (c.apiToken != "" || (c.username != "" && c.password != ""))
}

// issueResponse is the Mantis REST API response for a single issue.
type issueResponse struct {
	Issues []struct {
		ID          int    `json:"id"`
		Summary     string `json:"summary"`
		Description string `json:"description"`
	} `json:"issues"`
}

// FetchIssueSummary fetches the title + description of a Mantis issue by ID.
func (c *Client) FetchIssueSummary(issueID string) (title, description string, err error) {
	url := fmt.Sprintf("%s/api/rest/issues/%s", c.baseURL, issueID)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", "", fmt.Errorf("create request: %w", err)
	}

	if c.apiToken != "" {
		req.Header.Set("Authorization", c.apiToken)
	} else if c.username != "" {
		req.SetBasicAuth(c.username, c.password)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("fetch mantis issue: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("mantis returned %d: %s", resp.StatusCode, string(body))
	}

	var result issueResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return "", "", fmt.Errorf("parse response: %w", err)
	}
	if len(result.Issues) == 0 {
		return "", "", fmt.Errorf("mantis issue %s not found", issueID)
	}

	issue := result.Issues[0]
	// Cap description at 200 lines
	desc := issue.Description
	lines := strings.Split(desc, "\n")
	if len(lines) > 200 {
		desc = strings.Join(lines[:200], "\n") + "\n... [truncated]"
	}

	return issue.Summary, desc, nil
}

// ExtractIssueID tries to extract a Mantis issue ID from a URL.
// Matches patterns like: /view.php?id=12345 or /issues/12345
func ExtractIssueID(url string) string {
	// /view.php?id=12345
	re1 := regexp.MustCompile(`view\.php\?id=(\d+)`)
	if m := re1.FindStringSubmatch(url); len(m) > 1 {
		return m[1]
	}
	// /issues/12345
	re2 := regexp.MustCompile(`/issues/(\d+)`)
	if m := re2.FindStringSubmatch(url); len(m) > 1 {
		return m[1]
	}
	return ""
}

// IsMantisURL checks if a URL belongs to the configured Mantis instance.
func (c *Client) IsMantisURL(url string) bool {
	return c.baseURL != "" && strings.HasPrefix(url, c.baseURL)
}
