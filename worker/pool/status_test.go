package pool

import (
	"testing"

	"github.com/Ivantseng123/agentdock/shared/queue"
)

func TestStatusAccumulator_ApplyTotalsTo(t *testing.T) {
	tests := []struct {
		name           string
		setup          func(*statusAccumulator)
		initialResult  queue.JobResult
		wantCost       float64
		wantInTokens   int
		wantOutTokens  int
	}{
		{
			name: "completed with totals",
			setup: func(s *statusAccumulator) {
				s.costUSD = 0.05
				s.inputTokens = 100
				s.outputTokens = 50
			},
			initialResult: queue.JobResult{Status: "completed"},
			wantCost:      0.05,
			wantInTokens:  100,
			wantOutTokens: 50,
		},
		{
			name: "partial on failure",
			setup: func(s *statusAccumulator) {
				s.costUSD = 0.02
				s.inputTokens = 40
				s.outputTokens = 0
			},
			initialResult: queue.JobResult{Status: "failed", Error: "boom"},
			wantCost:      0.02,
			wantInTokens:  40,
			wantOutTokens: 0,
		},
		{
			name:          "never saw result event",
			setup:         func(s *statusAccumulator) {},
			initialResult: queue.JobResult{Status: "cancelled"},
			wantCost:      0,
			wantInTokens:  0,
			wantOutTokens: 0,
		},
		{
			name: "overwrites caller-set values",
			setup: func(s *statusAccumulator) {
				s.costUSD = 0.10
				s.inputTokens = 200
				s.outputTokens = 100
			},
			initialResult: queue.JobResult{
				Status:       "completed",
				CostUSD:      99.99,
				InputTokens:  9999,
				OutputTokens: 9999,
			},
			wantCost:      0.10,
			wantInTokens:  200,
			wantOutTokens: 100,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &statusAccumulator{}
			tc.setup(s)
			r := tc.initialResult

			s.applyTotalsTo(&r)

			if r.CostUSD != tc.wantCost {
				t.Errorf("CostUSD = %v, want %v", r.CostUSD, tc.wantCost)
			}
			if r.InputTokens != tc.wantInTokens {
				t.Errorf("InputTokens = %d, want %d", r.InputTokens, tc.wantInTokens)
			}
			if r.OutputTokens != tc.wantOutTokens {
				t.Errorf("OutputTokens = %d, want %d", r.OutputTokens, tc.wantOutTokens)
			}
		})
	}
}
