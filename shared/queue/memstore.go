package queue

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type MemJobStore struct {
	mu   sync.RWMutex
	jobs map[string]*JobState
}

func NewMemJobStore() *MemJobStore {
	return &MemJobStore{jobs: make(map[string]*JobState)}
}

func (s *MemJobStore) Put(_ context.Context, job *Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs[job.ID] = &JobState{Job: job, Status: JobPending}
	return nil
}

func (s *MemJobStore) Get(_ context.Context, jobID string) (*JobState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state, ok := s.jobs[jobID]
	if !ok {
		return nil, fmt.Errorf("job %q not found", jobID)
	}
	return state, nil
}

func (s *MemJobStore) GetByThread(_ context.Context, channelID, threadTS string) (*JobState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, state := range s.jobs {
		if state.Job.ChannelID == channelID && state.Job.ThreadTS == threadTS {
			return state, nil
		}
	}
	return nil, fmt.Errorf("no job found for thread %s:%s", channelID, threadTS)
}

func (s *MemJobStore) ListPending(_ context.Context) ([]*JobState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []*JobState
	for _, state := range s.jobs {
		if state.Status == JobPending {
			result = append(result, state)
		}
	}
	return result, nil
}

func (s *MemJobStore) UpdateStatus(_ context.Context, jobID string, status JobStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.jobs[jobID]
	if !ok {
		return fmt.Errorf("job %q not found", jobID)
	}
	state.Status = status
	if status == JobRunning && state.StartedAt.IsZero() {
		state.StartedAt = time.Now()
		state.WaitTime = state.StartedAt.Sub(state.Job.SubmittedAt)
	}
	if status == JobCancelled && state.CancelledAt.IsZero() {
		state.CancelledAt = time.Now()
	}
	return nil
}

func (s *MemJobStore) SetWorker(_ context.Context, jobID, workerID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.jobs[jobID]
	if !ok {
		return fmt.Errorf("job %q not found", jobID)
	}
	state.WorkerID = workerID
	return nil
}

func (s *MemJobStore) SetAgentStatus(_ context.Context, jobID string, report StatusReport) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.jobs[jobID]
	if !ok {
		return nil // silently ignore — job may have been deleted
	}
	state.AgentStatus = &report
	return nil
}

func (s *MemJobStore) Delete(_ context.Context, jobID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.jobs, jobID)
	return nil
}

func (s *MemJobStore) ListAll(_ context.Context) ([]*JobState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]*JobState, 0, len(s.jobs))
	for _, state := range s.jobs {
		result = append(result, state)
	}
	return result, nil
}

func (s *MemJobStore) StartCleanup(ttl time.Duration) {
	go func() {
		ticker := time.NewTicker(ttl / 2)
		defer ticker.Stop()
		for range ticker.C {
			s.mu.Lock()
			now := time.Now()
			for id, state := range s.jobs {
				if now.Sub(state.Job.SubmittedAt) > ttl {
					delete(s.jobs, id)
				}
			}
			s.mu.Unlock()
		}
	}()
}
