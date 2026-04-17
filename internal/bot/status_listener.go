package bot

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"agentdock/internal/queue"
)

type StatusListener struct {
	status queue.StatusBus
	store  queue.JobStore
	logger *slog.Logger
}

func NewStatusListener(status queue.StatusBus, store queue.JobStore, logger *slog.Logger) *StatusListener {
	return &StatusListener{status: status, store: store, logger: logger}
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
			l.store.SetAgentStatus(report.JobID, report)
		case <-ctx.Done():
			return
		}
	}
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
