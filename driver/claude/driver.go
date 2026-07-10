package claude

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/Fullstop000/unio/driver"
)

// Driver implements driver.ProtocolDriver for Claude Code headless mode. Claude
// is process-per-session, so the driver holds no shared child; each session owns
// its own. The factory field lets tests inject a scripted transport.
type Driver struct {
	factory transportFactory
}

// New constructs a Claude driver using the real `claude` process transport.
func New() *Driver {
	return &Driver{factory: spawnProcTransport}
}

// newWithTransport constructs a driver with an injected transport factory (used
// by integration tests).
func newWithTransport(f transportFactory) *Driver {
	return &Driver{factory: f}
}

// Transport implements driver.ProtocolDriver.
func (d *Driver) Transport() driver.Transport { return driver.TransportClaudeStreamJSON }

// Probe reports installed/authed state. Auth beyond "installed" is not
// separately detectable without a network call, so a present binary reports
// authed; a missing one reports not-installed.
func (d *Driver) Probe(ctx context.Context) (driver.RuntimeProbe, error) {
	spec := driver.AgentSpec{ExecutablePath: "claude"}
	if _, err := driver.ResolveExecutable(spec); err != nil {
		return driver.RuntimeProbe{Auth: driver.AuthNotInstalled, Transport: driver.TransportClaudeStreamJSON}, nil
	}
	return driver.RuntimeProbe{Auth: driver.AuthAuthed, Transport: driver.TransportClaudeStreamJSON}, nil
}

func (d *Driver) ListSessions(ctx context.Context) ([]driver.StoredSessionMeta, error) {
	return listStoredSessions(ctx)
}

// OpenSession resolves the executable (surfacing not_installed early) and builds
// an idle session handle. It does not spawn until Run.
func (d *Driver) OpenSession(ctx context.Context, key driver.SessionKey, spec driver.AgentSpec, params driver.OpenParams) (*driver.SessionAttachment, error) {
	if spec.ExecutablePath == "" {
		spec.ExecutablePath = "claude"
	}
	execPath, aerr := driver.ResolveExecutable(spec)
	if aerr != nil {
		return nil, aerr
	}

	bus := driver.NewEventBus()
	h := &handle{
		key:      key,
		spec:     spec,
		execPath: execPath,
		resume:   params.ResumeSessionID,
		factory:  d.factory,
		bus:      bus,
		acc:      newToolCallAccumulator(),
	}
	h.state.Store(&driver.ProcessState{Phase: driver.PhaseIdle})
	return &driver.SessionAttachment{Session: h, Events: bus}, nil
}

// handle is one Claude session (one child process). Implements driver.Session.
type handle struct {
	key      driver.SessionKey
	spec     driver.AgentSpec
	execPath string
	resume   driver.SessionID
	factory  transportFactory
	bus      *driver.EventBus
	acc      *toolCallAccumulator

	// lmu serialises the mutating lifecycle methods (Run/Prompt/Cancel/Close) so
	// the driver — not the caller — guarantees the Session is safe for concurrent
	// use (SPEC §Concurrency). Distinct from mu, which is the fine-grained guard
	// for tr/done shared with the reader loop. Held only across brief windows
	// (Prompt returns after the stdin write, not for the whole turn).
	lmu     sync.Mutex
	lclosed bool

	mu        sync.Mutex
	tr        transport
	sessionID atomic.Pointer[string]
	state     atomic.Pointer[driver.ProcessState]

	// runID of the currently in-flight turn; empty when idle.
	curRun atomic.Pointer[string]
	// streamedThisTurn is set when a stream_event delta produced output for the
	// current turn, so a trailing complete `assistant` message is treated as a
	// duplicate and skipped. Environments that DON'T stream deltas leave it
	// false, so the complete message is used as the content source instead.
	streamedThisTurn atomic.Bool
	interrupted      atomic.Bool
	// done is closed when the reader loop exits.
	done chan struct{}
}

func (h *handle) Key() driver.SessionKey { return h.key }

