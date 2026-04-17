package bot

import (
	"testing"
	"time"

	"agentdock/internal/queue"
)

func TestShortWorker(t *testing.T) {
	cases := []struct{ in, want string }{
		{"host-1/worker-3", "worker-3"},
		{"my-k8s-pod/worker-0", "worker-0"},
		{"noSlash", "noSlash"},
		{"", ""},
	}
	for _, c := range cases {
		if got := shortWorker(c.in); got != c.want {
			t.Errorf("shortWorker(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFormatElapsed(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "0m00s"},
		{65 * time.Second, "1m05s"},
		{600 * time.Second, "10m00s"},
		{3599 * time.Second, "59m59s"},
	}
	for _, c := range cases {
		if got := formatElapsed(c.d); got != c.want {
			t.Errorf("formatElapsed(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestInferPhase(t *testing.T) {
	cases := []struct {
		name   string
		status queue.JobStatus
		pid    int
		want   string
	}{
		{"preparing from status", queue.JobPreparing, 0, "preparing"},
		{"running from status", queue.JobRunning, 1234, "running"},
		{"unknown status PID>0", queue.JobPending, 42, "running"},
		{"unknown status PID=0", queue.JobPending, 0, "preparing"},
	}
	for _, c := range cases {
		state := &queue.JobState{Status: c.status}
		r := queue.StatusReport{PID: c.pid}
		if got := inferPhase(state, r); got != c.want {
			t.Errorf("%s: inferPhase = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestRenderStatusMessage_Preparing(t *testing.T) {
	state := &queue.JobState{Status: queue.JobPreparing}
	r := queue.StatusReport{WorkerID: "host/worker-0", PID: 0}
	got := renderStatusMessage(state, r, "preparing")
	want := ":gear: 準備中 · worker-0"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRenderStatusMessage_RunningNoStats(t *testing.T) {
	started := time.Now().Add(-2*time.Minute - 15*time.Second)
	state := &queue.JobState{Status: queue.JobRunning, StartedAt: started}
	r := queue.StatusReport{
		WorkerID: "host/worker-0",
		PID:      1234,
		AgentCmd: "codex",
	}
	got := renderStatusMessage(state, r, "running")
	// Allow ±1s drift on elapsed since test clock races.
	if !(got == ":hourglass_flowing_sand: 處理中 · worker-0 (codex) · 已執行 2m15s" ||
		got == ":hourglass_flowing_sand: 處理中 · worker-0 (codex) · 已執行 2m14s" ||
		got == ":hourglass_flowing_sand: 處理中 · worker-0 (codex) · 已執行 2m16s") {
		t.Errorf("unexpected output: %q", got)
	}
}

func TestRenderStatusMessage_RunningWithStats(t *testing.T) {
	state := &queue.JobState{Status: queue.JobRunning, StartedAt: time.Now()}
	r := queue.StatusReport{
		WorkerID:  "host/worker-0",
		PID:       1234,
		AgentCmd:  "claude",
		ToolCalls: 15,
		FilesRead: 8,
	}
	got := renderStatusMessage(state, r, "running")
	if !containsBoth(got, "處理中 · worker-0 (claude)", "工具呼叫 15 次 · 讀檔 8 份") {
		t.Errorf("missing expected substrings: %q", got)
	}
}

func TestRenderStatusMessage_RunningElapsedZeroWhenStartedAtUnset(t *testing.T) {
	state := &queue.JobState{Status: queue.JobRunning} // StartedAt zero
	r := queue.StatusReport{WorkerID: "host/worker-0", PID: 1234, AgentCmd: "claude"}
	got := renderStatusMessage(state, r, "running")
	if got != ":hourglass_flowing_sand: 處理中 · worker-0 (claude)" {
		t.Errorf("should omit elapsed when StartedAt is zero: %q", got)
	}
}

func TestRenderStatusMessage_RunningEmptyAgentCmd(t *testing.T) {
	state := &queue.JobState{Status: queue.JobRunning, StartedAt: time.Now()}
	r := queue.StatusReport{WorkerID: "host/worker-0", PID: 1234, AgentCmd: ""}
	got := renderStatusMessage(state, r, "running")
	if !contains(got, "處理中 · worker-0 (agent)") {
		t.Errorf("should fall back to 'agent' placeholder: %q", got)
	}
}

// helpers local to tests

func contains(s, sub string) bool { return len(sub) == 0 || indexOf(s, sub) >= 0 }
func containsBoth(s, a, b string) bool { return contains(s, a) && contains(s, b) }
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
