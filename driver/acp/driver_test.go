package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Fullstop000/unio/driver"
)

func TestTraeXExecutableAliases(t *testing.T) {
	aliases := configFor(TraeX).alternatives
	for _, want := range []string{"trae-cli", "coco"} {
		if !slices.Contains(aliases, want) {
			t.Fatalf("aliases %v do not contain %q", aliases, want)
		}
	}
}

func testAgentSpec() driver.AgentSpec {
	return driver.AgentSpec{Cwd: "/repo", ExecutablePath: "/bin/sh"}
}

func TestDriverBasicsAndRuntimeConfiguration(t *testing.T) {
	d := New(Runtime("definitely-not-installed-unio-test"))
	probe, err := d.Probe(context.Background())
	if err != nil || probe != driver.AuthNotInstalled {
		t.Fatalf("probe=%+v err=%v", probe, err)
	}
	if got := configFor(Kimi).buildArgs(driver.AgentSpec{Cwd: "/repo", Model: "m", ExtraArgs: []string{"--x"}}); !slices.Equal(got, []string{"--work-dir", "/repo", "--model", "m", "--x", "acp"}) {
		t.Fatalf("kimi args = %v", got)
	}
	if got := configFor(OpenCode).buildArgs(driver.AgentSpec{Cwd: "/repo", Model: "deepseek/deepseek-v4-flash", ExtraArgs: []string{"--pure"}}); !slices.Equal(got, []string{"acp", "--cwd", "/repo", "--pure"}) {
		t.Fatalf("opencode args = %v", got)
	}
	if configFor(OpenCode).modelConfig != "model" {
		t.Fatal("opencode model config is not declared")
	}
	if got := configFor(TraeX).buildArgs(driver.AgentSpec{Model: "m"}); !slices.Equal(got, []string{"--model", "m", "acp", "serve"}) {
		t.Fatalf("traex args = %v", got)
	}
	if errorMessage(nil) != "unknown ACP error" || errorMessage(&rpcError{Code: 42}) != "ACP error 42" {
		t.Fatal("unexpected ACP error formatting")
	}
	buffer := &boundedBuffer{limit: 4}
	_, _ = buffer.Write([]byte("abcdef"))
	if buffer.String() != "cdef" {
		t.Fatalf("buffer = %q", buffer.String())
	}
	buffer = &boundedBuffer{limit: 4}
	_, _ = buffer.Write([]byte("ab"))
	_, _ = buffer.Write([]byte("cde"))
	if buffer.String() != "bcde" {
		t.Fatalf("rolling buffer = %q", buffer.String())
	}
	disabledBuffer := &boundedBuffer{}
	_, _ = disabledBuffer.Write([]byte("ignored"))
	if disabledBuffer.String() != "" {
		t.Fatalf("disabled buffer = %q", disabledBuffer.String())
	}
	if _, err := marshalPermissionResponse(json.RawMessage("{"), "cancelled", ""); err == nil {
		t.Fatal("invalid JSON-RPC id should fail")
	}
	if len(mergeEnv([]string{"UNIO_ACP_TEST=1"})) == 0 {
		t.Fatal("merged environment is empty")
	}
}

