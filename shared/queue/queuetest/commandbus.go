package queuetest

import (
	"context"

	"github.com/Ivantseng123/agentdock/shared/queue"
)

type CommandBus struct {
	ch     chan queue.Command
	closed chan struct{}
}

func NewCommandBus(capacity int) *CommandBus {
	return &CommandBus{
		ch:     make(chan queue.Command, capacity),
		closed: make(chan struct{}),
	}
}

func (b *CommandBus) Send(ctx context.Context, cmd queue.Command) error {
	select {
	case b.ch <- cmd:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *CommandBus) Receive(ctx context.Context) (<-chan queue.Command, error) {
	return b.ch, nil
}

func (b *CommandBus) Close() error {
	select {
	case <-b.closed:
	default:
		close(b.closed)
	}
	return nil
}
