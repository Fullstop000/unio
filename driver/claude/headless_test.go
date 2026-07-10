package claude

import (
	"encoding/json"
	"testing"
)

func TestParseSystemInit(t *testing.T) {
	line := `{"type":"system","subtype":"init","session_id":"f574bca8-1234","tools":["Bash"],"mcp_servers":[],"model":"claude-sonnet-4-6"}`
	ev := ParseLine(line)
	if ev.Type != EvSystemInit || ev.SessionID != "f574bca8-1234" {
		t.Fatalf("unexpected: %+v", ev)
	}
}

func TestParseApiRetry(t *testing.T) {
	line := `{"type":"system","subtype":"api_retry","attempt":1,"max_retries":3,"error":"rate_limit"}`
	ev := ParseLine(line)
	if ev.Type != EvApiRetry || ev.Attempt != 1 || ev.ErrorMsg != "rate_limit" {
		t.Fatalf("unexpected: %+v", ev)
	}
}

func TestParseThinkingDelta(t *testing.T) {
	line := `{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me think..."}}}`
	ev := ParseLine(line)
	if ev.Type != EvThinkingDelta || ev.Text != "Let me think..." {
		t.Fatalf("unexpected: %+v", ev)
	}
}

func TestParseTextDelta(t *testing.T) {
	line := `{"type":"stream_event","event":{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Hello!"}}}`
	ev := ParseLine(line)
	if ev.Type != EvTextDelta || ev.Text != "Hello!" {
		t.Fatalf("unexpected: %+v", ev)
	}
}

func TestParseToolUseStart(t *testing.T) {
	line := `{"type":"stream_event","event":{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_abc123","name":"Read","input":{}}}}`
	ev := ParseLine(line)
	if ev.Type != EvToolUseStart || ev.Index != 2 || ev.ToolID != "toolu_abc123" || ev.ToolName != "Read" {
		t.Fatalf("unexpected: %+v", ev)
	}
}

func TestParseInputJSONDelta(t *testing.T) {
	line := `{"type":"stream_event","event":{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"file\""}}}`
	ev := ParseLine(line)
	if ev.Type != EvInputJSONDelta || ev.Index != 2 || ev.PartialJSON != `{"file"` {
		t.Fatalf("unexpected: %+v", ev)
	}
}

func TestParseContentBlockStop(t *testing.T) {
	line := `{"type":"stream_event","event":{"type":"content_block_stop","index":1}}`
	ev := ParseLine(line)
	if ev.Type != EvContentBlockStop || ev.Index != 1 {
		t.Fatalf("unexpected: %+v", ev)
	}
}

func TestParseTurnResultSuccess(t *testing.T) {
	line := `{"type":"result","subtype":"success","is_error":false,"result":"Hello!","stop_reason":"end_turn","session_id":"abc123","duration_ms":1851,"total_cost_usd":0.026,"usage":{"input_tokens":100,"output_tokens":42,"cache_read_input_tokens":5}}`
	ev := ParseLine(line)
	if ev.Type != EvTurnResult {
		t.Fatalf("expected TurnResult, got %+v", ev)
	}
	if ev.SessionID != "abc123" || ev.Result != "Hello!" || ev.IsError || ev.StopReason != "end_turn" || ev.Subtype != "success" {
		t.Fatalf("unexpected fields: %+v", ev)
	}
	if ev.CostUSD < 0.0259 || ev.CostUSD > 0.0261 || ev.DurationMs != 1851 {
		t.Fatalf("unexpected cost/duration: %+v", ev)
	}
	if ev.InputTokens != 100 || ev.OutputTokens != 42 || ev.CacheReadTokens != 5 {
		t.Fatalf("unexpected usage: %+v", ev)
	}
}

func TestParseTurnResultError(t *testing.T) {
	line := `{"type":"result","subtype":"error","is_error":true,"result":"Something went wrong","stop_reason":"error","session_id":"err456"}`
	ev := ParseLine(line)
	if ev.Type != EvTurnResult || !ev.IsError || ev.SessionID != "err456" || ev.Subtype != "error" {
		t.Fatalf("unexpected: %+v", ev)
	}
}

func TestParseIgnoredAndMalformed(t *testing.T) {
	cases := []string{
		`{"type":"foobar","data":"x"}`,
		`this is not json {{{`,
		``,
		`   `,
		`{"type":"stream_event","event":{"type":"message_start","message":{"id":"m"}}}`,
		`{"type":"system","subtype":"status"}`,
	}
	for _, line := range cases {
		if ev := ParseLine(line); ev.Type != EvUnknown {
			t.Fatalf("line %q should be Unknown, got %s", line, ev.Type)
		}
	}
}

func TestParseCompleteAssistantMessage(t *testing.T) {
	line := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Hi!"},{"type":"tool_use","id":"toolu_9","name":"Read","input":{"path":"main.go"}}]},"session_id":"s1"}`
	ev := ParseLine(line)
	if ev.Type != EvAssistantMessage {
		t.Fatalf("expected AssistantMessage, got %s", ev.Type)
	}
	if len(ev.Content) != 2 {
		t.Fatalf("expected 2 content items, got %d", len(ev.Content))
	}
	if ev.Content[0].Kind != "text" || ev.Content[0].Text != "Hi!" {
		t.Fatalf("unexpected first item: %+v", ev.Content[0])
	}
	tc := ev.Content[1]
	if tc.Kind != "tool_use" || tc.ToolName != "Read" {
		t.Fatalf("unexpected tool item: %+v", tc)
	}
	m, ok := tc.ToolInput.(map[string]any)
	if !ok || m["path"] != "main.go" {
		t.Fatalf("tool input not decoded: %+v", tc.ToolInput)
	}
}

func TestParseCompleteAssistantMessageEmptyContentIgnored(t *testing.T) {
	// An assistant message with no usable content is Unknown.
	line := `{"type":"assistant","message":{"role":"assistant","content":[]}}`
	if ev := ParseLine(line); ev.Type != EvUnknown {
		t.Fatalf("empty content should be Unknown, got %s", ev.Type)
	}
}

func TestBuildUserMessage(t *testing.T) {
	msg := BuildUserMessage(`He said "hello"` + "\nnew line")
	var v map[string]any
	if err := json.Unmarshal([]byte(msg), &v); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if v["type"] != "user" {
		t.Fatalf("unexpected type: %v", v["type"])
	}
	m := v["message"].(map[string]any)
	if m["role"] != "user" || m["content"] != `He said "hello"`+"\nnew line" {
		t.Fatalf("unexpected message: %v", m)
	}
	if msg[len(msg)-1] == '\n' {
		t.Fatal("should not append trailing newline")
	}
}

func TestParseFullTurnSequence(t *testing.T) {
	lines := []string{
		`{"type":"system","subtype":"init","session_id":"sess-001","model":"claude-sonnet-4-6"}`,
		`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Thinking..."}}}`,
		`{"type":"stream_event","event":{"type":"content_block_stop","index":0}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Hi there!"}}}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"Hi there!","stop_reason":"end_turn","session_id":"sess-001","duration_ms":500,"total_cost_usd":0.01}`,
	}
	want := []HeadlessEventType{EvSystemInit, EvUnknown, EvThinkingDelta, EvContentBlockStop, EvTextDelta, EvTurnResult}
	for i, line := range lines {
		if got := ParseLine(line).Type; got != want[i] {
			t.Fatalf("line %d: want %s got %s", i, want[i], got)
		}
	}
}
