package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestSupervisorListenLineMatches(t *testing.T) {
	cases := []struct {
		name string
		line string
		want string
	}{
		{"loopback-port-0-result", "opencode server listening on http://127.0.0.1:50000", "http://127.0.0.1:50000"},
		{"https-variant", "opencode server listening on https://127.0.0.1:443", "https://127.0.0.1:443"},
		{"hostname-with-trailing-text", "opencode server listening on http://127.0.0.1:8080 (ready)", "http://127.0.0.1:8080"},
		{"unrelated-line", "some other line", ""},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := supervisorListenLine.FindStringSubmatch(tc.line)
			var got string
			if len(m) > 1 {
				got = m[1]
			}
			if got != tc.want {
				t.Errorf("FindStringSubmatch(%q) = %q, want %q", tc.line, got, tc.want)
			}
		})
	}
}

func TestGenerateSupervisorPassword_UniqueAndHex(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 32; i++ {
		p, err := generateSupervisorPassword()
		if err != nil {
			t.Fatalf("generateSupervisorPassword: %v", err)
		}
		if len(p) != 48 {
			t.Errorf("password length = %d, want 48", len(p))
		}
		if seen[p] {
			t.Errorf("duplicate password generated: %q", p)
		}
		seen[p] = true
		for _, r := range p {
			if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
				t.Errorf("password %q contains non-hex char %q", p, r)
				break
			}
		}
	}
}

func TestBuildSupervisorEnv(t *testing.T) {
	t.Run("with-storage-dir", func(t *testing.T) {
		env := buildSupervisorEnv("secret", "/tmp/xdg")
		if !containsExact(env, "OPENCODE_SERVER_PASSWORD=secret") {
			t.Error("env missing OPENCODE_SERVER_PASSWORD")
		}
		if !containsExact(env, "XDG_DATA_HOME=/tmp/xdg") {
			t.Error("env missing XDG_DATA_HOME")
		}
	})
	t.Run("empty-storage-dir-omits-xdg", func(t *testing.T) {
		env := buildSupervisorEnv("secret", "")
		if !containsExact(env, "OPENCODE_SERVER_PASSWORD=secret") {
			t.Error("env missing OPENCODE_SERVER_PASSWORD")
		}
		for _, e := range env {
			if strings.HasPrefix(e, "XDG_DATA_HOME=") && e == "XDG_DATA_HOME=" {
				t.Error("empty StorageDir leaked into XDG_DATA_HOME=")
			}
		}
	})
}

