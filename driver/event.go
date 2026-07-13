package driver

// EventType tags a top-level AgentEvent. The event model is deliberately flat
// (struct + string tag) rather than a Rust-style enum-with-payload, so events
// serialise cleanly and a single envelope covers every transport.
type EventType string

const (
	// EventLifecycle: the session transitioned to a new ProcessState.
	EventLifecycle EventType = "lifecycle"
	// EventSessionAttached: a runtime session id was attached (new or resumed).
	EventSessionAttached EventType = "session_attached"
	// EventOutput: one content item from an in-flight run (see AgentEventItem).
	EventOutput EventType = "output"
	// EventCompleted: a run finished successfully; Result carries usage/finish.
	EventCompleted EventType = "completed"
	// EventFailed: a run failed; Err carries the reason.
	EventFailed EventType = "failed"
	// EventBlocked pauses a run until the host supplies external input.
	EventBlocked EventType = "blocked"
)

// ItemKind tags the content of an EventOutput item.
type ItemKind string

const (
	// ItemThinking: reasoning/thinking text.
	ItemThinking ItemKind = "thinking"
	// ItemText: assistant-visible text.
	ItemText ItemKind = "text"
	// ItemToolCall: a tool invocation. Tool + ToolInput are set. Transports MUST
	// coalesce any deferred partial tool-call updates before emitting, so callers
	// never observe a half-built tool call.
	ItemToolCall ItemKind = "tool_call"
	// ItemToolResult: the result of a tool call. Text carries the content.
	ItemToolResult ItemKind = "tool_result"
	// ItemTurnEnd: marks the end of a turn's content stream.
	ItemTurnEnd ItemKind = "turn_end"
)

// AgentEventItem is one content item emitted during a run (the payload of an
// EventOutput). Only the fields relevant to Kind are populated.
type AgentEventItem struct {
	Kind ItemKind
	// Text carries thinking/text/tool-result content.
	Text string
	// Tool is the tool name for ItemToolCall.
	Tool string
	// ToolInput is the (already-coalesced) tool input for ItemToolCall. Kept as
	// a decoded value so hosts can re-serialise or inspect it.
	ToolInput any
}

// AgentEvent is the unified event published on a Session's EventBus. It is the
// flattened union of Chorus's DriverEvent variants: Type selects which fields
// are meaningful.
//
//   - EventLifecycle       → State
//   - EventSessionAttached → SessionID
//   - EventOutput          → SessionID, RunID, Item
//   - EventCompleted       → SessionID, RunID, Result
//   - EventFailed          → SessionID, RunID, Err
type AgentEvent struct {
	Type      EventType
	SessionID SessionID
	RunID     RunID

	// State is set for EventLifecycle.
	State ProcessState
	// Item is set for EventOutput.
	Item AgentEventItem
	// Result is set for EventCompleted.
	Result RunResult
	// Err is set for EventFailed.
	Err *AgentError
	// Blocked is set for EventBlocked.
	Blocked *BlockedReason
}

// --- constructor helpers (used by drivers to keep call sites terse) ---

// LifecycleEvent builds an EventLifecycle.
func LifecycleEvent(state ProcessState) AgentEvent {
	return AgentEvent{Type: EventLifecycle, State: state, SessionID: state.SessionID, RunID: state.RunID}
}

// SessionAttachedEvent builds an EventSessionAttached.
func SessionAttachedEvent(sid SessionID) AgentEvent {
	return AgentEvent{Type: EventSessionAttached, SessionID: sid}
}

// OutputEvent builds an EventOutput.
func OutputEvent(sid SessionID, run RunID, item AgentEventItem) AgentEvent {
	return AgentEvent{Type: EventOutput, SessionID: sid, RunID: run, Item: item}
}

// CompletedEvent builds an EventCompleted.
func CompletedEvent(sid SessionID, run RunID, result RunResult) AgentEvent {
	return AgentEvent{Type: EventCompleted, SessionID: sid, RunID: run, Result: result}
}

// FailedEvent builds an EventFailed.
func FailedEvent(sid SessionID, run RunID, err *AgentError) AgentEvent {
	return AgentEvent{Type: EventFailed, SessionID: sid, RunID: run, Err: err}
}

// BlockedEvent builds an EventBlocked.
func BlockedEvent(sid SessionID, run RunID, reason BlockedReason) AgentEvent {
	return AgentEvent{Type: EventBlocked, SessionID: sid, RunID: run, Blocked: &reason}
}

// TypeName returns a short human-readable name for logging.
func (e AgentEvent) TypeName() string { return string(e.Type) }
