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
	"sync/atomic"

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
//
// SSEErrors carries non-fatal SSE-side errors (transport disconnects,
// scanner failures). Wait does NOT short-circuit on these — POST is
// the authoritative completion signal per spec line 92. Callers
// (runOneServer) should drain SSEErrors at warn level so operators
// can correlate SSE disruption with downstream issues without aborting
// otherwise-successful POST requests. (Cross-review M1 fix: SSE close
// alone must not fail a job whose POST is still computing.)
//
// Finish / TokensOut / SawTextPart expose the three signals Stage 3
// Task 3.2-11's Bug A detector AND-gates on. Finish and TokensOut are
// cached from the POST response by Wait; SawTextPart is set by the
// SSE consumer when any `message_delta` event is forwarded. Always
// call Wait first, then the accessors — pre-Wait reads return zero
// values.
type PromptRun struct {
	Events    <-chan queue.StreamEvent
	SSEErrors <-chan error

	cancel       context.CancelFunc
	responseCh   <-chan promptResponse
	closeOnce    sync.Once
	closedSignal chan struct{}

	sawText *atomic.Bool

	respMu        sync.Mutex
	lastFinish    string
	lastTokensOut int
}

type promptResponse struct {
	finalText string
	finish    string
	tokensOut int
	err       error
}

// Wait blocks until the POST /session/{id}/message request completes,
// returning the assembled final assistant text on success. POST is
// authoritative; SSE-side errors do not abort Wait — they surface via
// PromptRun.SSEErrors so the caller can log them without failing the
// request. Wait always cancels both goroutines on return; callers do
// not need to defer Close after a successful Wait.
//
// Caches `finish` and `tokensOut` into PromptRun so the Bug A detector
// in `runOneServer` can read them after Wait without expanding this
// signature.
func (r *PromptRun) Wait() (string, error) {
	defer r.Close()
	resp, ok := <-r.responseCh
	if !ok {
		return "", errors.New("opencode prompt response channel closed before sending")
	}
	r.respMu.Lock()
	r.lastFinish = resp.finish
	r.lastTokensOut = resp.tokensOut
	r.respMu.Unlock()
	if resp.err != nil {
		return "", resp.err
	}
	return resp.finalText, nil
}

// Finish returns the POST response's `info.finish` reason, cached by
// Wait. Returns "" before Wait completes or on a transport-error path
// (no body decoded). Stage 3 Task 3.2-11 reads "other" here as one of
// three Bug A conditions.
func (r *PromptRun) Finish() string {
	r.respMu.Lock()
	defer r.respMu.Unlock()
	return r.lastFinish
}

// TokensOut returns the POST response's `info.tokens.output`, cached
// by Wait. Returns 0 before Wait completes or when the body omits the
// tokens block. Stage 3 Task 3.2-11 reads 0 here as one of three Bug
// A conditions.
func (r *PromptRun) TokensOut() int {
	r.respMu.Lock()
	defer r.respMu.Unlock()
	return r.lastTokensOut
}

// SawTextPart returns true when the SSE consumer forwarded at least
// one `message_delta` event during this run. Used by Bug A detection
// (Task 3.2-11) — when finish=other + tokens=0 + !SawTextPart all
// hold, the run is the silent-drop signature and the job is failed
// with the user-facing copy "LLM 回應為空，請稍後再試或改用 @bot issue".
func (r *PromptRun) SawTextPart() bool {
	if r.sawText == nil {
		return false
	}
	return r.sawText.Load()
}

// Close cancels both goroutines but does NOT block waiting for their
// exit. Callers that need synchronous teardown should call Wait first,
// which drains responseCh and the deferred Close runs after.
// Idempotent — safe to call multiple times.
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
	sawText := &atomic.Bool{}
	if err := c.subscribeEvents(runCtx, directory, events, sseErrCh, sawText); err != nil {
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
		SSEErrors:    sseErrCh,
		cancel:       cancel,
		responseCh:   responseCh,
		closedSignal: closedSignal,
		sawText:      sawText,
	}, nil
}

// subscribeEvents fires GET /event, drains SSE lines on a goroutine,
// strips the `data: ` prefix and pushes decoded events to `events`.
// Recoverable signals (heartbeats, intermediate tool states) silently
// dropped. Per-step `result` events are accumulated internally and
// surfaced as ONE cumulative terminal `result` event on goroutine
// exit, so worker/pool/status.go's overwrite-on-result semantics
// record full-job tokens / cost instead of just the last step
// (cross-review M3 fix).
//
// SSE-side errors (scanner failures, decode errors, premature stream
// close) are reported through sseErrCh as informational — POST is the
// authoritative completion signal and PromptRun.Wait does not short-
// circuit on these (cross-review M1 fix). Callers (runOneServer)
// should log them at warn level so an operator can correlate SSE
// disruption with downstream behavior.
func (c *Client) subscribeEvents(ctx context.Context, directory string, events chan<- queue.StreamEvent, sseErrCh chan<- error, sawText *atomic.Bool) error {
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
		var (
			cumInputTokens  int
			cumOutputTokens int
			cumCostUSD      float64
			hasStepFinish   bool
			sentTerminal    bool
		)
		emitTerminal := func() {
			if sentTerminal || !hasStepFinish {
				return
			}
			sentTerminal = true
			select {
			case events <- queue.StreamEvent{
				Type:         "result",
				InputTokens:  cumInputTokens,
				OutputTokens: cumOutputTokens,
				CostUSD:      cumCostUSD,
			}:
			default:
				// events buffer (clientEventChannelSize=256) is sized
				// well above realistic per-job event counts; default
				// case is paranoia for the impossible-in-practice case
				// where drainer has stopped reading but channel isn't
				// closed yet. Drop is acceptable — POST response also
				// carries `tokens.output`, so cost data isn't unique
				// to this event.
			}
		}
		defer close(events)
		defer resp.Body.Close()
		defer emitTerminal()

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
			if ev.Type == "result" {
				cumInputTokens += ev.InputTokens
				cumOutputTokens += ev.OutputTokens
				cumCostUSD += ev.CostUSD
				hasStepFinish = true
				continue
			}
			// Track that at least one text part landed via SSE; Stage 3
			// Task 3.2-11's Bug A detector AND-gates on the absence of
			// any text part as one of its three conditions.
			if ev.Type == "message_delta" && sawText != nil {
				sawText.Store(true)
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
		// SSE close without ctx cancellation: surface as informational
		// SSE error. Wait() does NOT abort on these; runOneServer logs
		// them at warn level. Floor-version (1.14.41) keeps SSE open
		// for the duration of the prompt, so seeing this in practice
		// indicates either a proxy/middlebox disconnect, a worker
		// upgrading past floor and hitting the 1.14.42+ SSE-close
		// regression documented in POC report § Historical bisect, or
		// something else worth flagging.
		if ctx.Err() == nil {
			trySendErr(sseErrCh, errors.New("SSE stream closed before request completion (POST is authoritative)"))
		}
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
