package agent

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Ivantseng123/agentdock/shared/queue"
	"github.com/Ivantseng123/agentdock/worker/config"
)

// primedSupervisor builds a Supervisor in the running state pointed
// at a test HTTP server. Bypasses the real `opencode serve` spawn so
// runOneServer can be exercised end-to-end against httptest fakes
// without a real opencode binary. Test-only — production boot uses
// NewSupervisor + lazy Acquire.
func primedSupervisor(baseURL, password string, pid int) *Supervisor {
	return &Supervisor{
		baseURL:        baseURL,
		password:       password,
		pid:            pid,
		state:          stateRunning,
		activeSessions: make(map[string]struct{}),
	}
}

func TestRunOneServer_NilSupervisorGuard(t *testing.T) {
	r := &Runner{opencodeCfg: config.OpencodeConfig{Mode: config.OpencodeModeServer}}
	_, err := r.runOneServer(
		context.Background(),
		slog.Default(),
		config.AgentConfig{Command: "opencode"},
		t.TempDir(),
		"prompt",
		RunOptions{},
	)
	if err == nil {
		t.Fatal("expected nil-supervisor error")
	}
	if !strings.Contains(err.Error(), "supervisor not initialized") {
		t.Errorf("error %q missing nil-supervisor marker", err.Error())
	}
}

func TestRunOneServer_HappyPath_FiresAllCallbacks(t *testing.T) {
	srv := newFakeOpencodeServer(t, fakeOpencodeBehavior{
		sessionID:    "ses_runOne_test",
		finalText:    "final answer",
		finishReason: "stop",
		outputTokens: 3,
		sseEvents: []string{
			`{"id":"e1","type":"server.connected","properties":{}}`,
			`{"id":"e2","type":"message.part.updated","properties":{"part":{"type":"text","text":"hi"}}}`,
			`{"id":"e3a","type":"message.part.updated","properties":{"part":{"type":"tool","tool":"read","state":{"status":"pending"}}}}`,
			`{"id":"e3b","type":"message.part.updated","properties":{"part":{"type":"tool","tool":"read","state":{"status":"running","input":{"file_path":"/etc/hosts"}}}}}`,
			`{"id":"e3c","type":"message.part.updated","properties":{"part":{"type":"tool","tool":"read","state":{"status":"completed","input":{"file_path":"/etc/hosts"}}}}}`,
			`{"id":"e4","type":"message.part.updated","properties":{"part":{"type":"step-finish","tokens":{"input":2,"output":3},"cost":0.0005}}}`,
		},
	})
	defer srv.Close()

	sup := primedSupervisor(srv.URL, "secret", 54321)
	r := &Runner{
		opencodeCfg: config.OpencodeConfig{Mode: config.OpencodeModeServer},
		supervisor:  sup,
	}

	var (
		startedPID atomic.Int64
		startedCmd atomic.Value
		exitCode   atomic.Int64
		eventCount atomic.Int64
		eventsMu   sync.Mutex
		events     []queue.StreamEvent
	)
	startedCmd.Store("")
	exitCode.Store(-99)

	opts := RunOptions{
		OnStarted: func(pid int, command string) {
			startedPID.Store(int64(pid))
			startedCmd.Store(command)
		},
		OnEvent: func(ev queue.StreamEvent) {
			eventsMu.Lock()
			events = append(events, ev)
			eventsMu.Unlock()
			eventCount.Add(1)
		},
		OnExit: func(code int) {
			exitCode.Store(int64(code))
		},
	}

	output, err := r.runOneServer(
		context.Background(),
		slog.Default(),
		config.AgentConfig{Command: "opencode"},
		"/tmp/workdir",
		"please answer",
		opts,
	)
	if err != nil {
		t.Fatalf("runOneServer: %v", err)
	}
	if output != "final answer" {
		t.Errorf("output = %q, want %q", output, "final answer")
	}
	if startedPID.Load() != 54321 {
		t.Errorf("OnStarted PID = %d, want 54321", startedPID.Load())
	}
	if startedCmd.Load().(string) != "opencode" {
		t.Errorf("OnStarted command = %q, want opencode", startedCmd.Load().(string))
	}
	if exitCode.Load() != 0 {
		t.Errorf("OnExit code = %d, want 0", exitCode.Load())
	}
	if eventCount.Load() < 3 {
		t.Errorf("event count = %d, want ≥ 3 (delta + tool + result)", eventCount.Load())
	}
	eventsMu.Lock()
	defer eventsMu.Unlock()
	seenDelta, seenTool, seenResult := false, false, false
	for _, ev := range events {
		switch ev.Type {
		case "message_delta":
			seenDelta = true
		case "tool_use":
			seenTool = true
			if ev.ToolName != "Read" {
				t.Errorf("tool_use ToolName = %q, want Read", ev.ToolName)
			}
		case "result":
			seenResult = true
		}
	}
	if !seenDelta || !seenTool || !seenResult {
		t.Errorf("missing event types: delta=%v tool=%v result=%v", seenDelta, seenTool, seenResult)
	}
}

