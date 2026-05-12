package github

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestInstallationRepoDiscovery_UsesInstallationRepositoriesEndpoint(t *testing.T) {
	var paths []string
	var authHeaders []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.RequestURI())
		authHeaders = append(authHeaders, r.Header.Get("Authorization"))
		if r.URL.Path != "/installation/repositories" {
			t.Fatalf("unexpected path %q; App installation tokens must not use /user/repos", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"repositories":[{"full_name":"Acme/Service"},{"full_name":"acme/Library"}]}`))
	}))
	defer srv.Close()

	var calls atomic.Int32
	discovery := newInstallationRepoDiscoveryWithBaseURL(func() (string, error) {
		n := calls.Add(1)
		return fmt.Sprintf("ghs_token_%d", n), nil
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), srv.URL)

	repos, err := discovery.ListRepos(context.Background())
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	wantRepos := []string{"Acme/Service", "acme/Library"}
	if !reflect.DeepEqual(repos, wantRepos) {
		t.Errorf("repos = %v, want %v", repos, wantRepos)
	}
	if len(paths) != 1 || paths[0] != "/installation/repositories?per_page=100&page=1" {
		t.Errorf("paths = %v, want single installation repositories page", paths)
	}
	if len(authHeaders) != 1 || authHeaders[0] != "Bearer ghs_token_1" {
		t.Errorf("auth headers = %v", authHeaders)
	}
	if calls.Load() != 1 {
		t.Errorf("tokenFn calls = %d, want 1", calls.Load())
	}
}

func TestSearchRepos_FiltersAndCaps(t *testing.T) {
	d := &RepoDiscovery{
		ttl:     time.Hour,
		fetched: time.Now(),
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	d.cache = []string{
		"Acme/Service",
		"acme/Library",
		"OtherOrg/service-tools",
		"OtherOrg/unrelated",
	}

	cases := []struct {
		name  string
		query string
		want  []string
	}{
		{"empty query returns all up to cap", "", []string{"Acme/Service", "acme/Library", "OtherOrg/service-tools", "OtherOrg/unrelated"}},
		{"case-insensitive substring", "service", []string{"Acme/Service", "OtherOrg/service-tools"}},
		{"uppercase query matches lowercase repo", "LIBRARY", []string{"acme/Library"}},
		{"no match returns empty", "missing", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := d.SearchRepos(context.Background(), tc.query)
			if err != nil {
				t.Fatalf("SearchRepos: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSearchRepos_CapsAt25(t *testing.T) {
	cache := make([]string, 50)
	for i := range cache {
		cache[i] = fmt.Sprintf("org/repo-%02d", i)
	}
	d := &RepoDiscovery{
		ttl:     time.Hour,
		fetched: time.Now(),
		cache:   cache,
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	got, err := d.SearchRepos(context.Background(), "")
	if err != nil {
		t.Fatalf("SearchRepos empty: %v", err)
	}
	if len(got) != 25 {
		t.Errorf("empty query: got %d, want 25", len(got))
	}

	got, err = d.SearchRepos(context.Background(), "repo")
	if err != nil {
		t.Fatalf("SearchRepos query: %v", err)
	}
	if len(got) != 25 {
		t.Errorf("query match: got %d, want 25", len(got))
	}
}

func TestInstallationRepoDiscovery_PaginatesAndRefreshesTokenPerRequest(t *testing.T) {
	var paths []string
	var authHeaders []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.RequestURI())
		authHeaders = append(authHeaders, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("page") {
		case "1":
			items := make([]string, 100)
			for i := range items {
				items[i] = fmt.Sprintf(`{"full_name":"org/repo-%d"}`, i)
			}
			_, _ = w.Write([]byte(`{"repositories":[` + strings.Join(items, ",") + `]}`))
		case "2":
			_, _ = w.Write([]byte(`{"repositories":[{"full_name":"org/last"}]}`))
		default:
			t.Fatalf("unexpected page %q", r.URL.Query().Get("page"))
		}
	}))
	defer srv.Close()

	var calls atomic.Int32
	discovery := newInstallationRepoDiscoveryWithBaseURL(func() (string, error) {
		return fmt.Sprintf("ghs_token_%d", calls.Add(1)), nil
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), srv.URL)

	repos, err := discovery.ListRepos(context.Background())
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if len(repos) != 101 {
		t.Fatalf("repo count = %d, want 101", len(repos))
	}
	wantPaths := []string{
		"/installation/repositories?per_page=100&page=1",
		"/installation/repositories?per_page=100&page=2",
	}
	if !reflect.DeepEqual(paths, wantPaths) {
		t.Errorf("paths = %v, want %v", paths, wantPaths)
	}
	wantAuth := []string{"Bearer ghs_token_1", "Bearer ghs_token_2"}
	if !reflect.DeepEqual(authHeaders, wantAuth) {
		t.Errorf("auth headers = %v, want %v", authHeaders, wantAuth)
	}
}
