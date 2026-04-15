package worker

import (
	"sync"
	"time"

	"agentdock/internal/queue"
)

type statusAccumulator struct {
	mu           sync.Mutex
	jobID        string
	workerID     string
	pid          int
	agentCmd     string
	alive        bool
	lastEvent    string
	lastEventAt  time.Time
	toolCalls    int
	filesRead    int
	outputBytes  int
	costUSD        float64
	inputTokens    int
	outputTokens   int
	prepareSeconds float64
}

func (s *statusAccumulator) setPID(pid int, cmd string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pid = pid
	s.agentCmd = cmd
	s.alive = true
}

func (s *statusAccumulator) recordEvent(event queue.StreamEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastEventAt = time.Now()
	switch event.Type {
	case "tool_use":
		s.toolCalls++
		s.lastEvent = "tool_use:" + event.ToolName
		if event.ToolName == "Read" {
			s.filesRead++
		}
	case "message_delta":
		s.outputBytes += event.TextBytes
		s.lastEvent = "message_delta"
	case "result":
		s.costUSD = event.CostUSD
		s.inputTokens = event.InputTokens
		s.outputTokens = event.OutputTokens
		s.lastEvent = "result"
	}
}

func (s *statusAccumulator) setPrepareSeconds(d float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prepareSeconds = d
}

func (s *statusAccumulator) toReport() queue.StatusReport {
	s.mu.Lock()
	defer s.mu.Unlock()
	return queue.StatusReport{
		JobID:        s.jobID,
		WorkerID:     s.workerID,
		PID:          s.pid,
		AgentCmd:     s.agentCmd,
		Alive:        s.alive,
		LastEvent:    s.lastEvent,
		LastEventAt:  s.lastEventAt,
		ToolCalls:    s.toolCalls,
		FilesRead:    s.filesRead,
		OutputBytes:  s.outputBytes,
		CostUSD:        s.costUSD,
		InputTokens:    s.inputTokens,
		OutputTokens:   s.outputTokens,
		PrepareSeconds: s.prepareSeconds,
	}
}
