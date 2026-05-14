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

// Server supervisor for `opencode serve`. Stage 3 makes the lifecycle
// lazy: worker boot no longer Starts the server. The first job to call
// `Acquire` triggers spawn; when `activeCount` drops to zero an idle
// timer auto-stops the child after `IdleTimeout`. The existing `Start`
// method is preserved for tests and any eager-spawn caller; production
// boot path uses Acquire/Release instead.

const (
	supervisorStartTimeout       = 10 * time.Second
	supervisorStopGrace          = 5 * time.Second
	supervisorHealthInterval     = 200 * time.Millisecond
	supervisorHealthRequest      = 2 * time.Second
	supervisorDrainBudget        = 30 * time.Second
	supervisorDrainSettleBudget  = 10 * time.Second
	supervisorAbortRequest       = 5 * time.Second
	supervisorAuthUsername       = "opencode"
)

// supervisorListenLine matches opencode serve's startup line printed
// after `Server.listen` resolves. The trailing newline is stripped by the
// scanner. We capture the full URL — hostname + kernel-assigned port —
// so the supervisor never has to pre-pick a port (avoids the well-known
// pickFreePort → exec.Start race window).
var supervisorListenLine = regexp.MustCompile(`opencode server listening on (https?://\S+)`)

// supervisorState is the supervisor's internal lifecycle phase. Single-
// writer transitions under s.mu; readers may inspect via `State()` for
// retry-once decisions (Stage 3 Task 3.2-9). Not a stable contract — the
// enum is internal-but-exported only because the retry classifier in
// `runOneServer` needs to read `stateCrashed`.
type supervisorState int

const (
	stateIdle supervisorState = iota
	stateStarting
	stateRunning
	stateStopping
	stateCrashed
)

func (s supervisorState) String() string {
	switch s {
	case stateIdle:
		return "idle"
	case stateStarting:
		return "starting"
	case stateRunning:
		return "running"
	case stateStopping:
		return "stopping"
	case stateCrashed:
		return "crashed"
	}
	return "unknown"
}

// SupervisorConfig is the supervisor's constructor input. IdleTimeout is
// the auto-stop window after activeCount → 0 in lazy mode; zero disables
// idle stop (server stays up until Drain). Stage 2's always-on caller
// (eager `Start`) ignores IdleTimeout entirely.
type SupervisorConfig struct {
	BinaryPath  string
	StorageDir  string
	IdleTimeout time.Duration
	Logger      *slog.Logger
}

// Supervisor holds the long-running `opencode serve` child process for a
// worker. One Supervisor per worker process; the pool's N goroutines share
// the same BaseURL/Password via per-request `x-opencode-directory` header
// isolation (see spec § Code Style and § Boundaries Always: server-pool
// mapping = 1:N).
//
// Lifecycle (Stage 3, lazy):
//
//	idle → starting (first Acquire triggers spawn under boot lock)
//	starting → running (spawn done, /health green)
//	running → running (Acquire bumps activeCount; Release decrements;
//	                   activeCount==0 starts idle timer)
//	running → stopping (idle timer fires OR Drain called)
//	stopping → idle (clean child exit)
//	running → crashed (cmd.Wait returns unexpectedly; retry-once re-
//	                   spawns via the same Acquire path Stage 3 Task
//	                   3.2-9 wires)
//	crashed → starting (next Acquire triggers re-spawn)
//
// shuttingDown is a one-shot latch flipped by Drain so post-Drain Acquires
// fail loud rather than re-spawning during process teardown.
type Supervisor struct {
	cfg SupervisorConfig

	mu           sync.Mutex
	state        supervisorState
	activeCount  int
	idleTimer    *time.Timer
	shuttingDown bool
	// spawnReady is closed when the in-flight spawn settles (success or
	// failure). Stage 3 Acquire coalesces concurrent first-job goroutines
	// onto this channel rather than spinning a sync.Cond — channel close
	// is broadcast-safe and composes with ctx.Done in a select.
	// On spawn failure waiters re-check state under the lock and may
	// retry spawn themselves; deliberate, since the failure could be
	// transient (e.g. fake-binary-misconfigured-then-corrected) and
	// caching the error here would block legitimate retry.
	spawnReady chan struct{}
	// activeSessions tracks the in-flight session IDs so Drain can issue
	// POST /session/{id}/abort to each. Maintained by SetActiveSession /
	// ClearActiveSession from runOneServer once a session is created.
	activeSessions map[string]struct{}

	cmd      *exec.Cmd
	cancel   context.CancelFunc
	baseURL  string
	password string
	pid      int
	// exitCh is closed by the spawn's wait goroutine after cmd.Wait
	// returns. waitErr captures that return value and is safe to read
	// only after exitCh has closed. Two readers (internalStop and
	// idleStop / Drain via internalStop) coordinate via the close-as-
	// broadcast pattern — no chan-receive race.
	exitCh  chan struct{}
	waitErr error
}

