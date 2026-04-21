package workflow

import (
	"context"
	"testing"

	"github.com/Ivantseng123/agentdock/shared/queue"
)

type fakeWorkflow struct{ typ string }

func (f *fakeWorkflow) Type() string { return f.typ }
func (f *fakeWorkflow) Trigger(ctx context.Context, ev TriggerEvent, args string) (NextStep, error) {
	return NextStep{Kind: NextStepSubmit}, nil
}
func (f *fakeWorkflow) Selection(ctx context.Context, p *Pending, value string) (NextStep, error) {
	return NextStep{Kind: NextStepSubmit}, nil
}
func (f *fakeWorkflow) BuildJob(ctx context.Context, p *Pending) (*queue.Job, string, error) {
	return &queue.Job{TaskType: f.typ}, "status", nil
}
func (f *fakeWorkflow) HandleResult(ctx context.Context, job *queue.Job, r *queue.JobResult) error {
	return nil
}

func TestWorkflowInterfaceCompiles(t *testing.T) {
	var w Workflow = &fakeWorkflow{typ: "issue"}
	if w.Type() != "issue" {
		t.Errorf("Type() = %q, want issue", w.Type())
	}
}

func TestNextStepKinds(t *testing.T) {
	tests := []struct {
		name string
		kind NextStepKind
	}{
		{"post selector", NextStepPostSelector},
		{"open modal", NextStepOpenModal},
		{"submit", NextStepSubmit},
		{"error", NextStepError},
		{"noop", NextStepNoop},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := NextStep{Kind: tc.kind}
			if s.Kind != tc.kind {
				t.Errorf("Kind = %v", s.Kind)
			}
		})
	}
}
