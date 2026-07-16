package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sync"
	"sync/atomic"
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
	omitTurnStarted  bool
	requestApproval  bool
	approvalDecision string
	activeTurnID     string
	holdTurn         bool
	turnStartSeen    chan struct{}
	turnStartGate    chan struct{}
	interruptSeen    chan struct{}
	interruptError   bool
	killed           chan struct{}
	waited           chan struct{}
}

type overlapWriter struct {
	active     atomic.Int32
	overlapped atomic.Bool
	mu         sync.Mutex
	writes     [][]byte
}

func (w *overlapWriter) Write(p []byte) (int, error) {
	if w.active.Add(1) != 1 {
		w.overlapped.Store(true)
	}
	time.Sleep(time.Millisecond)
	w.mu.Lock()
	w.writes = append(w.writes, append([]byte(nil), p...))
	w.mu.Unlock()
	w.active.Add(-1)
	return len(p), nil
}

type writerTransport struct{ w io.Writer }

func (t *writerTransport) stdin() io.Writer       { return t.w }
func (t *writerTransport) stdout() *bufio.Scanner { return bufio.NewScanner(bytes.NewReader(nil)) }
func (t *writerTransport) wait() error            { return nil }
func (t *writerTransport) kill()                  {}
func (t *writerTransport) errText() string        { return "" }

func newScriptedServer() *scriptedServer {
	outR, outW := io.Pipe()
	inR, inW := io.Pipe()
	s := &scriptedServer{
		toDriver: outR, toDriverW: outW,
		fromDriver: inR, fromDriverW: inW,
		threadID: "thr-test-1",
		killed:   make(chan struct{}),
		waited:   make(chan struct{}),
	}
	go s.loop()
	return s
}

func (s *scriptedServer) stdin() io.Writer       { return s.fromDriverW }
func (s *scriptedServer) stdout() *bufio.Scanner { return bufio.NewScanner(s.toDriver) }
func (s *scriptedServer) errText() string        { return "" }
func (s *scriptedServer) wait() error {
	<-s.killed
	close(s.waited)
	return nil
}
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
			turnStartSeen := s.turnStartSeen
			turnStartGate := s.turnStartGate
			s.mu.Unlock()
			if turnStartSeen != nil {
				close(turnStartSeen)
			}
			if turnStartGate != nil {
				<-turnStartGate
			}
			s.mu.Lock()
			s.turnSeq++
			turnID := "turn-" + string(rune('a'+s.turnSeq-1))
			omitThreadID := s.omitThreadID
			omitTurnIDResp := s.omitTurnIDResp
			omitTurnStarted := s.omitTurnStarted
			s.mu.Unlock()
			s.mu.Lock()
			s.activeTurnID = turnID
			requestApproval := s.requestApproval
			holdTurn := s.holdTurn
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
			if !omitTurnStarted {
				s.send(map[string]any{"method": "turn/started", "params": startedParams})
			}
			if requestApproval {
				s.send(map[string]any{"id": 42, "method": "item/commandExecution/requestApproval", "params": map[string]any{"threadId": s.threadID, "turnId": turnID, "itemId": "cmd-1"}})
				continue
			}
			if holdTurn {
				continue
			}
			s.send(map[string]any{"method": "item/agentMessage/delta", "params": deltaParams})
			s.send(map[string]any{"method": "thread/tokenUsage/updated", "params": usageParams})
			s.send(map[string]any{"method": "turn/completed", "params": doneParams})
		case "turn/interrupt":
			s.mu.Lock()
			interruptSeen := s.interruptSeen
			interruptError := s.interruptError
			s.mu.Unlock()
			if interruptSeen != nil {
				close(interruptSeen)
			}
			if interruptError {
				s.send(map[string]any{"id": idv, "error": map[string]any{"message": "interrupt rejected"}})
				continue
			}
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
	d := newWithTransport(context.Background(), driver.AgentSpec{ExecutablePath: fakeCodex(t), Model: "gpt-5.5"}, func(ctx context.Context, execPath string, spec driver.AgentSpec) (stdioTransport, error) {
		return srv, nil
	})
	att, err := d.OpenSession(driver.OpenParams{})
	if err != nil {
		t.Fatal(err)
	}
	ch := att.Events.Subscribe()

	if err := att.Session.Start(); err != nil {
		t.Fatal(err)
	}
	if att.Session.SessionID() != "thr-test-1" {
		t.Fatalf("expected thread id attached, got %q", att.Session.SessionID())
	}
	runID, err := att.Session.Send(driver.UserMessage{Text: "say pong"})
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
	if err := att.Session.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-srv.waited:
	case <-time.After(time.Second):
		t.Fatal("Close returned before the app-server child was reaped")
	}
}

