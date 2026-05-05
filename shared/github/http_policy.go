package github

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

type TransportProfile string

const (
	ProfileInteractive TransportProfile = "interactive"
	ProfileBackground  TransportProfile = "background"
	ProfilePreflight   TransportProfile = "preflight"
)

var ErrGitHubUnavailable = errors.New("github unavailable")

type transportProfile struct {
	perAttemptTimeout time.Duration
	maxWallTime       time.Duration
	retryDelays       []time.Duration
}

func profileConfig(profile TransportProfile) transportProfile {
	switch profile {
	case ProfileBackground:
		return transportProfile{
			perAttemptTimeout: 15 * time.Second,
			maxWallTime:       45 * time.Second,
			retryDelays:       []time.Duration{500 * time.Millisecond, 1 * time.Second, 2 * time.Second},
		}
	case ProfilePreflight:
		return transportProfile{
			perAttemptTimeout: 15 * time.Second,
			maxWallTime:       45 * time.Second,
			retryDelays:       []time.Duration{500 * time.Millisecond, 1 * time.Second, 2 * time.Second},
		}
	default:
		return transportProfile{
			perAttemptTimeout: 12 * time.Second,
			maxWallTime:       30 * time.Second,
			retryDelays:       []time.Duration{500 * time.Millisecond, 1 * time.Second, 2 * time.Second},
		}
	}
}

func NewHTTPClient(profile TransportProfile) *http.Client {
	return NewHTTPClientWithTokenFn(nil, profile)
}

func NewHTTPClientWithTokenFn(tokenFn func() (string, error), profile TransportProfile) *http.Client {
	var delegate http.RoundTripper = http.DefaultTransport
	if tokenFn != nil {
		delegate = newTokenTransport(tokenFn, delegate)
	}
	return &http.Client{
		Transport: newRetryTransport(profileConfig(profile), delegate),
	}
}

type retryTransport struct {
	profile  transportProfile
	delegate http.RoundTripper
}

func newRetryTransport(profile transportProfile, delegate http.RoundTripper) *retryTransport {
	if delegate == nil {
		delegate = http.DefaultTransport
	}
	return &retryTransport{profile: profile, delegate: delegate}
}

func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	deadline := time.Now().Add(t.profile.maxWallTime)
	if d, ok := req.Context().Deadline(); ok && d.Before(deadline) {
		deadline = d
	}

	attempts := len(t.profile.retryDelays) + 1
	var lastErr error
	var lastStatus int

	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			delay := t.profile.retryDelays[attempt-1]
			if err := sleepWithinDeadline(req.Context(), deadline, delay); err != nil {
				return nil, transientError(lastStatus, attempt, lastErr)
			}
		}

		attemptReq, cancel, err := cloneRequestForAttempt(req, deadline, t.profile.perAttemptTimeout, attempt > 0)
		if err != nil {
			return nil, err
		}

		resp, roundTripErr := t.delegate.RoundTrip(attemptReq)
		if roundTripErr != nil {
			cancel()
			if errors.Is(roundTripErr, context.Canceled) && !errors.Is(req.Context().Err(), context.DeadlineExceeded) {
				return nil, roundTripErr
			}
			lastErr = roundTripErr
			if !isRetryableTransportError(roundTripErr) || attempt == attempts-1 {
				if isRetryableTransportError(roundTripErr) {
					return nil, transientError(lastStatus, attempt+1, lastErr)
				}
				return nil, roundTripErr
			}
			continue
		}

		retryable, statusCode, bodyBytes := isRetryableGitHubResponse(resp)
		if !retryable || !requestCanRetry(req) {
			resp.Body = &cancelOnCloseReadCloser{ReadCloser: resp.Body, cancel: cancel}
			return resp, nil
		}

		lastStatus = statusCode
		lastErr = fmt.Errorf("status %d", statusCode)
		_ = resp.Body.Close()
		cancel()

		if attempt == attempts-1 {
			_ = bodyBytes
			return nil, transientError(lastStatus, attempt+1, lastErr)
		}
	}

	return nil, transientError(lastStatus, attempts, lastErr)
}

type cancelOnCloseReadCloser struct {
	io.ReadCloser
	cancel func()
}

func (c *cancelOnCloseReadCloser) Close() error {
	err := c.ReadCloser.Close()
	if c.cancel != nil {
		c.cancel()
	}
	return err
}

func cloneRequestForAttempt(req *http.Request, deadline time.Time, perAttemptTimeout time.Duration, resetBody bool) (*http.Request, func(), error) {
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return nil, nil, context.DeadlineExceeded
	}
	timeout := perAttemptTimeout
	if timeout <= 0 || timeout > remaining {
		timeout = remaining
	}
	ctx, cancel := context.WithTimeout(req.Context(), timeout)
	cloned := req.Clone(ctx)
	if resetBody && req.GetBody != nil {
		body, err := req.GetBody()
		if err != nil {
			cancel()
			return nil, nil, fmt.Errorf("reset request body: %w", err)
		}
		cloned.Body = body
	}
	return cloned, cancel, nil
}

func requestCanRetry(req *http.Request) bool {
	return req.Body == nil || req.GetBody != nil
}

func sleepWithinDeadline(ctx context.Context, deadline time.Time, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	if time.Now().Add(delay).After(deadline) {
		return context.DeadlineExceeded
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func isRetryableTransportError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "deadline exceeded") ||
		strings.Contains(msg, "dial tcp") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "unexpected eof")
}

func isRetryableGitHubResponse(resp *http.Response) (bool, int, []byte) {
	switch resp.StatusCode {
	case http.StatusTooManyRequests:
		return true, resp.StatusCode, nil
	case http.StatusForbidden:
		bodyBytes, _ := io.ReadAll(resp.Body)
		resp.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		body := strings.ToLower(string(bodyBytes))
		return strings.Contains(body, "secondary rate limit") || strings.Contains(body, "abuse detection"), resp.StatusCode, bodyBytes
	default:
		if resp.StatusCode >= 500 {
			return true, resp.StatusCode, nil
		}
		return false, resp.StatusCode, nil
	}
}

type githubTransientError struct {
	statusCode int
	attempts   int
	cause      error
}

func transientError(statusCode, attempts int, cause error) error {
	return &githubTransientError{statusCode: statusCode, attempts: attempts, cause: cause}
}

func (e *githubTransientError) Error() string {
	if e == nil {
		return ErrGitHubUnavailable.Error()
	}
	if e.statusCode != 0 {
		return fmt.Sprintf("github unavailable after %d attempts: status=%d", e.attempts, e.statusCode)
	}
	if e.cause != nil {
		return fmt.Sprintf("github unavailable after %d attempts: %v", e.attempts, e.cause)
	}
	return fmt.Sprintf("github unavailable after %d attempts", e.attempts)
}

func (e *githubTransientError) Unwrap() error { return e.cause }

func (e *githubTransientError) Is(target error) bool {
	return target == ErrGitHubUnavailable
}
