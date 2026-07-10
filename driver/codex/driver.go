package codex

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/Fullstop000/unio/driver"
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

// ListSessions is not implemented yet (would scan ~/.codex/sessions).
func (d *Driver) ListSessions(ctx context.Context) ([]driver.StoredSessionMeta, error) {
	return nil, nil
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

	// mu serialises the mutating lifecycle methods (Run/Prompt/Cancel/Close) so
	// the SDK — not the caller — guarantees a Session is safe for concurrent
	// use (SPEC §Concurrency). It is held only across brief request/ack windows,
	// never for a whole turn, so Cancel is never blocked mid-turn. Read methods
	// (SessionID/ProcessState) stay lock-free on atomics.
	mu     sync.Mutex
	closed bool
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
	if s.resume != "" && codexThreadAlive(s.resume) {
		id := s.proc.allocID()
		ch := s.proc.registerAndSend(id, "thread/resume", BuildThreadResume(id, s.resume, s.spec.SystemPrompt))
		ev, err := waitResp(ctx, ch, s.proc.closed)
		if err == nil && ev.Type == EvThreadResponse && ev.ThreadID != "" {
			return ev.ThreadID, nil
		}
		// Fall through to a fresh start if resume failed/misfired.
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
	s.setState(driver.ProcessState{Phase: driver.PhasePromptInFlight, SessionID: threadID, RunID: runID})

	id := s.proc.allocID()
	ch := s.proc.registerAndSend(id, "turn/start", BuildTurnStart(id, threadID, req.Text))
	ev, err := waitResp(ctx, ch, s.proc.closed)
	if err != nil {
		aerr := toAgentErr(err)
		s.bus.Emit(driver.FailedEvent(s.key, threadID, runID, aerr))
		return runID, aerr
	}
	if ev.Type == EvError {
		aerr := driver.NewRuntimeReportedError(ev.ErrMsg)
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
	if s.closed {
		return nil
	}
	cur := s.curRun.Load()
	if cur == nil || *cur == "" {
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
	// The turn/completed{interrupted} notification will finalise the run.
	return nil
}

// Continue is implemented with approval routing in the blocked-turn task.
func (s *session) Continue(ctx context.Context, input string) (driver.RunID, error) {
	return "", driver.NewUnsupportedError("codex: no blocked turn")
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
	s.setState(driver.ProcessState{Phase: driver.PhaseClosed, SessionID: s.SessionID()})
	s.bus.Close()
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
	return driver.NewTransportError(err.Error())
}

// waitResp blocks for a response event or ctx/closed.
func waitResp(ctx context.Context, ch chan AppServerEvent, closed chan struct{}) (AppServerEvent, error) {
	select {
	case ev := <-ch:
		return ev, nil
	case <-ctx.Done():
		return AppServerEvent{}, driver.NewTimeoutError("codex: request timed out")
	case <-closed:
		return AppServerEvent{}, driver.NewTransportError("codex app-server closed")
	}
}

// Compile-time interface checks.
var (
	_ driver.ProtocolDriver = (*Driver)(nil)
	_ driver.Session        = (*session)(nil)
)
