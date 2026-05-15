package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
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

// TestRunOneServer_RetryOnce_RecoversFromTransportError verifies the
// Stage 3 Task 3.2-9 retry-once contract: when the first attempt hits
// a transport-layer failure (here, the server hijacks and closes the
// /session POST mid-call, producing EOF on the client), runOneServer
// transparently retries against the same supervisor and returns the
// second attempt's answer. Tests EOF-string fallback in
// isRecoverableSupervisorCrash, since primedSupervisor never flips
// state to crashed (no real child process).
func TestRunOneServer_RetryOnce_RecoversFromTransportError(t *testing.T) {
	var sessionCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != supervisorAuthUsername || p != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/session":
			n := sessionCalls.Add(1)
			if n == 1 {
				// Simulate transport-layer failure: hijack + close so
				// client sees Post URL: EOF.
				hj, ok := w.(http.Hijacker)
				if !ok {
					http.Error(w, "hijacker unavailable", http.StatusInternalServerError)
					return
				}
				conn, _, _ := hj.Hijack()
				_ = conn.Close()
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"id":"ses_retry"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/event":
			writeSSELines(w, r, []string{
				`{"id":"e1","type":"message.part.updated","properties":{"part":{"type":"text","text":"hello"}}}`,
			})
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/session/") && strings.HasSuffix(r.URL.Path, "/message"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"info":{"finish":"stop","tokens":{"input":1,"output":2}},"parts":[{"type":"text","text":"retried answer"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	sup := primedSupervisor(srv.URL, "secret", 99999)
	r := &Runner{
		opencodeCfg: config.OpencodeConfig{Mode: config.OpencodeModeServer},
		supervisor:  sup,
	}

	output, err := r.runOneServer(
		context.Background(),
		slog.Default(),
		config.AgentConfig{Command: "opencode"},
		"/tmp/work",
		"prompt",
		RunOptions{},
	)
	if err != nil {
		t.Fatalf("runOneServer (retry expected to succeed): %v", err)
	}
	if output != "retried answer" {
		t.Errorf("output = %q, want %q", output, "retried answer")
	}
	if got := sessionCalls.Load(); got != 2 {
		t.Errorf("CreateSession calls = %d, want 2 (attempt + retry)", got)
	}
}

// TestRunOneServer_OnStarted_FiresOnFirstSuccessfulAcquire verifies
// the cross-review pr fix for OnStarted on retry-once. Older code
// gated OnStarted on `attempt == 0`, which skipped firing when
// attempt 0 failed recoverably and attempt 1 succeeded — leaving the
// pool's status registry without a PID for the job. Fix: track the
// fire flag across attempts; first SUCCESSFUL Acquire fires it.
func TestRunOneServer_OnStarted_FiresOnFirstSuccessfulAcquire(t *testing.T) {
	var (
		sessionCalls atomic.Int32
		onStartedHit atomic.Int32
		gotPID       atomic.Int64
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != supervisorAuthUsername || p != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/session":
			n := sessionCalls.Add(1)
			if n == 1 {
				// Attempt 0: hijack to simulate transport failure.
				hj, _ := w.(http.Hijacker)
				conn, _, _ := hj.Hijack()
				_ = conn.Close()
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"id":"ses_retry_onstart"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/event":
			writeSSELines(w, r, []string{
				`{"id":"e1","type":"message.part.updated","properties":{"part":{"type":"text","text":"ok"}}}`,
			})
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/session/") && strings.HasSuffix(r.URL.Path, "/message"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"info":{"finish":"stop","tokens":{"input":1,"output":2}},"parts":[{"type":"text","text":"answer"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	sup := primedSupervisor(srv.URL, "secret", 12321)
	r := &Runner{
		opencodeCfg: config.OpencodeConfig{Mode: config.OpencodeModeServer},
		supervisor:  sup,
	}

	output, err := r.runOneServer(
		context.Background(),
		slog.Default(),
		config.AgentConfig{Command: "opencode"},
		"/tmp/work",
		"prompt",
		RunOptions{
			OnStarted: func(pid int, command string) {
				onStartedHit.Add(1)
				gotPID.Store(int64(pid))
			},
		},
	)
	if err != nil {
		t.Fatalf("runOneServer (retry expected to succeed): %v", err)
	}
	if output != "answer" {
		t.Errorf("output = %q, want %q", output, "answer")
	}
	if got := onStartedHit.Load(); got != 1 {
		t.Errorf("OnStarted call count = %d, want exactly 1 (first successful Acquire)", got)
	}
	if got := gotPID.Load(); got != 12321 {
		t.Errorf("OnStarted PID = %d, want 12321", got)
	}
}

// TestRunOneServer_RetryOnce_SecondFailureSurfaces verifies the hard-
// fail behavior: both attempts hit transport failures, retry-once does
// NOT escalate to a third attempt, and the second attempt's error is
// surfaced to the caller. Spec C4 — no fallback to spawn mode.
func TestRunOneServer_RetryOnce_SecondFailureSurfaces(t *testing.T) {
	var sessionCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != supervisorAuthUsername || p != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/session" {
			sessionCalls.Add(1)
			hj, _ := w.(http.Hijacker)
			conn, _, _ := hj.Hijack()
			_ = conn.Close()
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	sup := primedSupervisor(srv.URL, "secret", 88888)
	r := &Runner{
		opencodeCfg: config.OpencodeConfig{Mode: config.OpencodeModeServer},
		supervisor:  sup,
	}

	_, err := r.runOneServer(
		context.Background(),
		slog.Default(),
		config.AgentConfig{Command: "opencode"},
		"/tmp/work",
		"prompt",
		RunOptions{},
	)
	if err == nil {
		t.Fatal("expected hard error after 2 failed attempts")
	}
	if got := sessionCalls.Load(); got != 2 {
		t.Errorf("CreateSession calls = %d, want exactly 2 (no third attempt)", got)
	}
}

// TestRunOneServer_NoRetryOnApplicationError verifies retry-once does
// NOT fire on a non-transport failure (here, 400 Bad Request from
// CreateSession). Retry budget must be reserved for crashes — burning
// it on application errors burns latency for no recovery benefit.
func TestRunOneServer_NoRetryOnApplicationError(t *testing.T) {
	var sessionCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != supervisorAuthUsername || p != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/session" {
			sessionCalls.Add(1)
			http.Error(w, `{"error":"directory missing"}`, http.StatusBadRequest)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	sup := primedSupervisor(srv.URL, "secret", 77777)
	r := &Runner{
		opencodeCfg: config.OpencodeConfig{Mode: config.OpencodeModeServer},
		supervisor:  sup,
	}

	_, err := r.runOneServer(
		context.Background(),
		slog.Default(),
		config.AgentConfig{Command: "opencode"},
		"/tmp/work",
		"prompt",
		RunOptions{},
	)
	if err == nil {
		t.Fatal("expected error from 400 CreateSession")
	}
	if got := sessionCalls.Load(); got != 1 {
		t.Errorf("CreateSession calls = %d, want 1 (no retry on 4xx)", got)
	}
}

// TestIsRecoverableSupervisorCrash exhaustively covers the classifier
// matrix used by retry-once in runOneServer. The supervisor state hook
// is tested via direct field mutation rather than a real crash; the
// error-signature fallback is tested with the canonical error chains.
func TestIsRecoverableSupervisorCrash(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		state     supervisorState
		want      bool
	}{
		{"nil-err", nil, stateRunning, false},
		{"state-crashed-any-err", errors.New("does not matter"), stateCrashed, true},
		{"econnrefused-typed", fmt.Errorf("dial: %w", syscall.ECONNREFUSED), stateRunning, true},
		{"econnrefused-string", errors.New("Post http://x: dial tcp 127.0.0.1:5000: connection refused"), stateRunning, true},
		{"eof-typed", fmt.Errorf("post: %w", io.EOF), stateRunning, true},
		{"unexpected-eof-typed", fmt.Errorf("read: %w", io.ErrUnexpectedEOF), stateRunning, true},
		{"eof-string", errors.New("Post http://x: EOF"), stateRunning, true},
		{"broken-pipe-string", errors.New("write tcp: broken pipe"), stateRunning, true},
		{"4xx-no-retry", errors.New("create-session status 400: bad input"), stateRunning, false},
		{"ctx-canceled-no-retry", context.Canceled, stateRunning, false},
		{"ctx-deadline-no-retry", context.DeadlineExceeded, stateRunning, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sup := &Supervisor{}
			sup.state = tc.state
			got := isRecoverableSupervisorCrash(tc.err, sup)
			if got != tc.want {
				t.Errorf("isRecoverableSupervisorCrash(%v, state=%s) = %v, want %v", tc.err, tc.state, got, tc.want)
			}
		})
	}
}

// TestRunOneServer_BugA_TriggersOnSilentDropSignature verifies the
// Stage 3 Task 3.2-11 strict three-condition AND-gate: finish=other +
// tokens.output=0 + no message_delta event over SSE → fail loud with
// the user-facing copy. The error string matches the spec verbatim so
// the existing failure render in app/workflow/ask.go HandleResult
// shows it to the user without app-side changes.
func TestRunOneServer_BugA_TriggersOnSilentDropSignature(t *testing.T) {
	srv := newFakeOpencodeServer(t, fakeOpencodeBehavior{
		sessionID:    "ses_bug_a",
		finalText:    "",
		finishReason: "other",
		outputTokens: 0,
		sseEvents: []string{
			`{"id":"e1","type":"server.connected","properties":{}}`,
			// No `message.part.updated` text events — silent drop.
		},
	})
	defer srv.Close()

	sup := primedSupervisor(srv.URL, "secret", 65432)
	r := &Runner{
		opencodeCfg: config.OpencodeConfig{Mode: config.OpencodeModeServer},
		supervisor:  sup,
	}

	output, err := r.runOneServer(
		context.Background(),
		slog.Default(),
		config.AgentConfig{Command: "opencode"},
		"/tmp/work",
		"prompt",
		RunOptions{},
	)
	if err == nil {
		t.Fatal("expected Bug A error, got nil")
	}
	if !errors.Is(err, errOpencodeEmptyStream) {
		t.Errorf("error %v is not errOpencodeEmptyStream", err)
	}
	if got := err.Error(); got != "LLM 回應為空，請稍後再試或改用 @bot issue" {
		t.Errorf("error message = %q, want spec copy verbatim", got)
	}
	if output != "" {
		t.Errorf("output = %q, want empty on Bug A failure", output)
	}
}

// TestRunOneServer_BugA_NotTriggered_WhenTextReceived verifies the
// AND-gate's text-part clause: even if finish=other + tokens=0, a
// single message_delta event over SSE is enough to disqualify the
// Bug A signature. Runs return the (empty) text payload as-is.
func TestRunOneServer_BugA_NotTriggered_WhenTextReceived(t *testing.T) {
	srv := newFakeOpencodeServer(t, fakeOpencodeBehavior{
		sessionID:    "ses_no_bugA_text",
		finalText:    "",
		finishReason: "other",
		outputTokens: 0,
		sseEvents: []string{
			`{"id":"e1","type":"message.part.updated","properties":{"part":{"type":"text","text":"partial"}}}`,
		},
	})
	defer srv.Close()

	sup := primedSupervisor(srv.URL, "secret", 65431)
	r := &Runner{
		opencodeCfg: config.OpencodeConfig{Mode: config.OpencodeModeServer},
		supervisor:  sup,
	}

	output, err := r.runOneServer(
		context.Background(),
		slog.Default(),
		config.AgentConfig{Command: "opencode"},
		"/tmp/work",
		"prompt",
		RunOptions{},
	)
	if err != nil {
		t.Fatalf("unexpected error (saw text, should not be Bug A): %v", err)
	}
	if output != "" {
		t.Errorf("output = %q, want empty (POST body had empty parts)", output)
	}
}

// TestRunOneServer_BugA_NotTriggered_WhenTokensNonZero verifies the
// AND-gate's tokens clause: finish=other + no text + tokens>0 is not
// Bug A (the model produced something, even if no text emerged).
func TestRunOneServer_BugA_NotTriggered_WhenTokensNonZero(t *testing.T) {
	srv := newFakeOpencodeServer(t, fakeOpencodeBehavior{
		sessionID:    "ses_no_bugA_tokens",
		finalText:    "",
		finishReason: "other",
		outputTokens: 5,
		sseEvents: []string{
			`{"id":"e1","type":"server.connected","properties":{}}`,
		},
	})
	defer srv.Close()

	sup := primedSupervisor(srv.URL, "secret", 65430)
	r := &Runner{
		opencodeCfg: config.OpencodeConfig{Mode: config.OpencodeModeServer},
		supervisor:  sup,
	}

	output, err := r.runOneServer(
		context.Background(),
		slog.Default(),
		config.AgentConfig{Command: "opencode"},
		"/tmp/work",
		"prompt",
		RunOptions{},
	)
	if err != nil {
		t.Fatalf("unexpected error (tokens>0, not Bug A): %v", err)
	}
	_ = output
}

// TestRunOneServer_BugA_NotTriggered_WhenFinishStop verifies the
// AND-gate's finish-reason clause: finish=stop is a normal termination
// regardless of token count or text presence. Most no-op-style
// answers ("say OK") would hit finish=stop + tokens>0 but this case
// (finish=stop + tokens=0) still must not trip Bug A.
func TestRunOneServer_BugA_NotTriggered_WhenFinishStop(t *testing.T) {
	srv := newFakeOpencodeServer(t, fakeOpencodeBehavior{
		sessionID:    "ses_no_bugA_finish",
		finalText:    "",
		finishReason: "stop",
		outputTokens: 0,
		sseEvents: []string{
			`{"id":"e1","type":"server.connected","properties":{}}`,
		},
	})
	defer srv.Close()

	sup := primedSupervisor(srv.URL, "secret", 65429)
	r := &Runner{
		opencodeCfg: config.OpencodeConfig{Mode: config.OpencodeModeServer},
		supervisor:  sup,
	}

	output, err := r.runOneServer(
		context.Background(),
		slog.Default(),
		config.AgentConfig{Command: "opencode"},
		"/tmp/work",
		"prompt",
		RunOptions{},
	)
	if err != nil {
		t.Fatalf("unexpected error (finish=stop, not Bug A): %v", err)
	}
	_ = output
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
