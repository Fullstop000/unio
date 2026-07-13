// Package driver defines unio's core abstraction: a unified, stateful way to
// drive heterogeneous coding agents (Claude Code, Codex, ACP-native agents such
// as Trae/Kimi/OpenCode, …) behind one interface.
//
// This package is the pure abstraction leaf. It depends only on the Go standard
// library plus a UUID helper — never on any host application (niva or otherwise)
// and never on a concrete transport. Concrete transports live in sub-packages
// (driver/codex, driver/claude) and the in-memory test double lives in
// driver/fake.
//
// # Design philosophy (ported from Chorus)
//
//   - Abstraction vs. glue are physically separated: this package holds only
//     interfaces and plain-data types. Process handling, protocol I/O, and host
//     wiring live elsewhere.
//   - Enums-with-payload from the Rust original become struct + string type-tag
//     here, which is idiomatic Go and keeps the event envelope flat and
//     serialisable.
//   - The three responsibilities are kept distinct: Driver (a factory +
//     session-lifecycle owner), Session (one live conversation handle), and
//     AgentProcess (a cached OS process that a Registry evicts when stale).
//
// A driver turns "run this prompt, stream the result, let me cancel/resume it"
// into a transport-specific reality, while the caller sees one Session interface
// no matter which agent backs it.
package driver

import (
	"time"

	"github.com/Fullstop000/unio/errs"
)

// Phase is the lifecycle phase of a Session's underlying runtime, as observed by
// the driver. It is the flattened form of Chorus's ProcessState enum.
type Phase string

const (
	// PhaseIdle: handle constructed but Run has not been called.
	PhaseIdle Phase = "idle"
	// PhaseStarting: the runtime process is spinning up.
	PhaseStarting Phase = "starting"
	// PhaseActive: the runtime is live with a session; no prompt in flight.
	PhaseActive Phase = "active"
	// PhasePromptInFlight: a prompt is currently executing on the live session.
	PhasePromptInFlight Phase = "prompt_in_flight"
	// PhaseBlocked: a prompt is paused awaiting external intervention.
	PhaseBlocked Phase = "blocked"
	// PhaseClosed: the handle has been closed and cannot be reused.
	PhaseClosed Phase = "closed"
	// PhaseFailed: the handle is in a non-recoverable error state.
	PhaseFailed Phase = "failed"
)

// ProcessState is a snapshot of a Session's lifecycle. SessionID is set once the
// runtime assigns one (PhaseActive onward); RunID is set only while a prompt is
// in flight; Err is set only in PhaseFailed.
type ProcessState struct {
	Phase     Phase
	SessionID SessionID
	RunID     RunID
	Err       *AgentError
}

// The error contract lives in the errs package (a cross-language contract; see
// errs and SPEC.md). driver re-exports the pieces callers touch most so they
// can stay on the driver import, while errs remains the single source of truth
// for error categories and their frozen string values.
type (
	// ErrorKind is re-exported from errs; see errs.ErrorKind.
	ErrorKind = errs.ErrorKind
	// AgentError is re-exported from errs; see errs.AgentError.
	AgentError = errs.AgentError
)

// Error-kind constants re-exported from errs.
const (
	ErrTransport       = errs.KindTransport
	ErrProtocol        = errs.KindProtocol
	ErrTimeout         = errs.KindTimeout
	ErrRuntimeReported = errs.KindRuntimeReported
	ErrUnsupported     = errs.KindUnsupported
	ErrNotInstalled    = errs.KindNotInstalled
	ErrInvalidState    = errs.KindInvalidState
	ErrSessionNotFound = errs.KindSessionNotFound
)

// Error constructors re-exported from errs, keeping the driver-local names
// drivers already use.
var (
	// NewTransportError builds a transport-category AgentError.
	NewTransportError = errs.Transport
	// NewProtocolError builds a protocol-category AgentError.
	NewProtocolError = errs.Protocol
	// NewTimeoutError builds a timeout-category AgentError.
	NewTimeoutError = errs.Timeout
	// NewRuntimeReportedError builds a runtime-reported AgentError.
	NewRuntimeReportedError = errs.RuntimeReported
	// NewUnsupportedError builds an unsupported-operation AgentError.
	NewUnsupportedError = errs.Unsupported
	// NewNotInstalledError builds a not-installed AgentError naming the command.
	NewNotInstalledError    = errs.NotInstalledCmd
	NewInvalidStateError    = errs.InvalidState
	NewSessionNotFoundError = errs.SessionNotFound
)

