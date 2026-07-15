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
            result = await session.run("hello")
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
            stream = await agent.new_session().stream("go")
            chunks = [event.text async for event in stream]
            result = await stream.result()
            assert chunks == ["a", "b"]
            assert result.text == "ab"

    run(scenario())


def test_block_and_continue(tmp_path: Path) -> None:
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
            blocked = await session.run("go")
            assert blocked.blocked == reason
            assert session.state is unio.Blocked
            result = await session.continue_("allow_once")
            assert result.text == "continued"
            assert session.state is unio.Idle

    run(scenario())


def test_same_session_rejects_concurrent_turn(tmp_path: Path) -> None:
    async def scenario() -> None:
        gate = asyncio.Event()
        driver = FakeDriver(Script(wait=gate))
        async with unio.Agent(unio.Codex, cwd=str(tmp_path), _driver=driver) as agent:
            session = agent.new_session()
            stream = await session.stream("first")
            with pytest.raises(unio.AgentError) as caught:
                await session.stream("second")
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
            await session.run("first")
            assert await agent.get_session(session.id) is session

    run(scenario())
