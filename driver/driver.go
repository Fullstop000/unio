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
//   - The three responsibilities are kept distinct: ProtocolDriver (a factory +
//     session-lifecycle owner), Session (one live conversation handle), and
//     AgentProcess (a cached OS process that a Registry evicts when stale).
//
// A driver turns "run this prompt, stream the result, let me cancel/resume it"
// into a transport-specific reality, while the caller sees one Session interface
// no matter which agent backs it.
package driver

import (
	"context"
	"time"

	"github.com/Fullstop000/unio/errs"
)

// Transport identifies how a driver talks to its underlying runtime. It doubles
// as the capability selector a host uses to route an agent to the right driver.
type Transport string

const (
	// TransportFake is the in-memory test double (driver/fake). It spawns no
	// process and is used to prove the abstraction end-to-end.
	TransportFake Transport = "fake"
	// TransportACPNative speaks the Agent Client Protocol natively over stdio.
	// Backs Trae (`traecli acp`), Kimi, OpenCode, Gemini, … via one shared layer.
	TransportACPNative Transport = "acp_native"
	// TransportCodexAppServer speaks Codex's app-server JSON-RPC over stdio.
	// One child multiplexes many threads (= sessions).
	TransportCodexAppServer Transport = "codex_app_server"
	// TransportClaudeStreamJSON speaks Claude Code's `--output-format stream-json`.
	// One child per session (Claude cannot multiplex).
	TransportClaudeStreamJSON Transport = "claude_stream_json"
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
	NewNotInstalledError = errs.NotInstalledCmd
)

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

// CancelOutcome is the result of a Session.Cancel call.
type CancelOutcome string

const (
	// CancelAborted: the in-flight run was aborted; the session remains usable.
	CancelAborted CancelOutcome = "aborted"
	// CancelNotInFlight: no run was in flight when Cancel was invoked.
	CancelNotInFlight CancelOutcome = "not_in_flight"
)

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

// RuntimeProbe is the aggregate result of probing a runtime for availability.
type RuntimeProbe struct {
	Auth      ProbeAuth
	Transport Transport
}

// StoredSessionMeta describes a previously-stored session recovered from a
// runtime's on-disk history. Returned by ProtocolDriver.ListSessions and is the
// SDK-side support for a "session searcher" host feature.
type StoredSessionMeta struct {
	SessionID    SessionID
	Title        string
	StartedAt    time.Time
	MessageCount int
	Cwd          string
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
}

// IsResume reports whether these params request a resume.
func (p OpenParams) IsResume() bool { return p.ResumeSessionID != "" }

// AgentSpec is everything a driver needs to open a session on an agent. It is
// transport-agnostic; each driver reads the subset it understands.
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

// SessionAttachment is the return value of ProtocolDriver.OpenSession: a live
// Session handle plus the event stream it publishes to.
type SessionAttachment struct {
	Session Session
	Events  *EventBus
}

// ProtocolDriver is the runtime-level factory. One instance per runtime kind
// (Claude, Codex, an ACP agent, Fake). It owns session lifecycle: OpenSession
// yields a fresh Session (new or resumed).
//
// Implementations must be safe for concurrent use: a host may open sessions for
// several agents of the same runtime kind at once.
type ProtocolDriver interface {
	// Transport returns which transport this driver speaks.
	Transport() Transport

	// Probe detects whether the runtime is installed and authenticated.
	Probe(ctx context.Context) (RuntimeProbe, error)

	// ListSessions enumerates previously-stored sessions for this runtime on
	// this host. Drivers that cannot enumerate return an empty slice, nil.
	ListSessions(ctx context.Context) ([]StoredSessionMeta, error)

	// OpenSession opens a session for the given key. The returned Session is in
	// PhaseIdle; the caller must invoke Session.Run to bring it online.
	// OpenParams.ResumeSessionID selects new-vs-resume.
	OpenSession(ctx context.Context, key SessionKey, spec AgentSpec, params OpenParams) (*SessionAttachment, error)
}

// Session is a per-session lifecycle handle representing one conversation with a
// runtime. Consumers drive it through Run -> Prompt -> Cancel/Close and observe
// side effects on the paired EventBus.
//
// Implementations serialise mutating calls internally; callers do not need an
// external lock just to keep one session safe.
type Session interface {
	// Key returns the session key this handle belongs to.
	Key() SessionKey

	// SessionID returns the runtime-assigned session id, or "" before Run has
	// attached one. This is the value used for later resume.
	SessionID() SessionID

	// ProcessState returns the current lifecycle snapshot.
	ProcessState() ProcessState

	// Run brings the session online (spawning or attaching the runtime as
	// needed). If initPrompt is non-nil it is delivered as the first turn so
	// runtimes can bootstrap in one round-trip. Resume intent was threaded in
	// via OpenSession's OpenParams.
	Run(ctx context.Context, initPrompt *PromptReq) error

	// Prompt sends a prompt to the live session and returns the RunID assigned
	// so callers can correlate the subsequent Output/Completed events.
	Prompt(ctx context.Context, req PromptReq) (RunID, error)

	// Cancel cancels an in-flight run. See CancelOutcome for semantics.
	Cancel(ctx context.Context, run RunID) (CancelOutcome, error)

	// Close shuts this session down and releases its resources. It does not
	// tear down a shared runtime process while sibling sessions remain live.
	Close(ctx context.Context) error
}
