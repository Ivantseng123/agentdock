package queue

import (
	"context"
	"time"
)

type JobQueue interface {
	Submit(ctx context.Context, job *Job) error
	QueuePosition(jobID string) (int, error)
	QueueDepth() int
	Receive(ctx context.Context) (<-chan *Job, error)
	Ack(ctx context.Context, jobID string) error
	Register(ctx context.Context, info WorkerInfo) error
	Unregister(ctx context.Context, workerID string) error
	ListWorkers(ctx context.Context) ([]WorkerInfo, error)
	Close() error
}

type AttachmentStore interface {
	Prepare(ctx context.Context, jobID string, payloads []AttachmentPayload) error
	Resolve(ctx context.Context, jobID string) ([]AttachmentReady, error)
	Cleanup(ctx context.Context, jobID string) error
}

type ResultBus interface {
	Publish(ctx context.Context, result *JobResult) error
	Subscribe(ctx context.Context) (<-chan *JobResult, error)
	Close() error
}

type Command struct {
	JobID  string `json:"job_id"`
	Action string `json:"action"`
}

type CommandBus interface {
	Send(ctx context.Context, cmd Command) error
	Receive(ctx context.Context) (<-chan Command, error)
	Close() error
}

type StatusReport struct {
	JobID          string    `json:"job_id"`
	WorkerID       string    `json:"worker_id"`
	WorkerNickname string    `json:"worker_nickname,omitempty"`
	PID            int       `json:"pid"`
	AgentCmd     string    `json:"agent_cmd"`
	Alive        bool      `json:"alive"`
	LastEvent    string    `json:"last_event,omitempty"`
	LastEventAt  time.Time `json:"last_event_at"`
	ToolCalls    int       `json:"tool_calls"`
	FilesRead    int       `json:"files_read"`
	OutputBytes  int       `json:"output_bytes"`
	CostUSD      float64   `json:"cost_usd,omitempty"`
	InputTokens    int       `json:"input_tokens,omitempty"`
	OutputTokens   int       `json:"output_tokens,omitempty"`
	PrepareSeconds float64   `json:"prepare_seconds,omitempty"`
	// JobStatus carries the worker-side lifecycle state so the app's
	// JobStore can reflect `running`/`preparing` across pods. Empty on
	// reports from pre-fix workers — apply-side ignores the empty case.
	JobStatus JobStatus `json:"job_status,omitempty"`
}

type StatusBus interface {
	Report(ctx context.Context, report StatusReport) error
	Subscribe(ctx context.Context) (<-chan StatusReport, error)
	Close() error
}

// JobStore persists job lifecycle state. All methods accept a context so
// blocking implementations (Redis) honour caller deadlines / cancellation
// instead of hanging indefinitely on a degraded backend.
//
// In-memory implementations are free to ignore ctx (no blocking I/O), but the
// parameter is required for API uniformity so callers do not need to know
// which backend is wired. When no upstream ctx is available, callers should
// bound the call with context.WithTimeout — see DefaultStoreOpTimeout.
type JobStore interface {
	Put(ctx context.Context, job *Job) error
	Get(ctx context.Context, jobID string) (*JobState, error)
	GetByThread(ctx context.Context, channelID, threadTS string) (*JobState, error)
	ListPending(ctx context.Context) ([]*JobState, error)
	UpdateStatus(ctx context.Context, jobID string, status JobStatus) error
	SetWorker(ctx context.Context, jobID, workerID string) error
	SetAgentStatus(ctx context.Context, jobID string, report StatusReport) error
	Delete(ctx context.Context, jobID string) error
	ListAll(ctx context.Context) ([]*JobState, error)
}
