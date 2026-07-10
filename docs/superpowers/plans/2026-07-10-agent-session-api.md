# Agent and Session Core API Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the package-level facade with a small `Agent -> Session -> Run/Stream` API that supports session discovery, automatic resume, observable state, interrupt, and blocked continuation.

**Architecture:** A long-lived public `Agent` owns one concrete driver and a registry of public sessions. Public sessions expose only human-observable `Idle`, `Running`, and `Blocked` states; they lazily attach to runtime sessions and translate the existing driver event bus into synchronous or streaming results. The driver contract gains interrupt and blocked-continuation primitives, while process and attachment lifecycle remain internal.

**Tech Stack:** Go 1.23, standard library concurrency and JSON packages, existing UUID dependency, Claude stream-json transport, Codex app-server JSON-RPC transport.

---

## File map

- Create `agent.go`: public `AgentKind`, `Agent`, construction, session registry, listing, lookup, and cleanup.
- Modify `facade.go`: retain shared options and public event values; remove the old package-level agent selector and resume option.
- Rewrite `session.go`: public session state machine, lazy attachment, `Run`, `Stream`, `Interrupt`, and `Continue`.
- Rewrite `stream.go`: event aggregation and terminal result semantics.
- Modify `errs/errs.go`: add stable invalid-state and session-not-found errors.
- Modify `driver/driver.go`: blocked types and the `Interrupt`/`Continue` session contract.
- Modify `driver/event.go`: add a terminal blocked event.
- Modify `driver/fake/fake.go`: deterministic blocked and interrupt behavior for facade tests.
- Modify `driver/codex/driver.go`, `driver/codex/session_events.go`, `driver/codex/reader.go`: shared process ownership, interrupt confirmation, and approval blocking.
- Modify `driver/claude/driver.go`: process-kill interrupt with a resumable next public turn.
- Create `driver/codex/sessions.go` and `driver/claude/sessions.go`: runtime history discovery.
- Rewrite `facade_test.go`: public API behavior tests.
- Modify driver tests and `tests/harness.go`: updated low-level contract and shared lifecycle assertions.
- Modify `README.md`, `SPEC.md`, `CHANGELOG.md`, examples, and real E2E tests: publish one coherent API.

### Task 1: Extend the driver contract for blocked turns and interrupt

**Files:**
- Modify: `errs/errs.go`
- Modify: `errs/errs_test.go`
- Modify: `driver/driver.go`
- Modify: `driver/event.go`
- Modify: `driver/driver_test.go`
- Modify: `driver/fake/fake.go`
- Modify: `driver/fake/fake_test.go`
- Modify: `driver/fake/concurrency_test.go`
- Modify: `driver/claude/driver.go`
- Modify: `driver/codex/driver.go`
- Modify: `driver/codex/driver_test.go`
- Modify: `session.go`
- Modify: `tests/harness.go`

- [ ] **Step 1: Write failing contract tests**

Add these assertions to `errs/errs_test.go` and `driver/driver_test.go`:

```go
func TestInvalidStateAndSessionNotFoundKinds(t *testing.T) {
    for _, tc := range []struct {
        err  error
        kind ErrorKind
    }{
        {InvalidState("busy"), KindInvalidState},
        {SessionNotFound("s-1"), KindSessionNotFound},
    } {
        got, ok := KindOf(tc.err)
        if !ok || got != tc.kind {
            t.Fatalf("KindOf(%v) = %q, %v; want %q, true", tc.err, got, ok, tc.kind)
        }
    }
}

func TestBlockedEventCarriesReason(t *testing.T) {
    reason := BlockedReason{
        Kind:    BlockedToolApproval,
        Message: "Allow go test?",
        Options: []BlockOption{{Value: "allow_once", Label: "Allow once"}},
    }
    ev := BlockedEvent("key", "sid", "run", reason)
    if ev.Type != EventBlocked || ev.Blocked == nil || ev.Blocked.Kind != BlockedToolApproval {
        t.Fatalf("unexpected blocked event: %+v", ev)
    }
}
```

- [ ] **Step 2: Run the focused tests and confirm the new contract is absent**

Run:

```bash
/usr/local/go/bin/go test ./errs ./driver
```

Expected: compilation fails because `KindInvalidState`, `BlockedReason`, and `EventBlocked` do not exist.

- [ ] **Step 3: Add the minimal error and block types**

Add to `errs/errs.go`:

```go
const (
    KindInvalidState   ErrorKind = "invalid_state"
    KindSessionNotFound ErrorKind = "session_not_found"
)

func InvalidState(msg string) *AgentError {
    return New(KindInvalidState, msg)
}

func SessionNotFound(id string) *AgentError {
    return New(KindSessionNotFound, "session not found: "+id)
}
```

Include both kinds in `ErrorKind.Valid`. Re-export them and their constructors
from `driver/driver.go`, then add:

```go
type BlockedKind string

const (
    BlockedUserInput      BlockedKind = "user_input"
    BlockedToolApproval   BlockedKind = "tool_approval"
    BlockedPermission     BlockedKind = "permission"
    BlockedAuthentication BlockedKind = "authentication"
    BlockedExternal       BlockedKind = "external"
)

type BlockOption struct {
    Value string
    Label string
}

type BlockedReason struct {
    Kind    BlockedKind
    Message string
    Options []BlockOption
}
```

Add `EventBlocked = "blocked"`, a `Blocked *BlockedReason` field on
`AgentEvent`, and this constructor to `driver/event.go`:

```go
func BlockedEvent(key SessionKey, sid SessionID, run RunID, reason BlockedReason) AgentEvent {
    return AgentEvent{
        Type: EventBlocked, Key: key, SessionID: sid, RunID: run, Blocked: &reason,
    }
}
```

Add `PhaseBlocked Phase = "blocked"` to the low-level lifecycle states. It is
used only inside drivers; the public facade still exposes exactly
`Idle/Running/Blocked`.

