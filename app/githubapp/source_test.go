package githubapp

import (
	"crypto/rsa"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newTestSource(t *testing.T, srv *httptest.Server, key *rsa.PrivateKey, nowFn func() time.Time) *appInstallationSource {
	t.Helper()
	if nowFn == nil {
		fixed := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
		nowFn = func() time.Time { return fixed }
	}
	return &appInstallationSource{
		appID:          1234,
		installationID: 5678,
		privateKey:     key,
		httpClient:     srv.Client(),
		baseURL:        srv.URL,
		logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		now:            nowFn,
	}
}

// mintHandler returns a handler that responds to mint requests with
// sequential tokens (token-1, token-2, ...) and expiry = now + 60min,
// counting only mint hits. Other endpoints (notably
// /installation/repositories called from mintLocked) are answered with
// an empty list and do not count toward the mint counter.
func mintHandler(now func() time.Time, hits *atomic.Int32) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/access_tokens") {
			n := hits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			expires := now().Add(60 * time.Minute).UTC().Format(time.RFC3339)
			body := fmt.Sprintf(`{"token":"token-%d","expires_at":%q}`, n, expires)
			_, _ = w.Write([]byte(body))
			return
		}
		// /installation/repositories — return empty list so mintLocked's
		// accessibleRepos refresh succeeds without affecting the counter.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"repositories":[]}`))
	}
}

func TestStaticPATSource_BothMethodsReturnSameToken(t *testing.T) {
	s := &staticPATSource{token: "ghp_static"}
	got1, err := s.Get()
	if err != nil || got1 != "ghp_static" {
		t.Errorf("Get() = (%q, %v)", got1, err)
	}
	got2, err := s.MintFresh()
	if err != nil || got2 != "ghp_static" {
		t.Errorf("MintFresh() = (%q, %v)", got2, err)
	}
}

func TestAppInstallationSource_GetMintsThenCaches(t *testing.T) {
	hits := &atomic.Int32{}
	now := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(mintHandler(func() time.Time { return now }, hits))
	defer srv.Close()

	src := newTestSource(t, srv, generateTestKey(t), func() time.Time { return now })

	tok1, err := src.Get()
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}
	if tok1 != "token-1" {
		t.Errorf("first Get token = %q, want token-1", tok1)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("after first Get: hits = %d, want 1", got)
	}

	tok2, err := src.Get()
	if err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if tok2 != "token-1" {
		t.Errorf("second Get token = %q, want cached token-1", tok2)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("after second Get: hits = %d, want 1 (cache hit, no extra mint)", got)
	}
}

func TestAppInstallationSource_GetReMintsWhenCacheNearExpiry(t *testing.T) {
	hits := &atomic.Int32{}
	currentNow := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
	nowFn := func() time.Time { return currentNow }

	srv := httptest.NewServer(mintHandler(nowFn, hits))
	defer srv.Close()

	src := newTestSource(t, srv, generateTestKey(t), nowFn)

	if _, err := src.Get(); err != nil {
		t.Fatalf("first Get: %v", err)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("hits after first Get = %d", got)
	}

	// Advance time so cache has < 50min remaining (token expires at +60min,
	// move now forward 11min → 49min remaining → must re-mint).
	currentNow = currentNow.Add(11 * time.Minute)

	tok, err := src.Get()
	if err != nil {
		t.Fatalf("Get after expiry: %v", err)
	}
	if tok != "token-2" {
		t.Errorf("got %q, want token-2 (re-mint)", tok)
	}
	if got := hits.Load(); got != 2 {
		t.Errorf("hits = %d, want 2", got)
	}
}

