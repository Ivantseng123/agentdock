package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/Ivantseng123/agentdock/shared/queue"
)

// HTTP/SSE client for `opencode serve`. Wraps an http.Client with
// (a) CreateSession, which POSTs /session and returns the new sessionID,
// and (b) SendPrompt, which opens /event SSE in parallel with
// POST /session/{id}/message and exposes both event stream and final
// assistant text via PromptRun.
//
// Plan Task 3.2-6 acceptance writes the signature as
// `SendPrompt → (<-chan queue.StreamEvent, error)`. We deviate slightly:
// SendPrompt returns `*PromptRun`, with `Events <-chan queue.StreamEvent`
// as a struct field plus `Wait()` for the final assembled text. The
// channel-only signature would force runOneServer to either (a) ditch
// the final text from the POST response (lossy — POST body carries the
// authoritative answer and Bug A diagnostics for Task 3.2-11), or (b)
// accumulate text from SSE deltas (lossy too — server emits the final
// text part once via `time.end`, not via incremental deltas). Wrapping
// channel + Wait() preserves both. See Stage 2 manifest §3.5 +
// PR description for the spec/plan drift note.

// Session-create + prompt JSON bodies. Field names match opencode 1.14.41
// server schema (`SessionRoutes.create` and `SessionRoutes.prompt`); see
// docs/specs/opencode-server-mode-poc-report.md P8 for breaking-change
// verdict.

const (
	clientAgentName         = "build"
	clientSessionTitle      = "agentdock-worker"
	clientDirectoryHeader   = "X-Opencode-Directory"
	clientSSEDataLinePrefix = "data: "
	clientEventChannelSize  = 256
)

// Client wraps an *http.Client with the supervisor's BaseURL and the
// random OPENCODE_SERVER_PASSWORD. One Client per worker process is
// sufficient: opencode serve loads per-Instance state on demand via the
// `x-opencode-directory` header, so per-job workDir isolation happens
// at request time, not Client-construction time.
type Client struct {
	baseURL    string
	password   string
	httpClient *http.Client
}

// NewClient constructs a Client. Pass the supervisor's BaseURL (e.g.
// `http://127.0.0.1:50000`) and Password.
//
// `httpClient` may be nil; default is a no-timeout http.Client because
// the SSE GET /event is long-lived (context cancellation drives
// teardown, not Client.Timeout — a non-zero Timeout would slam the SSE
// stream after the elapsed period regardless of progress).
func NewClient(baseURL, password string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		password:   password,
		httpClient: httpClient,
	}
}

// CreateSession POSTs /session with `{"title":"agentdock-worker"}` and
// the `x-opencode-directory: directory` header. Returns the new session
// ID. Used by runOneServer once per ask job; the returned sessionID is
// then passed to SendPrompt for that same job.
func (c *Client) CreateSession(ctx context.Context, directory string) (string, error) {
	body := bytes.NewBufferString(fmt.Sprintf(`{"title":%q}`, clientSessionTitle))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/session", body)
	if err != nil {
		return "", fmt.Errorf("build create-session request: %w", err)
	}
	req.SetBasicAuth(supervisorAuthUsername, c.password)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(clientDirectoryHeader, directory)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("create-session request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		tail, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("create-session status %d: %s", resp.StatusCode, strings.TrimSpace(string(tail)))
	}

	var out struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("decode create-session response: %w", err)
	}
	if out.ID == "" {
		return "", errors.New("create-session response missing id")
	}
	return out.ID, nil
}

// PromptRun is the handle returned by SendPrompt. It owns two
// goroutines: an SSE reader that translates BusEvent payloads into
// queue.StreamEvent via decodeOpencodeBusEvent, and a POST worker that
// blocks until the server returns the final AssistantMessage. Caller
// must call Wait or Close before discarding the run.
type PromptRun struct {
	Events <-chan queue.StreamEvent

	cancel       context.CancelFunc
	sseErrCh     <-chan error
	responseCh   <-chan promptResponse
	closeOnce    sync.Once
	closedSignal chan struct{}
}

type promptResponse struct {
	finalText string
	finish    string
	tokensOut int
	err       error
}

// Wait blocks until the POST /session/{id}/message request completes,
// returning the assembled final assistant text on success. If the SSE
// stream errors first (e.g. transport close, /event 4xx) Wait surfaces
// that error instead. Wait always cancels both goroutines on return —
// callers do not need to defer Close after a successful Wait.
//
// Bug A detection (Task 3.2-11) will read `finish` and `tokensOut` on
// the same response; Stage 2 only exposes `finalText` + transport
// errors here. promptResponse keeps those fields populated so 3.2-11
// can lift the discriminator in without expanding this signature.
func (r *PromptRun) Wait() (string, error) {
	defer r.Close()
	select {
	case resp, ok := <-r.responseCh:
		if !ok {
			return "", errors.New("opencode prompt response channel closed before sending")
		}
		if resp.err != nil {
			return "", resp.err
		}
		return resp.finalText, nil
	case sseErr := <-r.sseErrCh:
		return "", fmt.Errorf("opencode /event SSE: %w", sseErr)
	}
}

// Close cancels both goroutines and waits for them to exit. Idempotent.
func (r *PromptRun) Close() {
	r.cancel()
	r.cleanup()
}

