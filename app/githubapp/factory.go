package githubapp

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/Ivantseng123/agentdock/app/config"
)

// productionMintBaseURL is GitHub's API root. Tests override the
// appInstallationSource.baseURL field directly; production callers
// always go through this factory and inherit the public endpoint.
const productionMintBaseURL = "https://api.github.com"

// NewFromConfig returns the TokenSource matching the provided config.
// App config takes priority when fully populated; partial App config
// is treated as not-set so preflight can surface a field-level error.
// Returns an error when neither auth mode is configured.
func NewFromConfig(cfg config.GitHubConfig, logger *slog.Logger) (TokenSource, error) {
	if cfg.App.IsConfigured() {
		return newAppInstallationSourceFromConfig(cfg.App, logger)
	}
	if cfg.Token != "" {
		return &staticPATSource{token: cfg.Token}, nil
	}
	return nil, errors.New("github auth not configured: set github.token or github.app.*")
}

func newAppInstallationSourceFromConfig(app config.GitHubAppConfig, logger *slog.Logger) (*appInstallationSource, error) {
	data, err := os.ReadFile(app.PrivateKeyPath)
	if err != nil {
		return nil, fmt.Errorf("github app private key invalid: %s: %w", app.PrivateKeyPath, err)
	}
	key, err := jwt.ParseRSAPrivateKeyFromPEM(data)
	if err != nil {
		return nil, fmt.Errorf("github app private key invalid: %s: %w", app.PrivateKeyPath, err)
	}
	return &appInstallationSource{
		appID:          app.AppID,
		installationID: app.InstallationID,
		privateKey:     key,
		httpClient:     http.DefaultClient,
		baseURL:        productionMintBaseURL,
		logger:         logger,
		now:            time.Now,
	}, nil
}