func TestAppInstallationSource_MintFreshBypassesCacheAndUpdates(t *testing.T) {
	hits := &atomic.Int32{}
	now := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(mintHandler(func() time.Time { return now }, hits))
	defer srv.Close()

	src := newTestSource(t, srv, generateTestKey(t), func() time.Time { return now })

	if _, err := src.Get(); err != nil {
		t.Fatalf("seed Get: %v", err)
	}

	tok, err := src.MintFresh()
	if err != nil {
		t.Fatalf("MintFresh: %v", err)
	}
	if tok != "token-2" {
		t.Errorf("MintFresh token = %q, want token-2", tok)
	}
	if got := hits.Load(); got != 2 {
		t.Errorf("hits = %d, want 2 (MintFresh bypasses cache)", got)
	}

	// Subsequent Get should now hit the cache populated by MintFresh.
	tok2, err := src.Get()
	if err != nil {
		t.Fatalf("post-MintFresh Get: %v", err)
	}
	if tok2 != "token-2" {
		t.Errorf("post-MintFresh Get = %q, want cached token-2", tok2)
	}
	if got := hits.Load(); got != 2 {
		t.Errorf("hits = %d, want still 2", got)
	}
}

func TestAppInstallationSource_ConcurrentGetMintFreshNoRace(t *testing.T) {
	hits := &atomic.Int32{}
	now := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(mintHandler(func() time.Time { return now }, hits))
	defer srv.Close()

	src := newTestSource(t, srv, generateTestKey(t), func() time.Time { return now })

	const goroutines = 32
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := range goroutines {
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				if _, err := src.Get(); err != nil {
					t.Errorf("goroutine %d Get: %v", i, err)
				}
			} else {
				if _, err := src.MintFresh(); err != nil {
					t.Errorf("goroutine %d MintFresh: %v", i, err)
				}
			}
		}(i)
	}
	wg.Wait()
}

// TestAppInstallationSource_ConcurrentMintFresh_SerializedNoRace forces
// every goroutine to actually contend on mintLocked (rather than mostly
// hitting the cache) so the mutex is exercised under -race. All 16
// goroutines call MintFresh, which always bypasses the cache.
func TestAppInstallationSource_ConcurrentMintFresh_SerializedNoRace(t *testing.T) {
	hits := &atomic.Int32{}
	now := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(mintHandler(func() time.Time { return now }, hits))
	defer srv.Close()

	src := newTestSource(t, srv, generateTestKey(t), func() time.Time { return now })

	const goroutines = 16
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			if _, err := src.MintFresh(); err != nil {
				t.Errorf("MintFresh: %v", err)
			}
		}()
	}
	wg.Wait()

	// All MintFresh calls must have actually hit the mint endpoint —
	// proving the mutex serialized contended mintLocked rather than
	// short-circuiting through the cache.
	if got := hits.Load(); got != int32(goroutines) {
		t.Errorf("hits = %d, want %d (every MintFresh must reach mint endpoint)", got, goroutines)
	}
}

func TestStaticPATSource_IsAccessibleAlwaysTrue(t *testing.T) {
	s := &staticPATSource{token: "p"}
	if !s.IsAccessible("any/repo") {
		t.Error("staticPATSource.IsAccessible should always return true")
	}
}

func TestAppInstallationSource_IsAccessibleNilSetReturnsTrue(t *testing.T) {
	src := &appInstallationSource{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if !src.IsAccessible("acme/repo") {
		t.Error("nil accessibleRepos should not block dispatch — defer to worker-side error")
	}
}

func TestAppInstallationSource_IsAccessiblePopulatedAfterMint(t *testing.T) {
	now := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/access_tokens") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			expires := now.Add(60 * time.Minute).UTC().Format(time.RFC3339)
			_, _ = w.Write([]byte(fmt.Sprintf(`{"token":"ghs_x","expires_at":%q}`, expires)))
			return
		}
		// /installation/repositories — return two repos on first page.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"repositories":[{"full_name":"Acme/Service"},{"full_name":"acme/library"}]}`))
	}))
	defer srv.Close()

	src := newTestSource(t, srv, generateTestKey(t), func() time.Time { return now })

	if _, err := src.Get(); err != nil {
		t.Fatalf("Get: %v", err)
	}

	if !src.IsAccessible("acme/service") {
		t.Error("expected acme/service in accessibleRepos (case-insensitive match)")
	}
	if !src.IsAccessible("ACME/LIBRARY") {
		t.Error("expected ACME/LIBRARY in accessibleRepos (case-insensitive match)")
	}
	if src.IsAccessible("other/repo") {
		t.Error("other/repo should not be accessible")
	}
}

