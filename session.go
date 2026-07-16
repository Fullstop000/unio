package unio

import (
	"sync"

	"github.com/Fullstop000/unio/driver"
	"github.com/Fullstop000/unio/errs"
)

// Session is one conversation with an Agent. One turn may run at a time; use
// separate Sessions for concurrent turns.
type Session struct {
	agent *Agent
	// opMu serializes whole-turn operations. Lock order is opMu then mu.
	opMu sync.Mutex

	mu      sync.Mutex
	state   SessionState
	id      string
	cwd     string // immutable after the handle is published
	inner   driver.Session
	events  <-chan driver.AgentEvent
	started bool // true once inner.Start has successfully initialized this attachment
	active  *Stream
}

func newSession(agent *Agent, id, cwd string) *Session {
	return &Session{agent: agent, id: id, cwd: cwd, state: Idle}
}

// ID returns the runtime-owned session ID. A new session has no runtime ID
// until its first Run or Stream starts, so ID returns "" before then. A session
// returned by GetSession has its known ID immediately.
func (s *Session) ID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.id
}

// State returns Idle, Running, or Blocked.
func (s *Session) State() SessionState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// Raw returns the runtime-owned persisted representation of this session. It
// may contain prompts, source, tool data, command output, paths, and secrets.
// The session must have a runtime ID and must not have an active turn.
func (s *Session) Raw() (RawSessionData, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	inner, err := s.dataSession()
	if err != nil {
		return RawSessionData{}, err
	}
	return inner.Raw()
}

func (s *Session) dataSession() (driver.Session, error) {
	if err := s.agent.ctx.Err(); err != nil {
		return nil, err
	}
	if s.agent.closed.Load() {
		return nil, errs.InvalidState("agent is closed")
	}
	s.mu.Lock()
	state := s.state
	id := s.id
	s.mu.Unlock()
	if state != Idle {
		return nil, errs.InvalidState("session data requires an idle session")
	}
	if id == "" {
		return nil, errs.InvalidState("session has no runtime ID")
	}
	if err := s.ensureHandle(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inner, nil
}

// TokenStatistics returns cumulative token usage recorded for this session.
// The session must have a runtime ID and must not have an active turn.
func (s *Session) TokenStatistics() (TokenStatistics, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	inner, err := s.dataSession()
	if err != nil {
		return TokenStatistics{}, err
	}
	usage, err := inner.TokenStatistics()
	if err != nil {
		return TokenStatistics{}, err
	}
	return TokenStatistics{
		InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens,
		CacheReadTokens: usage.CacheReadTokens, CacheWriteTokens: usage.CacheWriteTokens,
		CostUSD: usage.CostUSD,
	}, nil
}

// Run submits caller input and waits for completion, interruption, or blocking.
// An idle Session starts a new turn; a blocked Session answers its pending
// interaction.
func (s *Session) Run(input UserInput) (Result, error) {
	stream, err := s.Stream(input)
	if err != nil {
		return Result{}, err
	}
	return stream.Result()
}

// Stream submits caller input and returns its live event stream. Session state
// selects whether this starts a new turn or answers a blocked interaction.
func (s *Session) Stream(input UserInput) (*Stream, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.mu.Lock()
	if s.agent.closed.Load() {
		s.mu.Unlock()
		return nil, errs.InvalidState("agent is closed")
	}
	state := s.state
	if state == Running {
		s.mu.Unlock()
		return nil, errs.InvalidState("cannot submit input while " + string(state))
	}
	if state != Idle && state != Blocked {
		s.mu.Unlock()
		return nil, errs.InvalidState("cannot submit input while " + string(state))
	}
	if state == Idle {
		if _, ok := input.(UserMessage); !ok {
			s.mu.Unlock()
			return nil, errs.InvalidState("an idle session requires UserMessage")
		}
	}
	if state == Blocked && (s.inner == nil || s.inner.ProcessState().Phase == driver.PhaseClosed) {
		inner := s.detachLocked()
		s.transitionLocked(Idle)
		s.mu.Unlock()
		if inner != nil {
			_ = inner.Close()
		}
		return nil, driver.NewTransportError("agent transport closed while blocked")
	}
	s.transitionLocked(Running)
	s.mu.Unlock()

	if state == Idle {
		if err := s.ensureAttached(); err != nil {
			s.setState(Idle)
			return nil, err
		}
	}
	s.mu.Lock()
	inner := s.inner
	events := s.events
	s.mu.Unlock()
	var runID driver.RunID
	var err error
	if state == Blocked {
		runID, err = inner.Respond(input)
	} else {
		runID, err = inner.Send(input)
	}
	if err != nil {
		if s.discardIfClosed(inner) || state == Idle {
			s.setState(Idle)
		} else {
			s.setState(Blocked)
		}
		return nil, err
	}
	stream := newStream(s.agent.ctx, s, events, runID)
	s.mu.Lock()
	s.active = stream
	s.mu.Unlock()
	return stream, nil
}

func (s *Session) ensureAttached() error {
	if err := s.ensureHandle(); err != nil {
		return err
	}
	s.mu.Lock()
	inner := s.inner
	started := s.started
	s.mu.Unlock()
	if started {
		return nil
	}
	if err := inner.Start(); err != nil {
		s.detachIfCurrent(inner)
		_ = inner.Close()
		return err
	}
	sid := inner.SessionID()
	s.mu.Lock()
	if s.agent.closed.Load() {
		s.mu.Unlock()
		_ = inner.Close()
		return errs.InvalidState("agent is closed")
	}
	if sid != "" {
		s.id = sid
	}
	s.started = true
	s.mu.Unlock()
	if sid != "" {
		return s.agent.register(s, sid)
	}
	return nil
}

func (s *Session) ensureHandle() error {
	s.mu.Lock()
	if s.inner != nil {
		if s.inner.ProcessState().Phase != driver.PhaseClosed {
			s.mu.Unlock()
			return nil
		}
		stale := s.detachLocked()
		s.mu.Unlock()
		_ = stale.Close()
		s.mu.Lock()
	}
	resumeID := s.id
	s.mu.Unlock()

	att, err := s.agent.driver.OpenSession(driver.OpenParams{ResumeSessionID: resumeID, Cwd: s.cwd})
	if err != nil {
		return err
	}
	events := att.Events.Subscribe()
	s.mu.Lock()
	if s.agent.closed.Load() {
		s.mu.Unlock()
		_ = att.Session.Close()
		return errs.InvalidState("agent is closed")
	}
	s.inner = att.Session
	s.events = events
	s.started = false
	s.mu.Unlock()
	return nil
}

// Interrupt stops the current running or blocked turn. It is an idempotent
// no-op while idle.
func (s *Session) Interrupt() error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.mu.Lock()
	state := s.state
	if state == Idle {
		s.mu.Unlock()
		return nil
	}
	inner := s.inner
	s.mu.Unlock()
	if inner == nil {
		return errs.InvalidState("session is not attached")
	}
	if err := inner.Interrupt(); err != nil {
		return err
	}
	if inner.ProcessState().Phase == driver.PhaseClosed {
		s.detachIfCurrent(inner)
	}
	if state == Blocked {
		s.setState(Idle)
	}
	return nil
}

