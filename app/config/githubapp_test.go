package config

import (
	"path/filepath"
	"testing"
)

func TestGitHubAppConfig_IsConfigured(t *testing.T) {
	cases := []struct {
		name string
		cfg  GitHubAppConfig
		want bool
	}{
		{"all empty", GitHubAppConfig{}, false},
		{"only app_id", GitHubAppConfig{AppID: 1}, false},
		{"only installation_id", GitHubAppConfig{InstallationID: 2}, false},
		{"only key path", GitHubAppConfig{PrivateKeyPath: "/k.pem"}, false},
		{"missing key path", GitHubAppConfig{AppID: 1, InstallationID: 2}, false},
		{"missing app_id", GitHubAppConfig{InstallationID: 2, PrivateKeyPath: "/k.pem"}, false},
		{"missing installation_id", GitHubAppConfig{AppID: 1, PrivateKeyPath: "/k.pem"}, false},
		{"all set", GitHubAppConfig{AppID: 1, InstallationID: 2, PrivateKeyPath: "/k.pem"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.IsConfigured(); got != tc.want {
				t.Errorf("IsConfigured() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLoadConfig_GitHubAppYAML(t *testing.T) {
	yaml := `
github:
  token: ghp_xxx
  app:
    app_id: 123456
    installation_id: 7890123
    private_key_path: /etc/agentdock/app-key.pem
`
	cfg := loadFromString(t, yaml)
	if cfg.GitHub.Token != "ghp_xxx" {
		t.Errorf("Token = %q, want ghp_xxx", cfg.GitHub.Token)
	}
	if cfg.GitHub.App.AppID != 123456 {
		t.Errorf("AppID = %d, want 123456", cfg.GitHub.App.AppID)
	}
	if cfg.GitHub.App.InstallationID != 7890123 {
		t.Errorf("InstallationID = %d, want 7890123", cfg.GitHub.App.InstallationID)
	}
	if cfg.GitHub.App.PrivateKeyPath != "/etc/agentdock/app-key.pem" {
		t.Errorf("PrivateKeyPath = %q, want /etc/agentdock/app-key.pem", cfg.GitHub.App.PrivateKeyPath)
	}
	if !cfg.GitHub.App.IsConfigured() {
		t.Error("App.IsConfigured() should be true when all 3 fields set")
	}
}

func TestBuildKoanf_GitHubAppEnvOverrides(t *testing.T) {
	clearAppEnv(t)
	t.Setenv("GITHUB_APP_APP_ID", "999")
	t.Setenv("GITHUB_APP_INSTALLATION_ID", "888")
	t.Setenv("GITHUB_APP_PRIVATE_KEY_PATH", "/tmp/key.pem")

	dir := t.TempDir()
	path := filepath.Join(dir, "app.yaml")
	cmd := newTestCmd(t)
	if err := cmd.ParseFlags(nil); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	cfg, _, _, _, err := BuildKoanf(cmd, path)
	if err != nil {
		t.Fatalf("BuildKoanf: %v", err)
	}
	if cfg.GitHub.App.AppID != 999 {
		t.Errorf("AppID = %d, want 999 (env override)", cfg.GitHub.App.AppID)
	}
	if cfg.GitHub.App.InstallationID != 888 {
		t.Errorf("InstallationID = %d, want 888 (env override)", cfg.GitHub.App.InstallationID)
	}
	if cfg.GitHub.App.PrivateKeyPath != "/tmp/key.pem" {
		t.Errorf("PrivateKeyPath = %q, want /tmp/key.pem (env override)", cfg.GitHub.App.PrivateKeyPath)
	}
}

func TestBuildKoanf_GitHubAppPartialEnv(t *testing.T) {
	clearAppEnv(t)
	t.Setenv("GITHUB_APP_APP_ID", "999")

	dir := t.TempDir()
	path := filepath.Join(dir, "app.yaml")
	cmd := newTestCmd(t)
	if err := cmd.ParseFlags(nil); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	cfg, _, _, _, err := BuildKoanf(cmd, path)
	if err != nil {
		t.Fatalf("BuildKoanf: %v", err)
	}
	if cfg.GitHub.App.AppID != 999 {
		t.Errorf("AppID = %d, want 999", cfg.GitHub.App.AppID)
	}
	// Other fields stay zero; preflight (T13) is responsible for catching
	// partial config — env override layer just lets values through.
	if cfg.GitHub.App.IsConfigured() {
		t.Error("IsConfigured should be false with only AppID set; preflight handles the error message")
	}
}

func TestBuildKoanf_GitHubAppEnvBadInt(t *testing.T) {
	clearAppEnv(t)
	t.Setenv("GITHUB_APP_APP_ID", "not-a-number")

	dir := t.TempDir()
	path := filepath.Join(dir, "app.yaml")
	cmd := newTestCmd(t)
	if err := cmd.ParseFlags(nil); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	cfg, _, _, _, err := BuildKoanf(cmd, path)
	if err != nil {
		t.Fatalf("BuildKoanf: %v", err)
	}
	// Bad int silently skipped at the env layer; AppID stays zero so
	// IsConfigured returns false and preflight surfaces the error.
	if cfg.GitHub.App.AppID != 0 {
		t.Errorf("AppID = %d, want 0 (bad int parsed away)", cfg.GitHub.App.AppID)
	}
}
