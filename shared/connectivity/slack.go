package connectivity

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// SlackIdentity holds the bot's identifiers returned by auth.test.
type SlackIdentity struct {
	UserID   string
	BotID    string
	Username string // Slack handle, e.g. "ai_trigger_issue_bot"
}

// CheckSlackToken verifies the bot token via Slack auth.test API.
// Returns the authenticated identity (user_id + bot_id) on success.
func CheckSlackToken(token string) (SlackIdentity, error) {
	var zero SlackIdentity
	if token == "" {
		return zero, errors.New("token is empty")
	}
	if !strings.HasPrefix(token, "xoxb-") {
		return zero, errors.New("Slack bot token must start with xoxb-")
	}

	httpClient := &http.Client{Timeout: 10 * time.Second}
	req, _ := http.NewRequest(http.MethodPost, "https://slack.com/api/auth.test", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := httpClient.Do(req)
	if err != nil {
		return zero, err
	}
	defer resp.Body.Close()

	var body struct {
		OK     bool   `json:"ok"`
		UserID string `json:"user_id"`
		BotID  string `json:"bot_id"`
		User   string `json:"user"` // bot's handle, e.g. "ai_trigger_issue_bot"
		Error  string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return zero, err
	}
	if !body.OK {
		return zero, fmt.Errorf("auth.test failed: %s", body.Error)
	}
	return SlackIdentity{UserID: body.UserID, BotID: body.BotID, Username: body.User}, nil
}
