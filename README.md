# unio

Go SDK for using Claude Code, Codex, and ACP-native agents through one
human-aligned API.

Built-in ACP v1 runtimes are selected with `unio.Kimi`, `unio.TraeX`, and
`unio.OpenCode`. TraeX executable discovery recognizes `traex`, `trae-cli`,
`coco`, and `traecli`.

See [docs/API_SUPPORT.md](docs/API_SUPPORT.md) for the complete top-level API
inventory and the per-Agent support matrix.

See [docs/ERRORS.md](docs/ERRORS.md) for typed error categories, retry guidance,
context cancellation, and the distinction between errors, blocked turns, and
confirmed interruption.

## Install

```sh
go get github.com/Fullstop000/unio
```

The selected CLI must be installed and authenticated.

## Run a task

```go
ctx, cancel := context.WithCancel(context.Background())
defer cancel()

agent, err := unio.New(ctx, unio.Codex, unio.WithCwd("/path/to/repo"))
if err != nil {
    return err
}
defer agent.Close()

session, err := agent.NewSession()
if err != nil {
    return err
}

result, err := session.Run("Explain this repository")
fmt.Println(result.Text)
```

The context passed to `New` owns the Agent lifecycle. Cancelling it closes the
Agent and applies to every Session created by that Agent.

A new session has no runtime ID before its first turn:

```go
session.ID() // ""
_, _ = session.Run("Start a plan")
session.ID() // Claude/Codex runtime session ID
```

## Stream and interrupt

```go
stream, err := session.Stream("Refactor the authentication module")
if err != nil {
    return err
}

for stream.Next() {
    event := stream.Event()
    if event.Kind == unio.KindText {
        fmt.Print(event.Text)
    }
}

result, err := stream.Result()
```

Call `session.Interrupt()` from another goroutine to stop a running turn.
Interrupt is normal control flow; the waiting result has `Interrupted == true`.
When using `Stream`, drain `stream.Result()` before starting the next turn so
the interrupted turn's terminal event is finalized.

## Blocked turns

An agent can pause for user input, tool approval, permission, authentication,
or another external action:

```go
result, err := session.Run("Run the tests and fix failures")
if err != nil {
    return err
}
if result.Blocked != nil {
    fmt.Println(result.Blocked.Message)
    result, err = session.Continue("allow_once")
}
```

`BlockedReason.Options` is best-effort and may be empty. When present, pass an
option's `Value` to `Continue`; otherwise pass free-form input.

## Find and continue a session

```go
sessions, err := agent.ListSessions()
session, err := agent.GetSession(sessions[0].ID)

// Runtime resume is automatic.
result, err := session.Run("Continue the previous work")

// Read the runtime's complete persisted JSONL session data.
raw, err := session.Raw()

// Parse cumulative usage across the whole session.
stats, err := session.TokenStatistics()
```

`ListSessions` defaults to the Agent's working directory. Use
`ListSessions(unio.SessionsIn(dir))` for another workspace,
`ListSessions(unio.AllSessions())` for every workspace, or
`ListSessions(unio.MaxSessions(20))` to cap the returned conversations.
Options can be combined.

`Session.Raw` is available for Claude Code, Codex, Kimi, and TraeX.
`Result.Usage` describes one completed turn. `Session.TokenStatistics` parses
the raw persisted data into a session-wide aggregate for the same four agents.

`Session.State()` exposes only `Idle`, `Running`, and `Blocked`. Runtime process
and transport lifecycle remain internal.

## Test

```sh
go vet ./...
go test -race ./...
go test -tags e2e_real ./tests/...
```

Real E2E tests invoke authenticated CLIs and may consume tokens.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for development gates, runtime-test
expectations, contract-change rules, and pull request guidance.

## License

MIT
