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
const clientVersion = "0.2.0"

// Driver implements driver.Driver for Codex app-server. One child process
// multiplexes all sessions owned by the Agent.
type Driver struct {
	ctx     context.Context
	spec    driver.AgentSpec
	mu      sync.Mutex
	process *process
	factory transportFactory
}

// New constructs a Codex driver using the real app-server transport.
func New(ctx context.Context, spec driver.AgentSpec) *Driver {
	return &Driver{ctx: ctx, spec: spec, factory: spawnProcTransport}
}

func newWithTransport(ctx context.Context, spec driver.AgentSpec, f transportFactory) *Driver {
	return &Driver{ctx: ctx, spec: spec, factory: f}
}

// Probe reports installed/authed state based on binary presence.
func (d *Driver) Probe() (driver.ProbeAuth, error) {
	spec := d.spec
	if spec.ExecutablePath == "" {
		spec.ExecutablePath = "codex"
	}
	if _, err := driver.ResolveExecutable(spec); err != nil {
		return driver.AuthNotInstalled, nil
	}
	return driver.AuthAuthed, nil
}

func (d *Driver) ListSessions(params driver.ListSessionsParams) ([]driver.StoredSessionMeta, error) {
	return listStoredSessions(d.ctx, params.Cwd)
}

// OpenSession resolves the executable early (not_installed) and builds an idle
// session bound to the Agent's shared process.
func (d *Driver) OpenSession(params driver.OpenParams) (*driver.SessionAttachment, error) {
	spec := d.spec
	if params.Cwd != "" {
		spec.Cwd = params.Cwd
	}
	if spec.ExecutablePath == "" {
		spec.ExecutablePath = "codex"
	}
	execPath, aerr := driver.ResolveExecutable(spec)
	if aerr != nil {
		return nil, aerr
	}

	bus := driver.NewEventBus()
	d.mu.Lock()
	if d.process == nil || d.process.IsStale() {
		d.process = newProcess(execPath, spec, d.factory, clientVersion)
	}
	proc := d.process
	s := &session{ctx: d.ctx, spec: spec, proc: proc, resume: params.ResumeSessionID, bus: bus}
	if !proc.acquire(s) {
		proc = newProcess(execPath, spec, d.factory, clientVersion)
		d.process = proc
		s.proc = proc
		_ = proc.acquire(s)
	}
	d.mu.Unlock()
	s.state.Store(&driver.ProcessState{Phase: driver.PhaseIdle})
	return &driver.SessionAttachment{Session: s, Events: bus}, nil
}

// session is one codex thread. Implements driver.Session.
type session struct {
	ctx    context.Context
	spec   driver.AgentSpec
	proc   *process
	resume driver.SessionID
	bus    *driver.EventBus

	threadID        atomic.Pointer[string]
	turnID          atomic.Pointer[string]
	curRun          atomic.Pointer[string]
	state           atomic.Pointer[driver.ProcessState]
	transportClosed atomic.Bool
	stateMu         sync.Mutex
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
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if st.Phase != driver.PhaseClosed && (s.transportClosed.Load() || s.proc.IsStale()) {
		st = driver.ProcessState{Phase: driver.PhaseClosed, SessionID: s.SessionID()}
	}
	s.state.Store(&st)
	s.bus.Emit(driver.LifecycleEvent(st))
}

func (s *session) transportUnavailable() bool {
	return s.transportClosed.Load() || s.proc.IsStale()
}

