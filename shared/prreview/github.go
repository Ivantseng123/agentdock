package prreview

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const DefaultMaxWallTime = 30 * time.Second

const RetryAfterCap = 10 * time.Second

var fallbackDelays = []time.Duration{0, 2 * time.Second, 4 * time.Second}

// httpCallWithRetry executes req with GitHub-aware retry:
//   - 429 → retry (honor Retry-After header; cap at RetryAfterCap; fallback 2s/4s)
//   - 403 with secondary-rate-limit body → retry same way
//   - 5xx transient → retry
//   - Network error → retry
//   - Anything else → return response (caller handles)
//
// Max 3 attempts. Overall wall time bounded by maxWallTime.
// Request body must be re-readable between attempts — callers should use
// bytes.Buffer / bytes.Reader or set req.GetBody.
func httpCallWithRetry(ctx context.Context, req *http.Request, maxWallTime time.Duration) (*http.Response, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	deadline := time.Now().Add(maxWallTime)

	var lastResp *http.Response
	var lastErr error

	for attempt := 0; attempt < len(fallbackDelays); attempt++ {
		if attempt > 0 {
			wait := fallbackDelays[attempt]
			if lastResp != nil {
				if ra := parseRetryAfter(lastResp.Header); ra > 0 {
					wait = minDuration(ra, RetryAfterCap)
				}
			}
			if time.Now().Add(wait).After(deadline) {
				return nil, fmt.Errorf("%s: %w", ErrGitHubWallTime, lastErr)
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
		}

		if attempt > 0 && req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				return nil, fmt.Errorf("reset body: %w", err)
			}
			req.Body = body
		}

		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			lastResp = nil
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}

		if !shouldRetry(resp) {
			return resp, nil
		}

		bodyBytes, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		lastResp = resp
		lastErr = fmt.Errorf("status %d", resp.StatusCode)
	}

	return nil, fmt.Errorf("%s: %w", ErrGitHubRateLimit, lastErr)
}

func shouldRetry(resp *http.Response) bool {
	switch resp.StatusCode {
	case 429:
		return true
	case 502, 503, 504:
		return true
	case 403:
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(body))
		low := strings.ToLower(string(body))
		return strings.Contains(low, "secondary rate limit") ||
			strings.Contains(low, "abuse detection")
	}
	return false
}

func parseRetryAfter(h http.Header) time.Duration {
	v := h.Get("Retry-After")
	if v == "" {
		return 0
	}
	if n, err := strconv.Atoi(v); err == nil {
		return time.Duration(n) * time.Second
	}
	return 0
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// PRFile is the minimal shape we need from GET /pulls/:n/files.
type PRFile struct {
	Filename string `json:"filename"`
	Status   string `json:"status"`
	Patch    string `json:"patch"`
}

var prURLPattern = regexp.MustCompile(`github\.com/([^/]+)/([^/]+)/pull/(\d+)`)

func parsePRURL(s string) (owner, repo string, number int, err error) {
	m := prURLPattern.FindStringSubmatch(s)
	if m == nil {
		return "", "", 0, fmt.Errorf("not a github.com PR URL: %q", s)
	}
	n, err := strconv.Atoi(m[3])
	if err != nil {
		return "", "", 0, fmt.Errorf("invalid PR number in %q: %w", s, err)
	}
	return m[1], m[2], n, nil
}

// listDiffFiles fetches GET /repos/{owner}/{repo}/pulls/{n}/files with
// pagination. apiBase lets tests point at an httptest.Server.URL.
func listDiffFiles(ctx context.Context, apiBase, prURL, token string, maxWallTime time.Duration) ([]PRFile, error) {
	owner, repo, num, err := parsePRURL(prURL)
	if err != nil {
		return nil, err
	}

	var all []PRFile
	page := 1
	for {
		u := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/files?per_page=100&page=%d",
			strings.TrimRight(apiBase, "/"), url.PathEscape(owner), url.PathEscape(repo), num, page)
		req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}

		resp, err := httpCallWithRetry(ctx, req, maxWallTime)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode == 401 {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("%s", ErrGitHubUnauth)
		}
		if resp.StatusCode == 403 {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("%s", ErrGitHubForbidden)
		}
		if resp.StatusCode == 404 {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("%s", ErrGitHubNotFound)
		}
		if resp.StatusCode >= 400 {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			return nil, fmt.Errorf("list files: %d %s", resp.StatusCode, string(body))
		}

		var page1 []PRFile
		if err := json.NewDecoder(resp.Body).Decode(&page1); err != nil {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("decode list files: %w", err)
		}
		_ = resp.Body.Close()

		all = append(all, page1...)
		if len(page1) < 100 {
			break
		}
		page++
		if page > 20 {
			break
		}
	}
	return all, nil
}

// validLines holds the (line, side) tuples that may host a comment on one file.
type validLines struct {
	set map[string]bool
}

func newValidLines() *validLines { return &validLines{set: map[string]bool{}} }

func (v *validLines) add(line int, side string) {
	v.set[fmt.Sprintf("%d:%s", line, side)] = true
}

func (v *validLines) has(line int, side string) bool {
	return v.set[fmt.Sprintf("%d:%s", line, side)]
}

func parseDiffMap(files []PRFile) map[string]*validLines {
	out := map[string]*validLines{}
	for _, f := range files {
		out[f.Filename] = parsePatch(f.Patch)
	}
	return out
}

var hunkHeaderPattern = regexp.MustCompile(`^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

func parsePatch(patch string) *validLines {
	v := newValidLines()
	lines := strings.Split(patch, "\n")
	var leftLine, rightLine int
	for _, ln := range lines {
		if m := hunkHeaderPattern.FindStringSubmatch(ln); m != nil {
			leftLine, _ = strconv.Atoi(m[1])
			rightLine, _ = strconv.Atoi(m[2])
			continue
		}
		if ln == "" {
			continue
		}
		switch ln[0] {
		case '+':
			v.add(rightLine, string(SideRight))
			rightLine++
		case '-':
			v.add(leftLine, string(SideLeft))
			leftLine++
		case ' ':
			v.add(rightLine, string(SideRight))
			leftLine++
			rightLine++
		}
	}
	return v
}
