package queue

// Bundle holds the five transport interfaces. Both inmem and redis
// constructors return this type, so all downstream code is transport-agnostic.
type Bundle struct {
	Queue       JobQueue
	Results     ResultBus
	Status      StatusBus
	Commands    CommandBus
	Attachments AttachmentStore
}

type closer interface {
	Close() error
}

func (b *Bundle) Close() error {
	for _, c := range []any{b.Queue, b.Results, b.Commands, b.Status} {
		if cl, ok := c.(closer); ok {
			cl.Close()
		}
	}
	return nil
}
