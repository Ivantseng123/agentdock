package queue

func NewInMemBundle(capacity int, workerCount int, store JobStore) *Bundle {
	return &Bundle{
		Queue:       NewInMemJobQueue(capacity, store),
		Results:     NewInMemResultBus(capacity),
		Attachments: NewInMemAttachmentStore(),
		Commands:    NewInMemCommandBus(10),
		Status:      NewInMemStatusBus(workerCount * 2),
	}
}