- [ ] **Step 4: Replace low-level cancel outcomes with human-aligned methods**

Change `driver.Session` to:

```go
type Session interface {
    Key() SessionKey
    SessionID() SessionID
    ProcessState() ProcessState
    Run(ctx context.Context, initPrompt *PromptReq) error
    Prompt(ctx context.Context, req PromptReq) (RunID, error)
    Continue(ctx context.Context, input string) (RunID, error)
    Interrupt(ctx context.Context) error
    Close(ctx context.Context) error
}
```

For this contract-only commit, rename each concrete `Cancel` implementation to
`Interrupt`, make idle interrupt return nil, and add a temporary `Continue`
implementation returning a driver-specific `NewUnsupportedError`, such as
`NewUnsupportedError("claude: no blocked turn")`.
Update `tests/harness.go` so its idle scenario calls `Interrupt` and expects nil.
Update concrete driver tests and the temporary facade method in `session.go` to
call `Interrupt`. Do not retain `CancelOutcome` or the public `Cancel` method.

- [ ] **Step 5: Run contract and race tests**

Run:

```bash
/usr/local/go/bin/go test ./errs ./driver ./driver/fake ./driver/claude ./driver/codex ./tests
/usr/local/go/bin/go test -race ./driver/... ./tests/...
```

Expected: all listed packages pass.

- [ ] **Step 6: Commit the driver contract**

```bash
git add errs driver session.go tests/harness.go
git commit -m "feat: add blocked and interrupt driver contract"
```

### Task 2: Make the fake driver model asynchronous turns

**Files:**
- Modify: `driver/fake/fake.go`
- Modify: `driver/fake/fake_test.go`
- Modify: `driver/fake/concurrency_test.go`

- [ ] **Step 1: Write failing fake-driver tests for block, continue, and interrupt**

Add tests that use a controllable script:

```go
func TestBlockedTurnContinues(t *testing.T) {
    d := New()
    key := driver.SessionKey("blocked")
    d.ScriptSession(key,
        Script{Blocked: &driver.BlockedReason{
            Kind: driver.BlockedToolApproval,
            Options: []driver.BlockOption{
                {Value: "allow_once", Label: "Allow once"},
                {Value: "deny", Label: "Deny"},
            },
        }},
        Script{Items: []driver.AgentEventItem{{Kind: driver.ItemText, Text: "continued"}}},
    )

    att, _ := d.OpenSession(context.Background(), key, driver.AgentSpec{}, driver.OpenParams{})
    events := att.Events.Subscribe()
    _ = att.Session.Run(context.Background(), nil)
    run, _ := att.Session.Prompt(context.Background(), driver.PromptReq{Text: "go"})
    ev := waitForRunEvent(t, events, run, driver.EventBlocked)
    if ev.Blocked == nil || ev.Blocked.Kind != driver.BlockedToolApproval {
        t.Fatalf("unexpected block: %+v", ev)
    }

    continued, err := att.Session.Continue(context.Background(), "allow_once")
    if err != nil {
        t.Fatal(err)
    }
    waitForRunEvent(t, events, continued, driver.EventCompleted)
}

func TestInterruptHeldTurn(t *testing.T) {
    gate := make(chan struct{})
    d, att, events := openScripted(t, Script{Wait: gate})
    _ = d
    run, _ := att.Session.Prompt(context.Background(), driver.PromptReq{Text: "long"})
    if err := att.Session.Interrupt(context.Background()); err != nil {
        t.Fatal(err)
    }
    ev := waitForRunEvent(t, events, run, driver.EventCompleted)
    if ev.Result.FinishReason != driver.FinishCancelled {
        t.Fatalf("finish = %q", ev.Result.FinishReason)
    }
}
```

Implement the small `waitForRunEvent` and `openScripted` helpers in the test
file with a two-second timeout:

```go
func waitForRunEvent(t *testing.T, events <-chan driver.AgentEvent, run driver.RunID, typ driver.EventType) driver.AgentEvent {
    t.Helper()
    timer := time.NewTimer(2 * time.Second)
    defer timer.Stop()
    for {
        select {
        case ev := <-events:
            if ev.RunID == run && ev.Type == typ {
                return ev
            }
        case <-timer.C:
            t.Fatalf("timed out waiting for %s on run %s", typ, run)
        }
    }
}

func openScripted(t *testing.T, scripts ...Script) (*Driver, *driver.SessionAttachment, <-chan driver.AgentEvent) {
    t.Helper()
    d := New()
    key := driver.SessionKey("scripted")
    d.ScriptSession(key, scripts...)
    att, err := d.OpenSession(context.Background(), key, driver.AgentSpec{}, driver.OpenParams{})
    if err != nil {
        t.Fatal(err)
    }
    events := att.Events.Subscribe()
    if err := att.Session.Run(context.Background(), nil); err != nil {
        t.Fatal(err)
    }
    t.Cleanup(func() { _ = att.Session.Close(context.Background()) })
    return d, att, events
}
```

- [ ] **Step 2: Run tests and observe the missing script controls**

Run:

```bash
/usr/local/go/bin/go test ./driver/fake
```

Expected: compilation fails because `Script.Blocked` and `Script.Wait` are absent.

- [ ] **Step 3: Implement deterministic asynchronous scripts**

Extend `Script`:

```go
type Script struct {
    Items    []driver.AgentEventItem
    Result   driver.RunResult
    FailWith *driver.AgentError
    Blocked  *driver.BlockedReason
    Wait     <-chan struct{}
    InterruptWait <-chan struct{}
}
```

Move event production into a goroutine so `Prompt` returns while a held turn is
still running. Keep the current run under the session mutex:

```go
type session struct {
    // existing fields
    currentRun driver.RunID
    interrupted bool
    blocked *driver.BlockedReason
}
```

