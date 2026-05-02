package githubapp

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// productionMintBaseURL is GitHub's API root. Tests override the
// appInstallationSource.baseURL field directly; production callers
// always go through this factory and inherit the public endpoint.
const productionMintBaseURL = "https://api.github.com"

// AppCredentials is the minimal App config this package needs to mint
// installation tokens. Defined here (rather than imported from
// app/config) so app/config can call PreflightApp without an import
// cycle. Callers in app/ translate from config.GitHubAppConfig at
// call time — the two structs are intentionally isomorphic.
type AppCredentials struct {
	AppID          int64
	InstallationID int64
	PrivateKeyPath string
}

// IsConfigured reports whether all three fields are non-zero.
func (c AppCredentials) IsConfigured() bool {
	return c.AppID != 0 && c.InstallationID != 0 && c.PrivateKeyPath != ""
}

// NewPATSource wraps a PAT into a TokenSource. Used by the dispatch
// path's cross-installation fallback: when the App is not installed at
// the primary repo's owner but cfg.GitHub.Token is set, the job is
// minted with the PAT instead of the App installation token.
func NewPATSource(token string) TokenSource {
	return &staticPATSource{token: token}
}

// NewFromConfig returns the TokenSource matching the provided config.
// App credentials take priority when fully populated; partial App
// config is treated as not-set so preflight can surface a field-level
// error. Returns an error when neither auth mode is configured.
func NewFromConfig(patToken string, app AppCredentials, logger *slog.Logger) (TokenSource, error) {
	if app.IsConfigured() {
		return newAppInstallationSourceFromCredentials(app, logger)
	}
	if patToken != "" {
		return &staticPATSource{token: patToken}, nil
	}
	return nil, errors.New("github auth not configured: set github.token or github.app.*")
}

func newAppInstallationSourceFromCredentials(app AppCredentials, logger *slog.Logger) (*appInstallationSource, error) {
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
