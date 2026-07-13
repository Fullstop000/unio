// Package unio exposes a small, human-aligned API for using coding agents.
package unio

import (
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
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

var sessionSeq atomic.Uint64

var driverOverride func(AgentKind) (driver.ProtocolDriver, bool)

var driverFactories = map[AgentKind]func() driver.ProtocolDriver{
	Claude:   func() driver.ProtocolDriver { return claudedrv.New() },
	Codex:    func() driver.ProtocolDriver { return codexdrv.New() },
	Kimi:     func() driver.ProtocolDriver { return acpdrv.New(acpdrv.Kimi) },
	TraeX:    func() driver.ProtocolDriver { return acpdrv.New(acpdrv.TraeX) },
	OpenCode: func() driver.ProtocolDriver { return acpdrv.New(acpdrv.OpenCode) },
}

func driverFor(kind AgentKind) (driver.ProtocolDriver, error) {
	if driverOverride != nil {
		if d, ok := driverOverride(kind); ok {
			return d, nil
		}
	}
	if factory := driverFactories[kind]; factory != nil {
		return factory(), nil
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

func autoKey(kind AgentKind) driver.SessionKey {
	return fmt.Sprintf("%s-%d", kind, sessionSeq.Add(1))
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
