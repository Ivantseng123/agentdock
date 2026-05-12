package workflow

import (
	"testing"
	"time"

	"github.com/Ivantseng123/agentdock/shared/queue"
)

func TestFormatDiagnostics(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name   string
		state  *queue.JobState
		result *queue.JobResult
		want   string
	}{
		{
			name:   "all zero produces empty",
			state:  nil,
			result: &queue.JobResult{},
			want:   "",
		},
		{
			name:   "elapsed only",
			state:  nil,
			result: &queue.JobResult{StartedAt: now, FinishedAt: now.Add(5 * time.Second)},
			want:   "5s",
		},
		{
			name:   "elapsed + worker",
			state:  &queue.JobState{WorkerID: "w1"},
			result: &queue.JobResult{StartedAt: now, FinishedAt: now.Add(5 * time.Second)},
			want:   "5s · worker: w1",
		},
		{
			name:   "elapsed + cost + worker",
			state:  &queue.JobState{WorkerID: "w1"},
			result: &queue.JobResult{StartedAt: now, FinishedAt: now.Add(5 * time.Second), CostUSD: 0.42},
			want:   "5s · $0.42 · worker: w1",
		},
		{
			name:   "cost only (worker unavailable)",
			state:  nil,
			result: &queue.JobResult{CostUSD: 0.42},
			want:   "$0.42",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := formatDiagnostics(tc.state, tc.result)
			if got != tc.want {
				t.Errorf("formatDiagnostics() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestWithDiagnostics(t *testing.T) {
	tests := []struct {
		name, text, diag, want string
	}{
		{"empty diag returns text unchanged", "hello", "", "hello"},
		{"non-empty diag appended on a new line", "hello", "5s · worker: w1", "hello\n5s · worker: w1"},
		{"empty text and empty diag", "", "", ""},
		{"empty text with diag", "", "5s", "\n5s"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := withDiagnostics(tc.text, tc.diag)
			if got != tc.want {
				t.Errorf("withDiagnostics(%q, %q) = %q, want %q", tc.text, tc.diag, got, tc.want)
			}
		})
	}
}
