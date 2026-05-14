package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Ivantseng123/agentdock/worker/config"
)

// TestSpawnBugABReproduction is the empirical evidence gate for the
// opencode-server-mode spec premise. ADR-0005 documents two bugs that
// motivated the entire Phase 3.2 work:
//
//   - Bug A: opencode's LLM provider returns an empty SSE; opencode
//     defaults finish_reason="other" + tokens=0; worker logs success
//     but the answer is empty.
//   - Bug B: opencode CLI race — bootstrap.ts dispose runs ~131ms
//     before trailing `message.part.updated` time.end events arrive,
//     truncating short answers.
//
// Both manifest as `runOneSpawn` returning (output="", err=nil) — the
// "silent answer drop" pattern. opencode_spawn.go:252-256 logs a warn
// but returns nil err, so any caller (worker pool, perf bench) that
// only checks err sees "success".
//
// This test runs spawn mode N times with the perf harness's short
// prompt fixture and counts silent drops. If the count is nonzero on
// the current dev box + opencode 1.14.41, the spec premise stands and
// server-mode investigation (FUP-1..4) should continue. If silent
// drops do not reproduce in N=30, the premise is in question — the
// original production debugging (job 20260508-090827-13bf08a3) may
// have been driven by transient provider issues rather than a
// reproducible CLI race.
//
// This is investigation, NOT production code. The test is opt-in via
// OPENCODE_BUG_REPRO=1 because each invocation costs LLM tokens.
//
// Run:
//
//	OPENCODE_BUG_REPRO=1 go -C worker test ./agent -run TestSpawnBugABReproduction -count=1 -timeout 20m -v
const bugReproEnvVar = "OPENCODE_BUG_REPRO"

func TestSpawnBugABReproduction(t *testing.T) {
	if os.Getenv(bugReproEnvVar) != "1" {
		t.Skipf("bug-repro test skipped; set %s=1 to enable (requires real opencode + auth + token budget)", bugReproEnvVar)
	}
	if testing.Short() {
		t.Skip("bug-repro test skipped under -short")
	}

	const n = 30
	const prompt = "Reply with just the two letters OK and nothing else."

	cfg := config.OpencodeConfig{Mode: config.OpencodeModeSpawn}
	r := &Runner{
		agents:      []config.AgentConfig{perfAgent()},
		opencodeCfg: cfg,
	}

	// Capture warns from runOneSpawn — the "exit 0 但 stdout 空" log line
	// (opencode_spawn.go:254) fires on the Bug A/B silent-drop path; we
	// want both the return value AND the warn count.
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	ctx := context.Background()
	var silentDrops, nonNilErr, suspiciousOutput, healthy int
	var samples []string
	for i := 0; i < n; i++ {
		output, err := r.runOneSpawn(ctx, logger, perfAgent(), t.TempDir(), prompt, RunOptions{})
		if err != nil {
			nonNilErr++
			t.Logf("job %d/%d returned err: %v", i+1, n, err)
			continue
		}
		trimmed := strings.TrimSpace(output)
		if trimmed == "" {
			silentDrops++
			t.Logf("job %d/%d: SILENT DROP (output empty, err nil — Bug A/B pattern)", i+1, n)
			continue
		}
		lower := strings.ToLower(trimmed)
		if !strings.Contains(lower, "ok") {
			suspiciousOutput++
			snippet := trimmed
			if len(snippet) > 200 {
				snippet = snippet[:200] + "...(truncated)"
			}
			t.Logf("job %d/%d: suspicious output (no 'ok' in answer): %q", i+1, n, snippet)
			samples = append(samples, snippet)
			continue
		}
		healthy++
	}

	pct := func(c int) float64 { return float64(c) / float64(n) * 100 }
	var b strings.Builder
	fmt.Fprintf(&b, "\n=== Spawn Bug A/B reproduction (N=%d, opencode 1.14.41 on darwin/arm64) ===\n", n)
	fmt.Fprintf(&b, "Prompt: %q\n\n", prompt)
	fmt.Fprintf(&b, "Healthy (output contains 'ok'):           %d/%d  (%.1f%%)\n", healthy, n, pct(healthy))
	fmt.Fprintf(&b, "Silent drops (output empty, err nil):     %d/%d  (%.1f%%)  <- Bug A/B signature\n", silentDrops, n, pct(silentDrops))
	fmt.Fprintf(&b, "Suspicious output (no 'ok' in answer):    %d/%d  (%.1f%%)\n", suspiciousOutput, n, pct(suspiciousOutput))
	fmt.Fprintf(&b, "Non-nil err (timeout / exec failure):     %d/%d  (%.1f%%)\n", nonNilErr, n, pct(nonNilErr))
	fmt.Fprintln(&b)
	if silentDrops > 0 {
		fmt.Fprintf(&b, "VERDICT: Bug A/B reproduced %d times in %d attempts. Spec premise stands.\n", silentDrops, n)
	} else {
		fmt.Fprintf(&b, "VERDICT: Bug A/B did NOT reproduce in %d attempts on this binary.\n", n)
		fmt.Fprintln(&b, "Implication: either the bug is rare (P(silent) < 1/N), or it has been")
		fmt.Fprintln(&b, "fixed upstream, or the original production case was provider-transient.")
		fmt.Fprintln(&b, "Spec premise becomes weaker without a reliable repro.")
	}
	if len(samples) > 0 {
		fmt.Fprintln(&b, "\nSuspicious output samples:")
		for i, s := range samples {
			fmt.Fprintf(&b, "  [%d] %s\n", i+1, s)
		}
	}
	if warns := logBuf.String(); warns != "" {
		const tailN = 4000
		shown := warns
		if len(shown) > tailN {
			shown = "..." + shown[len(shown)-tailN:]
		}
		fmt.Fprintf(&b, "\nWorker warn-level logs (tail %d bytes):\n%s\n", tailN, shown)
	}
	t.Log(b.String())

	// This test always passes — its purpose is reporting, not gating.
	// The reviewer reads the verdict line and decides next steps.
}

