package codex

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Fullstop000/unio/driver"
	errcontract "github.com/Fullstop000/unio/errs"
)

// version reported to the app-server in initialize.
const clientVersion = "0.1.0"

// Driver implements driver.ProtocolDriver for Codex app-server. One shared child
// per agent key multiplexes threads; the registry caches it and evicts on death.
type Driver struct {
	mu      sync.Mutex
	process *process
	factory transportFactory
}

// New constructs a Codex driver using the real app-server transport.
func New() *Driver {
	return &Driver{factory: spawnProcTransport}
}

func newWithTransport(f transportFactory) *Driver {
	return &Driver{factory: f}
}

// Transport implements driver.ProtocolDriver.
func (d *Driver) Transport() driver.Transport { return driver.TransportCodexAppServer }

// Probe reports installed/authed state based on binary presence.
func (d *Driver) Probe(ctx context.Context) (driver.RuntimeProbe, error) {
	if _, err := driver.ResolveExecutable(driver.AgentSpec{ExecutablePath: "codex"}); err != nil {
		return driver.RuntimeProbe{Auth: driver.AuthNotInstalled, Transport: driver.TransportCodexAppServer}, nil
	}
	return driver.RuntimeProbe{Auth: driver.AuthAuthed, Transport: driver.TransportCodexAppServer}, nil
}

func (d *Driver) ListSessions(ctx context.Context) ([]driver.StoredSessionMeta, error) {
	return listStoredSessions(ctx)
}

// OpenSession resolves the executable early (not_installed) and builds an idle
// session bound to the shared process for this agent key.
func (d *Driver) OpenSession(ctx context.Context, key driver.SessionKey, spec driver.AgentSpec, params driver.OpenParams) (*driver.SessionAttachment, error) {
	if spec.ExecutablePath == "" {
		spec.ExecutablePath = "codex"
	}
	execPath, aerr := driver.ResolveExecutable(spec)
	if aerr != nil {
		return nil, aerr
	}

	proc := d.getProcess(execPath, spec)

	bus := driver.NewEventBus()
	s := &session{
		key:    key,
		spec:   spec,
		proc:   proc,
		resume: params.ResumeSessionID,
		bus:    bus,
	}
	s.state.Store(&driver.ProcessState{Phase: driver.PhaseIdle})
	return &driver.SessionAttachment{Session: s, Events: bus}, nil
}

func (d *Driver) getProcess(execPath string, spec driver.AgentSpec) *process {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.process == nil || d.process.IsStale() {
		d.process = newProcess(execPath, spec, d.factory, clientVersion)
	}
	d.process.acquire()
	return d.process
}

// session is one codex thread. Implements driver.Session.
type session struct {
	key    driver.SessionKey
	spec   driver.AgentSpec
	proc   *process
	resume driver.SessionID
	bus    *driver.EventBus

	threadID atomic.Pointer[string]
	turnID   atomic.Pointer[string]
	curRun   atomic.Pointer[string]
	state    atomic.Pointer[driver.ProcessState]
	// pendingUsage holds the latest thread/tokenUsage for the in-flight turn,
	// attached to the Completed event when the turn ends.
	pendingUsage atomic.Pointer[TurnTokenUsage]

	blockMu  sync.Mutex
	block    *pendingBlock
	doneMu   sync.Mutex
	turnDone chan struct{}

	// mu serialises the mutating lifecycle methods (Run/Prompt/Cancel/Close) so
	// the SDK — not the caller — guarantees a Session is safe for concurrent
	// use (SPEC §Concurrency). It is held only across brief request/ack windows,
	// never for a whole turn, so Cancel is never blocked mid-turn. Read methods
	// (SessionID/ProcessState) stay lock-free on atomics.
	mu     sync.Mutex
	closed bool
}

type pendingBlock struct {
	requestID json.RawMessage
	reason    driver.BlockedReason
}

func (s *session) Key() driver.SessionKey { return s.key }

