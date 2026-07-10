package codex

import "encoding/json"

// The app-server wire format omits the "jsonrpc":"2.0" header, so these builders
// emit bare {id,method,params} / {method,params} objects.

// appRequest serialises a request WITHOUT the jsonrpc header.
func appRequest(id uint64, method string, params any) string {
	b, err := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if err != nil {
		return ""
	}
	return string(b)
}

// appNotification serialises a notification (no id) WITHOUT the jsonrpc header.
func appNotification(method string, params any) string {
	b, err := json.Marshal(map[string]any{"method": method, "params": params})
	if err != nil {
		return ""
	}
	return string(b)
}

// BuildInitialize builds the initialize request (id 0).
func BuildInitialize(id uint64, version string) string {
	return appRequest(id, "initialize", map[string]any{
		"clientInfo": map[string]any{
			"name":    "unio",
			"title":   "unio",
			"version": version,
		},
		"capabilities": map[string]any{},
	})
}

// BuildInitialized builds the initialized notification (sent after the
// initialize response completes the handshake).
func BuildInitialized() string {
	return appNotification("initialized", map[string]any{})
}

// BuildThreadStart builds a thread/start request. developerInstructions carries
// the standing/system prompt when non-empty (codex's free-form slot; do NOT use
// personality, which is a 3-value enum).
func BuildThreadStart(id uint64, model, cwd, developerInstructions string) string {
	params := map[string]any{
		"cwd":            cwd,
		"approvalPolicy": "never",
		"sandbox":        "danger-full-access",
	}
	if model != "" {
		params["model"] = model
	}
	if developerInstructions != "" {
		params["developerInstructions"] = developerInstructions
	}
	return appRequest(id, "thread/start", params)
}

// BuildThreadResume builds a thread/resume request, forwarding
// developerInstructions so the standing prompt survives resume.
func BuildThreadResume(id uint64, threadID, developerInstructions string) string {
	params := map[string]any{"threadId": threadID}
	if developerInstructions != "" {
		params["developerInstructions"] = developerInstructions
	}
	return appRequest(id, "thread/resume", params)
}

// BuildTurnStart builds a turn/start request.
func BuildTurnStart(id uint64, threadID, text string) string {
	return appRequest(id, "turn/start", map[string]any{
		"threadId": threadID,
		"input":    []any{map[string]any{"type": "text", "text": text}},
	})
}

// BuildTurnInterrupt builds a turn/interrupt request.
func BuildTurnInterrupt(id uint64, threadID, turnID string) string {
	return appRequest(id, "turn/interrupt", map[string]any{
		"threadId": threadID,
		"turnId":   turnID,
	})
}

// BuildApprovalResponse builds a JSON-RPC result response echoing the server's
// request id. decision is "accept" | "decline" | "cancel". No method field.
func BuildApprovalResponse(requestID json.RawMessage, decision string) string {
	var idVal any
	if len(requestID) > 0 {
		_ = json.Unmarshal(requestID, &idVal)
	}
	b, err := json.Marshal(map[string]any{"id": idVal, "result": decision})
	if err != nil {
		return ""
	}
	return string(b)
}

// --- tolerant JSON accessors (missing/wrong-typed fields yield zero) ---

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

func asUint64(raw json.RawMessage) (uint64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	var n float64
	if json.Unmarshal(raw, &n) == nil {
		return uint64(n), true
	}
	return 0, false
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
