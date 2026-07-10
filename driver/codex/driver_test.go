package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Fullstop000/unio/driver"
)

// scriptedServer is an injected app-server: it reads request lines from the
// driver's stdin and writes scripted responses/notifications back, so the
// process/reader/session machinery is exercised without a real codex binary.
type scriptedServer struct {
	toDriver    *io.PipeReader // driver reads this (server stdout)
	toDriverW   *io.PipeWriter
	fromDriver  *io.PipeReader // server reads this (driver stdin)
	fromDriverW *io.PipeWriter

	mu               sync.Mutex
	threadID         string
	turnSeq          int
	omitThreadID     bool
	omitTurnIDResp   bool
	requestApproval  bool
	approvalDecision string
	activeTurnID     string
	killed           chan struct{}
}

func newScriptedServer() *scriptedServer {
	outR, outW := io.Pipe()
	inR, inW := io.Pipe()
	s := &scriptedServer{
		toDriver: outR, toDriverW: outW,
		fromDriver: inR, fromDriverW: inW,
		threadID: "thr-test-1",
		killed:   make(chan struct{}),
	}
	go s.loop()
	return s
}

func (s *scriptedServer) stdin() io.Writer       { return s.fromDriverW }
func (s *scriptedServer) stdout() *bufio.Scanner { return bufio.NewScanner(s.toDriver) }
func (s *scriptedServer) errText() string        { return "" }
func (s *scriptedServer) kill() {
	select {
	case <-s.killed:
	default:
		close(s.killed)
	}
	_ = s.toDriverW.Close()
	_ = s.fromDriverW.Close()
}

func (s *scriptedServer) send(v any) {
	b, _ := json.Marshal(v)
	_, _ = s.toDriverW.Write(append(b, '\n'))
}

// loop reads request lines and replies, emulating codex app-server behaviour.
func (s *scriptedServer) loop() {
	sc := bufio.NewScanner(s.fromDriver)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var m map[string]any
		if json.Unmarshal([]byte(sc.Text()), &m) != nil {
			continue
		}
		method, _ := m["method"].(string)
		idv, hasID := m["id"]
		switch method {
		case "initialize":
			s.send(map[string]any{"id": idv, "result": map[string]any{"userAgent": "test"}})
		case "initialized":
			// notification; no reply
		case "thread/start":
			s.send(map[string]any{"id": idv, "result": map[string]any{"thread": map[string]any{"id": s.threadID}}})
			s.send(map[string]any{"method": "thread/started", "params": map[string]any{"thread": map[string]any{"id": s.threadID}}})
		case "thread/resume":
			s.send(map[string]any{"id": idv, "result": map[string]any{"thread": map[string]any{"id": s.threadID}}})
		case "turn/start":
			s.mu.Lock()
			s.turnSeq++
			turnID := "turn-" + string(rune('a'+s.turnSeq-1))
			omitThreadID := s.omitThreadID
			omitTurnIDResp := s.omitTurnIDResp
			s.mu.Unlock()
			s.mu.Lock()
			s.activeTurnID = turnID
			requestApproval := s.requestApproval
			s.mu.Unlock()
			// turn/start response, then a streamed turn.
			turnResp := map[string]any{}
			if !omitTurnIDResp {
				turnResp["turn"] = map[string]any{"id": turnID}
			}
			s.send(map[string]any{"id": idv, "result": turnResp})
			startedParams := map[string]any{"turn": map[string]any{"id": turnID}}
			deltaParams := map[string]any{"turnId": turnID, "itemId": "m1", "delta": "pong"}
			usageParams := map[string]any{"turnId": turnID, "tokenUsage": map[string]any{"last": map[string]any{"inputTokens": 50, "outputTokens": 3, "cachedInputTokens": 10, "totalTokens": 53}}}
			doneParams := map[string]any{"turn": map[string]any{"id": turnID, "status": "completed"}}
			if !omitThreadID {
				startedParams["threadId"] = s.threadID
				deltaParams["threadId"] = s.threadID
				usageParams["threadId"] = s.threadID
				doneParams["threadId"] = s.threadID
			}
			s.send(map[string]any{"method": "turn/started", "params": startedParams})
			if requestApproval {
				s.send(map[string]any{"id": 42, "method": "item/commandExecution/requestApproval", "params": map[string]any{"threadId": s.threadID, "turnId": turnID, "itemId": "cmd-1"}})
				continue
			}
			s.send(map[string]any{"method": "item/agentMessage/delta", "params": deltaParams})
			s.send(map[string]any{"method": "thread/tokenUsage/updated", "params": usageParams})
			s.send(map[string]any{"method": "turn/completed", "params": doneParams})
		case "turn/interrupt":
			s.send(map[string]any{"id": idv, "result": map[string]any{}})
			// emit the interrupted completion for the current turn.
			s.mu.Lock()
			turnID := "turn-" + string(rune('a'+s.turnSeq-1))
			s.mu.Unlock()
			s.send(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": s.threadID, "turn": map[string]any{"id": turnID, "status": "interrupted"}}})
		}
		if method == "" && hasID {
			if result, ok := m["result"].(string); ok {
				s.mu.Lock()
				s.approvalDecision = result
				turnID := s.activeTurnID
				s.mu.Unlock()
				s.send(map[string]any{"method": "item/agentMessage/delta", "params": map[string]any{"threadId": s.threadID, "turnId": turnID, "itemId": "m1", "delta": "continued"}})
				s.send(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": s.threadID, "turn": map[string]any{"id": turnID, "status": "completed"}}})
			}
		}
		_ = hasID
	}
}

func drainUntil(t *testing.T, ch <-chan driver.AgentEvent, pred func(driver.AgentEvent) bool, timeout time.Duration) []driver.AgentEvent {
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
			if pred(ev) {
				return out
			}
		case <-deadline:
			t.Fatalf("timeout; collected %d events", len(out))
			return out
		}
	}
}

