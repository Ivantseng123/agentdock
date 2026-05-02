package github

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/Ivantseng123/agentdock/shared/metrics"

	gh "github.com/google/go-github/v60/github"
)

// RepoDiscovery lists repos accessible by the GitHub token, with caching.
type RepoDiscovery struct {
	client *gh.Client

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
	httpClient := &http.Client{Transport: newTokenTransport(tokenFn, nil)}
	return &RepoDiscovery{
		client: gh.NewClient(httpClient),
		ttl:    5 * time.Minute,
		logger: logger,
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

	var matched []string
	for _, r := range all {
		if containsIgnoreCase(r, query) {
			matched = append(matched, r)
			if len(matched) >= 25 {
				break
			}
		}
	}
	return matched, nil
}

func containsIgnoreCase(s, substr string) bool {
	sLower := make([]byte, len(s))
	subLower := make([]byte, len(substr))
	for i := range s {
		if s[i] >= 'A' && s[i] <= 'Z' {
			sLower[i] = s[i] + 32
		} else {
			sLower[i] = s[i]
		}
	}
	for i := range substr {
		if substr[i] >= 'A' && substr[i] <= 'Z' {
			subLower[i] = substr[i] + 32
		} else {
			subLower[i] = substr[i]
		}
	}
	return bytesContains(sLower, subLower)
}

func bytesContains(s, sub []byte) bool {
	if len(sub) == 0 {
		return true
	}
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i] == sub[0] {
			match := true
			for j := 1; j < len(sub); j++ {
				if s[i+j] != sub[j] {
					match = false
					break
				}
			}
			if match {
				return true
			}
		}
	}
	return false
}
