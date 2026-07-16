// Package fake provides an in-memory Driver/Session implementation that
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

// Script describes the events a single Send or Respond should emit, in order, before the
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

// Driver is an in-memory Driver. It mints monotonic session ids and
// hands each opened session an optional queue of Scripts consumed per submission.
type Driver struct {
	ctx            context.Context
	spec           driver.AgentSpec
	mu             sync.Mutex
	seq            atomic.Uint64
	stored         []driver.StoredSessionMeta
	scripts        [][]Script
	probe          driver.ProbeAuth
	probeErr       error
	requireInstall bool
}

// New constructs a fake driver reporting an installed+authed probe by default.
func New(ctx context.Context, spec driver.AgentSpec) *Driver {
	return &Driver{
		ctx: ctx, spec: spec, probe: driver.AuthAuthed,
	}
}

// SetProbe overrides what Probe returns (test knob).
func (d *Driver) SetProbe(p driver.ProbeAuth, err error) {
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

// ScriptNextSession sets the Scripts that the next opened session will replay.
func (d *Driver) ScriptNextSession(scripts ...Script) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.scripts = append(d.scripts, append([]Script(nil), scripts...))
}

// Probe implements driver.Driver.
func (d *Driver) Probe() (driver.ProbeAuth, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.probe, d.probeErr
}

// ListSessions implements driver.Driver.
func (d *Driver) ListSessions(params driver.ListSessionsParams) ([]driver.StoredSessionMeta, error) {
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

// OpenSession implements driver.Driver.
func (d *Driver) OpenSession(params driver.OpenParams) (*driver.SessionAttachment, error) {
	d.mu.Lock()
	require := d.requireInstall
	var scripts []Script
	if len(d.scripts) > 0 {
		scripts = d.scripts[0]
		d.scripts = d.scripts[1:]
	}
	d.mu.Unlock()

	// Real drivers always do this at OpenSession; the fake does it only when
	// asked, so the not_installed contract can be exercised without a process.
	if require {
		if _, err := driver.ResolveExecutable(d.spec); err != nil {
			return nil, err
		}
	}

	bus := driver.NewEventBus()
	s := &session{
		ctx: d.ctx, driver: d, bus: bus, resume: params.ResumeSessionID, scripts: scripts,
	}
	s.state.Store(&driver.ProcessState{Phase: driver.PhaseIdle})
	return &driver.SessionAttachment{Session: s, Events: bus}, nil
}

func (d *Driver) nextSessionID() driver.SessionID {
	return fmt.Sprintf("fake-session-%d", d.seq.Add(1))
}

// session implements driver.Session in memory.
type session struct {
	ctx     context.Context
	driver  *Driver
	bus     *driver.EventBus
	resume  driver.SessionID
	scripts []Script

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

func (s *session) Raw() (driver.RawSessionData, error) {
	return driver.RawSessionData{}, driver.NewUnsupportedError("fake: raw session data are not supported")
}

func (s *session) TokenStatistics() (driver.TokenUsage, error) {
	return driver.TokenUsage{}, driver.NewUnsupportedError("fake: session token statistics are not supported")
}

func (s *session) setState(st driver.ProcessState) {
	s.state.Store(&st)
	s.bus.Emit(driver.LifecycleEvent(st))
}

// Start brings the session online: resume reuses the pre-assigned id, otherwise
// a fresh one is minted.
func (s *session) Start() error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
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
	s.bus.Emit(driver.SessionAttachedEvent(sid))
	s.setState(driver.ProcessState{Phase: driver.PhaseActive, SessionID: sid})

	return nil
}

// Send replays the next scripted response (or a trivial echo when unscripted).
func (s *session) Send(input driver.UserInput) (driver.RunID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", driver.NewUnsupportedError("fake: session is closed")
	}
	if err := s.ctx.Err(); err != nil {
		return "", err
	}
	sid := s.sessionID
	if sid == "" {
		return "", driver.NewProtocolError("fake: Send before Start")
	}
	if s.active != nil || s.blocked != nil {
		return "", driver.NewInvalidStateError("fake: session is not idle")
	}
	message, ok := input.(driver.UserMessage)
	if !ok {
		return "", driver.NewInvalidStateError("fake: a new turn requires UserMessage")
	}
	script, scripted := s.nextScriptLocked()
	if !scripted {
		script = Script{
			Items:  []driver.AgentEventItem{{Kind: driver.ItemText, Text: "echo: " + message.Text}},
			Result: driver.RunResult{FinishReason: driver.FinishNatural},
		}
	}
	return s.startScriptLocked(script), nil
}

