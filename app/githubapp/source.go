package githubapp

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// TokenSource provides GitHub auth tokens for the app process. Two
// freshness modes (Get / MintFresh) plus a coarse accessibility check
// for the dispatch-time cross-installation guard.
type TokenSource interface {
	// Get returns a token, possibly cached. Caller accepts that the
	// returned token may have as little as 50 minutes remaining TTL.
	Get() (string, error)

	// MintFresh always mints a new installation token, bypassing any
	// cache, and updates the cache with the new value.
	MintFresh() (string, error)

	// IsAccessible reports whether the source can authenticate against
	// the given owner/repo slug (case-insensitive). PAT mode always
	// returns true (PAT scope is opaque to the source). App mode
	// consults the accessibleRepos set populated during minting; if
	// the set has not been populated yet (no mint has occurred), App
	// mode also returns true so the very first dispatch is not
	// reflexively blocked.
	IsAccessible(ownerRepo string) bool
}

// staticPATSource adapts a personal access token to the TokenSource
// interface so the dispatch path can call MintFresh in both modes
// without branching.
type staticPATSource struct{ token string }

func (s *staticPATSource) Get() (string, error)         { return s.token, nil }
func (s *staticPATSource) MintFresh() (string, error)   { return s.token, nil }
func (s *staticPATSource) IsAccessible(_ string) bool   { return true }

// appInstallationSource mints GitHub installation tokens via the
// /app/installations/{id}/access_tokens endpoint and caches the result
// until expiry approaches (50min remaining = 60min TTL minus 10min
// safety buffer).
type appInstallationSource struct {
	appID          int64
	installationID int64
	privateKey     *rsa.PrivateKey
	httpClient     *http.Client
	baseURL        string
	logger         *slog.Logger
	now            func() time.Time

	mu                   sync.Mutex
	cached               string
	expiresAt            time.Time
	accessibleRepos      map[string]struct{} // lower-cased "owner/repo"
	accessibleReposReady bool                // true once listInstallationRepos has succeeded at least once
}

// minCachedTTL is the floor TTL that Get is allowed to hand back. With a
// 60min installation-token TTL this leaves a 10min safety buffer for the
// caller's request to complete before the token expires.
const minCachedTTL = 50 * time.Minute

func (s *appInstallationSource) Get() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cached != "" && s.expiresAt.Sub(s.now()) >= minCachedTTL {
		return s.cached, nil
	}
	return s.mintLocked()
}

func (s *appInstallationSource) MintFresh() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mintLocked()
}

// mintLocked must be called with s.mu held. Updates cache on success
// and refreshes the accessibleRepos set so cross-installation checks
// at dispatch time stay consistent with the App's current install
// targets (typical lag ≤ TTL = 60min).
func (s *appInstallationSource) mintLocked() (string, error) {
	jwtStr, err := signJWT(s.privateKey, s.appID, s.now())
	if err != nil {
		return "", err
	}
	token, expiresAt, err := postInstallationToken(s.httpClient, s.baseURL, jwtStr, s.installationID)
	if err != nil {
		return "", err
	}
	s.cached = token
	s.expiresAt = expiresAt
	s.logger.Info("installation token minted",
		"phase", "完成",
		"installation_id", s.installationID,
		"expires_at", expiresAt.Format(time.RFC3339),
	)

	repos, repoErr := listInstallationRepos(s.httpClient, s.baseURL, token)
	if repoErr != nil {
		// Two failure shapes; treat them differently:
		//   * already-ready: keep the last good set, just warn — the
		//     refresh blipped, IsAccessible stays authoritative on the
		//     stale-but-recent data.
		//   * never-ready: log Error so operators see the gap; cross-
		//     installation guard remains permissive (returns true) so
		//     dispatch can still flow against worker-side 401s.
		level := slog.LevelWarn
		msg := "installation repository list refresh failed; using last-known set"
		if !s.accessibleReposReady {
			level = slog.LevelError
			msg = "installation repository list never populated; cross-installation guard disabled until next successful mint"
		}
		s.logger.Log(context.Background(), level, msg, "phase", "降級", "error", repoErr)
	} else {
		s.accessibleRepos = repos
		s.accessibleReposReady = true
		s.logger.Info("installation repository list refreshed",
			"phase", "完成",
			"count", len(repos),
		)
	}
	return token, nil
}

// IsAccessible reports whether the source can authenticate against
// the given owner/repo slug. Until at least one successful list-repos
// call has run (accessibleReposReady), returns true so the very first
// dispatch isn't reflexively blocked — that degraded mode is
// observable in logs (Error-level on each failed refresh).
func (s *appInstallationSource) IsAccessible(ownerRepo string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.accessibleReposReady {
		return true
	}
	_, ok := s.accessibleRepos[strings.ToLower(ownerRepo)]
	return ok
}

type installationReposResponse struct {
	Repositories []struct {
		FullName string `json:"full_name"`
	} `json:"repositories"`
}

// listInstallationRepos walks GET /installation/repositories with
// per_page=100 pagination and returns the lower-cased owner/repo set
// the App can currently authenticate against.
func listInstallationRepos(httpClient *http.Client, baseURL, installationToken string) (map[string]struct{}, error) {
	set := make(map[string]struct{})
	page := 1
	for {
		url := fmt.Sprintf("%s/installation/repositories?per_page=100&page=%d",
			strings.TrimRight(baseURL, "/"), page)
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return nil, fmt.Errorf("build list-repos request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+installationToken)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

		resp, err := httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("list-repos request: %w", err)
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("list-repos status=%d body=%s", resp.StatusCode, redactGitHubBody(strings.TrimSpace(string(body))))
		}
		var parsed installationReposResponse
		if err := json.Unmarshal(body, &parsed); err != nil {
			return nil, fmt.Errorf("decode list-repos: %w", err)
		}
		for _, r := range parsed.Repositories {
			set[strings.ToLower(r.FullName)] = struct{}{}
		}
		if len(parsed.Repositories) < 100 {
			break
		}
		page++
	}
	return set, nil
}
