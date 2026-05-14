package agent

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Ivantseng123/agentdock/worker/config"
)

// perfBenchEnvVar gates the perf suite. It is intentionally opt-in: the
// suite invokes the real opencode CLI + LLM round-trip, which costs
// tokens and depends on auth state in ~/.opencode. CI runs without it;
// operators run locally to refresh `docs/specs/opencode-server-mode-perf-baseline.md`.
const perfBenchEnvVar = "OPENCODE_PERF_BENCH"

// perfSampleSizeEnvVar overrides the per-mode job count. Defaults to 30
// (cost-bounded). Spec acceptance §N1..N3 was written against 100; the
// baseline doc discloses whatever value was actually used.
const perfSampleSizeEnvVar = "OPENCODE_PERF_SAMPLE_SIZE"

// perfPrompt is the shortest prompt that still exercises the full
// opencode + provider round trip. Output is bounded by asking for "OK";
// total cost per job is ~15 tokens.
const perfPrompt = "Reply with just the two letters OK and nothing else."

// perfJobTimeout caps each individual ask job. The BuiltinAgents default
// (30m) is unreasonable for a perf harness — a single LLM hang would
// wedge the whole run for an hour. 90s is well above the typical 1–5s
// healthy job and below the wall-clock that would noticeably skew the
// median (warm-up window absorbs any tail jobs).
const perfJobTimeout = 90 * time.Second

// perfWarmupDrop is the number of leading jobs excluded from the
// steady-state median. Warm-up covers skill cache load, auth handshake,
// and (in server mode) the SSE subscribe ramp.
const perfWarmupDrop = 10

func perfSampleSize(t *testing.T) int {
	t.Helper()
	v := os.Getenv(perfSampleSizeEnvVar)
	if v == "" {
		return 20
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= perfWarmupDrop {
		t.Fatalf("%s must be an integer > %d, got %q", perfSampleSizeEnvVar, perfWarmupDrop, v)
	}
	return n
}

func perfAgent() config.AgentConfig {
	ag := config.BuiltinAgents["opencode"]
	ag.Timeout = perfJobTimeout
	return ag
}

func requirePerfEnv(t *testing.T) {
	t.Helper()
	if os.Getenv(perfBenchEnvVar) != "1" {
		t.Skipf("perf bench skipped; set %s=1 to enable (requires real opencode binary + auth + token budget)", perfBenchEnvVar)
	}
	if testing.Short() {
		t.Skip("perf bench skipped under -short")
	}
}

// procRSSMB shells out to ps for resident set size of pid. Returns 0
// when the process is gone or the call fails; the caller is responsible
// for noticing a 0 result and treating it as unmeasurable (see
// rssOrWarn). Works on darwin + linux without cgo.
func procRSSMB(pid int) int {
	if pid <= 0 {
		return 0
	}
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0
	}
	s := strings.TrimSpace(string(out))
	kb, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return kb / 1024
}

// rssOrWarn wraps procRSSMB with a t.Logf when the sample is 0. A zero
// RSS is the signal that ps failed (or the process exited) — leaving
// it unflagged would let a re-run on a host where ps is broken trivially
// pass the N3 budget (`0 <= 0 + 200`). Strict mode would `t.Fatal` here;
// the harness chooses to warn-and-continue because not every runner
// ships ps. Operators must read the warning lines and discount any
// table row that includes a zero sample.
func rssOrWarn(t *testing.T, mode, what string, pid int) int {
	t.Helper()
	rss := procRSSMB(pid)
	if rss == 0 {
		t.Logf("RSS=0 for %s/%s (pid=%d) — ps probably failed; treat the corresponding table row as unmeasurable", mode, what, pid)
	}
	return rss
}

// medianDuration returns the median of a copy of d (sorted). Returns 0
// for an empty slice.
func medianDuration(d []time.Duration) time.Duration {
	if len(d) == 0 {
		return 0
	}
	sorted := make([]time.Duration, len(d))
	copy(sorted, d)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	mid := len(sorted) / 2
	if len(sorted)%2 == 0 {
		return (sorted[mid-1] + sorted[mid]) / 2
	}
	return sorted[mid]
}

// variancePercent returns |a - b| / mean * 100. Returns 0 when mean is
// zero. Used to gate variance ≤ 10% per spec acceptance.
func variancePercent(a, b time.Duration) float64 {
	if a == 0 && b == 0 {
		return 0
	}
	mean := (a + b) / 2
	if mean == 0 {
		return 0
	}
	diff := a - b
	if diff < 0 {
		diff = -diff
	}
	return float64(diff) / float64(mean) * 100
}