func TestRunOneServer_CreateSessionError_OnExitMinusOne(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"directory missing"}`, http.StatusBadRequest)
	}))
	defer srv.Close()

	sup := primedSupervisor(srv.URL, "secret", 11111)
	r := &Runner{
		opencodeCfg: config.OpencodeConfig{Mode: config.OpencodeModeServer},
		supervisor:  sup,
	}
	var exitCode atomic.Int64
	exitCode.Store(-99)
	_, err := r.runOneServer(
		context.Background(),
		slog.Default(),
		config.AgentConfig{Command: "opencode"},
		"/nonexistent",
		"prompt",
		RunOptions{
			OnExit: func(code int) { exitCode.Store(int64(code)) },
		},
	)
	if err == nil {
		t.Fatal("expected CreateSession error")
	}
	if exitCode.Load() != -1 {
		t.Errorf("OnExit code = %d, want -1 (transport-layer error)", exitCode.Load())
	}
}

func TestRunOneServer_POSTMessageError_OnExitMinusOne(t *testing.T) {
	srv := newFakeOpencodeServer(t, fakeOpencodeBehavior{
		sessionID:        "ses_post_err",
		failPostMessage:  true,
		failPostStatus:   500,
		failPostBody:     `{"error":"backend exploded"}`,
		sseEvents:        []string{`{"id":"e1","type":"server.connected","properties":{}}`},
	})
	defer srv.Close()

	sup := primedSupervisor(srv.URL, "secret", 22222)
	r := &Runner{
		opencodeCfg: config.OpencodeConfig{Mode: config.OpencodeModeServer},
		supervisor:  sup,
	}
	var exitCode atomic.Int64
	exitCode.Store(-99)
	_, err := r.runOneServer(
		context.Background(),
		slog.Default(),
		config.AgentConfig{Command: "opencode"},
		"/tmp/work",
		"prompt",
		RunOptions{
			OnExit: func(code int) { exitCode.Store(int64(code)) },
		},
	)
	if err == nil {
		t.Fatal("expected POST /session/{id}/message error")
	}
	if exitCode.Load() != -1 {
		t.Errorf("OnExit code = %d, want -1", exitCode.Load())
	}
}

// TestRunOneServer_SSECloseEarly_POSTAuthoritative verifies the M1
// fix at the runOneServer layer: SSE close before POST completes
// must NOT abort the job — runOneServer logs the SSE error at warn
// level and lets POST be authoritative. End user gets their answer.
func TestRunOneServer_SSECloseEarly_POSTAuthoritative(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != supervisorAuthUsername || p != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/session":
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"id":"ses_sse_drops"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/event":
			// Send connect event then close immediately
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher, _ := w.(http.Flusher)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", `{"id":"e1","type":"server.connected","properties":{}}`)
			if flusher != nil {
				flusher.Flush()
			}
			// handler returns → stream closes
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/session/") && strings.HasSuffix(r.URL.Path, "/message"):
			// POST returns successfully even though SSE has closed
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"info":{"finish":"stop","tokens":{"output":2}},"parts":[{"type":"text","text":"survives SSE close"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	sup := primedSupervisor(srv.URL, "secret", 33333)
	r := &Runner{
		opencodeCfg: config.OpencodeConfig{Mode: config.OpencodeModeServer},
		supervisor:  sup,
	}
	var exitCode atomic.Int64
	exitCode.Store(-99)
	output, err := r.runOneServer(
		context.Background(),
		slog.Default(),
		config.AgentConfig{Command: "opencode"},
		"/tmp/work",
		"prompt",
		RunOptions{
			OnExit: func(code int) { exitCode.Store(int64(code)) },
		},
	)
	if err != nil {
		t.Fatalf("runOneServer should succeed despite SSE close: %v", err)
	}
	if output != "survives SSE close" {
		t.Errorf("output = %q, want %q (POST is authoritative)", output, "survives SSE close")
	}
	if exitCode.Load() != 0 {
		t.Errorf("OnExit code = %d, want 0", exitCode.Load())
	}
}

// fakeOpencodeBehavior parameterizes the fake opencode HTTP API for
// runOneServer tests. Zero values are happy-path defaults; flip
// failPostMessage to exercise the POST /session/{id}/message error
// path.
type fakeOpencodeBehavior struct {
	sessionID    string
	finalText    string
	finishReason string
	outputTokens int
	sseEvents    []string

	failPostMessage bool
	failPostStatus  int
	failPostBody    string
}

func newFakeOpencodeServer(t *testing.T, b fakeOpencodeBehavior) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != supervisorAuthUsername || p != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/session":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"id":%q}`, b.sessionID)
		case r.Method == http.MethodGet && r.URL.Path == "/event":
			writeSSELines(w, r, b.sseEvents)
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/session/") && strings.HasSuffix(r.URL.Path, "/message"):
			if b.failPostMessage {
				status := b.failPostStatus
				if status == 0 {
					status = http.StatusInternalServerError
				}
				http.Error(w, b.failPostBody, status)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			body := fmt.Sprintf(
				`{"info":{"finish":%q,"tokens":{"input":2,"output":%d}},"parts":[{"type":"text","text":%q}]}`,
				b.finishReason, b.outputTokens, b.finalText,
			)
			_, _ = io.WriteString(w, body)
		default:
			http.NotFound(w, r)
		}
	}))
}
