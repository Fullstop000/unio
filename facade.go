// Package unio is the high-level, human-facing facade for the unio SDK.
//
// The driver package is the control plane — precise, composable, used by
// orchestration hosts. This package is the ergonomic front door for everyone
// else: "drive an agent, get a result" in one or two lines, with session ids,
// specs, subscription timing, and the event loop all handled for you.
//
//	// one-shot: send a prompt, get the answer
//	res, err := unio.Run(ctx, unio.Claude, "reply with one word: ping")
//	fmt.Println(res.Text, res.Usage)
//
//	// streaming: watch the turn unfold
//	s, _ := unio.Start(ctx, unio.Codex)
//	defer s.Close()
//	st := s.Prompt(ctx, "explain this repo")
//	for st.Next() {
//	    if ev := st.Event(); ev.Kind == unio.KindText { fmt.Print(ev.Text) }
//	}
//	res, err := st.Result()
//
//	// with options (functional options; zero config is fine)
//	res, _ := unio.Run(ctx, unio.Claude, "hi",
//	    unio.WithModel("claude-sonnet-4-6"),
//	    unio.WithResume(priorID))
package unio

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"

	"github.com/Fullstop000/unio/driver"
	claudedrv "github.com/Fullstop000/unio/driver/claude"
	codexdrv "github.com/Fullstop000/unio/driver/codex"
)

// Agent selects which coding agent to drive. It hides the transport/driver
// wiring behind a single friendly value.
type Agent string

const (
	Claude Agent = "claude"
	Codex  Agent = "codex"
)

// EventKind classifies a streamed Event. Exported as a typed enum so callers
// switch on constants (unio.KindText) rather than magic strings.
type EventKind string

const (
	// KindThinking is reasoning/thinking text.
	KindThinking EventKind = "thinking"
	// KindText is assistant-visible text.
	KindText EventKind = "text"
	// KindToolCall is a tool invocation (Tool + ToolInput set).
	KindToolCall EventKind = "tool_call"
	// KindToolResult is the result of a tool call (Text set).
	KindToolResult EventKind = "tool_result"
)

// FinishReason is why a turn ended. Typed enum for the same reason as EventKind.
type FinishReason string

const (
	// FinishNatural: the agent finished the turn normally.
	FinishNatural FinishReason = "natural"
	// FinishCancelled: the turn was interrupted before completion.
	FinishCancelled FinishReason = "cancelled"
	// FinishTransportClosed: the agent process/stream closed mid-turn.
	FinishTransportClosed FinishReason = "transport_closed"
)

// config is the private, accumulated configuration a set of Options builds up.
// It is never exposed; callers only ever pass Option values.
type config struct {
	cwd          string
	model        string
	systemPrompt string
	resume       string
	extraArgs    []string
	env          []string
}

// Option configures a Run or Open. Options are the idiomatic Go way to pass
// optional, extensible settings without a growing parameter list or an exposed
// config struct. The zero configuration is valid; Cwd defaults to the process
// working directory.
type Option func(*config)

// WithCwd sets the agent's working directory (default: process cwd).
func WithCwd(dir string) Option { return func(c *config) { c.cwd = dir } }

// WithModel selects a model (agent-specific; optional).
func WithModel(model string) Option { return func(c *config) { c.model = model } }

// WithSystemPrompt passes developer/system instructions to agents that carry them.
func WithSystemPrompt(prompt string) Option { return func(c *config) { c.systemPrompt = prompt } }

// WithResume resumes a prior session id (from a previous Result.SessionID).
func WithResume(sessionID string) Option { return func(c *config) { c.resume = sessionID } }

// WithExtraArgs appends extra CLI arguments to the agent invocation.
func WithExtraArgs(args ...string) Option {
	return func(c *config) { c.extraArgs = append(c.extraArgs, args...) }
}

// WithEnv appends extra "KEY=VALUE" environment entries.
func WithEnv(env ...string) Option {
	return func(c *config) { c.env = append(c.env, env...) }
}

// Event is a flattened, facade-friendly view of one streamed item.
type Event struct {
	// Kind identifies the item (thinking / text / tool_call / tool_result).
	Kind EventKind
	// Text carries thinking/text/tool_result content.
	Text string
	// Tool / ToolInput are set for KindToolCall.
	Tool      string
	ToolInput any
}

var sessionSeq atomic.Uint64

// driverOverride lets tests inject a driver for an agent without a real CLI.
// Nil in production. Keyed by Agent.
var driverOverride func(Agent) (driver.ProtocolDriver, bool)

// driverFor builds the concrete ProtocolDriver for an agent.
func driverFor(a Agent) (driver.ProtocolDriver, error) {
	if driverOverride != nil {
		if d, ok := driverOverride(a); ok {
			return d, nil
		}
	}
	switch a {
	case Claude:
		return claudedrv.New(), nil
	case Codex:
		return codexdrv.New(), nil
	default:
		return nil, fmt.Errorf("unio: unknown agent %q", a)
	}
}

// Installed reports whether the agent's CLI is available on this host.
func Installed(a Agent) bool {
	d, err := driverFor(a)
	if err != nil {
		return false
	}
	pr, err := d.Probe(context.Background())
	return err == nil && pr.Auth != driver.AuthNotInstalled
}

// buildConfig applies options over the zero config.
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
		Cwd:          cwd,
		Model:        c.model,
		SystemPrompt: c.systemPrompt,
		ExtraArgs:    c.extraArgs,
		Env:          c.env,
	}
}

func autoKey(a Agent) driver.SessionKey {
	return fmt.Sprintf("%s-%d", a, sessionSeq.Add(1))
}

// cwdOrDot returns the current working directory, or "." if it can't be
// determined — a safe default for an agent's working directory.
func cwdOrDot() string {
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "."
}
