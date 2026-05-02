package config

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/Ivantseng123/agentdock/app/githubapp"
	"github.com/Ivantseng123/agentdock/shared/prompt"
)

// withCapturedPromptStderr swaps prompt.Stderr for a bytes.Buffer so a
// test can assert what prompt.Warn / prompt.OK / prompt.Fail wrote.
func withCapturedPromptStderr(t *testing.T) *bytes.Buffer {
	t.Helper()
	prev := prompt.Stderr
	buf := &bytes.Buffer{}
	prompt.Stderr = buf
	t.Cleanup(func() { prompt.Stderr = prev })
	return buf
}


// withMockedAppPreflight swaps githubAppPreflightFn for the test and
// restores it on cleanup. Lets each test pin App-side preflight to a
// deterministic outcome without hitting GitHub.
func withMockedAppPreflight(t *testing.T, fn func(githubapp.AppCredentials, *slog.Logger) error) {
	t.Helper()
	prev := githubAppPreflightFn
	githubAppPreflightFn = fn
	t.Cleanup(func() { githubAppPreflightFn = prev })
}

func TestPreflightGitHub_AppPartialConfig_FieldSpecificError(t *testing.T) {
	withMockedAppPreflight(t, func(githubapp.AppCredentials, *slog.Logger) error {
		t.Fatal("PreflightApp should not be called when config is partial")
		return nil
	})

	cfg := &Config{
		GitHub: GitHubConfig{
			App: GitHubAppConfig{AppID: 123}, // missing installation_id, private_key_path
		},
	}
	err := preflightGitHub(cfg, false, map[string]any{})
	if err == nil {
		t.Fatal("expected partial-config error")
	}
	if !strings.Contains(err.Error(), "github app config partial") {
		t.Errorf("error = %v, want 'github app config partial'", err)
	}
	if !strings.Contains(err.Error(), "github.app.installation_id") {
		t.Errorf("error should name missing field installation_id; got %v", err)
	}
	if !strings.Contains(err.Error(), "github.app.private_key_path") {
		t.Errorf("error should name missing field private_key_path; got %v", err)
	}
}

func TestPreflightGitHub_AppMissingSecretKey_AC18(t *testing.T) {
	withMockedAppPreflight(t, func(githubapp.AppCredentials, *slog.Logger) error {
		t.Fatal("PreflightApp should not be called when secret_key is missing")
		return nil
	})

	cfg := &Config{
		GitHub: GitHubConfig{
			App: GitHubAppConfig{AppID: 1, InstallationID: 2, PrivateKeyPath: "/k.pem"},
		},
		SecretKey: "",
	}
	err := preflightGitHub(cfg, false, map[string]any{})
	if err == nil {
		t.Fatal("expected secret_key error")
	}
	if !strings.Contains(err.Error(), "secret_key") {
		t.Errorf("error = %v, want one mentioning secret_key", err)
	}
	if !strings.Contains(err.Error(), "boundary") {
		t.Errorf("error should explain why secret_key matters (boundary); got %v", err)
	}
}

func TestPreflightGitHub_AppJobTimeoutOver50Min_AC19EmitsWarn(t *testing.T) {
	withMockedAppPreflight(t, func(githubapp.AppCredentials, *slog.Logger) error { return nil })
	buf := withCapturedPromptStderr(t)

	cfg := &Config{
		GitHub: GitHubConfig{
			App: GitHubAppConfig{AppID: 1, InstallationID: 2, PrivateKeyPath: "/k.pem"},
		},
		SecretKey: "deadbeef",
		Queue:     QueueConfig{JobTimeout: 90 * time.Minute},
	}
	if err := preflightGitHub(cfg, false, map[string]any{}); err != nil {
		t.Fatalf("preflightGitHub: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "50min") {
		t.Errorf("expected warn mentioning 50min boundary; got %q", out)
	}
	if !strings.Contains(out, "MIGRATION-github-app.md") {
		t.Errorf("expected warn pointing at migration doc; got %q", out)
	}
}

func TestPreflightGitHub_AppJobTimeoutUnder50Min_NoWarn(t *testing.T) {
	withMockedAppPreflight(t, func(githubapp.AppCredentials, *slog.Logger) error { return nil })
	buf := withCapturedPromptStderr(t)

	cfg := &Config{
		GitHub: GitHubConfig{
			App: GitHubAppConfig{AppID: 1, InstallationID: 2, PrivateKeyPath: "/k.pem"},
		},
		SecretKey: "deadbeef",
		Queue:     QueueConfig{JobTimeout: 30 * time.Minute},
	}
	if err := preflightGitHub(cfg, false, map[string]any{}); err != nil {
		t.Fatalf("preflightGitHub: %v", err)
	}
	if strings.Contains(buf.String(), "50min") {
		t.Errorf("unexpected 50min warn for sub-boundary timeout: %q", buf.String())
	}
}

func TestPreflightGitHub_AppPreflightSuccess_PathRuns(t *testing.T) {
	called := false
	withMockedAppPreflight(t, func(creds githubapp.AppCredentials, _ *slog.Logger) error {
		called = true
		if creds.AppID != 1 || creds.InstallationID != 2 || creds.PrivateKeyPath != "/k.pem" {
			t.Errorf("creds = %+v, want passthrough from cfg", creds)
		}
		return nil
	})

	cfg := &Config{
		GitHub: GitHubConfig{
			App: GitHubAppConfig{AppID: 1, InstallationID: 2, PrivateKeyPath: "/k.pem"},
		},
		SecretKey: "deadbeef",
		Queue:     QueueConfig{JobTimeout: 30 * time.Minute},
	}
	if err := preflightGitHub(cfg, false, map[string]any{}); err != nil {
		t.Fatalf("preflightGitHub: %v", err)
	}
	if !called {
		t.Error("PreflightApp should have been invoked")
	}
}

func TestPreflightGitHub_AppPreflightFailure_PropagatesError(t *testing.T) {
	wantErr := errors.New("github app installation not found: id=2")
	withMockedAppPreflight(t, func(githubapp.AppCredentials, *slog.Logger) error {
		return wantErr
	})

	cfg := &Config{
		GitHub: GitHubConfig{
			App: GitHubAppConfig{AppID: 1, InstallationID: 2, PrivateKeyPath: "/k.pem"},
		},
		SecretKey: "deadbeef",
	}
	err := preflightGitHub(cfg, false, map[string]any{})
	if err == nil {
		t.Fatal("expected propagated error")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("got %v, want propagated %v", err, wantErr)
	}
}

func TestPreflightGitHub_AppNeitherSetNonInteractive_FailsClearly(t *testing.T) {
	withMockedAppPreflight(t, func(githubapp.AppCredentials, *slog.Logger) error {
		t.Fatal("should not be called")
		return nil
	})

	cfg := &Config{}
	err := preflightGitHub(cfg, false, map[string]any{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "github auth not configured") {
		t.Errorf("error = %v, want 'github auth not configured'", err)
	}
}
