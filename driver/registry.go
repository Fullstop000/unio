package driver

import "sync"

// AgentProcess is a per-agent runtime process that a Registry caches and evicts
// when stale. Multiplexing transports (Codex app-server, ACP) implement this on
// their shared-child struct so several sessions can reuse one process; the
// registry hands out the same instance until it dies.
type AgentProcess interface {
	// IsStale reports whether the cached process can no longer serve a new
	// attach — the shared child died, stdio closed, or Close marked it torn
	// down. A never-started process is NOT stale; the bootstrap path re-uses a
	// cached-but-unstarted entry.
	IsStale() bool
	// DriverName is a short label for registry debug logs, e.g. "codex", "acp".
	DriverName() string
}

// Registry is a process-global, per-driver cache of AgentProcess instances keyed
// by AgentKey. It replaces the near-identical per-driver maps a naive port would
// carry. All methods are safe for concurrent use; a driver instantiates one as a
// package-level value.
type Registry[P AgentProcess] struct {
	mu sync.Mutex
	m  map[AgentKey]P
}

// NewRegistry constructs an empty registry.
func NewRegistry[P AgentProcess]() *Registry[P] {
	return &Registry[P]{m: make(map[AgentKey]P)}
}

// GetOrInit returns the cached process for key, building a fresh one via factory
// if the slot is empty or the cached entry is stale. Lookup, eviction, and
// insert happen under one lock so two concurrent attaches on the same key
// observe each other.
func (r *Registry[P]) GetOrInit(key AgentKey, factory func() P) P {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensure()
	if existing, ok := r.m[key]; ok {
		if !existing.IsStale() {
			return existing
		}
		delete(r.m, key)
	}
	fresh := factory()
	r.m[key] = fresh
	return fresh
}

// GetOrEvictStale returns the cached process for key, evicting it first if
// stale. Used by drivers whose attach path constructs the process inline and
// calls Insert separately. The bool is false when there is no live entry.
func (r *Registry[P]) GetOrEvictStale(key AgentKey) (P, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensure()
	if existing, ok := r.m[key]; ok {
		if existing.IsStale() {
			delete(r.m, key)
			var zero P
			return zero, false
		}
		return existing, true
	}
	var zero P
	return zero, false
}

// Get is a raw read that does NOT evict stale entries. Mostly for tests.
func (r *Registry[P]) Get(key AgentKey) (P, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensure()
	p, ok := r.m[key]
	return p, ok
}

// Insert overwrites any existing entry without a stale check.
func (r *Registry[P]) Insert(key AgentKey, p P) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensure()
	r.m[key] = p
}

// Remove deletes an entry.
func (r *Registry[P]) Remove(key AgentKey) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensure()
	delete(r.m, key)
}

// Len returns the number of cached entries (test helper).
func (r *Registry[P]) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.m)
}

func (r *Registry[P]) ensure() {
	if r.m == nil {
		r.m = make(map[AgentKey]P)
	}
}
