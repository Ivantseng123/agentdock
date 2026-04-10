package queue

import (
	"context"
	"testing"
	"time"
)

func TestProcessRegistry_RegisterAndKill(t *testing.T) {
	reg := NewProcessRegistry()
	ctx, cancel := context.WithCancel(context.Background())
	reg.Register("j1", 12345, "claude", cancel)

	agent, ok := reg.Get("j1")
	if !ok {
		t.Fatal("expected agent to be registered")
	}
	if agent.PID != 12345 {
		t.Errorf("PID = %d, want 12345", agent.PID)
	}

	// Simulate process exit after cancel
	go func() {
		<-ctx.Done()
		time.Sleep(10 * time.Millisecond)
		reg.Remove("j1")
	}()

	err := reg.Kill("j1")
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Err() == nil {
		t.Error("expected context to be cancelled after kill")
	}
}

func TestProcessRegistry_RemoveClosesDone(t *testing.T) {
	reg := NewProcessRegistry()
	_, cancel := context.WithCancel(context.Background())
	reg.Register("j1", 100, "claude", cancel)

	agent, _ := reg.Get("j1")
	done := agent.Done()

	reg.Remove("j1")

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("done channel not closed after Remove")
	}
}

func TestProcessRegistry_KillNotFound(t *testing.T) {
	reg := NewProcessRegistry()
	err := reg.Kill("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent job")
	}
}
