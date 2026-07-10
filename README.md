# unio

Go SDK for using Claude Code and Codex through one human-aligned API.

## Install

```sh
go get github.com/Fullstop000/unio
```

The selected CLI must be installed and authenticated.

## Run a task

```go
agent, err := unio.New(unio.Codex, unio.WithCwd("/path/to/repo"))
if err != nil {
    return err
}
defer agent.Close()

session, err := agent.NewSession(ctx)
if err != nil {
    return err
}

result, err := session.Run(ctx, "Explain this repository")
fmt.Println(result.Text)
```

A new session has no runtime ID before its first turn:

```go
session.ID() // ""
_, _ = session.Run(ctx, "Start a plan")
session.ID() // Claude/Codex runtime session ID
```

## Stream and interrupt

```go
stream, err := session.Stream(ctx, "Refactor the authentication module")
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

Call `session.Interrupt(ctx)` from another goroutine to stop a running turn.
Interrupt is normal control flow; the waiting result has `Interrupted == true`.
When using `Stream`, drain `stream.Result()` before starting the next turn so
the interrupted turn's terminal event is finalized.

## Blocked turns

An agent can pause for user input, tool approval, permission, authentication,
or another external action:

```go
result, err := session.Run(ctx, "Run the tests and fix failures")
if err != nil {
    return err
}
if result.Blocked != nil {
    fmt.Println(result.Blocked.Message)
    result, err = session.Continue(ctx, "allow_once")
}
```

`BlockedReason.Options` is best-effort and may be empty. When present, pass an
option's `Value` to `Continue`; otherwise pass free-form input.

## Find and continue a session

```go
sessions, err := agent.ListSessions(ctx)
session, err := agent.GetSession(ctx, sessions[0].ID)

// Runtime resume is automatic.
result, err := session.Run(ctx, "Continue the previous work")
```

`Session.State()` exposes only `Idle`, `Running`, and `Blocked`. Runtime process
and transport lifecycle remain internal.

## Test

```sh
go vet ./...
go test -race ./...
go test -tags e2e_real ./tests/...
```

Real E2E tests invoke authenticated CLIs and may consume tokens.

## License

MIT