func (h *handle) SessionID() driver.SessionID {
	if p := h.sessionID.Load(); p != nil {
		return *p
	}
	return ""
}

func (h *handle) ProcessState() driver.ProcessState {
	if p := h.state.Load(); p != nil {
		return *p
	}
	return driver.ProcessState{Phase: driver.PhaseIdle}
}

func (h *handle) setState(st driver.ProcessState) {
	h.state.Store(&st)
	h.bus.Emit(driver.LifecycleEvent(h.key, st))
}

func (h *handle) setSessionID(sid string) {
	h.sessionID.Store(&sid)
}

// buildArgs assembles the claude headless argv. --resume is added only when the
// on-disk transcript for the prior session exists (liveness guard), else we
// start fresh.
func (h *handle) buildArgs() []string {
	args := []string{
		"-p",
		"--output-format", "stream-json",
		"--input-format", "stream-json",
		"--verbose",
		"--include-partial-messages",
	}
	if h.spec.Model != "" {
		args = append(args, "--model", h.spec.Model)
	}
	if h.spec.SystemPrompt != "" {
		args = append(args, "--append-system-prompt", h.spec.SystemPrompt)
	}
	if h.resume != "" && claudeSessionAlive(h.spec.Cwd, h.resume) {
		args = append(args, "--resume", h.resume)
	}
	args = append(args, h.spec.ExtraArgs...)
	return args
}

// Run spawns the child and starts the reader loop. If initPrompt is provided it
// is sent as the first user message (Claude requires stdin to emit system.init).
func (h *handle) Run(ctx context.Context, initPrompt *driver.PromptReq) error {
	h.lmu.Lock()
	if h.lclosed {
		h.lmu.Unlock()
		return driver.NewUnsupportedError("claude: session is closed")
	}
	h.setState(driver.ProcessState{Phase: driver.PhaseStarting})

	tr, err := h.factory(ctx, h.execPath, h.buildArgs(), h.spec)
	if err != nil {
		st := driver.ProcessState{Phase: driver.PhaseFailed}
		if ae, ok := err.(*driver.AgentError); ok {
			st.Err = ae
		} else {
			st.Err = driver.NewTransportError(err.Error())
		}
		h.setState(st)
		h.lmu.Unlock()
		return err
	}

	h.mu.Lock()
	h.tr = tr
	h.done = make(chan struct{})
	h.mu.Unlock()

	go h.readerLoop()

	// If a resume id was requested, pre-seed it so SessionID() is meaningful
	// even before the CLI echoes system.init (the reader overwrites it with the
	// authoritative value).
	if h.resume != "" {
		h.setSessionID(h.resume)
	}
	h.lmu.Unlock()

	if initPrompt != nil {
		if _, err := h.Prompt(ctx, *initPrompt); err != nil {
			return err
		}
	}
	return nil
}

// Prompt writes a user-message line to stdin and marks the turn in flight. The
// resulting Output/Completed events arrive via the reader loop.
func (h *handle) Prompt(ctx context.Context, req driver.PromptReq) (driver.RunID, error) {
	h.lmu.Lock()
	defer h.lmu.Unlock()
	if h.lclosed {
		return "", driver.NewUnsupportedError("claude: session is closed")
	}
	h.mu.Lock()
	tr := h.tr
	h.mu.Unlock()
	if tr == nil {
		return "", driver.NewTransportError("claude: prompt before Run")
	}

	runID := driver.NewRunID()
	h.curRun.Store(&runID)
	h.streamedThisTurn.Store(false)
	h.setState(driver.ProcessState{Phase: driver.PhasePromptInFlight, SessionID: h.SessionID(), RunID: runID})

	line := BuildUserMessage(req.Text) + "\n"
	if _, err := tr.stdin().Write([]byte(line)); err != nil {
		aerr := driver.NewTransportError("claude: write stdin: " + err.Error())
		h.bus.Emit(driver.FailedEvent(h.key, h.SessionID(), runID, aerr))
		return runID, aerr
	}
	return runID, nil
}

