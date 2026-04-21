package queuetest

import (
	"context"
	"sync"

	"github.com/Ivantseng123/agentdock/shared/queue"
)

type AttachmentStore struct {
	mu    sync.Mutex
	ready map[string]chan []queue.AttachmentReady
}

func NewAttachmentStore() *AttachmentStore {
	return &AttachmentStore{
		ready: make(map[string]chan []queue.AttachmentReady),
	}
}

func (s *AttachmentStore) Prepare(ctx context.Context, jobID string, payloads []queue.AttachmentPayload) error {
	s.mu.Lock()
	ch, ok := s.ready[jobID]
	if !ok {
		ch = make(chan []queue.AttachmentReady, 1)
		s.ready[jobID] = ch
	}
	s.mu.Unlock()
	result := make([]queue.AttachmentReady, len(payloads))
	for i, p := range payloads {
		result[i] = queue.AttachmentReady{Filename: p.Filename, Data: p.Data, MimeType: p.MimeType}
	}
	ch <- result
	return nil
}

func (s *AttachmentStore) Resolve(ctx context.Context, jobID string) ([]queue.AttachmentReady, error) {
	s.mu.Lock()
	ch, ok := s.ready[jobID]
	if !ok {
		ch = make(chan []queue.AttachmentReady, 1)
		s.ready[jobID] = ch
	}
	s.mu.Unlock()
	select {
	case result := <-ch:
		return result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *AttachmentStore) Cleanup(ctx context.Context, jobID string) error {
	s.mu.Lock()
	delete(s.ready, jobID)
	s.mu.Unlock()
	return nil
}
