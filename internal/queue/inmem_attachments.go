package queue

import (
	"context"
	"sync"
)

type InMemAttachmentStore struct {
	mu    sync.Mutex
	ready map[string]chan []AttachmentReady
}

func NewInMemAttachmentStore() *InMemAttachmentStore {
	return &InMemAttachmentStore{
		ready: make(map[string]chan []AttachmentReady),
	}
}

func (s *InMemAttachmentStore) Prepare(ctx context.Context, jobID string, payloads []AttachmentPayload) error {
	s.mu.Lock()
	ch, ok := s.ready[jobID]
	if !ok {
		ch = make(chan []AttachmentReady, 1)
		s.ready[jobID] = ch
	}
	s.mu.Unlock()
	result := make([]AttachmentReady, len(payloads))
	for i, p := range payloads {
		result[i] = AttachmentReady{Filename: p.Filename, Data: p.Data, MimeType: p.MimeType}
	}
	ch <- result
	return nil
}

func (s *InMemAttachmentStore) Resolve(ctx context.Context, jobID string) ([]AttachmentReady, error) {
	s.mu.Lock()
	ch, ok := s.ready[jobID]
	if !ok {
		ch = make(chan []AttachmentReady, 1)
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

func (s *InMemAttachmentStore) Cleanup(ctx context.Context, jobID string) error {
	s.mu.Lock()
	delete(s.ready, jobID)
	s.mu.Unlock()
	return nil
}