// Interrupt terminates the active headless process. The public session can
// transparently resume the runtime-owned session id on its next turn.
func (h *handle) Interrupt(ctx context.Context) error {
	h.lmu.Lock()
	if h.lclosed {
		h.lmu.Unlock()
		return nil
	}
	if p := h.curRun.Load(); p == nil || *p == "" {
		h.lmu.Unlock()
		return nil
	}
	h.lclosed = true
	h.interrupted.Store(true)
	h.mu.Lock()
	tr := h.tr
	done := h.done
	h.mu.Unlock()
	h.lmu.Unlock()
	if tr != nil {
		tr.kill()
	}
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	h.setState(driver.ProcessState{Phase: driver.PhaseClosed, SessionID: h.SessionID()})
	h.bus.Close()
	return nil
}

// Continue returns unsupported because this transport cannot currently emit a
// blocked permission/user-input event.
func (h *handle) Continue(ctx context.Context, input string) (driver.RunID, error) {
	return "", driver.NewUnsupportedError("claude: no blocked turn")
}

// Close terminates the child and closes the event bus. Idempotent; after Close,
// Run/Prompt return an error.
func (h *handle) Close(ctx context.Context) error {
	h.lmu.Lock()
	if h.lclosed {
		h.lmu.Unlock()
		return nil
	}
	h.lclosed = true
	h.lmu.Unlock()

	h.mu.Lock()
	tr := h.tr
	done := h.done
	h.mu.Unlock()

	if tr != nil {
		tr.kill()
	}
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
		}
	}
	h.setState(driver.ProcessState{Phase: driver.PhaseClosed, SessionID: h.SessionID()})
	h.bus.Close()
	return nil
}

// readerLoop consumes the child's stdout, mapping HeadlessEvents to AgentEvents.
func (h *handle) readerLoop() {
	h.mu.Lock()
	tr := h.tr
	done := h.done
	h.mu.Unlock()
	defer close(done)

	sc := tr.stdout()
	for sc.Scan() {
		h.handleLine(sc.Text())
	}

	// stdout closed: if a turn was in flight, report transport-closed.
	if p := h.curRun.Load(); p != nil && *p != "" {
		run := *p
		h.curRun.Store(ptr(""))
		finish := driver.FinishTransportClosed
		if h.interrupted.Swap(false) {
			finish = driver.FinishCancelled
		}
		h.bus.Emit(driver.OutputEvent(h.key, h.SessionID(), run, driver.AgentEventItem{Kind: driver.ItemTurnEnd}))
		h.bus.Emit(driver.CompletedEvent(h.key, h.SessionID(), run, driver.RunResult{FinishReason: finish}))
	}
}

// handleLine parses and dispatches one stdout line.
func (h *handle) handleLine(line string) {
	ev := ParseLine(line)
	run := h.currentRun()

	switch ev.Type {
	case EvSystemInit:
		if ev.SessionID != "" {
			h.setSessionID(ev.SessionID)
			h.bus.Emit(driver.SessionAttachedEvent(h.key, ev.SessionID))
			h.setState(driver.ProcessState{Phase: driver.PhaseActive, SessionID: ev.SessionID})
		}
	case EvThinkingDelta:
		h.streamedThisTurn.Store(true)
		h.emitItem(run, driver.AgentEventItem{Kind: driver.ItemThinking, Text: ev.Text})
	case EvTextDelta:
		h.streamedThisTurn.Store(true)
		h.emitItem(run, driver.AgentEventItem{Kind: driver.ItemText, Text: ev.Text})
	case EvToolUseStart:
		h.acc.start(ev.Index, ev.ToolID, ev.ToolName)
	case EvInputJSONDelta:
		h.acc.appendJSON(ev.Index, ev.PartialJSON)
	case EvContentBlockStop:
		// Emit a coalesced tool call if this block was a tool_use; text/thinking
		// blocks share this event and finish() returns ok=false for them.
		if name, input, ok := h.acc.finish(ev.Index); ok {
			h.streamedThisTurn.Store(true)
			h.emitItem(run, driver.AgentEventItem{Kind: driver.ItemToolCall, Tool: name, ToolInput: input})
		}
	case EvAssistantMessage:
		// Fallback for environments that deliver a whole assistant message
		// instead of streamed deltas. Skip if we already streamed this turn to
		// avoid duplicating content.
		if !h.streamedThisTurn.Load() {
			h.emitCompleteMessage(run, ev.Content)
		}
	case EvTurnResult:
		h.finishTurn(run, ev)
	case EvApiRetry, EvUnknown, EvToolUseStop:
		// no-op
	}
}

