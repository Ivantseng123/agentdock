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

func TestParseDiffMap_AddedAndContextLines(t *testing.T) {
	files := []PRFile{
		{
			Filename: "foo.go",
			Patch: "@@ -5,3 +5,4 @@\n" +
				" context\n" +
				"-old\n" +
				"+added1\n" +
				"+added2\n",
		},
	}
	m := parseDiffMap(files)
	valid := m["foo.go"]

	for _, ln := range []int{5, 6, 7} {
		if !valid.has(ln, string(SideRight)) {
			t.Errorf("want (line=%d, RIGHT) to be valid", ln)
		}
	}
	if !valid.has(6, string(SideLeft)) {
		t.Errorf("want (line=6, LEFT) valid for removed line")
	}
}

func TestParseDiffMap_MultipleHunks(t *testing.T) {
	files := []PRFile{
		{
			Filename: "bar.py",
			Patch: "@@ -1,2 +1,3 @@\n" +
				" a\n" +
				"+b\n" +
				" c\n" +
				"@@ -10,1 +11,2 @@\n" +
				"+z\n" +
				" y\n",
		},
	}
	m := parseDiffMap(files)
	valid := m["bar.py"]

	for _, ln := range []int{1, 2, 3} {
		if !valid.has(ln, string(SideRight)) {
			t.Errorf("hunk1: want (line=%d, RIGHT) valid", ln)
		}
	}
	for _, ln := range []int{11, 12} {
		if !valid.has(ln, string(SideRight)) {
			t.Errorf("hunk2: want (line=%d, RIGHT) valid", ln)
		}
	}
}

func TestParseDiffMap_EmptyPatch(t *testing.T) {
	files := []PRFile{{Filename: "binary.png", Patch: ""}}
	m := parseDiffMap(files)
	if v, ok := m["binary.png"]; !ok || v == nil {
		t.Fatalf("want empty valid-set entry for %q, got %+v", "binary.png", m)
	}
	if len(m["binary.png"].set) != 0 {
		t.Errorf("want 0 valid lines for empty patch, got %d", len(m["binary.png"].set))
	}
}
