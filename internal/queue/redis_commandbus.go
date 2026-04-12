package queue

import (
	"context"
	"encoding/json"

	"github.com/redis/go-redis/v9"
)

const commandChannel = keyPrefix + "jobs:commands"

// RedisCommandBus implements CommandBus using Redis Pub/Sub.
type RedisCommandBus struct {
	client *redis.Client
	pubsub *redis.PubSub
}

func NewRedisCommandBus(client *redis.Client) *RedisCommandBus {
	return &RedisCommandBus{client: client}
}

func (b *RedisCommandBus) Send(ctx context.Context, cmd Command) error {
	data, err := json.Marshal(cmd)
	if err != nil {
		return err
	}
	return b.client.Publish(ctx, commandChannel, data).Err()
}

func (b *RedisCommandBus) Receive(ctx context.Context) (<-chan Command, error) {
	b.pubsub = b.client.Subscribe(ctx, commandChannel)

	// Wait for confirmation that subscription is active.
	if _, err := b.pubsub.Receive(ctx); err != nil {
		b.pubsub.Close()
		return nil, err
	}

	ch := make(chan Command, 64)
	go func() {
		defer close(ch)
		for msg := range b.pubsub.Channel() {
			var cmd Command
			if err := json.Unmarshal([]byte(msg.Payload), &cmd); err != nil {
				continue
			}
			// Best-effort: drop if consumer is slow.
			select {
			case ch <- cmd:
			default:
			}
		}
	}()

	return ch, nil
}

func (b *RedisCommandBus) Close() error {
	if b.pubsub != nil {
		return b.pubsub.Close()
	}
	return nil
}