// TestServerModeTimingBreakdown is a diagnostic probe for the Stage 4
// perf finding ("server mode 20-30x slower than spawn"). It calls the
// supervisor + client sub-steps directly with per-step timing so we
// can localize where the latency is spent: supervisor warm-up,
// session creation, prompt POST, or SSE wait.
//
// Two variants run back to back to test the leading hypothesis (per-
// request workdir change triggers `opencode serve` to reload skills /
// config):
//
//   - fresh:  every job uses a fresh t.TempDir() as workdir (matches
//             production: each ask gets a unique cloned repo dir)
//   - shared: all jobs share one workdir (would-be best case if the
//             per-directory reload hypothesis is correct)
//
// Plus: verify whether server mode actually returns non-empty output
// (ADR-0005's "by construction Bug A/B is eliminated" claim).
//
// Run:
//
//	OPENCODE_BUG_REPRO=1 go -C worker test ./agent -run TestServerModeTimingBreakdown -count=1 -timeout 20m -v
func TestServerModeTimingBreakdown(t *testing.T) {
	if os.Getenv(bugReproEnvVar) != "1" {
		t.Skipf("server-mode diagnose skipped; set %s=1 to enable", bugReproEnvVar)
	}
	if testing.Short() {
		t.Skip("skipped under -short")
	}

	const (
		n      = 8
		prompt = "Reply with just the two letters OK and nothing else."
	)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	sup := NewSupervisor(SupervisorConfig{
		BinaryPath:  "opencode",
		StorageDir:  t.TempDir(),
		IdleTimeout: 10 * time.Minute,
		Logger:      logger,
	})
	defer func() {
		drainCtx, dcancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer dcancel()
		_ = sup.Drain(drainCtx)
	}()
	if err := sup.Acquire(ctx); err != nil {
		t.Fatalf("supervisor Acquire (warm up): %v", err)
	}
	defer sup.Release()
	client := NewClient(sup.BaseURL(), sup.Password(), nil)

	type sample struct {
		variant     string
		idx         int
		tSession    time.Duration
		tSendPrompt time.Duration
		tWait       time.Duration
		tTotal      time.Duration
		outputLen   int
		errMsg      string
	}

	runOne := func(variant, workDir string, idx int) sample {
		s := sample{variant: variant, idx: idx}
		jobStart := time.Now()

		t0 := time.Now()
		sessionID, err := client.CreateSession(ctx, workDir)
		s.tSession = time.Since(t0)
		if err != nil {
			s.errMsg = fmt.Sprintf("CreateSession: %v", err)
			s.tTotal = time.Since(jobStart)
			return s
		}
		sup.SetActiveSession(sessionID)
		defer sup.ClearActiveSession(sessionID)

		t1 := time.Now()
		run, err := client.SendPrompt(ctx, sessionID, workDir, prompt)
		s.tSendPrompt = time.Since(t1)
		if err != nil {
			s.errMsg = fmt.Sprintf("SendPrompt: %v", err)
			s.tTotal = time.Since(jobStart)
			return s
		}

		// Drain events so PromptRun.Wait isn't blocked on a full channel.
		go func() {
			for range run.Events {
			}
		}()

		t2 := time.Now()
		output, waitErr := run.Wait()
		s.tWait = time.Since(t2)
		if waitErr != nil {
			s.errMsg = fmt.Sprintf("Wait: %v", waitErr)
		}
		s.outputLen = len(strings.TrimSpace(output))
		s.tTotal = time.Since(jobStart)
		return s
	}

	var fresh, shared []sample
	for i := 0; i < n; i++ {
		fresh = append(fresh, runOne("fresh", t.TempDir(), i+1))
	}
	sharedDir := t.TempDir()
	for i := 0; i < n; i++ {
		shared = append(shared, runOne("shared", sharedDir, i+1))
	}

	medOf := func(samples []sample, pick func(sample) time.Duration) time.Duration {
		vals := make([]time.Duration, 0, len(samples))
		for _, s := range samples {
			vals = append(vals, pick(s))
		}
		return medianDuration(vals)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "\n=== Server-mode timing breakdown (N=%d each variant, prompt=%q) ===\n", n, prompt)
	fmt.Fprintln(&b, "Per-job stages: CreateSession (POST /session) + SendPrompt (POST /session/{id}/message + GET /event) + Wait (block until completion event)")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "Per-job samples (variant fresh = new t.TempDir() per job, shared = same workdir for all):")
	fmt.Fprintln(&b, "| Variant | # | CreateSession | SendPrompt | Wait | Total | OutputLen | Err |")
	fmt.Fprintln(&b, "|---|---|---|---|---|---|---|---|")
	for _, s := range fresh {
		fmt.Fprintf(&b, "| fresh | %d | %s | %s | %s | %s | %d | %s |\n", s.idx, s.tSession, s.tSendPrompt, s.tWait, s.tTotal, s.outputLen, s.errMsg)
	}
	for _, s := range shared {
		fmt.Fprintf(&b, "| shared | %d | %s | %s | %s | %s | %d | %s |\n", s.idx, s.tSession, s.tSendPrompt, s.tWait, s.tTotal, s.outputLen, s.errMsg)
	}
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "Medians:")
	fmt.Fprintf(&b, "  fresh:  CreateSession=%s  SendPrompt=%s  Wait=%s  Total=%s\n",
		medOf(fresh, func(s sample) time.Duration { return s.tSession }),
		medOf(fresh, func(s sample) time.Duration { return s.tSendPrompt }),
		medOf(fresh, func(s sample) time.Duration { return s.tWait }),
		medOf(fresh, func(s sample) time.Duration { return s.tTotal }))
	fmt.Fprintf(&b, "  shared: CreateSession=%s  SendPrompt=%s  Wait=%s  Total=%s\n",
		medOf(shared, func(s sample) time.Duration { return s.tSession }),
		medOf(shared, func(s sample) time.Duration { return s.tSendPrompt }),
		medOf(shared, func(s sample) time.Duration { return s.tWait }),
		medOf(shared, func(s sample) time.Duration { return s.tTotal }))
	fmt.Fprintln(&b)
	var freshOK, sharedOK int
	for _, s := range fresh {
		if s.outputLen > 0 && s.errMsg == "" {
			freshOK++
		}
	}
	for _, s := range shared {
		if s.outputLen > 0 && s.errMsg == "" {
			sharedOK++
		}
	}
	fmt.Fprintf(&b, "Healthy output (outputLen>0 + no err): fresh=%d/%d  shared=%d/%d\n", freshOK, n, sharedOK, n)
	t.Log(b.String())
}