func TestCodexRoutesSoleInFlightTurnWithoutThreadID(t *testing.T) {
	srv := newScriptedServer()
	srv.omitThreadID = true
	d := newWithTransport(context.Background(), driver.AgentSpec{ExecutablePath: fakeCodex(t)}, func(ctx context.Context, execPath string, spec driver.AgentSpec) (stdioTransport, error) {
		return srv, nil
	})
	att, err := d.OpenSession(driver.OpenParams{})
	if err != nil {
		t.Fatal(err)
	}
	ch := att.Events.Subscribe()
	if err := att.Session.Start(); err != nil {
		t.Fatal(err)
	}
	runID, err := att.Session.Send(driver.UserMessage{Text: "say pong"})
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
	_ = att.Session.Close()
}

func TestCodexDriverInterrupt(t *testing.T) {
	srv := newScriptedServer()
	d := newWithTransport(context.Background(), driver.AgentSpec{ExecutablePath: fakeCodex(t)}, func(ctx context.Context, execPath string, spec driver.AgentSpec) (stdioTransport, error) {
		return srv, nil
	})
	att, _ := d.OpenSession(driver.OpenParams{})
	ch := att.Events.Subscribe()
	_ = att.Session.Start()

	if err := att.Session.Interrupt(); err != nil {
		t.Fatalf("idle interrupt: %v", err)
	}

	// Start a turn, then interrupt it. Codex supports graceful mid-turn cancel.
	runID, _ := att.Session.Send(driver.UserMessage{Text: "long task"})
	// Cancel may race the fast scripted completion; both outcomes are valid so
	// we assert the run terminates (interrupted → FinishCancelled, or completed).
	_ = att.Session.Interrupt()

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
	_ = att.Session.Close()
}

func TestCodexConcurrentUseIsSafe(t *testing.T) {
	srv := newScriptedServer()
	d := newWithTransport(context.Background(), driver.AgentSpec{ExecutablePath: fakeCodex(t)}, func(ctx context.Context, execPath string, spec driver.AgentSpec) (stdioTransport, error) {
		return srv, nil
	})
	att, err := d.OpenSession(driver.OpenParams{})
	if err != nil {
		t.Fatal(err)
	}
	ch := att.Events.Subscribe()
	go func() {
		for range ch {
		}
	}()
	if err := att.Session.Start(); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 15; j++ {
				_, _ = att.Session.Send(driver.UserMessage{Text: "x"})
				_ = att.Session.Interrupt()
				_ = att.Session.ProcessState()
				_ = att.Session.SessionID()
			}
		}()
	}
	wg.Wait()
	_ = att.Session.Close()
}

func TestCodexOpenSessionNotInstalled(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	d := New(context.Background(), driver.AgentSpec{})
	_, err := d.OpenSession(driver.OpenParams{})
	if err == nil {
		t.Fatal("expected not_installed")
	}
	if ae, ok := err.(*driver.AgentError); !ok || ae.Kind != driver.ErrNotInstalled {
		t.Fatalf("expected not_installed AgentError, got %v", err)
	}
}

