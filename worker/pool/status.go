package pool

import (
	"sync"
	"time"

	"github.com/Ivantseng123/agentdock/shared/queue"
)

type statusAccumulator struct {
	mu             sync.Mutex
	jobID          string
	workerID       string
	nickname       string
	pid            int
	agentCmd       string
	alive          bool
	lastEvent      string
	lastTool       string
	lastToolArg    string
	lastEventAt    time.Time
	toolCalls      int
	filesRead      int
	outputBytes    int
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
		s.lastTool = event.ToolName
		s.lastToolArg = event.ToolInputFirstArg
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
		JobID:          s.jobID,
		WorkerID:       s.workerID,
		WorkerNickname: s.nickname,
		PID:            s.pid,
		AgentCmd:       s.agentCmd,
		Alive:          s.alive,
		LastEvent:      s.lastEvent,
		LastTool:       s.lastTool,
		LastToolArg:    s.lastToolArg,
		LastEventAt:    s.lastEventAt,
		ToolCalls:      s.toolCalls,
		FilesRead:      s.filesRead,
		OutputBytes:    s.outputBytes,
		CostUSD:        s.costUSD,
		InputTokens:    s.inputTokens,
		OutputTokens:   s.outputTokens,
		PrepareSeconds: s.prepareSeconds,
	}
}

// applyTotalsTo overwrites cost/token fields on result with the totals the
// accumulator captured from the agent stream. Safe to call regardless of
// result.Status: when the run ended before any "result" event landed, the
// fields stay at zero, which is the correct semantic. Worker is the sole
// writer of these fields on the success / cancelled / failed paths, so
// overwriting any caller-set value is intentional.
//
// Cancellation mid-stream keeps the totals: the runner's OnEvent forwarder
// drains AND forwards remaining events on the ctx.Done() path, so a final
// "result" event buffered before cancel still reaches the accumulator (#253).
func (s *statusAccumulator) applyTotalsTo(result *queue.JobResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result.CostUSD = s.costUSD
	result.InputTokens = s.inputTokens
	result.OutputTokens = s.outputTokens
}
