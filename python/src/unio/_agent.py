from __future__ import annotations

import asyncio
from pathlib import Path
from types import TracebackType
from typing import TYPE_CHECKING

from ._driver import AgentSpec, Driver
from ._session import Session
from .errors import invalid_state, session_not_found
from .models import AgentKind, SessionInfo

if TYPE_CHECKING:
    from collections.abc import Mapping, Sequence


def _normalize_path(path: str | Path) -> str:
    return str(Path(path).expanduser().resolve())


class Agent:
    """One configured runtime and the Sessions it owns."""

    def __init__(
        self,
        kind: AgentKind | str,
        *,
        cwd: str | Path | None = None,
        model: str = "",
        system_prompt: str = "",
        extra_args: Sequence[str] = (),
        env: Mapping[str, str] | None = None,
        _driver: Driver | None = None,
    ) -> None:
        self.kind = AgentKind(kind)
        self.cwd = Path(cwd or Path.cwd()).expanduser().resolve()
        self._spec = AgentSpec(
            cwd=self.cwd,
            model=model,
            system_prompt=system_prompt,
            extra_args=tuple(extra_args),
            env=dict(env or {}),
        )
        if _driver is None:
            from ._drivers import create_driver

            _driver = create_driver(self.kind, self._spec)
        self._driver = _driver
        self._driver.probe()
        self._sessions: dict[str, Session] = {}
        self._pending: set[Session] = set()
        self._closed = False

    async def __aenter__(self) -> Agent:
        self._require_open()
        return self

    async def __aexit__(
        self,
        exc_type: type[BaseException] | None,
        exc: BaseException | None,
        traceback: TracebackType | None,
    ) -> None:
        await self.close()

    def new_session(self) -> Session:
        """Create an idle local Session; its runtime ID appears on first use."""
        self._require_open()
        session = Session(self, cwd=str(self.cwd))
        self._pending.add(session)
        return session

    async def list_sessions(
        self,
        *,
        cwd: str | Path | None = None,
        all_workspaces: bool = False,
        limit: int | None = None,
    ) -> list[SessionInfo]:
        """List persisted sessions, scoped to this workspace by default."""
        self._require_open()
        selected_cwd = None if all_workspaces else _normalize_path(cwd or self.cwd)
        stored = await self._driver.list_sessions(selected_cwd)
        infos = [
            SessionInfo(
                id=item.session_id,
                title=item.title,
                cwd=item.cwd,
                started_at=item.started_at,
                updated_at=item.updated_at,
                message_count=item.message_count,
            )
            for item in stored
        ]
        seen = {item.id for item in infos}
        for session_id, session in self._sessions.items():
            if selected_cwd and _normalize_path(session._cwd) != selected_cwd:
                continue
            if session_id not in seen:
                infos.append(SessionInfo(id=session_id, cwd=session._cwd))
        if limit is not None and limit > 0:
            return infos[:limit]
        return infos

    async def get_session(self, session_id: str) -> Session:
        """Return the maintained handle for a persisted runtime session ID."""
        self._require_open()
        if not session_id:
            raise session_not_found(session_id)
        if existing := self._sessions.get(session_id):
            return existing
        stored = await self._driver.list_sessions(None)
        match = next((item for item in stored if item.session_id == session_id), None)
        if match is None:
            raise session_not_found(session_id)
        session = Session(self, session_id, match.cwd)
        self._sessions[session_id] = session
        return session

    async def close(self) -> None:
        """Close all Sessions and child processes; repeated calls are safe."""
        if self._closed:
            return
        self._closed = True
        sessions = {*self._sessions.values(), *self._pending}
        results = await asyncio.gather(
            *(item._close() for item in sessions), return_exceptions=True
        )
        await self._driver.close()
        failures = [item for item in results if isinstance(item, Exception)]
        if failures:
            raise ExceptionGroup("failed to close Agent sessions", failures)

    def _register(self, session: Session, session_id: str) -> None:
        existing = self._sessions.get(session_id)
        if existing is not None and existing is not session:
            raise invalid_state("runtime session already has another live handle")
        self._pending.discard(session)
        self._sessions[session_id] = session

    def _require_open(self) -> None:
        if self._closed:
            raise invalid_state("agent is closed")
