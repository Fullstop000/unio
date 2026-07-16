package claude

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Fullstop000/unio/driver"
)

// scriptedTransport is an injected transport that feeds pre-written stdout lines
// through the real handle/reader loop, so the driver's session logic is tested
// without a real `claude` process. Stdin writes are captured for assertions.
type scriptedTransport struct {
	lines      []string
	pr         *io.PipeReader
	pw         *io.PipeWriter
	mu         sync.Mutex
	written    []string
	closed     chan struct{}
	waited     chan struct{}
	waitGate   chan struct{}
	shortWrite bool
}

func newScriptedTransport(lines []string) *scriptedTransport {
	pr, pw := io.Pipe()
	return &scriptedTransport{
		lines: lines, pr: pr, pw: pw,
		closed: make(chan struct{}), waited: make(chan struct{}),
	}
}

func (s *scriptedTransport) stdin() io.Writer { return &captureWriter{s: s} }

func (s *scriptedTransport) stdout() *bufio.Scanner {
	return bufio.NewScanner(s.pr)
}

func (s *scriptedTransport) wait() error {
	<-s.closed
	if s.waitGate != nil {
		<-s.waitGate
	}
	close(s.waited)
	return nil
}

func (s *scriptedTransport) kill() {
	select {
	case <-s.closed:
	default:
		close(s.closed)
	}
	_ = s.pw.Close()
}

// feed writes the scripted lines to the reader, then keeps the pipe open so the
// session stays "alive" until Close.
func (s *scriptedTransport) feed() {
	for _, l := range s.lines {
		if _, err := io.WriteString(s.pw, l+"\n"); err != nil {
			return
		}
	}
}

type captureWriter struct{ s *scriptedTransport }

func (w *captureWriter) Write(p []byte) (int, error) {
	w.s.mu.Lock()
	w.s.written = append(w.s.written, string(p))
	w.s.mu.Unlock()
	if w.s.shortWrite && len(p) > 0 {
		return len(p) - 1, nil
	}
	return len(p), nil
}

func collect(t *testing.T, ch <-chan driver.AgentEvent, until func(driver.AgentEvent) bool, timeout time.Duration) []driver.AgentEvent {
	t.Helper()
	var out []driver.AgentEvent
	deadline := time.After(timeout)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, ev)
			if until(ev) {
				return out
			}
		case <-deadline:
			t.Fatalf("timed out; collected %d events", len(out))
			return out
		}
	}
}

