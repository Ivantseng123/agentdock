// Package queuetest provides an in-memory implementation of the
// shared/queue transport interfaces (JobQueue, ResultBus, StatusBus,
// CommandBus, AttachmentStore) for use as a test fixture only.
//
// It is not a production transport. Do not wire it into the transport
// switch in app/app.go or worker/worker.go. The live transport is
// Redis (see shared/queue/redis_*.go); per CLAUDE.md the inmem mode
// was retired in v2.1 because its dispatcher goroutine races with the
// priority-queue semantics.
//
// The implementation is preserved here only so tests can exercise
// queue-consuming code without spinning up a real Redis instance.
package queuetest
