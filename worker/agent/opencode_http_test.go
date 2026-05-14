package agent

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Ivantseng123/agentdock/shared/queue"
)

func TestClient_CreateSession_HappyPath(t *testing.T) {
	var seenDir, seenAuth atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/session" {
			if r.Header.Get(clientDirectoryHeader) == "/tmp/work" {
				seenDir.Store(true)
			}
			u, p, ok := r.BasicAuth()
			if ok && u == supervisorAuthUsername && p == "secret" {
				seenAuth.Store(true)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"id":"ses_test_001"}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "secret", nil)
	sid, err := c.CreateSession(context.Background(), "/tmp/work")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if sid != "ses_test_001" {
		t.Errorf("sid = %q, want ses_test_001", sid)
	}
	if !seenDir.Load() {
		t.Error("server never saw x-opencode-directory header")
	}
	if !seenAuth.Load() {
		t.Error("server never saw Basic auth with opencode + secret")
	}
}

func TestClient_CreateSession_Non200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"directory not found"}`, http.StatusBadRequest)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "secret", nil)
	_, err := c.CreateSession(context.Background(), "/nonexistent")
	if err == nil {
		t.Fatal("expected error on non-200")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error %q does not surface status code", err.Error())
	}
}

func TestClient_CreateSession_MissingID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{}`)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "secret", nil)
	_, err := c.CreateSession(context.Background(), "/tmp/work")
	if err == nil {
		t.Fatal("expected error on missing id")
	}
}

func TestClient_SendPrompt_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/event":
			writeSSELines(w, r, []string{
				`{"id":"e1","type":"server.connected","properties":{}}`,
				`{"id":"e2","type":"message.part.updated","properties":{"part":{"type":"text","text":"Hi there"}}}`,
				`{"id":"e3a","type":"message.part.updated","properties":{"part":{"type":"tool","tool":"read","state":{"status":"pending"}}}}`,
				`{"id":"e3b","type":"message.part.updated","properties":{"part":{"type":"tool","tool":"read","state":{"status":"running","input":{"file_path":"/etc/hostname"}}}}}`,
				`{"id":"e3c","type":"message.part.updated","properties":{"part":{"type":"tool","tool":"read","state":{"status":"completed","input":{"file_path":"/etc/hostname"}}}}}`,
				`{"id":"e4","type":"message.part.updated","properties":{"part":{"type":"step-finish","tokens":{"input":5,"output":3},"cost":0.0011}}}`,
				`{"id":"e5","type":"server.heartbeat","properties":{}}`,
			})
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/session/") && strings.HasSuffix(r.URL.Path, "/message"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"info":{"finish":"stop","tokens":{"input":5,"output":3}},"parts":[{"type":"text","text":"final answer"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "secret", nil)
	run, err := c.SendPrompt(context.Background(), "ses_test_001", "/tmp/work", "ask me")
	if err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}

	var collected []queue.StreamEvent
	collectDone := make(chan struct{})
	go func() {
		for ev := range run.Events {
			collected = append(collected, ev)
		}
		close(collectDone)
	}()

	text, err := run.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if text != "final answer" {
		t.Errorf("final text = %q, want %q", text, "final answer")
	}
	<-collectDone

	seenDelta, seenTool, seenResult := false, false, false
	for _, e := range collected {
		switch e.Type {
		case "message_delta":
			seenDelta = true
			if e.TextBytes != len("Hi there") {
				t.Errorf("message_delta TextBytes = %d, want %d", e.TextBytes, len("Hi there"))
			}
		case "tool_use":
			seenTool = true
			if e.ToolName != "Read" || e.ToolInputFirstArg != "/etc/hostname" {
				t.Errorf("tool_use = %+v, want Read/etc/hostname", e)
			}
		case "result":
			seenResult = true
			if e.InputTokens != 5 || e.OutputTokens != 3 {
				t.Errorf("result tokens = (%d,%d), want (5,3)", e.InputTokens, e.OutputTokens)
			}
			if e.CostUSD != 0.0011 {
				t.Errorf("result CostUSD = %v, want 0.0011", e.CostUSD)
			}
		}
	}
	if !seenDelta {
		t.Error("missing message_delta event")
	}
	if !seenTool {
		t.Error("missing tool_use event")
	}
	if !seenResult {
		t.Error("missing result event")
	}
}

func TestClient_SendPrompt_POSTError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/event":
			writeSSELines(w, r, []string{`{"id":"e1","type":"server.connected","properties":{}}`})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/message"):
			http.Error(w, `{"error":"backend"}`, http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "secret", nil)
	run, err := c.SendPrompt(context.Background(), "ses_001", "/tmp/work", "x")
	if err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	defer run.Close()
	// drain events so SSE goroutine doesn't block
	go func() {
		for range run.Events {
		}
	}()

	_, err = run.Wait()
	if err == nil {
		t.Fatal("expected error from POST /session/{id}/message")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error %q does not surface 500 status", err.Error())
	}
}

func TestClient_SendPrompt_SSEErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/event":
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/message"):
			// hang forever — SSE setup error should fail the call
			// outright (we never finished subscribing, so there's no
			// SSE side to fall back on).
			<-r.Context().Done()
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "secret", nil)
	_, err := c.SendPrompt(context.Background(), "ses_001", "/tmp/work", "x")
	if err == nil {
		t.Fatal("expected error when /event returns non-200 at subscription time")
	}
}

