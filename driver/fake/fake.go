// Package fake provides an in-memory ProtocolDriver/Session implementation that
// spawns no process. It exists to prove the driver abstraction end-to-end and to
// serve as a test double for hosts: a session can be scripted to emit an
// arbitrary sequence of events per prompt, and resume is modelled by
// pre-assigning a session id.
package fake

import (
	"context"
	"fmt"
	"path/filepath"
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
	Blocked  *driver.BlockedReason
	Wait     <-chan struct{}
	// InterruptWait delays interrupt confirmation for state-ordering tests.
	InterruptWait <-chan struct{}
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
func (d *Driver) ListSessions(ctx context.Context, params driver.ListSessionsParams) ([]driver.StoredSessionMeta, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]driver.StoredSessionMeta, 0, len(d.stored))
	for _, meta := range d.stored {
		if params.Cwd == "" || filepath.Clean(meta.Cwd) == filepath.Clean(params.Cwd) {
			out = append(out, meta)
		}
	}
	return out, nil
}

func (d *Driver) NewSessionData(ctx context.Context, _ driver.AgentSpec, _ driver.SessionID) *driver.SessionData {
	return driver.NewSessionData(ctx, nil, nil)
}

// OpenSession implements driver.ProtocolDriver.
func (d *Driver) OpenSession(ctx context.Context, key driver.SessionKey, spec driver.AgentSpec, params driver.OpenParams) (*driver.SessionAttachment, error) {
	d.mu.Lock()
	require := d.requireInstall
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
		driver: d,
		key:    key,
		bus:    bus,
		resume: params.ResumeSessionID,
	}
	s.state.Store(&driver.ProcessState{Phase: driver.PhaseIdle})
	return &driver.SessionAttachment{Session: s, Events: bus}, nil
}

func (d *Driver) script(key driver.SessionKey, index int) (Script, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	scripts := d.scripts[key]
	if index >= len(scripts) {
		return Script{}, false
	}
	return scripts[index], true
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
	scriptIdx int
	closed    bool
	active    *turn
	blocked   *driver.BlockedReason

	state atomic.Pointer[driver.ProcessState]
}

type turn struct {
	runID       driver.RunID
	script      Script
	stop        chan struct{}
	interrupted bool
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
	defer s.mu.Unlock()
	if s.closed {
		return "", driver.NewUnsupportedError("fake: session is closed")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	sid := s.sessionID
	if sid == "" {
		return "", driver.NewProtocolError("fake: Prompt before Run")
	}
	if s.active != nil || s.blocked != nil {
		return "", driver.NewInvalidStateError("fake: session is not idle")
	}
	script, scripted := s.nextScriptLocked()
	if !scripted {
		script = Script{
			Items:  []driver.AgentEventItem{{Kind: driver.ItemText, Text: "echo: " + req.Text}},
			Result: driver.RunResult{FinishReason: driver.FinishNatural},
		}
	}
	return s.startScriptLocked(script), nil
}

func (s *session) nextScriptLocked() (Script, bool) {
	script, ok := s.driver.script(s.key, s.scriptIdx)
	if ok {
		s.scriptIdx++
	}
	return script, ok
}

func (s *session) startScriptLocked(script Script) driver.RunID {
	runID := driver.NewRunID()
	t := &turn{runID: runID, script: script, stop: make(chan struct{})}
	s.active = t
	s.setState(driver.ProcessState{Phase: driver.PhasePromptInFlight, SessionID: s.sessionID, RunID: runID})
	go s.execute(t)
	return runID
}

func (s *session) execute(t *turn) {
	if t.script.Wait != nil {
		select {
		case <-t.script.Wait:
		case <-t.stop:
			return
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.active != t || t.interrupted {
		return
	}
	for _, item := range t.script.Items {
		s.bus.Emit(driver.OutputEvent(s.key, s.sessionID, t.runID, item))
	}
	if t.script.Blocked != nil {
		reason := *t.script.Blocked
		s.active = nil
		s.blocked = &reason
		s.bus.Emit(driver.BlockedEvent(s.key, s.sessionID, t.runID, reason))
		s.setState(driver.ProcessState{Phase: driver.PhaseBlocked, SessionID: s.sessionID, RunID: t.runID})
		return
	}
	s.bus.Emit(driver.OutputEvent(s.key, s.sessionID, t.runID, driver.AgentEventItem{Kind: driver.ItemTurnEnd}))
	if t.script.FailWith != nil {
		s.bus.Emit(driver.FailedEvent(s.key, s.sessionID, t.runID, t.script.FailWith))
	} else {
		result := t.script.Result
		if result.FinishReason == "" {
			result.FinishReason = driver.FinishNatural
		}
		s.bus.Emit(driver.CompletedEvent(s.key, s.sessionID, t.runID, result))
	}
	s.active = nil
	s.setState(driver.ProcessState{Phase: driver.PhaseActive, SessionID: s.sessionID})
}

func (s *session) Continue(ctx context.Context, input string) (driver.RunID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", driver.NewUnsupportedError("fake: session is closed")
	}
	if s.blocked == nil {
		return "", driver.NewInvalidStateError("fake: no blocked turn")
	}
	if len(s.blocked.Options) > 0 {
		valid := false
		for _, option := range s.blocked.Options {
			if option.Value == input {
				valid = true
				break
			}
		}
		if !valid {
			return "", driver.NewInvalidStateError("fake: invalid blocked response")
		}
	}
	s.blocked = nil
	script, scripted := s.nextScriptLocked()
	if !scripted {
		script = Script{Items: []driver.AgentEventItem{{Kind: driver.ItemText, Text: "echo: " + input}}}
	}
	return s.startScriptLocked(script), nil
}

// Interrupt is an idempotent no-op unless a run is currently in flight.
func (s *session) Interrupt(ctx context.Context) error {
	s.mu.Lock()
	if s.blocked != nil {
		s.blocked = nil
		s.setState(driver.ProcessState{Phase: driver.PhaseActive, SessionID: s.sessionID})
		s.mu.Unlock()
		return nil
	}
	t := s.active
	if t == nil {
		s.mu.Unlock()
		return nil
	}
	wait := t.script.InterruptWait
	s.mu.Unlock()
	if wait != nil {
		select {
		case <-wait:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active != t {
		return nil
	}
	t.interrupted = true
	close(t.stop)
	s.active = nil
	s.bus.Emit(driver.OutputEvent(s.key, s.sessionID, t.runID, driver.AgentEventItem{Kind: driver.ItemTurnEnd}))
	s.bus.Emit(driver.CompletedEvent(s.key, s.sessionID, t.runID, driver.RunResult{FinishReason: driver.FinishCancelled}))
	s.setState(driver.ProcessState{Phase: driver.PhaseActive, SessionID: s.sessionID})
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