func TestDriverSharesProcessAcrossSessions(t *testing.T) {
	d := New(context.Background(), driver.AgentSpec{ExecutablePath: fakeCodex(t)})
	first, err := d.OpenSession(driver.OpenParams{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := d.OpenSession(driver.OpenParams{})
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
	d := newWithTransport(context.Background(), driver.AgentSpec{ExecutablePath: fakeCodex(t)}, func(context.Context, string, driver.AgentSpec) (stdioTransport, error) {
		return srv, nil
	})
	att, err := d.OpenSession(driver.OpenParams{})
	if err != nil {
		t.Fatal(err)
	}
	events := att.Events.Subscribe()
	if err := att.Session.Start(); err != nil {
		t.Fatal(err)
	}
	run, err := att.Session.Send(driver.UserMessage{Text: "run command"})
	if err != nil {
		t.Fatal(err)
	}
	blocked := drainUntil(t, events, func(ev driver.AgentEvent) bool {
		return ev.Type == driver.EventBlocked && ev.RunID == run
	}, 3*time.Second)
	if blocked[len(blocked)-1].Blocked == nil || blocked[len(blocked)-1].Blocked.Kind != driver.BlockedToolApproval {
		t.Fatalf("unexpected blocked event: %+v", blocked[len(blocked)-1])
	}
	continuedRun, err := att.Session.Respond(driver.OptionSelection{Value: "allow_once"})
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

func TestCodexProcessLifetimeDoesNotUseTurnContext(t *testing.T) {
	srv := newScriptedServer()
	var factoryCtx context.Context
	ctx, cancel := context.WithCancel(context.Background())
	d := newWithTransport(ctx, driver.AgentSpec{ExecutablePath: fakeCodex(t)}, func(ctx context.Context, _ string, _ driver.AgentSpec) (stdioTransport, error) {
		factoryCtx = ctx
		return srv, nil
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

func TestCodexPromptCancellationWaitsForConfirmedInterrupt(t *testing.T) {
	srv := newScriptedServer()
	srv.holdTurn = true
	srv.turnStartSeen = make(chan struct{})
	srv.turnStartGate = make(chan struct{})
	srv.interruptSeen = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	d := newWithTransport(ctx, driver.AgentSpec{ExecutablePath: fakeCodex(t)}, func(context.Context, string, driver.AgentSpec) (stdioTransport, error) { return srv, nil })
	att, err := d.OpenSession(driver.OpenParams{})
	if err != nil {
		t.Fatal(err)
	}
	defer att.Session.Close()
	if err := att.Session.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := att.Session.Send(driver.UserMessage{Text: "long"})
		done <- err
	}()
	<-srv.turnStartSeen
	cancel()
	returnedEarly := false
	select {
	case <-done:
		returnedEarly = true
	case <-time.After(20 * time.Millisecond):
	}
	close(srv.turnStartGate)
	if returnedEarly {
		t.Fatal("Send returned before the submitted turn could be interrupted")
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Send error = %v; want context.Canceled", err)
	}
	select {
	case <-srv.interruptSeen:
	case <-time.After(time.Second):
		t.Fatal("cancelled Send did not send turn/interrupt")
	}
}

func TestCodexRejectsTurnStartWithoutTurnID(t *testing.T) {
	srv := newScriptedServer()
	srv.omitTurnIDResp = true
	d := newWithTransport(context.Background(), driver.AgentSpec{ExecutablePath: fakeCodex(t)}, func(context.Context, string, driver.AgentSpec) (stdioTransport, error) { return srv, nil })
	att, err := d.OpenSession(driver.OpenParams{})
	if err != nil {
		t.Fatal(err)
	}
	defer att.Session.Close()
	if err := att.Session.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err := att.Session.Send(driver.UserMessage{Text: "hello"}); err == nil {
		t.Fatal("turn/start without a turn ID was accepted")
	}
}

func TestCodexSurfacesRejectedInterrupt(t *testing.T) {
	srv := newScriptedServer()
	srv.holdTurn = true
	srv.interruptError = true
	d := newWithTransport(context.Background(), driver.AgentSpec{ExecutablePath: fakeCodex(t)}, func(context.Context, string, driver.AgentSpec) (stdioTransport, error) { return srv, nil })
	att, err := d.OpenSession(driver.OpenParams{})
	if err != nil {
		t.Fatal(err)
	}
	defer att.Session.Close()
	if err := att.Session.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err := att.Session.Send(driver.UserMessage{Text: "long"}); err != nil {
		t.Fatal(err)
	}
	err = att.Session.Interrupt()
	if kind, ok := driverErrorKind(err); !ok || kind != driver.ErrRuntimeReported {
		t.Fatalf("interrupt error = %v; want runtime_reported", err)
	}
}

func TestCodexForcedCancellationInvalidatesIdleSibling(t *testing.T) {
	srv := newScriptedServer()
	srv.holdTurn = true
	srv.omitTurnIDResp = true
	srv.omitTurnStarted = true
	srv.turnStartSeen = make(chan struct{})
	srv.turnStartGate = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	d := newWithTransport(ctx, driver.AgentSpec{ExecutablePath: fakeCodex(t)}, func(context.Context, string, driver.AgentSpec) (stdioTransport, error) { return srv, nil })
	active, err := d.OpenSession(driver.OpenParams{})
	if err != nil {
		t.Fatal(err)
	}
	idle, err := d.OpenSession(driver.OpenParams{})
	if err != nil {
		t.Fatal(err)
	}
	defer active.Session.Close()
	defer idle.Session.Close()
	if err := active.Session.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := active.Session.Send(driver.UserMessage{Text: "long"})
		done <- err
	}()
	<-srv.turnStartSeen
	cancel()
	time.Sleep(20 * time.Millisecond)
	close(srv.turnStartGate)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Send error = %v; want context.Canceled", err)
	}
	if idle.Session.ProcessState().Phase != driver.PhaseClosed {
		t.Fatalf("idle sibling phase = %q; want closed", idle.Session.ProcessState().Phase)
	}
	select {
	case <-srv.waited:
	case <-time.After(time.Second):
		t.Fatal("forced cancellation returned before process reaping")
	}
}

func TestProcessLastReleaseSerializesWithAcquire(t *testing.T) {
	for i := 0; i < 1000; i++ {
		p := newProcess("codex", driver.AgentSpec{}, nil, "test")
		first := &session{}
		second := &session{}
		if !p.acquire(first) {
			t.Fatal("initial acquire failed")
		}
		start := make(chan struct{})
		acquired := make(chan bool, 1)
		go func() {
			<-start
			acquired <- p.acquire(second)
		}()
		close(start)
		p.release(first)
		if ok := <-acquired; ok {
			if p.IsStale() {
				t.Fatal("process became stale after accepting a concurrent attachment")
			}
			p.release(second)
		}
	}
}

func TestProcessSerializesCompleteJSONLines(t *testing.T) {
	w := &overlapWriter{}
	p := newProcess("codex", driver.AgentSpec{}, nil, "test")
	p.tr = &writerTransport{w: w}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := p.writeLine(`{"method":"turn/start","params":{"text":"payload"}}`); err != nil {
				t.Errorf("writeLine: %v", err)
			}
		}()
	}
	wg.Wait()
	if w.overlapped.Load() {
		t.Fatal("concurrent JSONL writes overlapped")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.writes) != 50 {
		t.Fatalf("writes = %d; want 50", len(w.writes))
	}
	for _, line := range w.writes {
		if len(line) == 0 || line[len(line)-1] != '\n' {
			t.Fatalf("incomplete JSONL write: %q", line)
		}
	}
}

func TestTransportClosedStateIsMonotonic(t *testing.T) {
	for i := 0; i < 1000; i++ {
		p := newProcess("codex", driver.AgentSpec{}, nil, "test")
		s := &session{ctx: context.Background(), proc: p, bus: driver.NewEventBus()}
		s.state.Store(&driver.ProcessState{Phase: driver.PhaseIdle})
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			s.setState(driver.ProcessState{Phase: driver.PhasePromptInFlight})
		}()
		go func() {
			defer wg.Done()
			<-start
			s.onTransportClosed()
		}()
		close(start)
		wg.Wait()
		if s.ProcessState().Phase != driver.PhaseClosed {
			t.Fatalf("terminal state overwritten by %q", s.ProcessState().Phase)
		}
		s.bus.Close()
	}
}