// TestServerModeSessionReuse tests whether the ~10s per-job overhead
// (observed in TestServerModeTimingBreakdown shared variant) is paid
// at session creation (load skills, init provider) vs. per message
// inside an existing session.
//
//   - fresh-session:   N jobs, each creates a new session, then POSTs
//                      one message. Mirrors production worker behavior.
//   - reused-session:  ONE session created up-front, N messages POSTed
//                      into it. If this is significantly faster, the
//                      per-job overhead lives in session init.
//
// Mutating semantics (subsequent prompts share prior message history)
// is acceptable for a timing probe — the answer is still "OK" each
// time, so output check still works.
func TestServerModeSessionReuse(t *testing.T) {
	if os.Getenv(bugReproEnvVar) != "1" {
		t.Skipf("server-mode session-reuse skipped; set %s=1 to enable", bugReproEnvVar)
	}
	if testing.Short() {
		t.Skip("skipped under -short")
	}

	const (
		n      = 6
		prompt = "Reply with just the two letters OK and nothing else."
	)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	sup := NewSupervisor(SupervisorConfig{
		BinaryPath:  "opencode",
		StorageDir:  t.TempDir(),
		IdleTimeout: 10 * time.Minute,
		Logger:      logger,
	})
	defer func() {
		drainCtx, dcancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer dcancel()
		_ = sup.Drain(drainCtx)
	}()
	if err := sup.Acquire(ctx); err != nil {
		t.Fatalf("supervisor Acquire: %v", err)
	}
	defer sup.Release()
	client := NewClient(sup.BaseURL(), sup.Password(), nil)
	workDir := t.TempDir()

	runOnePrompt := func(sessionID string, idx int) (sendDur, waitDur, totalDur time.Duration, outputLen int, errMsg string) {
		jobStart := time.Now()
		t1 := time.Now()
		run, err := client.SendPrompt(ctx, sessionID, workDir, prompt)
		sendDur = time.Since(t1)
		if err != nil {
			errMsg = fmt.Sprintf("SendPrompt: %v", err)
			totalDur = time.Since(jobStart)
			return
		}
		go func() {
			for range run.Events {
			}
		}()
		t2 := time.Now()
		output, waitErr := run.Wait()
		waitDur = time.Since(t2)
		if waitErr != nil {
			errMsg = fmt.Sprintf("Wait: %v", waitErr)
		}
		outputLen = len(strings.TrimSpace(output))
		totalDur = time.Since(jobStart)
		_ = idx
		return
	}

	// Variant fresh: new session per job.
	type sample struct {
		variant   string
		idx       int
		tSession  time.Duration
		tSend     time.Duration
		tWait     time.Duration
		tTotal    time.Duration
		outputLen int
		errMsg    string
	}
	var fresh []sample
	for i := 0; i < n; i++ {
		s := sample{variant: "fresh-session", idx: i + 1}
		t0 := time.Now()
		sid, err := client.CreateSession(ctx, workDir)
		s.tSession = time.Since(t0)
		if err != nil {
			s.errMsg = fmt.Sprintf("CreateSession: %v", err)
			fresh = append(fresh, s)
			continue
		}
		sup.SetActiveSession(sid)
		s.tSend, s.tWait, s.tTotal, s.outputLen, s.errMsg = runOnePrompt(sid, i+1)
		sup.ClearActiveSession(sid)
		s.tTotal += s.tSession
		fresh = append(fresh, s)
	}

	// Variant reused: one session, N prompts.
	var reused []sample
	reuseStart := time.Now()
	sharedSID, err := client.CreateSession(ctx, workDir)
	reuseSessionDur := time.Since(reuseStart)
	if err != nil {
		t.Fatalf("CreateSession for reused variant: %v", err)
	}
	sup.SetActiveSession(sharedSID)
	defer sup.ClearActiveSession(sharedSID)
	for i := 0; i < n; i++ {
		s := sample{variant: "reused-session", idx: i + 1}
		// Only the first sample carries the upfront CreateSession cost.
		if i == 0 {
			s.tSession = reuseSessionDur
		}
		s.tSend, s.tWait, s.tTotal, s.outputLen, s.errMsg = runOnePrompt(sharedSID, i+1)
		if i == 0 {
			s.tTotal += reuseSessionDur
		}
		reused = append(reused, s)
	}

	medOf := func(samples []sample, pick func(sample) time.Duration) time.Duration {
		vals := make([]time.Duration, 0, len(samples))
		for _, s := range samples {
			vals = append(vals, pick(s))
		}
		return medianDuration(vals)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "\n=== Server-mode session-reuse breakdown (N=%d, prompt=%q) ===\n", n, prompt)
	fmt.Fprintln(&b, "Per-job samples:")
	fmt.Fprintln(&b, "| Variant | # | CreateSession | SendPrompt | Wait | Total | OutputLen | Err |")
	fmt.Fprintln(&b, "|---|---|---|---|---|---|---|---|")
	for _, s := range fresh {
		fmt.Fprintf(&b, "| %s | %d | %s | %s | %s | %s | %d | %s |\n",
			s.variant, s.idx, s.tSession, s.tSend, s.tWait, s.tTotal, s.outputLen, s.errMsg)
	}
	for _, s := range reused {
		fmt.Fprintf(&b, "| %s | %d | %s | %s | %s | %s | %d | %s |\n",
			s.variant, s.idx, s.tSession, s.tSend, s.tWait, s.tTotal, s.outputLen, s.errMsg)
	}
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "Medians:")
	fmt.Fprintf(&b, "  fresh-session  (new session per job): Wait=%s  Total=%s\n",
		medOf(fresh, func(s sample) time.Duration { return s.tWait }),
		medOf(fresh, func(s sample) time.Duration { return s.tTotal }))
	fmt.Fprintf(&b, "  reused-session (1 session, N msgs):    Wait=%s  Total=%s\n",
		medOf(reused, func(s sample) time.Duration { return s.tWait }),
		medOf(reused, func(s sample) time.Duration { return s.tTotal }))
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "Interpretation:")
	fmt.Fprintln(&b, "  - If reused Wait << fresh Wait: per-session init (skill/provider load) is the per-job overhead.")
	fmt.Fprintln(&b, "  - If reused Wait ≈ fresh Wait: overhead is per-message inside opencode server, not per-session.")
	t.Log(b.String())
}
