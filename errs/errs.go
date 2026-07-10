// Package errs is unio's cross-language error contract. It defines the error
// categories and the AgentError value that every unio implementation (Go today;
// TypeScript, Rust, … later) must expose with identical string values.
//
// # Why a separate package / contract
//
// unio is a multi-language SDK family: the same abstraction is implemented once
// per language so users call unio in their own language and get identical
// behaviour. For that to hold, the observable *contract* — error category
// strings, event kinds, state names, wire formats — must be language-neutral and
// frozen. Error classification is the most cross-cutting of these, so it lives
// in its own package and is mirrored verbatim in SPEC.md.
//
// CONTRACT: the ErrorKind string values below are part of the cross-language
// wire/behaviour contract. Do NOT rename or repurpose them without a spec bump;
// TS/Rust implementations pattern-match on these exact strings.
package errs

import "errors"

// ErrorKind classifies an AgentError so callers match on the category rather
// than string-comparing messages. The string values are the cross-language
// contract.
type ErrorKind string

const (
	// KindTransport: process exited, stdio closed, socket died.
	KindTransport ErrorKind = "transport"
	// KindProtocol: malformed frame, unexpected message, decode failure.
	KindProtocol ErrorKind = "protocol"
	// KindTimeout: timed out waiting for a runtime response.
	KindTimeout ErrorKind = "timeout"
	// KindRuntimeReported: the runtime reported a domain error via its protocol.
	KindRuntimeReported ErrorKind = "runtime_reported"
	// KindUnsupported: the operation is not supported by this driver/transport.
	KindUnsupported ErrorKind = "unsupported"
	// KindNotInstalled: the agent's CLI/adapter binary is not installed on this
	// host. Surfaced at OpenSession time so a host can tell the user which
	// executable to install rather than failing obscurely at spawn.
	KindNotInstalled ErrorKind = "not_installed"
	// KindInvalidState means the requested action is not valid in the current
	// human-observable session state.
	KindInvalidState ErrorKind = "invalid_state"
	// KindSessionNotFound means a runtime-owned session id does not exist.
	KindSessionNotFound ErrorKind = "session_not_found"
)

// Valid reports whether k is a recognised category. Useful when decoding an
// error kind received over a wire boundary from another-language peer.
func (k ErrorKind) Valid() bool {
	switch k {
	case KindTransport, KindProtocol, KindTimeout, KindRuntimeReported, KindUnsupported, KindNotInstalled, KindInvalidState, KindSessionNotFound:
		return true
	default:
		return false
	}
}

// AgentError is unio's driver-facing error. It is plain-data (a category tag
// plus a message) so it crosses goroutine and process boundaries cheaply and is
// safe to copy for fan-out to multiple subscribers.
//
// It supports errors.Is (match by Kind) and errors.As (extract the concrete
// type), and can wrap an underlying cause for errors.Unwrap.
type AgentError struct {
	Kind ErrorKind
	Msg  string
	// cause is an optional wrapped error for Unwrap; not part of the wire
	// contract (peers only observe Kind + Msg).
	cause error
}

// Error implements the error interface.
func (e *AgentError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Msg == "" {
		return string(e.Kind)
	}
	return string(e.Kind) + ": " + e.Msg
}

// Unwrap returns the wrapped cause, if any.
func (e *AgentError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// Is lets errors.Is match by category: errors.Is(err, errs.KindTimeout-error)
// or against another AgentError with the same Kind.
func (e *AgentError) Is(target error) bool {
	if e == nil {
		return false
	}
	var other *AgentError
	if errors.As(target, &other) {
		return other != nil && other.Kind == e.Kind
	}
	return false
}

// New builds an AgentError of the given kind.
func New(kind ErrorKind, msg string) *AgentError {
	return &AgentError{Kind: kind, Msg: msg}
}

// Wrap builds an AgentError of the given kind wrapping an underlying cause. The
// cause is available via errors.Unwrap but is not part of the cross-language
// contract.
func Wrap(kind ErrorKind, msg string, cause error) *AgentError {
	return &AgentError{Kind: kind, Msg: msg, cause: cause}
}

// Category constructors — the ergonomic call sites drivers use.

// Transport builds a transport-category error.
func Transport(msg string) *AgentError { return New(KindTransport, msg) }

// Protocol builds a protocol-category error.
func Protocol(msg string) *AgentError { return New(KindProtocol, msg) }

// Timeout builds a timeout-category error.
func Timeout(msg string) *AgentError { return New(KindTimeout, msg) }

// RuntimeReported builds a runtime-reported error.
func RuntimeReported(msg string) *AgentError { return New(KindRuntimeReported, msg) }

// Unsupported builds an unsupported-operation error.
func Unsupported(msg string) *AgentError { return New(KindUnsupported, msg) }

// NotInstalled builds a not-installed error. Prefer NotInstalledCmd when the
// missing command name is known so hosts can render an actionable message.
func NotInstalled(msg string) *AgentError { return New(KindNotInstalled, msg) }

// NotInstalledCmd builds a not-installed error naming the missing executable.
func NotInstalledCmd(command string) *AgentError {
	return New(KindNotInstalled, "executable not found on PATH: "+command)
}

// InvalidState builds an error for an action that does not apply to the
// session's current state.
func InvalidState(msg string) *AgentError { return New(KindInvalidState, msg) }

// SessionNotFound builds an error naming a missing runtime session id.
func SessionNotFound(id string) *AgentError {
	return New(KindSessionNotFound, "session not found: "+id)
}

// KindOf extracts the ErrorKind from any error that is (or wraps) an
// *AgentError, or returns ("", false) if none is present. Handy for host code
// that wants to branch on category without a type assertion.
func KindOf(err error) (ErrorKind, bool) {
	var ae *AgentError
	if errors.As(err, &ae) && ae != nil {
		return ae.Kind, true
	}
	return "", false
}
