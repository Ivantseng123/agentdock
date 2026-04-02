package diagnosis

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"slack-issue-bot/internal/llm"
)

// LoopInput defines the parameters for a single agent-loop run.
type LoopInput struct {
	Type      string          // "bug" or "feature"
	Message   string          // Original Slack message
	RepoPath  string          // Path to cloned repo
	Keywords  []string        // Pre-extracted keywords from message (for pre-grep)
	Prompt    llm.PromptOptions
	MaxTurns  int             // Default 5
	MaxTokens int             // Default 100000
}

// estimateTokens gives a rough token count based on rune length.
func estimateTokens(text string) int {
	return len([]rune(text))
}

// estimateMessages sums token estimates for all messages in the conversation.
func estimateMessages(msgs []llm.Message) int {
	total := 0
	for _, m := range msgs {
		total += estimateTokens(m.Content)
	}
	return total
}

// RunLoop executes the agent loop: chat with tools until the LLM produces
// a valid triage card or we hit the turn/token limit.
func RunLoop(ctx context.Context, chain llm.ConversationProvider, tools []Tool, input LoopInput) (llm.DiagnoseResponse, error) {
	if input.MaxTurns <= 0 {
		input.MaxTurns = 5
	}
	if input.MaxTokens <= 0 {
		input.MaxTokens = 100000
	}

	toolDefs := ToolDefs(tools)
	toolMap := ToolMap(tools)

	baseSystem := llm.AgentSystemPrompt(input.Type, input.Prompt)

	typeLabel := "Bug"
	if input.Type == "feature" {
		typeLabel = "Feature"
	}

	// Pre-grep: run keyword grep before the agent loop starts (free, no LLM call).
	// This catches Chinese/original-language terms that the LLM might miss when translating.
	var preGrepSection string
	if len(input.Keywords) > 0 {
		preGrepFiles, _ := grepFiles(input.RepoPath, input.Keywords, 10)
		if len(preGrepFiles) > 0 {
			slog.Info("agent loop pre-grep hit", "files", len(preGrepFiles), "keywords", len(input.Keywords))
			var sb strings.Builder
			sb.WriteString("\n\n## Pre-search Results\n\nThe following files matched keywords from the original message:\n")
			for _, f := range preGrepFiles {
				sb.WriteString(fmt.Sprintf("- %s\n", f))
			}
			sb.WriteString("\nUse read_file to examine the most relevant ones. You may also search for additional terms.")
			preGrepSection = sb.String()
		}
	}

	messages := []llm.Message{
		{Role: "user", Content: fmt.Sprintf("## %s Report\n\nRepository: %s\n\n> %s%s", typeLabel, input.RepoPath, input.Message, preGrepSection)},
	}

	var discoveredFiles []llm.FileRef

	for turn := 1; turn <= input.MaxTurns+1; turn++ {
		systemPrompt := baseSystem

		// On the last normal turn, warn the LLM to finish.
		if turn == input.MaxTurns {
			systemPrompt += "\n\nIMPORTANT: This is your last turn with tools. You MUST produce the triage JSON on your NEXT response. Wrap up your investigation now."
		}

		// Determine which tools to offer.
		var activeDefs []llm.ToolDef
		if turn <= input.MaxTurns {
			activeDefs = toolDefs
		}
		// turn > maxTurns: no tools, demand triage card.

		// Token budget check: force finish if over 80%.
		tokenEst := estimateMessages(messages) + estimateTokens(systemPrompt)
		if tokenEst > input.MaxTokens*80/100 {
			slog.Info("agent loop forced finish: token budget nearly exhausted",
				"turn", turn,
				"token_estimate", tokenEst,
				"max_tokens", input.MaxTokens,
			)
			activeDefs = nil
			systemPrompt += "\n\nYou have used most of the token budget. Produce the triage JSON NOW with whatever information you have."
		}

		resp, err := chain.Chat(ctx, llm.ChatRequest{
			SystemPrompt: systemPrompt,
			Messages:     messages,
			Tools:        activeDefs,
		})
		if err != nil {
			return llm.DiagnoseResponse{}, fmt.Errorf("agent loop turn %d: %w", turn, err)
		}

		// Case 1: Tool calls on a normal turn (turn <= maxTurns).
		if len(resp.ToolCalls) > 0 && turn <= input.MaxTurns {
			tc := resp.ToolCalls[0] // Execute first tool only.

			// Append assistant message (with tool call).
			messages = append(messages, llm.Message{
				Role:      "assistant",
				Content:   resp.Content,
				ToolCalls: resp.ToolCalls,
			})

			tool, ok := toolMap[tc.Name]
			var result string
			if !ok {
				result = fmt.Sprintf("Error: unknown tool %q. Available tools: %s",
					tc.Name, availableToolNames(tools))
			} else {
				result, err = tool.Execute(input.RepoPath, tc.Args)
				if err != nil {
					result = fmt.Sprintf("Error executing %s: %v", tc.Name, err)
				}
			}

			// Track discovered files from grep/search results.
			if ok && (tc.Name == "grep" || tc.Name == "search_code") {
				for _, line := range strings.Split(result, "\n") {
					line = strings.TrimSpace(line)
					if line != "" && !strings.HasPrefix(line, "No ") && !strings.HasPrefix(line, "Error") && !strings.HasPrefix(line, "...") {
						// Extract file path (before first colon for search_code results).
						path := line
						if idx := strings.Index(line, ":"); idx > 0 && tc.Name == "search_code" {
							path = line[:idx]
						}
						if !containsFile(discoveredFiles, path) {
							discoveredFiles = append(discoveredFiles, llm.FileRef{
								Path:        path,
								Description: fmt.Sprintf("discovered via %s", tc.Name),
							})
						}
					}
				}
			}

			// Truncate tool results if token budget is nearly exhausted.
			resultTokens := estimateTokens(result)
			remaining := input.MaxTokens - estimateMessages(messages) - estimateTokens(systemPrompt)
			if resultTokens > remaining*60/100 && resultTokens > 500 {
				cutoff := remaining * 60 / 100
				if cutoff < 500 {
					cutoff = 500
				}
				runes := []rune(result)
				if cutoff < len(runes) {
					result = string(runes[:cutoff]) + "\n... [truncated due to token budget]"
				}
			}

			// Append tool_result message.
			messages = append(messages, llm.Message{
				Role:       "tool_result",
				Content:    result,
				ToolCallID: tc.ID,
			})

			slog.Info("agent loop turn",
				"turn", turn,
				"tool", tc.Name,
				"result_bytes", len(result),
				"token_estimate", estimateMessages(messages),
			)
			continue
		}

		// Case 2: Tool calls on forced-finish turn (turn == maxTurns+1 or turn > maxTurns).
		// Ignore tool calls, append as assistant message, and loop will end.
		if len(resp.ToolCalls) > 0 && turn > input.MaxTurns {
			slog.Info("agent loop forced finish: ignoring tool call on extra turn",
				"turn", turn,
				"tool", resp.ToolCalls[0].Name,
			)
			messages = append(messages, llm.Message{
				Role:    "assistant",
				Content: resp.Content,
			})
			continue
		}

		// Case 3: Content with valid triage JSON.
		if resp.Content != "" {
			parsed, parseErr := llm.ParseLLMTextResponse(resp.Content)
			if parseErr == nil && parsed.Summary != "" && parsed.Summary != resp.Content {
				// Successful parse: ParseLLMTextResponse returns Summary != original text
				// when it successfully extracted structured JSON.
				slog.Info("agent loop finished",
					"turns", turn,
					"token_estimate", estimateMessages(messages),
					"confidence", parsed.Confidence,
				)
				return parsed, nil
			}

			// Not valid JSON — treat as "thinking".
			slog.Info("agent loop turn",
				"turn", turn,
				"tool", "none",
				"result_bytes", len(resp.Content),
				"token_estimate", estimateMessages(messages),
			)
			messages = append(messages, llm.Message{
				Role:    "assistant",
				Content: resp.Content,
			})
			continue
		}

		// Case 4: Empty response.
		slog.Warn("agent loop: empty response",
			"turn", turn,
		)
	}

	// Fallback: exceeded all turns without a valid triage card.
	slog.Info("agent loop forced finish: max turns exceeded",
		"turns", input.MaxTurns+1,
		"token_estimate", estimateMessages(messages),
	)

	// Cap discovered files at 5.
	if len(discoveredFiles) > 5 {
		discoveredFiles = discoveredFiles[:5]
	}

	return llm.DiagnoseResponse{
		Summary:    "Agent could not produce a complete triage within the turn limit.",
		Files:      discoveredFiles,
		Confidence: "low",
	}, nil
}

// availableToolNames returns a comma-separated list of tool names.
func availableToolNames(tools []Tool) string {
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Name()
	}
	return strings.Join(names, ", ")
}

// containsFile checks if a file path is already in the discovered files list.
func containsFile(files []llm.FileRef, path string) bool {
	for _, f := range files {
		if f.Path == path {
			return true
		}
	}
	return false
}
