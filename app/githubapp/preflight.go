package githubapp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"
)

// requiredPermissions encodes the App permission set this codebase
// requires (per spec §1). Preflight fails when any are missing or have
// less access than required.
var requiredPermissions = map[string]string{
	"issues":        "write",
	"contents":      "read",
	"metadata":      "read",
	"pull_requests": "write",
}

// preflightRetryDelays implements §7's transient-error retry schedule:
// 3 retries with 500ms / 1s / 2s back-off. 4xx errors fail fast (no
// retry); 5xx and network errors retry through the full schedule.
var preflightRetryDelays = []time.Duration{
	500 * time.Millisecond,
	1 * time.Second,
	2 * time.Second,
}

// PreflightApp validates that the configured GitHub App can mint
// installation tokens with the required permissions and is reachable.
// Maps every failure mode to a user-facing error string per spec §4.13.
func PreflightApp(app AppCredentials, logger *slog.Logger) error {
	src, err := newAppInstallationSourceFromCredentials(app, logger)
	if err != nil {
		return err
	}
	return preflightAppWithSource(src)
}

// preflightAppWithSource is the testable inner loop — accepts an
// already-built source so tests can wire an httptest server without
// going through the production base-URL constant.
func preflightAppWithSource(src *appInstallationSource) error {
	if _, err := mintWithRetry(src); err != nil {
		return classifyMintError(err, src.installationID)
	}
	if err := checkPermissionsWithRetry(src); err != nil {
		return err
	}
	src.logger.Info("github app preflight passed",
		"phase", "完成",
		"installation_id", src.installationID,
		"accessible_repos", len(src.accessibleRepos),
	)
	return nil
}

// mintWithRetry calls MintFresh with the retry schedule. Transient
// errors (5xx, network) retry through; 4xx fail-fast.
func mintWithRetry(src *appInstallationSource) (string, error) {
	var lastErr error
	for attempt := 0; attempt <= len(preflightRetryDelays); attempt++ {
		token, err := src.MintFresh()
		if err == nil {
			return token, nil
		}
		lastErr = err
		if !errors.Is(err, errMintTransient) {
			return "", err
		}
		if attempt < len(preflightRetryDelays) {
			time.Sleep(preflightRetryDelays[attempt])
		}
	}
	return "", lastErr
}

func classifyMintError(err error, installationID int64) error {
	switch {
	case errors.Is(err, errInvalidAppCredentials):
		return fmt.Errorf("github app credentials rejected: check github.app.app_id and private_key_path match")
	case errors.Is(err, errInstallationNotFound):
		return fmt.Errorf("github app installation not found: id=%d; verify github.app.installation_id", installationID)
	case errors.Is(err, errMintTransient):
		return fmt.Errorf("github api unavailable during preflight (after %d retries): %w; this is an infrastructure issue, not a config issue", len(preflightRetryDelays), err)
	default:
		return err
	}
}

type installationDetails struct {
	Permissions map[string]string `json:"permissions"`
}

func checkPermissionsWithRetry(src *appInstallationSource) error {
	var lastErr error
	for attempt := 0; attempt <= len(preflightRetryDelays); attempt++ {
		err := checkPermissionsOnce(src)
		if err == nil {
			return nil
		}
		lastErr = err
		if !errors.Is(err, errMintTransient) {
			return err
		}
		if attempt < len(preflightRetryDelays) {
			time.Sleep(preflightRetryDelays[attempt])
		}
	}
	return lastErr
}

func checkPermissionsOnce(src *appInstallationSource) error {
	jwtStr, err := signJWT(src.privateKey, src.appID, src.now())
	if err != nil {
		return err
	}

	url := fmt.Sprintf("%s/app/installations/%d",
		strings.TrimRight(src.baseURL, "/"), src.installationID)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build permissions request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+jwtStr)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := src.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("permissions request: %w", errors.Join(err, errMintTransient))
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	// Redact: see mint.go's note. Same proxy echo risk applies here.
	bodyExcerpt := redactGitHubBody(strings.TrimSpace(string(body)))
	if resp.StatusCode >= 500 {
		return fmt.Errorf("%w: status=%d body=%s", errMintTransient, resp.StatusCode, bodyExcerpt)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("github app permissions check status=%d body=%s", resp.StatusCode, bodyExcerpt)
	}

	var details installationDetails
	if err := json.Unmarshal(body, &details); err != nil {
		return fmt.Errorf("decode permissions: %w", err)
	}

	var missing []string
	for permKey, requiredAccess := range requiredPermissions {
		actualAccess, ok := details.Permissions[permKey]
		if !ok {
			missing = append(missing, permKey)
			continue
		}
		if !permissionSatisfies(actualAccess, requiredAccess) {
			missing = append(missing, permKey)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("github app installation missing required permissions: missing=%v; expected: Issues:rw, Contents:r, Metadata:r, Pull requests:rw", missing)
	}
	return nil
}

// permissionSatisfies returns true when actual access >= required
// access. GitHub uses "read", "write", "admin"; write implies read.
func permissionSatisfies(actual, required string) bool {
	rank := map[string]int{"read": 1, "write": 2, "admin": 3}
	return rank[actual] >= rank[required]
}