func (s *session) SessionID() driver.SessionID {
	if p := s.threadID.Load(); p != nil {
		return *p
	}
	return ""
}

func (s *session) ProcessState() driver.ProcessState {
	if p := s.state.Load(); p != nil {
		return *p
	}
	return driver.ProcessState{Phase: driver.PhaseIdle}
}

func (s *session) setState(st driver.ProcessState) {
	s.state.Store(&st)
	s.bus.Emit(driver.LifecycleEvent(s.key, st))
}

// Run starts the shared child (if needed) then starts or resumes this thread.
func (s *session) Run(ctx context.Context, initPrompt *driver.PromptReq) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return driver.NewUnsupportedError("codex: session is closed")
	}
	s.setState(driver.ProcessState{Phase: driver.PhaseStarting})

	if err := s.proc.ensureStarted(ctx); err != nil {
		st := driver.ProcessState{Phase: driver.PhaseFailed}
		if ae, ok := err.(*driver.AgentError); ok {
			st.Err = ae
		} else {
			st.Err = driver.NewTransportError(err.Error())
		}
		s.setState(st)
		s.mu.Unlock()
		return err
	}

	threadID, err := s.startOrResumeThread(ctx)
	if err != nil {
		s.setState(driver.ProcessState{Phase: driver.PhaseFailed, Err: toAgentErr(err)})
		s.mu.Unlock()
		return err
	}

	s.threadID.Store(&threadID)
	s.proc.registerSession(threadID, s)
	s.bus.Emit(driver.SessionAttachedEvent(s.key, threadID))
	s.setState(driver.ProcessState{Phase: driver.PhaseActive, SessionID: threadID})
	s.mu.Unlock()

	if initPrompt != nil {
		if _, err := s.Prompt(ctx, *initPrompt); err != nil {
			return err
		}
	}
	return nil
}

// startOrResumeThread sends thread/resume (with liveness guard) or thread/start
// and waits for the ThreadResponse carrying the thread id.
func (s *session) startOrResumeThread(ctx context.Context) (string, error) {
	if s.resume != "" {
		if !codexThreadAlive(s.resume) {
			return "", driver.NewSessionNotFoundError(s.resume)
		}
		id := s.proc.allocID()
		ch := s.proc.registerAndSend(id, "thread/resume", BuildThreadResume(id, s.resume, s.spec.SystemPrompt))
		ev, err := waitResp(ctx, ch, s.proc.closed)
		if err != nil {
			return "", err
		}
		if ev.Type == EvError {
			return "", driver.NewRuntimeReportedError(ev.ErrMsg)
		}
		if ev.Type != EvThreadResponse || ev.ThreadID == "" {
			return "", driver.NewProtocolError("codex: thread/resume returned no thread id")
		}
		if ev.ThreadID != s.resume {
			return "", driver.NewProtocolError("codex: thread/resume changed the session id")
		}
		return ev.ThreadID, nil
	}
	id := s.proc.allocID()
	ch := s.proc.registerAndSend(id, "thread/start", BuildThreadStart(id, s.spec.Model, s.spec.Cwd, s.spec.SystemPrompt))
	ev, err := waitResp(ctx, ch, s.proc.closed)
	if err != nil {
		return "", err
	}
	if ev.Type == EvError {
		return "", driver.NewRuntimeReportedError(ev.ErrMsg)
	}
	if ev.Type != EvThreadResponse || ev.ThreadID == "" {
		return "", driver.NewProtocolError("codex: thread/start returned no thread id")
	}
	return ev.ThreadID, nil
}

