# unio

Go SDK for using Claude Code, Codex, and ACP-native coding agents through one
human-aligned API.

unio v0.2 requires Go 1.23 or newer. The supported caller-facing packages are
the root `unio` package and `errs`; packages under `driver` are pre-1.0 adapter
APIs. v0.1 compatibility and migration guidance are intentionally not
maintained.

See the [documentation index](docs/README.md) for the support matrix, behavior
specification, error guide, and stability boundaries.

## Choose and prepare an agent

The selected CLI must be installed and authenticated before its first runtime
operation. `unio.New` verifies executable discovery only; authentication,
network, model, and provider errors may surface on the first turn or session
query.

| Go value | Executable discovery | Preparation |
| --- | --- | --- |
| `unio.Claude` | `claude` | [Install and authenticate Claude Code](https://docs.anthropic.com/en/docs/claude-code/getting-started) |
| `unio.Codex` | `codex` | [Install and authenticate Codex CLI](https://github.com/openai/codex#readme) |
| `unio.Kimi` | `kimi-cli`, then `kimi` | [Install Kimi Code CLI and run `/login`](https://moonshotai.github.io/kimi-cli/) |
| `unio.TraeX` | `traex`, `trae-cli`, `coco`, then `traecli` | Install and authenticate a TraeX distribution with ACP v1 support |
| `unio.OpenCode` | `opencode` | [Install and configure OpenCode](https://opencode.ai/docs/) |

Check the CLI before debugging the SDK:

```sh
codex --version # or: claude/kimi/opencode --version
```

Runtime support is not uniform. Review the [support and compatibility
matrix](docs/API_SUPPORT.md) before depending on approvals, usage, raw session
data, or resume for a particular agent. See the runnable [ACP
example](examples/acp/main.go) for Kimi, TraeX, and OpenCode.

## Install

```sh
go get github.com/Fullstop000/unio@v0.2.0
```

## Run a task

```go
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/Fullstop000/unio"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	agent, err := unio.New(ctx, unio.Codex, unio.WithCwd("/path/to/repo"))
	if err != nil {
		log.Fatal(err)
	}
	defer agent.Close()

	session, err := agent.NewSession()
	if err != nil {
		log.Fatal(err)
	}

	result, err := session.Run("Explain this repository")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(result.Text)
}
```

A new Session has no runtime ID until its first turn starts. The ID always
comes from the selected runtime:

```go
session.ID() // ""
_, _ = session.Run("Start a plan")
session.ID() // runtime-owned ID
```

## Configure an agent

Options apply to the Agent and all Sessions it creates:

| Option | Meaning |
| --- | --- |
| `WithCwd(dir)` | Working directory. Defaults to the current process directory. It is not a sandbox. |
| `WithModel(model)` | Runtime-specific model name. Validation normally happens when the runtime starts. |
| `WithSystemPrompt(prompt)` | Standing instructions. ACP agents prepend them to the first user prompt. |
| `WithExtraArgs(args...)` | Runtime-specific CLI arguments. Codex app-server arguments are fixed, so Codex ignores this option. |
| `WithEnv("KEY=VALUE", ...)` | Extra child-process environment entries appended to the inherited environment. Avoid duplicate keys. |

## Lifecycle and concurrency

The context passed to `New` owns the entire Agent lifecycle. Cancelling it
closes the Agent and every derived Session; there is no per-turn context. Use
`Session.Interrupt` for a reusable Agent when only one turn should stop.

Only one turn may run on a Session at a time. Multiple Sessions from one Agent
may run concurrently. `Agent.Close` and idle `Session.Interrupt` are idempotent.

With a manual Stream, always call `Result` after interruption or after `Next`
returns false. The terminal event returns the Session to idle; starting another
turn before consuming it returns `invalid_state`.

The internal event bus is bounded so a slow consumer cannot block the runtime.
Terminal events are preserved, but intermediate stream events can be dropped.
The final Result aggregates delivered events and may therefore also be
incomplete for a slow consumer. The top-level Stream does not expose the
dropped-event counter, so consume events promptly and do not use it as an audit
log.

See the runnable [stream and interrupt example](examples/stream/main.go).

## Stream events

```go
stream, err := session.Stream("Refactor the authentication module")
if err != nil {
	return err
}
for stream.Next() {
	event := stream.Event()
	switch event.Kind {
	case unio.KindText, unio.KindThinking, unio.KindToolResult:
		fmt.Print(event.Text)
	case unio.KindToolCall:
		fmt.Printf("tool=%s input=%v\n", event.Tool, event.ToolInput)
	}
}
result, err := stream.Result()
```

Text, thinking, and tool calls accumulate in the final Result. Tool-result
content is stream-only and is not retained in `Result.ToolCalls`.

## Blocked turns

Blocking is runtime-dependent. Claude does not block; Codex currently exposes
command/file approvals; ACP agents can expose tool approvals when the runtime
advertises them. See the [support matrix](docs/API_SUPPORT.md) for exact limits.

A blocked turn returns `err == nil`, sets `Result.Blocked`, and leaves the
Session blocked. A runtime can block more than once:

```go
result, err := session.Run("Apply the change")
if err != nil {
	return err
}
for result.Blocked != nil {
	if len(result.Blocked.Options) == 0 {
		return fmt.Errorf("agent needs input: %s", result.Blocked.Message)
	}
	result, err = session.Continue(result.Blocked.Options[0].Value)
	if err != nil {
		return err
	}
}
```

When no options are advertised, pass caller-provided free-form input rather
than inventing an option ID. See the runnable [blocked-turn
example](examples/blocked/main.go).

## Find and continue a session

```go
sessions, err := agent.ListSessions(unio.MaxSessions(20))
if err != nil {
	return err
}
if len(sessions) == 0 {
	return errors.New("no persisted sessions")
}
session, err := agent.GetSession(sessions[0].ID)
if err != nil {
	return err
}
result, err := session.Run("Continue the previous work")
```

`ListSessions` defaults to the Agent working directory. `SessionsIn(dir)`
selects another workspace and `AllSessions()` removes the filter. Result order
is runtime-defined and not stable; sort metadata before selecting by position.
Titles, timestamps, and message counts are best-effort and may be zero.

Runtime resume is automatic on the next `Run` or `Stream`. See the runnable
[sessions example](examples/sessions/main.go).

## Usage and persisted data

`Result.Usage` is per-turn and keyed by runtime-reported model name.
`Session.TokenStatistics` parses cumulative persisted usage. `Session.Raw`
returns the runtime-owned JSONL representation. Raw data and statistics are
available for Claude, Codex, Kimi, and TraeX; OpenCode currently returns
`unsupported`.

Raw session data can contain prompts, source code, tool inputs, commands,
outputs, paths, and credentials. Do not log or transmit it without full review
and redaction.

## Errors

Match typed errors by `errs.ErrorKind`, never by message text. Standard context
errors, blocked turns, and confirmed interruption have separate semantics. See
the [error-handling guide](docs/ERRORS.md).

## Security boundary

unio does not sandbox coding agents. The child CLI inherits the current OS
user's file, process, environment, and network permissions, subject to that
runtime's own approval model. `WithCwd` selects a working directory but does not
confine access to it.

Use a least-privilege environment for untrusted repositories and prompts. Do
not pass secrets through `WithEnv` unless the selected runtime needs them, and
do not assume every runtime will request approval before executing tools or
modifying files.

Report suspected vulnerabilities through the private channel in
[SECURITY.md](SECURITY.md), not a public issue.

## Test and contribute

```sh
go vet ./...
go test -race ./...
go test -tags e2e_real ./tests/...
```

Real E2E tests invoke authenticated CLIs and may consume tokens. See
[CONTRIBUTING.md](CONTRIBUTING.md) for development gates, compatibility changes,
bug reports, and pull request guidance.

## License

MIT
