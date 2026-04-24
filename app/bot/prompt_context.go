package bot

import (
	"github.com/Ivantseng123/agentdock/app/config"
	"github.com/Ivantseng123/agentdock/shared/queue"
)

// AssemblePromptContext packages Slack-thread inputs and app-side config
// into the wire struct the worker consumes. The app is intentionally
// unaware of the XML format — that concern lives in the worker module.
func AssemblePromptContext(
	threadMsgs []queue.ThreadMessage,
	extraDesc, channel, reporter, branch string,
	wp config.WorkflowPromptConfig,
	defaults config.PromptDefaultsConfig,
) queue.PromptContext {
	return queue.PromptContext{
		ThreadMessages:   threadMsgs,
		ExtraDescription: extraDesc,
		Channel:          channel,
		Reporter:         reporter,
		Branch:           branch,
		Language:         defaults.Language,
		Goal:             wp.Goal,
		OutputRules:      wp.OutputRules,
		AllowWorkerRules: defaults.IsWorkerRulesAllowed(),
	}
}
