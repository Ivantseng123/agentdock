package connectivity

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCheckMantis_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/rest/projects" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "my-token" {
			t.Errorf("Authorization = %q, want my-token (no Bearer prefix)", got)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"projects": []map[string]any{
				{"id": 1, "name": "Proj"},
			},
		})
	}))
	defer srv.Close()

	n, err := CheckMantis(srv.URL, "my-token")
	if err != nil {
		t.Fatalf("CheckMantis: %v", err)
	}
	if n != 1 {
		t.Errorf("projects = %d, want 1", n)
	}
}

func TestCheckMantis_Unauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := CheckMantis(srv.URL, "bad-token")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "invalid credentials") {
		t.Errorf("error = %q, want containing 'invalid credentials'", err.Error())
	}
}

func TestCheckMantis_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := CheckMantis(srv.URL, "tok")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "REST API not found") {
		t.Errorf("error = %q, want containing 'REST API not found'", err.Error())
	}
}

func TestCheckMantis_EmptyBaseURL(t *testing.T) {
	_, err := CheckMantis("", "tok")
	if err == nil || !strings.Contains(err.Error(), "base URL is empty") {
		t.Errorf("error = %v", err)
	}
}

func TestCheckMantis_EmptyToken(t *testing.T) {
	_, err := CheckMantis("https://example.com", "")
	if err == nil || !strings.Contains(err.Error(), "API token is empty") {
		t.Errorf("error = %v", err)
	}
}

func TestCheckMantis_MalformedURL(t *testing.T) {
	_, err := CheckMantis("://missing-scheme", "tok")
	if err == nil {
		t.Fatal("expected error for malformed URL, got nil")
	}
	if !strings.Contains(err.Error(), "build request") {
		t.Errorf("error = %q, want containing 'build request'", err.Error())
	}
}