func TestListInstallationRepos_Pagination(t *testing.T) {
	page := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		switch page {
		case 1:
			// Build a 100-item first page so pagination is forced.
			items := make([]string, 100)
			for i := range items {
				items[i] = fmt.Sprintf(`{"full_name":"org/repo-%d"}`, i)
			}
			_, _ = w.Write([]byte(`{"repositories":[` + strings.Join(items, ",") + `]}`))
		case 2:
			_, _ = w.Write([]byte(`{"repositories":[{"full_name":"org/last"}]}`))
		default:
			t.Errorf("unexpected page %d", page)
		}
	}))
	defer srv.Close()

	repos, err := listInstallationRepos(srv.Client(), srv.URL, "tok")
	if err != nil {
		t.Fatalf("listInstallationRepos: %v", err)
	}
	if len(repos) != 101 {
		t.Errorf("got %d repos, want 101 (100 + 1)", len(repos))
	}
	if _, ok := repos["org/last"]; !ok {
		t.Error("missing org/last from second page")
	}
	if page != 2 {
		t.Errorf("expected 2 page fetches, got %d", page)
	}
}

func TestListInstallationRepos_NonOKReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"resource not accessible by integration"}`))
	}))
	defer srv.Close()
	_, err := listInstallationRepos(srv.Client(), srv.URL, "tok")
	if err == nil {
		t.Fatal("expected error on 403")
	}
}

func TestAppInstallationSource_ListReposFails_KeepsLastGoodSet(t *testing.T) {
	now := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
	listShouldFail := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/access_tokens") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			expires := now.Add(60 * time.Minute).UTC().Format(time.RFC3339)
			_, _ = w.Write([]byte(fmt.Sprintf(`{"token":"ghs_x","expires_at":%q}`, expires)))
			return
		}
		if listShouldFail {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"repositories":[{"full_name":"acme/svc"}]}`))
	}))
	defer srv.Close()

	currentNow := now
	src := newTestSource(t, srv, generateTestKey(t), func() time.Time { return currentNow })

	// First mint: list-repos succeeds, set populated.
	if _, err := src.Get(); err != nil {
		t.Fatalf("first Get: %v", err)
	}
	if !src.IsAccessible("acme/svc") {
		t.Error("acme/svc should be accessible after successful list")
	}

	// Force re-mint by advancing past 50min remaining.
	currentNow = currentNow.Add(11 * time.Minute)
	listShouldFail = true
	if _, err := src.Get(); err != nil {
		t.Fatalf("second Get: %v", err)
	}

	// List failed on the refresh, but the prior good set must still be authoritative.
	if !src.IsAccessible("acme/svc") {
		t.Error("acme/svc should remain accessible after refresh failure (last-good-set)")
	}
	if src.IsAccessible("other/repo") {
		t.Error("other/repo should still NOT be accessible — set is stale, not nuked")
	}
}

func TestAppInstallationSource_FirstListFails_DegradesPermissive(t *testing.T) {
	now := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/access_tokens") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			expires := now.Add(60 * time.Minute).UTC().Format(time.RFC3339)
			_, _ = w.Write([]byte(fmt.Sprintf(`{"token":"ghs_x","expires_at":%q}`, expires)))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	src := newTestSource(t, srv, generateTestKey(t), func() time.Time { return now })

	if _, err := src.Get(); err != nil {
		t.Fatalf("Get: %v", err)
	}

	// Never-ready: IsAccessible returns true (degraded mode), worker-side
	// will surface 401s if the repo really isn't accessible.
	if !src.IsAccessible("any-org/any-repo") {
		t.Error("with never-populated set, IsAccessible should return true (degraded mode)")
	}
}

func TestAppInstallationSource_MintErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	defer srv.Close()

	src := newTestSource(t, srv, generateTestKey(t), nil)

	if _, err := src.Get(); err == nil {
		t.Error("expected error on 401")
	}
	if _, err := src.MintFresh(); err == nil {
		t.Error("expected error on 401 from MintFresh")
	}
}
