package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Ivantseng123/agentdock/shared/metrics"

	gh "github.com/google/go-github/v60/github"
)

// RepoDiscovery lists repos accessible by the GitHub token, with caching.
type RepoDiscovery struct {
	client       *gh.Client
	httpClient   *http.Client
	tokenFn      func() (string, error)
	baseURL      string
	installation bool

	mu      sync.Mutex
	cache   []string
	fetched time.Time
	ttl     time.Duration
	logger  *slog.Logger
}

// NewRepoDiscovery builds a discovery client. tokenFn is invoked per
// outbound request via tokenTransport so the underlying gh.Client can
// keep up with installation-token rotation without rebuilding.
func NewRepoDiscovery(tokenFn func() (string, error), logger *slog.Logger) *RepoDiscovery {
	httpClient := NewHTTPClientWithTokenFn(tokenFn, ProfileBackground)
	return &RepoDiscovery{
		client: gh.NewClient(httpClient),
		ttl:    5 * time.Minute,
		logger: logger,
	}
}

// NewInstallationRepoDiscovery builds a discovery client for GitHub App
// installation tokens. Installation tokens must use /installation/repositories,
// not /user/repos.
func NewInstallationRepoDiscovery(tokenFn func() (string, error), logger *slog.Logger) *RepoDiscovery {
	return newInstallationRepoDiscoveryWithBaseURL(tokenFn, logger, "https://api.github.com")
}

func newInstallationRepoDiscoveryWithBaseURL(tokenFn func() (string, error), logger *slog.Logger, baseURL string) *RepoDiscovery {
	return &RepoDiscovery{
		httpClient:   NewHTTPClient(ProfileBackground),
		tokenFn:      tokenFn,
		baseURL:      strings.TrimRight(baseURL, "/"),
		installation: true,
		ttl:          5 * time.Minute,
		logger:       logger,
	}
}

// ListRepos returns all repos the token can access (cached).
func (d *RepoDiscovery) ListRepos(ctx context.Context) ([]string, error) {
	d.mu.Lock()
	if d.cache != nil && time.Since(d.fetched) < d.ttl {
		result := d.cache
		d.mu.Unlock()
		return result, nil
	}
	d.mu.Unlock()

	start := time.Now()
	if d.installation {
		allRepos, err := d.listInstallationRepos(ctx)
		if err != nil {
			metrics.ExternalErrorsTotal.WithLabelValues("github", "list_repos").Inc()
			return nil, err
		}
		metrics.ExternalDuration.WithLabelValues("github", "list_repos").Observe(time.Since(start).Seconds())
		d.logger.Info("探索到 GitHub repos", "phase", "完成", "count", len(allRepos))

		d.mu.Lock()
		d.cache = allRepos
		d.fetched = time.Now()
		d.mu.Unlock()

		return allRepos, nil
	}

	var allRepos []string
	opts := &gh.RepositoryListOptions{
		Sort:        "updated",
		Direction:   "desc",
		ListOptions: gh.ListOptions{PerPage: 100},
	}

	for {
		repos, resp, err := d.client.Repositories.List(ctx, "", opts)
		if err != nil {
			metrics.ExternalErrorsTotal.WithLabelValues("github", "list_repos").Inc()
			return nil, err
		}
		for _, r := range repos {
			allRepos = append(allRepos, r.GetFullName())
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	metrics.ExternalDuration.WithLabelValues("github", "list_repos").Observe(time.Since(start).Seconds())
	d.logger.Info("探索到 GitHub repos", "phase", "完成", "count", len(allRepos))

	d.mu.Lock()
	d.cache = allRepos
	d.fetched = time.Now()
	d.mu.Unlock()

	return allRepos, nil
}

type installationRepoDiscoveryResponse struct {
	Repositories []struct {
		FullName string `json:"full_name"`
	} `json:"repositories"`
}

func (d *RepoDiscovery) listInstallationRepos(ctx context.Context) ([]string, error) {
	var allRepos []string
	page := 1
	for {
		token, err := d.tokenFn()
		if err != nil {
			return nil, fmt.Errorf("token source: %w", err)
		}
		url := fmt.Sprintf("%s/installation/repositories?per_page=100&page=%d", d.baseURL, page)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, fmt.Errorf("build installation repo request: %w", err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

		resp, err := d.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("installation repo request: %w", err)
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			bodyExcerpt := strings.TrimSpace(string(body))
			if token != "" {
				bodyExcerpt = strings.ReplaceAll(bodyExcerpt, token, "***")
			}
			return nil, fmt.Errorf("installation repo list status=%d body=%s", resp.StatusCode, bodyExcerpt)
		}

		var parsed installationRepoDiscoveryResponse
		if err := json.Unmarshal(body, &parsed); err != nil {
			return nil, fmt.Errorf("decode installation repo list: %w", err)
		}
		for _, repo := range parsed.Repositories {
			allRepos = append(allRepos, repo.FullName)
		}
		if len(parsed.Repositories) < 100 {
			break
		}
		page++
	}
	return allRepos, nil
}

// SearchRepos filters cached repos by query string.
func (d *RepoDiscovery) SearchRepos(ctx context.Context, query string) ([]string, error) {
	all, err := d.ListRepos(ctx)
	if err != nil {
		return nil, err
	}

	if query == "" {
		// Return first 25 repos when no query
		if len(all) > 25 {
			return all[:25], nil
		}
		return all, nil
	}

	q := strings.ToLower(query)
	var matched []string
	for _, r := range all {
		if strings.Contains(strings.ToLower(r), q) {
			matched = append(matched, r)
			if len(matched) >= 25 {
				break
			}
		}
	}
	return matched, nil
}
