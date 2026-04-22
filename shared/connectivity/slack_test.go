package connectivity

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// withMockSlack replaces the Slack auth.test URL with a test server and
// patches the single call site. Since slack.go uses a hardcoded URL, we
// use a transport-level interceptor instead.
type urlRewriter struct {
	base string
	rt   http.RoundTripper
}

func (r *urlRewriter) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.Contains(req.URL.String(), "slack.com/api/auth.test") {
		u, _ := req.URL.Parse(r.base + "/api/auth.test")
		req.URL = u
		req.Host = u.Host
	}
	return r.rt.RoundTrip(req)
}

func TestCheckSlackToken_ReturnsIdentity(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"ok":      true,
			"user_id": "UABC123",
			"bot_id":  "BDEF456",
		})
	}))
	defer srv.Close()

	origTransport := http.DefaultTransport
	http.DefaultTransport = &urlRewriter{base: srv.URL, rt: origTransport}
	defer func() { http.DefaultTransport = origTransport }()

	id, err := CheckSlackToken("xoxb-testtoken")
	if err != nil {
		t.Fatalf("CheckSlackToken: %v", err)
	}
	if id.UserID != "UABC123" {
		t.Errorf("UserID = %q, want UABC123", id.UserID)
	}
	if id.BotID != "BDEF456" {
		t.Errorf("BotID = %q, want BDEF456", id.BotID)
	}
}

func TestCheckSlackToken_InvalidToken(t *testing.T) {
	_, err := CheckSlackToken("")
	if err == nil {
		t.Error("expected error for empty token")
	}
	_, err = CheckSlackToken("wrongprefix-abc")
	if err == nil {
		t.Error("expected error for bad prefix")
	}
}
