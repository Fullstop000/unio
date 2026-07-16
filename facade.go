// Package unio exposes a small, human-aligned API for using coding agents.
// An Agent owns one configured runtime, Sessions own conversations, and Streams
// expose the live events of one turn.
package unio

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Fullstop000/unio/driver"
	acpdrv "github.com/Fullstop000/unio/driver/acp"
	claudedrv "github.com/Fullstop000/unio/driver/claude"
	codexdrv "github.com/Fullstop000/unio/driver/codex"
	"github.com/Fullstop000/unio/errs"
)

// AgentKind selects the coding agent runtime.
type AgentKind string

const (
	// Claude selects the Claude Code stream-JSON driver.
	Claude AgentKind = "claude"
	// Codex selects the Codex app-server driver.
	Codex AgentKind = "codex"
	// Kimi selects Kimi CLI through the shared ACP v1 driver.
	Kimi AgentKind = "kimi"
	// TraeX selects Trae CLI through the shared ACP v1 driver.
	TraeX AgentKind = "traex"
	// OpenCode selects OpenCode through the shared ACP v1 driver.
	OpenCode AgentKind = "opencode"
)

// SessionState is the state a person can observe while using a session.
type SessionState string

const (
	// Idle means the Session has no active or blocked turn.
	Idle SessionState = "idle"
	// Running means one turn is currently in flight.
	Running SessionState = "running"
	// Blocked means the runtime is waiting for caller input or approval.
	Blocked SessionState = "blocked"
)

// BlockedKind identifies why a turn needs external intervention.
type BlockedKind = driver.BlockedKind

// BlockOption is one runtime-advertised response to a blocked turn.
type BlockOption = driver.BlockOption

// BlockedReason describes why a turn blocked and any advertised responses.
type BlockedReason = driver.BlockedReason

// UserInput is one caller submission accepted by Run and Stream.
type UserInput = driver.UserInput

// UserMessage supplies natural-language input.
type UserMessage = driver.UserMessage

// OptionSelection selects a value advertised by Result.Blocked.Options.
type OptionSelection = driver.OptionSelection

// Message constructs natural-language caller input.
func Message(text string) UserMessage { return UserMessage{Text: text} }

// SelectOption constructs a response for one advertised blocked option value.
func SelectOption(value string) OptionSelection { return OptionSelection{Value: value} }

const (
	// BlockedUserInput requests free-form user input.
	BlockedUserInput = driver.BlockedUserInput
	// BlockedToolApproval requests approval for a tool call.
	BlockedToolApproval = driver.BlockedToolApproval
	// BlockedPermission requests a runtime permission decision.
	BlockedPermission = driver.BlockedPermission
	// BlockedAuthentication requests an authentication action.
	BlockedAuthentication = driver.BlockedAuthentication
	// BlockedExternal requests another runtime-defined external action.
	BlockedExternal = driver.BlockedExternal
)

// EventKind classifies streamed output.
type EventKind string

const (
	// KindThinking carries model reasoning text when the runtime exposes it.
	KindThinking EventKind = "thinking"
	// KindText carries assistant-visible text.
	KindText EventKind = "text"
	// KindToolCall carries a tool name and decoded input.
	KindToolCall EventKind = "tool_call"
	// KindToolResult carries runtime tool-result text.
	KindToolResult EventKind = "tool_result"
)

// Event is one streamed item.
type Event struct {
	// Kind selects which remaining fields are meaningful.
	Kind EventKind
	// Text is set for thinking, text, and tool-result events.
	Text string
	// Tool is set for tool-call events.
	Tool string
	// ToolInput is the runtime-decoded value for a tool-call event.
	ToolInput any
}

// ToolCall records one tool invocation observed during a turn.
type ToolCall struct {
	// Name is the runtime-reported tool name.
	Name string
	// Input is the decoded runtime tool input.
	Input any
}

// Result is the accumulated result of Run, Stream, or Continue.
type Result struct {
	// Text is all assistant-visible text accumulated before termination.
	Text string
	// Thinking is all exposed reasoning text accumulated before termination.
	Thinking string
	// ToolCalls contains observed calls; tool-result content remains stream-only.
	ToolCalls []ToolCall
	// SessionID is the runtime-owned conversation ID.
	SessionID string
	// Usage describes this turn and is keyed by runtime-reported model name.
	Usage map[string]TokenUsage
	// DurationMs is zero when the runtime does not report a duration.
	DurationMs int64
	// Interrupted reports a confirmed caller interruption.
	Interrupted bool
	// Blocked is non-nil when the turn needs external intervention.
	Blocked *BlockedReason
}

// TokenUsage describes token consumption for one model in one completed turn.
type TokenUsage = driver.TokenUsage

// TokenStatistics is cumulative token usage read from persisted session data.
// It is independent of Result.Usage, which describes one turn only.
type TokenStatistics struct {
	// InputTokens includes cached input tokens.
	InputTokens int64
	// OutputTokens is the cumulative generated-token count.
	OutputTokens int64
	// CacheReadTokens is the cached-input subset when persisted by the runtime.
	CacheReadTokens int64
	// CacheWriteTokens is the cache-write count when persisted by the runtime.
	CacheWriteTokens int64
	// CostUSD is zero because supported persisted session formats omit cost.
	CostUSD float64
}

// SessionDataFormat identifies a raw persisted session representation.
type SessionDataFormat = driver.SessionDataFormat

// SessionDataJSONL identifies newline-delimited runtime session JSON.
const SessionDataJSONL = driver.SessionDataJSONL

// RawSessionData is the runtime-owned persisted representation of one session.
type RawSessionData = driver.RawSessionData

