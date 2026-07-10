package unio

import (
	"context"
	"sync"

	"github.com/Fullstop000/unio/driver"
	"github.com/Fullstop000/unio/errs"
)

// Session is one conversation with an Agent.
type Session struct {
	agent *Agent
	key   driver.SessionKey
	opMu  sync.Mutex

	mu            sync.Mutex
	state         SessionState
	id            string
	cwd           string
	inner         driver.Session
	events        <-chan driver.AgentEvent
	active        *Stream
	closedByAgent bool
}

func newSession(agent *Agent, id, cwd string) *Session {
	return &Session{agent: agent, key: autoKey(agent.kind), id: id, cwd: cwd, state: Idle}
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

// Run sends one prompt and waits for completion, interruption, or blocking.
func (s *Session) Run(ctx context.Context, prompt string) (Result, error) {
	stream, err := s.Stream(ctx, prompt)
	if err != nil {
		return Result{}, err
	}
	return stream.Result()
}

// Stream sends one prompt and returns its live event stream.
func (s *Session) Stream(ctx context.Context, prompt string) (*Stream, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.mu.Lock()
	if s.closedByAgent || s.agent.isClosed() {
		s.mu.Unlock()
		return nil, errs.InvalidState("agent is closed")
	}
	if s.state != Idle {
		state := s.state
		s.mu.Unlock()
		return nil, errs.InvalidState("cannot run session while " + string(state))
	}
	s.state = Running
	s.mu.Unlock()

	if err := s.ensureAttached(ctx); err != nil {
		s.setState(Idle)
		return nil, err
	}
	s.mu.Lock()
	inner := s.inner
	events := s.events
	s.mu.Unlock()
	runID, err := inner.Prompt(ctx, driver.PromptReq{Text: prompt})
	if err != nil {
		s.setState(Idle)
		return nil, err
	}
	stream := newStream(ctx, s, events, runID)
	s.mu.Lock()
	s.active = stream
	s.mu.Unlock()
	return stream, nil
}

func (s *Session) ensureAttached(ctx context.Context) error {
	s.mu.Lock()
	if s.inner != nil {
		if s.inner.ProcessState().Phase != driver.PhaseClosed {
			s.mu.Unlock()
			return nil
		}
		stale := s.inner
		s.inner = nil
		s.events = nil
		s.mu.Unlock()
		_ = stale.Close(context.Background())
		s.mu.Lock()
	}
	resumeID := s.id
	s.mu.Unlock()

	spec := s.agent.cfg.spec()
	if s.cwd != "" {
		spec.Cwd = s.cwd
	}
	att, err := s.agent.driver.OpenSession(ctx, s.key, spec, driver.OpenParams{ResumeSessionID: resumeID})
	if err != nil {
		return err
	}
	events := att.Events.Subscribe()
	if err := att.Session.Run(ctx, nil); err != nil {
		_ = att.Session.Close(context.Background())
		return err
	}
	s.mu.Lock()
	if s.closedByAgent || s.agent.isClosed() {
		s.mu.Unlock()
		_ = att.Session.Close(context.Background())
		return errs.InvalidState("agent is closed")
	}
	s.inner = att.Session
	s.events = events
	sid := att.Session.SessionID()
	if sid != "" {
		s.id = sid
	}
	s.mu.Unlock()
	if sid != "" {
		return s.agent.register(s, sid)
	}
	return nil
}

// Interrupt stops the current running or blocked turn. It is an idempotent
// no-op while idle.
func (s *Session) Interrupt(ctx context.Context) error {
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
	if err := inner.Interrupt(ctx); err != nil {
		return err
	}
	if inner.ProcessState().Phase == driver.PhaseClosed {
		s.mu.Lock()
		if s.inner == inner {
			s.inner = nil
			s.events = nil
		}
		s.mu.Unlock()
	}
	if state == Blocked {
		s.setState(Idle)
	}
	return nil
}

// Continue supplies input requested by a blocked turn and waits for the agent
// to complete, block again, or be interrupted.
func (s *Session) Continue(ctx context.Context, input string) (Result, error) {
	s.opMu.Lock()
	s.mu.Lock()
	if s.closedByAgent || s.agent.isClosed() {
		s.mu.Unlock()
		s.opMu.Unlock()
		return Result{}, errs.InvalidState("agent is closed")
	}
	if s.state != Blocked {
		s.mu.Unlock()
		s.opMu.Unlock()
		return Result{}, errs.InvalidState("continue requires a blocked session")
	}
	inner := s.inner
	if inner == nil || inner.ProcessState().Phase == driver.PhaseClosed {
		s.inner = nil
		s.events = nil
		s.state = Idle
		s.mu.Unlock()
		s.opMu.Unlock()
		if inner != nil {
			_ = inner.Close(context.Background())
		}
		return Result{}, driver.NewTransportError("agent transport closed while blocked")
	}
	s.state = Running
	events := s.events
	s.mu.Unlock()
	runID, err := inner.Continue(ctx, input)
	if err != nil {
		s.setState(Blocked)
		s.opMu.Unlock()
		return Result{}, err
	}
	stream := newStream(ctx, s, events, runID)
	s.mu.Lock()
	s.active = stream
	s.mu.Unlock()
	s.opMu.Unlock()
	return stream.Result()
}

func (s *Session) setState(state SessionState) {
	s.mu.Lock()
	s.state = state
	if state != Running {
		s.active = nil
	}
	s.mu.Unlock()
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

func (s *Session) closeAttachment(ctx context.Context) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.mu.Lock()
	s.closedByAgent = true
	inner := s.inner
	s.inner = nil
	s.events = nil
	s.mu.Unlock()
	if inner != nil {
		return inner.Close(ctx)
	}
	return nil
}

func (s *Session) dropAttachment() {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.mu.Lock()
	inner := s.inner
	s.inner = nil
	s.events = nil
	s.mu.Unlock()
	if inner != nil {
		_ = inner.Close(context.Background())
	}
}
