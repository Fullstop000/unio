from __future__ import annotations

import asyncio
import json
from collections.abc import Coroutine
from pathlib import Path
from types import SimpleNamespace
from typing import Any

import pytest

import unio
from unio._driver import AgentSpec, DriverEventType, OutputKind, ProcessPhase
from unio._drivers.acp import _ACP_STREAM_LIMIT, ACPSession, _ACPProcess, _Config, _config
from unio._drivers.claude import ClaudeSession
from unio._drivers.codex import CodexSession, _CodexProcess


def run(coro: Coroutine[Any, Any, Any]) -> Any:
    return asyncio.run(coro)


async def _next(session: ClaudeSession | CodexSession | ACPSession) -> Any:
    return await session.events.get()


def test_claude_stream_json_maps_text_tool_and_usage(tmp_path: Path) -> None:
    spec = AgentSpec(tmp_path, model="claude-test")
    session = ClaudeSession("claude", spec, tmp_path, "")
    session._active_run = "run"
    session._phase = ProcessPhase.PROMPT_IN_FLIGHT
    session._handle_line(
        json.dumps({"type": "system", "subtype": "init", "session_id": "s1"}).encode()
    )
    session._handle_line(
        json.dumps(
            {
                "type": "stream_event",
                "event": {
                    "type": "content_block_delta",
                    "index": 0,
                    "delta": {"type": "text_delta", "text": "hello"},
                },
            }
        ).encode()
    )
    session._handle_line(
        json.dumps(
            {
                "type": "result",
                "session_id": "s1",
                "duration_ms": 12,
                "usage": {"input_tokens": 3, "output_tokens": 4},
            }
        ).encode()
    )
    attached, output, completed = run(_next(session)), run(_next(session)), run(_next(session))
    assert attached.type is DriverEventType.SESSION_ATTACHED
    assert output.item is not None and output.item.kind is OutputKind.TEXT
    assert output.item.text == "hello"
    assert completed.result is not None
    assert completed.result.usage["claude-test"].output_tokens == 4


def test_codex_notifications_map_output_approval_and_completion(tmp_path: Path) -> None:
    spec = AgentSpec(tmp_path)
    process = _CodexProcess("codex", spec)
    session = CodexSession(process, spec, tmp_path, "thread-1")
    session._id = "thread-1"
    session._active_run = "run-1"
    session._phase = ProcessPhase.PROMPT_IN_FLIGHT
    session._handle("item/agentMessage/delta", None, {"delta": "hello"})
    session._handle("item/commandExecution/requestApproval", 9, {"threadId": "thread-1"})
    output, blocked = run(_next(session)), run(_next(session))
    assert output.item is not None and output.item.text == "hello"
    assert blocked.type is DriverEventType.BLOCKED
    assert blocked.blocked is not None
    assert [item.value for item in blocked.blocked.options] == ["allow_once", "deny", "cancel"]

    session._active_run = "run-2"
    session._phase = ProcessPhase.PROMPT_IN_FLIGHT
    session._handle("turn/completed", None, {"turn": {"status": "completed"}})
    turn_end, completed = run(_next(session)), run(_next(session))
    assert turn_end.item is not None and turn_end.item.kind is OutputKind.TURN_END
    assert completed.type is DriverEventType.COMPLETED


def test_acp_updates_map_thinking_tools_and_permission(tmp_path: Path) -> None:
    spec = AgentSpec(tmp_path)
    config = _Config("kimi", "kimi-cli", ())
    process = _ACPProcess("kimi-cli", config, spec)
    session = ACPSession(process, config, spec, "s1")
    session._id = "s1"
    session._active_run = "run"
    session._phase = ProcessPhase.PROMPT_IN_FLIGHT
    session._update({"sessionUpdate": "agent_thought_chunk", "chunk": "thinking"})
    session._update(
        {
            "sessionUpdate": "tool_call",
            "toolCallId": "t1",
            "toolName": "shell",
            "rawInput": {"command": "pwd"},
        }
    )
    session._update(
        {
            "sessionUpdate": "tool_call_update",
            "toolCallId": "t1",
            "status": "completed",
            "content": [{"type": "text", "text": "ok"}],
        }
    )
    session._permission(
        7,
        {
            "toolCall": {"title": "Run command"},
            "options": [{"optionId": "yes", "name": "Allow"}],
        },
    )
    thinking, tool, result, blocked = (
        run(_next(session)),
        run(_next(session)),
        run(_next(session)),
        run(_next(session)),
    )
    assert thinking.item is not None and thinking.item.kind is OutputKind.THINKING
    assert tool.item is not None and tool.item.tool == "shell"
    assert result.item is not None and result.item.kind is OutputKind.TOOL_RESULT
    assert blocked.blocked is not None and blocked.blocked.options[0].value == "yes"


