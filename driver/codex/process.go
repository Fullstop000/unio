package codex

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/Fullstop000/unio/driver"
)

// stdioTransport is the stdio boundary of the shared codex app-server child.
// Abstracted so integration tests can inject a scripted server without spawning
// a real `codex`.
type stdioTransport interface {
	stdin() io.Writer
	stdout() *bufio.Scanner
	wait() error
	kill()
	errText() string
}

// transportFactory builds a transport for the shared child. Swapped in tests.
type transportFactory func(ctx context.Context, execPath string, spec driver.AgentSpec) (stdioTransport, error)

// procTransport wraps a real `codex app-server` child.
type procTransport struct {
	cmd    *exec.Cmd
	in     io.WriteCloser
	sc     *bufio.Scanner
	stderr *boundedBuffer
}

func (p *procTransport) stdin() io.Writer       { return p.in }
func (p *procTransport) stdout() *bufio.Scanner { return p.sc }
func (p *procTransport) wait() error            { return p.cmd.Wait() }
func (p *procTransport) kill() {
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
}
func (p *procTransport) errText() string {
	if p.stderr == nil {
		return ""
	}
	return p.stderr.String()
}

func spawnProcTransport(ctx context.Context, execPath string, spec driver.AgentSpec) (stdioTransport, error) {
	cmd := exec.CommandContext(ctx, execPath, "app-server", "--listen", "stdio://")
	if spec.Cwd != "" {
		cmd.Dir = spec.Cwd
	}
	cmd.Env = mergeEnv(spec.Env)
	stderr := &boundedBuffer{limit: 64 * 1024}
	cmd.Stderr = stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, driver.NewTransportError("codex: stdin pipe: " + err.Error())
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, driver.NewTransportError("codex: stdout pipe: " + err.Error())
	}
	if err := cmd.Start(); err != nil {
		return nil, driver.NewTransportError("codex: start: " + err.Error())
	}
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	return &procTransport{cmd: cmd, in: in, sc: sc, stderr: stderr}, nil
}

func mergeEnv(extra []string) []string {
	if len(extra) == 0 {
		return os.Environ()
	}
	base := os.Environ()
	out := make([]string, 0, len(base)+len(extra))
	out = append(out, base...)
	return append(out, extra...)
}

// pending is a waiter for a JSON-RPC response, keyed by request id. The reader
// resolves it with the classified event.
type pending struct {
	method string
	ch     chan AppServerEvent
}

// process is the shared codex app-server child that multiplexes many threads
// (sessions). It implements driver.AgentProcess so a Registry can cache one per
// agent key and evict it when the child dies.
type process struct {
	execPath string
	spec     driver.AgentSpec
	factory  transportFactory
	version  string

	startOnce sync.Once
	startErr  error
	dead      atomic.Bool
	lifecycle sync.Mutex

	mu      sync.Mutex
	writeMu sync.Mutex
	tr      stdioTransport
	nextID  uint64
	// pendingReqs maps request id → waiter; the reader routes responses here.
	pendingReqs map[uint64]*pending
	// sessions maps threadId → the live session handle, so notifications route
	// to the right session. Codex 0.142.x carries threadId on notifications, so
	// routing is exact (no heuristic).
	sessions map[string]*session
	// turnToThread maps turnId → threadId (some events carry only turnId).
	turnToThread map[string]string
	// attachments includes sessions without a runtime thread ID, so process
	// death invalidates every facade attachment, not only registered threads.
	attachments map[*session]struct{}

	closed chan struct{}
}

// IsStale implements driver.AgentProcess.
func (p *process) IsStale() bool { return p.dead.Load() }

// DriverName implements driver.AgentProcess.
func (p *process) DriverName() string { return "codex" }

func newProcess(execPath string, spec driver.AgentSpec, factory transportFactory, version string) *process {
	return &process{
		execPath:     execPath,
		spec:         spec,
		factory:      factory,
		version:      version,
		pendingReqs:  make(map[uint64]*pending),
		sessions:     make(map[string]*session),
		turnToThread: make(map[string]string),
		attachments:  make(map[*session]struct{}),
		closed:       make(chan struct{}),
	}
}

// ensureStarted spawns the child and completes the initialize handshake exactly
// once. Concurrent callers block until the first finishes.
func (p *process) ensureStarted(ctx context.Context) error {
	p.startOnce.Do(func() {
		tr, err := p.factory(context.WithoutCancel(ctx), p.execPath, p.spec)
		if err != nil {
			p.startErr = err
			p.shutdown()
			return
		}
		p.mu.Lock()
		p.tr = tr
		p.mu.Unlock()

		go p.readerLoop()

		// initialize (id 0) → wait for response → send initialized.
		respCh, err := p.registerAndSend(0, "initialize", BuildInitialize(0, p.version))
		if err != nil {
			p.startErr = err
			p.shutdown()
			return
		}
		select {
		case ev := <-respCh:
			if ev.Type == EvError {
				p.startErr = driver.NewProtocolError("codex initialize failed: " + ev.ErrMsg)
				p.shutdown()
				return
			}
		case <-ctx.Done():
			p.startErr = ctx.Err()
			p.shutdown()
			return
		case <-p.closed:
			p.startErr = driver.NewTransportError(p.closedMessage("codex app-server closed during initialize"))
			return
		}
		if err := p.writeLine(BuildInitialized()); err != nil {
			p.startErr = err
			p.shutdown()
			return
		}
	})
	return p.startErr
}