func TestOpenCodeSetsModelViaSessionConfig(t *testing.T) {
	var selected string
	d := newWithTransport(OpenCode, scriptedFactory(t, func(msg map[string]json.RawMessage, send func(any)) {
		switch rawString(msg["method"]) {
		case "initialize":
			send(response(msg["id"], map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{}}))
		case "session/new":
			send(response(msg["id"], map[string]any{"sessionId": "s1"}))
		case "session/set_config_option":
			var params struct {
				SessionID string `json:"sessionId"`
				ConfigID  string `json:"configId"`
				Value     string `json:"value"`
			}
			_ = json.Unmarshal(msg["params"], &params)
			if params.SessionID != "s1" || params.ConfigID != "model" {
				t.Errorf("model params = %+v", params)
			}
			selected = params.Value
			send(response(msg["id"], map[string]any{"configOptions": []any{}}))
		}
	}))
	spec := testAgentSpec()
	spec.Model = "deepseek/deepseek-v4-flash"
	att, err := d.OpenSession(context.Background(), "key", spec, driver.OpenParams{})
	if err != nil {
		t.Fatal(err)
	}
	if err := att.Session.Run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if selected != spec.Model {
		t.Fatalf("selected model = %q", selected)
	}
	if err := att.Session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestProcessTransportRoundTripAndKill(t *testing.T) {
	spec := driver.AgentSpec{Cwd: t.TempDir()}
	transport, err := spawnTransport(context.Background(), "/bin/sh", spec, []string{"-c", "read line; printf '%s\\n' \"$line\""})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transport.stdin().Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	if !transport.stdout().Scan() || transport.stdout().Text() != "hello" {
		t.Fatalf("stdout = %q", transport.stdout().Text())
	}
	if err := transport.wait(); err != nil {
		t.Fatal(err)
	}
	if transport.errText() != "" {
		t.Fatalf("stderr = %q", transport.errText())
	}

	sleeping, err := spawnTransport(context.Background(), "/bin/sh", spec, []string{"-c", "sleep 30"})
	if err != nil {
		t.Fatal(err)
	}
	sleeping.kill()
	_ = sleeping.wait()
}

func TestInitializationRejectsUnsupportedProtocolVersion(t *testing.T) {
	d := newWithTransport(TraeX, scriptedFactory(t, func(msg map[string]json.RawMessage, send func(any)) {
		if rawString(msg["method"]) == "initialize" {
			send(response(msg["id"], map[string]any{"protocolVersion": 2, "agentCapabilities": map[string]any{}}))
		}
	}))
	att, err := d.OpenSession(context.Background(), "key", testAgentSpec(), driver.OpenParams{})
	if err != nil {
		t.Fatal(err)
	}
	if err := att.Session.Run(context.Background(), nil); !errors.Is(err, driver.NewUnsupportedError("")) {
		t.Fatalf("Run error = %v", err)
	}
}

func TestResumeRequiresRuntimeCapability(t *testing.T) {
	d := newWithTransport(TraeX, scriptedFactory(t, func(msg map[string]json.RawMessage, send func(any)) {
		if rawString(msg["method"]) == "initialize" {
			send(response(msg["id"], map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{}}))
		}
	}))
	att, err := d.OpenSession(context.Background(), "key", testAgentSpec(), driver.OpenParams{ResumeSessionID: "old"})
	if err != nil {
		t.Fatal(err)
	}
	if att.Session.Key() != "key" {
		t.Fatalf("key = %q", att.Session.Key())
	}
	if err := att.Session.Run(context.Background(), nil); !errors.Is(err, driver.NewUnsupportedError("")) {
		t.Fatalf("Run error = %v", err)
	}
	if err := att.Session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := att.Session.Run(context.Background(), nil); !errors.Is(err, driver.NewInvalidStateError("")) {
		t.Fatalf("Run after Close error = %v", err)
	}
	if _, err := att.Session.Prompt(context.Background(), driver.PromptReq{Text: "closed"}); !errors.Is(err, driver.NewInvalidStateError("")) {
		t.Fatalf("Prompt after Close error = %v", err)
	}
	if _, err := att.Session.Continue(context.Background(), "anything"); !errors.Is(err, driver.NewInvalidStateError("")) {
		t.Fatalf("Continue after Close error = %v", err)
	}
}