// BlockedKind identifies why a running agent needs external intervention.
type BlockedKind string

const (
	BlockedUserInput      BlockedKind = "user_input"
	BlockedToolApproval   BlockedKind = "tool_approval"
	BlockedPermission     BlockedKind = "permission"
	BlockedAuthentication BlockedKind = "authentication"
	BlockedExternal       BlockedKind = "external"
)

// BlockOption is one best-effort response advertised by a blocked runtime.
type BlockOption struct {
	Value string
	Label string
}

// BlockedReason describes the external input a running turn needs.
type BlockedReason struct {
	Kind    BlockedKind
	Message string
	Options []BlockOption
}

// FinishReason is why a run ended.
type FinishReason string

const (
	// FinishNatural: the runtime finished the turn normally.
	FinishNatural FinishReason = "natural"
	// FinishCancelled: the run was cancelled before completion.
	FinishCancelled FinishReason = "cancelled"
	// FinishTransportClosed: transport closed mid-run (process exit, EOF).
	FinishTransportClosed FinishReason = "transport_closed"
)

// TokenUsage tracks token consumption for a single model within one run.
//
// This is a unio-specific enhancement over the Chorus abstraction, which
// carried no usage on its RunResult. First-class usage exists so cross-agent
// token/cost accounting can be built directly on the SDK without re-parsing
// each runtime's bespoke result line.
type TokenUsage struct {
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	// CostUSD is the runtime-reported cost when available (e.g. Claude's
	// total_cost_usd). Zero when the runtime does not report a cost.
	CostUSD float64
}

// Add accumulates another usage into u (used when a run reports usage across
// multiple models or in multiple deltas).
func (u *TokenUsage) Add(o TokenUsage) {
	u.InputTokens += o.InputTokens
	u.OutputTokens += o.OutputTokens
	u.CacheReadTokens += o.CacheReadTokens
	u.CacheWriteTokens += o.CacheWriteTokens
	u.CostUSD += o.CostUSD
}

// RunResult is the final outcome delivered alongside an EventCompleted. Usage is
// keyed by model name; it is nil when the runtime reported no usage.
type RunResult struct {
	FinishReason FinishReason
	Usage        map[string]TokenUsage
	DurationMs   int64
}

// ProbeAuth is the authentication state discovered by probing a runtime.
type ProbeAuth string

const (
	// AuthNotInstalled: the CLI/adapter binary is missing on this host.
	AuthNotInstalled ProbeAuth = "not_installed"
	// AuthUnauthed: the binary is present but the user has not logged in.
	AuthUnauthed ProbeAuth = "unauthed"
	// AuthAuthed: the binary is present and credentials are valid.
	AuthAuthed ProbeAuth = "authed"
)

// StoredSessionMeta describes a previously-stored session recovered from a
// runtime's on-disk history. Returned by Driver.ListSessions and is the
// SDK-side support for a "session searcher" host feature.
type StoredSessionMeta struct {
	SessionID    SessionID
	Title        string
	StartedAt    time.Time
	UpdatedAt    time.Time
	MessageCount int
	Cwd          string
}

// ListSessionsParams selects which persisted sessions a driver should return.
// An empty Cwd means every working directory known to the runtime.
type ListSessionsParams struct {
	// Cwd filters sessions by absolute working directory. Empty means all.
	Cwd string
}

// Attachment is a piece of non-text content attached to a prompt.
type Attachment struct {
	// Kind is "image" or "file".
	Kind string
	// MIME type of the payload.
	MIME string
	// Path is the on-disk path for file attachments (empty for inline images).
	Path string
	// Bytes is the raw payload.
	Bytes []byte
}

// PromptReq is a single prompt submitted to a live Session.
type PromptReq struct {
	Text        string
	Attachments []Attachment
}

