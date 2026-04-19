package bot

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/Ivantseng123/agentdock/shared/queue"
)

const statusUpdateDebounce = 15 * time.Second

// SlackStatusPoster is the narrow Slack surface StatusListener uses.
type SlackStatusPoster interface {
	UpdateMessageWithButton(channelID, messageTS, text, actionID, buttonText, value string) error
}

type StatusListener struct {
	status queue.StatusBus
	store  queue.JobStore
	slack  SlackStatusPoster

	mu         sync.Mutex
	lastUpdate map[string]time.Time // jobID → last Slack update
	lastPhase  map[string]string    // jobID → last rendered phase label

	clock  func() time.Time
	logger *slog.Logger
}

func NewStatusListener(status queue.StatusBus, store queue.JobStore, slack SlackStatusPoster, logger *slog.Logger) *StatusListener {
	return &StatusListener{
		status:     status,
		store:      store,
		slack:      slack,
		lastUpdate: make(map[string]time.Time),
		lastPhase:  make(map[string]string),
		clock:      time.Now,
		logger:     logger,
	}
}

func (l *StatusListener) Listen(ctx context.Context) {
	ch, err := l.status.Subscribe(ctx)
	if err != nil {
		l.logger.Error("訂閱 status bus 失敗", "phase", "失敗", "error", err)
		return
	}
	for {
		select {
		case report, ok := <-ch:
			if !ok {
				return
			}
			l.applyJobStatus(report)
			l.store.SetAgentStatus(report.JobID, report)
			l.maybeUpdateSlack(report)
		case <-ctx.Done():
			return
		}
	}
}

// applyJobStatus propagates the worker-side lifecycle state into the app's
// JobStore. Guards against empty reports from older workers, missing state
// entries, and regressions from terminal states (a stray late report must
// never downgrade a completed/failed/cancelled job back to running).
func (l *StatusListener) applyJobStatus(report queue.StatusReport) {
	if report.JobStatus == "" {
		return
	}
	state, err := l.store.Get(report.JobID)
	if err != nil || state == nil {
		return
	}
	if isTerminal(state.Status) || state.Status == report.JobStatus {
		return
	}
	if err := l.store.UpdateStatus(report.JobID, report.JobStatus); err != nil {
		l.logger.Warn("status listener: 套用 job 狀態失敗",
			"phase", "失敗", "job_id", report.JobID, "status", report.JobStatus, "error", err)
	}
}

func (l *StatusListener) maybeUpdateSlack(r queue.StatusReport) {
	if l.slack == nil {
		return // defensive; tests may wire nil
	}
	state, err := l.store.Get(r.JobID)
	if err != nil || state == nil {
		l.logger.Warn("status listener: job state missing",
			"phase", "失敗", "job_id", r.JobID, "error", err)
		return
	}

	// Terminal — let ResultListener own the final message; clean up.
	if isTerminal(state.Status) {
		l.mu.Lock()
		delete(l.lastUpdate, r.JobID)
		delete(l.lastPhase, r.JobID)
		l.mu.Unlock()
		return
	}

	if state.Job == nil || state.Job.StatusMsgTS == "" {
		return // workflow hasn't posted the first status message yet
	}

	phase := inferPhase(state, r)

	l.mu.Lock()
	prevTime, hadUpdate := l.lastUpdate[r.JobID]
	prevPhase := l.lastPhase[r.JobID]
	now := l.clock()
	phaseChanged := hadUpdate && prevPhase != phase
	debounceExpired := !hadUpdate || now.Sub(prevTime) >= statusUpdateDebounce
	if !phaseChanged && !debounceExpired {
		l.mu.Unlock()
		return
	}
	l.lastUpdate[r.JobID] = now
	l.lastPhase[r.JobID] = phase
	l.mu.Unlock()

	text := renderStatusMessage(state, r, phase)
	if text == "" {
		return
	}

	// Second terminal check right before the API call narrows race with ResultListener.
	if latest, err := l.store.Get(r.JobID); err == nil && latest != nil && isTerminal(latest.Status) {
		l.mu.Lock()
		delete(l.lastUpdate, r.JobID)
		delete(l.lastPhase, r.JobID)
		l.mu.Unlock()
		return
	}

	if err := l.slack.UpdateMessageWithButton(
		state.Job.ChannelID, state.Job.StatusMsgTS, text,
		"cancel_job", "取消", r.JobID,
	); err != nil {
		l.logger.Warn("status 訊息更新失敗", "phase", "失敗", "job_id", r.JobID, "error", err)
	}
}

// ClearJob wipes debounce state for a job. Intended for ResultListener to call
// when a terminal result is handled, protecting against leaked map entries when
// a worker crashes without emitting a final status report.
func (l *StatusListener) ClearJob(jobID string) {
	l.mu.Lock()
	delete(l.lastUpdate, jobID)
	delete(l.lastPhase, jobID)
	l.mu.Unlock()
}

func shortWorker(id string) string {
	if i := strings.LastIndex(id, "/"); i >= 0 {
		return id[i+1:]
	}
	return id
}

func formatElapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	secs := int(d.Seconds())
	return fmt.Sprintf("%dm%02ds", secs/60, secs%60)
}

func inferPhase(state *queue.JobState, r queue.StatusReport) string {
	switch state.Status {
	case queue.JobPreparing:
		return "preparing"
	case queue.JobRunning:
		return "running"
	}
	if r.PID > 0 {
		return "running"
	}
	return "preparing"
}

func isTerminal(s queue.JobStatus) bool {
	return s == queue.JobCompleted || s == queue.JobFailed || s == queue.JobCancelled
}

func renderStatusMessage(state *queue.JobState, r queue.StatusReport, phase string) string {
	worker := shortWorker(r.WorkerID)
	switch phase {
	case "preparing":
		return fmt.Sprintf(":gear: 準備中 · %s", worker)
	case "running":
		var suffix string
		if !state.StartedAt.IsZero() {
			suffix = fmt.Sprintf(" · 已執行 %s", formatElapsed(time.Since(state.StartedAt)))
		}
		agent := r.AgentCmd
		if agent == "" {
			agent = "agent"
		}
		base := fmt.Sprintf(":hourglass_flowing_sand: 處理中 · %s (%s)%s",
			worker, agent, suffix)
		if r.ToolCalls > 0 || r.FilesRead > 0 {
			base += fmt.Sprintf("\n工具呼叫 %d 次 · 讀檔 %d 份", r.ToolCalls, r.FilesRead)
		}
		return base
	}
	return ""
}
