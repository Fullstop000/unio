// Package unio exposes a small, human-aligned API for using coding agents.
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
	Idle    SessionState = "idle"
	Running SessionState = "running"
	Blocked SessionState = "blocked"
)

type BlockedKind = driver.BlockedKind
type BlockOption = driver.BlockOption
type BlockedReason = driver.BlockedReason

const (
	BlockedUserInput      = driver.BlockedUserInput
	BlockedToolApproval   = driver.BlockedToolApproval
	BlockedPermission     = driver.BlockedPermission
	BlockedAuthentication = driver.BlockedAuthentication
	BlockedExternal       = driver.BlockedExternal
)

// EventKind classifies streamed output.
type EventKind string

const (
	KindThinking   EventKind = "thinking"
	KindText       EventKind = "text"
	KindToolCall   EventKind = "tool_call"
	KindToolResult EventKind = "tool_result"
)

// Event is one streamed item.
type Event struct {
	Kind      EventKind
	Text      string
	Tool      string
	ToolInput any
}

// ToolCall records one tool invocation observed during a turn.
type ToolCall struct {
	Name  string
	Input any
}

// Result is the accumulated result of Run, Stream, or Continue.
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

// TokenStatistics is cumulative token usage read from persisted session data.
// It is independent of Result.Usage, which describes one turn only.
type TokenStatistics struct {
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	CostUSD          float64
}

// SessionDataFormat identifies a raw persisted session representation.
type SessionDataFormat = driver.SessionDataFormat

const SessionDataJSONL = driver.SessionDataJSONL

// RawSessionData is the runtime-owned persisted representation of one session.
type RawSessionData = driver.RawSessionData

// SessionInfo is persisted conversation metadata returned by ListSessions.
type SessionInfo struct {
	ID           string
	Title        string
	Cwd          string
	StartedAt    time.Time
	UpdatedAt    time.Time
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

// MaxSessions returns at most n conversations. A non-positive n means no limit.
func MaxSessions(n int) ListSessionsOption {
	return func(c *listSessionsConfig) {
		c.limit = n
	}
}

var (
	ErrInvalidState    = errs.New(errs.KindInvalidState, "")
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

func WithCwd(dir string) Option     { return func(c *config) { c.cwd = dir } }
func WithModel(model string) Option { return func(c *config) { c.model = model } }
func WithSystemPrompt(prompt string) Option {
	return func(c *config) { c.systemPrompt = prompt }
}
func WithExtraArgs(args ...string) Option {
	return func(c *config) { c.extraArgs = append(c.extraArgs, args...) }
}
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
