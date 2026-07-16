# unio for Python

Async Python SDK for using Claude Code, Codex, and ACP-native coding agents
through one API. Python 3.11 or newer is required.

The Python SDK follows the shared [unio behavior
specification](https://github.com/Fullstop000/unio/blob/master/docs/SPEC.md).
Its package and release versions are independent from the Go SDK.

## Install

Python 3.11+ is supported. After the first `python-v0.1.0` release, install the
SDK and separately install and authenticate the CLI you intend to drive:

```sh
python -m pip install unio-py
codex --version  # or claude/kimi/opencode --version
```

Runtime discovery matches the [support
matrix](https://github.com/Fullstop000/unio/blob/master/docs/API_SUPPORT.md).
Creating an `Agent` checks that the executable exists; authentication and
provider errors can surface on the first operation.

The PyPI distribution is named `unio-py`; the import package remains `unio`.

For development from this repository:

```sh
cd python
python -m venv .venv
. .venv/bin/activate
python -m pip install -e '.[dev]'
```

## Basic usage

```python
import asyncio

import unio


async def main() -> None:
    async with unio.Agent(unio.Codex, cwd="/path/to/repo") as agent:
        session = agent.new_session()
        result = await session.run(unio.UserMessage("Explain this repository"))
        print(result.text)


asyncio.run(main())
```

`Agent` owns its child process and sessions, so prefer `async with` or call
`await agent.close()`.

## Stream output

```python
async with unio.Agent(unio.Claude, cwd=".") as agent:
    session = agent.new_session()
    stream = await session.stream(unio.UserMessage("Review this repository"))
    async for event in stream:
        if event.kind in {unio.EventKind.TEXT, unio.EventKind.THINKING}:
            print(event.text, end="")
        elif event.kind is unio.EventKind.TOOL_CALL:
            print(f"tool={event.tool} input={event.tool_input!r}")
    result = await stream.result()
```

Always fully consume a manual stream or call `await stream.result()`. A Session
remains running until its terminal event is consumed.

## Approvals and interruption

```python
result = await session.run(unio.UserMessage("Apply the change"))
while result.blocked is not None:
    if result.blocked.options:
        for option in result.blocked.options:
            print(f"{option.value}: {option.label}")
        selected = await asyncio.to_thread(input, "Choose an option value: ")
        user_input = unio.OptionSelection(selected)
    else:
        reply = await asyncio.to_thread(input, f"{result.blocked.message}\nReply: ")
        user_input = unio.UserMessage(reply)
    result = await session.run(user_input)
```

`run` and `stream` accept the same `UserInput` union. On an idle Session,
`UserMessage` starts a turn. On a blocked Session, use `OptionSelection` for an
advertised option or `UserMessage` when the runtime requests free-form input.
Use `await session.interrupt()` to stop a running or blocked turn. Confirmed
interruption sets `result.interrupted`; an idle interrupt is a no-op.

Only one turn may run on a Session at a time; separate Sessions owned by one
Agent may run concurrently. Cancelling a task that is consuming a Stream asks
the runtime to interrupt that turn. `Agent.close()` closes every Session and
can raise an `ExceptionGroup` if one or more Session cleanups fail. Consume
stream events promptly: the internal queue is bounded and may drop intermediate
events for a slow consumer while preserving terminal events.

## Resume and inspect sessions

```python
items = await agent.list_sessions(limit=20)
if not items:
    raise RuntimeError("No persisted sessions")
session = await agent.get_session(items[0].id)
result = await session.run(unio.UserMessage("Continue the previous work"))

raw = await session.raw()
statistics = await session.token_statistics()
```

Listing defaults to the Agent working directory. Pass `all_workspaces=True` to
remove that filter. Raw data can contain prompts, code, commands, outputs,
paths, and credentials; review and redact it before logging or transmitting it.

## Configuration

```python
agent = unio.Agent(
    unio.OpenCode,
    cwd="/path/to/repo",
    model="provider/model",
    system_prompt="Keep changes minimal.",
    extra_args=("--some-runtime-flag",),
    env={"RUNTIME_SETTING": "value"},
)
```

`cwd` selects a working directory; it is not a sandbox. Codex app-server
arguments are fixed, so `extra_args` does not affect Codex. The child CLI
inherits the current OS user's file, process, environment, and network access;
approval behavior varies by runtime. `env` overrides inherited variables with
the same name. Model and system-prompt validation normally occurs when the
runtime starts. An invalid Agent kind raises `ValueError`; runtime failures use
`AgentError`. Use a least-privilege environment for untrusted repositories and
see the [security policy](https://github.com/Fullstop000/unio/blob/master/SECURITY.md).

## Errors and support

Catch `unio.AgentError` and branch on `error.kind`, not message text. See the
shared [error guide](https://github.com/Fullstop000/unio/blob/master/docs/ERRORS.md)
and [runtime support
matrix](https://github.com/Fullstop000/unio/blob/master/docs/API_SUPPORT.md). The
SDK is fully typed and ships `py.typed`.

Runnable examples are in
[`python/examples/`](https://github.com/Fullstop000/unio/tree/master/python/examples).
For local quality gates and release steps, see the [contribution
guide](https://github.com/Fullstop000/unio/blob/master/CONTRIBUTING.md).

Authenticated Codex, TraeX, and OpenCode E2E tests are opt-in because they can
consume tokens:

```sh
UNIO_RUN_REAL_E2E=1 pytest -s tests/e2e_real
```

Run one runtime directly with:

```sh
UNIO_RUN_REAL_E2E=1 pytest -s tests/e2e_real/test_codex.py
UNIO_RUN_REAL_E2E=1 pytest -s tests/e2e_real/test_acp.py -k traex
UNIO_RUN_REAL_E2E=1 pytest -s tests/e2e_real/test_acp.py -k opencode
```

The OpenCode test defaults to `deepseek/deepseek-v4-flash`. Override it with
`UNIO_E2E_OPENCODE_MODEL=provider/model` when another authenticated model is
available locally.
