package bot

import (
	"agentdock/internal/config"
	"agentdock/internal/queue"
)

// AssemblePromptContext packages Slack-thread inputs and app-side config
// into the wire struct the worker consumes. The app is intentionally
// unaware of the XML format — that concern lives in internal/worker.
func AssemblePromptContext(
	threadMsgs []queue.ThreadMessage,
	extraDesc, channel, reporter, branch string,
	pc config.PromptConfig,
) queue.PromptContext {
	return queue.PromptContext{
		ThreadMessages:   threadMsgs,
		ExtraDescription: extraDesc,
		Channel:          channel,
		Reporter:         reporter,
		Branch:           branch,
		Language:         pc.Language,
		Goal:             pc.Goal,
		OutputRules:      pc.OutputRules,
		AllowWorkerRules: pc.IsWorkerRulesAllowed(),
	}
}
