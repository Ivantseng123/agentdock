package githubapp

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ivantseng123/agentdock/app/config"
)

func writePEMKey(t *testing.T, key *rsa.PrivateKey) string {
	t.Helper()
	der := x509.MarshalPKCS1PrivateKey(key)
	block := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: der}
	dir := t.TempDir()
	path := filepath.Join(dir, "key.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("write pem: %v", err)
	}
	return path
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestNewFromConfig_AppPriority(t *testing.T) {
	key := generateTestKey(t)
	path := writePEMKey(t, key)
	cfg := config.GitHubConfig{
		Token: "ghp_pat",
		App: config.GitHubAppConfig{
			AppID:          1,
			InstallationID: 2,
			PrivateKeyPath: path,
		},
	}

	src, err := NewFromConfig(cfg, discardLogger())
	if err != nil {
		t.Fatalf("NewFromConfig: %v", err)
	}
	if _, ok := src.(*appInstallationSource); !ok {
		t.Errorf("got %T, want *appInstallationSource (App takes priority over PAT)", src)
	}
}

func TestNewFromConfig_AppOnly(t *testing.T) {
	key := generateTestKey(t)
	path := writePEMKey(t, key)
	cfg := config.GitHubConfig{
		App: config.GitHubAppConfig{
			AppID:          1,
			InstallationID: 2,
			PrivateKeyPath: path,
		},
	}
	src, err := NewFromConfig(cfg, discardLogger())
	if err != nil {
		t.Fatalf("NewFromConfig: %v", err)
	}
	if _, ok := src.(*appInstallationSource); !ok {
		t.Errorf("got %T, want *appInstallationSource", src)
	}
}

func TestNewFromConfig_PATOnly(t *testing.T) {
	cfg := config.GitHubConfig{Token: "ghp_only"}
	src, err := NewFromConfig(cfg, discardLogger())
	if err != nil {
		t.Fatalf("NewFromConfig: %v", err)
	}
	pat, ok := src.(*staticPATSource)
	if !ok {
		t.Fatalf("got %T, want *staticPATSource", src)
	}
	if pat.token != "ghp_only" {
		t.Errorf("token = %q, want ghp_only", pat.token)
	}
}

func TestNewFromConfig_NeitherSet(t *testing.T) {
	src, err := NewFromConfig(config.GitHubConfig{}, discardLogger())
	if err == nil {
		t.Fatal("expected error when neither PAT nor App is set")
	}
	if src != nil {
		t.Errorf("src = %v, want nil", src)
	}
	if !strings.Contains(err.Error(), "github auth not configured") {
		t.Errorf("error = %v, want one mentioning 'github auth not configured'", err)
	}
}

func TestNewFromConfig_PartialAppFallsToError(t *testing.T) {
	// Partial App config (AppID set but no key path) is not "configured"
	// per IsConfigured, and there's no PAT, so factory returns an error.
	// Preflight is responsible for the field-specific error message.
	cfg := config.GitHubConfig{
		App: config.GitHubAppConfig{AppID: 1},
	}
	if _, err := NewFromConfig(cfg, discardLogger()); err == nil {
		t.Fatal("expected error for partial App config without PAT")
	}
}

func TestNewFromConfig_BadKeyPath(t *testing.T) {
	cfg := config.GitHubConfig{
		App: config.GitHubAppConfig{
			AppID:          1,
			InstallationID: 2,
			PrivateKeyPath: "/nonexistent/key.pem",
		},
	}
	_, err := NewFromConfig(cfg, discardLogger())
	if err == nil {
		t.Fatal("expected error for nonexistent key path")
	}
	if !strings.Contains(err.Error(), "/nonexistent/key.pem") {
		t.Errorf("error should include path; got %v", err)
	}
}

func TestNewFromConfig_BadPEMContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.pem")
	if err := os.WriteFile(path, []byte("this is not a PEM"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg := config.GitHubConfig{
		App: config.GitHubAppConfig{
			AppID:          1,
			InstallationID: 2,
			PrivateKeyPath: path,
		},
	}
	_, err := NewFromConfig(cfg, discardLogger())
	if err == nil {
		t.Fatal("expected error for bad PEM")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error should include path; got %v", err)
	}
}