// Prompt sends turn/start and marks the turn in flight; output arrives via the
// reader routing into onEvent.
func (s *session) Prompt(ctx context.Context, req driver.PromptReq) (driver.RunID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", driver.NewUnsupportedError("codex: session is closed")
	}
	threadID := s.SessionID()
	if threadID == "" {
		return "", driver.NewTransportError("codex: prompt before Run")
	}
	runID := driver.NewRunID()
	s.curRun.Store(&runID)
	s.doneMu.Lock()
	s.turnDone = make(chan struct{})
	s.doneMu.Unlock()
	s.setState(driver.ProcessState{Phase: driver.PhasePromptInFlight, SessionID: threadID, RunID: runID})

	id := s.proc.allocID()
	ch := s.proc.registerAndSend(id, "turn/start", BuildTurnStart(id, threadID, req.Text))
	ev, err := waitResp(ctx, ch, s.proc.closed)
	if err != nil {
		aerr := toAgentErr(err)
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			s.stopSubmittedTurn(ch)
		}
		s.bus.Emit(driver.FailedEvent(s.key, threadID, runID, aerr))
		return runID, aerr
	}
	if ev.Type == EvError {
		aerr := driver.NewRuntimeReportedError(ev.ErrMsg)
		s.clearSubmittedTurn()
		s.bus.Emit(driver.FailedEvent(s.key, threadID, runID, aerr))
		return runID, aerr
	}
	if ev.Type == EvTurnResponse && ev.TurnID != "" {
		s.turnID.Store(&ev.TurnID)
		s.proc.mapTurn(ev.TurnID, threadID)
	}
	return runID, nil
}

// Interrupt sends turn/interrupt for the in-flight turn. Codex supports graceful
// mid-turn interrupt (unlike Claude headless).
func (s *session) Interrupt(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.interruptLocked(ctx)
}

func (s *session) interruptLocked(ctx context.Context) error {
	if s.closed {
		return nil
	}
	tp := s.turnID.Load()
	if tp == nil || *tp == "" {
		return nil
	}
	id := s.proc.allocID()
	ch := s.proc.registerAndSend(id, "turn/interrupt", BuildTurnInterrupt(id, s.SessionID(), *tp))
	if _, err := waitResp(ctx, ch, s.proc.closed); err != nil {
		return toAgentErr(err)
	}
	s.doneMu.Lock()
	done := s.turnDone
	s.doneMu.Unlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-s.proc.closed:
		return driver.NewTransportError("codex app-server closed while interrupting")
	}
}

// stopSubmittedTurn resolves the ambiguous interval after turn/start was
// written but its acknowledgement lost the race with context cancellation.
// It prefers a normal turn interrupt; if acknowledgement/interrupt cannot be
// confirmed promptly, it tears down and reaps the shared process before
// Prompt returns, so the public session can never expose a false Idle state.
func (s *session) stopSubmittedTurn(ch chan AppServerEvent) {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	ev, err := waitResp(cleanupCtx, ch, s.proc.closed)
	if err == nil {
		if ev.Type == EvError {
			s.clearSubmittedTurn()
			cancel()
			return
		}
		if ev.Type == EvTurnResponse && ev.TurnID != "" {
			s.turnID.Store(&ev.TurnID)
			s.proc.mapTurn(ev.TurnID, s.SessionID())
		}
		turnID := s.turnID.Load()
		if s.currentRun() == "" || (turnID != nil && *turnID != "" && s.interruptLocked(cleanupCtx) == nil) {
			cancel()
			return
		}
	}
	cancel()
	s.proc.shutdown()
	<-s.proc.closed
}

func (s *session) clearSubmittedTurn() {
	s.curRun.Store(ptr(""))
	s.turnID.Store(ptr(""))
	s.finishTurnDone()
	s.setState(driver.ProcessState{Phase: driver.PhaseActive, SessionID: s.SessionID()})
}

func (s *session) Continue(ctx context.Context, input string) (driver.RunID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", driver.NewUnsupportedError("codex: session is closed")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	s.blockMu.Lock()
	block := s.block
	if block == nil {
		s.blockMu.Unlock()
		return "", driver.NewInvalidStateError("codex: no blocked turn")
	}
	decision, ok := map[string]string{
		"allow_once": "accept",
		"deny":       "decline",
		"cancel":     "cancel",
	}[input]
	if !ok {
		s.blockMu.Unlock()
		return "", driver.NewInvalidStateError("codex: invalid blocked response")
	}
	s.block = nil
	s.blockMu.Unlock()
	runID := driver.NewRunID()
	s.curRun.Store(&runID)
	s.setState(driver.ProcessState{Phase: driver.PhasePromptInFlight, SessionID: s.SessionID(), RunID: runID})
	s.proc.writeLine(BuildApprovalResponse(block.requestID, decision))
	return runID, nil
}