func (s *session) nextScriptLocked() (Script, bool) {
	if s.scriptIdx >= len(s.scripts) {
		return Script{}, false
	}
	script := s.scripts[s.scriptIdx]
	s.scriptIdx++
	return script, true
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
		s.bus.Emit(driver.OutputEvent(s.sessionID, t.runID, item))
	}
	if t.script.Blocked != nil {
		reason := *t.script.Blocked
		s.active = nil
		s.blocked = &reason
		s.setState(driver.ProcessState{Phase: driver.PhaseBlocked, SessionID: s.sessionID, RunID: t.runID})
		s.bus.Emit(driver.BlockedEvent(s.sessionID, t.runID, reason))
		return
	}
	s.bus.Emit(driver.OutputEvent(s.sessionID, t.runID, driver.AgentEventItem{Kind: driver.ItemTurnEnd}))
	if t.script.FailWith != nil {
		s.bus.Emit(driver.FailedEvent(s.sessionID, t.runID, t.script.FailWith))
	} else {
		result := t.script.Result
		if result.FinishReason == "" {
			result.FinishReason = driver.FinishNatural
		}
		s.bus.Emit(driver.CompletedEvent(s.sessionID, t.runID, result))
	}
	s.active = nil
	s.setState(driver.ProcessState{Phase: driver.PhaseActive, SessionID: s.sessionID})
}

func (s *session) Respond(input driver.UserInput) (driver.RunID, error) {
	if err := s.ctx.Err(); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", driver.NewUnsupportedError("fake: session is closed")
	}
	if s.blocked == nil {
		return "", driver.NewInvalidStateError("fake: no blocked turn")
	}
	responseText := ""
	if len(s.blocked.Options) > 0 {
		selection, ok := input.(driver.OptionSelection)
		if !ok {
			return "", driver.NewInvalidStateError("fake: blocked options require OptionSelection")
		}
		valid := false
		for _, option := range s.blocked.Options {
			if option.Value == selection.Value {
				valid = true
				break
			}
		}
		if !valid {
			return "", driver.NewInvalidStateError("fake: invalid blocked response")
		}
		responseText = selection.Value
	} else {
		message, ok := input.(driver.UserMessage)
		if !ok {
			return "", driver.NewInvalidStateError("fake: blocked user input requires UserMessage")
		}
		responseText = message.Text
	}
	s.blocked = nil
	script, scripted := s.nextScriptLocked()
	if !scripted {
		script = Script{Items: []driver.AgentEventItem{{Kind: driver.ItemText, Text: "echo: " + responseText}}}
	}
	return s.startScriptLocked(script), nil
}

// Interrupt is an idempotent no-op unless a run is currently in flight.
func (s *session) Interrupt() error {
	ctx := s.ctx
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
	s.bus.Emit(driver.OutputEvent(s.sessionID, t.runID, driver.AgentEventItem{Kind: driver.ItemTurnEnd}))
	s.bus.Emit(driver.CompletedEvent(s.sessionID, t.runID, driver.RunResult{FinishReason: driver.FinishCancelled}))
	s.setState(driver.ProcessState{Phase: driver.PhaseActive, SessionID: s.sessionID})
	return nil
}

// Close moves to PhaseClosed and closes the event bus. Idempotent.
func (s *session) Close() error {
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
	_ driver.Driver  = (*Driver)(nil)
	_ driver.Session = (*session)(nil)
)
