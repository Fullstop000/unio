# unio — Cross-Language Specification

unio is a **multi-language SDK family**: the same agent-driving abstraction is
implemented once per language (Go today; TypeScript, Rust, … later). Users call
unio in their own language and must observe **identical behaviour**.

This document is the language-neutral contract every implementation MUST honour.
The Go packages under this repo are the **reference implementation**; when this
spec and the Go code disagree, that is a bug in one of them — file it.

> Conformance: shared, language-agnostic fixtures live under `testdata/`
> (added per transport in P1+). Every implementation runs them to prove
> behavioural parity. Wire-format fixtures (ACP / Codex app-server / Claude
> stream-json) are inherently cross-language and are the backbone of conformance.

## 1. Core concepts

| Concept | Meaning |
|---|---|
| **ProtocolDriver** | Factory for one runtime kind. Owns session lifecycle. |
| **Session** | One live conversation: `run → prompt → cancel/close`. Stateful. |
| **AgentProcess** | A cached OS process a registry evicts when stale (multiplexing transports). |
| **EventBus** | Bounded, drop-on-full fan-out of `AgentEvent`. |
| **SessionKey** | Host-facing key used to correlate a live SDK session. In the Go reference this is a string. |
| **SessionID** | Runtime-owned native id (Claude sessionId, Codex thread id, ACP session id). May change on resume. Never synthesised by unio. |
| **RunID** | unio-generated id correlating a prompt with its Completed/Failed events. |

## 2. Frozen string values (the wire/behaviour contract)

These strings are the contract. Implementations MUST use these exact values;
renaming requires a spec version bump.

### 2.1 Transport
`fake`, `acp_native`, `codex_app_server`, `claude_stream_json`

### 2.2 Lifecycle phase (`ProcessState.phase`)
`idle`, `starting`, `active`, `prompt_in_flight`, `closed`, `failed`

State machine (allowed transitions):
```
idle ──run──▶ starting ──▶ active ⇄ prompt_in_flight
                              │
                              ├──close──▶ closed
                              └──fatal──▶ failed
```
- `active`: live session, no prompt in flight; carries `session_id`.
- `prompt_in_flight`: carries `session_id` + `run_id`.
- `failed`: carries an error (see §2.5).

### 2.3 Event type (`AgentEvent.type`)
`lifecycle`, `session_attached`, `output`, `completed`, `failed`

Field applicability:
| type | required fields |
|---|---|
| `lifecycle` | `key`, `state` |
| `session_attached` | `key`, `session_id` |
| `output` | `key`, `session_id`, `run_id`, `item` |
| `completed` | `key`, `session_id`, `run_id`, `result` |
| `failed` | `key`, `session_id`, `run_id`, `err` |

### 2.4 Output item kind (`AgentEventItem.kind`)
`thinking`, `text`, `tool_call`, `tool_result`, `turn_end`
- `tool_call`: carries `tool` (name) + `tool_input` (decoded value). Transports
  MUST coalesce partial/streamed tool-call updates before emitting — callers
  never see a half-built tool call.
- Every run's output stream ends with a `turn_end` item before `completed`.

### 2.5 Error kind (`AgentError.kind`)
`transport`, `protocol`, `timeout`, `runtime_reported`, `unsupported`, `not_installed`
- An error is `{kind, msg}`. `kind` is the frozen contract; `msg` is
  human-readable and NOT part of the contract.
- `not_installed`: the agent's CLI/adapter binary is not on the host. Every
  implementation MUST detect this at **OpenSession** time (before spawning) and
  return a `not_installed` error whose `msg` names the missing executable, so a
  host can tell the user what to install rather than failing obscurely later.

### 2.6 Finish reason (`RunResult.finish_reason`)
`natural`, `cancelled`, `transport_closed`

### 2.7 Cancel outcome
`aborted` (a run was in flight and was aborted), `not_in_flight` (nothing to cancel)

## 3. Data shapes (language-neutral)

```
ProcessState  { phase, session_id?, run_id?, err? }
AgentError    { kind, msg }
TokenUsage    { input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, cost_usd }
RunResult     { finish_reason, usage: map<model, TokenUsage>?, duration_ms }
AgentEventItem{ kind, text?, tool?, tool_input? }
AgentEvent    { type, key, session_id?, run_id?, state?, item?, result?, err? }
OpenParams    { resume_session_id? }   // non-empty ⇒ resume
AgentSpec     { executable_path?, alt_commands?, extra_args?, env?, cwd, model?, system_prompt? }
```

`usage` is a **first-class** field (a deliberate enhancement over the origin
abstraction) so cross-agent token/cost accounting needs no per-runtime
re-parsing. Implementations MUST populate it when the runtime reports usage.

## 4. Lifecycle contract

1. `OpenSession` returns a Session in phase `idle` plus its EventBus. Callers
   subscribe **before** `run` so no early event is missed.
2. `run` brings the session online; it MUST emit a `session_attached` event with
   the runtime-owned `session_id`, then a `lifecycle` → `active`.
3. `prompt` returns a `run_id`; the resulting `output` / `completed` / `failed`
   events carry that `run_id`.
4. `cancel(run_id)` returns `aborted` iff a run was in flight, else
   `not_in_flight`. The session remains usable after `aborted`.
5. `close` moves to `closed` and closes the EventBus. It MUST NOT tear down a
   shared runtime process while sibling sessions remain live.

## 5. Concurrency contract

A Session is safe for concurrent use — the **driver**, not the caller,
guarantees this. Hosts may call `run`/`prompt`/`cancel`/`close` and read
`session_id`/`process_state` from multiple threads without external locking.

- The mutating lifecycle methods (`run`/`prompt`/`cancel`/`close`) are
  serialised internally. Serialisation is held only across brief request/ack
  windows, never for a whole turn, so `cancel` is never blocked while a turn
  runs.
- Read methods (`session_id`, `process_state`) are lock-free snapshots and never
  block behind an in-flight `prompt`.
- `close` is idempotent. After `close`, `run`/`prompt` return an `unsupported`
  error rather than acting on a dead runtime.

## 6. Resume contract

- Resume is not a separate verb; it is `OpenParams.resume_session_id` on a normal
  open. Empty ⇒ new session; non-empty ⇒ resume that runtime-owned session.
- The id to pass is the `session_id` observed from a prior `session_attached` /
  `completed`. The host persists it against the `SessionKey`.
- Each transport SHOULD apply a **liveness guard**: if the runtime's on-disk
  session artifact for that id is gone, drop the resume id and start fresh rather
  than failing.
- On successful resume, `run` MUST re-attach the **same** `session_id`.

## 7. Back-pressure contract

The EventBus is bounded and **drops on full** rather than blocking the producer.
A slow subscriber degrades only its own stream; it MUST NOT stall the reader for
other subscribers. Implementations SHOULD expose a dropped-event counter. The
inbound queue is never closed while producers may still emit, so a reader
goroutine racing a `close` never sends on a closed channel.

## 8. Versioning

This spec is versioned. Breaking changes to any frozen value in §2 or any shape
in §3 require a major bump and coordinated updates across implementations.

**Spec version: 0.2.0 (core abstraction + concurrency contract; claude
stream-json and codex app-server transports implemented in the Go reference;
ACP-native next).**
