package workflow

import (
	"fmt"
	"strings"

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
