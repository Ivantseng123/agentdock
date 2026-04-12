package queue

import (
	"context"
	"testing"
	"time"
)

func TestRedisCommandBus_SendAndReceive(t *testing.T) {
	client := testRedisClient(t)

	bus := NewRedisCommandBus(client)
	defer bus.Close()

	ctx := context.Background()

	ch, err := bus.Receive(ctx)
	if err != nil {
		t.Fatalf("Receive failed: %v", err)
	}

	// Wait for subscription to be established.
	time.Sleep(100 * time.Millisecond)

	cmd := Command{
		JobID:  "job-456",
		Action: "kill",
	}

	if err := bus.Send(ctx, cmd); err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	select {
	case got := <-ch:
		if got.JobID != cmd.JobID {
			t.Errorf("JobID = %q, want %q", got.JobID, cmd.JobID)
		}
		if got.Action != cmd.Action {
			t.Errorf("Action = %q, want %q", got.Action, cmd.Action)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for command")
	}
}