func TestPollSupervisorHealth_OkAfterRetries(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		u, p, ok := r.BasicAuth()
		if !ok || u != supervisorAuthUsername || p != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if hits.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"healthy":true,"version":"fake"}`))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pollSupervisorHealth(ctx, srv.URL, "secret"); err != nil {
		t.Fatalf("pollSupervisorHealth: %v", err)
	}
	if hits.Load() < 3 {
		t.Errorf("hits = %d, expected ≥ 3 to confirm retry", hits.Load())
	}
}

func TestPollSupervisorHealth_NeverHealthyTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	err := pollSupervisorHealth(ctx, srv.URL, "secret")
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error %v not a context.DeadlineExceeded", err)
	}
}

func TestPollSupervisorHealth_BadCredentialsErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _, ok := r.BasicAuth()
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err := pollSupervisorHealth(ctx, srv.URL, "secret")
	if err == nil {
		t.Fatal("expected error on persistent 401, got nil")
	}
}

func TestIsExpectedExitOnSignal(t *testing.T) {
	if isExpectedExitOnSignal(nil) != true {
		t.Error("nil should be treated as expected (clean exit)")
	}
	other := errors.New("non-exec error")
	if isExpectedExitOnSignal(other) {
		t.Error("non-exec error should not be expected")
	}
}

func TestSupervisor_StartStop_HappyPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh script not supported on windows")
	}
	srv := newFakeHealthServer(t, "")
	defer srv.Close()

	binPath := writeFakeOpencodeServeBinary(t, srv.URL, "sleep")
	sup := NewSupervisor(SupervisorConfig{BinaryPath: binPath})
	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := sup.BaseURL(); got != srv.URL {
		t.Errorf("BaseURL = %q, want %q", got, srv.URL)
	}
	if sup.Password() == "" {
		t.Error("Password is empty after Start")
	}
	pid := sup.ChildPID()
	if pid == 0 {
		t.Error("ChildPID is 0 after Start")
	}
	if err := sup.Stop(context.Background()); err != nil {
		t.Errorf("Stop: %v", err)
	}
	if alive, err := processAlive(pid); alive || err == nil {
		t.Errorf("process %d still alive after Stop (err=%v)", pid, err)
	}
}

func TestSupervisor_Start_BinaryMissing(t *testing.T) {
	sup := NewSupervisor(SupervisorConfig{
		BinaryPath: filepath.Join(t.TempDir(), "does-not-exist"),
	})
	err := sup.Start(context.Background())
	if err == nil {
		_ = sup.Stop(context.Background())
		t.Fatal("Start succeeded with missing binary")
	}
}

func TestSupervisor_Start_BinaryCrashesBeforeReady(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh script not supported on windows")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "opencode")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 7\n"), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}
	sup := NewSupervisor(SupervisorConfig{BinaryPath: bin})
	err := sup.Start(context.Background())
	if err == nil {
		_ = sup.Stop(context.Background())
		t.Fatal("Start succeeded with binary that exits before ready")
	}
}

func TestSupervisor_Start_BinaryPrintsLineButNoHealth(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh script not supported on windows")
	}
	// Point listen URL at a non-listening loopback port — health probe must
	// surface ctx.Deadline rather than hang past supervisorStartTimeout.
	binPath := writeFakeOpencodeServeBinary(t, "http://127.0.0.1:1", "sleep")
	sup := NewSupervisor(SupervisorConfig{BinaryPath: binPath})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := sup.Start(ctx)
	if err == nil {
		_ = sup.Stop(context.Background())
		t.Fatal("Start succeeded against non-listening URL")
	}
}

func TestSupervisor_Stop_Idempotent(t *testing.T) {
	sup := NewSupervisor(SupervisorConfig{BinaryPath: "/never-started"})
	if err := sup.Stop(context.Background()); err != nil {
		t.Errorf("Stop on never-started supervisor: %v", err)
	}
}

// Stage 3 lazy lifecycle tests. The supervisor in Stage 3 boots in
// `idle` state; the first Acquire triggers spawn. Release decrements
// the active count and arms an idle timer (when IdleTimeout > 0).

func TestSupervisor_Acquire_LazySpawnAndRelease(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh script not supported on windows")
	}
	srv := newFakeHealthServer(t, "")
	defer srv.Close()

	binPath := writeFakeOpencodeServeBinary(t, srv.URL, "sleep")
	sup := NewSupervisor(SupervisorConfig{BinaryPath: binPath})
	defer func() { _ = sup.Stop(context.Background()) }()

	if got := sup.State(); got != stateIdle {
		t.Errorf("pre-Acquire State = %s, want idle", got)
	}
	if pid := sup.ChildPID(); pid != 0 {
		t.Errorf("pre-Acquire ChildPID = %d, want 0", pid)
	}

	if err := sup.Acquire(context.Background()); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if got := sup.State(); got != stateRunning {
		t.Errorf("post-Acquire State = %s, want running", got)
	}
	if sup.ChildPID() == 0 {
		t.Error("post-Acquire ChildPID = 0")
	}
	if sup.BaseURL() != srv.URL {
		t.Errorf("BaseURL = %q, want %q", sup.BaseURL(), srv.URL)
	}

	sup.Release()
	if got := sup.State(); got != stateRunning {
		t.Errorf("post-Release State = %s, want running (no IdleTimeout)", got)
	}
}

func TestSupervisor_Acquire_ConcurrentCoalesceSingleSpawn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh script not supported on windows")
	}
	srv := newFakeHealthServer(t, "")
	defer srv.Close()

	binPath := writeFakeOpencodeServeBinary(t, srv.URL, "sleep")
	sup := NewSupervisor(SupervisorConfig{BinaryPath: binPath})
	defer func() { _ = sup.Stop(context.Background()) }()

	const N = 8
	var wg sync.WaitGroup
	pids := make([]int, N)
	urls := make([]string, N)
	errs := make([]error, N)
	start := make(chan struct{})
	for i := 0; i < N; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			err := sup.Acquire(context.Background())
			if err != nil {
				errs[i] = err
				return
			}
			pids[i] = sup.ChildPID()
			urls[i] = sup.BaseURL()
		}()
	}
	close(start)
	wg.Wait()

	var firstPID int
	var firstURL string
	for i := 0; i < N; i++ {
		if errs[i] != nil {
			t.Errorf("Acquire[%d]: %v", i, errs[i])
			continue
		}
		if firstPID == 0 {
			firstPID = pids[i]
			firstURL = urls[i]
			continue
		}
		if pids[i] != firstPID {
			t.Errorf("Acquire[%d] PID = %d, want %d (coalescence broken — multiple spawns)", i, pids[i], firstPID)
		}
		if urls[i] != firstURL {
			t.Errorf("Acquire[%d] URL = %q, want %q", i, urls[i], firstURL)
		}
	}

	// Drain all Releases to confirm activeCount tracks correctly.
	for i := 0; i < N; i++ {
		sup.Release()
	}
	sup.mu.Lock()
	got := sup.activeCount
	sup.mu.Unlock()
	if got != 0 {
		t.Errorf("activeCount after N Releases = %d, want 0", got)
	}
}

func TestSupervisor_Acquire_IdleStopThenRespawn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh script not supported on windows")
	}
	srv := newFakeHealthServer(t, "")
	defer srv.Close()

	binPath := writeFakeOpencodeServeBinary(t, srv.URL, "sleep")
	sup := NewSupervisor(SupervisorConfig{
		BinaryPath:  binPath,
		IdleTimeout: 100 * time.Millisecond,
	})
	defer func() { _ = sup.Stop(context.Background()) }()

	if err := sup.Acquire(context.Background()); err != nil {
		t.Fatalf("Acquire 1: %v", err)
	}
	pid1 := sup.ChildPID()
	if pid1 == 0 {
		t.Fatal("PID is 0 after first Acquire")
	}
	sup.Release()

	// Idle timer fires after 100ms; internal stop adds another ~50ms
	// for the sleep child to die. 3s is plenty of headroom.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if sup.State() == stateIdle {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if sup.State() != stateIdle {
		t.Fatalf("supervisor state = %s, want idle after IdleTimeout", sup.State())
	}
	if alive, _ := processAlive(pid1); alive {
		t.Errorf("PID %d still alive after idle stop", pid1)
	}
	if sup.ChildPID() != 0 {
		t.Errorf("ChildPID = %d after idle stop, want 0", sup.ChildPID())
	}

	// Re-acquire must trigger fresh spawn — different PID.
	if err := sup.Acquire(context.Background()); err != nil {
		t.Fatalf("Acquire 2: %v", err)
	}
	pid2 := sup.ChildPID()
	if pid2 == 0 {
		t.Fatal("PID is 0 after second Acquire")
	}
	if pid2 == pid1 {
		t.Errorf("re-Acquire produced same PID %d (no respawn)", pid2)
	}
	sup.Release()
}

func TestSupervisor_Drain_RejectsPostDrainAcquire(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh script not supported on windows")
	}
	srv := newFakeHealthServer(t, "")
	defer srv.Close()

	binPath := writeFakeOpencodeServeBinary(t, srv.URL, "sleep")
	sup := NewSupervisor(SupervisorConfig{BinaryPath: binPath})

	if err := sup.Acquire(context.Background()); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	pid := sup.ChildPID()
	sup.Release()

	if err := sup.Drain(context.Background()); err != nil {
		t.Errorf("Drain: %v", err)
	}
	if alive, _ := processAlive(pid); alive {
		t.Errorf("PID %d still alive after Drain", pid)
	}

	err := sup.Acquire(context.Background())
	if err == nil {
		t.Fatal("Acquire after Drain succeeded")
	}
	if !strings.Contains(err.Error(), "shutting down") {
		t.Errorf("Acquire post-Drain error = %v, want 'shutting down' substring", err)
	}
}

func TestSupervisor_Drain_AbortsActiveSessions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh script not supported on windows")
	}

	var (
		abortedMu sync.Mutex
		aborted   []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != supervisorAuthUsername || p == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"healthy":true,"version":"fake"}`))
			return
		}
		if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/session/") && strings.HasSuffix(r.URL.Path, "/abort") {
			parts := strings.Split(r.URL.Path, "/")
			if len(parts) >= 4 {
				abortedMu.Lock()
				aborted = append(aborted, parts[2])
				abortedMu.Unlock()
			}
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	binPath := writeFakeOpencodeServeBinary(t, srv.URL, "sleep")
	sup := NewSupervisor(SupervisorConfig{BinaryPath: binPath})

	if err := sup.Acquire(context.Background()); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	pid := sup.ChildPID()
	sup.SetActiveSession("ses_1")
	sup.SetActiveSession("ses_2")
	sup.SetActiveSession("ses_3")

	if err := sup.Drain(context.Background()); err != nil {
		t.Errorf("Drain: %v", err)
	}
	if alive, _ := processAlive(pid); alive {
		t.Errorf("PID %d still alive after Drain", pid)
	}

	abortedMu.Lock()
	got := append([]string(nil), aborted...)
	abortedMu.Unlock()
	if len(got) != 3 {
		t.Fatalf("aborted = %v, want 3 abort calls", got)
	}
	seen := map[string]bool{}
	for _, id := range got {
		seen[id] = true
	}
	for _, want := range []string{"ses_1", "ses_2", "ses_3"} {
		if !seen[want] {
			t.Errorf("session %q never aborted (got %v)", want, got)
		}
	}
}

