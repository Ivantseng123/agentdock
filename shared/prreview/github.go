package prreview

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
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
