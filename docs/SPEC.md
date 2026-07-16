# unio cross-language specification

This specification defines the language-neutral behavior shared by the Go and
Python SDKs. Implementations preserve the same observable contract even when
language idioms and runtime protocols differ.

**Spec version: 0.7.0**

## 1. Public object model

- `Agent`: one configured runtime instance. It owns drivers, shared processes,
  and session handles.
- `Session`: one conversation. It exposes conversation state, not OS-process
  or transport state.
- `Stream`: observable events and the final result of one session turn.

The common flow is:

```text
New Agent -> NewSession/GetSession -> Run/Stream -> Interrupt
```

Runtime attach and resume are automatic. There is no public `Session.Close` or
`Session.Resume`; closing the `Agent` releases all SDK resources.

## 2. Frozen values

The machine-readable mirror used by implementation tests is
[`contract.json`](contract.json). This document remains normative. Its
`spec_version` identifies the specification revision; the filename remains
stable across revisions.

### Agent kind

`claude`, `codex`, `kimi`, `traex`, `opencode`

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

### Session data format

`jsonl`

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

Creating an Agent resolves the concrete runtime and probes executable
availability. A missing CLI returns `not_installed`. Authentication, model,
network, and provider errors may surface when the first runtime operation starts;
a successful `New` does not guarantee that the CLI is authenticated.

Each language exposes one Agent lifecycle owner. Closing or cancelling it
closes the Agent and every Session derived from it. A turn is interrupted with
the Session interruption operation rather than by discarding its result.

One Agent owns one concrete driver for its lifetime. Multiplexing runtimes such
as Codex and ACP v1 agents share one child process across that Agent's sessions.

### New session and ID

`NewSession` creates an idle local handle without sending a hidden prompt. Its
ID is empty until the first `Run` or `Stream` starts the runtime conversation.
The SDK never synthesizes a replacement for the runtime's canonical ID.

### List and get session

The session-listing operation returns persisted runtime metadata for the
Agent's working directory by default. Callers can select another working
directory, remove the filter, and apply a positive final-result limit. Drivers
may page internally, but pagination is not part of the public SDK contract.
Unsupported listing returns an `unsupported` error rather than an empty
successful result.

`GetSession(id)` returns the one maintained handle for that runtime ID. An
unknown ID returns `session_not_found`. It performs no visible resume work; the
next `Run` or `Stream` attaches or resumes automatically.

### Run and stream

`Run` waits for a submission. `Stream` exposes text, thinking, tool calls, and
tool results before returning the same final Result semantics. Both accept the
same `UserInput` sum type:

- `UserMessage`: natural-language user input;
- `OptionSelection`: the value of an option advertised by `Result.Blocked`.

Session state selects the operation. On an idle Session, `UserMessage` starts a
new runtime turn. On a blocked Session, the input answers the pending
interaction. A blocked interaction with options requires `OptionSelection`; a
free-form interaction without options requires `UserMessage`.

Submission errors are returned by `Stream` directly. A stream object must never
be returned with an error hidden inside it.

Only one turn may run per Session. A concurrent submission or an input variant
that is incompatible with the current state returns `invalid_state` and leaves
the Session in its previous usable state.

### Interrupt

`Interrupt` matches the user-visible coding-agent action:

- running -> interrupt -> terminal event consumed -> idle;
- blocked -> interrupt -> idle;
- idle -> interrupt is an idempotent no-op.

Confirmed interruption is normal control flow and sets `Result.Interrupted`.
Failure to deliver or confirm interruption is an error. Closing or cancelling
the Agent lifecycle terminates the Agent instead of acting as a reusable
per-turn interruption. For a manual Stream, the Session remains running until
that Stream consumes its terminal event; a new turn is rejected before then.

### Block and respond

A blocked turn returns immediately with `Result.Blocked` and leaves the Session
in `blocked`. Blocking is normal control flow, not an error.

`BlockedReason.Options` is best-effort. Callers respond through `Run` or
`Stream`: submit `OptionSelection` for an advertised value, or `UserMessage`
when no options are available. A valid response moves the Session back to
running.

Partial text, thinking, tool calls, and usage produced before blocking or
interruption remain in the Result.

### Token usage and session statistics

`Result.Usage` describes one turn. It is populated only from usage associated
with that turn and must not contain an entire session's cumulative usage.

`Session.Raw` is a separate, optional capability that returns the complete
runtime-owned persisted session representation and its format. It requires an
idle Session with a runtime ID.

`Session.TokenStatistics` parses the data returned by `Session.Raw` into a
session-wide aggregate. It never reads another source. Unsupported runtimes
return `unsupported`; an idle new Session without an ID returns `invalid_state`.
An incomplete persisted turn returns `protocol` rather than a partial aggregate.

Input token statistics include cached input. Cache-read and cache-write values
are also exposed separately when present. Missing cost data remains zero.

## 4. Driver event contract

Drivers emit:

```text
session_attached -> output* -> blocked | completed | failed
```

Responding to a blocked interaction starts a new SDK correlation run while the
runtime may continue the same native turn. Every output or terminal event
carries its SDK run ID.

Driver sessions are concurrency-safe. Lifecycle mutation is serialized only
around request/acknowledgment windows so Interrupt remains available while a
turn runs.

## 5. Back pressure

The internal driver event queue is bounded and drop-on-full so one slow
subscriber cannot block a runtime reader. A terminal `blocked`, `completed`, or
`failed` event evicts one older buffered event rather than being dropped itself.
Driver APIs expose a dropped-event counter where supported. The public `Stream`
does not expose that counter, so callers must not assume every intermediate
event is delivered to a slow consumer. Producers never send to a closed queue.

## 6. Versioning

Changing a frozen value or observable behavior requires a spec version bump and
coordinated implementation updates.
