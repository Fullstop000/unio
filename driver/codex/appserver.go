// Package codex implements the unio Driver for Codex's app-server
// protocol (`codex app-server --listen stdio://`).
//
// Transport model: one long-lived child multiplexes many threads, and each
// thread is one unio Session. Requests are JSON-RPC over stdio, but the wire
// format OMITS the `"jsonrpc":"2.0"` header (unlike ACP); the parser tolerates
// its presence for defensive compatibility.
//
// This file is the STATELESS codec: pure build/parse, no process, no channels,
// no state. It mirrors Chorus's codex_app_server.rs so both implementations
// decode the same bytes, and additionally surfaces thread/tokenUsage so unio can
// populate first-class TokenUsage (which Chorus dropped).
package codex

import "encoding/json"

// AppServerEventType tags a parsed line of codex app-server stdout.
type AppServerEventType string

const (
	// Responses (carry an id).
	EvInitializeResponse    AppServerEventType = "initialize_response"
	EvThreadResponse        AppServerEventType = "thread_response"         // thread/start | thread/resume
	EvTurnResponse          AppServerEventType = "turn_response"           // turn/start
	EvTurnInterruptResponse AppServerEventType = "turn_interrupt_response" // turn/interrupt

	// Notifications (no id).
	EvThreadStarted  AppServerEventType = "thread_started"
	EvTurnStarted    AppServerEventType = "turn_started"
	EvTurnCompleted  AppServerEventType = "turn_completed"
	EvItemStarted    AppServerEventType = "item_started"
	EvItemCompleted  AppServerEventType = "item_completed"
	EvAgentMsgDelta  AppServerEventType = "agent_message_delta"
	EvReasoningDelta AppServerEventType = "reasoning_summary_delta"
	EvCommandDelta   AppServerEventType = "command_output_delta"
	EvTokenUsage     AppServerEventType = "token_usage" // thread/tokenUsage/updated (unio addition)

	// Server-initiated requests (carry both method and id).
	EvCommandApproval    AppServerEventType = "command_approval"
	EvFileChangeApproval AppServerEventType = "file_change_approval"

	// Error and fallback.
	EvError   AppServerEventType = "error"
	EvUnknown AppServerEventType = "unknown"
)

// TurnStatusKind classifies a turn/completed outcome.
type TurnStatusKind string

const (
	TurnCompletedOK  TurnStatusKind = "completed"
	TurnInterrupted  TurnStatusKind = "interrupted"
	TurnFailedStatus TurnStatusKind = "failed"
)

// ItemKind classifies an item/started or item/completed payload.
type ItemKind string

const (
	ItemAgentMessage     ItemKind = "agentMessage"
	ItemCommandExecution ItemKind = "commandExecution"
	ItemMcpToolCall      ItemKind = "mcpToolCall"
	ItemUserMessage      ItemKind = "userMessage"
	ItemOther            ItemKind = "other"
)

// Item is the decoded payload of an item lifecycle event.
type Item struct {
	Kind      ItemKind
	ID        string
	ItemType  string // raw type string (for ItemOther)
	Text      string // agentMessage
	Command   string // commandExecution
	Cwd       string // commandExecution
	ExitCode  *int   // commandExecution
	Server    string // mcpToolCall
	Tool      string // mcpToolCall
	Arguments any    // mcpToolCall (decoded)
}

// AppServerEvent is a parsed line (struct+tag form of the Rust enum). Only the
// fields relevant to Type are populated.
type AppServerEvent struct {
	Type AppServerEventType

	// Response payloads.
	ThreadID string // EvThreadResponse, EvThreadStarted, notifications
	TurnID   string // EvTurnResponse, EvTurnStarted, EvTurnCompleted

	// Notification payloads.
	TurnStatus  TurnStatusKind // EvTurnCompleted
	TurnFailMsg string         // EvTurnCompleted (failed)
	Item        Item           // EvItemStarted, EvItemCompleted
	ItemID      string         // deltas
	Text        string         // deltas

	// Token usage (EvTokenUsage).
	Usage TurnTokenUsage

	// Server-request payloads (approvals).
	RequestID json.RawMessage // echoed back in the approval response

	// Error payload.
	ErrCode   int64
	ErrMsg    string
	ErrMethod string // method the caller registered for the failing id, if any
}

// TurnTokenUsage carries the per-turn token counts from
// thread/tokenUsage/updated (the "last" bucket).
type TurnTokenUsage struct {
	InputTokens       int64
	OutputTokens      int64
	CachedInputTokens int64
	ReasoningTokens   int64
	TotalTokens       int64
}

