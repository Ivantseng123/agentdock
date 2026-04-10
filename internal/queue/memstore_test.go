package queue

import (
	"testing"
	"time"
)

func TestMemJobStore_PutAndGet(t *testing.T) {
	s := NewMemJobStore()
	job := &Job{ID: "j1", ChannelID: "C1", ThreadTS: "T1", SubmittedAt: time.Now()}
	if err := s.Put(job); err != nil {
		t.Fatal(err)
	}
	state, err := s.Get("j1")
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
	s := NewMemJobStore()
	s.Put(&Job{ID: "j1", ChannelID: "C1", ThreadTS: "T1"})
	s.Put(&Job{ID: "j2", ChannelID: "C2", ThreadTS: "T2"})
	state, err := s.GetByThread("C1", "T1")
	if err != nil {
		t.Fatal(err)
	}
	if state.Job.ID != "j1" {
		t.Errorf("got %q, want j1", state.Job.ID)
	}
}

func TestMemJobStore_UpdateStatus(t *testing.T) {
	s := NewMemJobStore()
	s.Put(&Job{ID: "j1"})
	s.UpdateStatus("j1", JobRunning)
	state, _ := s.Get("j1")
	if state.Status != JobRunning {
		t.Errorf("status = %q, want running", state.Status)
	}
}

func TestMemJobStore_Delete(t *testing.T) {
	s := NewMemJobStore()
	s.Put(&Job{ID: "j1"})
	s.Delete("j1")
	_, err := s.Get("j1")
	if err == nil {
		t.Error("expected error after delete")
	}
}

func TestMemJobStore_ListPending(t *testing.T) {
	s := NewMemJobStore()
	s.Put(&Job{ID: "j1"})
	s.Put(&Job{ID: "j2"})
	s.UpdateStatus("j2", JobRunning)
	pending, _ := s.ListPending()
	if len(pending) != 1 {
		t.Errorf("pending count = %d, want 1", len(pending))
	}
	if pending[0].Job.ID != "j1" {
		t.Errorf("pending job = %q, want j1", pending[0].Job.ID)
	}
}

func TestMemJobStore_GetNotFound(t *testing.T) {
	s := NewMemJobStore()
	_, err := s.Get("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent job")
	}
}
