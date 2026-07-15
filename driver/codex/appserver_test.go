package codex

import (
	"encoding/json"
	"testing"
)

// noMethod is a MethodForID that resolves nothing (for notification/request
// tests where the registry must not matter).
func noMethod(uint64) (string, bool) { return "", false }

// --- builders ---

func TestBuildInitializeNoJSONRPC(t *testing.T) {
	var v map[string]json.RawMessage
	if err := json.Unmarshal([]byte(BuildInitialize(0, "test-version")), &v); err != nil {
		t.Fatal(err)
	}
	if _, ok := v["jsonrpc"]; ok {
		t.Fatal("jsonrpc header must be absent")
	}
	if asString(v["method"]) != "initialize" {
		t.Fatalf("unexpected method: %s", asString(v["method"]))
	}
}

func TestBuildInitializedIsNotification(t *testing.T) {
	var v map[string]json.RawMessage
	_ = json.Unmarshal([]byte(BuildInitialized()), &v)
	if _, ok := v["id"]; ok {
		t.Fatal("notification must not carry id")
	}
	if asString(v["method"]) != "initialized" {
		t.Fatal("expected initialized")
	}
}

func TestBuildThreadStartShape(t *testing.T) {
	var v map[string]json.RawMessage
	_ = json.Unmarshal([]byte(BuildThreadStart(1, "gpt-5.5", "/tmp", "be helpful")), &v)
	params, _ := asObject(v["params"])
	if _, ok := params["approvalPolicy"]; ok {
		t.Fatal("thread/start must respect the user's configured approval policy")
	}
	if _, ok := params["sandbox"]; ok {
		t.Fatal("thread/start must respect the user's configured sandbox")
	}
	if asString(params["cwd"]) != "/tmp" || asString(params["model"]) != "gpt-5.5" {
		t.Fatal("cwd/model not set")
	}
	if asString(params["developerInstructions"]) != "be helpful" {
		t.Fatal("developerInstructions should carry the system prompt")
	}
	if _, ok := params["personality"]; ok {
		t.Fatal("must not use personality field")
	}
}

func TestBuildTurnStartAndInterrupt(t *testing.T) {
	var v map[string]json.RawMessage
	_ = json.Unmarshal([]byte(BuildTurnStart(2, "thr_1", "hello")), &v)
	params, _ := asObject(v["params"])
	if asString(params["threadId"]) != "thr_1" {
		t.Fatal("threadId")
	}
	var input []map[string]any
	_ = json.Unmarshal(params["input"], &input)
	if len(input) != 1 || input[0]["type"] != "text" || input[0]["text"] != "hello" {
		t.Fatalf("unexpected input: %v", input)
	}

	var iv map[string]json.RawMessage
	_ = json.Unmarshal([]byte(BuildTurnInterrupt(3, "thr_1", "turn_9")), &iv)
	ip, _ := asObject(iv["params"])
	if asString(ip["threadId"]) != "thr_1" || asString(ip["turnId"]) != "turn_9" {
		t.Fatal("interrupt params")
	}
}

func TestBuildApprovalResponse(t *testing.T) {
	var v map[string]json.RawMessage
	_ = json.Unmarshal([]byte(BuildApprovalResponse(json.RawMessage(`42`), "accept")), &v)
	if _, ok := v["method"]; ok {
		t.Fatal("approval response must not carry method")
	}
	if asString(v["result"]) != "accept" {
		t.Fatal("result")
	}
	if n, ok := asUint64(v["id"]); !ok || n != 42 {
		t.Fatal("id should echo 42")
	}
	// string id form
	var sv map[string]json.RawMessage
	_ = json.Unmarshal([]byte(BuildApprovalResponse(json.RawMessage(`"req-abc"`), "decline")), &sv)
	if asString(sv["id"]) != "req-abc" || asString(sv["result"]) != "decline" {
		t.Fatal("string id form")
	}
}

// --- response classification (id-agnostic via MethodForID) ---

func TestParseThreadResponse(t *testing.T) {
	// Real 0.142.5 shape: result.thread.id.
	line := `{"id":1,"result":{"thread":{"id":"019f46a0-thread","sessionId":"019f46a0-thread"}}}`
	ev := ParseLine(line, func(id uint64) (string, bool) {
		if id != 1 {
			t.Fatalf("unexpected id %d", id)
		}
		return "thread/start", true
	})
	if ev.Type != EvThreadResponse || ev.ThreadID != "019f46a0-thread" {
		t.Fatalf("unexpected: %+v", ev)
	}
}

