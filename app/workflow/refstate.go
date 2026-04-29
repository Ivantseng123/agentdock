package workflow

import "github.com/Ivantseng123/agentdock/shared/queue"

// refExclusionsFor returns repos that should NOT appear as ref candidates
// when the user is picking refs: the primary plus any refs already picked.
// Shared between AskWorkflow and IssueWorkflow — both states call this from
// their RefExclusions() method, which app/bot uses to filter repo-suggestion
// type-ahead results.
func refExclusionsFor(primary string, refs []queue.RefRepo) []string {
	out := make([]string, 0, 1+len(refs))
	if primary != "" {
		out = append(out, primary)
	}
	for _, r := range refs {
		out = append(out, r.Repo)
	}
	return out
}