When `Script.Blocked` is set, emit `EventBlocked`, store the block, and move to
`PhaseBlocked`. `Continue` validates the supplied value when options are
present, clears the block, and executes the next script under a fresh `RunID`.
`Interrupt` marks the held turn interrupted, emits one cancelled completion,
and returns the session to active. Ensure the script goroutine checks the
interrupted flag after its gate opens so it cannot emit a second terminal event.
When `InterruptWait` is non-nil, `Interrupt` waits on it before emitting the
cancelled completion; this is a deterministic test hook for confirmation
ordering.

- [ ] **Step 4: Run fake tests with the race detector**

Run:

```bash
/usr/local/go/bin/go test -race ./driver/fake
```

Expected: all fake tests pass with no race report.

- [ ] **Step 5: Commit the fake state machine**

```bash
git add driver/fake
git commit -m "test: model blocked and interrupted fake turns"
```

### Task 3: Introduce the long-lived Agent object

**Files:**
- Create: `agent.go`
- Modify: `facade.go`
- Rewrite: `facade_test.go`
- Modify: `session.go`

- [ ] **Step 1: Replace old facade tests with failing Agent construction tests**

Start `facade_test.go` with:

```go
func newAgentWithDriver(t *testing.T, fd *fake.Driver) *Agent {
    t.Helper()
    prev := driverOverride
    driverOverride = func(AgentKind) (driver.ProtocolDriver, bool) {
        return fd, true
    }
    t.Cleanup(func() { driverOverride = prev })
    agent, err := New(Claude)
    if err != nil {
        t.Fatal(err)
    }
    t.Cleanup(func() { _ = agent.Close() })
    return agent
}

func newFakeAgent(t *testing.T) *Agent {
    t.Helper()
    return newAgentWithDriver(t, fake.New())
}

func TestNewAgentOwnsOneDriver(t *testing.T) {
    fd := fake.New()
    installs := 0
    prev := driverOverride
    driverOverride = func(kind AgentKind) (driver.ProtocolDriver, bool) {
        installs++
        return fd, true
    }
    t.Cleanup(func() { driverOverride = prev })

    agent, err := New(Claude, WithCwd("/tmp/project"), WithModel("m1"))
    if err != nil {
        t.Fatal(err)
    }
    defer agent.Close()

    if _, err := agent.NewSession(context.Background()); err != nil {
        t.Fatal(err)
    }
    if _, err := agent.NewSession(context.Background()); err != nil {
        t.Fatal(err)
    }
    if installs != 1 {
        t.Fatalf("driver constructed %d times; want 1", installs)
    }
}

func TestNewSessionStartsIdleWithoutRuntimeID(t *testing.T) {
    agent := newFakeAgent(t)
    session, err := agent.NewSession(context.Background())
    if err != nil {
        t.Fatal(err)
    }
    if session.ID() != "" || session.State() != Idle {
        t.Fatalf("new session: id=%q state=%q", session.ID(), session.State())
    }
}

func TestNewReportsUnavailableAgent(t *testing.T) {
    fd := fake.New()
    fd.SetProbe(driver.RuntimeProbe{
        Auth: driver.AuthNotInstalled, Transport: driver.TransportFake,
    }, nil)
    prev := driverOverride
    driverOverride = func(AgentKind) (driver.ProtocolDriver, bool) { return fd, true }
    t.Cleanup(func() { driverOverride = prev })

    _, err := New(Claude)
    if kind, ok := errs.KindOf(err); !ok || kind != errs.KindNotInstalled {
        t.Fatalf("error = %v; want not_installed", err)
    }
}
```

- [ ] **Step 2: Run the facade tests and confirm the object model is absent**

Run:

```bash
/usr/local/go/bin/go test .
```

Expected: compilation fails because `AgentKind`, `New`, and the new `Agent`
methods do not exist.

- [ ] **Step 3: Add AgentKind and the Agent owner**

Rename the current string selector to:

```go
type AgentKind string

const (
    Claude AgentKind = "claude"
    Codex  AgentKind = "codex"
)
```

Create `agent.go` with:

```go
type Agent struct {
    kind   AgentKind
    cfg    config
    driver driver.ProtocolDriver

    mu       sync.Mutex
    sessions map[string]*Session
    pending  map[*Session]struct{}
    closed   bool
}

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
        return nil, errs.RuntimeReported(string(kind)+" is not authenticated")
    }
    return &Agent{
        kind: kind, cfg: buildConfig(opts), driver: d,
        sessions: make(map[string]*Session), pending: make(map[*Session]struct{}),
    }, nil
}

func (a *Agent) NewSession(ctx context.Context) (*Session, error) {
    if err := ctx.Err(); err != nil {
        return nil, err
    }
    a.mu.Lock()
    defer a.mu.Unlock()
    if a.closed {
        return nil, errs.InvalidState("agent is closed")
    }
    s := newSession(a, "")
    a.pending[s] = struct{}{}
    return s, nil
}
```

Remove the package-level `Installed` helper; successful `New` means the agent
can be initialized, and initialization failures are returned directly.

For this compile-safe intermediate commit, update the old package-level
`Run`/`Start`/`Open` selector parameter from the renamed string type to
`AgentKind`, and extend the existing `Session` struct with the new owner/key/id
fields used by `newSession`. Keep the old functions only until Task 4 replaces
the session implementation and updates examples; do not add deprecation or
compatibility branches.

`newSession` initializes an atomic state snapshot to `Idle`. Add the required
GoDoc to `Session.ID` exactly as approved in the design: it returns `""` for a
new session before the first `Run` or `Stream`, and the runtime ID afterward.

Use the existing process-local key sequence for driver correlation:

```go
func newSession(agent *Agent, id string) *Session {
    return &Session{
        agent: agent,
        key:   autoKey(agent.kind),
        id:    id,
        state: Idle,
    }
}
```

Remove `WithResume`; resume is no longer a constructor option. Do not add a
`SessionOption` type because no concrete per-session option is required yet.
Expose category sentinels for `errors.Is` without creating more result types:

```go
var (
    ErrInvalidState   = errs.New(errs.KindInvalidState, "")
    ErrSessionNotFound = errs.New(errs.KindSessionNotFound, "")
)
```

- [ ] **Step 4: Implement Agent.Close as the only public cleanup operation**

Snapshot all sessions under the agent mutex, mark the agent closed, release the
mutex, then call the private session attachment cleanup. Aggregate cleanup
errors with `errors.Join`:

```go
func (a *Agent) Close() error {
    a.mu.Lock()
    if a.closed {
        a.mu.Unlock()
        return nil
    }
    a.closed = true
    all := a.allSessionsLocked()
    a.mu.Unlock()

    var failures []error
    for _, s := range all {
        if err := s.closeAttachment(context.Background()); err != nil {
            failures = append(failures, err)
        }
    }
    return errors.Join(failures...)
}
```

Implement the snapshot helper without retaining the agent lock during driver
cleanup:

```go
func (a *Agent) allSessionsLocked() []*Session {
    out := make([]*Session, 0, len(a.sessions)+len(a.pending))
    for _, s := range a.sessions {
        out = append(out, s)
    }
    for s := range a.pending {
        out = append(out, s)
    }
    return out
}
```

There is no public `Session.Close`.

- [ ] **Step 5: Run facade and package tests**

Run:

```bash
/usr/local/go/bin/go test . ./driver/...
```

Expected: all packages pass.

- [ ] **Step 6: Commit the Agent object model**

```bash
git add agent.go facade.go session.go facade_test.go
git commit -m "feat: introduce long-lived agent instances"
```

### Task 4: Implement Run, Stream, and the public session state machine

**Files:**
- Rewrite: `session.go`
- Rewrite: `stream.go`
- Modify: `facade.go`
- Modify: `facade_test.go`
- Modify: `examples/basic/main.go`
- Modify: `examples/multi/main.go`

- [ ] **Step 1: Write failing public turn tests**

Add:

```go
func TestRunSetsRuntimeIDAndReturnsToIdle(t *testing.T) {
    agent := newFakeAgent(t)
    session, _ := agent.NewSession(context.Background())

    result, err := session.Run(context.Background(), "hello")
    if err != nil {
        t.Fatal(err)
    }
    if result.Text != "echo: hello" || session.ID() == "" || session.State() != Idle {
        t.Fatalf("result=%+v id=%q state=%q", result, session.ID(), session.State())
    }
}

func TestStreamReportsSubmissionErrorDirectly(t *testing.T) {
    agent := newFakeAgent(t)
    session, _ := agent.NewSession(context.Background())
    first, err := session.Stream(context.Background(), "one")
    if err != nil {
        t.Fatal(err)
    }
    if _, err := session.Stream(context.Background(), "two"); !errors.Is(err, ErrInvalidState) {
        t.Fatalf("second Stream error = %v", err)
    }
    _, _ = first.Result()
}

func TestStreamDoesNotExposeTurnEnd(t *testing.T) {
    agent := newFakeAgent(t)
    session, _ := agent.NewSession(context.Background())
    stream, _ := session.Stream(context.Background(), "hello")
    for stream.Next() {
        if stream.Event().Kind == EventKind(driver.ItemTurnEnd) {
            t.Fatal("turn_end must stay internal")
        }
    }
    if _, err := stream.Result(); err != nil {
        t.Fatal(err)
    }
}
```

- [ ] **Step 2: Run the tests and confirm Run/Stream are missing**

Run:

```bash
/usr/local/go/bin/go test .
```

Expected: compilation fails at the new `Session.Run` and `Session.Stream` calls.

- [ ] **Step 3: Implement lazy driver attachment**

Give public `Session` these internal fields:

```go
type Session struct {
    agent *Agent
    key   driver.SessionKey

    mu       sync.Mutex
    state    SessionState
    id       string
    inner    driver.Session
    events   <-chan driver.AgentEvent
    active   *Stream
    closedByAgent bool
}
```

Define the facade types in `facade.go` and alias the stable block contract
rather than copying it:

```go
type SessionState string

const (
    Idle    SessionState = "idle"
    Running SessionState = "running"
    Blocked SessionState = "blocked"
)

type BlockedKind = driver.BlockedKind
type BlockOption = driver.BlockOption
type BlockedReason = driver.BlockedReason

type Result struct {
    Text        string
    Thinking    string
    ToolCalls   []ToolCall
    SessionID   string
    Usage       map[string]driver.TokenUsage
    DurationMs  int64
    Interrupted bool
    Blocked     *BlockedReason
}
```

`newSession` assigns the private driver key once. Implement state helpers under
the session mutex:

```go
func (s *Session) setState(state SessionState) {
    s.mu.Lock()
    s.state = state
    s.mu.Unlock()
}

func (s *Session) compareState(old, next SessionState) bool {
    s.mu.Lock()
    defer s.mu.Unlock()
    if s.state != old {
        return false
    }
    s.state = next
    return true
}

func (s *Session) setActive(stream *Stream) {
    s.mu.Lock()
    s.active = stream
    s.mu.Unlock()
}
```

Define the stream storage and constructor in `stream.go`:

```go
type Stream struct {
    ctx    context.Context
    owner  *Session
    events <-chan driver.AgentEvent
    runID  driver.RunID

    cur      Event
    done     bool
    result   Result
    err      error
    text     []byte
    thinking []byte
}

func newStream(ctx context.Context, owner *Session, events <-chan driver.AgentEvent, runID driver.RunID) *Stream {
    return &Stream{ctx: ctx, owner: owner, events: events, runID: runID}
}
```

`ensureAttached` calls `OpenSession` with `OpenParams.ResumeSessionID = s.id`,
subscribes before `Run`, and stores the driver session and event channel. A new
session remains ID-less until a `SessionAttached` or terminal event provides the
runtime ID. A `GetSession` handle carries its known ID into `OpenParams`.

