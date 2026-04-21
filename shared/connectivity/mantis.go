package connectivity

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// CheckMantis probes the Mantis REST API with the given credentials by
// listing projects. Returns the number of accessible projects on
// success. Uses the `Authorization: <token>` header as Mantis REST
// expects (no Bearer prefix).
func CheckMantis(baseURL, apiToken string) (int, error) {
	if baseURL == "" {
		return 0, errors.New("base URL is empty")
	}
	if apiToken == "" {
		return 0, errors.New("API token is empty")
	}

	url := strings.TrimRight(baseURL, "/") + "/api/rest/projects?page_size=1"
	httpClient := &http.Client{Timeout: 10 * time.Second}
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Authorization", apiToken)

	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("connect %s: %w", baseURL, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var body struct {
			Projects []struct{} `json:"projects"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			return 0, fmt.Errorf("decode response: %w", err)
		}
		return len(body.Projects), nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return 0, errors.New("invalid credentials")
	case http.StatusNotFound:
		return 0, fmt.Errorf("REST API not found at %s; confirm URL or REST plugin enabled", baseURL)
	default:
		return 0, fmt.Errorf("Mantis returned HTTP %d", resp.StatusCode)
	}
}