func (r *PromptRun) cleanup() {
	r.closeOnce.Do(func() { close(r.closedSignal) })
}

// SendPrompt subscribes to /event SSE first, then POSTs
// /session/{id}/message with `agent: build` + text prompt. Returns a
// PromptRun whose Events channel emits queue.StreamEvent translated
// from server BusEvents, and whose Wait method blocks for the final
// assistant text.
//
// `directory` populates the `x-opencode-directory` header on both
// GET /event and POST /session/{id}/message so per-job workDir
// isolation reaches the server consistently.
func (c *Client) SendPrompt(ctx context.Context, sessionID, directory, prompt string) (*PromptRun, error) {
	if sessionID == "" {
		return nil, errors.New("SendPrompt: empty sessionID")
	}

	runCtx, cancel := context.WithCancel(ctx)
	events := make(chan queue.StreamEvent, clientEventChannelSize)
	sseErrCh := make(chan error, 1)
	if err := c.subscribeEvents(runCtx, directory, events, sseErrCh); err != nil {
		cancel()
		return nil, err
	}

	responseCh := make(chan promptResponse, 1)
	closedSignal := make(chan struct{})
	go func() {
		defer close(responseCh)
		resp := c.postPromptMessage(runCtx, sessionID, directory, prompt)
		select {
		case responseCh <- resp:
		case <-closedSignal:
		}
	}()

	return &PromptRun{
		Events:       events,
		cancel:       cancel,
		sseErrCh:     sseErrCh,
		responseCh:   responseCh,
		closedSignal: closedSignal,
	}, nil
}

// subscribeEvents fires GET /event, drains SSE lines on a goroutine,
// strips the `data: ` prefix and pushes decoded events to `events`.
// Recoverable signals (heartbeats, unmapped event types) silently
// dropped. Errors surface via sseErrCh.
func (c *Client) subscribeEvents(ctx context.Context, directory string, events chan<- queue.StreamEvent, sseErrCh chan<- error) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/event", nil)
	if err != nil {
		return fmt.Errorf("build event request: %w", err)
	}
	req.SetBasicAuth(supervisorAuthUsername, c.password)
	req.Header.Set(clientDirectoryHeader, directory)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("event request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		tail, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("event status %d: %s", resp.StatusCode, strings.TrimSpace(string(tail)))
	}

	go func() {
		defer close(events)
		defer resp.Body.Close()
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, clientSSEDataLinePrefix) {
				continue
			}
			raw := []byte(strings.TrimPrefix(line, clientSSEDataLinePrefix))
			ev, ok, decErr := decodeOpencodeBusEvent(raw)
			if decErr != nil {
				trySendErr(sseErrCh, fmt.Errorf("decode SSE event: %w", decErr))
				return
			}
			if !ok {
				continue
			}
			select {
			case events <- ev:
			case <-ctx.Done():
				return
			}
		}
		if err := scanner.Err(); err != nil {
			trySendErr(sseErrCh, fmt.Errorf("scan SSE stream: %w", err))
			return
		}
		if ctx.Err() != nil {
			return
		}
		trySendErr(sseErrCh, errors.New("SSE stream closed unexpectedly"))
	}()
	return nil
}

// postPromptMessage POSTs `/session/{id}/message` with the prompt
// payload and decodes the AssistantMessage response. The response body
// carries the final assistant parts; we join all `type=text` parts as
// the final text (matching POC AssistantMessage.Text()), and surface
// finish + output token count for Task 3.2-11 Bug A detection.
func (c *Client) postPromptMessage(ctx context.Context, sessionID, directory, prompt string) promptResponse {
	payload := map[string]any{
		"agent": clientAgentName,
		"parts": []map[string]any{
			{"type": "text", "text": prompt},
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return promptResponse{err: fmt.Errorf("marshal prompt payload: %w", err)}
	}

	url := fmt.Sprintf("%s/session/%s/message", c.baseURL, sessionID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return promptResponse{err: fmt.Errorf("build prompt request: %w", err)}
	}
	req.SetBasicAuth(supervisorAuthUsername, c.password)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(clientDirectoryHeader, directory)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return promptResponse{err: fmt.Errorf("prompt request: %w", err)}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		tail, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return promptResponse{err: fmt.Errorf("prompt status %d: %s", resp.StatusCode, strings.TrimSpace(string(tail)))}
	}

	var body struct {
		Info struct {
			Finish string `json:"finish"`
			Tokens *struct {
				Input  int `json:"input"`
				Output int `json:"output"`
			} `json:"tokens,omitempty"`
		} `json:"info"`
		Parts []struct {
			Type string `json:"type"`
			Text string `json:"text,omitempty"`
		} `json:"parts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return promptResponse{err: fmt.Errorf("decode prompt response: %w", err)}
	}

	var b strings.Builder
	for _, p := range body.Parts {
		if p.Type == "text" {
			b.WriteString(p.Text)
		}
	}
	out := promptResponse{
		finalText: b.String(),
		finish:    body.Info.Finish,
	}
	if body.Info.Tokens != nil {
		out.tokensOut = body.Info.Tokens.Output
	}
	return out
}

func trySendErr(ch chan<- error, err error) {
	select {
	case ch <- err:
	default:
	}
}