// NewSupervisor constructs a Supervisor; call Acquire (lazy) or Start
// (eager) to spawn the child.
func NewSupervisor(cfg SupervisorConfig) *Supervisor {
	return &Supervisor{
		cfg:            cfg,
		state:          stateIdle,
		activeSessions: make(map[string]struct{}),
	}
}

// Start spawns the opencode serve child immediately and transitions the
// supervisor to `running` without bumping activeCount. Stage 3 worker
// boot does NOT call this — the lazy path via Acquire is preferred so
// idle workers don't hold an opencode process. Kept for tests and any
// caller that needs to confirm the binary is launchable at boot.
//
// Returns an error if the binary is missing, crashes before becoming
// ready, or if Start is called on a supervisor not in `idle` state.
func (s *Supervisor) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.shuttingDown {
		s.mu.Unlock()
		return errors.New("supervisor shutting down")
	}
	if s.state != stateIdle {
		curr := s.state
		s.mu.Unlock()
		return fmt.Errorf("supervisor already started (state=%s)", curr)
	}
	s.state = stateStarting
	s.spawnReady = make(chan struct{})
	s.mu.Unlock()

	err := s.spawn(ctx)

	s.mu.Lock()
	if err != nil {
		s.state = stateIdle
		close(s.spawnReady)
		s.mu.Unlock()
		return err
	}
	s.state = stateRunning
	close(s.spawnReady)
	s.mu.Unlock()
	return nil
}