func TestClaudeDriverFullTurn(t *testing.T) {
	lines := []string{
		`{"type":"system","subtype":"init","session_id":"sess-abc","model":"claude-sonnet-4-6"}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"planning"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"Read"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"main.go\"}"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_stop","index":1}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"done"}}}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"done","stop_reason":"end_turn","session_id":"sess-abc","duration_ms":42,"total_cost_usd":0.005,"usage":{"input_tokens":100,"output_tokens":20}}`,
	}
	tr := newScriptedTransport(lines)

	d := newWithTransport(context.Background(), driver.AgentSpec{ExecutablePath: fakeInstalledBinary(t), Model: "claude-sonnet-4-6"}, func(ctx context.Context, execPath string, args []string, spec driver.AgentSpec) (transport, error) {
		return tr, nil
	})
	// Bypass the PATH check for the injected transport.
	att, err := d.OpenSession(driver.OpenParams{})
	if err != nil {
		t.Fatal(err)
	}
	ch := att.Events.Subscribe()

	if err := att.Session.Start(); err != nil {
		t.Fatal(err)
	}
	runID, err := att.Session.Send(driver.UserMessage{Text: "read main.go"})
	if err != nil {
		t.Fatal(err)
	}

	// Feed the scripted stdout now that the turn is in flight.
	go tr.feed()

	evs := collect(t, ch, func(ev driver.AgentEvent) bool {
		return ev.Type == driver.EventCompleted && ev.RunID == runID
	}, 3*time.Second)

	var attached, thinking, toolCall, text, completed bool
	for _, ev := range evs {
		switch ev.Type {
		case driver.EventSessionAttached:
			if ev.SessionID == "sess-abc" {
				attached = true
			}
		case driver.EventOutput:
			switch ev.Item.Kind {
			case driver.ItemThinking:
				thinking = true
			case driver.ItemToolCall:
				toolCall = true
				// The coalesced tool input must be whole JSON, not fragments.
				m, ok := ev.Item.ToolInput.(map[string]any)
				if !ok || m["path"] != "main.go" || ev.Item.Tool != "Read" {
					t.Fatalf("tool call not coalesced correctly: %+v", ev.Item)
				}
			case driver.ItemText:
				text = true
			}
		case driver.EventCompleted:
			completed = true
			u, ok := ev.Result.Usage["claude-sonnet-4-6"]
			if !ok || u.InputTokens != 100 || u.OutputTokens != 20 {
				t.Fatalf("usage not propagated: %+v", ev.Result.Usage)
			}
			if u.CostUSD < 0.0049 || u.CostUSD > 0.0051 {
				t.Fatalf("cost not propagated: %v", u.CostUSD)
			}
		}
	}
	if !attached || !thinking || !toolCall || !text || !completed {
		t.Fatalf("missing events: attached=%v thinking=%v tool=%v text=%v completed=%v",
			attached, thinking, toolCall, text, completed)
	}

	// The user message must have been written to stdin as a JSON line.
	tr.mu.Lock()
	joined := strings.Join(tr.written, "")
	tr.mu.Unlock()
	if !strings.Contains(joined, `"content":"read main.go"`) {
		t.Fatalf("stdin should carry the user message, got %q", joined)
	}

	_ = att.Session.Close()
}

func TestClaudeDriverErrorResult(t *testing.T) {
	lines := []string{
		`{"type":"system","subtype":"init","session_id":"sess-err"}`,
		`{"type":"result","subtype":"error","is_error":true,"result":"context limit","stop_reason":"error","session_id":"sess-err"}`,
	}
	tr := newScriptedTransport(lines)
	d := newWithTransport(context.Background(), driver.AgentSpec{ExecutablePath: fakeInstalledBinary(t)}, func(ctx context.Context, execPath string, args []string, spec driver.AgentSpec) (transport, error) {
		return tr, nil
	})
	att, err := d.OpenSession(driver.OpenParams{})
	if err != nil {
		t.Fatal(err)
	}
	ch := att.Events.Subscribe()
	_ = att.Session.Start()
	runID, _ := att.Session.Send(driver.UserMessage{Text: "x"})
	go tr.feed()

	evs := collect(t, ch, func(ev driver.AgentEvent) bool {
		return (ev.Type == driver.EventFailed || ev.Type == driver.EventCompleted) && ev.RunID == runID
	}, 3*time.Second)

	var failed bool
	for _, ev := range evs {
		if ev.Type == driver.EventFailed && ev.Err != nil && ev.Err.Kind == driver.ErrRuntimeReported {
			failed = true
		}
	}
	if !failed {
		t.Fatal("expected a runtime_reported Failed event")
	}
	_ = att.Session.Close()
}

