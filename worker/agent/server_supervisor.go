package agent

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Server supervisor for `opencode serve`. Stage 2 is always-on: Start is
// called at worker boot, Stop on worker shutdown. Lazy lifecycle (spawn on
// first job, idle-stop, sync.Once boot lock) is deferred to plan Task
// 3.2-8 / Stage 3.

const (
	supervisorStartTimeout   = 10 * time.Second
	supervisorStopGrace      = 5 * time.Second
	supervisorHealthInterval = 200 * time.Millisecond
	supervisorHealthRequest  = 2 * time.Second
	supervisorAuthUsername   = "opencode"
)

// supervisorListenLine matches opencode serve's startup line printed
// after `Server.listen` resolves. The trailing newline is stripped by the
// scanner. We capture the full URL — hostname + kernel-assigned port —
// so the supervisor never has to pre-pick a port (avoids the well-known
// pickFreePort → exec.Start race window).
var supervisorListenLine = regexp.MustCompile(`opencode server listening on (https?://\S+)`)

// SupervisorConfig is the supervisor's constructor input.
type SupervisorConfig struct {
	BinaryPath string
	StorageDir string
	Logger     *slog.Logger
}

// Supervisor holds the long-running `opencode serve` child process for a
// worker. One Supervisor per worker process; the pool's N goroutines share
// the same BaseURL/Password via per-request `x-opencode-directory` header
// isolation (see spec § Code Style and § Boundaries Always: server-pool
// mapping = 1:N).
type Supervisor struct {
	cfg SupervisorConfig

	mu       sync.Mutex
	cmd      *exec.Cmd
	cancel   context.CancelFunc
	baseURL  string
	password string
	pid      int
	started  bool
	waitErr  chan error
}

// NewSupervisor constructs a Supervisor; call Start to spawn the child.
func NewSupervisor(cfg SupervisorConfig) *Supervisor {
	return &Supervisor{cfg: cfg}
}

// Start spawns `opencode serve --port 0 --hostname 127.0.0.1` with an
// isolated XDG_DATA_HOME (spec § Boundaries Always: storage isolation) and
// a random OPENCODE_SERVER_PASSWORD. Blocks until the child prints its
// listen URL AND GET /health returns 200, both within
// supervisorStartTimeout. Returns an error if the binary is missing,
// crashes before becoming ready, or never reports healthy in time.
func (s *Supervisor) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return errors.New("supervisor already started")
	}
	s.mu.Unlock()

	password, err := generateSupervisorPassword()
	if err != nil {
		return fmt.Errorf("generate password: %w", err)
	}

	runCtx, cancelRun := context.WithCancel(context.Background())
	cmd := exec.CommandContext(runCtx, s.cfg.BinaryPath, "serve",
		"--port", "0",
		"--hostname", "127.0.0.1",
	)
	cmd.Env = buildSupervisorEnv(password, s.cfg.StorageDir)
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = supervisorStopGrace

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancelRun()
		return fmt.Errorf("stdout pipe: %w", err)
	}
	cmd.Stderr = newSupervisorLogWriter(s.cfg.Logger, "stderr")

	if err := cmd.Start(); err != nil {
		cancelRun()
		return fmt.Errorf("start opencode serve: %w", err)
	}

	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()

	startCtx, cancelStart := context.WithTimeout(ctx, supervisorStartTimeout)
	defer cancelStart()

	baseURL, err := awaitSupervisorListenURL(startCtx, stdout, waitErr, s.cfg.Logger)
	if err != nil {
		cancelRun()
		return err
	}

	if err := pollSupervisorHealth(startCtx, baseURL, password); err != nil {
		cancelRun()
		return fmt.Errorf("opencode serve /health 探測失敗: %w", err)
	}

	s.mu.Lock()
	s.cmd = cmd
	s.cancel = cancelRun
	s.baseURL = baseURL
	s.password = password
	s.pid = cmd.Process.Pid
	s.started = true
	s.waitErr = waitErr
	s.mu.Unlock()

	if s.cfg.Logger != nil {
		s.cfg.Logger.Info("opencode serve 啟動",
			"phase", "完成",
			"base_url", baseURL,
			"pid", cmd.Process.Pid,
		)
	}
	return nil
}

