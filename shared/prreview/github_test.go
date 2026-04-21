package prreview

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHTTPCallRetry_SuccessFirstTry(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	resp, err := httpCallWithRetry(context.Background(), req, 5*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
	if hits != 1 {
		t.Errorf("want 1 hit, got %d", hits)
	}
}

func TestHTTPCallRetry_429ThenSuccess(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		if n == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(429)
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	resp, err := httpCallWithRetry(context.Background(), req, 10*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
	if hits != 2 {
		t.Errorf("want 2 hits, got %d", hits)
	}
}

func TestHTTPCallRetry_429ExhaustsAttempts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(429)
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	_, err := httpCallWithRetry(context.Background(), req, 30*time.Second)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), ErrGitHubRateLimit) {
		t.Errorf("error should mention rate-limit exhaustion, got %v", err)
	}
}

func TestHTTPCallRetry_WallTimeExceeded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "5")
		w.WriteHeader(429)
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	_, err := httpCallWithRetry(context.Background(), req, 100*time.Millisecond)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), ErrGitHubWallTime) {
		t.Errorf("want wall time error, got %v", err)
	}
}

func TestHTTPCallRetry_403NonRateLimitDoesNotRetry(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(403)
		_, _ = io.WriteString(w, `{"message":"unrelated"}`)
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	resp, err := httpCallWithRetry(context.Background(), req, 10*time.Second)
	if err != nil {
		t.Fatalf("want non-error response, got %v", err)
	}
	resp.Body.Close()
	if hits != 1 {
		t.Errorf("403 non-rate-limit should not retry, got %d hits", hits)
	}
}

func TestHTTPCallRetry_403RateLimitRetries(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		if n == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(403)
			_, _ = io.WriteString(w, `{"message":"You have exceeded a secondary rate limit."}`)
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	resp, err := httpCallWithRetry(context.Background(), req, 10*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
	if hits != 2 {
		t.Errorf("want 2 hits, got %d", hits)
	}
}
