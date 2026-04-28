package bot

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/Ivantseng123/agentdock/shared/queue"
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
	want := ":toolbox: worker-0 正在暖機..."
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRenderStatusMessage_PreparingWithNickname(t *testing.T) {
	state := &queue.JobState{Status: queue.JobPreparing}
	r := queue.StatusReport{WorkerID: "host/worker-0", WorkerNickname: "小明", PID: 0}
	got := renderStatusMessage(state, r, "preparing")
	want := ":toolbox: 小明 正在暖機..."
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
	want14 := ":fire: worker-0 開工啦！(codex) · 奮鬥 2m14s"
	want15 := ":fire: worker-0 開工啦！(codex) · 奮鬥 2m15s"
	want16 := ":fire: worker-0 開工啦！(codex) · 奮鬥 2m16s"
	if got != want14 && got != want15 && got != want16 {
		t.Errorf("unexpected output: %q", got)
	}
}

func TestRenderStatusMessage_RunningWithStats(t *testing.T) {
	state := &queue.JobState{Status: queue.JobRunning, StartedAt: time.Now()}
	r := queue.StatusReport{
		WorkerID:       "host/worker-0",
		WorkerNickname: "小明",
		PID:            1234,
		AgentCmd:       "claude",
		ToolCalls:      15,
		FilesRead:      8,
	}
	got := renderStatusMessage(state, r, "running")
	if !containsBoth(got, ":fire: 小明 開工啦！(claude)", "小明 已經敲了 15 次工具、翻了 8 份檔") {
		t.Errorf("missing expected substrings: %q", got)
	}
}

func TestRenderStatusMessage_RunningElapsedZeroWhenStartedAtUnset(t *testing.T) {
	state := &queue.JobState{Status: queue.JobRunning} // StartedAt zero
	r := queue.StatusReport{WorkerID: "host/worker-0", PID: 1234, AgentCmd: "claude"}
	got := renderStatusMessage(state, r, "running")
	if got != ":fire: worker-0 開工啦！(claude)" {
		t.Errorf("should omit elapsed when StartedAt is zero: %q", got)
	}
}

func TestRenderStatusMessage_RunningEmptyAgentCmd(t *testing.T) {
	state := &queue.JobState{Status: queue.JobRunning, StartedAt: time.Now()}
	r := queue.StatusReport{WorkerID: "host/worker-0", PID: 1234, AgentCmd: ""}
	got := renderStatusMessage(state, r, "running")
	if !contains(got, ":fire: worker-0 開工啦！(agent)") {
		t.Errorf("should fall back to 'agent' placeholder: %q", got)
	}
}

func TestRenderStatusMessage_EscapesNickname(t *testing.T) {
	state := &queue.JobState{Status: queue.JobPreparing}
	r := queue.StatusReport{WorkerID: "host/worker-0", WorkerNickname: "<@U123>"}
	got := renderStatusMessage(state, r, "preparing")
	if !contains(got, "&lt;@U123&gt;") {
		t.Errorf("nickname should be escaped: %q", got)
	}
	if contains(got, "<@U123>") {
		t.Errorf("raw mention syntax leaked: %q", got)
	}
}

