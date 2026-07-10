package unio

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/Fullstop000/unio/driver"
	"github.com/Fullstop000/unio/errs"
)

// Agent is one configured coding-agent runtime. It owns the concrete driver,
// shared runtime processes, and all session handles created through it.
type Agent struct {
	kind   AgentKind
	cfg    config
	driver driver.ProtocolDriver

	mu       sync.Mutex
	sessions map[string]*Session
	pending  map[*Session]struct{}
	closed   atomic.Bool
}

// New initializes an agent runtime. A successful return means its CLI is
// installed and available to create sessions.
func New(kind AgentKind, opts ...Option) (*Agent, error) {
	d, err := driverFor(kind)
	if err != nil {
		return nil, err
	}
	probe, err := d.Probe(context.Background())
	if err != nil {
		return nil, err
	}
	if probe.Auth == driver.AuthNotInstalled {
		return nil, errs.NotInstalledCmd(string(kind))
	}
	if probe.Auth == driver.AuthUnauthed {
		return nil, errs.RuntimeReported(string(kind) + " is not authenticated")
	}
	return &Agent{
		kind: kind, cfg: buildConfig(opts), driver: d,
		sessions: make(map[string]*Session), pending: make(map[*Session]struct{}),
	}, nil
}

// NewSession creates an idle local conversation handle without sending a
// hidden prompt. Its runtime ID is assigned by the first Run or Stream.
func (a *Agent) NewSession(ctx context.Context) (*Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed.Load() {
		return nil, errs.InvalidState("agent is closed")
	}
	s := newSession(a, "", normalizeCwd(a.cfg.spec().Cwd))
	a.pending[s] = struct{}{}
	return s, nil
}

// ListSessions lists conversations for the Agent's working directory.
// SessionsIn selects another directory and AllSessions removes the directory
// filter. Maintained live handles are included even when runtime history has
// not reached disk yet.
func (a *Agent) ListSessions(ctx context.Context, opts ...ListSessionsOption) ([]SessionInfo, error) {
	if a.closed.Load() {
		return nil, errs.InvalidState("agent is closed")
	}
	spec := a.cfg.spec()
	listCfg := buildListSessionsConfig(spec.Cwd, opts)
	params := driver.ListSessionsParams{Cwd: listCfg.cwd, Spec: spec}
	if listCfg.all {
		params.Cwd = ""
	}
	stored, err := a.driver.ListSessions(ctx, params)
	if err != nil {
		return nil, err
	}
	infos := make([]SessionInfo, 0, len(stored))
	seen := make(map[string]struct{}, len(stored))
	for _, meta := range stored {
		infos = append(infos, sessionInfo(meta))
		seen[meta.SessionID] = struct{}{}
	}
	a.mu.Lock()
	for id, session := range a.sessions {
		if params.Cwd != "" && normalizeCwd(session.cwd) != params.Cwd {
			continue
		}
		if _, ok := seen[id]; !ok {
			infos = append(infos, SessionInfo{ID: id, Cwd: session.cwd})
		}
	}
	a.mu.Unlock()
	return infos, nil
}

// GetSession returns the maintained handle for a persisted runtime session.
// It does not attach to the runtime; the next Run or Stream resumes it.
func (a *Agent) GetSession(ctx context.Context, id string) (*Session, error) {
	if id == "" {
		return nil, errs.SessionNotFound(id)
	}
	if a.closed.Load() {
		return nil, errs.InvalidState("agent is closed")
	}
	a.mu.Lock()
	if existing := a.sessions[id]; existing != nil {
		a.mu.Unlock()
		return existing, nil
	}
	a.mu.Unlock()

	stored, err := a.driver.ListSessions(ctx, driver.ListSessionsParams{Spec: a.cfg.spec()})
	if err != nil {
		return nil, err
	}
	var matched driver.StoredSessionMeta
	for _, meta := range stored {
		if meta.SessionID == id {
			matched = meta
			break
		}
	}
	if matched.SessionID == "" {
		return nil, errs.SessionNotFound(id)
	}
	candidate := newSession(a, id, matched.Cwd)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed.Load() {
		return nil, errs.InvalidState("agent is closed")
	}
	if existing := a.sessions[id]; existing != nil {
		return existing, nil
	}
	a.sessions[id] = candidate
	return candidate, nil
}

func sessionInfo(meta driver.StoredSessionMeta) SessionInfo {
	return SessionInfo{
		ID: meta.SessionID, Title: meta.Title, Cwd: meta.Cwd,
		StartedAt: meta.StartedAt, UpdatedAt: meta.UpdatedAt, MessageCount: meta.MessageCount,
	}
}

func (a *Agent) register(s *Session, id string) error {
	if id == "" {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if existing := a.sessions[id]; existing != nil && existing != s {
		return errs.InvalidState("runtime session already has another live handle")
	}
	delete(a.pending, s)
	a.sessions[id] = s
	return nil
}

// Close releases every runtime process and goroutine owned by this Agent.
func (a *Agent) Close() error {
	if !a.closed.CompareAndSwap(false, true) {
		return nil
	}
	a.mu.Lock()
	all := make([]*Session, 0, len(a.sessions)+len(a.pending))
	for _, s := range a.sessions {
		all = append(all, s)
	}
	for s := range a.pending {
		all = append(all, s)
	}
	a.mu.Unlock()

	var failures []error
	for _, s := range all {
		if err := s.closeAttachment(context.Background()); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
