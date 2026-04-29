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
	JobCancelled JobStatus = "cancelled"
)

type SkillPayload struct {
	Files map[string][]byte `json:"files"`
}

type Job struct {
	ID               string                   `json:"id"`
	Priority         int                      `json:"priority"`
	Seq              uint64                   `json:"seq"`
	ChannelID        string                   `json:"channel_id"`
	ThreadTS         string                   `json:"thread_ts"`
	UserID           string                   `json:"user_id"`
	Repo             string                   `json:"repo"`
	Branch           string                   `json:"branch"`
	CloneURL         string                   `json:"clone_url"`
	Skills           map[string]*SkillPayload `json:"skills"`
	RequestID        string                   `json:"request_id"`
	Attachments      []AttachmentMeta         `json:"attachments"`
	StatusMsgTS      string                   `json:"status_msg_ts,omitempty"`
	TaskType         string                   `json:"task_type,omitempty"`
	WorkflowArgs     map[string]string        `json:"workflow_args,omitempty"`
	RetryCount       int                      `json:"retry_count,omitempty"`
	RetryOfJobID     string                   `json:"retry_of_job_id,omitempty"`
	PromptContext    *PromptContext           `json:"prompt_context,omitempty"`
	SubmittedAt      time.Time                `json:"submitted_at"`
	EncryptedSecrets []byte                   `json:"encrypted_secrets,omitempty"`
	RefRepos         []RefRepo                `json:"ref_repos,omitempty"`
}

type AttachmentMeta struct {
	SlackFileID string `json:"slack_file_id"`
	Filename    string `json:"filename"`
	Size        int64  `json:"size"`
	MimeType    string `json:"mime_type"`
	DownloadURL string `json:"download_url"`
}

// RefRepo is a read-only reference repo attached alongside the primary repo.
// Repo is owner/name (mirrors Job.Repo); CloneURL is the worker's clone target;
// Branch empty = default branch.
type RefRepo struct {
	Repo     string `json:"repo"`
	CloneURL string `json:"clone_url"`
	Branch   string `json:"branch,omitempty"`
}

type ThreadMessage struct {
	User      string `json:"user"`
	Timestamp string `json:"timestamp"`
	Text      string `json:"text"`
}

type PromptContext struct {
	ThreadMessages   []ThreadMessage `json:"thread_messages"`
	ExtraDescription string          `json:"extra_description,omitempty"`
	Channel          string          `json:"channel"`
	Reporter         string          `json:"reporter"`
	BotName          string          `json:"bot_name,omitempty"` // Slack handle the agent should refer to itself by
	Branch           string          `json:"branch,omitempty"`
	Language         string          `json:"language"`
	Goal             string          `json:"goal"`
	ResponseSchema   string          `json:"response_schema,omitempty"`
	OutputRules      []string        `json:"output_rules"`
	AllowWorkerRules bool            `json:"allow_worker_rules"`
	// PriorAnswer carries bot's own previous substantive answers in this
	// thread so multi-turn Ask conversations don't regress to amnesia.
	// Slice shape reserves room for multi-turn history; v1 only fills the
	// most recent qualifying message.
	PriorAnswer []ThreadMessage `json:"prior_answer,omitempty"`
	// RefRepos lists ref repos that prepared successfully, with their
	// absolute paths on the worker for the agent to grep/read. Filled by
	// worker after PrepareAt, not by the app at BuildJob time.
	RefRepos []RefRepoContext `json:"ref_repos,omitempty"`
	// UnavailableRefs lists ref repos (owner/name) whose clone failed.
	// Filled by worker; the prompt builder renders an `<unavailable_refs>`
	// block so the agent knows which context is missing.
	UnavailableRefs []string `json:"unavailable_refs,omitempty"`
}

// RefRepoContext is one ref repo as seen by the prompt builder. Path is the
// absolute on-disk path the agent should grep/read; filled by worker after
// PrepareAt succeeds. Empty Branch = default branch.
type RefRepoContext struct {
	Repo   string `json:"repo"`
	Branch string `json:"branch,omitempty"`
	Path   string `json:"path"`
}

type JobResult struct {
	JobID          string    `json:"job_id"`
	Status         string    `json:"status"`
	RawOutput      string    `json:"raw_output"`
	Error          string    `json:"error"`
	StartedAt      time.Time `json:"started_at"`
	FinishedAt     time.Time `json:"finished_at"`
	CostUSD        float64   `json:"cost_usd,omitempty"`
	InputTokens    int       `json:"input_tokens,omitempty"`
	OutputTokens   int       `json:"output_tokens,omitempty"`
	// RefViolations lists ref repos (owner/name) where the post-execute
	// guard detected agent writes. Worker is task-agnostic — it always
	// reports; app side decides how to react (Ask: metric only; Issue:
	// fail-fast at createAndPostIssue s1).
	RefViolations  []string  `json:"ref_violations,omitempty"`
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
	CancelledAt time.Time
}

type WorkerInfo struct {
	WorkerID    string   `json:"worker_id"`
	Name        string   `json:"name"`
	Nickname    string   `json:"nickname,omitempty"`
	Agents      []string `json:"agents"`
	Tags        []string `json:"tags"`
	Slots       int      `json:"slots,omitempty"` // concurrent jobs this worker handles; 0 normalised to 1 by consumers
	ConnectedAt time.Time
}

var ErrQueueFull = fmt.Errorf("queue is full")
