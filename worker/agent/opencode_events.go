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
//
// State.Status is required to gate `tool` part emission: opencode
// upstream emits `message.part.updated` for `pending` → `running` →
// `completed` (or `error`) transitions of the same tool call. We
// follow opencode/src/cli/cmd/run.ts:623's gate and only emit a
// downstream `tool_use` event on the terminal `completed`/`error`
// status, so Slack counters reflect one event per tool call instead
// of three.
type opencodeAssistantPart struct {
	Type  string `json:"type"`
	Text  string `json:"text,omitempty"`
	Tool  string `json:"tool,omitempty"`
	State *struct {
		Status string         `json:"status,omitempty"`
		Input  map[string]any `json:"input,omitempty"`
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
// no-op (heartbeats, intermediate tool states, recognized-but-irrelevant
// types); error only on JSON-shape failures.
//
// Mapping mirrors `ReadStreamJSONOpencode`'s taxonomy so consumers can
// stay parser-agnostic:
//
//   - server.connected, server.heartbeat, message.updated,
//     session.status, message.part.delta → no-op.
//   - session.error → emit `error` event so Stage 3 Task 3.2-9 / 3.2-11
//     can read it for retry / failure classification without re-touching
//     the decoder. Stage 2 happy path treats `error` as informational
//     (POST response is still the authoritative completion signal).
//   - message.part.updated + part.type=text → message_delta (TextBytes
//     = len(text)). Mirrors the per-delta event ReadStreamJSONOpencode
//     emits for each `text` NDJSON line.
//   - message.part.updated + part.type=tool → tool_use, **only when
//     part.state.status ∈ {completed, error}**. Intermediate pending /
//     running updates are dropped so a single tool call yields exactly
//     one downstream tool_use (matching opencode run.ts:623). Without
//     this gate, Slack's status display inflates `toolCalls` /
//     `filesRead` ~3x per tool call.
//   - message.part.updated + part.type=step-finish → result, carrying
//     part.tokens and part.cost. Server emits one of these per step;
//     the SSE consumer in opencode_http.go accumulates them and emits
//     ONE terminal `result` event at stream end so downstream
//     `worker/pool/status.go` (which OVERWRITES on `result`) records
//     cumulative tokens, not just the last step's.
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
		"message.updated", "session.status",
		"message.part.delta":
		return queue.StreamEvent{}, false, nil
	case "session.error":
		// Surface upstream errors as a typed event so Stage 3's
		// retry / Bug A classifier can see them. Stage 2 happy path
		// doesn't act on this beyond letting it ride to downstream
		// consumers (which currently ignore unknown event types,
		// matching spawn-mode's behavior on the same upstream noise).
		return queue.StreamEvent{Type: "error"}, true, nil
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
		// Gate on terminal status only (opencode run.ts:623). Pending /
		// running intermediates yield empty input + would inflate
		// downstream counters 3x.
		if part.State == nil {
			return queue.StreamEvent{}, false
		}
		if part.State.Status != "completed" && part.State.Status != "error" {
			return queue.StreamEvent{}, false
		}
		return queue.StreamEvent{
			Type:              "tool_use",
			ToolName:          titleCaseFirstASCII(part.Tool),
			ToolInputFirstArg: firstArgFromInput(part.State.Input),
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
