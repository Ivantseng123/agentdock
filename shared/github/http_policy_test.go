package github

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestRetryTransport_RetriesTransient5xx(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`ok`))
	}))
	defer srv.Close()

	client := &http.Client{
		Transport: newRetryTransport(transportProfile{
			perAttemptTimeout: 200 * time.Millisecond,
			maxWallTime:       500 * time.Millisecond,
			retryDelays:       []time.Duration{1 * time.Millisecond, 1 * time.Millisecond},
		}, http.DefaultTransport),
	}

	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Fatalf("body = %q, want ok", string(body))
	}
	if got := hits.Load(); got != 3 {
		t.Fatalf("hits = %d, want 3", got)
	}
}

func TestRetryTransport_ExhaustsAsGitHubUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGatewayTimeout)
	}))
	defer srv.Close()

	client := &http.Client{
		Transport: newRetryTransport(transportProfile{
			perAttemptTimeout: 200 * time.Millisecond,
			maxWallTime:       500 * time.Millisecond,
			retryDelays:       []time.Duration{1 * time.Millisecond, 1 * time.Millisecond},
		}, http.DefaultTransport),
	}

	resp, err := client.Get(srv.URL)
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrGitHubUnavailable) {
		t.Fatalf("err = %v, want ErrGitHubUnavailable", err)
	}
}

func TestProfileConfig_InteractivePinsLongerThanLegacy10s(t *testing.T) {
	cfg := profileConfig(ProfileInteractive)
	if cfg.perAttemptTimeout <= 10*time.Second {
		t.Fatalf("interactive per-attempt timeout = %v, want > 10s", cfg.perAttemptTimeout)
	}
	if cfg.maxWallTime != 30*time.Second {
		t.Fatalf("interactive maxWallTime = %v, want 30s", cfg.maxWallTime)
	}
}