// Run starts the shared child (if needed) then starts or resumes this thread.
func (s *session) Run(initPrompt *driver.PromptReq) error {
	ctx := s.ctx
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return driver.NewUnsupportedError("codex: session is closed")
	}
	if s.transportUnavailable() {
		s.mu.Unlock()
		return driver.NewTransportError("codex app-server is closed")
	}
	s.setState(driver.ProcessState{Phase: driver.PhaseStarting})

	if err := s.proc.ensureStarted(ctx); err != nil {
		st := driver.ProcessState{Phase: driver.PhaseFailed}
		if ae, ok := err.(*driver.AgentError); ok {
			st.Err = ae
		} else {
			st.Err = driver.NewTransportError(err.Error())
		}
		if s.transportUnavailable() {
			s.setState(driver.ProcessState{Phase: driver.PhaseClosed, SessionID: s.SessionID()})
		} else {
			s.setState(st)
		}
		s.mu.Unlock()
		return err
	}

	threadID, err := s.startOrResumeThread(ctx)
	if err != nil {
		s.setState(driver.ProcessState{Phase: driver.PhaseFailed, Err: toAgentErr(err)})
		s.mu.Unlock()
		return err
	}
	if s.transportUnavailable() {
		s.setState(driver.ProcessState{Phase: driver.PhaseClosed, SessionID: s.SessionID()})
		s.mu.Unlock()
		return driver.NewTransportError("codex app-server closed during thread start")
	}

	s.threadID.Store(&threadID)
	s.proc.registerSession(threadID, s)
	s.bus.Emit(driver.SessionAttachedEvent(threadID))
	s.setState(driver.ProcessState{Phase: driver.PhaseActive, SessionID: threadID})
	s.mu.Unlock()

	if initPrompt != nil {
		if _, err := s.Prompt(*initPrompt); err != nil {
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
		ch, err := s.proc.registerAndSend(id, "thread/resume", BuildThreadResume(id, s.resume, s.spec.SystemPrompt))
		if err != nil {
			return "", err
		}
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
	ch, err := s.proc.registerAndSend(id, "thread/start", BuildThreadStart(id, s.spec.Model, s.spec.Cwd, s.spec.SystemPrompt))
	if err != nil {
		return "", err
	}
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
func (s *session) Prompt(req driver.PromptReq) (driver.RunID, error) {
	ctx := s.ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", driver.NewUnsupportedError("codex: session is closed")
	}
	if s.transportUnavailable() {
		return "", driver.NewTransportError("codex app-server is closed")
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
	if s.transportUnavailable() {
		s.clearSubmittedTurn()
		return runID, driver.NewTransportError("codex app-server closed before prompt submission")
	}

	id := s.proc.allocID()
	ch, sendErr := s.proc.registerAndSend(id, "turn/start", BuildTurnStart(id, threadID, req.Text))
	if sendErr != nil {
		s.clearSubmittedTurn()
		return runID, sendErr
	}
	ev, err := waitResp(ctx, ch, s.proc.closed)
	if err != nil {
		aerr := toAgentErr(err)
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			s.stopSubmittedTurn(ch)
		}
		s.bus.Emit(driver.FailedEvent(threadID, runID, aerr))
		return runID, aerr
	}
	if ev.Type == EvError {
		aerr := driver.NewRuntimeReportedError(ev.ErrMsg)
		s.clearSubmittedTurn()
		s.bus.Emit(driver.FailedEvent(threadID, runID, aerr))
		return runID, aerr
	}
	if ev.Type != EvTurnResponse || ev.TurnID == "" {
		aerr := driver.NewProtocolError("codex: turn/start returned no turn id")
		s.proc.shutdown()
		<-s.proc.closed
		s.clearSubmittedTurn()
		s.bus.Emit(driver.FailedEvent(threadID, runID, aerr))
		return runID, aerr
	}
	s.turnID.Store(&ev.TurnID)
	s.proc.mapTurn(ev.TurnID, threadID)
	return runID, nil
}

// Interrupt sends turn/interrupt for the in-flight turn. Codex supports graceful
// mid-turn interrupt (unlike Claude headless).
func (s *session) Interrupt() error {
	ctx := s.ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.interruptLocked(ctx)
}

func (s *session) interruptLocked(ctx context.Context) error {
	if s.closed {
		return nil
	}
	if s.transportUnavailable() {
		return driver.NewTransportError("codex app-server is closed")
	}
	tp := s.turnID.Load()
	if tp == nil || *tp == "" {
		return nil
	}
	id := s.proc.allocID()
	ch, err := s.proc.registerAndSend(id, "turn/interrupt", BuildTurnInterrupt(id, s.SessionID(), *tp))
	if err != nil {
		return err
	}
	ev, err := waitResp(ctx, ch, s.proc.closed)
	if err != nil {
		return toAgentErr(err)
	}
	if ev.Type == EvError {
		return driver.NewRuntimeReportedError(ev.ErrMsg)
	}
	if ev.Type != EvTurnInterruptResponse {
		return driver.NewProtocolError("codex: unexpected turn/interrupt response")
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

func (s *session) Continue(input string) (driver.RunID, error) {
	ctx := s.ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", driver.NewUnsupportedError("codex: session is closed")
	}
	if s.transportUnavailable() {
		return "", driver.NewTransportError("codex app-server is closed")
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
	if s.transportUnavailable() {
		s.curRun.Store(ptr(""))
		return runID, driver.NewTransportError("codex app-server closed before continue")
	}
	if err := s.proc.writeLine(BuildApprovalResponse(block.requestID, decision)); err != nil {
		s.proc.shutdown()
		s.curRun.Store(ptr(""))
		s.setState(driver.ProcessState{Phase: driver.PhaseClosed, SessionID: s.SessionID()})
		return runID, err
	}
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
	s.bus.Emit(driver.BlockedEvent(s.SessionID(), run, reason))
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
func (s *session) Close() error {
	ctx := context.WithoutCancel(s.ctx)
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
	wait := s.proc.release(s)
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
	_ driver.Driver  = (*Driver)(nil)
	_ driver.Session = (*session)(nil)
)
