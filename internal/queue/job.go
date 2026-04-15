package queue

import (
	"fmt"
	"time"
)

type JobStatus string

const (
	JobPending   JobStatus = "pending"
	JobPreparing JobStatus = "preparing"
	JobRunning   JobStatus = "running"
	JobCompleted JobStatus = "completed"
	JobFailed    JobStatus = "failed"
)

type SkillPayload struct {
	Files map[string][]byte `json:"files"`
}

type Job struct {
	ID          string                    `json:"id"`
	Priority    int                       `json:"priority"`
	Seq         uint64                    `json:"seq"`
	ChannelID   string                    `json:"channel_id"`
	ThreadTS    string                    `json:"thread_ts"`
	UserID      string                    `json:"user_id"`
	Repo        string                    `json:"repo"`
	Branch      string                    `json:"branch"`
	CloneURL    string                    `json:"clone_url"`
	Prompt      string                    `json:"prompt"`
	Skills      map[string]*SkillPayload  `json:"skills"`
	RequestID   string            `json:"request_id"`
	Attachments []AttachmentMeta  `json:"attachments"`
	StatusMsgTS string            `json:"status_msg_ts,omitempty"`
	TaskType     string            `json:"task_type,omitempty"`
	RetryCount   int               `json:"retry_count,omitempty"`
	RetryOfJobID string            `json:"retry_of_job_id,omitempty"`
	SubmittedAt  time.Time         `json:"submitted_at"`
}

type AttachmentMeta struct {
	SlackFileID string `json:"slack_file_id"`
	Filename    string `json:"filename"`
	Size        int64  `json:"size"`
	MimeType    string `json:"mime_type"`
	DownloadURL string `json:"download_url"`
}

type JobResult struct {
	JobID        string    `json:"job_id"`
	Status       string    `json:"status"`
	Title        string    `json:"title"`
	Body         string    `json:"body"`
	Labels       []string  `json:"labels"`
	Confidence   string    `json:"confidence"`
	FilesFound   int       `json:"files_found"`
	Questions    int       `json:"open_questions"`
	RawOutput    string    `json:"raw_output"`
	Error        string    `json:"error"`
	StartedAt    time.Time `json:"started_at"`
	FinishedAt   time.Time `json:"finished_at"`
	CostUSD      float64   `json:"cost_usd,omitempty"`
	InputTokens  int       `json:"input_tokens,omitempty"`
	OutputTokens int       `json:"output_tokens,omitempty"`
	RepoPath       string    `json:"-"` // local only, not serialized over Redis
	PrepareSeconds float64   `json:"-"` // local only, not serialized over Redis
}

type AttachmentReady struct {
	Filename string `json:"filename"`
	Data     []byte `json:"data"`
	MimeType string `json:"mime_type"`
}

type AttachmentPayload struct {
	Filename string
	MimeType string
	Data     []byte
	Size     int64
}

type JobState struct {
	Job         *Job
	Status      JobStatus
	Position    int
	WorkerID    string
	StartedAt   time.Time
	WaitTime    time.Duration
	AgentStatus *StatusReport
}

type WorkerInfo struct {
	WorkerID    string   `json:"worker_id"`
	Name        string   `json:"name"`
	Agents      []string `json:"agents"`
	Tags        []string `json:"tags"`
	ConnectedAt time.Time
}

var ErrQueueFull = fmt.Errorf("queue is full")
