package bot

import (
	"context"
	"log/slog"

	"agentdock/internal/queue"
)

type StatusListener struct {
	status queue.StatusBus
	store  queue.JobStore
}

func NewStatusListener(status queue.StatusBus, store queue.JobStore) *StatusListener {
	return &StatusListener{status: status, store: store}
}

func (l *StatusListener) Listen(ctx context.Context) {
	ch, err := l.status.Subscribe(ctx)
	if err != nil {
		slog.Error("failed to subscribe to status bus", "error", err)
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