// transitionLocked applies a state change and its side effects. The caller
// must hold s.mu.
func (s *Session) transitionLocked(state SessionState) {
	s.state = state
	if state != Running {
		s.active = nil
	}
}

func (s *Session) setState(state SessionState) {
	s.mu.Lock()
	s.transitionLocked(state)
	s.mu.Unlock()
}

// detachLocked clears the current attachment and returns the previous inner
// session, if any. The caller must hold s.mu.
func (s *Session) detachLocked() driver.Session {
	inner := s.inner
	s.inner = nil
	s.events = nil
	s.started = false
	return inner
}

// detachIfCurrent clears the attachment only when inner is still the live one,
// guarding against a concurrent re-attach. The caller must not hold s.mu.
func (s *Session) detachIfCurrent(inner driver.Session) {
	s.mu.Lock()
	if s.inner == inner {
		s.inner = nil
		s.events = nil
		s.started = false
	}
	s.mu.Unlock()
}

// discardIfClosed detaches and closes inner when its process has already
// terminated. It reports whether the attachment was discarded.
func (s *Session) discardIfClosed(inner driver.Session) bool {
	if inner.ProcessState().Phase != driver.PhaseClosed {
		return false
	}
	s.detachIfCurrent(inner)
	_ = inner.Close()
	return true
}

func (s *Session) setID(id string) error {
	if id == "" {
		return nil
	}
	s.mu.Lock()
	if s.id != "" && s.id != id {
		old := s.id
		s.mu.Unlock()
		return errs.InvalidState("runtime session ID changed from " + old + " to " + id)
	}
	s.id = id
	s.mu.Unlock()
	return s.agent.register(s, id)
}

func (s *Session) closeAttachment() error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.mu.Lock()
	inner := s.detachLocked()
	s.mu.Unlock()
	if inner != nil {
		return inner.Close()
	}
	return nil
}

func (s *Session) dropAttachment() {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.mu.Lock()
	inner := s.detachLocked()
	s.mu.Unlock()
	if inner != nil {
		_ = inner.Close()
	}
}
