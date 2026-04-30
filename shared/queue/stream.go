package queue

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
)

type StreamEvent struct {
	Type              string
	ToolName          string
	ToolInputFirstArg string // truncated to toolInputArgMaxLen runes; empty when no recognized key
	TextBytes         int
	CostUSD           float64
	InputTokens       int
	OutputTokens      int
}

// ReadStreamJSONClaude reads NDJSON from `claude --print --output-format
// stream-json`. Tool calls are nested under `message.content[]` blocks of
// type `tool_use`; the final answer arrives as a single `result` event with
// totals.
func ReadStreamJSONClaude(r io.Reader, eventCh chan<- StreamEvent) string {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	var reassembled strings.Builder
	var resultText string

	for scanner.Scan() {
		line := scanner.Text()
		var raw map[string]any
		if json.Unmarshal([]byte(line), &raw) != nil {
			continue
		}

		eventType, _ := raw["type"].(string)
		switch eventType {
		case "assistant":
			// Claude CLI wraps content in message.content[] blocks.
			msg, _ := raw["message"].(map[string]any)
			if msg == nil {
				continue
			}
			blocks, _ := msg["content"].([]any)
			for _, b := range blocks {
				block, _ := b.(map[string]any)
				if block == nil {
					continue
				}
				blockType, _ := block["type"].(string)
				switch blockType {
				case "tool_use":
					name, _ := block["name"].(string)
					input, _ := block["input"].(map[string]any)
					select {
					case eventCh <- StreamEvent{
						Type:              "tool_use",
						ToolName:          name,
						ToolInputFirstArg: extractFirstArg(input),
					}:
					default:
					}
				case "text":
					text, _ := block["text"].(string)
					if text != "" {
						reassembled.WriteString(text)
						select {
						case eventCh <- StreamEvent{Type: "message_delta", TextBytes: len(text)}:
						default:
						}
					}
				}
			}
		case "result":
			if res, ok := raw["result"].(string); ok {
				resultText = res
			}
			costEvent := StreamEvent{Type: "result"}
			if cost, ok := raw["total_cost_usd"].(float64); ok {
				costEvent.CostUSD = cost
			}
			if usage, ok := raw["usage"].(map[string]any); ok {
				if in, ok := usage["input_tokens"].(float64); ok {
					costEvent.InputTokens = int(in)
				}
				if out, ok := usage["output_tokens"].(float64); ok {
					costEvent.OutputTokens = int(out)
				}
			}
			select {
			case eventCh <- costEvent:
			default:
			}
		}
	}

	if resultText != "" {
		return resultText
	}
	return reassembled.String()
}

// ReadStreamJSONOpencode reads NDJSON from `opencode run --format json`.
// Event shape is FLAT — tool calls live at top-level `part.tool` /
// `part.state.input` instead of claude's `message.content[].tool_use`. Tool
// names are lowercase (`read`/`bash`/`grep`); titleCaseTool normalizes them
// to claude's PascalCase taxonomy so downstream handlers built around it
// (e.g. statusAccumulator's "Read" filesRead match) work without per-agent
// special cases.
//
// opencode reports tokens per `step_finish` event rather than once at end;
// we accumulate and emit a single synthesized `result` event after the
// scanner closes, mirroring claude's terminal-result contract.
//
// Final text is reassembled from `text` events (opencode has no equivalent
// of claude's `result.result` field).
func ReadStreamJSONOpencode(r io.Reader, eventCh chan<- StreamEvent) string {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	var output strings.Builder
	var totalCost float64
	var totalInputTokens, totalOutputTokens int

	for scanner.Scan() {
		line := scanner.Text()
		var raw struct {
			Type string `json:"type"`
			Part struct {
				Tool  string `json:"tool"`
				Text  string `json:"text"`
				State *struct {
					Input map[string]any `json:"input"`
				} `json:"state,omitempty"`
				Tokens *struct {
					Input  int `json:"input"`
					Output int `json:"output"`
				} `json:"tokens,omitempty"`
				Cost float64 `json:"cost"`
			} `json:"part"`
		}
		if json.Unmarshal([]byte(line), &raw) != nil {
			continue
		}
		switch raw.Type {
		case "tool_use":
			var input map[string]any
			if raw.Part.State != nil {
				input = raw.Part.State.Input
			}
			select {
			case eventCh <- StreamEvent{
				Type:              "tool_use",
				ToolName:          titleCaseTool(raw.Part.Tool),
				ToolInputFirstArg: extractFirstArg(input),
			}:
			default:
			}
		case "text":
			text := raw.Part.Text
			if text != "" {
				output.WriteString(text)
				select {
				case eventCh <- StreamEvent{Type: "message_delta", TextBytes: len(text)}:
				default:
				}
			}
		case "step_finish":
			if raw.Part.Tokens != nil {
				totalInputTokens += raw.Part.Tokens.Input
				totalOutputTokens += raw.Part.Tokens.Output
			}
			totalCost += raw.Part.Cost
		}
	}

	select {
	case eventCh <- StreamEvent{
		Type:         "result",
		CostUSD:      totalCost,
		InputTokens:  totalInputTokens,
		OutputTokens: totalOutputTokens,
	}:
	default:
	}

	return output.String()
}

// ReadRawOutput reads plain text stdout (non-stream agents).
func ReadRawOutput(r io.Reader) string {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	var buf strings.Builder
	for scanner.Scan() {
		buf.WriteString(scanner.Text())
		buf.WriteByte('\n')
	}
	return buf.String()
}

// toolInputArgMaxLen caps how much of a tool_use input field surfaces into
// StreamEvent. Slack rendering may truncate further; this is the upstream cap.
const toolInputArgMaxLen = 100

// extractFirstArg returns a human-readable string from a tool_use input
// object, truncated to toolInputArgMaxLen runes. Tries common keys in
// priority order:
//   - file_path / filePath: Read, Edit, Write (claude snake / opencode camel)
//   - command:              Bash
//   - pattern:              Grep
//   - path:                 LS, Glob
//   - url:                  WebFetch
//
// Returns empty when no key matches — Slack render then falls back to the
// counter line. Both snake_case and camelCase are accepted to cover the two
// agents' input conventions; tools rarely carry both, but if they do the
// snake form wins (claude's convention is more widely tested).
func extractFirstArg(input map[string]any) string {
	if input == nil {
		return ""
	}
	keys := []string{"file_path", "filePath", "command", "pattern", "path", "url"}
	for _, k := range keys {
		if v, ok := input[k].(string); ok && v != "" {
			return truncateRunes(v, toolInputArgMaxLen)
		}
	}
	return ""
}

// titleCaseTool capitalizes the first ASCII letter of name. opencode emits
// lowercase tool names (read/bash/grep); claude emits PascalCase
// (Read/Bash). Normalizing here lets downstream consumers — Slack render,
// statusAccumulator's hardcoded "Read" match for filesRead — share a single
// taxonomy. Non-ASCII or empty input passes through unchanged so future tool
// names with extended characters don't get mangled.
func titleCaseTool(name string) string {
	if name == "" || name[0] < 'a' || name[0] > 'z' {
		return name
	}
	return string(name[0]-'a'+'A') + name[1:]
}

// truncateRunes caps s to max runes, appending "…" when truncation occurs.
// Rune-based to avoid splitting multi-byte UTF-8 characters mid-byte.
func truncateRunes(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max-1]) + "…"
}
