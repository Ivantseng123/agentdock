package logging

// Component identifies which subsystem produced a log entry.
const (
	CompSlack     = "Slack"
	CompGitHub    = "GitHub"
	CompAgent     = "Agent"
	CompQueue     = "Queue"
	CompWorker    = "Worker"
	CompSkill     = "Skill"
	CompConfig    = "Config"
	CompMantis    = "Mantis"
	CompApp       = "App"
	CompGitHubApp = "GitHubApp"
)

// Phase identifies the lifecycle stage of an operation.
const (
	PhaseReceive    = "接收"
	PhaseProcessing = "處理中"
	PhaseWaiting    = "等待中"
	PhaseComplete   = "完成"
	PhaseDegraded   = "降級"
	PhaseFailed     = "失敗"
	PhaseRetry      = "重試"
)
