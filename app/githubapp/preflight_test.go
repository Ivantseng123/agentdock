package githubapp

import (
	"crypto/rsa"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newPreflightSource(t *testing.T, srv *httptest.Server, key *rsa.PrivateKey) *appInstallationSource {
	t.Helper()
	return &appInstallationSource{
		appID:          1234,
		installationID: 5678,
		privateKey:     key,
		httpClient:     srv.Client(),
		baseURL:        srv.URL,
		logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		now:            func() time.Time { return time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC) },
	}
}

// fastDelays overrides preflightRetryDelays for the duration of a test
// so the suite isn't dragging out 3.5s on every retry case.
func fastDelays(t *testing.T) {
	t.Helper()
	prev := preflightRetryDelays
	preflightRetryDelays = []time.Duration{1 * time.Millisecond, 2 * time.Millisecond, 4 * time.Millisecond}
	t.Cleanup(func() { preflightRetryDelays = prev })
}

func happyHandler(t *testing.T, fullPermissions bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/access_tokens"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			expires := time.Date(2026, 5, 2, 13, 0, 0, 0, time.UTC).Format(time.RFC3339)
			_, _ = w.Write([]byte(fmt.Sprintf(`{"token":"ghs_pf","expires_at":%q}`, expires)))
		case strings.Contains(r.URL.Path, "/installation/repositories"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"repositories":[{"full_name":"org/repo"}]}`))
		case strings.HasSuffix(r.URL.Path, fmt.Sprintf("/app/installations/%d", 5678)):
			perms := `"issues":"write","contents":"read","metadata":"read","pull_requests":"write"`
			if !fullPermissions {
				perms = `"issues":"read","contents":"read","metadata":"read"`
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"permissions":{` + perms + `}}`))
		default:
			t.Errorf("unexpected request path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func TestPreflightApp_HappyPath(t *testing.T) {
	fastDelays(t)
	srv := httptest.NewServer(happyHandler(t, true))
	defer srv.Close()

	src := newPreflightSource(t, srv, generateTestKey(t))
	if err := preflightAppWithSource(src); err != nil {
		t.Fatalf("preflight: %v", err)
	}
}

func TestPreflightApp_MissingPermissions(t *testing.T) {
	fastDelays(t)
	srv := httptest.NewServer(happyHandler(t, false))
	defer srv.Close()

	src := newPreflightSource(t, srv, generateTestKey(t))
	err := preflightAppWithSource(src)
	if err == nil {
		t.Fatal("expected error on missing permissions")
	}
	if !strings.Contains(err.Error(), "missing required permissions") {
		t.Errorf("error = %v, want one mentioning missing permissions", err)
	}
	// issues=read does not satisfy required write
	if !strings.Contains(err.Error(), "issues") {
		t.Errorf("error should list 'issues' as missing; got %v", err)
	}
	if !strings.Contains(err.Error(), "pull_requests") {
		t.Errorf("error should list 'pull_requests' as missing; got %v", err)
	}
}

func TestPreflightApp_Mint401_CredentialsRejected(t *testing.T) {
	fastDelays(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	defer srv.Close()

	src := newPreflightSource(t, srv, generateTestKey(t))
	err := preflightAppWithSource(src)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "credentials rejected") {
		t.Errorf("error = %v, want 'credentials rejected'", err)
	}
}

func TestPreflightApp_Mint404_InstallationNotFound(t *testing.T) {
	fastDelays(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	}))
	defer srv.Close()

	src := newPreflightSource(t, srv, generateTestKey(t))
	err := preflightAppWithSource(src)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "installation not found") {
		t.Errorf("error = %v, want 'installation not found'", err)
	}
	if !strings.Contains(err.Error(), "5678") {
		t.Errorf("error should include installation_id 5678; got %v", err)
	}
}

func TestPreflightApp_Mint5xxExhausted_InfrastructureError(t *testing.T) {
	fastDelays(t)
	hits := atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	src := newPreflightSource(t, srv, generateTestKey(t))
	err := preflightAppWithSource(src)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "infrastructure") {
		t.Errorf("error = %v, want 'infrastructure issue'", err)
	}
	// 1 initial + 3 retries = 4 calls
	if got := hits.Load(); got != 4 {
		t.Errorf("hits = %d, want 4 (initial + 3 retries)", got)
	}
}

func TestPreflightApp_Mint5xxThenSuccess_RetryWorks(t *testing.T) {
	fastDelays(t)
	hits := atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/access_tokens"):
			n := hits.Add(1)
			if n == 1 {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusCreated)
			expires := time.Date(2026, 5, 2, 13, 0, 0, 0, time.UTC).Format(time.RFC3339)
			_, _ = w.Write([]byte(fmt.Sprintf(`{"token":"ghs_after_retry","expires_at":%q}`, expires)))
		case strings.Contains(r.URL.Path, "/installation/repositories"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"repositories":[]}`))
		case strings.Contains(r.URL.Path, "/app/installations/5678"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"permissions":{"issues":"write","contents":"read","metadata":"read","pull_requests":"write"}}`))
		}
	}))
	defer srv.Close()

	src := newPreflightSource(t, srv, generateTestKey(t))
	if err := preflightAppWithSource(src); err != nil {
		t.Fatalf("preflight should succeed after retry: %v", err)
	}
}

func TestPermissionSatisfies(t *testing.T) {
	cases := []struct {
		actual, required string
		want             bool
	}{
		{"write", "write", true},
		{"write", "read", true},
		{"admin", "write", true},
		{"read", "write", false},
		{"read", "read", true},
		{"", "read", false},
		{"none", "read", false},
	}
	for _, tc := range cases {
		if got := permissionSatisfies(tc.actual, tc.required); got != tc.want {
			t.Errorf("permissionSatisfies(%q, %q) = %v, want %v", tc.actual, tc.required, got, tc.want)
		}
	}
}
