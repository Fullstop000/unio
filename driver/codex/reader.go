package codex

import (
	"encoding/json"
)

// readerLoop consumes the shared child's stdout, routing responses to their
// pending waiters and notifications/deltas to the owning session.
func (p *process) readerLoop() {
	p.mu.Lock()
	tr := p.tr
	p.mu.Unlock()
	defer close(p.closed)

	sc := tr.stdout()
	for sc.Scan() {
		p.dispatch(sc.Text())
	}
	// Reap the shared app-server before p.closed is closed. The last session's
	// Close waits on p.closed, so it cannot leave a zombie child behind.
	_ = tr.wait()
	p.lifecycle.Lock()
	p.dead.Store(true)
	p.lifecycle.Unlock()

	// stdout closed: invalidate every attachment, including idle sessions and
	// sessions whose thread registration never completed.
	for _, s := range p.attachedSessions() {
		s.onTransportClosed()
	}
}

// dispatch parses one line and routes it.
func (p *process) dispatch(line string) {
	ev := ParseLine(line, func(id uint64) (string, bool) {
		// Consult and remove the pending entry so the error path also clears it.
		// The waiter is resolved here after classification.
		p.mu.Lock()
		pend, ok := p.pendingReqs[id]
		p.mu.Unlock()
		if !ok {
			return "", false
		}
		return pend.method, true
	})

	// Responses: resolve the waiter.
	switch ev.Type {
	case EvInitializeResponse, EvThreadResponse, EvTurnResponse, EvTurnInterruptResponse, EvError:
		if p.resolvePending(line, ev) {
			return
		}
	}

	// Notifications / deltas: route to the owning session.
	p.route(ev)
}

// resolvePending finds the request id on the raw line, removes the waiter, and
// delivers the event. Returns true if it was a response we routed.
func (p *process) resolvePending(line string, ev AppServerEvent) bool {
	id, ok := responseID(line)
	if !ok {
		return false
	}
	p.mu.Lock()
	pend, exists := p.pendingReqs[id]
	if exists {
		delete(p.pendingReqs, id)
	}
	p.mu.Unlock()
	if !exists {
		return false
	}
	pend.ch <- ev
	return true
}

// route sends a notification/delta to the session that owns its thread.
func (p *process) route(ev AppServerEvent) {
	threadID := ev.ThreadID
	if threadID == "" && ev.TurnID != "" {
		threadID = p.threadForTurn(ev.TurnID)
	}

	switch ev.Type {
	case EvTurnStarted:
		p.mapTurn(ev.TurnID, ev.ThreadID)
	case EvThreadStarted:
		// informational; session already knows its id from the response.
	}

	if threadID == "" {
		if s := p.soleInFlightSession(); s != nil {
			s.onEvent(ev)
		}
		return
	}
	s := p.sessionForThread(threadID)
	if s == nil {
		return
	}
	s.onEvent(ev)
}

// responseID extracts the numeric id from a response line for waiter lookup.
func responseID(line string) (uint64, bool) {
	var m map[string]any
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		return 0, false
	}
	idv, ok := m["id"]
	if !ok {
		return 0, false
	}
	f, ok := idv.(float64)
	if !ok {
		return 0, false
	}
	return uint64(f), true
}