// SessionInfo is persisted conversation metadata returned by ListSessions.
type SessionInfo struct {
	// ID is the runtime-owned session ID.
	ID string
	// Title is best-effort and may be empty.
	Title string
	// Cwd is the recorded working directory and may be empty.
	Cwd string
	// StartedAt is best-effort and may be the zero time.
	StartedAt time.Time
	// UpdatedAt is best-effort and may be the zero time.
	UpdatedAt time.Time
	// MessageCount is runtime-provided and may be zero when unavailable.
	MessageCount int
}

// ListSessionsOption narrows which persisted conversations ListSessions returns.
type ListSessionsOption func(*listSessionsConfig)

type listSessionsConfig struct {
	cwd   string
	all   bool
	limit int
}

// SessionsIn lists conversations belonging to dir instead of the Agent's
// configured working directory.
func SessionsIn(dir string) ListSessionsOption {
	return func(c *listSessionsConfig) {
		if dir != "" {
			c.cwd = normalizeCwd(dir)
		}
		c.all = false
	}
}

// AllSessions lists conversations from every working directory known to the
// runtime.
func AllSessions() ListSessionsOption {
	return func(c *listSessionsConfig) {
		c.cwd = ""
		c.all = true
	}
}

// MaxSessions returns at most n conversations in runtime-defined order. A
// non-positive n means no limit.
func MaxSessions(n int) ListSessionsOption {
	return func(c *listSessionsConfig) {
		c.limit = n
	}
}

var (
	// ErrInvalidState matches errors for operations invalid in the current state.
	ErrInvalidState = errs.New(errs.KindInvalidState, "")
	// ErrSessionNotFound matches errors for unknown runtime-owned session IDs.
	ErrSessionNotFound = errs.New(errs.KindSessionNotFound, "")
)

type config struct {
	cwd          string
	model        string
	systemPrompt string
	extraArgs    []string
	env          []string
}

// Option configures an Agent instance.
type Option func(*config)

// WithCwd selects the Agent working directory. An empty value uses the current
// process directory. It does not sandbox runtime file access.
func WithCwd(dir string) Option { return func(c *config) { c.cwd = dir } }

// WithModel selects a runtime-specific model name. The runtime validates it
// when an operation starts.
func WithModel(model string) Option { return func(c *config) { c.model = model } }

// WithSystemPrompt supplies standing instructions. ACP agents prepend them to
// the first user prompt; other drivers use their native instruction channel.
func WithSystemPrompt(prompt string) Option {
	return func(c *config) { c.systemPrompt = prompt }
}

// WithExtraArgs appends runtime-specific CLI arguments. Codex app-server
// arguments are fixed and ignore this option.
func WithExtraArgs(args ...string) Option {
	return func(c *config) { c.extraArgs = append(c.extraArgs, args...) }
}

// WithEnv appends child-process environment entries in KEY=VALUE form. Callers
// should avoid duplicate keys because their interpretation is platform-specific.
func WithEnv(env ...string) Option {
	return func(c *config) { c.env = append(c.env, env...) }
}

var driverOverride func(context.Context, AgentKind, driver.AgentSpec) (driver.Driver, bool)

var driverFactories = map[AgentKind]func(context.Context, driver.AgentSpec) driver.Driver{
	Claude: func(ctx context.Context, spec driver.AgentSpec) driver.Driver { return claudedrv.New(ctx, spec) },
	Codex:  func(ctx context.Context, spec driver.AgentSpec) driver.Driver { return codexdrv.New(ctx, spec) },
	Kimi: func(ctx context.Context, spec driver.AgentSpec) driver.Driver {
		return acpdrv.New(ctx, acpdrv.Kimi, spec)
	},
	TraeX: func(ctx context.Context, spec driver.AgentSpec) driver.Driver {
		return acpdrv.New(ctx, acpdrv.TraeX, spec)
	},
	OpenCode: func(ctx context.Context, spec driver.AgentSpec) driver.Driver {
		return acpdrv.New(ctx, acpdrv.OpenCode, spec)
	},
}

func driverFor(ctx context.Context, kind AgentKind, spec driver.AgentSpec) (driver.Driver, error) {
	if driverOverride != nil {
		if d, ok := driverOverride(ctx, kind, spec); ok {
			return d, nil
		}
	}
	if factory := driverFactories[kind]; factory != nil {
		return factory(ctx, spec), nil
	}
	return nil, fmt.Errorf("unio: unknown agent %q", kind)
}

func buildConfig(opts []Option) config {
	var c config
	for _, opt := range opts {
		opt(&c)
	}
	return c
}

func buildListSessionsConfig(defaultCwd string, opts []ListSessionsOption) listSessionsConfig {
	c := listSessionsConfig{cwd: normalizeCwd(defaultCwd)}
	for _, opt := range opts {
		opt(&c)
	}
	return c
}

func normalizeCwd(dir string) string {
	if dir == "" {
		return ""
	}
	if abs, err := filepath.Abs(dir); err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(dir)
}

func (c config) spec() driver.AgentSpec {
	cwd := c.cwd
	if cwd == "" {
		cwd = cwdOrDot()
	}
	return driver.AgentSpec{
		Cwd: cwd, Model: c.model, SystemPrompt: c.systemPrompt,
		ExtraArgs: c.extraArgs, Env: c.env,
	}
}

func cwdOrDot() string {
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "."
}

func failedToError(ev driver.AgentEvent) error {
	if ev.Err != nil {
		return ev.Err
	}
	return driver.NewRuntimeReportedError("unio: run failed")
}