func (s *session) setBlocked(ev AppServerEvent, kind driver.BlockedKind, message string) {
	run := s.currentRun()
	reason := driver.BlockedReason{
		Kind: kind, Message: message,
		Options: []driver.BlockOption{
			{Value: "allow_once", Label: "Allow once"},
			{Value: "deny", Label: "Deny"},
		},
	}
	s.blockMu.Lock()
	s.block = &pendingBlock{requestID: append(json.RawMessage(nil), ev.RequestID...), reason: reason}
	s.blockMu.Unlock()
	s.bus.Emit(driver.BlockedEvent(s.key, s.SessionID(), run, reason))
	s.curRun.Store(ptr(""))
	s.setState(driver.ProcessState{Phase: driver.PhaseBlocked, SessionID: s.SessionID(), RunID: run})
}

func (s *session) finishTurnDone() {
	s.doneMu.Lock()
	done := s.turnDone
	s.turnDone = nil
	s.doneMu.Unlock()
	if done != nil {
		close(done)
	}
}

// Close unregisters the session (tearing down the shared child only when this
// was the last one) and closes the bus. Idempotent and safe against concurrent
// Run/Prompt/Cancel.
func (s *session) Close(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()

	if tid := s.SessionID(); tid != "" {
		s.proc.unregisterSession(tid)
	}
	wait := s.proc.release()
	s.setState(driver.ProcessState{Phase: driver.PhaseClosed, SessionID: s.SessionID()})
	s.bus.Close()
	if wait {
		select {
		case <-s.proc.closed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (s *session) currentRun() driver.RunID {
	if p := s.curRun.Load(); p != nil {
		return *p
	}
	return ""
}

// codexThreadAlive checks for the on-disk rollout file of a prior thread so
// resume only fires when it can succeed. Path (0.142.x):
// ~/.codex/sessions/<Y>/<M>/<D>/rollout-*-<threadID>.jsonl. We scan for any file
// whose name ends with "<threadID>.jsonl" under ~/.codex/sessions.
func codexThreadAlive(threadID string) bool {
	if threadID == "" {
		return false
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	root := filepath.Join(home, ".codex", "sessions")
	found := false
	suffix := threadID + ".jsonl"
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || found {
			return nil
		}
		if !d.IsDir() && filepathHasSuffix(path, suffix) {
			found = true
		}
		return nil
	})
	return found
}

func filepathHasSuffix(path, suffix string) bool {
	return len(path) >= len(suffix) && path[len(path)-len(suffix):] == suffix
}

func toAgentErr(err error) *driver.AgentError {
	if ae, ok := err.(*driver.AgentError); ok {
		return ae
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return errcontract.Wrap(errcontract.KindTimeout, "codex: request deadline exceeded", err)
	}
	if errors.Is(err, context.Canceled) {
		return errcontract.Wrap(errcontract.KindTransport, "codex: request canceled", err)
	}
	return driver.NewTransportError(err.Error())
}

// waitResp blocks for a response event or ctx/closed.
func waitResp(ctx context.Context, ch chan AppServerEvent, closed chan struct{}) (AppServerEvent, error) {
	select {
	case ev := <-ch:
		return ev, nil
	case <-ctx.Done():
		return AppServerEvent{}, ctx.Err()
	case <-closed:
		return AppServerEvent{}, driver.NewTransportError("codex app-server closed")
	}
}

// Compile-time interface checks.
var (
	_ driver.ProtocolDriver = (*Driver)(nil)
	_ driver.Session        = (*session)(nil)
)
