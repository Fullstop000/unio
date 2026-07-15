from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING

from ._driver import DriverSession, ProcessPhase
from ._stream import Stream
from .errors import invalid_state, transport
from .models import RawSessionData, Result, SessionState, TokenStatistics

if TYPE_CHECKING:
    from ._agent import Agent


class Session:
    """One runtime-owned conversation."""

    def __init__(self, agent: Agent, session_id: str = "", cwd: str = "") -> None:
        self._agent = agent
        self._id = session_id
        self._cwd = cwd
        self._state = SessionState.IDLE
        self._inner: DriverSession | None = None
        self._started = False
        self._active: Stream | None = None
        self._op_lock = asyncio.Lock()

    @property
    def id(self) -> str:
        return self._id

    @property
    def state(self) -> SessionState:
        return self._state

    async def run(self, prompt: str) -> Result:
        """Run one turn and collect its final Result."""
        stream = await self.stream(prompt)
        return await stream.result()

    async def stream(self, prompt: str) -> Stream:
        """Submit one turn and return its asynchronous event Stream."""
        async with self._op_lock:
            self._agent._require_open()
            if self._state is not SessionState.IDLE:
                raise invalid_state(f"cannot run session while {self._state}")
            self._state = SessionState.RUNNING
            try:
                inner = await self._ensure_attached()
                run_id = await inner.prompt(prompt)
            except BaseException:
                self._state = SessionState.IDLE
                raise
            stream = Stream(self, inner.events, run_id)
            self._active = stream
            return stream

    async def continue_(self, value: str) -> Result:
        """Resume a blocked turn with an advertised option or free-form value."""
        async with self._op_lock:
            self._agent._require_open()
            if self._state is not SessionState.BLOCKED:
                raise invalid_state("continue_ requires a blocked session")
            inner = self._inner
            if inner is None or inner.phase is ProcessPhase.CLOSED:
                self._state = SessionState.IDLE
                raise transport("agent transport closed while blocked")
            self._state = SessionState.RUNNING
            try:
                run_id = await inner.continue_(value)
            except BaseException:
                self._state = SessionState.BLOCKED
                raise
            stream = Stream(self, inner.events, run_id)
            self._active = stream
        return await stream.result()

    async def interrupt(self) -> None:
        """Interrupt the active turn; an idle interrupt is a no-op."""
        async with self._op_lock:
            if self._state is SessionState.IDLE:
                return
            inner = self._inner
            if inner is None:
                raise invalid_state("session is not attached")
            blocked = self._state is SessionState.BLOCKED
            await inner.interrupt()
            if blocked:
                self._finish_turn(SessionState.IDLE)

    async def raw(self) -> RawSessionData:
        """Read the complete runtime-owned persisted JSONL session."""
        inner = await self._data_session()
        return await inner.raw()

    async def token_statistics(self) -> TokenStatistics:
        """Parse cumulative token statistics from :meth:`raw` session data."""
        inner = await self._data_session()
        return await inner.token_statistics()

    async def _data_session(self) -> DriverSession:
        async with self._op_lock:
            self._agent._require_open()
            if self._state is not SessionState.IDLE:
                raise invalid_state("session data requires an idle session")
            if not self._id:
                raise invalid_state("session has no runtime ID")
            return await self._ensure_handle()

    async def _ensure_attached(self) -> DriverSession:
        inner = await self._ensure_handle()
        if self._started:
            return inner
        try:
            await inner.start()
        except BaseException:
            await inner.close()
            self._inner = None
            raise
        self._started = True
        self._set_id(inner.session_id)
        return inner

    async def _ensure_handle(self) -> DriverSession:
        if self._inner is not None and self._inner.phase is not ProcessPhase.CLOSED:
            return self._inner
        if self._inner is not None:
            await self._inner.close()
        self._inner = await self._agent._driver.open_session(self._id, self._cwd)
        self._started = False
        return self._inner

    def _set_id(self, session_id: str) -> None:
        if not session_id:
            return
        if self._id and self._id != session_id:
            raise invalid_state(f"runtime session ID changed from {self._id} to {session_id}")
        self._id = session_id
        self._agent._register(self, session_id)

    def _finish_turn(self, state: SessionState) -> None:
        self._state = state
        if state is not SessionState.RUNNING:
            self._active = None

    async def _close(self) -> None:
        async with self._op_lock:
            inner = self._inner
            self._inner = None
            self._started = False
            self._state = SessionState.IDLE
            if inner is not None:
                await inner.close()