func TestSupervisor_Acquire_RejectedDuringShutdown(t *testing.T) {
	sup := NewSupervisor(SupervisorConfig{BinaryPath: "/never-spawned"})
	sup.mu.Lock()
	sup.shuttingDown = true
	sup.mu.Unlock()

	err := sup.Acquire(context.Background())
	if err == nil {
		t.Fatal("Acquire on shuttingDown supervisor succeeded")
	}
	if !strings.Contains(err.Error(), "shutting down") {
		t.Errorf("error = %v, want 'shutting down'", err)
	}
}

// newFakeHealthServer spins up an httptest server that responds 200 to
// authenticated GET /health and 401 otherwise. The optional sessionsHandler
// lets caller layer in additional routes for HTTP/SSE client tests.
func newFakeHealthServer(t *testing.T, expectedPassword string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			u, p, ok := r.BasicAuth()
			if !ok || u != supervisorAuthUsername {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if expectedPassword != "" && p != expectedPassword {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"healthy":true,"version":"fake"}`))
			return
		}
		http.NotFound(w, r)
	}))
}

// writeFakeOpencodeServeBinary creates a sh script that prints the
// expected listen line and either sleeps (mode=sleep, default for happy
// path) or exits (mode=exit, for crash-before-ready coverage).
func writeFakeOpencodeServeBinary(t *testing.T, fakeBaseURL, mode string) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "opencode")
	var body string
	switch mode {
	case "exit":
		body = "exit 0"
	default:
		// exec sleep replaces the shell process image so SIGTERM is
		// delivered straight to sleep, which terminates immediately.
		// (Avoids the trap-while-sleep race on some POSIX shells.)
		body = "exec sleep 600"
	}
	script := fmt.Sprintf("#!/bin/sh\necho \"opencode server listening on %s\"\n%s\n", fakeBaseURL, body)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}
	return bin
}

func processAlive(pid int) (bool, error) {
	if pid <= 0 {
		return false, errors.New("invalid pid")
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false, err
	}
	err = p.Signal(syscall.Signal(0))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrProcessDone) {
		return false, err
	}
	// macOS / linux: ESRCH from kill(2)
	if isProcessNotFound(err) {
		return false, err
	}
	return false, err
}

func isProcessNotFound(err error) bool {
	var sysErr syscall.Errno
	if errors.As(err, &sysErr) {
		return sysErr == syscall.ESRCH
	}
	return false
}

func containsExact(env []string, want string) bool {
	for _, e := range env {
		if e == want {
			return true
		}
	}
	return false
}

// Compile-time sanity: ensure exec.ExitError is reachable for sad-path
// assertions even when no test directly returns one.
var _ = (*exec.ExitError)(nil)
