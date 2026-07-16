package acp

import (
	"context"
	"path/filepath"
	"sync"

	"github.com/Fullstop000/unio/driver"
)

// Driver implements driver.Driver for one ACP-native runtime.
type Driver struct {
	ctx     context.Context
	spec    driver.AgentSpec
	cfg     runtimeConfig
	factory transportFactory

	mu      sync.Mutex
	process *process
}

// New constructs a shared ACP v1 driver for runtime.
func New(ctx context.Context, runtime Runtime, spec driver.AgentSpec) *Driver {
	return newWithTransport(ctx, runtime, spec, spawnTransport)
}

func newWithTransport(ctx context.Context, runtime Runtime, spec driver.AgentSpec, factory transportFactory) *Driver {
	return &Driver{ctx: ctx, spec: spec, cfg: configFor(runtime), factory: factory}
}

func (d *Driver) Probe() (driver.ProbeAuth, error) {
	spec := d.cfg.applyDefaults(d.spec)
	if _, err := driver.ResolveExecutable(spec); err != nil {
		return driver.AuthNotInstalled, nil
	}
	return driver.AuthAuthed, nil
}

func (d *Driver) ListSessions(params driver.ListSessionsParams) ([]driver.StoredSessionMeta, error) {
	ctx := d.ctx
	spec := d.prepareSpec(params.Cwd)
	execPath, resolveErr := driver.ResolveExecutable(spec)
	if resolveErr != nil {
		return nil, resolveErr
	}

	d.mu.Lock()
	proc := d.process
	ephemeral := proc == nil || proc.dead.Load()
	if ephemeral {
		proc = newProcess(d.cfg, execPath, spec, d.factory)
	}
	d.mu.Unlock()

	if err := proc.ensureStarted(ctx); err != nil {
		if ephemeral {
			proc.shutdown()
		}
		return nil, err
	}
	if ephemeral {
		defer proc.shutdown()
	}
	return proc.listSessions(ctx, params.Cwd)
}

func (d *Driver) OpenSession(params driver.OpenParams) (*driver.SessionAttachment, error) {
	spec := d.prepareSpec(params.Cwd)
	execPath, resolveErr := driver.ResolveExecutable(spec)
	if resolveErr != nil {
		return nil, resolveErr
	}

	d.mu.Lock()
	if d.process == nil || d.process.dead.Load() {
		d.process = newProcess(d.cfg, execPath, spec, d.factory)
	}
	proc := d.process
	bus := driver.NewEventBus()
	session := newSession(d.ctx, proc, spec, params.ResumeSessionID, bus)
	if !proc.acquire(session) {
		proc = newProcess(d.cfg, execPath, spec, d.factory)
		d.process = proc
		session.proc = proc
		_ = proc.acquire(session)
	}
	d.mu.Unlock()
	return &driver.SessionAttachment{Session: session, Events: bus}, nil
}

// Close terminates the shared ACP child. Agent normally reaches the same state
// by closing its final session; Close is also useful to concrete-driver users.
func (d *Driver) Close() error {
	d.mu.Lock()
	proc := d.process
	d.process = nil
	d.mu.Unlock()
	if proc != nil {
		proc.shutdown()
		<-proc.closed
	}
	return nil
}

func (d *Driver) prepareSpec(cwd string) driver.AgentSpec {
	spec := d.spec
	if cwd != "" {
		spec.Cwd = cwd
	}
	spec = d.cfg.applyDefaults(spec)
	if spec.Cwd == "" {
		spec.Cwd = "."
	}
	if absolute, err := filepath.Abs(spec.Cwd); err == nil {
		spec.Cwd = filepath.Clean(absolute)
	}
	return spec
}

var _ driver.Driver = (*Driver)(nil)