func unknown() AppServerEvent { return AppServerEvent{Type: EvUnknown} }

// MethodForID resolves the method a caller associated with a numeric request id
// (typically a map lookup). Response classification is id-agnostic: it depends
// on the registered method, not the id value, which is what lets one connection
// multiplex many threads.
type MethodForID func(id uint64) (string, bool)

// ParseLine parses one line of codex app-server stdout. Response lines are
// classified via methodForID; notifications and server requests by method name.
func ParseLine(line string, methodForID MethodForID) AppServerEvent {
	var msg map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
		return unknown()
	}
	_, hasID := msg["id"]
	_, hasResult := msg["result"]
	_, hasError := msg["error"]
	_, hasMethod := msg["method"]

	if hasID && (hasResult || hasError) && !hasMethod {
		return parseResponse(msg, methodForID)
	}
	if hasMethod && hasID {
		return parseServerRequest(asString(msg["method"]), msg)
	}
	if hasMethod {
		return parseNotification(asString(msg["method"]), msg)
	}
	return unknown()
}

func parseResponse(msg map[string]json.RawMessage, methodForID MethodForID) AppServerEvent {
	idU64, hasU64 := asUint64(msg["id"])
	var method string
	var methodOK bool
	if hasU64 && methodForID != nil {
		method, methodOK = methodForID(idU64)
	}

	if errRaw, ok := msg["error"]; ok {
		errObj, _ := asObject(errRaw)
		code := asInt(errObj["code"])
		emsg := "unknown error"
		if data, ok := asObject(errObj["data"]); ok {
			if m := asString(data["message"]); m != "" {
				emsg = m
			}
		} else if m := asString(errObj["message"]); m != "" {
			emsg = m
		}
		ev := AppServerEvent{Type: EvError, ErrCode: code, ErrMsg: emsg}
		if methodOK {
			ev.ErrMethod = method
		}
		return ev
	}

	if !hasU64 || !methodOK {
		return unknown()
	}
	result, _ := asObject(msg["result"])
	switch method {
	case "initialize":
		return AppServerEvent{Type: EvInitializeResponse}
	case "thread/start", "thread/resume":
		if thread, ok := asObject(result["thread"]); ok {
			if tid := asString(thread["id"]); tid != "" {
				return AppServerEvent{Type: EvThreadResponse, ThreadID: tid}
			}
		}
		return unknown()
	case "turn/start":
		if turn, ok := asObject(result["turn"]); ok {
			if tid := asString(turn["id"]); tid != "" {
				return AppServerEvent{Type: EvTurnResponse, TurnID: tid}
			}
		}
		return AppServerEvent{Type: EvTurnResponse}
	case "turn/interrupt":
		return AppServerEvent{Type: EvTurnInterruptResponse}
	default:
		return unknown()
	}
}

func parseNotification(method string, msg map[string]json.RawMessage) AppServerEvent {
	params, _ := asObject(msg["params"])
	switch method {
	case "thread/started":
		if thread, ok := asObject(params["thread"]); ok {
			return AppServerEvent{Type: EvThreadStarted, ThreadID: asString(thread["id"])}
		}
		return AppServerEvent{Type: EvThreadStarted}
	case "turn/started":
		if turn, ok := asObject(params["turn"]); ok {
			return AppServerEvent{Type: EvTurnStarted, TurnID: asString(turn["id"])}
		}
		return AppServerEvent{Type: EvTurnStarted}
	case "turn/completed":
		return parseTurnCompleted(params)
	case "item/started":
		item, _ := asObject(params["item"])
		return AppServerEvent{Type: EvItemStarted, ThreadID: asString(params["threadId"]), TurnID: asString(params["turnId"]), Item: parseItem(item)}
	case "item/completed":
		item, _ := asObject(params["item"])
		return AppServerEvent{Type: EvItemCompleted, ThreadID: asString(params["threadId"]), TurnID: asString(params["turnId"]), Item: parseItem(item)}
	case "item/agentMessage/delta":
		return AppServerEvent{Type: EvAgentMsgDelta, ThreadID: asString(params["threadId"]), TurnID: asString(params["turnId"]), ItemID: asString(params["itemId"]), Text: deltaText(params["delta"])}
	case "item/reasoning/summaryTextDelta":
		return AppServerEvent{Type: EvReasoningDelta, ThreadID: asString(params["threadId"]), TurnID: asString(params["turnId"]), ItemID: asString(params["itemId"]), Text: asString(params["delta"])}
	case "item/commandExecution/outputDelta":
		text := asString(params["output"])
		if text == "" {
			text = asString(params["delta"])
		}
		return AppServerEvent{Type: EvCommandDelta, ThreadID: asString(params["threadId"]), TurnID: asString(params["turnId"]), ItemID: asString(params["itemId"]), Text: text}
	case "thread/tokenUsage/updated":
		ev := parseTokenUsage(params)
		ev.ThreadID = asString(params["threadId"])
		ev.TurnID = asString(params["turnId"])
		return ev
	default:
		return unknown()
	}
}

