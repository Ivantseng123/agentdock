package queue

// Adapter represents a pluggable execution backend.
type Adapter interface {
	Name() string
	Capabilities() []string
	Start(deps AdapterDeps) error
	Stop() error
}

// AdapterDeps contains only transport interfaces — shared by all adapter types.
type AdapterDeps struct {
	Jobs        JobQueue
	Results     ResultBus
	Status      StatusBus
	Commands    CommandBus
	Attachments AttachmentStore
}
