package githubapp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Typed sentinel errors so callers (TokenSource + preflight) can map to
// user-facing config diagnostics without string-matching the error body.
var (
	errInvalidAppCredentials = errors.New("github app credentials rejected")
	errInstallationNotFound  = errors.New("github app installation not found")
	errMintTransient         = errors.New("github api transient error")
)

type mintResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// postInstallationToken calls
// POST {baseURL}/app/installations/{id}/access_tokens with the App JWT
// and returns the minted installation token + its absolute expiry. 5xx
// returns errMintTransient so the retry layer (preflight, MintFresh
// callers) can treat it as infrastructure error; 4xx other than 401/404
// returns a wrapped error including status + body excerpt.
func postInstallationToken(httpClient *http.Client, baseURL, jwt string, installationID int64) (string, time.Time, error) {
	url := fmt.Sprintf("%s/app/installations/%d/access_tokens", strings.TrimRight(baseURL, "/"), installationID)
	req, err := http.NewRequest(http.MethodPost, url, nil)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("build mint request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("mint request: %w", errors.Join(err, errMintTransient))
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
		var body mintResponse
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			return "", time.Time{}, fmt.Errorf("decode mint response: %w", err)
		}
		return body.Token, body.ExpiresAt, nil
	}

	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	// Redact: a misbehaving proxy can echo our Authorization header
	// back into a 5xx body. Status code is enough to diagnose mint
	// failures; the body is for context, not credentials.
	bodyExcerpt := redactGitHubBody(strings.TrimSpace(string(bodyBytes)))

	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return "", time.Time{}, fmt.Errorf("%w: %s", errInvalidAppCredentials, bodyExcerpt)
	case resp.StatusCode == http.StatusNotFound:
		return "", time.Time{}, fmt.Errorf("%w: %s", errInstallationNotFound, bodyExcerpt)
	case resp.StatusCode >= 500:
		return "", time.Time{}, fmt.Errorf("%w: status=%d body=%s", errMintTransient, resp.StatusCode, bodyExcerpt)
	default:
		return "", time.Time{}, fmt.Errorf("github app mint unexpected status %d: %s", resp.StatusCode, bodyExcerpt)
	}
}