func TestParseThreadResumeSameVariant(t *testing.T) {
	line := `{"id":9,"result":{"thread":{"id":"thr_xyz"}}}`
	ev := ParseLine(line, func(uint64) (string, bool) { return "thread/resume", true })
	if ev.Type != EvThreadResponse || ev.ThreadID != "thr_xyz" {
		t.Fatalf("unexpected: %+v", ev)
	}
}

func TestParseTurnResponse(t *testing.T) {
	line := `{"id":2,"result":{"turn":{"id":"turn_9","status":"inProgress","items":[]}}}`
	ev := ParseLine(line, func(uint64) (string, bool) { return "turn/start", true })
	if ev.Type != EvTurnResponse || ev.TurnID != "turn_9" {
		t.Fatalf("unexpected: %+v", ev)
	}
}

func TestParseTurnResponseWithoutTurnIDStillAcks(t *testing.T) {
	ev := ParseLine(`{"id":2,"result":{}}`, func(uint64) (string, bool) { return "turn/start", true })
	if ev.Type != EvTurnResponse || ev.TurnID != "" {
		t.Fatalf("unexpected: %+v", ev)
	}
}

func TestParseTurnInterruptResponse(t *testing.T) {
	ev := ParseLine(`{"id":3,"result":{}}`, func(uint64) (string, bool) { return "turn/interrupt", true })
	if ev.Type != EvTurnInterruptResponse {
		t.Fatalf("unexpected: %+v", ev)
	}
}

func TestParseInitializeResponse(t *testing.T) {
	ev := ParseLine(`{"id":0,"result":{"userAgent":"x"}}`, func(uint64) (string, bool) { return "initialize", true })
	if ev.Type != EvInitializeResponse {
		t.Fatalf("unexpected: %+v", ev)
	}
}

func TestParseErrorPathConsultsMethod(t *testing.T) {
	called := false
	line := `{"id":77,"error":{"code":-32600,"message":"no rollout found for thread id"}}`
	ev := ParseLine(line, func(id uint64) (string, bool) {
		called = true
		if id != 77 {
			t.Fatalf("id %d", id)
		}
		return "thread/resume", true
	})
	if !called {
		t.Fatal("error path must consult MethodForID (so the reader can clear the pending entry)")
	}
	if ev.Type != EvError || ev.ErrCode != -32600 || ev.ErrMethod != "thread/resume" {
		t.Fatalf("unexpected: %+v", ev)
	}
}

func TestParseUnknownIDReturnsUnknown(t *testing.T) {
	ev := ParseLine(`{"id":999,"result":{"thread":{"id":"x"}}}`, noMethod)
	if ev.Type != EvUnknown {
		t.Fatalf("unknown id should be Unknown, got %+v", ev)
	}
}

// --- notifications ---

func TestParseThreadStarted(t *testing.T) {
	ev := ParseLine(`{"method":"thread/started","params":{"thread":{"id":"thr_123"}}}`, noMethod)
	if ev.Type != EvThreadStarted || ev.ThreadID != "thr_123" {
		t.Fatalf("unexpected: %+v", ev)
	}
}

func TestParseTurnStarted(t *testing.T) {
	ev := ParseLine(`{"method":"turn/started","params":{"turn":{"id":"turn_456"}}}`, noMethod)
	if ev.Type != EvTurnStarted || ev.TurnID != "turn_456" {
		t.Fatalf("unexpected: %+v", ev)
	}
}

func TestParseTurnCompletedStatuses(t *testing.T) {
	ok := ParseLine(`{"method":"turn/completed","params":{"turn":{"id":"t","status":"completed"}}}`, noMethod)
	if ok.Type != EvTurnCompleted || ok.TurnStatus != TurnCompletedOK {
		t.Fatalf("completed: %+v", ok)
	}
	inter := ParseLine(`{"method":"turn/completed","params":{"turn":{"id":"t","status":"interrupted"}}}`, noMethod)
	if inter.TurnStatus != TurnInterrupted {
		t.Fatalf("interrupted: %+v", inter)
	}
	failed := ParseLine(`{"method":"turn/completed","params":{"turn":{"id":"t","status":"failed","error":{"message":"out of context"}}}}`, noMethod)
	if failed.TurnStatus != TurnFailedStatus || failed.TurnFailMsg != "out of context" {
		t.Fatalf("failed: %+v", failed)
	}
}

