// Package claude implements the unio ProtocolDriver for Claude Code's headless
// streaming-JSON mode (`claude -p --output-format stream-json`).
//
// Transport model: process-per-session. Claude's headless CLI cannot multiplex
// several sessions over one child, so each unio Session owns its own `claude`
// child. The CLI emits one JSON object per line on stdout; the caller writes
// user messages as single JSON lines to stdin.
//
// This file is the STATELESS codec: pure parse/encode of the wire protocol, no
// process, no channels, no state. It mirrors Chorus's claude_headless.rs so the
// two implementations decode the same bytes identically.
package claude

import "encoding/json"

// HeadlessEventType tags a parsed line of Claude headless stdout.
type HeadlessEventType string

const (
	// EvSystemInit: first line, carries the session id.
	EvSystemInit HeadlessEventType = "system_init"
	// EvApiRetry: an API retry notification.
	EvApiRetry HeadlessEventType = "api_retry"
	// EvThinkingDelta: a thinking content delta.
	EvThinkingDelta HeadlessEventType = "thinking_delta"
	// EvTextDelta: an assistant text content delta.
	EvTextDelta HeadlessEventType = "text_delta"
	// EvToolUseStart: a tool_use content block started.
	EvToolUseStart HeadlessEventType = "tool_use_start"
	// EvInputJSONDelta: partial JSON input for a tool_use block.
	EvInputJSONDelta HeadlessEventType = "input_json_delta"
	// EvToolUseStop: a tool_use content block stopped.
	EvToolUseStop HeadlessEventType = "tool_use_stop"
	// EvContentBlockStop: a text/thinking content block stopped.
	EvContentBlockStop HeadlessEventType = "content_block_stop"
	// EvAssistantMessage: a COMPLETE assistant message (not a stream delta).
	// Some environments (e.g. proxy CLIs that don't emit stream_event deltas)
	// deliver the whole turn's content in one `{"type":"assistant"}` line. We
	// parse its content[] into coalesced items so text/tool_use are captured
	// regardless of whether the runtime streamed deltas.
	EvAssistantMessage HeadlessEventType = "assistant_message"
	// EvTurnResult: final event of a turn, carries result + cost.
	EvTurnResult HeadlessEventType = "turn_result"
	// EvUnknown: unrecognised or irrelevant line.
	EvUnknown HeadlessEventType = "unknown"
)

// HeadlessEvent is a parsed line of Claude headless stdout (struct+tag form of
// the Rust enum; only the fields relevant to Type are populated).
type HeadlessEvent struct {
	Type HeadlessEventType

	// SessionID: EvSystemInit, EvTurnResult.
	SessionID string
	// Index: EvToolUseStart, EvInputJSONDelta, EvToolUseStop, EvContentBlockStop.
	Index int
	// Text: EvThinkingDelta, EvTextDelta.
	Text string
	// ToolID / ToolName: EvToolUseStart.
	ToolID   string
	ToolName string
	// PartialJSON: EvInputJSONDelta.
	PartialJSON string
	// Attempt / ErrorMsg: EvApiRetry.
	Attempt  int
	ErrorMsg string
	// Result / IsError / StopReason / Subtype: EvTurnResult.
	Result     string
	IsError    bool
	StopReason string
	Subtype    string
	// CostUSD / DurationMs: EvTurnResult (unio usage enhancement — Chorus
	// dropped these; we keep them for first-class TokenUsage).
	CostUSD    float64
	DurationMs int64
	// Usage: EvTurnResult token counts when the CLI reports them.
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	// Content: EvAssistantMessage — the fully-formed content items of a
	// complete assistant message (text and/or tool_use).
	Content []ContentItem
}

// ContentItem is one item of a complete assistant message's content[] array.
type ContentItem struct {
	// Kind is "text", "thinking", or "tool_use".
	Kind string
	// Text: for "text"/"thinking".
	Text string
	// ToolID / ToolName / ToolInput: for "tool_use" (ToolInput is decoded JSON).
	ToolID    string
	ToolName  string
	ToolInput any
}

// unknown is a small helper for the many "irrelevant line" branches.
func unknown() HeadlessEvent { return HeadlessEvent{Type: EvUnknown} }

// ParseLine parses one stdout JSONL line into a HeadlessEvent. Malformed or
// irrelevant lines return EvUnknown rather than an error — the reader loop skips
// them.
func ParseLine(line string) HeadlessEvent {
	var v map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &v); err != nil {
		return unknown()
	}
	switch asString(v["type"]) {
	case "system":
		return parseSystem(v)
	case "stream_event":
		return parseStreamEvent(v)
	case "assistant":
		return parseAssistantMessage(v)
	case "user":
		// User echoes carry no assistant content we need.
		return unknown()
	case "result":
		return parseResult(v)
	default:
		return unknown()
	}
}

func parseSystem(v map[string]json.RawMessage) HeadlessEvent {
	switch asString(v["subtype"]) {
	case "init":
		return HeadlessEvent{Type: EvSystemInit, SessionID: asString(v["session_id"])}
	case "api_retry":
		return HeadlessEvent{
			Type:     EvApiRetry,
			Attempt:  int(asInt(v["attempt"])),
			ErrorMsg: asString(v["error"]),
		}
	default:
		// hook_started / hook_response / status and friends: ignore.
		return unknown()
	}
}

