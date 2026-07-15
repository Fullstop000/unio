package acp

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Fullstop000/unio/driver"
)

const clientVersion = "0.2.0"

type pendingRequest struct {
	method string
	ch     chan rpcResponse
}

type process struct {
	cfg      runtimeConfig
	execPath string
	spec     driver.AgentSpec
	factory  transportFactory

	startMu sync.Mutex
	started bool
	caps    capabilities
	dead    atomic.Bool

	mu          sync.Mutex
	writeMu     sync.Mutex
	transport   stdioTransport
	nextID      uint64
	pending     map[string]*pendingRequest
	sessions    map[string]*session
	attachments map[*session]struct{}

	closed    chan struct{}
	closeOnce sync.Once
	stopOnce  sync.Once
}

func newProcess(cfg runtimeConfig, execPath string, spec driver.AgentSpec, factory transportFactory) *process {
	return &process{
		cfg: cfg, execPath: execPath, spec: spec, factory: factory,
		pending: make(map[string]*pendingRequest), sessions: make(map[string]*session),
		attachments: make(map[*session]struct{}), closed: make(chan struct{}),
	}
}

func (p *process) ensureStarted(ctx context.Context) error {
	p.startMu.Lock()
	defer p.startMu.Unlock()
	if p.started {
		return nil
	}
	if p.dead.Load() {
		return driver.NewTransportError(p.closedMessage("ACP runtime is closed"))
	}
	tr, err := p.factory(context.WithoutCancel(ctx), p.execPath, p.spec, p.cfg.buildArgs(p.spec))
	if err != nil {
		p.markClosed()
		return err
	}
	p.mu.Lock()
	p.transport = tr
	p.mu.Unlock()
	go p.readerLoop(tr)

	resp, err := p.call(ctx, "initialize", map[string]any{
		"protocolVersion":    protocolVersion,
		"clientCapabilities": map[string]any{},
		"clientInfo":         map[string]any{"name": "unio", "title": "unio", "version": clientVersion},
	})
	if err != nil {
		p.shutdown()
		return err
	}
	var init initializeResult
	if err := json.Unmarshal(resp, &init); err != nil {
		p.shutdown()
		return driver.NewProtocolError("acp: invalid initialize response: " + err.Error())
	}
	if init.ProtocolVersion != protocolVersion {
		p.shutdown()
		return driver.NewUnsupportedError("acp: runtime selected unsupported protocol version")
	}
	p.caps = init.capabilities()
	p.started = true
	return nil
}

func (p *process) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id, ch, err := p.request(method, params)
	if err != nil {
		return nil, err
	}
	select {
	case response := <-ch:
		if response.err != nil {
			return nil, driver.NewProtocolError("acp " + method + ": " + errorMessage(response.err))
		}
		return response.result, nil
	case <-ctx.Done():
		p.removePending(id)
		return nil, ctx.Err()
	case <-p.closed:
		p.removePending(id)
		return nil, driver.NewTransportError(p.closedMessage("ACP runtime closed during " + method))
	}
}

func (p *process) removePending(id uint64) {
	p.mu.Lock()
	delete(p.pending, jsonIDKey(id))
	p.mu.Unlock()
}

func (p *process) request(method string, params any) (uint64, <-chan rpcResponse, error) {
	p.mu.Lock()
	p.nextID++
	id := p.nextID
	ch := make(chan rpcResponse, 1)
	key := jsonIDKey(id)
	p.pending[key] = &pendingRequest{method: method, ch: ch}
	p.mu.Unlock()

	payload, err := marshalRequest(id, method, params)
	if err == nil {
		err = p.write(payload)
	}
	if err != nil {
		p.mu.Lock()
		delete(p.pending, key)
		p.mu.Unlock()
		return 0, nil, err
	}
	return id, ch, nil
}

func jsonIDKey(id uint64) string {
	payload, _ := json.Marshal(id)
	return string(payload)
}

func (p *process) notify(method string, params any) error {
	payload, err := marshalNotification(method, params)
	if err != nil {
		return driver.NewProtocolError("acp: encode " + method + ": " + err.Error())
	}
	return p.write(payload)
}

func (p *process) write(payload []byte) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	p.mu.Lock()
	tr := p.transport
	p.mu.Unlock()
	if tr == nil || p.dead.Load() {
		return driver.NewTransportError("acp: runtime is not running")
	}
	payload = append(payload, '\n')
	n, err := tr.stdin().Write(payload)
	if err != nil {
		p.shutdown()
		return driver.NewTransportError("acp: write stdin: " + err.Error())
	}
	if n != len(payload) {
		p.shutdown()
		return driver.NewTransportError("acp: short write to stdin")
	}
	return nil
}

func (p *process) readerLoop(tr stdioTransport) {
	for scanner := tr.stdout(); scanner.Scan(); {
		p.dispatch(scanner.Bytes())
	}
	_ = tr.wait()
	p.markClosed()
	for _, session := range p.attachedSessions() {
		session.onTransportClosed()
	}
}

