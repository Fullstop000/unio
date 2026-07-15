from __future__ import annotations

import asyncio
import json
import os
from collections.abc import Coroutine
from dataclasses import asdict, dataclass
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


@dataclass(frozen=True, slots=True)
class RuntimeCase:
    kind: unio.AgentKind
    marker: str


RUNTIMES = (
    pytest.param(RuntimeCase(unio.TraeX, "TRAEX"), id="traex"),
    pytest.param(RuntimeCase(unio.OpenCode, "OPENCODE"), id="opencode"),
)


def run(coro: Coroutine[Any, Any, Any]) -> Any:
    return asyncio.run(coro)


def _agent(case: RuntimeCase, cwd: Path) -> unio.Agent:
    if case.kind is unio.OpenCode:
        return unio.Agent(
            case.kind,
            cwd=cwd,
            model=os.environ.get("UNIO_E2E_OPENCODE_MODEL", "deepseek/deepseek-v4-flash"),
            extra_args=("--pure",),
        )
    return unio.Agent(case.kind, cwd=cwd)


def _preview(text: str, limit: int = 240) -> str:
    return " ".join(text.split())[:limit]


async def _traex_persistence(session: unio.Session) -> dict[str, Any]:
    deadline = asyncio.get_running_loop().time() + 15
    while True:
        try:
            raw = await session.raw()
            statistics = await session.token_statistics()
            return {
                "raw_format": raw.format,
                "raw_bytes": len(raw.data),
                "session_statistics": asdict(statistics),
            }
        except unio.AgentError as error:
            if error.kind is not unio.ErrorKind.PROTOCOL:
                raise
            if asyncio.get_running_loop().time() >= deadline:
                raise
            await asyncio.sleep(0.25)


@pytest.mark.parametrize("case", RUNTIMES)
def test_real_acp_stream_persistence_and_resume(case: RuntimeCase, tmp_path: Path) -> None:
    async def scenario() -> None:
        first_marker = f"PYTHON_{case.marker}_E2E_PONG"
        resumed_marker = f"PYTHON_{case.marker}_E2E_RESUMED"

        async with _agent(case, tmp_path) as agent:
            session = agent.new_session()
            stream = await session.stream(f"Reply with exactly: {first_marker}")
            events = [event async for event in stream]
            result = await stream.result()
            assert result.text.strip() == first_marker
            assert session.id
            assert result.session_id == session.id

            details: dict[str, Any] = {}
            if case.kind is unio.TraeX:
                details = await _traex_persistence(session)

            listed = await agent.list_sessions()
            assert any(item.id == session.id for item in listed)
            session_id = session.id
            print(
                f"E2E {case.kind} stream:",
                json.dumps(
                    {
                        "session_id_prefix": session_id[:8],
                        "event_kinds": [event.kind for event in events],
                        "text": _preview(result.text),
                        **details,
                    },
                    ensure_ascii=False,
                    default=str,
                ),
            )

        async with _agent(case, tmp_path) as resumed_agent:
            sessions = await resumed_agent.list_sessions()
            assert any(item.id == session_id for item in sessions)
            resumed = await resumed_agent.get_session(session_id)
            result = await resumed.run(f"Reply with exactly: {resumed_marker}")
            assert result.text.strip() == resumed_marker
            assert resumed.id == session_id
            print(
                f"E2E {case.kind} resume:",
                json.dumps(
                    {
                        "session_id_prefix": resumed.id[:8],
                        "text": _preview(result.text),
                    },
                    ensure_ascii=False,
                ),
            )

    run(asyncio.wait_for(scenario(), timeout=180))


@pytest.mark.parametrize("case", RUNTIMES)
def test_real_acp_interrupt_and_reuse(case: RuntimeCase, tmp_path: Path) -> None:
    async def scenario() -> None:
        follow_up_marker = f"PYTHON_{case.marker}_E2E_OK"
        async with _agent(case, tmp_path) as agent:
            session = agent.new_session()
            stream = await session.stream(
                "Without using tools, write every integer from 1 to 100000, one per line."
            )
            await asyncio.sleep(0.5)
            await session.interrupt()
            interrupted = await asyncio.wait_for(stream.result(), timeout=60)
            assert interrupted.interrupted

            follow_up = await session.run(f"Reply with exactly: {follow_up_marker}")
            assert follow_up.text.strip() == follow_up_marker
            print(
                f"E2E {case.kind} interrupt:",
                json.dumps(
                    {
                        "interrupted": interrupted.interrupted,
                        "partial_text": _preview(interrupted.text),
                        "follow_up_text": _preview(follow_up.text),
                    },
                    ensure_ascii=False,
                ),
            )

    run(asyncio.wait_for(scenario(), timeout=180))