// Acquire reserves one slot on the supervisor for an in-flight job. If
// the supervisor is `idle` (or `crashed`), the caller becomes the
// spawner; concurrent Acquires coalesce on `spawnReady`. Cancels any
// pending idle timer on success.
//
// Returns an error if the supervisor is shutting down (Drain has been
// called) or if the spawn ultimately fails.
func (s *Supervisor) Acquire(ctx context.Context) error {
	s.mu.Lock()
	for {
		if s.shuttingDown {
			s.mu.Unlock()
			return errors.New("supervisor shutting down")
		}
		switch s.state {
		case stateRunning:
			s.activeCount++
			if s.idleTimer != nil {
				s.idleTimer.Stop()
				s.idleTimer = nil
			}
			s.mu.Unlock()
			return nil
		case stateStarting:
			readyCh := s.spawnReady
			s.mu.Unlock()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-readyCh:
			}
			s.mu.Lock()
			// Re-evaluate state after spawn settles.
			continue
		case stateStopping:
			// Either idle-timer stop or Drain. For idle stop, wait until
			// stopping completes (transitions to idle), then retry. For
			// drain, shuttingDown=true so the next iteration of this loop
			// will surface the error.
			readyCh := s.spawnReady
			if readyCh == nil {
				// stopping without a paired ready channel means the stop
				// was initiated outside of Acquire's view; fall back to
				// brief sleep + re-check.
				s.mu.Unlock()
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(50 * time.Millisecond):
				}
				s.mu.Lock()
				continue
			}
			s.mu.Unlock()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-readyCh:
			}
			s.mu.Lock()
			continue
		case stateIdle, stateCrashed:
			s.state = stateStarting
			s.spawnReady = make(chan struct{})
			s.mu.Unlock()
			err := s.spawn(ctx)
			s.mu.Lock()
			if err != nil {
				s.state = stateIdle
				close(s.spawnReady)
				s.spawnReady = nil
				s.mu.Unlock()
				return err
			}
			// Drain may have flipped shuttingDown while spawn was
			// running. If so, the spawn-completion path owns the
			// teardown — Drain's `state == stateStarting` wait
			// observes our spawnReady close, but Drain itself never
			// learns about the child unless we hand it off. Tear
			// it down inline rather than transitioning to running.
			if s.shuttingDown {
				cancelRun := s.cancel
				exitCh := s.exitCh
				pid := s.pid
				s.state = stateStopping
				readyCh := s.spawnReady
				s.mu.Unlock()
				_ = s.internalStop(context.Background(), cancelRun, exitCh, pid)
				s.mu.Lock()
				s.state = stateIdle
				close(readyCh)
				s.spawnReady = nil
				s.mu.Unlock()
				return errors.New("supervisor shutting down (spawn raced with drain)")
			}
			s.state = stateRunning
			s.activeCount++
			close(s.spawnReady)
			s.spawnReady = nil
			s.mu.Unlock()
			return nil
		}
	}
}

// Release decrements activeCount. When the count reaches zero the idle
// timer is armed (`cfg.IdleTimeout`) to auto-stop the child. Safe to call
// on a supervisor in any non-idle state; a no-op if activeCount is
// already zero (defensive — should not happen in correct usage).
func (s *Supervisor) Release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeCount <= 0 {
		return
	}
	s.activeCount--
	if s.activeCount != 0 || s.state != stateRunning || s.shuttingDown {
		return
	}
	if s.cfg.IdleTimeout <= 0 {
		return
	}
	if s.idleTimer != nil {
		s.idleTimer.Stop()
	}
	s.idleTimer = time.AfterFunc(s.cfg.IdleTimeout, s.idleStop)
}

// State returns the current supervisor lifecycle phase. Reserved for
// the retry-once classifier in Task 3.2-9 (`runOneServer` reads
// `stateCrashed` to decide whether to spawn-and-retry vs. fail loud).
// Not a stable cross-package contract.
func (s *Supervisor) State() supervisorState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// SetActiveSession registers a session ID for drain bookkeeping. Called
// from runOneServer after CreateSession succeeds; Drain reads this set
// to know which abort endpoints to hit.
func (s *Supervisor) SetActiveSession(sessionID string) {
	if sessionID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activeSessions[sessionID] = struct{}{}
}

// ClearActiveSession is the runOneServer-side counterpart to
// SetActiveSession. Called from the same defer that calls Release.
func (s *Supervisor) ClearActiveSession(sessionID string) {
	if sessionID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.activeSessions, sessionID)
}

// idleStop is the idle-timer callback. Runs on a fresh goroutine; takes
// the supervisor lock to confirm the stop conditions are still valid and
// hands off to internalStop. If state has moved away from `running` by
// the time the timer fires (e.g. a new Acquire raced ahead), the stop
// is silently skipped.
func (s *Supervisor) idleStop() {
	s.mu.Lock()
	if s.state != stateRunning || s.activeCount != 0 || s.shuttingDown {
		s.mu.Unlock()
		return
	}
	s.state = stateStopping
	s.spawnReady = make(chan struct{})
	cancelRun := s.cancel
	exitCh := s.exitCh
	pid := s.pid
	s.mu.Unlock()

	if s.cfg.Logger != nil {
		s.cfg.Logger.Info("opencode serve 進入 idle stop",
			"phase", "處理中",
			"pid", pid,
			"idle_timeout", s.cfg.IdleTimeout,
		)
	}

	s.internalStop(context.Background(), cancelRun, exitCh, pid)

	s.mu.Lock()
	s.state = stateIdle
	s.idleTimer = nil
	close(s.spawnReady)
	s.spawnReady = nil
	s.mu.Unlock()
}

