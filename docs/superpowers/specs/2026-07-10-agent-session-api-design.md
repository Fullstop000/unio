# Agent and Session Core API Design

Date: 2026-07-10

## Goal

The public SDK should let application code describe the same actions a person
takes when using Claude Code or Codex:

1. Choose and configure an agent.
2. Start a new conversation or find an existing one.
3. Give the agent work.
4. Observe the work as it happens when needed.
5. Interrupt an in-progress turn.
6. Respond when the agent is blocked on external input.
7. Continue the conversation later.

The API must stay small. Runtime processes, transport attachment, persistence
paths, and protocol differences remain internal implementation details.

## Scope

This design covers only behavior common to all supported agents:

- agent initialization;
- new, listed, and existing sessions;
- synchronous and streaming turns;
- session state;
- interruption;
- blocked turns and continuation;
- SDK resource cleanup.

Agent-specific features will be added later through agent-specific extensions.
They must not enlarge or weaken the common API.

## Public object model

An `Agent` is a configured, usable agent runtime. It owns runtime drivers,
shared processes where supported, and the session registry.

A `Session` is one conversation with that agent. It owns conversation state,
not OS-process state.

```go
agent, err := unio.New(unio.Codex, options...)
if err != nil {
    return err
}
defer agent.Close()

session, err := agent.NewSession(ctx)
if err != nil {
    return err
}

result, err := session.Run(ctx, "Analyze this project")
```

`Agent.Close` is SDK resource cleanup, analogous to quitting the entire agent
application. There is no public `Session.Close`: people leave and later reopen
conversations; they do not close them as process resources.

## Core API

The intended public surface is:

```go
func New(kind AgentKind, opts ...Option) (*Agent, error)

func (a *Agent) NewSession(ctx context.Context) (*Session, error)
func (a *Agent) ListSessions(ctx context.Context) ([]SessionInfo, error)
func (a *Agent) GetSession(ctx context.Context, id string) (*Session, error)
func (a *Agent) Close() error

// ID returns the runtime-owned session ID. A new session has no runtime ID
// until its first Run or Stream starts; ID returns "" before then. A session
// returned by GetSession has its known ID immediately.
func (s *Session) ID() string
func (s *Session) State() SessionState
func (s *Session) Run(ctx context.Context, prompt string) (Result, error)
func (s *Session) Stream(ctx context.Context, prompt string) (*Stream, error)
func (s *Session) Interrupt(ctx context.Context) error
func (s *Session) Continue(ctx context.Context, input string) (Result, error)

func (s *Stream) Next() bool
func (s *Stream) Event() Event
func (s *Stream) Result() (Result, error)
```

`Run` and `Stream` are the same operation with different observation styles.
They produce the same final `Result` semantics.

Package-level `Start`, `Open`, and `Run` are not part of the primary object
model. The project is pre-v1, so the implementation should prefer one clear
model instead of preserving two competing ways to use the SDK.

## Session discovery and automatic resume

`ListSessions` lists conversations known to the underlying agent, including
persisted history. It returns metadata, not live session handles.

```go
type SessionInfo struct {
    ID           string
    Title        string
    Cwd          string
    StartedAt    time.Time
    UpdatedAt    time.Time
    MessageCount int
}
```

Fields unavailable from a runtime use their zero values. An unsupported listing
operation returns a typed unsupported error; it must not return an empty list
that looks like a successful query.

`GetSession` obtains a session instance and does not perform visible resume
work. The next `Run` or `Stream` automatically attaches to or resumes the
runtime conversation when necessary:

```go
sessions, err := agent.ListSessions(ctx)
session, err := agent.GetSession(ctx, sessions[0].ID)
result, err := session.Run(ctx, "Continue the previous work")
```

There is no public `Resume` method. Whether a runtime process is alive or needs
to be recreated is not a user-facing behavior.

`NewSession` returns a new local session handle without sending a hidden prompt.
Its runtime-owned ID is therefore unavailable until the first `Run` or `Stream`
starts the runtime conversation. `ID` returns an empty string before that point
and the runtime-owned ID afterward. This behavior must be stated in the public
GoDoc. A session returned by `GetSession` already has its known ID.

The SDK does not synthesize a replacement for the runtime's canonical session
ID.

## Session state

Public state describes only behavior a person can observe:

```go
type SessionState string

const (
    Idle    SessionState = "idle"
    Running SessionState = "running"
    Blocked SessionState = "blocked"
)
```

- `Idle`: ready for another `Run` or `Stream`.
- `Running`: currently executing a turn.
- `Blocked`: the current turn needs external intervention.

Connection, attachment, process, and transport states remain internal. Errors
are returned as errors rather than represented as a public `Failed` state.

Normal transitions are:

```text
Idle ──Run/Stream──> Running ──complete──> Idle
                           └──blocked────> Blocked
Blocked ──Continue──> Running
Running/Blocked ──Interrupt──> Idle
```

`State` is safe for concurrent reads. A second `Run`, `Stream`, or `Continue`
that is invalid for the current state returns a typed invalid-state error.

## Result and blocked turns

One `Result` type describes normal completion, interruption, and blocking. The
SDK does not introduce separate outcome types for each state.

```go
type Result struct {
    Text        string
    Thinking    string
    ToolCalls   []ToolCall
    Usage       Usage
    Interrupted bool
    Blocked     *BlockedReason
}
```

Interpretation is deliberately direct:

- normal completion: `err == nil`, `Interrupted == false`, `Blocked == nil`;
- interruption: `err == nil`, `Interrupted == true`;
- blocked: `err == nil`, `Blocked != nil`;
- operational failure: `err != nil`.

Partial text, thinking, tool calls, and usage produced before interruption or
blocking remain in the result.

A block uses one stable shape:

```go
type BlockedReason struct {
    Kind    BlockedKind
    Message string
    Options []BlockOption
}

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
```

Common kinds include user input, tool approval, permission, authentication, and
other external intervention. `Options` is best-effort and may be empty. When it
is populated, callers pass an option's `Value` to `Continue`; otherwise they
pass free-form input.

```go
result, err := session.Run(ctx, "Update the authentication package")
if result.Blocked != nil {
    result, err = session.Continue(ctx, "allow_once")
}
```

`Continue` is the only common blocked-resolution method. Agent-specific APIs
may later expose richer typed controls without changing this core path.

## Interrupt

The public operation is named `Interrupt`, matching the action shown by coding
agent interfaces. It is not named `Cancel`, which is commonly confused with
context cancellation or request cleanup.

Semantics are:

- from `Running`, interrupt the active turn and move to `Idle`;
- from `Blocked`, abandon the suspended turn and move to `Idle`;
- from `Idle`, do nothing and return nil;
- interruption is normal control flow, not an error;
- failure to deliver or confirm the interrupt is an error.

Any waiting `Run`, `Stream`, or `Continue` returns the partial result with
`Interrupted` set after the runtime confirms interruption.

Context cancellation on `Run`, `Stream`, or `Continue` requests an interrupt.
The session must not report `Idle` while the underlying turn is still running.

## Streaming

Stream creation reports submission errors immediately:

```go
stream, err := session.Stream(ctx, "Refactor the auth module")
if err != nil {
    return err
}

for stream.Next() {
    event := stream.Event()
    // text, thinking, tool call, or tool result
}

result, err := stream.Result()
```

`Stream.Result` uses exactly the same normal, interrupted, blocked, and error
semantics as `Run`. Internal turn-end markers are not exposed as public content
events; `Next` returning false already expresses that the stream has stopped.

## Runtime ownership

Each `Agent` owns its concrete driver for the lifetime of the instance. This
allows Codex sessions to share one app-server process while Claude remains free
to use one process per conversation. Session handles do not expose this
difference.

The SDK may automatically detach or release resources for idle sessions. Such
cleanup does not change their public `Idle` state because the next `Run` or
`Stream` restores the runtime connection transparently.

`Agent.Close` interrupts or tears down owned work and releases all processes and
goroutines. Calls made through that agent after close return an error. This is
agent-instance resource lifecycle, not session conversation state.

## Error handling

Errors are reserved for failures and invalid API use. Normal completion,
blocking, and confirmed interruption are represented by `Result`.

At minimum, callers must be able to distinguish:

- not installed or unavailable agent;
- invalid session ID;
- invalid operation for the current session state;
- transport or protocol failure;
- runtime-reported failure;
- context cancellation or timeout.

The public API returns submission errors directly instead of returning a stream
or session object that internally contains an error.

## Verification

The implementation must prove the design with shared tests covering:

- new session, run, and multi-turn reuse;
- empty new-session ID before the first turn and runtime ID afterward;
- list, get, and automatic resume;
- `Idle -> Running -> Idle`;
- `Running -> Blocked -> Continue -> Running`;
- interrupt from running and blocked states;
- idle interrupt as an idempotent no-op;
- synchronous and streaming result parity;
- context cancellation without premature idle state;
- concurrent state reads and interrupt calls;
- Codex process sharing within one agent instance;
- no process-sharing assumptions in the public session API;
- unsupported session listing reported as an error.

Fake-driver tests remain the default deterministic suite. Opt-in real Claude and
Codex tests verify transport behavior and automatic resume.

## Non-goals

This design does not yet standardize:

- agent-specific model, sandbox, approval, or reasoning controls;
- session deletion, archiving, renaming, or branching;
- pagination and search syntax for large session histories;
- typed per-agent blocked-resolution payloads;
- a package-global agent or session registry.

These should be added only after a real use case requires them.