func TestParseAgentMessageDelta(t *testing.T) {
	// Real 0.142.5: delta is a plain string.
	plain := ParseLine(`{"method":"item/agentMessage/delta","params":{"itemId":"m1","delta":"ping"}}`, noMethod)
	if plain.Type != EvAgentMsgDelta || plain.ItemID != "m1" || plain.Text != "ping" {
		t.Fatalf("plain: %+v", plain)
	}
	// Object form {"value": ...} also supported.
	obj := ParseLine(`{"method":"item/agentMessage/delta","params":{"itemId":"m2","delta":{"value":"hi"}}}`, noMethod)
	if obj.Text != "hi" {
		t.Fatalf("obj: %+v", obj)
	}
}

func TestParseItemCompletedVariants(t *testing.T) {
	msg := ParseLine(`{"method":"item/completed","params":{"item":{"type":"agentMessage","id":"i1","text":"done"}}}`, noMethod)
	if msg.Type != EvItemCompleted || msg.Item.Kind != ItemAgentMessage || msg.Item.Text != "done" {
		t.Fatalf("agentMessage: %+v", msg)
	}
	cmd := ParseLine(`{"method":"item/completed","params":{"item":{"type":"commandExecution","id":"i2","command":"ls","exitCode":0}}}`, noMethod)
	if cmd.Item.Kind != ItemCommandExecution || cmd.Item.ExitCode == nil || *cmd.Item.ExitCode != 0 {
		t.Fatalf("command: %+v", cmd)
	}
	mcp := ParseLine(`{"method":"item/completed","params":{"item":{"type":"mcpToolCall","id":"i3","server":"s","tool":"do","arguments":{"k":"v"}}}}`, noMethod)
	if mcp.Item.Kind != ItemMcpToolCall || mcp.Item.Tool != "do" {
		t.Fatalf("mcp: %+v", mcp)
	}
	other := ParseLine(`{"method":"item/completed","params":{"item":{"type":"fileChange","id":"i4"}}}`, noMethod)
	if other.Item.Kind != ItemOther || other.Item.ItemType != "fileChange" {
		t.Fatalf("other: %+v", other)
	}
}

func TestParseTokenUsage(t *testing.T) {
	// Real 0.142.5 shape captured from the live server.
	line := `{"method":"thread/tokenUsage/updated","params":{"threadId":"t","turnId":"u","tokenUsage":{"total":{"totalTokens":100,"inputTokens":90,"outputTokens":10},"last":{"totalTokens":17642,"inputTokens":17637,"cachedInputTokens":9600,"outputTokens":5,"reasoningOutputTokens":0}}}}`
	ev := ParseLine(line, noMethod)
	if ev.Type != EvTokenUsage {
		t.Fatalf("expected TokenUsage, got %+v", ev)
	}
	// Must use the per-turn "last" bucket, not "total".
	if ev.Usage.InputTokens != 17637 || ev.Usage.OutputTokens != 5 || ev.Usage.CachedInputTokens != 9600 {
		t.Fatalf("usage should come from last bucket: %+v", ev.Usage)
	}
}

// --- server-initiated approvals ---

func TestParseCommandApproval(t *testing.T) {
	line := `{"method":"item/commandExecution/requestApproval","id":42,"params":{"itemId":"i1","threadId":"thr","turnId":"turn"}}`
	ev := ParseLine(line, func(uint64) (string, bool) {
		t.Fatal("registry must not be called for server requests")
		return "", false
	})
	if ev.Type != EvCommandApproval || ev.ThreadID != "thr" || ev.ItemID != "i1" {
		t.Fatalf("unexpected: %+v", ev)
	}
	// request id must be echoable.
	if n, ok := asUint64(ev.RequestID); !ok || n != 42 {
		t.Fatalf("request id not captured: %s", string(ev.RequestID))
	}
}

func TestParseFileChangeApproval(t *testing.T) {
	line := `{"method":"item/fileChange/requestApproval","id":43,"params":{"itemId":"i5","threadId":"thr","turnId":"turn"}}`
	ev := ParseLine(line, noMethod)
	if ev.Type != EvFileChangeApproval {
		t.Fatalf("unexpected: %+v", ev)
	}
}

// --- malformed / unknown ---

func TestParseMalformedAndUnknown(t *testing.T) {
	for _, line := range []string{``, `not json {{{`, `{"method":"some/unknown","params":{}}`} {
		if ev := ParseLine(line, noMethod); ev.Type != EvUnknown {
			t.Fatalf("line %q should be Unknown, got %s", line, ev.Type)
		}
	}
}
