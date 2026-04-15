package logging

// Standardized attribute keys. Use these for high-frequency keys to prevent typos.
// One-off keys can be written as literal strings.
const (
	KeyRequestID = "request_id"
	KeyJobID     = "job_id"
	KeyWorkerID  = "worker_id"
	KeyChannelID = "channel_id"
	KeyThreadTS  = "thread_ts"
	KeyUserID    = "user_id"
	KeyRepo      = "repo"
	KeyProvider  = "provider"
	KeyStatus    = "status"
	KeyError     = "error"
	KeyURL       = "url"
	KeyDuration  = "duration_ms"
	KeyActionID  = "action_id"
	KeyVersion   = "version"
	KeyAddr      = "addr"
	KeyPath      = "path"
	KeyName      = "name"
	KeyCount     = "count"
)