func TestWaitRespPreservesContextCause(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := waitResp(ctx, make(chan AppServerEvent), make(chan struct{}))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v", err)
	}

	ctx, cancel = context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()
	_, err = waitResp(ctx, make(chan AppServerEvent), make(chan struct{}))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline error = %v", err)
	}
	if !errors.Is(toAgentErr(context.Canceled), context.Canceled) {
		t.Fatal("toAgentErr lost context.Canceled")
	}
	if !errors.Is(toAgentErr(context.DeadlineExceeded), context.DeadlineExceeded) {
		t.Fatal("toAgentErr lost context.DeadlineExceeded")
	}
}

func TestCodexMissingResumeDoesNotStartFresh(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	srv := newScriptedServer()
	d := newWithTransport(context.Background(), driver.AgentSpec{ExecutablePath: fakeCodex(t)}, func(context.Context, string, driver.AgentSpec) (stdioTransport, error) { return srv, nil })
	att, err := d.OpenSession(driver.OpenParams{ResumeSessionID: "missing"})
	if err != nil {
		t.Fatal(err)
	}
	err = att.Session.Start()
	if kind, ok := driverErrorKind(err); !ok || kind != driver.ErrSessionNotFound {
		t.Fatalf("resume error = %v", err)
	}
	if err := att.Session.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-srv.waited:
	case <-time.After(time.Second):
		t.Fatal("failed pre-registration session did not reap its app-server")
	}
}

func driverErrorKind(err error) (driver.ErrorKind, bool) {
	var agentErr *driver.AgentError
	if !errors.As(err, &agentErr) || agentErr == nil {
		return "", false
	}
	return agentErr.Kind, true
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