func parseTurnCompleted(params map[string]json.RawMessage) AppServerEvent {
	turn, _ := asObject(params["turn"])
	ev := AppServerEvent{Type: EvTurnCompleted, ThreadID: asString(params["threadId"]), TurnID: asString(turn["id"])}
	switch asString(turn["status"]) {
	case "completed", "":
		ev.TurnStatus = TurnCompletedOK
	case "interrupted":
		ev.TurnStatus = TurnInterrupted
	case "failed":
		ev.TurnStatus = TurnFailedStatus
		if e, ok := asObject(turn["error"]); ok {
			ev.TurnFailMsg = asString(e["message"])
		}
		if ev.TurnFailMsg == "" {
			ev.TurnFailMsg = "turn failed"
		}
	default:
		ev.TurnStatus = TurnFailedStatus
		ev.TurnFailMsg = "unknown status: " + asString(turn["status"])
	}
	return ev
}

func parseTokenUsage(params map[string]json.RawMessage) AppServerEvent {
	tu, ok := asObject(params["tokenUsage"])
	if !ok {
		return unknown()
	}
	// Prefer the per-turn "last" bucket; fall back to "total".
	bucket, ok := asObject(tu["last"])
	if !ok {
		bucket, ok = asObject(tu["total"])
		if !ok {
			return unknown()
		}
	}
	return AppServerEvent{
		Type: EvTokenUsage,
		Usage: TurnTokenUsage{
			InputTokens:       asInt(bucket["inputTokens"]),
			OutputTokens:      asInt(bucket["outputTokens"]),
			CachedInputTokens: asInt(bucket["cachedInputTokens"]),
			ReasoningTokens:   asInt(bucket["reasoningOutputTokens"]),
			TotalTokens:       asInt(bucket["totalTokens"]),
		},
	}
}

func parseServerRequest(method string, msg map[string]json.RawMessage) AppServerEvent {
	reqID := msg["id"]
	params, _ := asObject(msg["params"])
	base := AppServerEvent{
		RequestID: append(json.RawMessage(nil), reqID...),
		ThreadID:  asString(params["threadId"]),
		TurnID:    asString(params["turnId"]),
		ItemID:    asString(params["itemId"]),
	}
	switch method {
	case "item/commandExecution/requestApproval":
		base.Type = EvCommandApproval
		return base
	case "item/fileChange/requestApproval":
		base.Type = EvFileChangeApproval
		return base
	default:
		return unknown()
	}
}

func parseItem(item map[string]json.RawMessage) Item {
	it := Item{ID: asString(item["id"]), ItemType: asString(item["type"])}
	switch it.ItemType {
	case "agentMessage":
		it.Kind = ItemAgentMessage
		it.Text = asString(item["text"])
	case "commandExecution":
		it.Kind = ItemCommandExecution
		it.Command = asString(item["command"])
		it.Cwd = asString(item["cwd"])
		if raw, ok := item["exitCode"]; ok && len(raw) > 0 && string(raw) != "null" {
			code := int(asInt(raw))
			it.ExitCode = &code
		}
	case "mcpToolCall":
		it.Kind = ItemMcpToolCall
		it.Server = asString(item["server"])
		it.Tool = asString(item["tool"])
		if raw, ok := item["arguments"]; ok {
			_ = json.Unmarshal(raw, &it.Arguments)
		}
	case "userMessage":
		it.Kind = ItemUserMessage
	default:
		it.Kind = ItemOther
	}
	return it
}

// deltaText extracts a delta payload that may be a plain string or {"value": s}.
func deltaText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	if s := asString(raw); s != "" {
		return s
	}
	if obj, ok := asObject(raw); ok {
		return asString(obj["value"])
	}
	return ""
}
