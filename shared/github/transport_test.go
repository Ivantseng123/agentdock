package github

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTokenTransport_InjectsBearerOnEveryRequest(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tokens := []string{"first", "second", "third"}
	idx := 0
	tokenFn := func() (string, error) {
		tok := tokens[idx]
		idx++
		return tok, nil
	}

	httpClient := &http.Client{Transport: newTokenTransport(tokenFn, nil)}

	for range tokens {
		req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}

	want := []string{"Bearer first", "Bearer second", "Bearer third"}
	if len(seen) != len(want) {
		t.Fatalf("got %d requests, want %d", len(seen), len(want))
	}
	for i, got := range seen {
		if got != want[i] {
			t.Errorf("request %d: got Authorization=%q, want %q", i, got, want[i])
		}
	}
}

func TestTokenTransport_EmptyTokenSkipsHeader(t *testing.T) {
	var sawAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			sawAuth = true
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tokenFn := func() (string, error) { return "", nil }
	httpClient := &http.Client{Transport: newTokenTransport(tokenFn, nil)}

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if sawAuth {
		t.Errorf("Authorization header should be empty when tokenFn returns empty")
	}
}

func TestTokenTransport_TokenFnErrorAborts(t *testing.T) {
	var hit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	wantErr := errors.New("token unavailable")
	tokenFn := func() (string, error) { return "", wantErr }
	httpClient := &http.Client{Transport: newTokenTransport(tokenFn, nil)}

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := httpClient.Do(req)
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil {
		t.Fatal("expected error from RoundTrip when tokenFn fails")
	}
	if !errors.Is(err, wantErr) && !strings.Contains(err.Error(), wantErr.Error()) {
		t.Errorf("expected wrapped error to surface tokenFn error, got: %v", err)
	}
	if hit {
		t.Error("server should not have been hit when tokenFn errored")
	}
}

func TestTokenTransport_DoesNotMutateInputRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tokenFn := func() (string, error) { return "secret", nil }
	httpClient := &http.Client{Transport: newTokenTransport(tokenFn, nil)}

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req.Header.Set("X-Original", "kept")

	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("input request was mutated: Authorization=%q (RoundTripper must clone)", got)
	}
	if got := req.Header.Get("X-Original"); got != "kept" {
		t.Errorf("input request lost original header: X-Original=%q", got)
	}
}

func TestTokenTransport_NilDelegateUsesDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer t" {
			t.Errorf("server got Authorization=%q, want Bearer t", r.Header.Get("Authorization"))
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tokenFn := func() (string, error) { return "t", nil }
	tr := newTokenTransport(tokenFn, nil)
	if tr.delegate == nil {
		t.Fatal("nil delegate should default to a non-nil RoundTripper")
	}

	httpClient := &http.Client{Transport: tr}
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
}
