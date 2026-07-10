# unio cross-language specification

unio is a multi-language SDK family. Every implementation must expose the same
human-aligned behavior even though Claude Code and Codex use different runtime
protocols.

**Spec version: 0.3.0**

## 1. Public object model

- `Agent`: one configured runtime instance. It owns drivers, shared processes,
  and session handles.
- `Session`: one conversation. It exposes conversation state, not OS-process
  or transport state.
- `Stream`: observable events and the final result of one session turn.

The common flow is:

```text
New Agent -> NewSession/GetSession -> Run/Stream -> Interrupt/Continue
```

Runtime attach and resume are automatic. There is no public `Session.Close` or
`Session.Resume`; closing the `Agent` releases all SDK resources.

## 2. Frozen values

### Session state

`idle`, `running`, `blocked`

### Blocked kind

`user_input`, `tool_approval`, `permission`, `authentication`, `external`

### Stream event kind

`thinking`, `text`, `tool_call`, `tool_result`

Internal `turn_end` markers are never exposed by the public stream.

### Error kind

`transport`, `protocol`, `timeout`, `runtime_reported`, `unsupported`,
`not_installed`, `invalid_state`, `session_not_found`

### Driver transport

`fake`, `acp_native`, `codex_app_server`, `claude_stream_json`

### Internal driver lifecycle

`idle`, `starting`, `active`, `prompt_in_flight`, `blocked`, `closed`, `failed`

### Internal driver event type

`lifecycle`, `session_attached`, `output`, `blocked`, `completed`, `failed`

### Finish reason

`natural`, `cancelled`, `transport_closed`

## 3. Core API behavior

### Agent initialization

Creating an Agent resolves the concrete runtime and probes availability. A
missing CLI returns `not_installed`; an unavailable authentication state returns
an error rather than a half-initialized Agent.

One Agent owns one concrete driver for its lifetime. Multiplexing runtimes such
as Codex and ACP v1 agents share one child process across that Agent's sessions.

### New session and ID

`NewSession` creates an idle local handle without sending a hidden prompt. Its
ID is empty until the first `Run` or `Stream` starts the runtime conversation.
The SDK never synthesizes a replacement for the runtime's canonical ID.

### List and get session

`ListSessions` returns persisted runtime metadata for the Agent's working
directory by default. `SessionsIn(dir)` selects another working directory and
`AllSessions()` removes the filter. Drivers may page internally, but pagination
is not part of the public SDK contract. Unsupported listing returns an
`unsupported` error rather than an empty successful result.

`GetSession(id)` returns the one maintained handle for that runtime ID. An
unknown ID returns `session_not_found`. It performs no visible resume work; the
next `Run` or `Stream` attaches or resumes automatically.

### Run and stream

`Run` waits for a turn. `Stream` exposes text, thinking, tool calls, and tool
results before returning the same final Result semantics.

Submission errors are returned by `Stream` directly. A stream object must never
be returned with an error hidden inside it.

Only one turn may run per Session. A concurrent `Run`, `Stream`, or invalid
`Continue` returns `invalid_state`.

### Interrupt

`Interrupt` matches the user-visible coding-agent action:

- running -> interrupt -> terminal event consumed -> idle;
- blocked -> interrupt -> idle;
- idle -> interrupt is an idempotent no-op.

Confirmed interruption is normal control flow and sets `Result.Interrupted`.
Failure to deliver or confirm interruption is an error. Context cancellation
requests interruption and must not expose idle before runtime confirmation.
For a manual Stream, the Session remains running until that Stream consumes its
terminal event; a new turn is rejected before then.

### Block and continue

A blocked turn returns immediately with `Result.Blocked` and leaves the Session
in `blocked`. Blocking is normal control flow, not an error.

`BlockedReason.Options` is best-effort. `Continue` accepts an advertised option
value or free-form input when no options are available, then moves the Session
back to running.

Partial text, thinking, tool calls, and usage produced before blocking or
interruption remain in the Result.

## 4. Driver event contract

Drivers emit:

```text
session_attached -> output* -> blocked | completed | failed
```

`Continue` starts a new SDK correlation run while the runtime may continue the
same native turn. Every output or terminal event carries its SDK run ID.

Driver sessions are concurrency-safe. Lifecycle mutation is serialized only
around request/acknowledgment windows so Interrupt remains available while a
turn runs.

## 5. Back pressure

The EventBus is bounded and drop-on-full so one slow subscriber cannot block a
runtime reader. A terminal `blocked`, `completed`, or `failed` event evicts one
older buffered event rather than being dropped itself. Implementations expose a
dropped-event counter. Producers never send on a closed channel.

## 6. Versioning

Changing a frozen value or observable behavior requires a spec version bump and
coordinated implementation updates.
