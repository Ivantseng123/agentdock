package queuetest

import (
	"context"

	"github.com/Ivantseng123/agentdock/shared/queue"
)

type ResultBus struct {
	ch     chan *queue.JobResult
	closed chan struct{}
}

func NewResultBus(capacity int) *ResultBus {
	return &ResultBus{
		ch:     make(chan *queue.JobResult, capacity),
		closed: make(chan struct{}),
	}
}

func (b *ResultBus) Publish(ctx context.Context, result *queue.JobResult) error {
	select {
	case b.ch <- result:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *ResultBus) Subscribe(ctx context.Context) (<-chan *queue.JobResult, error) {
	return b.ch, nil
}

func (b *ResultBus) Close() error {
	select {
	case <-b.closed:
	default:
		close(b.closed)
	}
	return nil
}