func TestClaudeInterruptKillsTurnAsCancelled(t *testing.T) {
	tr := newScriptedTransport([]string{
		`{"type":"system","subtype":"init","session_id":"sess-interrupt"}`,
	})
	d := newWithTransport(context.Background(), driver.AgentSpec{ExecutablePath: fakeInstalledBinary(t)}, func(context.Context, string, []string, driver.AgentSpec) (transport, error) {
		return tr, nil
	})
	att, err := d.OpenSession(driver.OpenParams{})
	if err != nil {
		t.Fatal(err)
	}
	events := att.Events.Subscribe()
	if err := att.Session.Start(); err != nil {
		t.Fatal(err)
	}
	run, err := att.Session.Send(driver.UserMessage{Text: "long"})
	if err != nil {
		t.Fatal(err)
	}
	go tr.feed()
	collect(t, events, func(ev driver.AgentEvent) bool {
		return ev.Type == driver.EventSessionAttached
	}, 2*time.Second)
	if err := att.Session.Interrupt(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-tr.waited:
	case <-time.After(time.Second):
		t.Fatal("Interrupt returned before the child was reaped")
	}
	evs := collect(t, events, func(ev driver.AgentEvent) bool {
		return ev.Type == driver.EventCompleted && ev.RunID == run
	}, 2*time.Second)
	last := evs[len(evs)-1]
	if last.Result.FinishReason != driver.FinishCancelled {
		t.Fatalf("finish = %q; want cancelled", last.Result.FinishReason)
	}
}

func TestClaudePromptRejectsShortWrite(t *testing.T) {
	tr := newScriptedTransport(nil)
	tr.shortWrite = true
	d := newWithTransport(context.Background(), driver.AgentSpec{ExecutablePath: fakeInstalledBinary(t)}, func(context.Context, string, []string, driver.AgentSpec) (transport, error) { return tr, nil })
	att, err := d.OpenSession(driver.OpenParams{})
	if err != nil {
		t.Fatal(err)
	}
	if err := att.Session.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err := att.Session.Send(driver.UserMessage{Text: "hello"}); err == nil {
		t.Fatal("short JSONL write was accepted")
	}
}