func (p *process) dispatch(line []byte) {
	var msg rpcMessage
	if json.Unmarshal(line, &msg) != nil {
		return
	}
	if len(msg.ID) != 0 && msg.Method == "" && (msg.Result != nil || msg.Error != nil) {
		p.mu.Lock()
		pending := p.pending[idKey(msg.ID)]
		delete(p.pending, idKey(msg.ID))
		p.mu.Unlock()
		if pending != nil {
			pending.ch <- rpcResponse{result: msg.Result, err: msg.Error}
		}
		return
	}
	if msg.Method == "session/update" {
		var params struct {
			SessionID string          `json:"sessionId"`
			Update    json.RawMessage `json:"update"`
		}
		if json.Unmarshal(msg.Params, &params) == nil {
			if session := p.sessionForID(params.SessionID); session != nil {
				session.onUpdate(params.Update)
			}
		}
		return
	}
	if msg.Method == "session/request_permission" && len(msg.ID) != 0 {
		var params struct {
			SessionID string `json:"sessionId"`
		}
		if json.Unmarshal(msg.Params, &params) == nil {
			if session := p.sessionForID(params.SessionID); session != nil {
				session.onPermission(msg.ID, msg.Params)
				return
			}
		}
		payload, err := marshalPermissionResponse(msg.ID, "cancelled", "")
		if err == nil {
			_ = p.write(payload)
		}
	}
}

func (p *process) listSessions(ctx context.Context, cwd string) ([]driver.StoredSessionMeta, error) {
	if !p.caps.List {
		return nil, driver.NewUnsupportedError("acp: runtime does not support session/list")
	}
	var out []driver.StoredSessionMeta
	cursor := ""
	seen := make(map[string]struct{})
	for {
		params := make(map[string]any)
		if cwd != "" {
			params["cwd"] = cwd
		}
		if cursor != "" {
			params["cursor"] = cursor
		}
		result, err := p.call(ctx, "session/list", params)
		if err != nil {
			return nil, err
		}
		var page struct {
			Sessions []struct {
				SessionID string `json:"sessionId"`
				Cwd       string `json:"cwd"`
				Title     string `json:"title"`
				UpdatedAt string `json:"updatedAt"`
				Meta      struct {
					MessageCount int `json:"messageCount"`
				} `json:"_meta"`
			} `json:"sessions"`
			NextCursor string `json:"nextCursor"`
		}
		if err := json.Unmarshal(result, &page); err != nil {
			return nil, driver.NewProtocolError("acp: invalid session/list response: " + err.Error())
		}
		for _, item := range page.Sessions {
			updated, _ := time.Parse(time.RFC3339Nano, item.UpdatedAt)
			out = append(out, driver.StoredSessionMeta{
				SessionID: item.SessionID, Cwd: item.Cwd, Title: item.Title,
				UpdatedAt: updated, MessageCount: item.Meta.MessageCount,
			})
		}
		if page.NextCursor == "" {
			return out, nil
		}
		if _, duplicate := seen[page.NextCursor]; duplicate {
			return nil, driver.NewProtocolError("acp: session/list repeated nextCursor")
		}
		seen[page.NextCursor] = struct{}{}
		cursor = page.NextCursor
	}
}

func (p *process) acquire(session *session) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dead.Load() {
		return false
	}
	p.attachments[session] = struct{}{}
	return true
}

func (p *process) release(session *session) {
	p.mu.Lock()
	delete(p.attachments, session)
	empty := len(p.attachments) == 0
	p.mu.Unlock()
	if empty {
		p.shutdown()
	}
}

func (p *process) registerSession(id string, session *session) {
	p.mu.Lock()
	p.sessions[id] = session
	p.mu.Unlock()
}

func (p *process) unregisterSession(id string) {
	p.mu.Lock()
	delete(p.sessions, id)
	p.mu.Unlock()
}

func (p *process) sessionForID(id string) *session {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sessions[id]
}

func (p *process) attachedSessions() []*session {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]*session, 0, len(p.attachments))
	for session := range p.attachments {
		out = append(out, session)
	}
	return out
}

func (p *process) shutdown() {
	p.stopOnce.Do(func() {
		p.mu.Lock()
		tr := p.transport
		p.mu.Unlock()
		if tr == nil {
			p.markClosed()
			return
		}
		tr.kill()
	})
}

func (p *process) markClosed() {
	p.closeOnce.Do(func() {
		p.dead.Store(true)
		p.mu.Lock()
		clear(p.pending)
		p.mu.Unlock()
		close(p.closed)
	})
}

func (p *process) closedMessage(fallback string) string {
	p.mu.Lock()
	tr := p.transport
	p.mu.Unlock()
	if tr != nil {
		if stderr := strings.TrimSpace(tr.errText()); stderr != "" {
			return fallback + ": " + stderr
		}
	}
	return fallback
}
