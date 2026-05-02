package githubapp

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPostInstallationToken_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer signed-jwt" {
			t.Errorf("Authorization = %q, want Bearer signed-jwt", got)
		}
		if got := r.Header.Get("Accept"); got != "application/vnd.github+json" {
			t.Errorf("Accept = %q", got)
		}
		if got := r.Header.Get("X-GitHub-Api-Version"); got != "2022-11-28" {
			t.Errorf("X-GitHub-Api-Version = %q", got)
		}
		if r.URL.Path != "/app/installations/42/access_tokens" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"token":"ghs_xyz","expires_at":"2026-05-02T13:00:00Z"}`))
	}))
	defer srv.Close()

	token, expiresAt, err := postInstallationToken(srv.Client(), srv.URL, "signed-jwt", 42)
	if err != nil {
		t.Fatalf("postInstallationToken: %v", err)
	}
	if token != "ghs_xyz" {
		t.Errorf("token = %q, want ghs_xyz", token)
	}
	want := time.Date(2026, 5, 2, 13, 0, 0, 0, time.UTC)
	if !expiresAt.Equal(want) {
		t.Errorf("expiresAt = %v, want %v", expiresAt, want)
	}
}

func TestPostInstallationToken_Errors(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		wantErr error
	}{
		{"401 invalid credentials", http.StatusUnauthorized, `{"message":"Bad credentials"}`, errInvalidAppCredentials},
		{"404 installation not found", http.StatusNotFound, `{"message":"Not Found"}`, errInstallationNotFound},
		{"500 transient", http.StatusInternalServerError, `oops`, errMintTransient},
		{"502 transient", http.StatusBadGateway, `gateway`, errMintTransient},
		{"503 transient", http.StatusServiceUnavailable, `unavailable`, errMintTransient},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			_, _, err := postInstallationToken(srv.Client(), srv.URL, "j", 1)
			if err == nil {
				t.Fatal("expected error")
			}
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("errors.Is(%v, %v) = false; got err = %v", err, tc.wantErr, err)
			}
		})
	}
}

func TestPostInstallationToken_Other4xxIncludesBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"Resource protected by org SAML enforcement"}`))
	}))
	defer srv.Close()

	_, _, err := postInstallationToken(srv.Client(), srv.URL, "j", 1)
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.Is(err, errInvalidAppCredentials) || errors.Is(err, errInstallationNotFound) || errors.Is(err, errMintTransient) {
		t.Errorf("unexpected typed error: %v", err)
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error should mention status 403; got %v", err)
	}
	if !strings.Contains(err.Error(), "SAML") {
		t.Errorf("error should include body excerpt mentioning SAML; got %v", err)
	}
}

func TestPostInstallationToken_MalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`not-json`))
	}))
	defer srv.Close()

	_, _, err := postInstallationToken(srv.Client(), srv.URL, "j", 1)
	if err == nil {
		t.Fatal("expected error on malformed JSON")
	}
	if !strings.Contains(err.Error(), "decode") {
		t.Errorf("error should mention decode; got %v", err)
	}
}
