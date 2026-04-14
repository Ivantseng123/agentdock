package bot

import (
	"context"

	"agentdock/internal/queue"
)

// SkillProvider loads skills for a job.
type SkillProvider interface {
	LoadAll(ctx context.Context) (map[string]*queue.SkillPayload, error)
}
