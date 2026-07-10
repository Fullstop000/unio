# unio

Go SDK for driving coding-agent CLIs with one API.

Supported:

- Claude Code
- Codex

## Install

```sh
go get github.com/Fullstop000/unio
```

The target CLI must be installed and logged in.

Current stability: Go API v0.1.0. `Start`, `Run`, `Session`, `Stream`,
`Result`, and typed errors are intended as the public surface.

## Usage

```go
res, err := unio.Run(ctx, unio.Claude, "Reply with exactly one word: ping")
if err != nil {
    return err
}

fmt.Println(res.Text)
fmt.Println(res.SessionID)
fmt.Println(res.Usage)
```

```go
s, err := unio.Start(ctx, unio.Codex, unio.WithCwd("/path/to/repo"))
if err != nil {
    return err
}
defer s.Close()

st := s.Prompt(ctx, "Explain this repository")
for st.Next() {
    ev := st.Event()
    if ev.Kind == unio.KindText {
        fmt.Print(ev.Text)
    }
}

res, err := st.Result()
```

Prompts on the same `Session` are serial. Open multiple sessions for parallel
runs.

Resume uses the runtime session id:

```go
res, _ := unio.Run(ctx, unio.Claude, "Start a plan")
s, err := unio.Start(ctx, unio.Claude, unio.WithResume(res.SessionID))
```

## API

```go
unio.Run(ctx, agent, prompt, opts...) (unio.Result, error)
unio.Start(ctx, agent, opts...) (*unio.Session, error)
unio.Installed(agent) bool

session.Prompt(ctx, prompt) *unio.Stream
session.Cancel(ctx) (bool, error)
session.Close() error

stream.Next() bool
stream.Event() unio.Event
stream.Result() (unio.Result, error)
```

`Result` includes text, thinking, tool calls, session id, finish reason, and
token usage.

## Test

```sh
go test -race ./...
go test -tags e2e_real ./tests/...
```

Real E2E tests require installed, authenticated CLIs.

## License

MIT
