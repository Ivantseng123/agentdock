package queue

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"
)

type agentStatusEntry struct {
	PID          int     `json:"pid,omitempty"`
	Command      string  `json:"command,omitempty"`
	Alive        bool    `json:"alive,omitempty"`
	LastEvent    string  `json:"last_event,omitempty"`
	LastEventAge string  `json:"last_event_age,omitempty"`
	ToolCalls    int     `json:"tool_calls,omitempty"`
	FilesRead    int     `json:"files_read,omitempty"`
	OutputBytes  int     `json:"output_bytes,omitempty"`
	CostUSD      float64 `json:"cost_usd,omitempty"`
	InputTokens  int     `json:"input_tokens,omitempty"`
	OutputTokens int     `json:"output_tokens,omitempty"`
}

type jobStatusEntry struct {
	ID        string            `json:"id"`
	Status    JobStatus         `json:"status"`
	Repo      string            `json:"repo"`
	Branch    string            `json:"branch,omitempty"`
	Position  int               `json:"position,omitempty"`
	Age       string            `json:"age"`
	WaitTime  string            `json:"wait_time,omitempty"`
	WorkerID  string            `json:"worker_id,omitempty"`
	Agent     *agentStatusEntry `json:"agent,omitempty"`
	ChannelID string            `json:"channel_id"`
	ThreadTS  string            `json:"thread_ts"`
}

type workerEntry struct {
	WorkerID    string   `json:"worker_id"`
	Name        string   `json:"name"`
	Agents      []string `json:"agents,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	ConnectedAt string   `json:"connected_at"`
	Uptime      string   `json:"uptime"`
}

type jobsResponse struct {
	QueueDepth int              `json:"queue_depth"`
	Workers    []workerEntry    `json:"workers"`
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
				as := state.AgentStatus
				agentEntry := &agentStatusEntry{
					PID:          as.PID,
					Command:      as.AgentCmd,
					ToolCalls:    as.ToolCalls,
					FilesRead:    as.FilesRead,
					OutputBytes:  as.OutputBytes,
					CostUSD:      as.CostUSD,
					InputTokens:  as.InputTokens,
					OutputTokens: as.OutputTokens,
				}
				if !as.LastEventAt.IsZero() {
					agentEntry.LastEvent = as.LastEvent
					agentEntry.LastEventAge = now.Sub(as.LastEventAt).Truncate(time.Second).String()
				}
				if as.PID > 0 {
					agentEntry.Alive = isProcessAlive(as.PID)
				}
				entry.Agent = agentEntry
			}
			entries = append(entries, entry)
		}

		var workers []workerEntry
		if wl, err := queue.ListWorkers(r.Context()); err == nil {
			for _, wi := range wl {
				workers = append(workers, workerEntry{
					WorkerID:    wi.WorkerID,
					Name:        wi.Name,
					Agents:      wi.Agents,
					Tags:        wi.Tags,
					ConnectedAt: wi.ConnectedAt.Format(time.RFC3339),
					Uptime:      now.Sub(wi.ConnectedAt).Truncate(time.Second).String(),
				})
			}
		}

		resp := jobsResponse{
			QueueDepth: queue.QueueDepth(),
			Workers:    workers,
			Total:      len(entries),
			Jobs:       entries,
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}

// KillHandler returns an http.HandlerFunc that kills a running job via DELETE /jobs/{id}.
func KillHandler(store JobStore, commands CommandBus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/jobs/"), "/")
		if len(parts) == 0 || parts[0] == "" {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "job ID required"})
			return
		}
		jobID := parts[0]

		state, err := store.Get(jobID)
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"error": "job not found"})
			return
		}

		if state.Status == JobCompleted || state.Status == JobFailed {
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]string{"error": "job not running"})
			return
		}

		store.UpdateStatus(jobID, JobFailed)
		if commands != nil {
			commands.Send(r.Context(), Command{JobID: jobID, Action: "kill"})
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "killing", "job_id": jobID})
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