def test_acp_submission_failure_restores_active_state(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    async def scenario() -> None:
        spec = AgentSpec(tmp_path, system_prompt="system")
        config = _Config("traex", "traex", ())
        process = _ACPProcess("traex", config, spec)
        session = ACPSession(process, config, spec, "s1")
        session._id = "s1"
        session._phase = ProcessPhase.ACTIVE

        async def fail_request(method: str, params: dict[str, Any]) -> Any:
            raise unio.AgentError(unio.ErrorKind.TRANSPORT, "write failed")

        monkeypatch.setattr(process, "request", fail_request)
        with pytest.raises(unio.AgentError, match="write failed"):
            await session.send(unio.UserMessage("hello"))
        assert session.phase is ProcessPhase.ACTIVE
        assert session._active_run == ""
        assert session._first_turn

    run(scenario())


@pytest.mark.parametrize("runtime", ["acp", "codex"])
def test_initialization_failure_allows_retry(
    runtime: str, tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    async def scenario() -> None:
        spec = AgentSpec(tmp_path)
        child = SimpleNamespace(stdout=None, stderr=None, returncode=1)

        async def spawn(*args: Any, **kwargs: Any) -> Any:
            return child

        async def fail_initialize(*args: Any, **kwargs: Any) -> Any:
            raise unio.AgentError(unio.ErrorKind.PROTOCOL, "initialize failed")

        monkeypatch.setattr(asyncio, "create_subprocess_exec", spawn)
        if runtime == "acp":
            process = _ACPProcess("traex", _Config("traex", "traex", ()), spec)
            monkeypatch.setattr(process, "call", fail_initialize)
        else:
            process = _CodexProcess("codex", spec)
            monkeypatch.setattr(process, "request", fail_initialize)

        with pytest.raises(unio.AgentError, match="initialize failed"):
            await process.start()
        assert process._process is None
        assert process._reader is None
        assert process._stderr is None

    run(scenario())


@pytest.mark.parametrize("runtime", ["acp", "codex"])
def test_blocked_response_failure_remains_retryable(
    runtime: str, tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    async def scenario() -> None:
        spec = AgentSpec(tmp_path)

        async def fail_respond(request_id: Any, result: Any) -> None:
            raise unio.AgentError(unio.ErrorKind.TRANSPORT, "write failed")

        if runtime == "acp":
            config = _Config("traex", "traex", ())
            process = _ACPProcess("traex", config, spec)
            session = ACPSession(process, config, spec, "s1")
            session._permission_id = 7
            session._permission_options = {"allow_once"}
        else:
            process = _CodexProcess("codex", spec)
            session = CodexSession(process, spec, tmp_path, "s1")
            session._approval_id = 7
        session._id = "s1"
        session._phase = ProcessPhase.BLOCKED
        monkeypatch.setattr(process, "respond", fail_respond)

        with pytest.raises(unio.AgentError, match="write failed"):
            await session.respond(unio.OptionSelection("allow_once"))
        assert session.phase is ProcessPhase.BLOCKED
        assert session._active_run == ""
        if runtime == "acp":
            assert isinstance(session, ACPSession)
            assert session._permission_id == 7
        else:
            assert isinstance(session, CodexSession)
            assert session._approval_id == 7

    run(scenario())


def test_codex_blocked_interrupt_waits_for_completion_and_allows_reuse(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    async def scenario() -> None:
        spec = AgentSpec(tmp_path)
        process = _CodexProcess("codex", spec)
        session = CodexSession(process, spec, tmp_path, "s1")
        session._id = "s1"
        session._turn_id = "turn-1"
        session._turn_done = asyncio.Event()
        session._approval_id = 7
        session._phase = ProcessPhase.BLOCKED

        async def request(method: str, params: dict[str, Any]) -> dict[str, Any]:
            if method == "turn/interrupt":
                session._handle(
                    "turn/completed",
                    None,
                    {"turn": {"id": "turn-1", "status": "interrupted"}},
                )
                return {}
            assert method == "turn/start"
            return {"turn": {"id": "turn-2"}}

        monkeypatch.setattr(process, "request", request)
        await session.interrupt()
        assert session.phase is ProcessPhase.ACTIVE
        assert session._approval_id is None
        assert session._turn_id == ""

        run_id = await session.send(unio.UserMessage("next"))
        assert run_id
        assert session.phase is ProcessPhase.PROMPT_IN_FLIGHT
        assert session._turn_id == "turn-2"

    run(scenario())


def test_acp_stream_limit_accepts_large_single_line_responses() -> None:
    # OpenCode includes its complete model catalog in session/new. That JSON-RPC
    # response can exceed asyncio's 64 KiB default StreamReader limit.
    response = json.dumps({"models": ["x" * 1024] * 128}).encode() + b"\n"
    assert len(response) > 64 * 1024
    assert len(response) < _ACP_STREAM_LIMIT


def test_traex_discovers_the_canonical_user_install_path() -> None:
    assert str(Path.home() / ".local/bin/traecli") in _config(unio.TraeX).alternatives


def test_acp_persisted_statistics_match_runtime_formats(tmp_path: Path) -> None:
    spec = AgentSpec(tmp_path)
    kimi_config = _Config("kimi", "kimi-cli", ())
    kimi = ACPSession(_ACPProcess("kimi-cli", kimi_config, spec), kimi_config, spec, "s")
    kimi_data = b"\n".join(
        (
            b'{"type":"usage.record","usageScope":"turn","usage":{"inputOther":10,"output":3,"inputCacheRead":20,"inputCacheCreation":4}}',
            b'{"message":{"type":"StatusUpdate","payload":{"token_usage":{"input_other":2,"output":1,"input_cache_read":5}}}}',
        )
    )
    statistics = kimi._statistics(kimi_data)
    assert statistics.input_tokens == 41
    assert statistics.output_tokens == 4
    assert statistics.cache_read_tokens == 25
    assert statistics.cache_write_tokens == 4

    traex_config = _Config("traex", "traex", ())
    traex = ACPSession(_ACPProcess("traex", traex_config, spec), traex_config, spec, "s")
    traex_data = (
        b'{"type":"event_msg","payload":{"type":"token_count","info":'
        b'{"last_token_usage":{"input_tokens":20,"output_tokens":4,'
        b'"cached_input_tokens":12,"cache_creation_input_tokens":3}}}}'
    )
    statistics = traex._statistics(traex_data)
    assert statistics.input_tokens == 20
    assert statistics.output_tokens == 4
    assert statistics.cache_read_tokens == 12
    assert statistics.cache_write_tokens == 3


def test_acp_statistics_reject_incomplete_data(tmp_path: Path) -> None:
    spec = AgentSpec(tmp_path)
    kimi_config = _Config("kimi", "kimi-cli", ())
    kimi = ACPSession(_ACPProcess("kimi-cli", kimi_config, spec), kimi_config, spec, "s")
    with pytest.raises(unio.AgentError, match="not fully persisted"):
        kimi._statistics(
            b'{"type":"context.append_loop_event","event":{"type":"step.begin","uuid":"step","turnId":1}}'
        )

    traex_config = _Config("traex", "traex", ())
    traex = ACPSession(_ACPProcess("traex", traex_config, spec), traex_config, spec, "s")
    with pytest.raises(unio.AgentError, match="not fully persisted"):
        traex._statistics(b'{"type":"event_msg","payload":{"type":"task_started"}}')
