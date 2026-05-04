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
