// Package unio exposes a small, human-aligned API for using coding agents.
package unio

import (
	"fmt"
	"os"
	"sync/atomic"

	"github.com/Fullstop000/unio/driver"
	claudedrv "github.com/Fullstop000/unio/driver/claude"
	codexdrv "github.com/Fullstop000/unio/driver/codex"
	"github.com/Fullstop000/unio/errs"
)

// AgentKind selects the coding agent runtime.
type AgentKind string

const (
	Claude AgentKind = "claude"
	Codex  AgentKind = "codex"
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

func driverFor(kind AgentKind) (driver.ProtocolDriver, error) {
	if driverOverride != nil {
		if d, ok := driverOverride(kind); ok {
			return d, nil
		}
	}
	switch kind {
	case Claude:
		return claudedrv.New(), nil
	case Codex:
		return codexdrv.New(), nil
	default:
		return nil, fmt.Errorf("unio: unknown agent %q", kind)
	}
}

func buildConfig(opts []Option) config {
	var c config
	for _, opt := range opts {
		opt(&c)
	}
	return c
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
