package queue

import (
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

func (s *MemJobStore) Put(job *Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs[job.ID] = &JobState{Job: job, Status: JobPending}
	return nil
}

func (s *MemJobStore) Get(jobID string) (*JobState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state, ok := s.jobs[jobID]
	if !ok {
		return nil, fmt.Errorf("job %q not found", jobID)
	}
	return state, nil
}

func (s *MemJobStore) GetByThread(channelID, threadTS string) (*JobState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, state := range s.jobs {
		if state.Job.ChannelID == channelID && state.Job.ThreadTS == threadTS {
			return state, nil
		}
	}
	return nil, fmt.Errorf("no job found for thread %s:%s", channelID, threadTS)
}

func (s *MemJobStore) ListPending() ([]*JobState, error) {
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

func (s *MemJobStore) UpdateStatus(jobID string, status JobStatus) error {
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
	return nil
}

func (s *MemJobStore) SetWorker(jobID, workerID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.jobs[jobID]
	if !ok {
		return fmt.Errorf("job %q not found", jobID)
	}
	state.WorkerID = workerID
	return nil
}

func (s *MemJobStore) Delete(jobID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.jobs, jobID)
	return nil
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
