package queue

import "github.com/redis/go-redis/v9"

// NewRedisBundle creates a Bundle backed by Redis. Options are forwarded to the
// underlying RedisJobQueue so callers can wire a component-scoped logger for
// receive-loop diagnostics without reaching past the bundle.
func NewRedisBundle(rdb *redis.Client, store JobStore, taskType string, opts ...RedisJobQueueOption) *Bundle {
	return &Bundle{
		Queue:       NewRedisJobQueue(rdb, store, taskType, opts...),
		Results:     NewRedisResultBus(rdb),
		Attachments: NewRedisAttachmentStore(rdb),
		Commands:    NewRedisCommandBus(rdb),
		Status:      NewRedisStatusBus(rdb),
	}
}