// allocID returns the next request id.
func (p *process) allocID() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nextID++
	return p.nextID
}

// registerAndSend records a pending waiter for id+method, then writes the line.
// The waiter MUST be registered before the write so a fast response can't race
// ahead of the map insert.
func (p *process) registerAndSend(id uint64, method, line string) (chan AppServerEvent, error) {
	ch := make(chan AppServerEvent, 1)
	p.mu.Lock()
	p.pendingReqs[id] = &pending{method: method, ch: ch}
	p.mu.Unlock()
	if err := p.writeLine(line); err != nil {
		p.mu.Lock()
		delete(p.pendingReqs, id)
		p.mu.Unlock()
		p.shutdown()
		return ch, err
	}
	return ch, nil
}

func (p *process) writeLine(line string) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	p.mu.Lock()
	tr := p.tr
	p.mu.Unlock()
	if tr == nil {
		return driver.NewTransportError("codex app-server is not running")
	}
	payload := []byte(line + "\n")
	n, err := tr.stdin().Write(payload)
	if err != nil {
		return driver.NewTransportError("codex: write stdin: " + err.Error())
	}
	if n != len(payload) {
		return driver.NewTransportError("codex: short write to stdin")
	}
	return nil
}

// registerSession/unregisterSession maintain the threadId → session map.
func (p *process) registerSession(threadID string, s *session) {
	p.mu.Lock()
	p.sessions[threadID] = s
	p.mu.Unlock()
}

func (p *process) unregisterSession(threadID string) {
	p.mu.Lock()
	delete(p.sessions, threadID)
	p.mu.Unlock()
}

func (p *process) acquire(s *session) bool {
	p.lifecycle.Lock()
	defer p.lifecycle.Unlock()
	if p.dead.Load() {
		return false
	}
	p.attachments[s] = struct{}{}
	return true
}

// release drops one attachment lease. It shuts down the child when the last
// attachment goes away and reports whether the caller must await p.closed.
func (p *process) release(s *session) bool {
	p.lifecycle.Lock()
	delete(p.attachments, s)
	last := len(p.attachments) == 0
	shouldKill := last && !p.dead.Swap(true)
	p.lifecycle.Unlock()

	p.mu.Lock()
	hasTransport := p.tr != nil
	p.mu.Unlock()
	if shouldKill && hasTransport {
		p.killTransport()
	}
	return last && hasTransport
}

func (p *process) attachedSessions() []*session {
	p.lifecycle.Lock()
	defer p.lifecycle.Unlock()
	out := make([]*session, 0, len(p.attachments))
	for s := range p.attachments {
		out = append(out, s)
	}
	return out
}

func (p *process) sessionForThread(threadID string) *session {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sessions[threadID]
}

func (p *process) mapTurn(turnID, threadID string) {
	if turnID == "" || threadID == "" {
		return
	}
	p.mu.Lock()
	p.turnToThread[turnID] = threadID
	p.mu.Unlock()
}

func (p *process) threadForTurn(turnID string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.turnToThread[turnID]
}

func (p *process) soleInFlightSession() *session {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out *session
	for _, s := range p.sessions {
		if s.currentRun() == "" {
			continue
		}
		if out != nil {
			return nil
		}
		out = s
	}
	return out
}

func (p *process) closedMessage(prefix string) string {
	p.mu.Lock()
	tr := p.tr
	p.mu.Unlock()
	if tr == nil {
		return prefix
	}
	stderr := strings.TrimSpace(tr.errText())
	if stderr == "" {
		return prefix
	}
	return prefix + ": " + stderr
}

type boundedBuffer struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	limit int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if b.limit <= 0 {
		return n, nil
	}
	if b.buf.Len()+len(p) > b.limit {
		overflow := b.buf.Len() + len(p) - b.limit
		if overflow >= b.buf.Len() {
			b.buf.Reset()
		} else {
			cur := append([]byte(nil), b.buf.Bytes()[overflow:]...)
			b.buf.Reset()
			_, _ = b.buf.Write(cur)
		}
	}
	_, _ = b.buf.Write(p)
	return n, nil
}

func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// shutdown kills the child and marks the process stale.
func (p *process) shutdown() {
	p.lifecycle.Lock()
	if p.dead.Swap(true) {
		p.lifecycle.Unlock()
		return
	}
	p.lifecycle.Unlock()
	p.killTransport()
}

func (p *process) killTransport() {
	p.mu.Lock()
	tr := p.tr
	p.mu.Unlock()
	if tr != nil {
		tr.kill()
	}
}
