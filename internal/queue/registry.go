package queue

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type ProcessRegistry struct {
	mu        sync.RWMutex
	processes map[string]*RunningAgent
}

type RunningAgent struct {
	JobID     string
	PID       int
	Command   string
	StartedAt time.Time
	cancel    context.CancelFunc
	done      chan struct{}
}

func (a *RunningAgent) Done() <-chan struct{} {
	return a.done
}

func NewProcessRegistry() *ProcessRegistry {
	return &ProcessRegistry{
		processes: make(map[string]*RunningAgent),
	}
}

func (r *ProcessRegistry) Register(jobID string, pid int, command string, cancel context.CancelFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.processes[jobID] = &RunningAgent{
		JobID:     jobID,
		PID:       pid,
		Command:   command,
		StartedAt: time.Now(),
		cancel:    cancel,
		done:      make(chan struct{}),
	}
}

func (r *ProcessRegistry) Remove(jobID string) {
	r.mu.Lock()
	agent, ok := r.processes[jobID]
	if ok {
		delete(r.processes, jobID)
		close(agent.done)
	}
	r.mu.Unlock()
}

func (r *ProcessRegistry) Kill(jobID string) error {
	r.mu.RLock()
	agent, ok := r.processes[jobID]
	r.mu.RUnlock()
	if !ok {
		return fmt.Errorf("no running agent for job %q", jobID)
	}
	agent.cancel()
	select {
	case <-agent.done:
		return nil
	case <-time.After(15 * time.Second):
		return fmt.Errorf("kill timeout for job %q", jobID)
	}
}

func (r *ProcessRegistry) Get(jobID string) (*RunningAgent, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	agent, ok := r.processes[jobID]
	return agent, ok
}
