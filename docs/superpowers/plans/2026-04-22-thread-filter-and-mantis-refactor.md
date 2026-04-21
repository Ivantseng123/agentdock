# Thread Filter Fix + Mantis Agent-Skill Refactor — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Specs:**
- `docs/superpowers/specs/2026-04-21-bot-message-thread-filter-design.md`
- `docs/superpowers/specs/2026-04-22-mantis-agent-skill-design.md`

**Goal:** Fix the thread filter that drops all non-self bot messages, and refactor Mantis extraction from a backend HTTP client into an agent-invoked skill receiving creds via the existing secret bus.

**Architecture:** Three concurrent changes in one PR: (1) extend preflight's `CheckSlackToken` to return full Slack identity so runtime doesn't double-call `auth.test`; (2) widen `filterThreadMessages` to keep external bot messages, reconstruct their text from blocks/attachments, and tag them `bot:<name>`; (3) delete `app/mantis/` + `app/bot/enrich.go`, route `mantis.*` config through the existing `Secrets` map as `MANTIS_API_URL`/`MANTIS_API_TOKEN` env vars for the agent process, and bundle a pre-built mantis skill into `agents/skills/mantis/` for the agent to invoke when it sees Mantis URLs.

**Tech Stack:** Go 1.22+ / slack-go SDK / koanf config / net/http for Mantis REST / Node.js 18+ (worker host runtime for the bundled JS skill).

**Phases (commits):**
1. Shared infra (helpers)
2. Slack client (text extraction + filter rewrite)
3. Bot layer (identity wiring + Mantis removal from workflow)
4. App layer wiring (preflight → app.go → cmd/agentdock)
5. Delete obsolete Mantis backend code
6. Config layer Mantis changes
7. Init interactive prompt
8. Skill bundle + docs

---

## Phase 1 — Shared Infrastructure

### Task 1: Add `YesNoDefault` helper to `shared/prompt`

**Files:**
- Modify: `shared/prompt/prompt.go`
- Modify: `shared/prompt/prompt_test.go`

- [ ] **Step 1: Write the failing test**

Append to `shared/prompt/prompt_test.go`:

```go
func TestYesNoDefault_EmptyInputUsesDefault(t *testing.T) {
	origStdin := Stdin
	defer func() { Stdin = origStdin }()

	cases := []struct {
		name       string
		input      string
		defaultYes bool
		want       bool
	}{
		{"empty_default_yes", "\n", true, true},
		{"empty_default_no", "\n", false, false},
		{"y_default_no", "y\n", false, true},
		{"n_default_yes", "n\n", true, false},
		{"yes_default_no", "yes\n", false, true},
		{"capital_Y_default_no", "Y\n", false, true},
		{"whitespace_empty_default_no", "   \n", false, false},
	}

	var buf bytes.Buffer
	origStderr := Stderr
	Stderr = &buf
	defer func() { Stderr = origStderr }()

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			Stdin = strings.NewReader(tc.input)
			got := YesNoDefault("prompt", tc.defaultYes)
			if got != tc.want {
				t.Errorf("YesNoDefault(%q, default=%v) = %v, want %v", tc.input, tc.defaultYes, got, tc.want)
			}
		})
	}
}

func TestYesNoDefault_SuffixMatchesDefault(t *testing.T) {
	origStdin := Stdin
	Stdin = strings.NewReader("\n")
	defer func() { Stdin = origStdin }()

	var buf bytes.Buffer
	origStderr := Stderr
	Stderr = &buf
	defer func() { Stderr = origStderr }()

	YesNoDefault("enable feature?", false)
	out := buf.String()
	if !strings.Contains(out, "[y/N]") {
		t.Errorf("default=false prompt should show [y/N], got: %q", out)
	}

	buf.Reset()
	Stdin = strings.NewReader("\n")
	YesNoDefault("enable feature?", true)
	out = buf.String()
	if !strings.Contains(out, "[Y/n]") {
		t.Errorf("default=true prompt should show [Y/n], got: %q", out)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./shared/prompt/ -run 'TestYesNoDefault' -v`
Expected: FAIL with "undefined: YesNoDefault"

- [ ] **Step 3: Implement `YesNoDefault` and refactor `YesNo`**

Replace lines 71-76 of `shared/prompt/prompt.go`:

```go
// YesNoDefault prints a yes/no prompt with an explicit default. Pressing
// Enter returns defaultYes. The [Y/n] / [y/N] suffix reflects the default.
func YesNoDefault(prompt string, defaultYes bool) bool {
	suffix := "[y/N]"
	if defaultYes {
		suffix = "[Y/n]"
	}
	answer := Line(fmt.Sprintf("%s %s: ", prompt, suffix))
	if answer == "" {
		return defaultYes
	}
	lower := strings.ToLower(answer)
	return lower == "y" || lower == "yes"
}

// YesNo prints a yes/no prompt with a "yes" default.
func YesNo(prompt string) bool {
	return YesNoDefault(prompt, true)
}
```

- [ ] **Step 4: Run tests to verify all pass**

Run: `go test ./shared/prompt/ -v`
Expected: PASS (including pre-existing `TestOK_PrintsGreenCheckmark`, `TestFail_PrintsRedCross`, `TestCheckAgentCLI_MissingBinaryReturnsError` and new tests)

- [ ] **Step 5: Commit**

```bash
git add shared/prompt/prompt.go shared/prompt/prompt_test.go
git commit -m "feat(shared/prompt): add YesNoDefault helper"
```

---

### Task 2: Extend `CheckSlackToken` to return `SlackIdentity`

**Files:**
- Modify: `shared/connectivity/slack.go`
- Create: `shared/connectivity/slack_test.go`
- Modify: `cmd/agentdock/init.go:201`
- Modify: `app/config/preflight.go:60`, `:82`

Rationale: `auth.test` already returns both `user_id` and `bot_id`. Currently `CheckSlackToken` returns only `user_id`. Widening lets us reuse its result in app.go instead of calling `auth.test` a second time.

- [ ] **Step 1: Write failing test**

Create `shared/connectivity/slack_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./shared/connectivity/ -run 'TestCheckSlackToken' -v`
Expected: FAIL with type mismatch on `id.UserID` / `id.BotID` (current signature returns plain string).

- [ ] **Step 3: Modify `CheckSlackToken` signature and implementation**

Replace the contents of `shared/connectivity/slack.go`:

```go
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
	UserID string
	BotID  string
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
		Error  string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return zero, err
	}
	if !body.OK {
		return zero, fmt.Errorf("auth.test failed: %s", body.Error)
	}
	return SlackIdentity{UserID: body.UserID, BotID: body.BotID}, nil
}
```

- [ ] **Step 4: Update caller in `cmd/agentdock/init.go:201`**

Change the line currently reading `userID, err := connectivity.CheckSlackToken(tok)` and the subsequent usage. Open `cmd/agentdock/init.go` around line 201 and replace:

```go
			userID, err := connectivity.CheckSlackToken(tok)
			if err != nil {
				prompt.Fail("%v (attempt %d/3)", err, attempt)
				continue
			}
			cfg.Slack.BotToken = tok
			prompt.OK("Slack bot token valid (user_id: %s)", userID)
```

with:

```go
			identity, err := connectivity.CheckSlackToken(tok)
			if err != nil {
				prompt.Fail("%v (attempt %d/3)", err, attempt)
				continue
			}
			cfg.Slack.BotToken = tok
			prompt.OK("Slack bot token valid (user_id: %s)", identity.UserID)
```

- [ ] **Step 5: Update callers in `app/config/preflight.go`**

Line 60 area: change

```go
		userID, err := connectivity.CheckSlackToken(cfg.Slack.BotToken)
		if err != nil {
			prompt.Fail("Slack bot token invalid: %v", err)
			return err
		}
		prompt.OK("Slack bot token valid (user_id: %s)", userID)
		return nil
```

to

