package queuetest

import "github.com/Ivantseng123/agentdock/shared/queue"

func NewBundle(capacity int, workerCount int, store queue.JobStore) *queue.Bundle {
	return &queue.Bundle{
		Queue:       NewJobQueue(capacity, store),
		Results:     NewResultBus(capacity),
		Attachments: NewAttachmentStore(),
		Commands:    NewCommandBus(10),
		Status:      NewStatusBus(workerCount * 2),
	}
}