Use a monotonically increasing private key for driver correlation. It must not
be exposed as `Session.ID`.

- [ ] **Step 4: Implement Stream and Run around one event loop**

`Stream` validates `Idle`, attaches, submits `Prompt`, then returns a stream:

```go
func (s *Session) Stream(ctx context.Context, prompt string) (*Stream, error) {
    s.mu.Lock()
    if s.state != Idle {
        state := s.state
        s.mu.Unlock()
        return nil, errs.InvalidState("cannot run session while "+string(state))
    }
    s.state = Running
    s.mu.Unlock()

    if err := s.ensureAttached(ctx); err != nil {
        s.setState(Idle)
        return nil, err
    }
    runID, err := s.inner.Prompt(ctx, driver.PromptReq{Text: prompt})
    if err != nil {
        s.setState(Idle)
        return nil, err
    }
    st := newStream(ctx, s, s.events, runID)
    s.setActive(st)
    return st, nil
}

func (s *Session) Run(ctx context.Context, prompt string) (Result, error) {
    stream, err := s.Stream(ctx, prompt)
    if err != nil {
        return Result{}, err
    }
    return stream.Result()
}
```

The stream aggregates text, thinking, tool calls, session ID, usage, and
duration. It consumes `ItemTurnEnd` internally. `EventCompleted` with
`FinishCancelled` sets `Result.Interrupted`; `EventBlocked` sets
`Result.Blocked` and public state `Blocked`; success and failure return the
session to `Idle`.

Do not clear `Running` merely because the caller's context ends. Context
cancellation requests interrupt, then the stream keeps the state non-idle until
the driver emits or confirms a terminal event. After confirmation it returns
the partial result with `Interrupted == true` together with `ctx.Err()`; a
direct call to `Session.Interrupt` returns the same partial result to the
waiting `Run` or `Stream` with a nil error.

At the end of this step, delete the old package-level `Run`, `Start`, and
`Open`, plus public `Session.Prompt`, `Session.Cancel`, and `Session.Close`.
Update the basic and multi examples to construct `Agent` and call
`Agent.NewSession` so the whole repository continues to compile after this
breaking change.

- [ ] **Step 5: Run public API tests under race detection**

Run:

```bash
/usr/local/go/bin/go test -race .
```

Expected: all public API tests pass with no race report.

- [ ] **Step 6: Commit the turn state machine**

```bash
git add session.go stream.go facade.go facade_test.go examples/basic/main.go examples/multi/main.go
git commit -m "feat: add session run and streaming state machine"
```

### Task 5: Add session listing, lookup, and automatic resume

**Files:**
- Modify: `agent.go`
- Modify: `facade.go`
- Modify: `facade_test.go`

- [ ] **Step 1: Write failing list/get/identity tests**

Add:

```go
func TestListAndGetSession(t *testing.T) {
    fd := fake.New()
    fd.SetStoredSessions([]driver.StoredSessionMeta{{
        SessionID: "stored-1", Title: "auth refactor", Cwd: "/repo",
    }})
    agent := newAgentWithDriver(t, fd)

    infos, err := agent.ListSessions(context.Background())
    if err != nil || len(infos) != 1 || infos[0].ID != "stored-1" {
        t.Fatalf("infos=%+v err=%v", infos, err)
    }
    first, err := agent.GetSession(context.Background(), "stored-1")
    if err != nil {
        t.Fatal(err)
    }
    second, _ := agent.GetSession(context.Background(), "stored-1")
    if first != second || first.ID() != "stored-1" || first.State() != Idle {
        t.Fatal("GetSession must return the maintained session instance")
    }
    result, err := first.Run(context.Background(), "continue")
    if err != nil || result.Text == "" || first.ID() != "stored-1" {
        t.Fatalf("resume result=%+v id=%q err=%v", result, first.ID(), err)
    }
}

func TestGetSessionRejectsUnknownID(t *testing.T) {
    agent := newFakeAgent(t)
    _, err := agent.GetSession(context.Background(), "missing")
    if !errors.Is(err, ErrSessionNotFound) {
        t.Fatalf("error = %v", err)
    }
}
```

- [ ] **Step 2: Run the focused tests and observe missing Agent methods**

Run:

```bash
/usr/local/go/bin/go test . -run 'Test(ListAndGetSession|GetSessionRejectsUnknownID)'
```

Expected: compilation fails because `ListSessions` and `GetSession` are absent.

- [ ] **Step 3: Implement SessionInfo and stable handle identity**

Add the approved `SessionInfo` fields to `facade.go`. `ListSessions` maps
`driver.StoredSessionMeta` values and overlays any currently maintained session
IDs without producing duplicates.

`GetSession` must:

1. reject an empty ID;
2. return an existing maintained handle when present;
3. call `driver.ListSessions` and require an exact ID match;
4. create an idle lazy session initialized with that runtime ID;
5. insert it with a second locked check so concurrent callers receive one
   canonical pointer.

Do not call `OpenSession` in `GetSession`; the first `Run` or `Stream` performs
automatic resume.

- [ ] **Step 4: Register new session IDs after their first turn**

When the stream observes `SessionAttached` or completion, call an agent helper
that atomically removes the handle from `pending` and inserts it under the
runtime ID. If another different handle is already registered under that ID,
return an invalid-state error instead of silently replacing it.

- [ ] **Step 5: Run facade tests with race detection**

Run:

```bash
/usr/local/go/bin/go test -race .
```

Expected: all facade tests pass.

- [ ] **Step 6: Commit session discovery**

```bash
git add agent.go facade.go facade_test.go
git commit -m "feat: list and recover agent sessions"
```

### Task 6: Implement confirmed interrupt and blocked continuation

