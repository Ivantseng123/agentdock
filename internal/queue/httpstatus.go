package queue

import (
	"encoding/json"
	"net/http"
	"os"
	"syscall"
	"time"
)

type jobStatusEntry struct {
	ID           string    `json:"id"`
	Status       JobStatus `json:"status"`
	Repo         string    `json:"repo"`
	Branch       string    `json:"branch,omitempty"`
	Position     int       `json:"position,omitempty"`
	Age          string    `json:"age"`
	WaitTime     string    `json:"wait_time,omitempty"`
	WorkerID     string    `json:"worker_id,omitempty"`
	AgentPID     int       `json:"agent_pid,omitempty"`
	AgentCommand string    `json:"agent_command,omitempty"`
	AgentAlive   *bool     `json:"agent_alive,omitempty"`
	ChannelID    string    `json:"channel_id"`
	ThreadTS     string    `json:"thread_ts"`
}

type jobsResponse struct {
	QueueDepth int              `json:"queue_depth"`
	Total      int              `json:"total"`
	Jobs       []jobStatusEntry `json:"jobs"`
}

// StatusHandler returns an http.HandlerFunc that reports current job states.
func StatusHandler(store JobStore, queue JobQueue) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		all, err := store.ListAll()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		now := time.Now()
		entries := make([]jobStatusEntry, 0, len(all))
		for _, state := range all {
			entry := jobStatusEntry{
				ID:        state.Job.ID,
				Status:    state.Status,
				Repo:      state.Job.Repo,
				Branch:    state.Job.Branch,
				Age:       now.Sub(state.Job.SubmittedAt).Truncate(time.Second).String(),
				ChannelID: state.Job.ChannelID,
				ThreadTS:  state.Job.ThreadTS,
			}
			if state.WorkerID != "" {
				entry.WorkerID = state.WorkerID
			}
			if state.WaitTime > 0 {
				entry.WaitTime = state.WaitTime.Truncate(time.Second).String()
			}
			if state.Status == JobPending {
				pos, _ := queue.QueuePosition(state.Job.ID)
				entry.Position = pos
			}
			if state.AgentStatus != nil {
				entry.AgentPID = state.AgentStatus.PID
				entry.AgentCommand = state.AgentStatus.AgentCmd
				alive := isProcessAlive(state.AgentStatus.PID)
				entry.AgentAlive = &alive
			}
			entries = append(entries, entry)
		}

		resp := jobsResponse{
			QueueDepth: queue.QueueDepth(),
			Total:      len(entries),
			Jobs:       entries,
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}

// isProcessAlive checks if a process with the given PID is still running.
func isProcessAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// On Unix, signal 0 checks if process exists without actually sending a signal.
	err = proc.Signal(syscall.Signal(0))
	return err == nil
}