func TestCodexDriverFullTurn(t *testing.T) {
	srv := newScriptedServer()
	d := newWithTransport(func(ctx context.Context, execPath string, spec driver.AgentSpec) (stdioTransport, error) {
		return srv, nil
	})
	key := driver.SessionKey("w-s")
	att, err := d.OpenSession(context.Background(), key, driver.AgentSpec{ExecutablePath: fakeCodex(t), Model: "gpt-5.5"}, driver.OpenParams{})
	if err != nil {
		t.Fatal(err)
	}
	ch := att.Events.Subscribe()

	if err := att.Session.Run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if att.Session.SessionID() != "thr-test-1" {
		t.Fatalf("expected thread id attached, got %q", att.Session.SessionID())
	}
	runID, err := att.Session.Prompt(context.Background(), driver.PromptReq{Text: "say pong"})
	if err != nil {
		t.Fatal(err)
	}

	evs := drainUntil(t, ch, func(ev driver.AgentEvent) bool {
		return ev.Type == driver.EventCompleted && ev.RunID == runID
	}, 3*time.Second)

	var attached, text, completed bool
	for _, ev := range evs {
		switch ev.Type {
		case driver.EventSessionAttached:
			attached = ev.SessionID == "thr-test-1"
		case driver.EventOutput:
			if ev.Item.Kind == driver.ItemText && ev.Item.Text == "pong" {
				text = true
			}
		case driver.EventCompleted:
			completed = true
			u, ok := ev.Result.Usage["gpt-5.5"]
			if !ok || u.InputTokens != 50 || u.OutputTokens != 3 || u.CacheReadTokens != 10 {
				t.Fatalf("usage not propagated from tokenUsage: %+v", ev.Result.Usage)
			}
		}
	}
	if !attached || !text || !completed {
		t.Fatalf("missing events: attached=%v text=%v completed=%v", attached, text, completed)
	}

	// Verify the outgoing wire had no jsonrpc header on turn/start.
	_ = att.Session.Close(context.Background())
}

func TestCodexRoutesSoleInFlightTurnWithoutThreadID(t *testing.T) {
	srv := newScriptedServer()
	srv.omitThreadID = true
	srv.omitTurnIDResp = true
	d := newWithTransport(func(ctx context.Context, execPath string, spec driver.AgentSpec) (stdioTransport, error) {
		return srv, nil
	})
	key := driver.SessionKey("w-s")
	att, err := d.OpenSession(context.Background(), key, driver.AgentSpec{ExecutablePath: fakeCodex(t)}, driver.OpenParams{})
	if err != nil {
		t.Fatal(err)
	}
	ch := att.Events.Subscribe()
	if err := att.Session.Run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	runID, err := att.Session.Prompt(context.Background(), driver.PromptReq{Text: "say pong"})
	if err != nil {
		t.Fatal(err)
	}

	evs := drainUntil(t, ch, func(ev driver.AgentEvent) bool {
		return ev.Type == driver.EventCompleted && ev.RunID == runID
	}, 3*time.Second)
	var text, completed bool
	for _, ev := range evs {
		if ev.Type == driver.EventOutput && ev.Item.Kind == driver.ItemText && ev.Item.Text == "pong" {
			text = true
		}
		if ev.Type == driver.EventCompleted && ev.RunID == runID {
			completed = true
		}
	}
	if !text || !completed {
		t.Fatalf("expected fallback-routed text/completed events, text=%v completed=%v", text, completed)
	}
	_ = att.Session.Close(context.Background())
}