// OpenParams unifies the "new" vs. "resume" intents (Chorus's SessionIntent).
// A non-empty ResumeSessionID means: resume that runtime-owned session; empty
// means start a fresh one.
type OpenParams struct {
	ResumeSessionID SessionID
	// Cwd overrides the Agent's working directory for this session. It is used
	// when resuming a persisted session that belongs to another workspace.
	Cwd string
}

// IsResume reports whether these params request a resume.
func (p OpenParams) IsResume() bool { return p.ResumeSessionID != "" }

// AgentSpec is the transport-agnostic configuration injected into one concrete
// Driver when its Agent is created. Each driver reads the subset it understands.
type AgentSpec struct {
	// ExecutablePath is the CLI binary to run (may be a bare name resolved on PATH).
	ExecutablePath string
	// AltCommands are fallback binary names tried (in order) when ExecutablePath
	// is not found on PATH — e.g. Trae's "trae-cli" with alt "traecli".
	AltCommands []string
	// ExtraArgs are appended after the driver's own arguments.
	ExtraArgs []string
	// Env holds extra environment variables in "KEY=VALUE" form.
	Env []string
	// Cwd is the working directory for the runtime process.
	Cwd string
	// Model, when non-empty, selects a model (drivers that support it).
	Model string
	// SystemPrompt, when non-empty, is passed as developer/system instructions
	// by drivers that can carry it.
	SystemPrompt string
}

// SessionAttachment is the return value of Driver.OpenSession: a live
// Session handle plus the event stream it publishes to.
type SessionAttachment struct {
	Session Session
	Events  *EventBus
}

// Driver implements one Agent's runtime behavior. The Agent owns one concrete
// Driver, and OpenSession yields its fresh or resumed Session handles.
//
// Implementations must be safe for concurrent use: one Agent may own several
// sessions at once.
type Driver interface {
	// Probe detects whether the runtime is installed and authenticated.
	Probe() (ProbeAuth, error)

	// ListSessions enumerates previously-stored sessions for this runtime on
	// this host. Drivers that cannot enumerate return an unsupported error.
	ListSessions(params ListSessionsParams) ([]StoredSessionMeta, error)

	// OpenSession opens a new or persisted session. The returned Session is in
	// PhaseIdle; the caller must invoke Session.Run to bring it online.
	// OpenParams.ResumeSessionID selects new-vs-resume.
	OpenSession(params OpenParams) (*SessionAttachment, error)
}

// SessionDataFormat identifies a persisted session representation.
type SessionDataFormat string

const SessionDataJSONL SessionDataFormat = "jsonl"

// RawSessionData is the runtime-owned persisted representation of one session.
type RawSessionData struct {
	Format SessionDataFormat
	Data   []byte
}

// Session is a per-session lifecycle handle representing one conversation with a
// runtime. Consumers drive it through Run -> Prompt -> Cancel/Close and observe
// side effects on the paired EventBus.
//
// Implementations serialise mutating calls internally; callers do not need an
// external lock just to keep one session safe.
type Session interface {
	// SessionID returns the runtime-assigned session id, or "" before Run has
	// attached one. This is the value used for later resume.
	SessionID() SessionID

	// ProcessState returns the current lifecycle snapshot.
	ProcessState() ProcessState

	// Raw returns the runtime-owned persisted representation of this session.
	Raw() (RawSessionData, error)

	// TokenStatistics returns cumulative usage parsed from Raw.
	TokenStatistics() (TokenUsage, error)

	// Run brings the session online (spawning or attaching the runtime as
	// needed). If initPrompt is non-nil it is delivered as the first turn so
	// runtimes can bootstrap in one round-trip. Resume intent was threaded in
	// via OpenSession's OpenParams.
	Run(initPrompt *PromptReq) error

	// Prompt sends a prompt to the live session and returns the RunID assigned
	// so callers can correlate the subsequent Output/Completed events.
	Prompt(req PromptReq) (RunID, error)

	// Continue supplies external input to a blocked run and returns the new SDK
	// run id used to correlate events after the pause.
	Continue(input string) (RunID, error)

	// Interrupt stops the active or blocked turn. It is an idempotent no-op when
	// no turn is active.
	Interrupt() error

	// Close shuts this session down and releases its resources. It does not
	// tear down a shared runtime process while sibling sessions remain live.
	Close() error
}
