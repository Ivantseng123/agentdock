package workflow

import (
	"fmt"
	"strings"
	"time"

	"github.com/Ivantseng123/agentdock/shared/queue"
)

// formatDiagnostics renders the elapsed time, cost, and worker-label diagnostics line.
// Order matches result_listener's failure-path format: stats first, identity last.
// Returns "" when no fields are populated.
func formatDiagnostics(state *queue.JobState, result *queue.JobResult) string {
	var parts []string
	if elapsed := result.FinishedAt.Sub(result.StartedAt); elapsed > 0 {
		parts = append(parts, humanDuration(elapsed))
	}
	if result.CostUSD > 0 {
		parts = append(parts, fmt.Sprintf("$%.2f", result.CostUSD))
	}
	if label := workerLabel(state); label != "" {
		parts = append(parts, "worker: "+label)
	}
	return strings.Join(parts, " · ")
}

// withDiagnostics joins a body and a diagnostics line with a newline.
// Returns text unchanged when diag is empty so callers don't need a guard.
func withDiagnostics(text, diag string) string {
	if diag == "" {
		return text
	}
	return text + "\n" + diag
}

// workerLabel derives the worker identity label for diagnostics, preferring
// the live AgentStatus report (relayed by StatusListener) but falling back to
// JobState.WorkerID for jobs that finished before any status reports landed.
// Returns empty string when no identity is available.
func workerLabel(state *queue.JobState) string {
	if state == nil {
		return ""
	}
	workerID := ""
	workerNickname := ""
	if state.AgentStatus != nil {
		workerID = state.AgentStatus.WorkerID
		workerNickname = state.AgentStatus.WorkerNickname
	}
	if workerID == "" {
		workerID = state.WorkerID
	}
	label := workerNickname
	if label == "" {
		label = workerID
	} else if workerID != "" {
		label = fmt.Sprintf("%s (%s)", workerNickname, workerID)
	}
	return label
}

// humanDuration formats a duration as a compact human-readable string.
func humanDuration(d time.Duration) string {
	s := int(d.Seconds())
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	m := s / 60
	s = s % 60
	if s == 0 {
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%dm %ds", m, s)
}
