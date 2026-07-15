# Error handling

unio returns typed SDK errors from `github.com/Fullstop000/unio/errs`. Match an
error by its `ErrorKind`; error messages contain runtime-specific diagnostic
details and are not a stable API.

```go
result, err := session.Run("Explain this repository")
if err != nil {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}

	kind, ok := errs.KindOf(err)
	if !ok {
		return fmt.Errorf("run agent: %w", err)
	}
	switch kind {
	case errs.KindNotInstalled:
		return fmt.Errorf("install the selected agent CLI: %w", err)
	case errs.KindUnsupported:
		return fmt.Errorf("selected agent does not support this operation: %w", err)
	default:
		return fmt.Errorf("agent failed (%s): %w", kind, err)
	}
}

fmt.Println(result.Text)
```

## Error kinds

| Kind | Meaning | Common caller action | Retry guidance |
| --- | --- | --- | --- |
| `not_installed` | The selected CLI executable cannot be found. | Install the CLI or correct `PATH`/the configured executable. | Retry after fixing the installation. |
| `invalid_state` | The operation is not valid for the current Agent or Session state. Examples include starting a second turn on one Session or calling `Continue` while it is not blocked. | Inspect `Session.State`, finish the active `Stream`, or use a different Session. | Do not retry unchanged. |
| `session_not_found` | The runtime-owned session ID does not exist in the selected runtime's history. | Refresh `ListSessions`, choose another ID, or create a new Session. | Retry only with a valid ID. |
| `unsupported` | The selected runtime or its advertised capabilities do not implement the operation. | Check the [support matrix](API_SUPPORT.md) and choose another runtime or operation. | Do not retry unchanged. |
| `runtime_reported` | The CLI accepted the request but reported a domain failure, such as authentication, model, quota, or turn failure. | Inspect the message and the CLI's own authentication/configuration. | Depends on the runtime error. |
| `transport` | The child process, stdio transport, or runtime-owned history could not be opened, read, written, or kept alive. | Check whether the CLI is healthy and inspect the wrapped diagnostic. Recreate the Agent if failures persist. | Retry only when duplicate turn submission is acceptable. |
| `protocol` | The runtime returned malformed, incomplete, or incompatible protocol/session data. | Check the CLI version and report a compatibility issue with the full error. | Usually not retryable without changing the runtime/version or waiting for persistence to finish. |
| `timeout` | A driver timed out waiting for a runtime response. | Check runtime health and the surrounding lifecycle deadline. | Retry only when duplicate work is acceptable. |

`transport`, `protocol`, and `runtime_reported` messages may include diagnostics
originating from the selected CLI. Sanitize them before sending logs to another
system if prompts, paths, command output, or credentials may be sensitive.

## Where errors surface

| Operation | Typical failures |
| --- | --- |
| `unio.New` | A cancelled parent context, unknown Agent kind, or `not_installed`. `New` currently verifies executable discovery; authentication and model errors may surface on the first runtime operation. |
| `Agent.ListSessions` | `unsupported`, `transport`, `protocol`, or a context error while starting/querying the runtime. |
| `Agent.GetSession` | `session_not_found`, `unsupported`, or an error from listing runtime history. It does not contact the live runtime to resume the session. |
| `Agent.NewSession` | `invalid_state` or a context error after the Agent has closed. |
| `Session.Run` / `Session.Stream` / `Stream.Result` | `invalid_state`, runtime/transport/protocol failures, or a context error. A returned `Result` may contain partial text, thinking, and tool calls produced before the error. |
| `Session.Interrupt` | A transport error when the runtime cannot confirm interruption. Calling it while idle is a successful no-op. |
| `Session.Continue` | `invalid_state` unless the Session is blocked; otherwise the same runtime and transport failures as a turn. |
| `Session.Raw` / `Session.TokenStatistics` | `invalid_state` for an active or ID-less Session, `unsupported` for runtimes without persisted data, `session_not_found`, `transport`, or `protocol`. |

The context passed to `unio.New` owns the entire Agent lifecycle. Cancelling it
can make an in-flight operation return `context.Canceled` or
`context.DeadlineExceeded`; these are standard Go context errors, not unio
`ErrorKind` values. Check them with `errors.Is` before calling `errs.KindOf`.

## Normal control flow is not an error

A blocked turn returns `err == nil`, sets `Result.Blocked`, and leaves the
Session in `unio.Blocked`. Supply an advertised option value (or free-form input
when no options are advertised) to `Session.Continue`.

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

Confirmed interruption is also normal control flow: `err == nil` and
`Result.Interrupted == true`. With a manual Stream, always call `Result` after
interrupting so the terminal event is consumed before starting another turn.

## Matching and wrapping

`errs.KindOf` works through wrapped errors and is the preferred way to branch
on all eight categories. Use `errors.As` when the diagnostic message is needed:

```go
var agentErr *errs.AgentError
if errors.As(err, &agentErr) {
	fmt.Printf("kind=%s message=%s\n", agentErr.Kind, agentErr.Msg)
}
```

The root package also exposes `unio.ErrInvalidState` and
`unio.ErrSessionNotFound` for concise category matching:

```go
if errors.Is(err, unio.ErrInvalidState) {
	// Finish the active turn or wait until the Session returns to idle.
}
```

Wrapping an error with `%w` preserves all of these matching behaviors.
