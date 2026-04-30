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

// ReadStreamJSON reads NDJSON from claude --output-format stream-json.
// Returns final text from "result" event, or reassembled message_delta as fallback.
func ReadStreamJSON(r io.Reader, eventCh chan<- StreamEvent) string {
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

// extractFirstArg returns a human-readable string from a claude tool_use
// input object, truncated to toolInputArgMaxLen runes. Tries common keys in
// priority order: file_path (Read/Write/Edit), command (Bash), pattern
// (Grep), path (LS/Glob), url (WebFetch). Returns empty when no key matches
// — Slack render then falls back to the counter line.
func extractFirstArg(input map[string]any) string {
	if input == nil {
		return ""
	}
	keys := []string{"file_path", "command", "pattern", "path", "url"}
	for _, k := range keys {
		if v, ok := input[k].(string); ok && v != "" {
			return truncateRunes(v, toolInputArgMaxLen)
		}
	}
	return ""
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