// Stop signals SIGTERM to the child and waits up to grace + 2s for it
// to exit. Provided as a backstop / always-on tear-down (e.g. for tests).
// Production worker shutdown should call Drain, which aborts active
// sessions first. Idempotent.
func (s *Supervisor) Stop(ctx context.Context) error {
	s.mu.Lock()
	if s.state != stateRunning {
		s.mu.Unlock()
		return nil
	}
	s.state = stateStopping
	s.spawnReady = make(chan struct{})
	if s.idleTimer != nil {
		s.idleTimer.Stop()
		s.idleTimer = nil
	}
	cancelRun := s.cancel
	exitCh := s.exitCh
	pid := s.pid
	s.mu.Unlock()

	err := s.internalStop(ctx, cancelRun, exitCh, pid)

	s.mu.Lock()
	s.state = stateIdle
	close(s.spawnReady)
	s.spawnReady = nil
	s.mu.Unlock()
	return err
}

// Drain is the worker-shutdown teardown. Snapshots active session IDs,
// POSTs `/session/{id}/abort` to each (subject to `supervisorDrainBudget`),
// then SIGTERMs the child. Sets `shuttingDown=true` permanently so any
// post-Drain Acquire fails fast rather than re-spawning during process
// teardown.
//
// Returns an error only if the abort phase itself errors hard (e.g. the
// abort budget expires with sessions still active). The teardown of
// the child still proceeds via internalStop.
func (s *Supervisor) Drain(ctx context.Context) error {
	s.mu.Lock()
	if s.shuttingDown {
		s.mu.Unlock()
		return nil
	}
	s.shuttingDown = true
	if s.idleTimer != nil {
		s.idleTimer.Stop()
		s.idleTimer = nil
	}
	// Wait out transient states (starting / stopping). shuttingDown
	// latch above tells the Acquire spawn-completion path to tear its
	// new child down rather than transition to running; idle stop in
	// flight just runs to completion. After this loop state is one of
	// {idle, running, crashed} — no orphan-child risk.
	for s.state == stateStarting || s.state == stateStopping {
		readyCh := s.spawnReady
		s.mu.Unlock()
		if readyCh == nil {
			select {
			case <-time.After(50 * time.Millisecond):
			case <-ctx.Done():
				return ctx.Err()
			}
		} else {
			select {
			case <-readyCh:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		s.mu.Lock()
	}
	if s.state != stateRunning {
		s.mu.Unlock()
		return nil
	}
	sessions := make([]string, 0, len(s.activeSessions))
	for id := range s.activeSessions {
		sessions = append(sessions, id)
	}
	baseURL := s.baseURL
	password := s.password
	s.state = stateStopping
	s.spawnReady = make(chan struct{})
	cancelRun := s.cancel
	exitCh := s.exitCh
	pid := s.pid
	s.mu.Unlock()

	var drainErr error
	if len(sessions) > 0 {
		if s.cfg.Logger != nil {
			s.cfg.Logger.Info("opencode supervisor 開始 drain",
				"phase", "處理中",
				"active_sessions", len(sessions),
				"budget", supervisorDrainBudget,
			)
		}
		drainErr = s.abortSessions(ctx, baseURL, password, sessions)

		// Wait for in-flight runOneServer attempts to observe the
		// abort and clear their session slots. Capped at
		// supervisorDrainSettleBudget; on timeout we proceed to
		// SIGTERM anyway and the warn surfaces stragglers.
		s.waitActiveSessionsCleared(ctx)
	}

	stopErr := s.internalStop(ctx, cancelRun, exitCh, pid)

	s.mu.Lock()
	s.state = stateIdle
	close(s.spawnReady)
	s.spawnReady = nil
	s.mu.Unlock()

	if drainErr != nil {
		return drainErr
	}
	return stopErr
}

// waitActiveSessionsCleared spins up to supervisorDrainSettleBudget
// polling for the activeSessions map to empty. Bound is intentionally
// shorter than the abort budget — POST aborts already had their own
// 30s window; this is just letting the in-flight runOneServer
// goroutines unwind their defers (ClearActiveSession + Release). On
// timeout we proceed to SIGTERM; the warn log surfaces remaining
// session IDs so an operator can correlate.
func (s *Supervisor) waitActiveSessionsCleared(ctx context.Context) {
	settleCtx, cancel := context.WithTimeout(ctx, supervisorDrainSettleBudget)
	defer cancel()
	for {
		s.mu.Lock()
		remaining := len(s.activeSessions)
		s.mu.Unlock()
		if remaining == 0 {
			return
		}
		select {
		case <-settleCtx.Done():
			s.mu.Lock()
			remaining = len(s.activeSessions)
			s.mu.Unlock()
			if s.cfg.Logger != nil && remaining > 0 {
				s.cfg.Logger.Warn("opencode supervisor drain settle 超時，仍有 in-flight session",
					"phase", "處理中",
					"remaining", remaining,
				)
			}
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// abortSessions POSTs /session/{id}/abort for each active session, in
// parallel, with a per-call short timeout and a global budget of
// supervisorDrainBudget. Errors are logged at warn level; the function
// returns nil if every abort either succeeded or returned a 4xx
// (session already gone), and returns an error if the global budget
// expires with abort calls still pending.
func (s *Supervisor) abortSessions(ctx context.Context, baseURL, password string, sessions []string) error {
	if len(sessions) == 0 {
		return nil
	}
	budgetCtx, cancel := context.WithTimeout(ctx, supervisorDrainBudget)
	defer cancel()

	httpClient := &http.Client{Timeout: supervisorAbortRequest}
	base := strings.TrimRight(baseURL, "/")
	var wg sync.WaitGroup
	for _, id := range sessions {
		id := id
		wg.Add(1)
		go func() {
			defer wg.Done()
			abortURL := fmt.Sprintf("%s/session/%s/abort", base, id)
			req, err := http.NewRequestWithContext(budgetCtx, http.MethodPost, abortURL, nil)
			if err != nil {
				if s.cfg.Logger != nil {
					s.cfg.Logger.Warn("opencode 取消 session 失敗",
						"phase", "失敗",
						"session_id", id,
						"error", err,
					)
				}
				return
			}
			req.SetBasicAuth(supervisorAuthUsername, password)
			resp, err := httpClient.Do(req)
			if err != nil {
				if s.cfg.Logger != nil {
					s.cfg.Logger.Warn("opencode 取消 session 失敗",
						"phase", "失敗",
						"session_id", id,
						"error", err,
					)
				}
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}()
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-budgetCtx.Done():
		return fmt.Errorf("opencode supervisor abort budget exceeded (%s)", supervisorDrainBudget)
	}
}

// internalStop runs the SIGTERM → grace → reset sequence shared by
// Stop, idleStop, and Drain. Caller is responsible for state transitions
// (flipping to stopping, closing spawnReady on completion). Waits for
// exitCh to close (signalled by spawn's wait goroutine after cmd.Wait
// returns) rather than reading the wait error channel directly — close-
// as-broadcast lets multiple stop initiators coexist without a race for
// a single-receive channel value. The wait error itself is read from
// s.waitErr via getWaitErr after exitCh has closed.
func (s *Supervisor) internalStop(ctx context.Context, cancelRun context.CancelFunc, exitCh chan struct{}, pid int) error {
	if cancelRun != nil {
		cancelRun()
	}

	var err error
	if exitCh != nil {
		select {
		case <-exitCh:
			err = s.getWaitErr()
		case <-ctx.Done():
			err = fmt.Errorf("stop opencode serve: %w", ctx.Err())
		case <-time.After(supervisorStopGrace + 2*time.Second):
			err = errors.New("opencode serve did not exit within grace window")
		}
	}

	if s.cfg.Logger != nil {
		s.cfg.Logger.Info("opencode serve 已停止",
			"phase", "完成",
			"pid", pid,
			"wait_err", errString(err),
		)
	}

	s.mu.Lock()
	s.cmd = nil
	s.cancel = nil
	s.baseURL = ""
	s.password = ""
	s.pid = 0
	s.exitCh = nil
	s.waitErr = nil
	s.activeCount = 0
	s.activeSessions = make(map[string]struct{})
	s.mu.Unlock()

	if isExpectedExitOnSignal(err) {
		return nil
	}
	return err
}

// BaseURL returns the listen URL the child reported on startup, e.g.
// "http://127.0.0.1:50000". Empty before Acquire/Start succeeds.
func (s *Supervisor) BaseURL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.baseURL
}

// Password returns the random OPENCODE_SERVER_PASSWORD passed to the
// child via env. Empty before Acquire/Start succeeds.
func (s *Supervisor) Password() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.password
}

// ChildPID returns the OS process ID of the spawned `opencode serve`.
// Zero before Acquire/Start succeeds. Returns the stale-but-dead PID
// briefly during Stop/idleStop/Drain before internal reset; treat the
// value as informational.
func (s *Supervisor) ChildPID() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pid
}

// spawn is the pure fork-and-wait-ready primitive shared by Start and
// Acquire. Does not touch state machine fields directly (caller flips
// state before / after) — except for the unexpected-exit branch in the
// wait goroutine, which flips `running → crashed` on its own. On
// success, fills s.cmd / cancel / baseURL / password / pid / exitCh
// under s.mu. On failure, ensures runCtx is cancelled so no leak.
func (s *Supervisor) spawn(ctx context.Context) error {
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

	exitCh := make(chan struct{})
	// Wait goroutine: single owner of cmd.Wait. On exit it captures the
	// error into s.waitErr (single-writer; readers gate on exitCh close)
	// and flips state to `crashed` when state was still `running` —
	// signals an unexpected death that retry-once (Task 3.2-9) can act
	// on. When state is already `stopping`, the goroutine just observes
	// the exit; the stop initiator owns the transition.
	go func() {
		werr := cmd.Wait()
		s.mu.Lock()
		s.waitErr = werr
		if s.state == stateRunning {
			s.state = stateCrashed
			if s.cfg.Logger != nil {
				s.cfg.Logger.Warn("opencode serve 意外退出",
					"phase", "失敗",
					"pid", s.pid,
					"wait_err", errString(werr),
				)
			}
		}
		s.mu.Unlock()
		close(exitCh)
	}()

	startCtx, cancelStart := context.WithTimeout(ctx, supervisorStartTimeout)
	defer cancelStart()

	baseURL, err := awaitSupervisorListenURL(startCtx, stdout, exitCh, s.getWaitErr, s.cfg.Logger)
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
	s.exitCh = exitCh
	s.waitErr = nil
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

// getWaitErr returns the wait goroutine's last captured cmd.Wait error.
// Only valid to read after exitCh closes (single-writer pattern). Used
// by awaitSupervisorListenURL on the early-exit branch to surface the
// child's exit error before listen URL is printed.
func (s *Supervisor) getWaitErr() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.waitErr
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

func awaitSupervisorListenURL(ctx context.Context, stdout io.ReadCloser, exitCh <-chan struct{}, getWaitErr func() error, logger *slog.Logger) (string, error) {
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
	case <-exitCh:
		return "", fmt.Errorf("opencode serve 啟動失敗 (process exited): %w", getWaitErr())
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
