package queuetest

import (
	"context"

	"github.com/Ivantseng123/agentdock/shared/queue"
)

type StatusBus struct {
	ch     chan queue.StatusReport
	closed chan struct{}
}

func NewStatusBus(capacity int) *StatusBus {
	return &StatusBus{
		ch:     make(chan queue.StatusReport, capacity),
		closed: make(chan struct{}),
	}
}

func (b *StatusBus) Report(ctx context.Context, report queue.StatusReport) error {
	select {
	case b.ch <- report:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *StatusBus) Subscribe(ctx context.Context) (<-chan queue.StatusReport, error) {
	return b.ch, nil
}

func (b *StatusBus) Close() error {
	select {
	case <-b.closed:
	default:
		close(b.closed)
	}
	return nil
}