**Files:**
- Modify: `session.go`
- Modify: `stream.go`
- Modify: `facade_test.go`
- Modify: `driver/codex/driver.go`
- Modify: `driver/codex/session_events.go`
- Modify: `driver/codex/reader.go`
- Modify: `driver/codex/driver_test.go`
- Modify: `driver/claude/driver.go`
- Modify: `driver/claude/driver_test.go`

- [ ] **Step 1: Write failing public interrupt and continue tests**

Use the fake script controls from Task 2:

```go
func TestInterruptRunningAndIdle(t *testing.T) {
    gate := make(chan struct{})
    fd := fake.New()
    agent := newAgentWithDriver(t, fd)
    session, _ := agent.NewSession(context.Background())
    fd.ScriptSession(session.key, fake.Script{Wait: gate})

    stream, _ := session.Stream(context.Background(), "long")
    if session.State() != Running {
        t.Fatalf("state = %q", session.State())
    }
    if err := session.Interrupt(context.Background()); err != nil {
        t.Fatal(err)
    }
    result, err := stream.Result()
    if err != nil || !result.Interrupted || session.State() != Idle {
        t.Fatalf("result=%+v state=%q err=%v", result, session.State(), err)
    }
    if err := session.Interrupt(context.Background()); err != nil {
        t.Fatalf("idle interrupt: %v", err)
    }
}

func TestBlockedContinueAndInterrupt(t *testing.T) {
    fd := fake.New()
    agent := newAgentWithDriver(t, fd)
    session, _ := agent.NewSession(context.Background())
    fd.ScriptSession(session.key,
        fake.Script{Blocked: &driver.BlockedReason{
            Kind: driver.BlockedToolApproval,
            Options: []driver.BlockOption{
                {Value: "allow_once", Label: "Allow once"},
                {Value: "deny", Label: "Deny"},
            },
        }},
        fake.Script{Items: []driver.AgentEventItem{{Kind: driver.ItemText, Text: "continued"}}},
    )

    blocked, err := session.Run(context.Background(), "needs approval")
    if err != nil || blocked.Blocked == nil || session.State() != Blocked {
        t.Fatalf("result=%+v state=%q err=%v", blocked, session.State(), err)
    }
    continued, err := session.Continue(context.Background(), "allow_once")
    if err != nil || continued.Text != "continued" || session.State() != Idle {
        t.Fatalf("result=%+v state=%q err=%v", continued, session.State(), err)
    }
}
```

- [ ] **Step 2: Run public and driver tests and confirm behavior is incomplete**

Run:

```bash
/usr/local/go/bin/go test . ./driver/codex ./driver/claude
```

Expected: facade tests fail because public `Interrupt` and `Continue` are not
fully wired; driver tests fail for confirmation and block routing.

- [ ] **Step 3: Implement public Interrupt and Continue**

`Session.Interrupt` is idempotent while idle. While running or blocked, it calls
the driver, waits for driver confirmation, and only then changes state to idle.
It does not create an outcome type.

`Session.Continue` requires `Blocked`, changes to `Running`, calls the driver,
and drains a new stream for the continuation run ID. If submission fails, it
returns to `Blocked` so the caller can retry or interrupt.

```go
func (s *Session) Continue(ctx context.Context, input string) (Result, error) {
    if !s.compareState(Blocked, Running) {
        return Result{}, errs.InvalidState("continue requires a blocked session")
    }
    runID, err := s.inner.Continue(ctx, input)
    if err != nil {
        s.setState(Blocked)
        return Result{}, err
    }
    st := newStream(ctx, s, s.events, runID)
    s.setActive(st)
    return st.Result()
}
```

- [ ] **Step 4: Route Codex approval requests into blocked turns**

Store the pending server request and a per-turn completion channel on the Codex
session:

```go
type pendingBlock struct {
    requestID json.RawMessage
    reason    driver.BlockedReason
}
```

On `EvCommandApproval` and `EvFileChangeApproval`, emit `BlockedEvent` with
`allow_once` and `deny` options, clear the current phase run ID, and retain the
Codex turn ID. `Continue` accepts only advertised values and maps them as:

```go
var approvalDecision = map[string]string{
    "allow_once": "accept",
    "deny":       "decline",
    "cancel":     "cancel",
}
```

Set a fresh unio run ID before writing `BuildApprovalResponse` so subsequent
deltas cannot race ahead of correlation. `Interrupt` sends `turn/interrupt`,
then waits for `turn/completed{interrupted}` via the per-turn completion channel
before returning.

- [ ] **Step 5: Implement Claude interrupt by terminating only the active transport**

Add an atomic `interrupted` marker. `Interrupt` marks it, kills the child, waits
for the reader, and closes that low-level attachment. The reader maps EOF to
`FinishCancelled` when the marker is set and to `FinishTransportClosed`
otherwise. The public session drops a closed attachment after an interrupted
result; its next `Run` automatically opens a new Claude process with the known
session ID as resume input.

Do not claim generic blocked continuation for Claude until its transport emits
a permission/user-input request; `Continue` returns invalid state because a
Claude session cannot enter public `Blocked` without such an event.

- [ ] **Step 6: Verify context cancellation does not expose premature idle**

Add this facade test; it cancels a running `Run` context, holds driver interrupt
confirmation, and proves the state does not change early:

```go
func TestContextCancellationWaitsForInterruptConfirmation(t *testing.T) {
    turnGate := make(chan struct{})
    interruptGate := make(chan struct{})
    fd := fake.New()
    agent := newAgentWithDriver(t, fd)
    session, _ := agent.NewSession(context.Background())
    fd.ScriptSession(session.key, fake.Script{Wait: turnGate, InterruptWait: interruptGate})

    ctx, cancel := context.WithCancel(context.Background())
    done := make(chan error, 1)
    go func() {
        _, err := session.Run(ctx, "long")
        done <- err
    }()
    waitForState(t, session, Running)
    cancel()
    time.Sleep(20 * time.Millisecond)
    if session.State() != Running {
        t.Fatalf("state changed before interrupt confirmation: %q", session.State())
    }
    close(interruptGate)
    if err := <-done; !errors.Is(err, context.Canceled) {
        t.Fatalf("run error = %v; want context.Canceled", err)
    }
    if session.State() != Idle {
        t.Fatalf("state after confirmation = %q", session.State())
    }
}
```