func TestClaudeCloseWaitsForReapAfterLifecycleCancellation(t *testing.T) {
	tr := newScriptedTransport(nil)
	tr.waitGate = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	d := newWithTransport(ctx, driver.AgentSpec{ExecutablePath: fakeInstalledBinary(t)}, func(context.Context, string, []string, driver.AgentSpec) (transport, error) { return tr, nil })
	att, err := d.OpenSession(driver.OpenParams{})
	if err != nil {
		t.Fatal(err)
	}
	if err := att.Session.Start(); err != nil {
		t.Fatal(err)
	}
	cancel()
	done := make(chan error, 1)
	go func() { done <- att.Session.Close() }()
	select {
	case err := <-done:
		t.Fatalf("Close returned before reaping: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(tr.waitGate)
	if err := <-done; err != nil {
		t.Fatalf("Close after reaping: %v", err)
	}
}

func TestClaudeProcessLifetimeDoesNotUseTurnContext(t *testing.T) {
	tr := newScriptedTransport(nil)
	var factoryCtx context.Context
	ctx, cancel := context.WithCancel(context.Background())
	d := newWithTransport(ctx, driver.AgentSpec{ExecutablePath: fakeInstalledBinary(t)}, func(ctx context.Context, _ string, _ []string, _ driver.AgentSpec) (transport, error) {
		factoryCtx = ctx
		return tr, nil
	})
	att, err := d.OpenSession(driver.OpenParams{})
	if err != nil {
		t.Fatal(err)
	}
	if err := att.Session.Start(); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := factoryCtx.Err(); err != nil {
		t.Fatalf("process context was cancelled with turn context: %v", err)
	}
	_ = att.Session.Close()
}

func TestClaudeMissingResumeDoesNotStartFresh(t *testing.T) {
	original := fileExists
	fileExists = func(string) bool { return false }
	t.Cleanup(func() { fileExists = original })
	d := New(context.Background(), driver.AgentSpec{ExecutablePath: fakeInstalledBinary(t), Cwd: "/repo"})
	_, err := d.OpenSession(driver.OpenParams{ResumeSessionID: "missing"})
	var agentErr *driver.AgentError
	if !errors.As(err, &agentErr) || agentErr.Kind != driver.ErrSessionNotFound {
		t.Fatalf("resume error = %v", err)
	}
}

func TestClaudeDriverNonStreamingCompleteMessage(t *testing.T) {
	// Environment that does NOT emit stream_event deltas — the whole turn's
	// content arrives in one `assistant` message (e.g. a proxy CLI). The driver
	// must still emit text + tool-call Output items from the complete message.
	lines := []string{
		`{"type":"system","subtype":"init","session_id":"sess-ns"}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Hi!"},{"type":"tool_use","id":"toolu_1","name":"Read","input":{"path":"go.mod"}}]},"session_id":"sess-ns"}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"Hi!","stop_reason":"end_turn","session_id":"sess-ns","duration_ms":10,"total_cost_usd":0.08}`,
	}
	tr := newScriptedTransport(lines)
	d := newWithTransport(context.Background(), driver.AgentSpec{ExecutablePath: fakeInstalledBinary(t)}, func(ctx context.Context, execPath string, args []string, spec driver.AgentSpec) (transport, error) {
		return tr, nil
	})
	att, err := d.OpenSession(driver.OpenParams{})
	if err != nil {
		t.Fatal(err)
	}
	ch := att.Events.Subscribe()
	_ = att.Session.Start()
	runID, _ := att.Session.Send(driver.UserMessage{Text: "hi"})
	go tr.feed()

	evs := collect(t, ch, func(ev driver.AgentEvent) bool {
		return ev.Type == driver.EventCompleted && ev.RunID == runID
	}, 3*time.Second)

	var text, toolCall bool
	for _, ev := range evs {
		if ev.Type == driver.EventOutput {
			switch ev.Item.Kind {
			case driver.ItemText:
				if ev.Item.Text == "Hi!" {
					text = true
				}
			case driver.ItemToolCall:
				m, ok := ev.Item.ToolInput.(map[string]any)
				if ok && ev.Item.Tool == "Read" && m["path"] == "go.mod" {
					toolCall = true
				}
			}
		}
	}
	if !text || !toolCall {
		t.Fatalf("non-streaming path should emit text+tool from complete message: text=%v tool=%v", text, toolCall)
	}
	_ = att.Session.Close()
}

func TestClaudeArgsKeepExplicitResumeID(t *testing.T) {
	origExists := fileExists
	fileExists = func(string) bool { return false }
	defer func() { fileExists = origExists }()

	h := &handle{spec: driver.AgentSpec{Cwd: "/tmp/x"}, resume: "prior-id"}
	args := h.buildArgs()
	if !containsArg(args, "--resume") || !containsArg(args, "prior-id") {
		t.Fatalf("explicit resume ID must never silently become a fresh session: %v", args)
	}
}

func TestClaudeProbeNotInstalled(t *testing.T) {
	origHome := homeDir
	defer func() { homeDir = origHome }()
	// Point PATH at an empty dir so `claude` won't resolve.
	t.Setenv("PATH", t.TempDir())

	d := New(context.Background(), driver.AgentSpec{})
	pr, err := d.Probe()
	if err != nil {
		t.Fatal(err)
	}
	if pr != driver.AuthNotInstalled {
		t.Fatalf("expected not-installed probe, got %s", pr)
	}
}

func TestClaudeOpenSessionNotInstalled(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	d := New(context.Background(), driver.AgentSpec{})
	_, err := d.OpenSession(driver.OpenParams{})
	if err == nil {
		t.Fatal("OpenSession should fail not-installed when claude is absent")
	}
	if ae, ok := err.(*driver.AgentError); !ok || ae.Kind != driver.ErrNotInstalled {
		t.Fatalf("expected not_installed AgentError, got %v", err)
	}
}

// fakeInstalledBinary creates a dummy executable on PATH so OpenSession's
// ResolveExecutable check passes for the injected-transport tests.
func fakeInstalledBinary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	name := "claude"
	path := dir + "/" + name
	if err := writeExecutable(path); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return name
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func writeExecutable(path string) error {
	return os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755)
}