// TestClient_SendPrompt_CumulativeResultEmittedOnce verifies the M3
// fix: per-step `result` events from `step-finish` SSE updates are
// accumulated inside the SSE consumer and surfaced as exactly ONE
// terminal `result` event at stream end with summed tokens / cost.
// Without this, worker/pool/status.go's overwrite-on-result records
// only the last step's metrics.
func TestClient_SendPrompt_CumulativeResultEmittedOnce(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/event":
			writeSSELines(w, r, []string{
				`{"id":"e1","type":"server.connected","properties":{}}`,
				`{"id":"e2","type":"message.part.updated","properties":{"part":{"type":"step-finish","tokens":{"input":5,"output":3},"cost":0.001}}}`,
				`{"id":"e3","type":"message.part.updated","properties":{"part":{"type":"step-finish","tokens":{"input":7,"output":4},"cost":0.002}}}`,
				`{"id":"e4","type":"message.part.updated","properties":{"part":{"type":"step-finish","tokens":{"input":2,"output":1},"cost":0.0005}}}`,
			})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/message"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"info":{"finish":"stop","tokens":{"input":14,"output":8}},"parts":[{"type":"text","text":"answer"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "secret", nil)
	run, err := c.SendPrompt(context.Background(), "ses_001", "/tmp/work", "x")
	if err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}

	var results []queue.StreamEvent
	collectDone := make(chan struct{})
	go func() {
		for ev := range run.Events {
			if ev.Type == "result" {
				results = append(results, ev)
			}
		}
		close(collectDone)
	}()

	_, err = run.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	<-collectDone

	if len(results) != 1 {
		t.Fatalf("expected exactly 1 cumulative result event, got %d: %+v", len(results), results)
	}
	got := results[0]
	if got.InputTokens != 14 {
		t.Errorf("cumulative InputTokens = %d, want 14 (5+7+2)", got.InputTokens)
	}
	if got.OutputTokens != 8 {
		t.Errorf("cumulative OutputTokens = %d, want 8 (3+4+1)", got.OutputTokens)
	}
	if got.CostUSD < 0.00349 || got.CostUSD > 0.00351 {
		t.Errorf("cumulative CostUSD = %f, want ~0.0035", got.CostUSD)
	}
}

// TestClient_SendPrompt_SSECloseEarly_POSTAuthoritative verifies the
// M1 fix: SSE stream closing before POST returns is informational
// (logged via SSEErrors), not fatal. Wait() blocks on the POST
// response and returns the final text.
func TestClient_SendPrompt_SSECloseEarly_POSTAuthoritative(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/event":
			// Write one event then return immediately, closing SSE
			// stream before POST finishes (simulates floor+1
			// regression or proxy disconnect).
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher, _ := w.(http.Flusher)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", `{"id":"e1","type":"server.connected","properties":{}}`)
			if flusher != nil {
				flusher.Flush()
			}
			// return → stream ends
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/message"):
			// Simulate server taking time to compute final answer
			// AFTER SSE has already closed.
			select {
			case <-time.After(200 * time.Millisecond):
			case <-r.Context().Done():
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"info":{"finish":"stop","tokens":{"output":2}},"parts":[{"type":"text","text":"late answer"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "secret", nil)
	run, err := c.SendPrompt(context.Background(), "ses_001", "/tmp/work", "x")
	if err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	// drain events + collect SSE errors
	var sseErrs []error
	collectDone := make(chan struct{})
	go func() {
		defer close(collectDone)
		for {
			select {
			case _, ok := <-run.Events:
				if !ok {
					return
				}
			case err, ok := <-run.SSEErrors:
				if !ok {
					continue
				}
				sseErrs = append(sseErrs, err)
			}
		}
	}()

	text, err := run.Wait()
	if err != nil {
		t.Fatalf("Wait should succeed despite SSE close: %v", err)
	}
	if text != "late answer" {
		t.Errorf("text = %q, want %q (POST is authoritative)", text, "late answer")
	}
	<-collectDone
	if len(sseErrs) == 0 {
		t.Log("SSE error not surfaced; acceptable if scanner saw clean EOF")
	}
}

func TestClient_SendPrompt_CloseIdempotent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/event":
			writeSSELines(w, r, []string{`{"id":"e1","type":"server.connected","properties":{}}`})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/message"):
			<-r.Context().Done()
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "secret", nil)
	run, err := c.SendPrompt(context.Background(), "ses_001", "/tmp/work", "x")
	if err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	go func() {
		for range run.Events {
		}
	}()
	run.Close()
	run.Close()
	run.Close()
}

func TestClient_SendPrompt_EmptySessionID(t *testing.T) {
	c := NewClient("http://127.0.0.1:1", "secret", nil)
	_, err := c.SendPrompt(context.Background(), "", "/tmp/work", "x")
	if err == nil {
		t.Fatal("expected error on empty sessionID")
	}
}

// writeSSELines emits lines as `data: <line>\n\n` and holds the
// connection open until the request context is canceled (matches real
// SSE handler shape).
func writeSSELines(w http.ResponseWriter, r *http.Request, lines []string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	for _, line := range lines {
		_, _ = fmt.Fprintf(w, "data: %s\n\n", line)
		if flusher != nil {
			flusher.Flush()
		}
	}
	select {
	case <-r.Context().Done():
	case <-time.After(5 * time.Second):
		// safety belt so the test can never wedge if a client
		// forgets to cancel.
	}
}