Implement this polling test helper:

```go
func waitForState(t *testing.T, session *Session, want SessionState) {
    t.Helper()
    deadline := time.Now().Add(2 * time.Second)
    for time.Now().Before(deadline) {
        if session.State() == want {
            return
        }
        runtime.Gosched()
    }
    t.Fatalf("state = %q; want %q", session.State(), want)
}
```

Run:

```bash
/usr/local/go/bin/go test -race . ./driver/codex ./driver/claude
```

Expected: all tests pass with no race report.

- [ ] **Step 7: Commit interrupt and continuation**

```bash
git add session.go stream.go facade_test.go driver/codex driver/claude
git commit -m "feat: interrupt and continue blocked sessions"
```

### Task 7: Make one Agent share one Codex app-server process

**Files:**
- Modify: `driver/codex/driver.go`
- Modify: `driver/codex/process.go`
- Modify: `driver/codex/driver_test.go`

- [ ] **Step 1: Write a failing shared-process test**

Add a test that opens two different session keys through one Codex driver and
asserts both low-level sessions point at the same process object:

```go
func TestDriverSharesProcessAcrossSessionKeys(t *testing.T) {
    d := New()
    spec := driver.AgentSpec{ExecutablePath: fakeCodex(t)}
    first, err := d.OpenSession(context.Background(), "key-1", spec, driver.OpenParams{})
    if err != nil {
        t.Fatal(err)
    }
    second, err := d.OpenSession(context.Background(), "key-2", spec, driver.OpenParams{})
    if err != nil {
        t.Fatal(err)
    }

    firstSession := first.Session.(*session)
    secondSession := second.Session.(*session)
    if firstSession.proc != secondSession.proc {
        t.Fatal("one driver instance must share one app-server process")
    }
}
```

- [ ] **Step 2: Run the test and confirm the current registry key prevents sharing**

Run:

```bash
/usr/local/go/bin/go test ./driver/codex -run TestDriverSharesProcessAcrossSessionKeys
```

Expected: failure because the two sessions contain different process pointers.

- [ ] **Step 3: Scope process ownership to the driver instance**

Replace the session-keyed registry field with:

```go
type Driver struct {
    mu      sync.Mutex
    process *process
    factory transportFactory
}

func (d *Driver) getProcess(execPath string, spec driver.AgentSpec) *process {
    d.mu.Lock()
    defer d.mu.Unlock()
    if d.process == nil || d.process.IsStale() {
        d.process = newProcess(execPath, spec, d.factory, clientVersion)
    }
    return d.process
}
```

`OpenSession` calls `getProcess` regardless of session key. Keep notification
routing by runtime thread ID. When the last low-level session unregisters, the
process may shut down; the next open sees a stale process and creates one new
child.

- [ ] **Step 4: Run Codex concurrency tests**

Run:

```bash
/usr/local/go/bin/go test -race ./driver/codex
```

Expected: all Codex tests pass and the factory counter remains one for sibling
sessions.

- [ ] **Step 5: Commit process sharing**

```bash
git add driver/codex/driver.go driver/codex/process.go driver/codex/driver_test.go
git commit -m "fix: share codex app-server per agent"
```

### Task 8: Implement real Claude and Codex session discovery

**Files:**
- Create: `driver/claude/sessions.go`
- Create: `driver/claude/sessions_test.go`
- Create: `driver/codex/sessions.go`
- Create: `driver/codex/sessions_test.go`
- Modify: `driver/claude/driver.go`
- Modify: `driver/codex/driver.go`

- [ ] **Step 1: Write fixture-based failing tests**

For Claude, create temporary
`.claude/projects/-repo-api/session-1.jsonl` content containing a `last-prompt`
record and user/assistant records. Assert ID, title, decoded cwd, timestamps,
and message count. Use this exact fixture:

```json
{"type":"last-prompt","sessionId":"session-1","lastPrompt":"Refactor auth"}
{"type":"user","sessionId":"session-1","cwd":"/repo/api","message":{"role":"user","content":"Refactor auth"}}
{"type":"assistant","sessionId":"session-1","message":{"role":"assistant","content":"I will inspect it."}}
```

For Codex, create temporary
`.codex/sessions/2026/07/10/rollout-...-thread-1.jsonl` content beginning with:

```json
{"type":"session_meta","timestamp":"2026-07-10T02:00:00Z","payload":{"id":"thread-1","cwd":"/repo/api","timestamp":"2026-07-10T02:00:00Z"}}
```

Follow it with one user-message record and assert the same common metadata.
Use this exact second line:

```json
{"type":"event_msg","timestamp":"2026-07-10T02:00:01Z","payload":{"type":"user_message","message":"Refactor auth"}}
```
Point package-level test root variables at the temporary directories and restore
them with `t.Cleanup`.

- [ ] **Step 2: Run discovery tests and confirm ListSessions is empty**

Run:

```bash
/usr/local/go/bin/go test ./driver/claude ./driver/codex -run ListSessions
```

Expected: tests fail because both real drivers return no sessions.

- [ ] **Step 3: Implement bounded JSONL metadata scanning**

Implement one package-local scanner per runtime using `bufio.Scanner` with an
8 MiB maximum record size. Read only the fields required by
`StoredSessionMeta`; malformed files are skipped, while a root directory access
failure is returned as a transport error.

Claude rules:

- session ID comes from `sessionId`, falling back to the filename stem;
- title comes from `lastPrompt` when present;
- cwd comes from the encoded project directory name;
- message count includes user and assistant records;
- file mod time is the fallback timestamp.

