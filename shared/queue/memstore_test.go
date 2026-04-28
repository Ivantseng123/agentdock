package queue

import (
	"context"
	"testing"
	"time"
)

func TestMemJobStore_PutAndGet(t *testing.T) {
	ctx := context.Background()
	s := NewMemJobStore()
	job := &Job{ID: "j1", ChannelID: "C1", ThreadTS: "T1", SubmittedAt: time.Now()}
	if err := s.Put(ctx, job); err != nil {
		t.Fatal(err)
	}
	state, err := s.Get(ctx, "j1")
	if err != nil {
		t.Fatal(err)
	}
	if state.Job.ID != "j1" {
		t.Errorf("ID = %q, want j1", state.Job.ID)
	}
	if state.Status != JobPending {
		t.Errorf("status = %q, want pending", state.Status)
	}
}

func TestMemJobStore_GetByThread(t *testing.T) {
	ctx := context.Background()
	s := NewMemJobStore()
	s.Put(ctx, &Job{ID: "j1", ChannelID: "C1", ThreadTS: "T1"})
	s.Put(ctx, &Job{ID: "j2", ChannelID: "C2", ThreadTS: "T2"})
	state, err := s.GetByThread(ctx, "C1", "T1")
	if err != nil {
		t.Fatal(err)
	}
	if state.Job.ID != "j1" {
		t.Errorf("got %q, want j1", state.Job.ID)
	}
}

func TestMemJobStore_UpdateStatus(t *testing.T) {
	ctx := context.Background()
	s := NewMemJobStore()
	s.Put(ctx, &Job{ID: "j1"})
	s.UpdateStatus(ctx, "j1", JobRunning)
	state, _ := s.Get(ctx, "j1")
	if state.Status != JobRunning {
		t.Errorf("status = %q, want running", state.Status)
	}
}

func TestMemJobStore_Delete(t *testing.T) {
	ctx := context.Background()
	s := NewMemJobStore()
	s.Put(ctx, &Job{ID: "j1"})
	s.Delete(ctx, "j1")
	_, err := s.Get(ctx, "j1")
	if err == nil {
		t.Error("expected error after delete")
	}
}

func TestMemJobStore_ListPending(t *testing.T) {
	ctx := context.Background()
	s := NewMemJobStore()
	s.Put(ctx, &Job{ID: "j1"})
	s.Put(ctx, &Job{ID: "j2"})
	s.UpdateStatus(ctx, "j2", JobRunning)
	pending, _ := s.ListPending(ctx)
	if len(pending) != 1 {
		t.Errorf("pending count = %d, want 1", len(pending))
	}
	if pending[0].Job.ID != "j1" {
		t.Errorf("pending job = %q, want j1", pending[0].Job.ID)
	}
}

func TestMemJobStore_GetNotFound(t *testing.T) {
	ctx := context.Background()
	s := NewMemJobStore()
	_, err := s.Get(ctx, "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent job")
	}
}

func TestMemJobStore_UpdateStatus_StampsCancelledAt(t *testing.T) {
	ctx := context.Background()
	s := NewMemJobStore()
	s.Put(ctx, &Job{ID: "j1"})

	before := time.Now()
	if err := s.UpdateStatus(ctx, "j1", JobCancelled); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	state, _ := s.Get(ctx, "j1")
	if state.CancelledAt.IsZero() {
		t.Fatal("CancelledAt should be stamped")
	}
	if state.CancelledAt.Before(before) {
		t.Errorf("CancelledAt (%v) earlier than call start (%v)", state.CancelledAt, before)
	}
}

func TestMemJobStore_UpdateStatus_CancelledAtIdempotent(t *testing.T) {
	ctx := context.Background()
	s := NewMemJobStore()
	s.Put(ctx, &Job{ID: "j1"})

	s.UpdateStatus(ctx, "j1", JobCancelled)
	state, _ := s.Get(ctx, "j1")
	first := state.CancelledAt

	time.Sleep(5 * time.Millisecond)
	s.UpdateStatus(ctx, "j1", JobCancelled)
	state, _ = s.Get(ctx, "j1")

	if !state.CancelledAt.Equal(first) {
		t.Errorf("second UpdateStatus should not re-stamp; first=%v second=%v", first, state.CancelledAt)
	}
}