// Stop signals SIGTERM to the child process and waits up to
// supervisorStopGrace + 1s for it to exit. Cancellation of runCtx triggers
// cmd.Cancel (SIGTERM); cmd.WaitDelay escalates to SIGKILL after the
// grace window if the child is still alive. Idempotent: calling Stop on
// a never-started or already-stopped supervisor is a no-op.
func (s *Supervisor) Stop(ctx context.Context) error {
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return nil
	}
	s.started = false
	cancelRun := s.cancel
	waitErr := s.waitErr
	pid := s.pid
	s.mu.Unlock()

	cancelRun()

	select {
	case err := <-waitErr:
		if s.cfg.Logger != nil {
			s.cfg.Logger.Info("opencode serve 已停止",
				"phase", "完成",
				"pid", pid,
				"wait_err", errString(err),
			)
		}
		if isExpectedExitOnSignal(err) {
			return nil
		}
		return err
	case <-ctx.Done():
		return fmt.Errorf("stop opencode serve: %w", ctx.Err())
	case <-time.After(supervisorStopGrace + 2*time.Second):
		return errors.New("opencode serve did not exit within grace window")
	}
}

// BaseURL returns the listen URL the child reported on startup, e.g.
// "http://127.0.0.1:50000". Empty before Start succeeds.
func (s *Supervisor) BaseURL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.baseURL
}

// Password returns the random OPENCODE_SERVER_PASSWORD passed to the
// child via env. Empty before Start succeeds.
func (s *Supervisor) Password() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.password
}

// ChildPID returns the OS process ID of the spawned `opencode serve`.
// Zero before Start succeeds or after Stop completes.
func (s *Supervisor) ChildPID() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pid
}

func generateSupervisorPassword() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func buildSupervisorEnv(password, storageDir string) []string {
	env := os.Environ()
	env = append(env, fmt.Sprintf("OPENCODE_SERVER_PASSWORD=%s", password))
	if storageDir != "" {
		env = append(env, fmt.Sprintf("XDG_DATA_HOME=%s", storageDir))
	}
	return env
}

func awaitSupervisorListenURL(ctx context.Context, stdout io.ReadCloser, waitErr <-chan error, logger *slog.Logger) (string, error) {
	urlCh := make(chan string, 1)
	scanDone := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
		sent := false
		for scanner.Scan() {
			line := scanner.Text()
			if !sent {
				if m := supervisorListenLine.FindStringSubmatch(line); m != nil {
					urlCh <- m[1]
					sent = true
				}
			}
			if logger != nil {
				logger.Debug("opencode stdout",
					"phase", "處理中",
					"line", line,
				)
			}
		}
		scanDone <- scanner.Err()
	}()

	select {
	case u := <-urlCh:
		return u, nil
	case err := <-scanDone:
		if err != nil {
			return "", fmt.Errorf("opencode serve stdout 讀取失敗: %w", err)
		}
		return "", errors.New("opencode serve stdout 結束但未印出 listen URL")
	case err := <-waitErr:
		return "", fmt.Errorf("opencode serve 啟動失敗 (process exited): %w", err)
	case <-ctx.Done():
		return "", fmt.Errorf("opencode serve 啟動逾時 (%s): %w", supervisorStartTimeout, ctx.Err())
	}
}

func pollSupervisorHealth(ctx context.Context, baseURL, password string) error {
	client := &http.Client{Timeout: supervisorHealthRequest}
	healthURL := strings.TrimRight(baseURL, "/") + "/health"
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
		if err != nil {
			return err
		}
		req.SetBasicAuth(supervisorAuthUsername, password)
		resp, err := client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(supervisorHealthInterval):
		}
	}
}

func isExpectedExitOnSignal(err error) bool {
	if err == nil {
		return true
	}
	// runCtx cancellation by Stop is an expected termination path; Go's
	// exec.Cmd surfaces the cause as context.Canceled in some shells.
	if errors.Is(err, context.Canceled) {
		return true
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
		if status.Signaled() {
			sig := status.Signal()
			return sig == syscall.SIGTERM || sig == syscall.SIGKILL || sig == syscall.SIGINT
		}
	}
	return false
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// supervisorLogWriter forwards each line from the child's stderr to the
// supervisor's slog logger at DEBUG. Uses an internal buffer to coalesce
// partial writes into whole lines — opencode's hono logger emits
// multi-line records that arrive split across pipe writes.
type supervisorLogWriter struct {
	logger *slog.Logger
	stream string
	buf    strings.Builder
}

func newSupervisorLogWriter(logger *slog.Logger, stream string) io.Writer {
	if logger == nil {
		return io.Discard
	}
	return &supervisorLogWriter{logger: logger, stream: stream}
}

func (w *supervisorLogWriter) Write(p []byte) (int, error) {
	w.buf.Write(p)
	current := w.buf.String()
	for {
		i := strings.IndexByte(current, '\n')
		if i < 0 {
			w.buf.Reset()
			w.buf.WriteString(current)
			return len(p), nil
		}
		line := current[:i]
		current = current[i+1:]
		if line != "" {
			w.logger.Debug("opencode "+w.stream,
				"phase", "處理中",
				"line", line,
			)
		}
	}
}