```go
		identity, err := connectivity.CheckSlackToken(cfg.Slack.BotToken)
		if err != nil {
			prompt.Fail("Slack bot token invalid: %v", err)
			return err
		}
		prompt.OK("Slack bot token valid (user_id: %s)", identity.UserID)
		prompted["slack.bot_user_id"] = identity.UserID
		prompted["slack.bot_id"] = identity.BotID
		return nil
```

Line 82 area (the interactive retry loop): change

```go
		userID, err := connectivity.CheckSlackToken(token)
		if err != nil {
			prompt.Fail("%v (attempt %d/%d)", err, attempt, maxRetries)
			if attempt == maxRetries {
				return fmt.Errorf("max retries exceeded for Slack bot token")
			}
			continue
		}
```

to

```go
		identity, err := connectivity.CheckSlackToken(token)
		if err != nil {
			prompt.Fail("%v (attempt %d/%d)", err, attempt, maxRetries)
			if attempt == maxRetries {
				return fmt.Errorf("max retries exceeded for Slack bot token")
			}
			continue
		}
```

Then a few lines down in the same function, after the existing `prompted["slack.bot_token"] = token` line, add:

```go
		prompted["slack.bot_user_id"] = identity.UserID
		prompted["slack.bot_id"] = identity.BotID
		_ = identity // ensure identity is used even if UserID/BotID are empty (AuthTest succeeded but unusual response)
```

(Keep `_ = identity` only if the compiler complains about unused var; drop otherwise.)

Also update the `prompt.OK` success line in the interactive branch to reference `identity.UserID` instead of `userID`.

- [ ] **Step 6: Run all relevant tests**

Run:
```
go test ./shared/connectivity/ ./app/config/ ./cmd/agentdock/ -v
```
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add shared/connectivity/slack.go shared/connectivity/slack_test.go \
        cmd/agentdock/init.go app/config/preflight.go
git commit -m "feat(connectivity): CheckSlackToken returns SlackIdentity (user_id+bot_id)

Preflight now writes both values into the prompted map so app startup
can reuse them instead of calling auth.test a second time."
```

---

### Task 3: Add `CheckMantis` helper

**Files:**
- Create: `shared/connectivity/mantis.go`
- Create: `shared/connectivity/mantis_test.go`

- [ ] **Step 1: Write failing tests**

Create `shared/connectivity/mantis_test.go`:

```go
package connectivity

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCheckMantis_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/rest/projects" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "my-token" {
			t.Errorf("Authorization = %q, want my-token (no Bearer prefix)", got)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"projects": []map[string]any{
				{"id": 1, "name": "Proj"},
			},
		})
	}))
	defer srv.Close()

	n, err := CheckMantis(srv.URL, "my-token")
	if err != nil {
		t.Fatalf("CheckMantis: %v", err)
	}
	if n != 1 {
		t.Errorf("projects = %d, want 1", n)
	}
}

func TestCheckMantis_Unauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := CheckMantis(srv.URL, "bad-token")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "invalid credentials") {
		t.Errorf("error = %q, want containing 'invalid credentials'", err.Error())
	}
}

func TestCheckMantis_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := CheckMantis(srv.URL, "tok")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "REST API not found") {
		t.Errorf("error = %q, want containing 'REST API not found'", err.Error())
	}
}

func TestCheckMantis_EmptyBaseURL(t *testing.T) {
	_, err := CheckMantis("", "tok")
	if err == nil || !strings.Contains(err.Error(), "base URL is empty") {
		t.Errorf("error = %v", err)
	}
}

