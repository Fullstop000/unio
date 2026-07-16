from __future__ import annotations

import asyncio
from collections.abc import Coroutine
from pathlib import Path
from typing import Any, TypeVar

import pytest

import unio
from unio._driver import FinishReason, OutputItem, OutputKind, RunResult
from unio._drivers.fake import FakeDriver, Script

T = TypeVar("T")


def run(coro: Coroutine[Any, Any, T]) -> T:
    return asyncio.run(coro)


def test_run_assigns_runtime_id_and_accumulates_result(tmp_path: Path) -> None:
    async def scenario() -> None:
        driver = FakeDriver(
            Script(
                items=[
                    OutputItem(OutputKind.THINKING, text="considering"),
                    OutputItem(OutputKind.TEXT, text="hello"),
                    OutputItem(OutputKind.TOOL_CALL, tool="shell", tool_input={"cmd": "pwd"}),
                ],
                result=RunResult(FinishReason.NATURAL),
            )
        )
        async with unio.Agent(unio.Codex, cwd=str(tmp_path), _driver=driver) as agent:
            session = agent.new_session()
            assert session.id == ""
            result = await session.run(unio.UserMessage("hello"))
            assert result.text == "hello"
            assert result.thinking == "considering"
            assert result.tool_calls[0].name == "shell"
            assert result.session_id == session.id
            assert session.state is unio.Idle

    run(scenario())


def test_stream_yields_events(tmp_path: Path) -> None:
    async def scenario() -> None:
        driver = FakeDriver(
            Script(
                items=[OutputItem(OutputKind.TEXT, text="a"), OutputItem(OutputKind.TEXT, text="b")]
            )
        )
        async with unio.Agent(unio.Codex, cwd=str(tmp_path), _driver=driver) as agent:
            stream = await agent.new_session().stream(unio.UserMessage("go"))
            chunks = [event.text async for event in stream]
            result = await stream.result()
            assert chunks == ["a", "b"]
            assert result.text == "ab"

    run(scenario())


def test_block_and_respond_with_option(tmp_path: Path) -> None:
    async def scenario() -> None:
        reason = unio.BlockedReason(
            unio.BlockedKind.TOOL_APPROVAL,
            "approve shell",
            (unio.BlockOption("allow_once", "Allow once"),),
        )
        driver = FakeDriver(
            Script(blocked=reason),
            Script(items=[OutputItem(OutputKind.TEXT, text="continued")]),
        )
        async with unio.Agent(unio.Codex, cwd=str(tmp_path), _driver=driver) as agent:
            session = agent.new_session()
            blocked = await session.run(unio.UserMessage("go"))
            assert blocked.blocked == reason
            assert session.state is unio.Blocked
            stream = await session.stream(unio.OptionSelection("allow_once"))
            events = [event async for event in stream]
            result = await stream.result()
            assert result.text == "continued"
            assert "".join(event.text for event in events) == result.text
            assert session.state is unio.Idle

    run(scenario())


def test_input_variant_must_match_session_state(tmp_path: Path) -> None:
    async def scenario() -> None:
        driver = FakeDriver(
            Script(
                blocked=unio.BlockedReason(
                    unio.BlockedKind.TOOL_APPROVAL,
                    "approve shell",
                    (unio.BlockOption("allow_once", "Allow once"),),
                )
            )
        )
        async with unio.Agent(unio.Codex, cwd=tmp_path, _driver=driver) as agent:
            session = agent.new_session()
            with pytest.raises(unio.AgentError) as idle_error:
                await session.run(unio.OptionSelection("allow_once"))
            assert idle_error.value.kind is unio.ErrorKind.INVALID_STATE
            assert session.state is unio.Idle
            assert session.id == ""

            blocked = await session.run(unio.UserMessage("run shell"))
            assert blocked.blocked is not None
            with pytest.raises(unio.AgentError) as blocked_error:
                await session.run(unio.UserMessage("allow_once"))
            assert blocked_error.value.kind is unio.ErrorKind.INVALID_STATE
            assert session.state is unio.Blocked

    run(scenario())


def test_blocked_user_input_rejects_option(tmp_path: Path) -> None:
    async def scenario() -> None:
        driver = FakeDriver(
            Script(blocked=unio.BlockedReason(unio.BlockedKind.USER_INPUT, "Reply"))
        )
        async with unio.Agent(unio.Codex, cwd=tmp_path, _driver=driver) as agent:
            session = agent.new_session()
            blocked = await session.run(unio.UserMessage("ask"))
            assert blocked.blocked is not None
            with pytest.raises(unio.AgentError) as caught:
                await session.run(unio.OptionSelection("reply"))
            assert caught.value.kind is unio.ErrorKind.INVALID_STATE
            assert session.state is unio.Blocked

    run(scenario())


def test_block_and_respond_with_user_message(tmp_path: Path) -> None:
    async def scenario() -> None:
        reason = unio.BlockedReason(
            unio.BlockedKind.USER_INPUT,
            "Which database?",
        )
        driver = FakeDriver(
            Script(blocked=reason),
            Script(items=[OutputItem(OutputKind.TEXT, text="using PostgreSQL")]),
        )
        async with unio.Agent(unio.Codex, cwd=tmp_path, _driver=driver) as agent:
            session = agent.new_session()
            blocked = await session.run(unio.UserMessage("change the database"))
            assert blocked.blocked == reason
            assert session.state is unio.Blocked
            result = await session.run(unio.UserMessage("PostgreSQL"))
            assert result.text == "using PostgreSQL"
            assert session.state is unio.Idle

    run(scenario())


def test_same_session_rejects_concurrent_turn(tmp_path: Path) -> None:
    async def scenario() -> None:
        gate = asyncio.Event()
        driver = FakeDriver(Script(wait=gate))
        async with unio.Agent(unio.Codex, cwd=str(tmp_path), _driver=driver) as agent:
            session = agent.new_session()
            stream = await session.stream(unio.UserMessage("first"))
            with pytest.raises(unio.AgentError) as caught:
                await session.stream(unio.UserMessage("second"))
            assert caught.value.kind is unio.ErrorKind.INVALID_STATE
            await session.interrupt()
            result = await stream.result()
            assert result.interrupted

    run(scenario())


def test_get_session_reuses_handle(tmp_path: Path) -> None:
    async def scenario() -> None:
        driver = FakeDriver()
        async with unio.Agent(unio.Codex, cwd=str(tmp_path), _driver=driver) as agent:
            session = agent.new_session()
            await session.run(unio.UserMessage("first"))
            assert await agent.get_session(session.id) is session

    run(scenario())