func parseStreamEvent(v map[string]json.RawMessage) HeadlessEvent {
	event, ok := asObject(v["event"])
	if !ok {
		return unknown()
	}
	switch asString(event["type"]) {
	case "content_block_start":
		return parseContentBlockStart(event)
	case "content_block_delta":
		return parseContentBlockDelta(event)
	case "content_block_stop":
		return HeadlessEvent{Type: EvContentBlockStop, Index: int(asInt(event["index"]))}
	default:
		// message_start / message_delta / message_stop: ignore.
		return unknown()
	}
}

func parseContentBlockStart(event map[string]json.RawMessage) HeadlessEvent {
	cb, ok := asObject(event["content_block"])
	if !ok {
		return unknown()
	}
	if asString(cb["type"]) == "tool_use" {
		return HeadlessEvent{
			Type:     EvToolUseStart,
			Index:    int(asInt(event["index"])),
			ToolID:   asString(cb["id"]),
			ToolName: asString(cb["name"]),
		}
	}
	return unknown()
}

func parseContentBlockDelta(event map[string]json.RawMessage) HeadlessEvent {
	delta, ok := asObject(event["delta"])
	if !ok {
		return unknown()
	}
	switch asString(delta["type"]) {
	case "thinking_delta":
		return HeadlessEvent{Type: EvThinkingDelta, Text: asString(delta["thinking"])}
	case "text_delta":
		return HeadlessEvent{Type: EvTextDelta, Text: asString(delta["text"])}
	case "input_json_delta":
		return HeadlessEvent{
			Type:        EvInputJSONDelta,
			Index:       int(asInt(event["index"])),
			PartialJSON: asString(delta["partial_json"]),
		}
	default:
		return unknown()
	}
}

func parseResult(v map[string]json.RawMessage) HeadlessEvent {
	ev := HeadlessEvent{
		Type:       EvTurnResult,
		SessionID:  asString(v["session_id"]),
		Result:     asString(v["result"]),
		IsError:    asBool(v["is_error"]),
		StopReason: asString(v["stop_reason"]),
		Subtype:    asString(v["subtype"]),
		CostUSD:    asFloat(v["total_cost_usd"]),
		DurationMs: asInt(v["duration_ms"]),
	}
	// usage object is optional and shaped {input_tokens, output_tokens,
	// cache_read_input_tokens, cache_creation_input_tokens}.
	if usage, ok := asObject(v["usage"]); ok {
		ev.InputTokens = asInt(usage["input_tokens"])
		ev.OutputTokens = asInt(usage["output_tokens"])
		ev.CacheReadTokens = asInt(usage["cache_read_input_tokens"])
		ev.CacheWriteTokens = asInt(usage["cache_creation_input_tokens"])
	}
	return ev
}

// parseAssistantMessage extracts the content[] of a complete assistant message.
// Shape: {"type":"assistant","message":{"content":[{"type":"text","text":...},
// {"type":"tool_use","id":...,"name":...,"input":{...}}, ...]}}.
func parseAssistantMessage(v map[string]json.RawMessage) HeadlessEvent {
	msg, ok := asObject(v["message"])
	if !ok {
		return unknown()
	}
	var content []json.RawMessage
	if len(msg["content"]) > 0 {
		_ = json.Unmarshal(msg["content"], &content)
	}
	if len(content) == 0 {
		return unknown()
	}
	items := make([]ContentItem, 0, len(content))
	for _, raw := range content {
		block, ok := asObject(raw)
		if !ok {
			continue
		}
		switch asString(block["type"]) {
		case "text":
			if t := asString(block["text"]); t != "" {
				items = append(items, ContentItem{Kind: "text", Text: t})
			}
		case "thinking":
			if t := asString(block["thinking"]); t != "" {
				items = append(items, ContentItem{Kind: "thinking", Text: t})
			}
		case "tool_use":
			var input any
			if len(block["input"]) > 0 {
				_ = json.Unmarshal(block["input"], &input)
			}
			items = append(items, ContentItem{
				Kind:      "tool_use",
				ToolID:    asString(block["id"]),
				ToolName:  asString(block["name"]),
				ToolInput: input,
			})
		}
	}
	if len(items) == 0 {
		return unknown()
	}
	return HeadlessEvent{Type: EvAssistantMessage, Content: items}
}
func BuildUserMessage(text string) string {
	b, err := json.Marshal(map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": text,
		},
	})
	if err != nil {
		// Marshalling a fixed-shape map of strings cannot fail.
		return `{"type":"user","message":{"role":"user","content":""}}`
	}
	return string(b)
}

// --- small JSON accessors (tolerant: missing/wrong-typed fields yield zero) ---

func asString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return ""
}

func asInt(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var n float64
	if json.Unmarshal(raw, &n) == nil {
		return int64(n)
	}
	return 0
}

func asFloat(raw json.RawMessage) float64 {
	if len(raw) == 0 {
		return 0
	}
	var n float64
	if json.Unmarshal(raw, &n) == nil {
		return n
	}
	return 0
}

func asBool(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var b bool
	if json.Unmarshal(raw, &b) == nil {
		return b
	}
	return false
}

func asObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) == nil && m != nil {
		return m, true
	}
	return nil, false
}