func TestCheckMantis_EmptyToken(t *testing.T) {
	_, err := CheckMantis("https://example.com", "")
	if err == nil || !strings.Contains(err.Error(), "API token is empty") {
		t.Errorf("error = %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./shared/connectivity/ -run 'TestCheckMantis' -v`
Expected: FAIL with "undefined: CheckMantis".

- [ ] **Step 3: Implement `CheckMantis`**

Create `shared/connectivity/mantis.go`:

```go
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
```

- [ ] **Step 4: Run tests**

Run: `go test ./shared/connectivity/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add shared/connectivity/mantis.go shared/connectivity/mantis_test.go
git commit -m "feat(connectivity): add CheckMantis probe for init-time validation"
```

---

## Phase 2 — Slack Client Helpers & Filter Rewrite

### Task 4: Add message text extraction helpers

**Files:**
- Modify: `app/slack/client.go` (add `extractMessageText` + `extractFromBlocks` + `extractFromAttachments`)
- Modify: `app/slack/client_test.go`

- [ ] **Step 1: Write failing tests**

Append to `app/slack/client_test.go`:

```go
func TestExtractMessageText_PrefersText(t *testing.T) {
	msg := slack.Message{Msg: slack.Msg{
		Text:        "primary",
		Attachments: []slack.Attachment{{Fallback: "fallback"}},
	}}
	got := extractMessageText(msg)
	if got != "primary" {
		t.Errorf("got %q, want primary", got)
	}
}

func TestExtractMessageText_BlocksFallback(t *testing.T) {
	blocks := slack.Blocks{BlockSet: []slack.Block{
		slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn", "hello world", false, false),
			nil, nil,
		),
	}}
	msg := slack.Message{Msg: slack.Msg{Text: "", Blocks: blocks}}
	got := extractMessageText(msg)
	if !strings.Contains(got, "hello world") {
		t.Errorf("got %q, want containing 'hello world'", got)
	}
}

func TestExtractMessageText_AttachmentsFallback(t *testing.T) {
	msg := slack.Message{Msg: slack.Msg{
		Text: "",
		Attachments: []slack.Attachment{{
			Pretext: "pre",
			Title:   "title",
			Text:    "body",
			Fields: []slack.AttachmentField{
				{Title: "Env", Value: "prod"},
			},
		}},
	}}
	got := extractMessageText(msg)
	for _, want := range []string{"pre", "title", "body", "Env", "prod"} {
		if !strings.Contains(got, want) {
			t.Errorf("got %q, missing %q", got, want)
		}
	}
}

func TestExtractMessageText_BlocksWinOverAttachments(t *testing.T) {
	blocks := slack.Blocks{BlockSet: []slack.Block{
		slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn", "from-blocks", false, false),
			nil, nil,
		),
	}}
	msg := slack.Message{Msg: slack.Msg{
		Text:        "",
		Blocks:      blocks,
		Attachments: []slack.Attachment{{Fallback: "from-attach"}},
	}}
	got := extractMessageText(msg)
	if !strings.Contains(got, "from-blocks") {
		t.Errorf("got %q, want blocks content preferred", got)
	}
	if strings.Contains(got, "from-attach") {
		t.Errorf("got %q, should not include attachment when blocks present", got)
	}
}

func TestExtractMessageText_AllEmpty(t *testing.T) {
	msg := slack.Message{Msg: slack.Msg{}}
	if got := extractMessageText(msg); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}
```

Also add `"strings"` to the imports of `app/slack/client_test.go` if not already present.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./app/slack/ -run 'TestExtractMessageText' -v`
Expected: FAIL with "undefined: extractMessageText".

- [ ] **Step 3: Implement the helpers**

Add to `app/slack/client.go` (near the bottom, before `ExtractKeywords`):

```go
// extractMessageText returns m.Text if non-empty, otherwise reconstructs
// text from blocks (modern integrations) or attachments (legacy). Returns
// "" only when no renderable text is present.
func extractMessageText(m slack.Message) string {
	if strings.TrimSpace(m.Text) != "" {
		return m.Text
	}
	if s := extractFromBlocks(m.Blocks.BlockSet); s != "" {
		return s
	}
	if s := extractFromAttachments(m.Attachments); s != "" {
		return s
	}
	return ""
}

// extractFromBlocks walks block kit content pulling text from
// text-bearing block types. Interactive / image blocks are ignored.
func extractFromBlocks(blocks []slack.Block) string {
	var parts []string
	for _, b := range blocks {
		switch bb := b.(type) {
		case *slack.SectionBlock:
			if bb.Text != nil && bb.Text.Text != "" {
				parts = append(parts, bb.Text.Text)
			}
			for _, f := range bb.Fields {
				if f != nil && f.Text != "" {
					parts = append(parts, f.Text)
				}
			}
		case *slack.HeaderBlock:
			if bb.Text != nil && bb.Text.Text != "" {
				parts = append(parts, bb.Text.Text)
			}
		case *slack.ContextBlock:
			for _, e := range bb.ContextElements.Elements {
				if tb, ok := e.(*slack.TextBlockObject); ok && tb.Text != "" {
					parts = append(parts, tb.Text)
				}
			}
		case *slack.RichTextBlock:
			for _, el := range bb.Elements {
				if s, ok := el.(*slack.RichTextSection); ok {
					for _, inner := range s.Elements {
						if te, ok := inner.(*slack.RichTextSectionTextElement); ok && te.Text != "" {
							parts = append(parts, te.Text)
						}
					}
				}
			}
		}
	}
	return strings.Join(parts, "\n")
}

// extractFromAttachments renders legacy-API attachment content as plain
// text. Each attachment contributes its Pretext / Title / Text / Fallback
// plus any Fields; multiple attachments are joined with a blank line.
func extractFromAttachments(atts []slack.Attachment) string {
	var segments []string
	for _, a := range atts {
		var parts []string
		if a.Pretext != "" {
			parts = append(parts, a.Pretext)
		}
		if a.Title != "" {
			parts = append(parts, a.Title)
		}
		if a.Text != "" {
			parts = append(parts, a.Text)
		} else if a.Fallback != "" {
			parts = append(parts, a.Fallback)
		}
		for _, f := range a.Fields {
			if f.Title != "" || f.Value != "" {
				parts = append(parts, fmt.Sprintf("*%s*: %s", f.Title, f.Value))
			}
		}
		if len(parts) > 0 {
			segments = append(segments, strings.Join(parts, "\n"))
		}
	}
	return strings.Join(segments, "\n\n")
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./app/slack/ -run 'TestExtractMessageText' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add app/slack/client.go app/slack/client_test.go
git commit -m "feat(slack): add extractMessageText for bot messages with empty Text

Pulls content from blocks first (modern integrations) then falls back to
attachments (legacy). Either/or semantics — compat-mode messages get the
fuller block content, not the shorter attachment fallback."
```

---

### Task 5: Add `resolveBotDisplayName` helper

**Files:**
- Modify: `app/slack/client.go`
- Modify: `app/slack/client_test.go`

- [ ] **Step 1: Write failing tests**

Append to `app/slack/client_test.go`:

```go
func TestResolveBotDisplayName_PrefersBotProfileName(t *testing.T) {
	m := slack.Message{Msg: slack.Msg{
		BotProfile: &slack.BotProfile{Name: "GitHub"},
		Username:   "github[bot]",
		BotID:      "B123",
	}}
	if got := resolveBotDisplayName(m); got != "GitHub" {
		t.Errorf("got %q, want GitHub", got)
	}
}

func TestResolveBotDisplayName_FallsBackToUsername(t *testing.T) {
	m := slack.Message{Msg: slack.Msg{
		Username: "my-webhook",
		BotID:    "B123",
	}}
	if got := resolveBotDisplayName(m); got != "my-webhook" {
		t.Errorf("got %q, want my-webhook", got)
	}
}

func TestResolveBotDisplayName_FallsBackToBotID(t *testing.T) {
	m := slack.Message{Msg: slack.Msg{
		BotID: "B123",
	}}
	if got := resolveBotDisplayName(m); got != "B123" {
		t.Errorf("got %q, want B123", got)
	}
}

func TestResolveBotDisplayName_ReturnsEmptyForNonBot(t *testing.T) {
	m := slack.Message{Msg: slack.Msg{}}
	if got := resolveBotDisplayName(m); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./app/slack/ -run 'TestResolveBotDisplayName' -v`
Expected: FAIL with "undefined: resolveBotDisplayName".

- [ ] **Step 3: Implement**

Add to `app/slack/client.go` (right after `extractFromAttachments`):

```go
// resolveBotDisplayName picks the best human-friendly name for a bot
// message, preferring BotProfile.Name (what Slack's UI shows) over
// Username (integration-set) and falling back to BotID.
func resolveBotDisplayName(m slack.Message) string {
	if m.BotProfile != nil && m.BotProfile.Name != "" {
		return m.BotProfile.Name
	}
	if m.Username != "" {
		return m.Username
	}
	if m.BotID != "" {
		return m.BotID
	}
	return ""
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./app/slack/ -run 'TestResolveBotDisplayName' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add app/slack/client.go app/slack/client_test.go
git commit -m "feat(slack): add resolveBotDisplayName for bot identity"
```

---

### Task 6: Add Slack user ID pattern guard to `ResolveUser`

**Files:**
- Modify: `app/slack/client.go` (`ResolveUser`)
- Modify: `app/slack/client_test.go`

Rationale: Under the new filter, `ThreadRawMessage.User` for bot messages becomes `"bot:GitHub"` etc. That value later reaches `ResolveUser` in `workflow.go:417`. Without a guard, `GetUserInfo("bot:GitHub")` fires a HTTP call which fails, emitting a warn log per bot message. The guard avoids the API call entirely for non-Slack-ID strings.

- [ ] **Step 1: Write failing test**

Append to `app/slack/client_test.go`:

```go
func TestIsSlackUserID(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"UABC123", true},
		{"WXYZ789", true},
		{"U1", true},
		{"bot:GitHub", false},
		{"ivan", false},
		{"abc123", false},
		{"", false},
		{"U abc", false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := isSlackUserID(tc.in); got != tc.want {
				t.Errorf("isSlackUserID(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./app/slack/ -run 'TestIsSlackUserID' -v`
Expected: FAIL with "undefined: isSlackUserID".

- [ ] **Step 3: Implement `isSlackUserID` and wire it into `ResolveUser`**

Add to `app/slack/client.go` imports if not already present: `"regexp"`.

Add near the package-level vars:

```go
var slackUserIDPattern = regexp.MustCompile(`^[UW][A-Z0-9]+$`)

// isSlackUserID reports whether s matches the shape of a Slack user ID
// (uppercase U or W followed by alphanumeric uppercase). Used to short-
// circuit API calls for strings we know aren't resolvable.
func isSlackUserID(s string) bool {
	return slackUserIDPattern.MatchString(s)
}
```

Then replace `ResolveUser` (lines 181-191):

```go
func (c *Client) ResolveUser(userID string) string {
	if !isSlackUserID(userID) {
		// Not a Slack-shaped ID (bot display name, already-resolved name,
		// etc.). Skip the API call and return as-is.
		return userID
	}
	user, err := c.api.GetUserInfo(userID)
	if err != nil {
		c.logger.Warn("使用者名稱解析失敗", "phase", "失敗", "user_id", userID, "error", err)
		return userID
	}
	if user.Profile.DisplayName != "" {
		return user.Profile.DisplayName
	}
	return user.RealName
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./app/slack/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add app/slack/client.go app/slack/client_test.go
git commit -m "fix(slack): ResolveUser skips API call for non-Slack-ID input

Avoids warn-log pollution when bot display names (e.g. \"bot:GitHub\")
reach this function after the filter-widening change."
```

---

### Task 7: Rewrite `filterThreadMessages` + widen `FetchThreadContext` signature

**Files:**
- Modify: `app/slack/client.go` (`filterThreadMessages`, `FetchThreadContext`)
- Modify: `app/slack/client_test.go` (replace `TestFilterThreadMessages`)

- [ ] **Step 1: Write failing test (replace the existing one)**

Replace `TestFilterThreadMessages` in `app/slack/client_test.go` with:

```go
func TestFilterThreadMessages(t *testing.T) {
	messages := []slack.Message{
		{Msg: slack.Msg{User: "U001", Text: "bug report", Timestamp: "1000.0"}},
		// External bot with text content — keep, mark with bot: prefix.
		{Msg: slack.Msg{
			User: "", Text: "PR #42 opened by alice", Timestamp: "1001.0",
			BotID: "B_GH", BotProfile: &slack.BotProfile{Name: "GitHub"},
		}},
		// Self via UserID match — drop.
		{Msg: slack.Msg{User: "UBOT", Text: "self via user id", Timestamp: "1002.0", BotID: "B_SELF"}},
		// Self via BotID match (User mismatched) — drop.
		{Msg: slack.Msg{User: "", Text: "self via bot id", Timestamp: "1003.0", BotID: "B_SELF"}},
		// External bot empty text/blocks/attachments — drop.
		{Msg: slack.Msg{
			User: "", Text: "", Timestamp: "1004.0",
			BotID: "B_OTHER", BotProfile: &slack.BotProfile{Name: "OtherBot"},
		}},
		{Msg: slack.Msg{User: "U002", Text: "me too", Timestamp: "1005.0"}},
		// Trigger itself — drop (>= triggerTS).
		{Msg: slack.Msg{User: "U001", Text: "@bot", Timestamp: "1006.0"}},
	}

	result := filterThreadMessages(messages, "1006.0", "UBOT", "B_SELF")
	if len(result) != 3 {
		t.Fatalf("expected 3 messages, got %d: %+v", len(result), result)
	}
	if result[0].Text != "bug report" {
		t.Errorf("msg[0].Text = %q", result[0].Text)
	}
	if result[1].User != "bot:GitHub" {
		t.Errorf("msg[1].User = %q, want bot:GitHub", result[1].User)
	}
	if result[1].Text != "PR #42 opened by alice" {
		t.Errorf("msg[1].Text = %q", result[1].Text)
	}
	if result[2].Text != "me too" {
		t.Errorf("msg[2].Text = %q", result[2].Text)
	}
}

func TestFilterThreadMessages_BotTextFromBlocks(t *testing.T) {
	blocks := slack.Blocks{BlockSet: []slack.Block{
		slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn", "block content", false, false),
			nil, nil,
		),
	}}
	messages := []slack.Message{
		{Msg: slack.Msg{
			User: "", Timestamp: "1000.0",
			BotID:      "B_GH",
			BotProfile: &slack.BotProfile{Name: "GitHub"},
			Blocks:     blocks,
		}},
	}
	result := filterThreadMessages(messages, "9999.0", "UBOT", "B_SELF")
	if len(result) != 1 {
		t.Fatalf("expected 1 message, got %d", len(result))
	}
	if !strings.Contains(result[0].Text, "block content") {
		t.Errorf("Text = %q, want blocks content", result[0].Text)
	}
	if result[0].User != "bot:GitHub" {
		t.Errorf("User = %q", result[0].User)
	}
}

func TestFilterThreadMessages_EmptyIdentityKeepsAll(t *testing.T) {
	messages := []slack.Message{
		{Msg: slack.Msg{User: "U001", Text: "human", Timestamp: "1000.0"}},
		{Msg: slack.Msg{User: "", Text: "bot text", Timestamp: "1001.0", BotID: "B1",
			BotProfile: &slack.BotProfile{Name: "AnyBot"}}},
	}
	result := filterThreadMessages(messages, "9999.0", "", "")
	if len(result) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./app/slack/ -run 'TestFilterThreadMessages' -v`
Expected: FAIL — signature mismatch (only 3 args expected currently) and/or behavior mismatch.

- [ ] **Step 3: Rewrite `filterThreadMessages` and widen `FetchThreadContext`**

In `app/slack/client.go`, replace `filterThreadMessages` (lines 474-492):

```go
// filterThreadMessages keeps messages from other participants (human or
// external bots) and drops our own bot's posts (identified by botUserID
// or botID). Bot messages get their text reconstructed from blocks or
// attachments when m.Text is empty; messages whose reconstructed text is
// also empty are dropped entirely. Bot display names are prefixed with
// "bot:" in the User field so downstream prompts can tell them apart
// from humans.
func filterThreadMessages(messages []slack.Message, triggerTS, botUserID, botID string) []ThreadRawMessage {
	var result []ThreadRawMessage
	for _, m := range messages {
		if m.Timestamp >= triggerTS {
			continue
		}
		if botUserID != "" && m.User == botUserID {
			continue
		}
		if botID != "" && m.BotID == botID {
			continue
		}
		text := extractMessageText(m)
		if m.BotID != "" && text == "" {
			// Pure interactive / reaction-only bot message — no signal for triage.
			continue
		}
		user := m.User
		if m.BotID != "" {
			if name := resolveBotDisplayName(m); name != "" {
				user = "bot:" + name
			}
		}
		result = append(result, ThreadRawMessage{
			User:      user,
			Text:      text,
			Timestamp: m.Timestamp,
			Files:     m.Files,
		})
	}
	return result
}
```

Also widen `FetchThreadContext` (lines 438-472):

```go
// FetchThreadContext reads all messages in a thread up to the trigger
// point, filtering out our own bot's posts. botUserID and botID are
// both checked because edge cases (custom username, thread broadcast,
// new block API) can leave one field mismatched.
func (c *Client) FetchThreadContext(channelID, threadTS, triggerTS, botUserID, botID string, limit int) ([]ThreadRawMessage, error) {
	start := time.Now()
	if limit <= 0 {
		limit = 50
	}

	var allMessages []slack.Message
	cursor := ""

	for {
		params := &slack.GetConversationRepliesParameters{
			ChannelID: channelID,
			Timestamp: threadTS,
			Cursor:    cursor,
			Limit:     200,
		}

		msgs, hasMore, nextCursor, err := c.api.GetConversationReplies(params)
		if err != nil {
			return nil, fmt.Errorf("conversations.replies: %w", err)
		}

		allMessages = append(allMessages, msgs...)

		if !hasMore || len(allMessages) >= limit {
			break
		}
		cursor = nextCursor
	}

	result := filterThreadMessages(allMessages, triggerTS, botUserID, botID)
	c.logger.Debug("訊息串內容已讀取", "phase", "處理中", "channel_id", channelID, "message_count", len(result), "duration_ms", time.Since(start).Milliseconds())
	return result, nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./app/slack/ -v`
Expected: PASS (all existing tests plus the new ones in this task and prior tasks).

- [ ] **Step 5: Commit**

```bash
git add app/slack/client.go app/slack/client_test.go
git commit -m "fix(slack): keep external bot messages in thread context

- filterThreadMessages now accepts both botUserID and botID to precisely
  identify self (edge cases mismatch one or the other).
- External bot text is reconstructed via extractMessageText when m.Text
  is empty; all-empty bot messages are dropped.
- Bot display names are prefixed with 'bot:' in the User field so the
  agent prompt can distinguish bot notifications from human speech."
```

---

## Phase 3 — Bot Layer Integration

### Task 8: Add `app/bot/identity.go`

**Files:**
- Create: `app/bot/identity.go`

- [ ] **Step 1: Create the file**

```go
// Package bot owns the triage workflow: reading Slack threads,
// orchestrating repo selection, enqueuing jobs, and resolving results.
// Identity lives here (not in shared/) because it's only used to
// tell Slack thread messages from our own bot's posts.
package bot

// Identity holds the bot's own user_id and bot_id as returned by
// Slack's auth.test. Used by filterThreadMessages to drop our own
// status / selector / result posts from thread context.
type Identity struct {
	UserID string // e.g. "UBOTxxxxx"
	BotID  string // e.g. "BBOTxxxxx"
}
```

- [ ] **Step 2: Verify compile**

Run: `go build ./app/bot/`
Expected: succeeds.

- [ ] **Step 3: Commit**

```bash
git add app/bot/identity.go
git commit -m "feat(bot): add Identity struct for bot self-identification"
```

---

### Task 9: Modify `Workflow` struct — add identity, remove mantisClient, remove enrichMessage

**Files:**
- Modify: `app/bot/workflow.go`
- Modify: `app/bot/workflow_test.go`

This one is heavy because multiple concerns change in the same struct. Doing them in one task avoids two rounds of compile-breakage.

- [ ] **Step 1: Update `slackAPI` interface (line 38)**

In `app/bot/workflow.go`, change:

```go
	FetchThreadContext(channelID, threadTS, triggerTS, botUserID string, limit int) ([]slackclient.ThreadRawMessage, error)
```

to

```go
	FetchThreadContext(channelID, threadTS, triggerTS, botUserID, botID string, limit int) ([]slackclient.ThreadRawMessage, error)
```

- [ ] **Step 2: Remove `mantisClient` field and add `identity`**

Edit the `Workflow` struct (around lines 61-78):

Remove the line `mantisClient  *mantis.Client` (line 67).

Add before the closing brace (after `secretKey  []byte`):

```go
	identity      Identity
```

- [ ] **Step 3: Remove `mantisClient` from `NewWorkflow` signature, add `identity`**

Change `NewWorkflow` (lines 80-91):

```go
func NewWorkflow(
	cfg *config.Config,
	slack slackAPI,
	repoCache *ghclient.RepoCache,
	repoDiscovery *ghclient.RepoDiscovery,
	jobQueue queue.JobQueue,
	jobStore queue.JobStore,
	attachStore queue.AttachmentStore,
	resultBus queue.ResultBus,
	skillProvider SkillProvider,
	identity Identity,
) *Workflow {
```

Remove the `mantisClient *mantis.Client,` parameter. Inside the body, in the returned `&Workflow{...}` literal, remove the `mantisClient: mantisClient,` line and add `identity: identity,`.

- [ ] **Step 4: Remove the mantis import**

At the top of `app/bot/workflow.go`, delete the line:

```go
	"github.com/Ivantseng123/agentdock/app/mantis"
```

- [ ] **Step 5: Remove the enrichMessage call and fix FetchThreadContext call**

Around line 400, change:

```go
	// 1. Read thread context.
	botUserID := ""
	rawMsgs, err := w.slack.FetchThreadContext(pt.ChannelID, pt.ThreadTS, pt.TriggerTS, botUserID, w.cfg.MaxThreadMessages)
```

to

```go
	// 1. Read thread context.
	rawMsgs, err := w.slack.FetchThreadContext(
		pt.ChannelID, pt.ThreadTS, pt.TriggerTS,
		w.identity.UserID, w.identity.BotID,
		w.cfg.MaxThreadMessages,
	)
```

Around lines 409-421 (the message-enrichment loop), change:

```go
	// 2. Enrich messages.
	var threadMsgs []queue.ThreadMessage
	for _, m := range rawMsgs {
		text := m.Text
		if w.mantisClient != nil {
			text = enrichMessage(text, w.mantisClient)
		}
		threadMsgs = append(threadMsgs, queue.ThreadMessage{
			User:      w.slack.ResolveUser(m.User),
			Timestamp: m.Timestamp,
			Text:      text,
		})
	}
```

to

```go
	// 2. Shape messages for the queue. (Mantis enrichment is now the
	// agent's job via the mantis skill + env vars.)
	var threadMsgs []queue.ThreadMessage
	for _, m := range rawMsgs {
		threadMsgs = append(threadMsgs, queue.ThreadMessage{
			User:      w.slack.ResolveUser(m.User),
			Timestamp: m.Timestamp,
			Text:      m.Text,
		})
	}
```

- [ ] **Step 6: Update the test stub**

Edit `app/bot/workflow_test.go` line 115:

```go
func (s *stubSlack) FetchThreadContext(channelID, threadTS, triggerTS, botUserID string, limit int) ([]slackclient.ThreadRawMessage, error) {
```

to

```go
func (s *stubSlack) FetchThreadContext(channelID, threadTS, triggerTS, botUserID, botID string, limit int) ([]slackclient.ThreadRawMessage, error) {
```

Also find every `NewWorkflow(...)` call in `workflow_test.go` and adjust to the new signature: drop the `mantisClient` argument (pass nil was the original, now the parameter is gone), add a `bot.Identity{}` zero-value at the end.

Use grep to find call sites:

```
grep -n 'NewWorkflow(' app/bot/workflow_test.go
```

For each call, the pattern transformation is: remove the `mantisClient` / `nil` passed in the old mantis slot, append `Identity{}` as the last arg.

- [ ] **Step 7: Run tests**

Run: `go test ./app/bot/ -v`
Expected: tests compile and pass. If there is a lingering reference to `enrichMessage`, Go will flag it — see next task for `enrich.go` deletion. For this task, `enrich.go` still exists; its function is merely unused. Leave it for Task 14.

Run: `go vet ./...`
Expected: may warn about unused `enrich.go` / `app/mantis/`. That's fine; Tasks 13–14 delete them.

- [ ] **Step 8: Commit**

```bash
git add app/bot/workflow.go app/bot/workflow_test.go
git commit -m "refactor(bot/workflow): wire Identity in, remove mantisClient path

- Workflow now carries a bot.Identity (UserID + BotID) set by the app
  from preflight's CheckSlackToken result.
- FetchThreadContext gets both IDs so filterThreadMessages can drop only
  our own posts.
- Mantis enrichment call is removed — the agent invokes the mantis skill
  instead (spec: 2026-04-22-mantis-agent-skill-design.md)."
```

---

## Phase 4 — App & CLI Wiring

### Task 10: Thread identity through `app.Run` + `cmd/agentdock/app.go`

**Files:**
- Modify: `app/app.go`
- Modify: `cmd/agentdock/app.go`

- [ ] **Step 1: Update `app.Run` signature**

In `app/app.go`, find the `Run` function. Its current signature takes `(cfg *config.Config)` (or similar). Widen to accept `bot.Identity`:

```go
func Run(cfg *config.Config, identity bot.Identity) (*Handle, error) {
```

Near the top of `Run`, **remove** the block (currently lines 215-221 area):

```go
	botUserID := ""
	if authResp, err := api.AuthTest(); err == nil {
		botUserID = authResp.UserID
		appLogger.Info("Bot 身份已解析", "phase", "處理中", "user_id", botUserID)
	} else {
		appLogger.Warn("Bot 身份解析失敗", "phase", "失敗", "error", err)
	}
```

Replace it with:

```go
	appLogger.Info("Bot 身份已解析", "phase", "處理中",
		"user_id", identity.UserID, "bot_id", identity.BotID)
```

Further down where `botUserID` was referenced (the AutoBind check, lines 275 & 279 area), change `botUserID` references to `identity.UserID`.

- [ ] **Step 2: Remove `mantis.NewClient` block**

Still in `app/app.go`, delete lines 104-112 (the mantis.NewClient construction and its log):

```go
	mantisClient := mantis.NewClient(
		cfg.Mantis.BaseURL,
		cfg.Mantis.APIToken,
		cfg.Mantis.Username,
		cfg.Mantis.Password,
	)
	if mantisClient.IsConfigured() {
		appLogger.Info("Mantis 整合已啟用", "phase", "處理中", "url", cfg.Mantis.BaseURL)
	}
```

Remove the `"github.com/Ivantseng123/agentdock/app/mantis"` import at the top.

- [ ] **Step 3: Update the `NewWorkflow` call in `app/app.go`**

Current (line 153):

```go
	wf := bot.NewWorkflow(cfg, slackClient, repoCache, repoDiscovery, mantisClient, coordinator, jobStore, bundle.Attachments, bundle.Results, skillLoader)
```

Change to:

```go
	wf := bot.NewWorkflow(cfg, slackClient, repoCache, repoDiscovery, coordinator, jobStore, bundle.Attachments, bundle.Results, skillLoader, identity)
```

- [ ] **Step 4: Update `cmd/agentdock/app.go` to capture prompted map and pass identity**

Replace the body of `appCmd.RunE`:

```go
		RunE: func(cmd *cobra.Command, args []string) error {
			appCfg, _, err := loadAppConfig(cmd, appConfigPath)
			if err != nil {
				return err
			}
			if err := appconfig.Validate(appCfg); err != nil {
				return err
			}
			prompted, err := appconfig.RunPreflight(appCfg)
			if err != nil {
				return fmt.Errorf("preflight: %w", err)
			}

			identity := bot.Identity{}
			if v, ok := prompted["slack.bot_user_id"].(string); ok {
				identity.UserID = v
			}
			if v, ok := prompted["slack.bot_id"].(string); ok {
				identity.BotID = v
			}

			handle, err := app.Run(appCfg, identity)
			if err != nil {
				return err
			}
			return handle.Wait()
		},
```

Add `"github.com/Ivantseng123/agentdock/app/bot"` to the imports.

- [ ] **Step 5: Run build and tests**

Run: `go build ./...`
Expected: succeeds.

Run: `go test ./app/... ./cmd/... -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add app/app.go cmd/agentdock/app.go
git commit -m "refactor(app): wire Identity from preflight; drop 2nd auth.test call

- app.Run accepts bot.Identity from cmd/agentdock after preflight.
- Removes second api.AuthTest() call (preflight's CheckSlackToken
  already returns user_id+bot_id).
- Drops mantis.NewClient construction (agent will invoke mantis skill)."
```

---

## Phase 5 — Delete Obsolete Mantis Backend Code

### Task 11: Delete `app/mantis/` and `app/bot/enrich.go`

**Files:**
- Delete: `app/mantis/client.go` (and directory if empty)
- Delete: `app/bot/enrich.go`

- [ ] **Step 1: Verify no remaining references**

Run:
```
grep -rn 'app/mantis\|enrichMessage\|mantis\.Client\|mantis\.NewClient\|mantis\.ExtractIssueID\|mantisClient' --include='*.go' .
```

Expected: zero matches in non-deleted files. If any match, trace to the source and update. The only matches allowed are inside `app/mantis/client.go` and `app/bot/enrich.go` themselves (which we're about to delete).

- [ ] **Step 2: Delete files**

```bash
rm app/mantis/client.go
rmdir app/mantis
rm app/bot/enrich.go
```

- [ ] **Step 3: Run full test suite**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add -A app/mantis app/bot/enrich.go
git commit -m "refactor(mantis): delete backend Mantis client + enrichMessage

Mantis is now handled by the agent via the bundled mantis skill. The
backend no longer fetches Mantis content before queuing jobs."
```

---

## Phase 6 — Config Layer Mantis Changes

### Task 12: Trim `MantisConfig` and remove flags

**Files:**
- Modify: `app/config/config.go`
- Modify: `app/config/flags.go`
- Modify: `app/config/env.go` (if applicable)

- [ ] **Step 1: Trim struct**

In `app/config/config.go` (lines 94-99), replace:

```go
type MantisConfig struct {
	BaseURL  string `yaml:"base_url"`
	APIToken string `yaml:"api_token"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}
```

with:

```go
type MantisConfig struct {
	BaseURL  string `yaml:"base_url"`
	APIToken string `yaml:"api_token"`
}
```

- [ ] **Step 2: Remove `mantis-username` and `mantis-password` flags**

In `app/config/flags.go`, remove lines:

```go
	"mantis-username":          "mantis.username",
	"mantis-password":          "mantis.password",
```

from the map, and in the `RegisterFlags` block remove:

```go
	f.String("mantis-username", "", "Mantis username (basic auth fallback)")
	f.String("mantis-password", "", "Mantis password (basic auth fallback)")
```

- [ ] **Step 3: Check env var mappings**

Inspect `app/config/env.go`. If either `MANTIS_USERNAME` or `MANTIS_PASSWORD` appears as a known env var, remove those entries.

- [ ] **Step 4: Run build**

Run: `go build ./...`
Expected: succeeds. If the compiler flags any lingering reference to `cfg.Mantis.Username` / `cfg.Mantis.Password` outside of this task, trace and fix (there shouldn't be any since we deleted `app/mantis/` in Task 11).

- [ ] **Step 5: Run tests**

Run: `go test ./app/config/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add app/config/config.go app/config/flags.go app/config/env.go
git commit -m "refactor(config): drop Mantis basic-auth fields

The bundled mantis skill only supports API token auth; the unused
username/password config fields are removed. Any existing YAML with
these keys will trigger a 'unknown setting key' warn log via koanf's
existing warnUnknownKeys — no hard failure."
```

---

### Task 13: Inject Mantis secrets in `resolveSecrets`

**Files:**
- Modify: `app/config/defaults.go`
- Modify: `app/config/config_test.go`

- [ ] **Step 1: Write failing tests**

Append to `app/config/config_test.go`:

```go
func TestResolveSecrets_MantisInjected(t *testing.T) {
	cfg := loadFromString(t, `
mantis:
  base_url: https://mantis.example.com
  api_token: mantis-token
`)
	if got := cfg.Secrets["MANTIS_API_URL"]; got != "https://mantis.example.com/api/rest" {
		t.Errorf("MANTIS_API_URL = %q, want https://mantis.example.com/api/rest", got)
	}
	if got := cfg.Secrets["MANTIS_API_TOKEN"]; got != "mantis-token" {
		t.Errorf("MANTIS_API_TOKEN = %q, want mantis-token", got)
	}
}

func TestResolveSecrets_MantisStripsTrailingSlash(t *testing.T) {
	cfg := loadFromString(t, `
mantis:
  base_url: https://mantis.example.com/
  api_token: t
`)
	if got := cfg.Secrets["MANTIS_API_URL"]; got != "https://mantis.example.com/api/rest" {
		t.Errorf("MANTIS_API_URL = %q", got)
	}
}

func TestResolveSecrets_MantisEmpty_NoInjection(t *testing.T) {
	cfg := loadFromString(t, ``)
	if _, ok := cfg.Secrets["MANTIS_API_URL"]; ok {
		t.Error("MANTIS_API_URL should not be set when Mantis is unconfigured")
	}
	if _, ok := cfg.Secrets["MANTIS_API_TOKEN"]; ok {
		t.Error("MANTIS_API_TOKEN should not be set when Mantis is unconfigured")
	}
}

func TestResolveSecrets_MantisExistingSecretNotOverridden(t *testing.T) {
	cfg := loadFromString(t, `
mantis:
  base_url: https://mantis.example.com
  api_token: from-config
secrets:
  MANTIS_API_TOKEN: from-secrets
`)
	if got := cfg.Secrets["MANTIS_API_TOKEN"]; got != "from-secrets" {
		t.Errorf("MANTIS_API_TOKEN = %q, want from-secrets (user override preserved)", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./app/config/ -run 'TestResolveSecrets_Mantis' -v`
Expected: FAIL — `Secrets` map lacks the keys because `resolveSecrets` doesn't inject Mantis yet.

- [ ] **Step 3: Add injection logic**

In `app/config/defaults.go`, modify `resolveSecrets` (lines 107-120):

```go
// resolveSecrets merges github.token and mantis.* into secrets and
// applies env var overrides.
func resolveSecrets(cfg *Config) {
	if cfg.Secrets == nil {
		cfg.Secrets = make(map[string]string)
	}
	if cfg.GitHub.Token != "" {
		if _, exists := cfg.Secrets["GH_TOKEN"]; !exists {
			cfg.Secrets["GH_TOKEN"] = cfg.GitHub.Token
		}
	}
	if cfg.Mantis.BaseURL != "" && cfg.Mantis.APIToken != "" {
		if _, exists := cfg.Secrets["MANTIS_API_URL"]; !exists {
			cfg.Secrets["MANTIS_API_URL"] = strings.TrimRight(cfg.Mantis.BaseURL, "/") + "/api/rest"
		}
		if _, exists := cfg.Secrets["MANTIS_API_TOKEN"]; !exists {
			cfg.Secrets["MANTIS_API_TOKEN"] = cfg.Mantis.APIToken
		}
	}
	for k, v := range scanSecretEnvVars() {
		cfg.Secrets[k] = v
	}
}
```

Add `"strings"` to the import block at the top of the file if not already present.

- [ ] **Step 4: Run tests**

Run: `go test ./app/config/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add app/config/defaults.go app/config/config_test.go
git commit -m "feat(config): inject mantis.* into Secrets as MANTIS_API_URL/TOKEN

The worker's agent runner already forwards Secrets as env vars. The
bundled mantis skill expects MANTIS_API_URL (with /api/rest suffix)
and MANTIS_API_TOKEN — we build these from mantis.base_url + /api/rest
and mantis.api_token respectively."
```

---

### Task 14: Add Mantis partial-config validation

**Files:**
- Modify: `app/config/validate.go`
- Modify: `app/config/config_test.go` (or dedicated `validate_test.go`)

- [ ] **Step 1: Write failing tests**

Check for an existing `app/config/validate_test.go`:

```
test -f app/config/validate_test.go && echo exists || echo missing
```

If it doesn't exist, add tests to `config_test.go`. If it does, append there. Use the pattern:

```go
func TestValidate_Mantis_PartialConfigBaseURLOnly(t *testing.T) {
	cfg := loadFromString(t, `
mantis:
  base_url: https://mantis.example.com
`)
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected validation error for partial mantis config")
	}
	if !strings.Contains(err.Error(), "mantis.base_url and mantis.api_token") {
		t.Errorf("error = %v, want message naming both fields", err)
	}
}

func TestValidate_Mantis_PartialConfigTokenOnly(t *testing.T) {
	cfg := loadFromString(t, `
mantis:
  api_token: just-a-token
`)
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected validation error for partial mantis config")
	}
}

func TestValidate_Mantis_BothEmpty_OK(t *testing.T) {
	cfg := loadFromString(t, ``)
	if err := Validate(cfg); err != nil {
		// Other validation may still fail; only assert Mantis-related error absent.
		if strings.Contains(err.Error(), "mantis") {
			t.Errorf("got unexpected mantis error: %v", err)
		}
	}
}

func TestValidate_Mantis_BothSet_OK(t *testing.T) {
	cfg := loadFromString(t, `
mantis:
  base_url: https://mantis.example.com
  api_token: t
`)
	if err := Validate(cfg); err != nil {
		if strings.Contains(err.Error(), "mantis") {
			t.Errorf("got unexpected mantis error: %v", err)
		}
	}
}
```

Add `"strings"` to imports if not present.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./app/config/ -run 'TestValidate_Mantis' -v`
Expected: FAIL.

- [ ] **Step 3: Add validation check**

In `app/config/validate.go`, before the final `if len(errs) > 0 { ... }` block, add:

```go
	if (cfg.Mantis.BaseURL != "") != (cfg.Mantis.APIToken != "") {
		errs = append(errs, "mantis.base_url and mantis.api_token must both be set or both be empty")
	}
```

- [ ] **Step 4: Run tests**

Run: `go test ./app/config/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add app/config/validate.go app/config/config_test.go
git commit -m "feat(config): fail validation on partial Mantis config

If either mantis.base_url or mantis.api_token is set, both must be
set. Catches the silent-drop case where resolveSecrets wouldn't
inject due to one missing field."
```

---

## Phase 7 — Init Interactive Prompt

### Task 15: Add `promptMantis` to `agentdock init app --interactive`

**Files:**
- Modify: `cmd/agentdock/init.go`

- [ ] **Step 1: Add `promptMantis` function and wire into `promptAppInit`**

In `cmd/agentdock/init.go`, add a new function (place after `promptAppInit`):

```go
// promptMantis collects Mantis base_url + api_token, validates via
// connectivity.CheckMantis. 3 retries then offers to skip.
func promptMantis(cfg *appconfig.Config) error {
	fmt.Fprintln(prompt.Stderr)
	fmt.Fprintln(prompt.Stderr, "  Mantis base URL (e.g. https://mantis.example.com):")
	var baseURL string
	for attempt := 1; attempt <= 3; attempt++ {
		baseURL = strings.TrimRight(prompt.Line("URL: "), "/")
		if baseURL == "" {
			prompt.Fail("URL is required (attempt %d/3)", attempt)
			continue
		}
		break
	}
	if baseURL == "" {
		return nil // user gave up; leave mantis unconfigured
	}

	fmt.Fprintln(prompt.Stderr)
	fmt.Fprintln(prompt.Stderr, "  Mantis API token:")
	var token string
	for attempt := 1; attempt <= 3; attempt++ {
		token = prompt.Hidden("Token: ")
		if token == "" {
			prompt.Fail("token is required (attempt %d/3)", attempt)
			continue
		}
		n, err := connectivity.CheckMantis(baseURL, token)
		if err != nil {
			prompt.Fail("%v (attempt %d/3)", err, attempt)
			if attempt == 3 {
				// 3 strikes — offer skip (default yes). Either way we exit;
				// no infinite retry loop.
				_ = prompt.YesNoDefault("  Skip Mantis setup?", true)
				return nil
			}
			continue
		}
		cfg.Mantis.BaseURL = baseURL
		cfg.Mantis.APIToken = token
		prompt.OK("Mantis connected (%d projects accessible)", n)
		return nil
	}
	return nil
}
```

- [ ] **Step 2: Wire into `promptAppInit`**

Find the end of `promptAppInit` (just before the final `return nil`, around line 270). Add:

```go
	fmt.Fprintln(prompt.Stderr)
	fmt.Fprintln(prompt.Stderr, "  Mantis enrichment (optional) — lets the agent fetch Mantis issue details.")
	if prompt.YesNoDefault("  Enable Mantis?", false) {
		if err := promptMantis(cfg); err != nil {
			return err
		}
	}
	return nil
```

(Delete the existing final `return nil` line — the code above now returns.)

- [ ] **Step 3: Run build and tests**

Run: `go build ./cmd/agentdock/` and `go test ./cmd/agentdock/ -v`
Expected: succeeds. (No unit test for interactive prompt; covered by manual run.)

- [ ] **Step 4: Manual verification**

Run (interactive, requires TTY):

```
go run ./cmd/agentdock init app -c /tmp/test-app.yaml -i --force
```

Walk through prompts. At Mantis section, verify:
- Default Enter = no (skip Mantis setup entirely)
- Entering "y" triggers the base_url / token sub-prompts
- Bad token retries 3 times then offers skip

Delete `/tmp/test-app.yaml` after testing.

- [ ] **Step 5: Commit**

```bash
git add cmd/agentdock/init.go
git commit -m "feat(init): optional Mantis setup segment in 'init app --interactive'

Default is no (opt-in). On yes, validates base_url + api_token via
connectivity.CheckMantis. 3 retries then offers skip so the init
flow isn't held hostage by a misconfigured token."
```

---

## Phase 8 — Skill Bundle & Documentation

### Task 16: Bundle the mantis skill into `agents/skills/mantis/`

**Files:**
- Create: `agents/skills/mantis/SKILL.md`
- Create: `agents/skills/mantis/scripts/mantis.js`
- Create: `agents/skills/mantis/scripts/maintenance/bootstrap-project-metadata.mjs`
- Create: `agents/skills/mantis/scripts/maintenance/refresh-project-metadata.mjs`
- Create: `agents/skills/mantis/references/issue-workflow.md`
- Create: `agents/skills/mantis/references/metadata-maintenance.md`

**Prerequisites:** The source lives at `https://github.com/softleader/agent-skills` — specifically the `mantis` skill subdirectory. The engineer must have access to clone it. If not, ask the human for a local tarball or provide the files another way before proceeding.

- [ ] **Step 1: Obtain skill source**

```bash
git clone https://github.com/softleader/agent-skills /tmp/softleader-skills
```

- [ ] **Step 2: Copy skill into this repo**

```bash
mkdir -p agents/skills/mantis
cp -r /tmp/softleader-skills/mantis/* agents/skills/mantis/
```

Verify structure:

```bash
ls agents/skills/mantis/
# Should contain at minimum: SKILL.md scripts/ references/
ls agents/skills/mantis/scripts/
# Should contain: mantis.js (and possibly a maintenance/ subdir)
```

- [ ] **Step 3: Verify the skill loader picks it up**

Run:

```bash
go run ./cmd/agentdock app -c /nonexistent 2>&1 | head -20
```

(Expected: fails because config missing, but during startup the loader should have scanned `agents/skills/` and not errored on the new directory. If the scan logs an error about `agents/skills/mantis/`, fix the copy.)

Alternatively, write a one-shot test:

```bash
ls agents/skills/mantis/SKILL.md  # must exist for loadSingleBakedIn
head -5 agents/skills/mantis/SKILL.md  # should be YAML frontmatter with 'name: mantis'
```

- [ ] **Step 4: Commit**

```bash
git add agents/skills/mantis/
git commit -m "feat(skills): bundle mantis skill from softleader/agent-skills

Lets the agent fetch Mantis issue details on demand via env vars
MANTIS_API_URL and MANTIS_API_TOKEN (populated by Task 13 through
the existing Secrets bus)."
```

---

### Task 17: Point `triage-issue` at the `mantis` skill

**Files:**
- Modify: `agents/skills/triage-issue/SKILL.md`

- [ ] **Step 1: Find the "Process" / "Understand the problem" section**

Open `agents/skills/triage-issue/SKILL.md` and locate the `### 1. Understand the problem` heading.

- [ ] **Step 2: Insert a `### 1a.` subsection right after it**

Add (verbatim):

```markdown
### 1a. Fetch Mantis issue context (if applicable)

If the thread context contains Mantis issue URLs — patterns like
`view.php?id=<N>` or `/issues/<N>` — invoke the `mantis` skill to
fetch full issue details before proceeding with code investigation:

```bash
# 1. Verify connectivity (one-shot; skip the skill on failure)
node <skill-path>/mantis/scripts/mantis.js status

# 2. Fetch the full issue
node <skill-path>/mantis/scripts/mantis.js get-issue <N> --full

# 3. Optionally grab screenshots / attachments for visual bugs
node <skill-path>/mantis/scripts/mantis.js list-attachments <N>
node <skill-path>/mantis/scripts/mantis.js download-attachment <N> <file_id> --output /tmp/<name>
```

Incorporate the issue's description, severity, handler, and any
relevant attachment content (use Read on downloaded images) into
your root-cause analysis.

If `status` reports `reason=""` or `reason="auth_failed"`, Mantis
enrichment is unavailable — keep the URL in your output as-is and
proceed with the rest of triage from thread context alone.
```

- [ ] **Step 3: Commit**

```bash
git add agents/skills/triage-issue/SKILL.md
git commit -m "docs(skills/triage-issue): tell the agent to use the mantis skill

When the thread contains a Mantis URL, the agent should use the
mantis skill to fetch the issue before diving into the code. On
'skill unavailable' it should gracefully degrade and keep the URL."
```

---

### Task 18: Update user-facing docs

**Files:**
- Modify: `docs/configuration-app.md`
- Modify: `docs/configuration-app.en.md`
- Modify: `README.md`
- Modify: `README.en.md`
- Modify: `docs/deployment.md`

- [ ] **Step 1: Rewrite Mantis section in `docs/configuration-app.md`**

Find the `mantis:` yaml snippet (around line 42) and replace the surrounding section. The resulting section should read (fit style of rest of file):

```markdown
### Mantis（選用）

當 thread 中出現 Mantis issue URL（`view.php?id=` 或 `/issues/`），agent 會透過內建的
`mantis` skill 抓 issue title/description/附件。設定檔如下：

```yaml
mantis:
  base_url: https://mantis.example.com    # host 根即可，不必含 /api/rest
  api_token: <your-mantis-api-token>
```

兩個欄位必須同時填寫或同時留空；只填一個會在啟動時失敗。

**運作機制**：app 啟動時把 `base_url + /api/rest` 存入 `Secrets["MANTIS_API_URL"]`、
`api_token` 存入 `Secrets["MANTIS_API_TOKEN"]`，worker 在啟動 agent 子程序時把這兩個值當 env
var 推入。mantis skill 讀 env，agent 看到 thread 裡的 Mantis URL 就主動呼叫 skill 擷取內容。

**Basic auth 已移除**：bundled skill 只支援 API token，過去保留的 `username`/`password`
欄位已刪除。若你的 Mantis 版本太舊不支援 API token，請升級 Mantis 或留空 Mantis 區段（不啟用）。

**未配置行為**：agent 仍會看到 thread 裡的 Mantis URL，只是不會擷取內容——URL 照原樣留在
輸出。
```

- [ ] **Step 2: Mirror the change in `docs/configuration-app.en.md`**

Write an English equivalent of the above section and replace the same region.

- [ ] **Step 3: Add Features line to `README.md` and `README.en.md`**

Find the Features (`## Features` or similar) list and add a bullet:

- zh-TW: `- **Mantis issue lookup** (選用): 配好 `mantis.base_url` + `mantis.api_token` 後，agent 會自動擷取 Mantis issue 內容`
- en: `- **Mantis issue lookup** (optional): once \`mantis.base_url\` and \`mantis.api_token\` are configured, the agent fetches Mantis issue details and attachments on demand`

- [ ] **Step 4: Add Node prerequisite to `docs/deployment.md`**

Find the prerequisites / system requirements section. Add:

```markdown
- **Node.js 18+** (required on worker hosts for the bundled `mantis` skill; only relevant if Mantis is configured)
```

- [ ] **Step 5: Run full test suite**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add docs/configuration-app.md docs/configuration-app.en.md \
        README.md README.en.md docs/deployment.md
git commit -m "docs: mantis skill-based extraction (replaces backend enrichment)

Rewrites the mantis config section to explain the new flow: config ->
Secrets -> worker env -> mantis skill -> agent. Adds Node 18+ as a
worker host prerequisite. README features list gains an optional
Mantis entry."
```

---

## Final Verification

After all tasks complete:

- [ ] **Step 1: Full test suite**

```bash
go test ./...
```
Expected: PASS across all modules.

- [ ] **Step 2: Full build**

```bash
go build ./...
```
Expected: clean build.

- [ ] **Step 3: Import direction enforcement**

```bash
go test ./test -run TestImportDirection -v
```
Expected: PASS. No `app ↔ worker` imports added.

- [ ] **Step 4: Lint**

```bash
go vet ./...
```
Expected: zero findings.

- [ ] **Step 5: Visual grep for orphaned references**

```
grep -rn 'mantis\.Client\|app/mantis\|enrichMessage\|mantisClient\|mantis.NewClient' --include='*.go' .
```
Expected: zero matches (all removed cleanly).

- [ ] **Step 6: Manual smoke test (optional — requires real Slack/Mantis env)**

1. Start Redis and worker in separate terminals (or use docker-compose).
2. Start `agentdock app` with Mantis configured.
3. In a Slack channel where the bot is invited, post a message containing a Mantis URL.
4. `@bot` in the thread.
5. Verify in worker logs that the agent invoked `mantis` skill (look for `node <path>/mantis.js` in agent output).
6. Verify in the bot's Slack reply that Mantis issue title/description was incorporated.
7. Repeat with a thread that has no Mantis URL → agent should not invoke the skill.
8. Stop the app with Mantis unconfigured → threads with Mantis URLs still get triaged, but URL stays as text.

Document any issues in a follow-up PR comment, not in this plan.