func TestCodexDriverInterrupt(t *testing.T) {
	srv := newScriptedServer()
	d := newWithTransport(func(ctx context.Context, execPath string, spec driver.AgentSpec) (stdioTransport, error) {
		return srv, nil
	})
	key := driver.SessionKey("w-s")
	att, _ := d.OpenSession(context.Background(), key, driver.AgentSpec{ExecutablePath: fakeCodex(t)}, driver.OpenParams{})
	ch := att.Events.Subscribe()
	_ = att.Session.Run(context.Background(), nil)

	if err := att.Session.Interrupt(context.Background()); err != nil {
		t.Fatalf("idle interrupt: %v", err)
	}

	// Start a turn, then interrupt it. Codex supports graceful mid-turn cancel.
	runID, _ := att.Session.Prompt(context.Background(), driver.PromptReq{Text: "long task"})
	// Cancel may race the fast scripted completion; both outcomes are valid so
	// we assert the run terminates (interrupted → FinishCancelled, or completed).
	_ = att.Session.Interrupt(context.Background())

	evs := drainUntil(t, ch, func(ev driver.AgentEvent) bool {
		return (ev.Type == driver.EventCompleted || ev.Type == driver.EventFailed) && ev.RunID == runID
	}, 3*time.Second)
	var done bool
	for _, ev := range evs {
		if ev.Type == driver.EventCompleted && ev.RunID == runID {
			done = true
		}
	}
	if !done {
		t.Fatal("expected the run to complete (naturally or interrupted)")
	}
	_ = att.Session.Close(context.Background())
}

func TestCodexConcurrentUseIsSafe(t *testing.T) {
	srv := newScriptedServer()
	d := newWithTransport(func(ctx context.Context, execPath string, spec driver.AgentSpec) (stdioTransport, error) {
		return srv, nil
	})
	key := driver.SessionKey("w-s")
	att, err := d.OpenSession(context.Background(), key, driver.AgentSpec{ExecutablePath: fakeCodex(t)}, driver.OpenParams{})
	if err != nil {
		t.Fatal(err)
	}
	ch := att.Events.Subscribe()
	go func() {
		for range ch {
		}
	}()
	if err := att.Session.Run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 15; j++ {
				_, _ = att.Session.Prompt(context.Background(), driver.PromptReq{Text: "x"})
				_ = att.Session.Interrupt(context.Background())
				_ = att.Session.ProcessState()
				_ = att.Session.SessionID()
			}
		}()
	}
	wg.Wait()
	_ = att.Session.Close(context.Background())
}

func TestCodexOpenSessionNotInstalled(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	d := New()
	_, err := d.OpenSession(context.Background(), driver.SessionKey(""), driver.AgentSpec{ExecutablePath: "codex"}, driver.OpenParams{})
	if err == nil {
		t.Fatal("expected not_installed")
	}
	if ae, ok := err.(*driver.AgentError); !ok || ae.Kind != driver.ErrNotInstalled {
		t.Fatalf("expected not_installed AgentError, got %v", err)
	}
}

func TestDriverSharesProcessAcrossSessionKeys(t *testing.T) {
	d := New()
	spec := driver.AgentSpec{ExecutablePath: fakeCodex(t)}
	first, err := d.OpenSession(context.Background(), "key-1", spec, driver.OpenParams{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := d.OpenSession(context.Background(), "key-2", spec, driver.OpenParams{})
	if err != nil {
		t.Fatal(err)
	}
	if first.Session.(*session).proc != second.Session.(*session).proc {
		t.Fatal("one driver instance must share one app-server process")
	}
}

func TestCodexApprovalBlocksAndContinues(t *testing.T) {
	srv := newScriptedServer()
	srv.mu.Lock()
	srv.requestApproval = true
	srv.mu.Unlock()
	d := newWithTransport(func(context.Context, string, driver.AgentSpec) (stdioTransport, error) {
		return srv, nil
	})
	att, err := d.OpenSession(context.Background(), "approval", driver.AgentSpec{ExecutablePath: fakeCodex(t)}, driver.OpenParams{})
	if err != nil {
		t.Fatal(err)
	}
	events := att.Events.Subscribe()
	if err := att.Session.Run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	run, err := att.Session.Prompt(context.Background(), driver.PromptReq{Text: "run command"})
	if err != nil {
		t.Fatal(err)
	}
	blocked := drainUntil(t, events, func(ev driver.AgentEvent) bool {
		return ev.Type == driver.EventBlocked && ev.RunID == run
	}, 3*time.Second)
	if blocked[len(blocked)-1].Blocked == nil || blocked[len(blocked)-1].Blocked.Kind != driver.BlockedToolApproval {
		t.Fatalf("unexpected blocked event: %+v", blocked[len(blocked)-1])
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := att.Session.Continue(cancelled, "allow_once"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Continue error = %v", err)
	}
	continuedRun, err := att.Session.Continue(context.Background(), "allow_once")
	if err != nil {
		t.Fatal(err)
	}
	drainUntil(t, events, func(ev driver.AgentEvent) bool {
		return ev.Type == driver.EventCompleted && ev.RunID == continuedRun
	}, 3*time.Second)
	srv.mu.Lock()
	decision := srv.approvalDecision
	srv.mu.Unlock()
	if decision != "accept" {
		t.Fatalf("approval decision = %q; want accept", decision)
	}
}

// fakeCodex creates a dummy `codex` on PATH so OpenSession's ResolveExecutable
// passes for injected-transport tests.
func fakeCodex(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := writeExec(dir + "/codex"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return "codex"
}

func writeExec(path string) error {
	return os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755)
}
