package unio

import (
	"context"
	"sync"

	"github.com/Fullstop000/unio/driver"
	"github.com/Fullstop000/unio/errs"
)

// Run drives one prompt to completion and returns the result. It is the
// one-liner for the common case: open a session, send the prompt, wait for the
// result, and close. An agent-reported failure (or transport death) is returned
// as an error; a not_installed agent likewise errors here.
func Run(ctx context.Context, agent Agent, prompt string, opts ...Option) (Result, error) {
	s, err := Start(ctx, agent, opts...)
	if err != nil {
		return Result{}, err
	}
	defer s.Close()
	return s.Prompt(ctx, prompt).Result()
}

// Session is the ergonomic handle for a multi-turn conversation. It owns the
// underlying driver session, its event bus, and the (auto-generated) key, so
// callers never touch SessionKey/AgentSpec/subscription timing.
//
// A Session's prompts are strictly SERIAL: it exposes one shared event stream,
// so at most one turn may be in flight at a time. Prompt returns an error-loaded
// Stream if a prior turn hasn't been drained. This matches the agents' own
// model (a Codex thread / a Claude process handles one turn at a time); to run
// turns in parallel, open multiple Sessions.
type Session struct {
	inner driver.Session
	sub   <-chan driver.AgentEvent

	mu   sync.Mutex
	busy bool // true while a turn's Stream is active
}

// Start starts a session and brings it online, ready for prompts. It resolves and
// spawns the agent (surfacing not_installed early), subscribes before Run so no
// event is missed, and attaches the runtime session id — all internally.
func Start(ctx context.Context, agent Agent, opts ...Option) (*Session, error) {
	cfg := buildConfig(opts)
	d, err := driverFor(agent)
	if err != nil {
		return nil, err
	}
	att, err := d.OpenSession(ctx, autoKey(agent), cfg.spec(), driver.OpenParams{ResumeSessionID: cfg.resume})
	if err != nil {
		return nil, err // includes not_installed, surfaced before spawn
	}
	sub := att.Events.Subscribe()
	if err := att.Session.Run(ctx, nil); err != nil {
		_ = att.Session.Close(ctx)
		return nil, err
	}
	return &Session{inner: att.Session, sub: sub}, nil
}

// Open is kept as a compatibility alias for Start.
func Open(ctx context.Context, agent Agent, opts ...Option) (*Session, error) {
	return Start(ctx, agent, opts...)
}

// SessionID returns the runtime-owned id, usable with WithResume later.
func (s *Session) SessionID() string { return s.inner.SessionID() }

// Prompt sends a prompt and returns a Stream handle for the turn. Callers can
// either range it (st.Next()/st.Event()) to watch the turn unfold, or call
// st.Result() directly to block for the final outcome — one method, both styles,
// and streaming callers still get the final usage/finish via Result.
//
// Prompts on one Session are serial: if a prior turn's Stream has not been
// drained (via Result() or ranging Next() to completion), Prompt returns a
// Stream whose Result() yields an error. Open another Session for parallelism.
func (s *Session) Prompt(ctx context.Context, prompt string) *Stream {
	s.mu.Lock()
	if s.busy {
		s.mu.Unlock()
		st := &Stream{ctx: ctx}
		st.finish(Result{}, errs.Unsupported("unio: a turn is already in flight on this session; prompts are serial"))
		return st
	}
	s.busy = true
	s.mu.Unlock()

	runID, err := s.inner.Prompt(ctx, driver.PromptReq{Text: prompt})
	st := &Stream{
		sub:    s.sub,
		runID:  runID,
		sid:    s.inner.SessionID,
		ctx:    ctx,
		onDone: s.clearBusy,
	}
	if err != nil {
		// Prompt failed to submit: mark done so Result() yields the error, and
		// release the session for the next turn.
		st.finish(Result{}, err)
	}
	return st
}

// clearBusy releases the session so the next turn can start. Called exactly once
// when a Stream reaches its terminal state.
func (s *Session) clearBusy() {
	s.mu.Lock()
	s.busy = false
	s.mu.Unlock()
}

// Cancel interrupts the in-flight turn (graceful where the agent supports it,
// e.g. Codex). Returns whether a run was actually aborted.
func (s *Session) Cancel(ctx context.Context) (bool, error) {
	out, err := s.inner.Cancel(ctx, "")
	return out == driver.CancelAborted, err
}

// Close shuts the session down and releases the agent process/resources.
func (s *Session) Close() error {
	return s.inner.Close(context.Background())
}

func failedToError(ev driver.AgentEvent) error {
	if ev.Err != nil {
		return ev.Err
	}
	return driver.NewRuntimeReportedError("unio: run failed")
}