func TestListSessionsFiltersAndExhaustsCursor(t *testing.T) {
	var cursors []string
	d := newWithTransport(Kimi, scriptedFactory(t, func(msg map[string]json.RawMessage, send func(any)) {
		method := rawString(msg["method"])
		switch method {
		case "initialize":
			send(response(msg["id"], map[string]any{
				"protocolVersion": 1,
				"agentCapabilities": map[string]any{
					"sessionCapabilities": map[string]any{"list": map[string]any{}},
				},
			}))
		case "session/list":
			var params struct {
				Cwd    string `json:"cwd"`
				Cursor string `json:"cursor"`
			}
			_ = json.Unmarshal(msg["params"], &params)
			if params.Cwd != "/repo" {
				t.Errorf("cwd = %q", params.Cwd)
			}
			cursors = append(cursors, params.Cursor)
			if params.Cursor == "" {
				send(response(msg["id"], map[string]any{
					"sessions":   []any{map[string]any{"sessionId": "s1", "cwd": "/repo", "title": "one"}},
					"nextCursor": "page-2",
				}))
			} else {
				send(response(msg["id"], map[string]any{
					"sessions": []any{map[string]any{"sessionId": "s2", "cwd": "/repo", "title": "two"}},
				}))
			}
		}
	}))

	got, err := d.ListSessions(context.Background(), driver.ListSessionsParams{
		Cwd:  "/repo",
		Spec: testAgentSpec(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].SessionID != "s1" || got[1].SessionID != "s2" {
		t.Fatalf("sessions = %+v", got)
	}
	if len(cursors) != 2 || cursors[0] != "" || cursors[1] != "page-2" {
		t.Fatalf("cursors = %v", cursors)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestListSessionsRequiresAdvertisedCapability(t *testing.T) {
	d := newWithTransport(TraeX, scriptedFactory(t, func(msg map[string]json.RawMessage, send func(any)) {
		if rawString(msg["method"]) == "initialize" {
			send(response(msg["id"], map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{}}))
		}
	}))
	_, err := d.ListSessions(context.Background(), driver.ListSessionsParams{Cwd: "/repo", Spec: testAgentSpec()})
	if !errors.Is(err, driver.NewUnsupportedError("")) {
		t.Fatalf("ListSessions error = %v", err)
	}
}

func TestPermissionBecomesBlockedAndContinueResumesTurn(t *testing.T) {
	var promptID json.RawMessage
	d := newWithTransport(Kimi, scriptedFactory(t, func(msg map[string]json.RawMessage, send func(any)) {
		switch rawString(msg["method"]) {
		case "initialize":
			send(response(msg["id"], map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{}}))
		case "session/new":
			send(response(msg["id"], map[string]any{"sessionId": "s1"}))
		case "session/prompt":
			promptID = append(json.RawMessage(nil), msg["id"]...)
			send(notification("session/update", map[string]any{
				"sessionId": "s1",
				"update":    map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "before"}},
			}))
			send(map[string]any{
				"jsonrpc": "2.0", "id": 99, "method": "session/request_permission",
				"params": map[string]any{
					"sessionId": "s1",
					"toolCall":  map[string]any{"title": "Write file"},
					"options": []any{
						map[string]any{"kind": "allow_once", "optionId": "once"},
						map[string]any{"kind": "reject_once", "optionId": "reject"},
					},
				},
			})
		default:
			if len(msg["id"]) != 0 && len(msg["result"]) != 0 {
				send(notification("session/update", map[string]any{
					"sessionId": "s1",
					"update":    map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "after"}},
				}))
				send(response(promptID, map[string]any{"stopReason": "end_turn"}))
			}
		}
	}))

	att, err := d.OpenSession(context.Background(), "key", testAgentSpec(), driver.OpenParams{})
	if err != nil {
		t.Fatal(err)
	}
	events := att.Events.Subscribe()
	if err := att.Session.Run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	run1, err := att.Session.Prompt(context.Background(), driver.PromptReq{Text: "change it"})
	if err != nil {
		t.Fatal(err)
	}
	blocked := waitEvent(t, events, driver.EventBlocked)
	if blocked.RunID != run1 || blocked.Blocked == nil || len(blocked.Blocked.Options) != 2 || blocked.Blocked.Options[0].Value != "once" {
		t.Fatalf("blocked = %+v", blocked)
	}
	run2, err := att.Session.Continue(context.Background(), "once")
	if err != nil {
		t.Fatal(err)
	}
	completed := waitEvent(t, events, driver.EventCompleted)
	if completed.RunID != run2 || completed.Result.FinishReason != driver.FinishNatural {
		t.Fatalf("completed = %+v", completed)
	}
	if state := att.Session.ProcessState(); state.Phase != driver.PhaseActive {
		t.Fatalf("state after Continue = %+v", state)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestInterruptWaitsForCancelledPromptResponse(t *testing.T) {
	var promptID json.RawMessage
	d := newWithTransport(OpenCode, scriptedFactory(t, func(msg map[string]json.RawMessage, send func(any)) {
		switch rawString(msg["method"]) {
		case "initialize":
			send(response(msg["id"], map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{}}))
		case "session/new":
			send(response(msg["id"], map[string]any{"sessionId": "s1"}))
		case "session/prompt":
			promptID = append(json.RawMessage(nil), msg["id"]...)
		case "session/cancel":
			send(response(promptID, map[string]any{"stopReason": "cancelled"}))
		}
	}))

	att, err := d.OpenSession(context.Background(), "key", testAgentSpec(), driver.OpenParams{})
	if err != nil {
		t.Fatal(err)
	}
	events := att.Events.Subscribe()
	if err := att.Session.Run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := att.Session.Prompt(context.Background(), driver.PromptReq{Text: "wait"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := att.Session.Interrupt(ctx); err != nil {
		t.Fatal(err)
	}
	completed := waitEvent(t, events, driver.EventCompleted)
	if completed.Result.FinishReason != driver.FinishCancelled {
		t.Fatalf("completed = %+v", completed)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestResumeUsesCapabilityAndKeepsRequestedID(t *testing.T) {
	d := newWithTransport(TraeX, scriptedFactory(t, func(msg map[string]json.RawMessage, send func(any)) {
		switch rawString(msg["method"]) {
		case "initialize":
			send(response(msg["id"], map[string]any{
				"protocolVersion": 1,
				"agentCapabilities": map[string]any{
					"sessionCapabilities": map[string]any{"resume": map[string]any{}},
				},
			}))
		case "session/resume":
			var params struct {
				SessionID string `json:"sessionId"`
			}
			_ = json.Unmarshal(msg["params"], &params)
			if params.SessionID != "existing" {
				t.Errorf("resume session ID = %q", params.SessionID)
			}
			send(response(msg["id"], nil))
		}
	}))
	att, err := d.OpenSession(context.Background(), "key", testAgentSpec(), driver.OpenParams{ResumeSessionID: "existing"})
	if err != nil {
		t.Fatal(err)
	}
	_ = att.Events.Subscribe()
	if err := att.Session.Run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if att.Session.SessionID() != "existing" {
		t.Fatalf("session ID = %q", att.Session.SessionID())
	}
	if err := att.Session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestResumeFallsBackToSessionLoad(t *testing.T) {
	d := newWithTransport(Kimi, scriptedFactory(t, func(msg map[string]json.RawMessage, send func(any)) {
		switch rawString(msg["method"]) {
		case "initialize":
			send(response(msg["id"], map[string]any{
				"protocolVersion":   1,
				"agentCapabilities": map[string]any{"loadSession": true},
			}))
		case "session/load":
			send(response(msg["id"], nil))
		}
	}))
	att, err := d.OpenSession(context.Background(), "key", testAgentSpec(), driver.OpenParams{ResumeSessionID: "existing"})
	if err != nil {
		t.Fatal(err)
	}
	_ = att.Events.Subscribe()
	if err := att.Session.Run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if att.Session.SessionID() != "existing" {
		t.Fatalf("session ID = %q", att.Session.SessionID())
	}
	if err := att.Session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestToolCallUpdateIsCoalescedBeforeEmission(t *testing.T) {
	d := newWithTransport(TraeX, scriptedFactory(t, func(msg map[string]json.RawMessage, send func(any)) {
		switch rawString(msg["method"]) {
		case "initialize":
			send(response(msg["id"], map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{}}))
		case "session/new":
			send(response(msg["id"], map[string]any{"sessionId": "s1"}))
		case "session/prompt":
			send(notification("session/update", map[string]any{
				"sessionId": "s1", "update": map[string]any{
					"sessionUpdate": "tool_call", "toolCallId": "tool-1", "title": "read_file", "rawInput": map[string]any{},
				},
			}))
			send(notification("session/update", map[string]any{
				"sessionId": "s1", "update": map[string]any{
					"sessionUpdate": "tool_call_update", "toolCallId": "tool-1", "status": "completed",
					"rawInput": map[string]any{"path": "main.go"},
					"content":  []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": "package main"}}},
				},
			}))
			send(response(msg["id"], map[string]any{"stopReason": "end_turn"}))
		}
	}))
	att, err := d.OpenSession(context.Background(), "key", testAgentSpec(), driver.OpenParams{})
	if err != nil {
		t.Fatal(err)
	}
	events := att.Events.Subscribe()
	if err := att.Session.Run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	runID, err := att.Session.Prompt(context.Background(), driver.PromptReq{Text: "read"})
	if err != nil {
		t.Fatal(err)
	}
	var call, result *driver.AgentEventItem
	deadline := time.After(2 * time.Second)
	for call == nil || result == nil {
		select {
		case event := <-events:
			if event.RunID != runID || event.Type != driver.EventOutput {
				continue
			}
			switch event.Item.Kind {
			case driver.ItemToolCall:
				item := event.Item
				call = &item
			case driver.ItemToolResult:
				item := event.Item
				result = &item
			}
		case <-deadline:
			t.Fatal("timed out waiting for tool events")
		}
	}
	input, ok := call.ToolInput.(map[string]any)
	if call.Tool != "read_file" || !ok || input["path"] != "main.go" || result.Text != "package main" {
		t.Fatalf("call=%+v result=%+v", call, result)
	}
	if err := att.Session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCloseWithCancelledContextStillReleasesSession(t *testing.T) {
	d := newWithTransport(TraeX, scriptedFactory(t, func(msg map[string]json.RawMessage, send func(any)) {
		switch rawString(msg["method"]) {
		case "initialize":
			send(response(msg["id"], map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{}}))
		case "session/new":
			send(response(msg["id"], map[string]any{"sessionId": "s1"}))
		}
	}))
	att, err := d.OpenSession(context.Background(), "key", testAgentSpec(), driver.OpenParams{})
	if err != nil {
		t.Fatal(err)
	}
	_ = att.Events.Subscribe()
	if err := att.Session.Run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := att.Session.Prompt(context.Background(), driver.PromptReq{Text: "wait"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := att.Session.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Close error = %v", err)
	}
	if state := att.Session.ProcessState(); state.Phase != driver.PhaseClosed {
		t.Fatalf("state after Close = %+v", state)
	}
}

func waitEvent(t *testing.T, events <-chan driver.AgentEvent, typ driver.EventType) driver.AgentEvent {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case event := <-events:
			if event.Type == typ {
				return event
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s", typ)
		}
	}
}

type scriptedTransport struct {
	inW     *io.PipeWriter
	outScan *bufio.Scanner
	close   func()
	done    chan struct{}
}

func (t *scriptedTransport) stdin() io.Writer       { return t.inW }
func (t *scriptedTransport) stdout() *bufio.Scanner { return t.outScan }
func (t *scriptedTransport) wait() error            { <-t.done; return nil }
func (t *scriptedTransport) kill()                  { t.close() }
func (t *scriptedTransport) errText() string        { return "" }

func scriptedFactory(t *testing.T, handle func(map[string]json.RawMessage, func(any))) transportFactory {
	t.Helper()
	return func(context.Context, string, driver.AgentSpec, []string) (stdioTransport, error) {
		inR, inW := io.Pipe()
		outR, outW := io.Pipe()
		done := make(chan struct{})
		var once sync.Once
		closeAll := func() {
			once.Do(func() {
				_ = inR.Close()
				_ = inW.Close()
				_ = outR.Close()
				_ = outW.Close()
			})
		}
		go func() {
			defer close(done)
			scanner := bufio.NewScanner(inR)
			for scanner.Scan() {
				var msg map[string]json.RawMessage
				if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
					t.Errorf("decode request: %v", err)
					continue
				}
				handle(msg, func(value any) {
					payload, err := json.Marshal(value)
					if err != nil {
						t.Errorf("encode response: %v", err)
						return
					}
					_, _ = outW.Write(append(payload, '\n'))
				})
			}
		}()
		outScan := bufio.NewScanner(outR)
		outScan.Buffer(make([]byte, 64*1024), 8*1024*1024)
		return &scriptedTransport{inW: inW, outScan: outScan, close: closeAll, done: done}, nil
	}
}

func response(id json.RawMessage, result any) map[string]any {
	var decodedID any
	_ = json.Unmarshal(id, &decodedID)
	return map[string]any{"jsonrpc": "2.0", "id": decodedID, "result": result}
}

func notification(method string, params any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
}

func rawString(raw json.RawMessage) string {
	var value string
	_ = json.Unmarshal(raw, &value)
	return value
}
