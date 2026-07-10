// Package fake provides an in-memory ProtocolDriver/Session implementation that
// spawns no process. It exists to prove the driver abstraction end-to-end and to
// serve as a test double for hosts: a session can be scripted to emit an
// arbitrary sequence of events per prompt, and resume is modelled by
// pre-assigning a session id.
package fake

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/Fullstop000/unio/driver"
)

// Script describes the events a single Prompt should emit, in order, before the
// terminal Completed/Failed. If FailWith is non-nil the run ends with a Failed
// event carrying it; otherwise it ends Completed with Result.
type Script struct {
	Items    []driver.AgentEventItem
	Result   driver.RunResult
	FailWith *driver.AgentError
}

// Driver is an in-memory ProtocolDriver. It mints monotonic session ids and
// hands each opened session an optional queue of Scripts consumed one-per-Prompt.
type Driver struct {
	mu             sync.Mutex
	seq            atomic.Uint64
	stored         []driver.StoredSessionMeta
	scripts        map[driver.SessionKey][]Script
	probe          driver.RuntimeProbe
	probeErr       error
	requireInstall bool
}

// New constructs a fake driver reporting an installed+authed probe by default.
func New() *Driver {
	return &Driver{
		scripts: make(map[driver.SessionKey][]Script),
		probe: driver.RuntimeProbe{
			Auth:      driver.AuthAuthed,
			Transport: driver.TransportFake,
		},
	}
}

// SetProbe overrides what Probe returns (test knob).
func (d *Driver) SetProbe(p driver.RuntimeProbe, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.probe = p
	d.probeErr = err
}

// SetRequireInstall makes OpenSession run driver.ResolveExecutable on the spec
// (like a real driver), so tests can exercise the not_installed path. Off by
// default because the fake normally spawns no process.
func (d *Driver) SetRequireInstall(on bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.requireInstall = on
}

// SetStoredSessions sets what ListSessions returns (test knob).
func (d *Driver) SetStoredSessions(metas []driver.StoredSessionMeta) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stored = metas
}

// ScriptSession queues the Scripts a session (identified by key) will replay,
// one per Prompt call, in order.
func (d *Driver) ScriptSession(key driver.SessionKey, scripts ...Script) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.scripts[key] = append(d.scripts[key], scripts...)
}

// Transport implements driver.ProtocolDriver.
func (d *Driver) Transport() driver.Transport { return driver.TransportFake }

// Probe implements driver.ProtocolDriver.
func (d *Driver) Probe(ctx context.Context) (driver.RuntimeProbe, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.probe, d.probeErr
}

// ListSessions implements driver.ProtocolDriver.
func (d *Driver) ListSessions(ctx context.Context) ([]driver.StoredSessionMeta, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]driver.StoredSessionMeta, len(d.stored))
	copy(out, d.stored)
	return out, nil
}

// OpenSession implements driver.ProtocolDriver.
func (d *Driver) OpenSession(ctx context.Context, key driver.SessionKey, spec driver.AgentSpec, params driver.OpenParams) (*driver.SessionAttachment, error) {
	d.mu.Lock()
	require := d.requireInstall
	scripts := d.scripts[key]
	d.mu.Unlock()

	// Real drivers always do this at OpenSession; the fake does it only when
	// asked, so the not_installed contract can be exercised without a process.
	if require {
		if _, err := driver.ResolveExecutable(spec); err != nil {
			return nil, err
		}
	}

	bus := driver.NewEventBus()
	s := &session{
		driver:  d,
		key:     key,
		bus:     bus,
		scripts: scripts,
		resume:  params.ResumeSessionID,
	}
	s.state.Store(&driver.ProcessState{Phase: driver.PhaseIdle})
	return &driver.SessionAttachment{Session: s, Events: bus}, nil
}

func (d *Driver) nextSessionID() driver.SessionID {
	return fmt.Sprintf("fake-session-%d", d.seq.Add(1))
}

// session implements driver.Session in memory.
type session struct {
	driver *Driver
	key    driver.SessionKey
	bus    *driver.EventBus
	resume driver.SessionID

	mu        sync.Mutex
	sessionID driver.SessionID
	scripts   []Script
	scriptIdx int
	closed    bool

	state atomic.Pointer[driver.ProcessState]
}

