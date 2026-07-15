from __future__ import annotations

import asyncio
import json
import os
from collections.abc import Coroutine
from dataclasses import asdict
from pathlib import Path
from typing import Any

import pytest

import unio

pytestmark = [
    pytest.mark.e2e_real,
    pytest.mark.skipif(
        os.environ.get("UNIO_RUN_REAL_E2E") != "1",
        reason="set UNIO_RUN_REAL_E2E=1 to run authenticated CLI tests",
    ),
]


def run(coro: Coroutine[Any, Any, Any]) -> Any:
    return asyncio.run(coro)


def _preview(text: str, limit: int = 240) -> str:
    return " ".join(text.split())[:limit]


async def _statistics_when_persisted(
    session: unio.Session, timeout_seconds: float = 15
) -> unio.TokenStatistics:
    deadline = asyncio.get_running_loop().time() + timeout_seconds
    while True:
        try:
            return await session.token_statistics()
        except unio.AgentError as error:
            if error.kind is not unio.ErrorKind.PROTOCOL:
                raise
            if asyncio.get_running_loop().time() >= deadline:
                raise
            await asyncio.sleep(0.25)


def test_real_codex_stream_persistence_and_resume(tmp_path: Path) -> None:
    async def scenario() -> None:
        session_id = ""
        async with unio.Agent(unio.Codex, cwd=tmp_path) as agent:
            session = agent.new_session()
            stream = await session.stream("Reply with exactly: PYTHON_E2E_PONG")
            events = [event async for event in stream]
            result = await stream.result()
            assert result.text.strip()
            assert session.id
            assert result.session_id == session.id
            session_id = session.id

            raw = await session.raw()
            statistics = await _statistics_when_persisted(session)
            listed = await agent.list_sessions()
            assert any(item.id == session_id for item in listed)

            print(
                "E2E stream:",
                json.dumps(
                    {
                        "session_id_prefix": session_id[:8],
                        "event_kinds": [event.kind for event in events],
                        "text": _preview(result.text),
                        "turn_usage": {
                            model: asdict(usage) for model, usage in result.usage.items()
                        },
                        "raw_format": raw.format,
                        "raw_bytes": len(raw.data),
                        "session_statistics": asdict(statistics),
                    },
                    ensure_ascii=False,
                    default=str,
                ),
            )

        async with unio.Agent(unio.Codex, cwd=tmp_path) as resumed_agent:
            sessions = await resumed_agent.list_sessions()
            assert any(item.id == session_id for item in sessions)
            resumed = await resumed_agent.get_session(session_id)
            result = await resumed.run("Reply with exactly: PYTHON_E2E_RESUMED")
            assert result.text.strip()
            assert resumed.id == session_id
            print(
                "E2E resume:",
                json.dumps(
                    {
                        "session_id_prefix": resumed.id[:8],
                        "text": _preview(result.text),
                    },
                    ensure_ascii=False,
                ),
            )

    run(asyncio.wait_for(scenario(), timeout=150))


def test_real_codex_interrupt_and_reuse(tmp_path: Path) -> None:
    async def scenario() -> None:
        async with unio.Agent(unio.Codex, cwd=tmp_path) as agent:
            session = agent.new_session()
            stream = await session.stream(
                "Use the shell tool to run exactly `sleep 30`, then reply with done."
            )
            await asyncio.sleep(2)
            await session.interrupt()
            interrupted = await asyncio.wait_for(stream.result(), timeout=60)
            assert interrupted.interrupted

            follow_up = await session.run("Reply with exactly: PYTHON_E2E_OK")
            assert follow_up.text.strip()
            print(
                "E2E interrupt:",
                json.dumps(
                    {
                        "interrupted": interrupted.interrupted,
                        "partial_text": _preview(interrupted.text),
                        "follow_up_text": _preview(follow_up.text),
                    },
                    ensure_ascii=False,
                    default=str,
                ),
            )

    run(asyncio.wait_for(scenario(), timeout=150))
