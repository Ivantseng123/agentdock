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
				`{"id":"e3","type":"message.part.updated","properties":{"part":{"type":"tool","tool":"read","state":{"input":{"file_path":"/etc/hostname"}}}}}`,
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
			// hang forever — SSE error should preempt
			<-r.Context().Done()
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "secret", nil)
	_, err := c.SendPrompt(context.Background(), "ses_001", "/tmp/work", "x")
	if err == nil {
		t.Fatal("expected error when /event returns non-200")
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