func TestRenderStatusMessage_EscapesAgentCmd(t *testing.T) {
	state := &queue.JobState{Status: queue.JobRunning, StartedAt: time.Now()}
	r := queue.StatusReport{WorkerID: "host/worker-0", PID: 1234, AgentCmd: "foo&bar"}
	got := renderStatusMessage(state, r, "running")
	if !contains(got, "(foo&amp;bar)") {
		t.Errorf("agent_cmd should be escaped: %q", got)
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

// ---- Task 4: maybeUpdateSlack tests ----

type stubSlackStatusPoster struct {
	calls []struct {
		ChannelID  string
		MessageTS  string
		Text       string
		ActionID   string
		ButtonText string
		Value      string
	}
	err error
}

func (s *stubSlackStatusPoster) UpdateMessageWithButton(channelID, messageTS, text, actionID, buttonText, value string) error {
	s.calls = append(s.calls, struct {
		ChannelID, MessageTS, Text, ActionID, ButtonText, Value string
	}{channelID, messageTS, text, actionID, buttonText, value})
	return s.err
}

func newTestListener(store queue.JobStore, slack SlackStatusPoster, now time.Time) *StatusListener {
	l := NewStatusListener(nil, store, slack, slogDiscardLogger())
	l.clock = func() time.Time { return now }
	return l
}

func slogDiscardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestMaybeUpdateSlack_PreparingPhase(t *testing.T) {
	ctx := context.Background()
	store := queue.NewMemJobStore()
	store.Put(ctx, &queue.Job{ID: "j1", ChannelID: "C1", StatusMsgTS: "S1"})
	store.UpdateStatus(ctx, "j1", queue.JobPreparing)

	slack := &stubSlackStatusPoster{}
	l := newTestListener(store, slack, time.Now())

	l.maybeUpdateSlack(ctx, queue.StatusReport{
		JobID: "j1", WorkerID: "host/worker-0", PID: 0, Alive: true,
	})

	if len(slack.calls) != 1 {
		t.Fatalf("expected 1 Slack call, got %d", len(slack.calls))
	}
	c := slack.calls[0]
	if c.ChannelID != "C1" || c.MessageTS != "S1" {
		t.Errorf("wrong target: %+v", c)
	}
	if c.ActionID != "cancel_job" || c.ButtonText != "取消" || c.Value != "j1" {
		t.Errorf("wrong button: %+v", c)
	}
	if !contains(c.Text, "正在暖機") || !contains(c.Text, "worker-0") {
		t.Errorf("text missing expected markers: %q", c.Text)
	}
}

func TestMaybeUpdateSlack_RunningWithToolCalls(t *testing.T) {
	ctx := context.Background()
	store := queue.NewMemJobStore()
	store.Put(ctx, &queue.Job{ID: "j1", ChannelID: "C1", StatusMsgTS: "S1"})
	store.UpdateStatus(ctx, "j1", queue.JobRunning)

	slack := &stubSlackStatusPoster{}
	l := newTestListener(store, slack, time.Now())

	l.maybeUpdateSlack(ctx, queue.StatusReport{
		JobID: "j1", WorkerID: "host/worker-0", PID: 1234,
		AgentCmd: "claude", ToolCalls: 15, FilesRead: 8,
	})

	if len(slack.calls) != 1 {
		t.Fatalf("expected 1 Slack call")
	}
	if !containsBoth(slack.calls[0].Text, ":fire: worker-0 開工啦！(claude)", "已經敲了 15 次工具") {
		t.Errorf("missing expected substrings: %q", slack.calls[0].Text)
	}
}

func TestMaybeUpdateSlack_RunningNoToolCalls(t *testing.T) {
	ctx := context.Background()
	store := queue.NewMemJobStore()
	store.Put(ctx, &queue.Job{ID: "j1", ChannelID: "C1", StatusMsgTS: "S1"})
	store.UpdateStatus(ctx, "j1", queue.JobRunning)

	slack := &stubSlackStatusPoster{}
	l := newTestListener(store, slack, time.Now())

	l.maybeUpdateSlack(ctx, queue.StatusReport{
		JobID: "j1", WorkerID: "host/worker-0", PID: 1234, AgentCmd: "codex",
	})

	if len(slack.calls) != 1 {
		t.Fatalf("expected 1 Slack call")
	}
	if contains(slack.calls[0].Text, "已經敲了") {
		t.Errorf("should NOT include tool-call line for codex: %q", slack.calls[0].Text)
	}
}

func TestMaybeUpdateSlack_DebounceSkips(t *testing.T) {
	ctx := context.Background()
	store := queue.NewMemJobStore()
	store.Put(ctx, &queue.Job{ID: "j1", ChannelID: "C1", StatusMsgTS: "S1"})
	store.UpdateStatus(ctx, "j1", queue.JobRunning)

	slack := &stubSlackStatusPoster{}
	t0 := time.Now()
	l := newTestListener(store, slack, t0)

	l.maybeUpdateSlack(ctx, queue.StatusReport{JobID: "j1", WorkerID: "w", PID: 1, AgentCmd: "claude"})
	// 5 seconds later — still within 15s debounce, same phase
	l.clock = func() time.Time { return t0.Add(5 * time.Second) }
	l.maybeUpdateSlack(ctx, queue.StatusReport{JobID: "j1", WorkerID: "w", PID: 1, AgentCmd: "claude"})

	if len(slack.calls) != 1 {
		t.Errorf("debounce failed: got %d calls", len(slack.calls))
	}
}

func TestMaybeUpdateSlack_PhaseChangeForcesUpdate(t *testing.T) {
	ctx := context.Background()
	store := queue.NewMemJobStore()
	store.Put(ctx, &queue.Job{ID: "j1", ChannelID: "C1", StatusMsgTS: "S1"})
	store.UpdateStatus(ctx, "j1", queue.JobPreparing)

	slack := &stubSlackStatusPoster{}
	t0 := time.Now()
	l := newTestListener(store, slack, t0)

	// First update in preparing.
	l.maybeUpdateSlack(ctx, queue.StatusReport{JobID: "j1", WorkerID: "w", PID: 0})

	// 2 seconds later — within debounce but phase changed to running.
	store.UpdateStatus(ctx, "j1", queue.JobRunning)
	l.clock = func() time.Time { return t0.Add(2 * time.Second) }
	l.maybeUpdateSlack(ctx, queue.StatusReport{JobID: "j1", WorkerID: "w", PID: 1234, AgentCmd: "claude"})

	if len(slack.calls) != 2 {
		t.Errorf("phase change should force update; got %d calls", len(slack.calls))
	}
}

func TestMaybeUpdateSlack_TerminalSkips(t *testing.T) {
	ctx := context.Background()
	store := queue.NewMemJobStore()
	store.Put(ctx, &queue.Job{ID: "j1", ChannelID: "C1", StatusMsgTS: "S1"})
	store.UpdateStatus(ctx, "j1", queue.JobCompleted)

	slack := &stubSlackStatusPoster{}
	l := newTestListener(store, slack, time.Now())

	// Pre-populate lastUpdate to confirm it gets cleared.
	l.lastUpdate["j1"] = time.Now()
	l.lastPhase["j1"] = "running"

	l.maybeUpdateSlack(ctx, queue.StatusReport{JobID: "j1", WorkerID: "w", PID: 1234})

	if len(slack.calls) != 0 {
		t.Errorf("terminal should skip; got %d calls", len(slack.calls))
	}
	if _, ok := l.lastUpdate["j1"]; ok {
		t.Error("lastUpdate should be cleared for terminal jobs")
	}
}

func TestMaybeUpdateSlack_StoreMissing(t *testing.T) {
	ctx := context.Background()
	store := queue.NewMemJobStore()
	// no Put — store.Get returns error
	slack := &stubSlackStatusPoster{}
	l := newTestListener(store, slack, time.Now())

	l.maybeUpdateSlack(ctx, queue.StatusReport{JobID: "missing"})

	if len(slack.calls) != 0 {
		t.Errorf("missing state should skip; got %d calls", len(slack.calls))
	}
}

func TestMaybeUpdateSlack_StatusMsgTSEmpty(t *testing.T) {
	ctx := context.Background()
	store := queue.NewMemJobStore()
	store.Put(ctx, &queue.Job{ID: "j1", ChannelID: "C1"}) // no StatusMsgTS
	store.UpdateStatus(ctx, "j1", queue.JobPreparing)

	slack := &stubSlackStatusPoster{}
	l := newTestListener(store, slack, time.Now())

	l.maybeUpdateSlack(ctx, queue.StatusReport{JobID: "j1", WorkerID: "w", PID: 0})

	if len(slack.calls) != 0 {
		t.Errorf("empty StatusMsgTS should skip; got %d calls", len(slack.calls))
	}
}

func TestMaybeUpdateSlack_SlackErrorNonFatal(t *testing.T) {
	ctx := context.Background()
	store := queue.NewMemJobStore()
	store.Put(ctx, &queue.Job{ID: "j1", ChannelID: "C1", StatusMsgTS: "S1"})
	store.UpdateStatus(ctx, "j1", queue.JobRunning)

	slack := &stubSlackStatusPoster{err: fmt.Errorf("slack boom")}
	l := newTestListener(store, slack, time.Now())

	// Should not panic.
	l.maybeUpdateSlack(ctx, queue.StatusReport{JobID: "j1", WorkerID: "w", PID: 1, AgentCmd: "claude"})

	if len(slack.calls) != 1 {
		t.Errorf("expected one attempt; got %d", len(slack.calls))
	}
}

// ---- applyJobStatus: cross-pod lifecycle propagation via StatusBus ----

func TestApplyJobStatus_AppliesWhenPending(t *testing.T) {
	ctx := context.Background()
	store := queue.NewMemJobStore()
	store.Put(ctx, &queue.Job{ID: "j1"}) // Put initialises as JobPending
	l := newTestListener(store, &stubSlackStatusPoster{}, time.Now())

	l.applyJobStatus(ctx, queue.StatusReport{JobID: "j1", JobStatus: queue.JobRunning})

	state, _ := store.Get(ctx, "j1")
	if state.Status != queue.JobRunning {
		t.Errorf("status = %q, want JobRunning", state.Status)
	}
}

func TestApplyJobStatus_SkipsEmptyStatus(t *testing.T) {
	ctx := context.Background()
	store := queue.NewMemJobStore()
	store.Put(ctx, &queue.Job{ID: "j1"})
	l := newTestListener(store, &stubSlackStatusPoster{}, time.Now())

	l.applyJobStatus(ctx, queue.StatusReport{JobID: "j1" /* JobStatus zero */})

	state, _ := store.Get(ctx, "j1")
	if state.Status != queue.JobPending {
		t.Errorf("status = %q, want JobPending (unchanged)", state.Status)
	}
}

func TestApplyJobStatus_DoesNotRegressFromTerminal(t *testing.T) {
	ctx := context.Background()
	cases := []queue.JobStatus{queue.JobCompleted, queue.JobFailed, queue.JobCancelled}
	for _, terminal := range cases {
		store := queue.NewMemJobStore()
		store.Put(ctx, &queue.Job{ID: "j1"})
		store.UpdateStatus(ctx, "j1", terminal)

		l := newTestListener(store, &stubSlackStatusPoster{}, time.Now())

		l.applyJobStatus(ctx, queue.StatusReport{JobID: "j1", JobStatus: queue.JobRunning})

		state, _ := store.Get(ctx, "j1")
		if state.Status != terminal {
			t.Errorf("status = %q, want %q (no regression)", state.Status, terminal)
		}
	}
}

func TestApplyJobStatus_SkipsWhenStateMissing(t *testing.T) {
	ctx := context.Background()
	store := queue.NewMemJobStore()
	l := newTestListener(store, &stubSlackStatusPoster{}, time.Now())

	// Should be a no-op, not panic, and not create anything.
	l.applyJobStatus(ctx, queue.StatusReport{JobID: "missing", JobStatus: queue.JobRunning})

	if _, err := store.Get(ctx, "missing"); err == nil {
		t.Error("no entry should have been created")
	}
}

func TestSlackEscape(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"plain", "小明", "小明"},
		{"lt only", "<heart>", "&lt;heart&gt;"},
		{"amp only", "A & B", "A &amp; B"},
		{"user mention neutralised", "<@U12345>", "&lt;@U12345&gt;"},
		{"channel broadcast neutralised", "<!channel>", "&lt;!channel&gt;"},
		{"amp before lt — no double-escape", "&<", "&amp;&lt;"},
		{"already-encoded stays idempotent-ish",
			"&amp;", "&amp;amp;"}, // we DO double-escape existing entities — that's correct for user input
		{"empty", "", ""},
	}
	for _, c := range cases {
		if got := slackEscape(c.in); got != c.want {
			t.Errorf("%s: slackEscape(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

func TestFormatWorkerLabel(t *testing.T) {
	cases := []struct {
		name, workerID, nickname, want string
	}{
		{"nickname wins", "host/worker-0", "小明", "小明"},
		{"empty nickname falls back to shortWorker", "host/worker-2", "", "worker-2"},
		{"empty nickname empty workerID falls back to empty", "", "", ""},
		{"nickname beats even multi-slash workerID", "k8s/pod/worker-5", "Alice", "Alice"},
	}
	for _, c := range cases {
		if got := formatWorkerLabel(c.workerID, c.nickname); got != c.want {
			t.Errorf("%s: formatWorkerLabel(%q, %q) = %q, want %q", c.name, c.workerID, c.nickname, got, c.want)
		}
	}
}
