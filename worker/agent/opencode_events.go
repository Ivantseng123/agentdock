package agent

import (
	"encoding/json"

	"github.com/Ivantseng123/agentdock/shared/queue"
)

// SSE event decoder for `opencode serve`'s `/event` endpoint.
//
// Spec line 88 / plan Task 3.2-6 say "feeds inner JSON to
// shared/queue/stream.go ReadStreamJSONOpencode". That description is
// schema-blind: `opencode run --format json` emits NDJSON with
// `{"type":"text","part":{...}}` at root, but `opencode serve`'s SSE
// emits BusEvent envelopes — `{"id":..., "type":"message.part.updated",
// "properties":{"part":{...}}}`. The POC (refs/heads/poc/opencode-server-
// mode cmd/dev/poc-opencode-server/events.go) already discovered this
// and wrote a dedicated decoder. Stage 2 follows the POC's approach
// rather than the plan's literal wording — see Stage 2 manifest §3.5
// for the cross-source reconciliation note.
//
// Output is `shared/queue.StreamEvent` so downstream consumers (Slack
// render, status accumulator) stay unchanged; only the input parser
// differs between spawn mode (run --format json NDJSON) and server
// mode (BusEvent SSE).

// opencodeBusEventEnvelope is the outer SSE payload from `/event`. We
// only care about `type` and `properties` for Stage 2 happy path; the
// `id` field is ignored.
type opencodeBusEventEnvelope struct {
	Type       string          `json:"type"`
	Properties json.RawMessage `json:"properties"`
}

// opencodeAssistantPart mirrors the per-part shape inside
// `message.part.updated` events. opencode emits `tool` / `text` /
// `step-start` / `step-finish` part types; Stage 2 only acts on
// `text` (final assistant answer), `tool` (tool_use events), and
// `step-finish` (token counters + cost). `reasoning` and `step-start`
// are recognized but produce no event.
type opencodeAssistantPart struct {
	Type   string `json:"type"`
	Text   string `json:"text,omitempty"`
	Tool   string `json:"tool,omitempty"`
	State  *struct {
		Input map[string]any `json:"input"`
	} `json:"state,omitempty"`
	Tokens *struct {
		Input  int `json:"input"`
		Output int `json:"output"`
	} `json:"tokens,omitempty"`
	Cost float64 `json:"cost"`
	Time *struct {
		End *int64 `json:"end,omitempty"`
	} `json:"time,omitempty"`
}

// opencodeMessagePartUpdated is the body shape of the
// `message.part.updated` BusEvent. SessionID may be absent in some
// shapes; caller is responsible for cross-checking sessionID externally
// if filtering is needed (Stage 2 happy path runs one session per
// runOneServer call, so cross-talk isn't a concern yet).
type opencodeMessagePartUpdated struct {
	Part opencodeAssistantPart `json:"part"`
}

// decodeOpencodeBusEvent translates one raw BusEvent JSON line into a
// `queue.StreamEvent`. Returns (ev, true, nil) when the event maps to a
// downstream-visible signal; (zero, false, nil) when the event is a
// no-op (heartbeats, recognized-but-irrelevant types); error only on
// JSON-shape failures.
//
// Mapping mirrors `ReadStreamJSONOpencode`'s taxonomy so consumers can
// stay parser-agnostic:
//
//   - server.connected, server.heartbeat, message.updated,
//     session.status, session.error → no-op for Stage 2 happy path.
//   - message.part.updated + part.type=text → message_delta (TextBytes
//     = len(text)). Mirrors the per-delta event ReadStreamJSONOpencode
//     emits for each `text` NDJSON line.
//   - message.part.updated + part.type=tool → tool_use, with ToolName
//     pulled from part.tool and ToolInputFirstArg from part.state.input
//     using the same priority order as extractFirstArg in
//     shared/queue/stream.go.
//   - message.part.updated + part.type=step-finish → result, carrying
//     part.tokens and part.cost. (ReadStreamJSONOpencode synthesizes a
//     terminal result event after the scanner closes; the BusEvent
//     stream emits one per step. Both shapes downstream-equivalent
//     for Slack render's cost / token totals.)
//
// Bug A detection (Task 3.2-11) will examine `finish=other` +
// `tokens.output=0` on the POST /session/{id}/message response, not on
// SSE events, so it doesn't live here.
func decodeOpencodeBusEvent(raw []byte) (queue.StreamEvent, bool, error) {
	var env opencodeBusEventEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return queue.StreamEvent{}, false, err
	}
	switch env.Type {
	case "server.connected", "server.heartbeat",
		"message.updated", "session.status", "session.error",
		"message.part.delta":
		return queue.StreamEvent{}, false, nil
	case "message.part.updated":
		var updated opencodeMessagePartUpdated
		if err := json.Unmarshal(env.Properties, &updated); err != nil {
			return queue.StreamEvent{}, false, err
		}
		event, ok := mapOpencodePart(updated.Part)
		return event, ok, nil
	default:
		return queue.StreamEvent{}, false, nil
	}
}

func mapOpencodePart(part opencodeAssistantPart) (queue.StreamEvent, bool) {
	switch part.Type {
	case "text":
		return queue.StreamEvent{
			Type:      "message_delta",
			TextBytes: len(part.Text),
		}, true
	case "tool":
		var input map[string]any
		if part.State != nil {
			input = part.State.Input
		}
		return queue.StreamEvent{
			Type:              "tool_use",
			ToolName:          titleCaseFirstASCII(part.Tool),
			ToolInputFirstArg: firstArgFromInput(input),
		}, true
	case "step-finish":
		event := queue.StreamEvent{
			Type:    "result",
			CostUSD: part.Cost,
		}
		if part.Tokens != nil {
			event.InputTokens = part.Tokens.Input
			event.OutputTokens = part.Tokens.Output
		}
		return event, true
	}
	return queue.StreamEvent{}, false
}

// titleCaseFirstASCII normalizes lowercase tool names (opencode
// convention) to PascalCase so the worker-side Slack render's
// hardcoded "Read" / "Bash" / "Grep" filters match. Same logic as
// shared/queue/stream.go's titleCaseTool; duplicated here to keep the
// worker/agent package free of any private-to-shared/queue helper.
func titleCaseFirstASCII(name string) string {
	if name == "" || name[0] < 'a' || name[0] > 'z' {
		return name
	}
	return string(name[0]-'a'+'A') + name[1:]
}

// firstArgFromInput picks the first non-empty string from a tool input
// map using the same priority order as shared/queue/stream.go's
// extractFirstArg (file_path/filePath/command/pattern/path/url),
// truncated to 100 runes.
func firstArgFromInput(input map[string]any) string {
	if input == nil {
		return ""
	}
	for _, k := range []string{"file_path", "filePath", "command", "pattern", "path", "url"} {
		if v, ok := input[k].(string); ok && v != "" {
			return truncateRunesShared(v, 100)
		}
	}
	return ""
}

func truncateRunesShared(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}