// emitCompleteMessage emits Output items for a fully-formed assistant message
// (the non-streaming path).
func (h *handle) emitCompleteMessage(run driver.RunID, content []ContentItem) {
	for _, c := range content {
		switch c.Kind {
		case "thinking":
			h.emitItem(run, driver.AgentEventItem{Kind: driver.ItemThinking, Text: c.Text})
		case "text":
			h.emitItem(run, driver.AgentEventItem{Kind: driver.ItemText, Text: c.Text})
		case "tool_use":
			h.emitItem(run, driver.AgentEventItem{Kind: driver.ItemToolCall, Tool: c.ToolName, ToolInput: c.ToolInput})
		}
	}
}

func (h *handle) finishTurn(run driver.RunID, ev HeadlessEvent) {
	if ev.SessionID != "" {
		h.setSessionID(ev.SessionID)
	}
	// Close the turn's content stream.
	h.emitItem(run, driver.AgentEventItem{Kind: driver.ItemTurnEnd})

	if ev.IsError {
		aerr := driver.NewRuntimeReportedError(ev.Result)
		h.bus.Emit(driver.FailedEvent(h.key, h.SessionID(), run, aerr))
	} else {
		result := driver.RunResult{
			FinishReason: driver.FinishNatural,
			DurationMs:   ev.DurationMs,
		}
		if ev.CostUSD != 0 || ev.InputTokens != 0 || ev.OutputTokens != 0 {
			result.Usage = map[string]driver.TokenUsage{
				modelKey(h.spec.Model): {
					InputTokens:      ev.InputTokens,
					OutputTokens:     ev.OutputTokens,
					CacheReadTokens:  ev.CacheReadTokens,
					CacheWriteTokens: ev.CacheWriteTokens,
					CostUSD:          ev.CostUSD,
				},
			}
		}
		h.bus.Emit(driver.CompletedEvent(h.key, h.SessionID(), run, result))
	}
	h.curRun.Store(ptr(""))
	h.setState(driver.ProcessState{Phase: driver.PhaseActive, SessionID: h.SessionID()})
}

func (h *handle) emitItem(run driver.RunID, item driver.AgentEventItem) {
	h.bus.Emit(driver.OutputEvent(h.key, h.SessionID(), run, item))
}

func (h *handle) currentRun() driver.RunID {
	if p := h.curRun.Load(); p != nil {
		return *p
	}
	return ""
}

func ptr[T any](v T) *T { return &v }

func modelKey(model string) string {
	if model == "" {
		return "claude"
	}
	return model
}

// claudeSessionAlive reports whether the on-disk transcript for a prior session
// exists, so resume only adds --resume when it can actually succeed. Path:
// ~/.claude/projects/<encoded-cwd>/<session>.jsonl.
func claudeSessionAlive(cwd, sessionID string) bool {
	home, err := homeDir()
	if err != nil || sessionID == "" {
		return false
	}
	encoded := encodeCwd(cwd)
	p := filepath.Join(home, ".claude", "projects", encoded, sessionID+".jsonl")
	return fileExists(p)
}

// Compile-time interface checks.
var (
	_ driver.ProtocolDriver = (*Driver)(nil)
	_ driver.Session        = (*handle)(nil)
)

// small fs helpers kept here to avoid an extra file; overridable in tests.
var (
	homeDir    = defaultHomeDir
	fileExists = defaultFileExists
)