Codex rules:

- session ID, cwd, and start time come from the first `session_meta.payload`;
- title comes from the first user text, truncated to 120 UTF-8 runes;
- message count includes user and assistant message records;
- file mod time is the fallback update time.

Sort newest first, then by session ID for deterministic ties. Do not expose raw
transcript content.

- [ ] **Step 4: Run discovery and facade list tests**

Run:

```bash
/usr/local/go/bin/go test ./driver/claude ./driver/codex .
```

Expected: all tests pass.

- [ ] **Step 5: Commit runtime discovery**

```bash
git add driver/claude/sessions.go driver/claude/sessions_test.go driver/claude/driver.go
git add driver/codex/sessions.go driver/codex/sessions_test.go driver/codex/driver.go
git commit -m "feat: discover persisted agent sessions"
```

### Task 9: Publish the new API and remove the competing facade

**Files:**
- Modify: `README.md`
- Modify: `SPEC.md`
- Modify: `CHANGELOG.md`
- Modify: `examples/basic/main.go`
- Modify: `examples/multi/main.go`
- Modify: `examples/sessions/main.go`
- Modify: `tests/real_claude_test.go`
- Modify: `tests/real_codex_test.go`
- Modify: `tests/fake_e2e_test.go`
- Modify: `tests/harness.go`

- [ ] **Step 1: Update examples to compile only against the object API**

The basic example must use:

```go
agent, err := unio.New(unio.Claude)
if err != nil {
    log.Fatal(err)
}
defer agent.Close()

session, err := agent.NewSession(context.Background())
if err != nil {
    log.Fatal(err)
}
result, err := session.Run(context.Background(), "Reply with exactly one word: ping")
```

The multi example creates one `Agent` per kind and one session per agent. The
sessions example calls real `ListSessions`/`GetSession` rather than maintaining
its own map.

- [ ] **Step 2: Update README and GoDoc around human actions**

Document only this primary flow:

```go
agent, err := unio.New(unio.Codex, unio.WithCwd("/path/to/repo"))
defer agent.Close()

session, err := agent.NewSession(ctx)
result, err := session.Run(ctx, "Explain this repository")
```

Include explicit comments that `session.ID()` is empty before the first run for
a new session and available immediately for `GetSession`. Document
`Idle/Running/Blocked`, `Interrupt`, blocked `Continue`, and streaming. Remove
the package-level `Run`, `Start`, `Open`, `Prompt`, `Cancel`, and public
`Session.Close` examples.

- [ ] **Step 3: Update the cross-language spec as a breaking pre-v1 contract**

Set the spec version to `0.3.0` and update frozen values:

- lifecycle includes `blocked` internally;
- event types include `blocked`;
- blocked kinds use the five approved strings;
- errors include `invalid_state` and `session_not_found`;
- session behavior uses `Run/Stream`, `Interrupt`, and `Continue`;
- public session states are only `idle`, `running`, and `blocked`;
- runtime attachment and driver close remain internal;
- new session ID timing follows the approved GoDoc.

Do not add backward-compatibility wrappers. Record the breaking facade change in
`CHANGELOG.md` under an unreleased section.

- [ ] **Step 4: Update fake and real E2E tests**

Real tests construct an agent once, create or get sessions, and use
`Session.Interrupt`. Preserve the `e2e_real` build tag and environment skips.
The default fake E2E must cover:

```text
New Agent -> NewSession -> Run -> ListSessions -> GetSession -> Run
Running -> Interrupt -> Idle
Running -> Blocked -> Continue -> Idle
```

- [ ] **Step 5: Run formatting, vet, race tests, and example builds**

Run:

```bash
/usr/local/go/bin/gofmt -w $(rg --files -g '*.go')
/usr/local/go/bin/go vet ./...
/usr/local/go/bin/go test -race ./...
/usr/local/go/bin/go build ./examples/...
```

Expected: every command exits zero; no real CLI is invoked by the default test
suite.

- [ ] **Step 6: Check the public symbol surface**

Run:

```bash
/usr/local/go/bin/go doc github.com/Fullstop000/unio
rg -n 'func (Run|Start|Open)\(' --glob '*.go' .
```

Expected: GoDoc shows `New`, `Agent`, `Session`, `Run`, `Stream`, `Interrupt`,
and `Continue`; ripgrep finds no old package-level facade functions.

- [ ] **Step 7: Commit the published API**

```bash
git add README.md SPEC.md CHANGELOG.md examples tests
git commit -m "docs: publish agent session API"
```

### Task 10: Final conformance and regression verification

**Files:**
- Modify only if a verification failure exposes a defect in files already listed above.

- [ ] **Step 1: Verify the complete repository from a clean test cache**

Run:

```bash
/usr/local/go/bin/go clean -testcache
/usr/local/go/bin/go vet ./...
/usr/local/go/bin/go test -count=1 ./...
/usr/local/go/bin/go test -race -count=1 ./...
```

Expected: all commands exit zero.

- [ ] **Step 2: Verify the worktree and commit sequence**

Run:

```bash
git status --short
git log --oneline --decorate -12
git diff HEAD~9..HEAD --check
```

Expected: worktree is clean, the implementation is split into reviewable
commits, and `git diff --check` reports no whitespace errors.

- [ ] **Step 3: Run real E2E only with explicit human authorization**

When authorized and both CLIs are authenticated:

```bash
/usr/local/go/bin/go test -tags e2e_real -count=1 ./tests/...
```

Expected: Claude and Codex create sessions, stream output, recover history, and
Codex confirms interrupt. If authorization is not given, record this check as
not run rather than claiming real-runtime verification.

- [ ] **Step 4: Commit any verification-only fixes**

If Step 1 or Step 2 required a code correction, rerun both steps and commit only
the corrected files:

```bash
git add -u
git commit -m "fix: close agent session API regressions"
```

If no correction was required, do not create an empty commit.