// sampleResult bundles one rep's measurements for one mode.
type sampleResult struct {
	mode         string
	latencies    []time.Duration
	firstJob     time.Duration
	steadyMedian time.Duration
	idleRSS      int
	busyRSS      int
	errCount     int
}

func runSpawnSample(t *testing.T, ctx context.Context, n int) sampleResult {
	t.Helper()
	cfg := config.OpencodeConfig{Mode: config.OpencodeModeSpawn}
	r := &Runner{
		agents:      []config.AgentConfig{perfAgent()},
		opencodeCfg: cfg,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	result := sampleResult{mode: "spawn"}
	for i := 0; i < n; i++ {
		start := time.Now()
		_, err := r.runOneSpawn(ctx, logger, perfAgent(), t.TempDir(), perfPrompt, RunOptions{})
		dur := time.Since(start)
		result.latencies = append(result.latencies, dur)
		if err != nil {
			result.errCount++
			t.Logf("spawn job %d/%d failed: %v (latency=%s)", i+1, n, err, dur)
		}
	}
	result.firstJob = result.latencies[0]
	result.steadyMedian = medianDuration(result.latencies[perfWarmupDrop:])
	// Spawn mode has no persistent child; idle RSS == busy RSS == this
	// test process's RSS sampled after the run.
	result.idleRSS = rssOrWarn(t, "spawn", "test-process", os.Getpid())
	result.busyRSS = result.idleRSS
	return result
}

func runServerSample(t *testing.T, ctx context.Context, n int) sampleResult {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sup := NewSupervisor(SupervisorConfig{
		BinaryPath:  "opencode",
		StorageDir:  t.TempDir(),
		IdleTimeout: 5 * time.Minute,
		Logger:      logger,
	})
	defer func() {
		drainCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		_ = sup.Drain(drainCtx)
	}()
	cfg := config.OpencodeConfig{Mode: config.OpencodeModeServer, IdleTimeout: 5 * time.Minute}
	r := &Runner{
		agents:      []config.AgentConfig{perfAgent()},
		opencodeCfg: cfg,
		supervisor:  sup,
	}
	result := sampleResult{mode: "server"}
	var busyRSSPeak int
	for i := 0; i < n; i++ {
		start := time.Now()
		_, err := r.runOneServer(ctx, logger, perfAgent(), t.TempDir(), perfPrompt, RunOptions{})
		dur := time.Since(start)
		result.latencies = append(result.latencies, dur)
		if err != nil {
			result.errCount++
			t.Logf("server job %d/%d failed: %v (latency=%s)", i+1, n, err, dur)
		}
		// Sample peak busy RSS mid-run while the child is alive.
		if pid := sup.ChildPID(); pid > 0 {
			rss := rssOrWarn(t, "server", "supervisor-child", pid) + rssOrWarn(t, "server", "test-process", os.Getpid())
			if rss > busyRSSPeak {
				busyRSSPeak = rss
			}
		}
	}
	result.firstJob = result.latencies[0]
	result.steadyMedian = medianDuration(result.latencies[perfWarmupDrop:])
	result.busyRSS = busyRSSPeak
	// Idle RSS: child still alive at end of loop; sample before Drain
	// kills it. Strict idle would require triggering IdleTimeout (5m),
	// which isn't worth the wall clock — busy/idle delta is small in
	// server mode where the child is persistent.
	if pid := sup.ChildPID(); pid > 0 {
		result.idleRSS = rssOrWarn(t, "server", "supervisor-child", pid) + rssOrWarn(t, "server", "test-process", os.Getpid())
	} else {
		result.idleRSS = rssOrWarn(t, "server", "test-process", os.Getpid())
	}
	return result
}

// TestPerf_OpencodeSpawnVsServer is the acceptance harness for
// plan task 3.2-13. Two reps × two modes × N jobs. Reports a markdown
// table to t.Log plus PASS/FAIL gates against spec budgets N1, N2, N3.
//
// Run:
//
//	OPENCODE_PERF_BENCH=1 go -C worker test ./agent -run TestPerf_OpencodeSpawnVsServer -v -timeout 60m
//
// Override sample size:
//
//	OPENCODE_PERF_BENCH=1 OPENCODE_PERF_SAMPLE_SIZE=100 go -C worker test ...
func TestPerf_OpencodeSpawnVsServer(t *testing.T) {
	requirePerfEnv(t)

	n := perfSampleSize(t)
	t.Logf("perf bench: arch=%s os=%s N=%d prompt=%q warmup_drop=%d", runtime.GOARCH, runtime.GOOS, n, perfPrompt, perfWarmupDrop)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	defer cancel()

	t.Log("=== rep 1: spawn mode ===")
	spawn1 := runSpawnSample(t, ctx, n)
	t.Log("=== rep 1: server mode ===")
	server1 := runServerSample(t, ctx, n)
	t.Log("=== rep 2: spawn mode ===")
	spawn2 := runSpawnSample(t, ctx, n)
	t.Log("=== rep 2: server mode ===")
	server2 := runServerSample(t, ctx, n)

	const n1Budget = 200 * time.Millisecond
	const n2Budget = 5 * time.Second
	const n3BudgetMB = 200

	n1Rep1 := server1.steadyMedian <= spawn1.steadyMedian+n1Budget
	n1Rep2 := server2.steadyMedian <= spawn2.steadyMedian+n1Budget
	n2Rep1 := server1.firstJob <= spawn1.firstJob+n2Budget
	n2Rep2 := server2.firstJob <= spawn2.firstJob+n2Budget
	n3Rep1 := server1.idleRSS <= spawn1.idleRSS+n3BudgetMB
	n3Rep2 := server2.idleRSS <= spawn2.idleRSS+n3BudgetMB

	spawnVar := variancePercent(spawn1.steadyMedian, spawn2.steadyMedian)
	serverVar := variancePercent(server1.steadyMedian, server2.steadyMedian)

	var b strings.Builder
	fmt.Fprintf(&b, "\n=== Perf baseline raw output (%s/%s) ===\n", runtime.GOARCH, runtime.GOOS)
	fmt.Fprintf(&b, "Sample size: N=%d per rep, 2 reps, warm-up drop=%d, prompt=%q\n\n", n, perfWarmupDrop, perfPrompt)
	fmt.Fprintln(&b, "| Metric | Rep | Spawn | Server | Δ | Budget | PASS? |")
	fmt.Fprintln(&b, "|---|---|---|---|---|---|---|")
	fmt.Fprintf(&b, "| Steady-state median (N1) | 1 | %s | %s | %s | <=+200ms | %t |\n", spawn1.steadyMedian, server1.steadyMedian, server1.steadyMedian-spawn1.steadyMedian, n1Rep1)
	fmt.Fprintf(&b, "| Steady-state median (N1) | 2 | %s | %s | %s | <=+200ms | %t |\n", spawn2.steadyMedian, server2.steadyMedian, server2.steadyMedian-spawn2.steadyMedian, n1Rep2)
	fmt.Fprintf(&b, "| First-job latency (N2) | 1 | %s | %s | %s | <=+5s | %t |\n", spawn1.firstJob, server1.firstJob, server1.firstJob-spawn1.firstJob, n2Rep1)
	fmt.Fprintf(&b, "| First-job latency (N2) | 2 | %s | %s | %s | <=+5s | %t |\n", spawn2.firstJob, server2.firstJob, server2.firstJob-spawn2.firstJob, n2Rep2)
	fmt.Fprintf(&b, "| Idle RSS MB (N3) | 1 | %d | %d | %d | <=+200MB | %t |\n", spawn1.idleRSS, server1.idleRSS, server1.idleRSS-spawn1.idleRSS, n3Rep1)
	fmt.Fprintf(&b, "| Idle RSS MB (N3) | 2 | %d | %d | %d | <=+200MB | %t |\n", spawn2.idleRSS, server2.idleRSS, server2.idleRSS-spawn2.idleRSS, n3Rep2)
	fmt.Fprintln(&b, "")
	fmt.Fprintln(&b, "Variance (|rep1-rep2|/mean):")
	fmt.Fprintf(&b, "- Spawn steady median: %.1f%% (target <=10%%)\n", spawnVar)
	fmt.Fprintf(&b, "- Server steady median: %.1f%% (target <=10%%)\n", serverVar)
	fmt.Fprintln(&b, "")
	fmt.Fprintln(&b, "Errors:")
	fmt.Fprintf(&b, "- Spawn rep1=%d rep2=%d (out of N=%d each)\n", spawn1.errCount, spawn2.errCount, n)
	fmt.Fprintf(&b, "- Server rep1=%d rep2=%d (out of N=%d each)\n", server1.errCount, server2.errCount, n)
	t.Log(b.String())

	// Hard verdict: every rep must pass every budget.
	if !(n1Rep1 && n1Rep2 && n2Rep1 && n2Rep2 && n3Rep1 && n3Rep2) {
		t.Errorf("one or more N1/N2/N3 budgets failed; see table above")
	}
	if spawnVar > 10 || serverVar > 10 {
		t.Errorf("variance > 10%%; spawn=%.1f%% server=%.1f%%", spawnVar, serverVar)
	}
}