func (s *session) Key() driver.SessionKey { return s.key }

func (s *session) SessionID() driver.SessionID {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessionID
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

// Run brings the session online: resume reuses the pre-assigned id, otherwise a
// fresh one is minted. An init prompt, if given, is delivered as the first turn.
func (s *session) Run(ctx context.Context, initPrompt *driver.PromptReq) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return driver.NewUnsupportedError("fake: session is closed")
	}
	if s.resume != "" {
		s.sessionID = s.resume
	} else {
		s.sessionID = s.driver.nextSessionID()
	}
	sid := s.sessionID
	s.mu.Unlock()

	s.setState(driver.ProcessState{Phase: driver.PhaseStarting})
	s.bus.Emit(driver.SessionAttachedEvent(s.key, sid))
	s.setState(driver.ProcessState{Phase: driver.PhaseActive, SessionID: sid})

	if initPrompt != nil {
		if _, err := s.Prompt(ctx, *initPrompt); err != nil {
			return err
		}
	}
	return nil
}

// Prompt replays the next scripted response (or a trivial echo when unscripted).
func (s *session) Prompt(ctx context.Context, req driver.PromptReq) (driver.RunID, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return "", driver.NewUnsupportedError("fake: session is closed")
	}
	sid := s.sessionID
	if sid == "" {
		s.mu.Unlock()
		return "", driver.NewProtocolError("fake: Prompt before Run")
	}
	var script Script
	scripted := false
	if s.scriptIdx < len(s.scripts) {
		script = s.scripts[s.scriptIdx]
		s.scriptIdx++
		scripted = true
	}
	s.mu.Unlock()

	runID := driver.NewRunID()
	s.setState(driver.ProcessState{Phase: driver.PhasePromptInFlight, SessionID: sid, RunID: runID})

	if !scripted {
		// Default behaviour: echo the prompt text back as one text item.
		script = Script{
			Items: []driver.AgentEventItem{
				{Kind: driver.ItemText, Text: "echo: " + req.Text},
			},
			Result: driver.RunResult{FinishReason: driver.FinishNatural},
		}
	}

	for _, item := range script.Items {
		select {
		case <-ctx.Done():
			s.setState(driver.ProcessState{Phase: driver.PhaseActive, SessionID: sid})
			return runID, ctx.Err()
		default:
		}
		s.bus.Emit(driver.OutputEvent(s.key, sid, runID, item))
	}
	// Always cap the stream with a turn-end item.
	s.bus.Emit(driver.OutputEvent(s.key, sid, runID, driver.AgentEventItem{Kind: driver.ItemTurnEnd}))

	if script.FailWith != nil {
		s.bus.Emit(driver.FailedEvent(s.key, sid, runID, script.FailWith))
		s.setState(driver.ProcessState{Phase: driver.PhaseActive, SessionID: sid})
		return runID, nil
	}

	result := script.Result
	if result.FinishReason == "" {
		result.FinishReason = driver.FinishNatural
	}
	s.bus.Emit(driver.CompletedEvent(s.key, sid, runID, result))
	s.setState(driver.ProcessState{Phase: driver.PhaseActive, SessionID: sid})
	return runID, nil
}

// Continue is implemented once scripted blocking support is configured.
func (s *session) Continue(ctx context.Context, input string) (driver.RunID, error) {
	return "", driver.NewUnsupportedError("fake: no blocked turn")
}

// Interrupt is an idempotent no-op unless a run is currently in flight.
func (s *session) Interrupt(ctx context.Context) error {
	st := s.ProcessState()
	if st.Phase != driver.PhasePromptInFlight {
		return nil
	}
	s.setState(driver.ProcessState{Phase: driver.PhaseActive, SessionID: st.SessionID})
	return nil
}

// Close moves to PhaseClosed and closes the event bus. Idempotent.
func (s *session) Close(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	s.setState(driver.ProcessState{Phase: driver.PhaseClosed, SessionID: s.SessionID()})
	s.bus.Close()
	return nil
}

// Compile-time interface checks.
var (
	_ driver.ProtocolDriver = (*Driver)(nil)
	_ driver.Session        = (*session)(nil)
)
