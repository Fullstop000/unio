from __future__ import annotations

import asyncio
import uuid
from collections import deque
from dataclasses import dataclass, field

from .._driver import (
    Driver,
    DriverEvent,
    DriverEventType,
    DriverSession,
    FinishReason,
    OutputItem,
    ProcessPhase,
    RunResult,
    StoredSession,
    new_run_id,
)
from ..errors import AgentError, invalid_state, unsupported
from ..models import (
    BlockedReason,
    OptionSelection,
    RawSessionData,
    TokenStatistics,
    UserInput,
    UserMessage,
)


@dataclass(slots=True)
class Script:
    items: list[OutputItem] = field(default_factory=lambda: [])
    result: RunResult = field(default_factory=RunResult)
    blocked: BlockedReason | None = None
    error: AgentError | None = None
    wait: asyncio.Event | None = None


class FakeDriver(Driver):
    def __init__(self, *scripts: Script) -> None:
        self._scripts: deque[Script] = deque(scripts)
        self._sessions: list[FakeSession] = []

    def probe(self) -> None:
        return

    async def list_sessions(self, cwd: str | None) -> list[StoredSession]:
        return [
            StoredSession(item.session_id, cwd=item.cwd)
            for item in self._sessions
            if item.session_id and (cwd is None or item.cwd == cwd)
        ]

    async def open_session(self, resume_id: str = "", cwd: str = "") -> DriverSession:
        session = FakeSession(self, resume_id, cwd)
        self._sessions.append(session)
        return session

    async def close(self) -> None:
        await asyncio.gather(*(item.close() for item in self._sessions))

    def next_script(self, prompt: str) -> Script:
        if self._scripts:
            return self._scripts.popleft()
        from .._driver import OutputKind

        return Script(items=[OutputItem(OutputKind.TEXT, text=f"echo: {prompt}")])


class FakeSession(DriverSession):
    def __init__(self, driver: FakeDriver, resume_id: str, cwd: str) -> None:
        super().__init__()
        self._driver = driver
        self._id = resume_id
        self.cwd = cwd
        self._phase = ProcessPhase.IDLE
        self._active_run = ""
        self._blocked: BlockedReason | None = None
        self._task: asyncio.Task[None] | None = None

    @property
    def session_id(self) -> str:
        return self._id

    @property
    def phase(self) -> ProcessPhase:
        return self._phase

    async def start(self) -> None:
        if self._phase is ProcessPhase.CLOSED:
            raise invalid_state("fake session is closed")
        if not self._id:
            self._id = f"fake-{uuid.uuid4()}"
        self._phase = ProcessPhase.ACTIVE
        self.events.emit(DriverEvent(DriverEventType.SESSION_ATTACHED, session_id=self._id))

    async def send(self, value: UserInput) -> str:
        if self._phase is not ProcessPhase.ACTIVE:
            raise invalid_state("fake session is not active")
        if not isinstance(value, UserMessage):
            raise invalid_state("fake: a new turn requires UserMessage")
        return self._start_script(self._driver.next_script(value.text))

    async def respond(self, value: UserInput) -> str:
        if self._phase is not ProcessPhase.BLOCKED or self._blocked is None:
            raise invalid_state("fake session is not blocked")
        if self._blocked.options:
            if not isinstance(value, OptionSelection):
                raise invalid_state("fake: blocked options require OptionSelection")
            if value.value not in {item.value for item in self._blocked.options}:
                raise invalid_state("invalid blocked response")
            response = value.value
        else:
            if not isinstance(value, UserMessage):
                raise invalid_state("fake: blocked user input requires UserMessage")
            response = value.text
        self._blocked = None
        return self._start_script(self._driver.next_script(response))

    async def interrupt(self) -> None:
        if self._phase is ProcessPhase.BLOCKED:
            self._blocked = None
            self._phase = ProcessPhase.ACTIVE
            return
        if self._phase is not ProcessPhase.PROMPT_IN_FLIGHT:
            return
        if self._task is not None:
            self._task.cancel()
            try:
                await self._task
            except asyncio.CancelledError:
                pass
        run_id = self._active_run
        self._active_run = ""
        self._phase = ProcessPhase.ACTIVE
        self.events.emit(
            DriverEvent(
                DriverEventType.COMPLETED,
                session_id=self._id,
                run_id=run_id,
                result=RunResult(FinishReason.CANCELLED),
            )
        )

    async def raw(self) -> RawSessionData:
        raise unsupported("fake session data is unsupported")

    async def token_statistics(self) -> TokenStatistics:
        raise unsupported("fake session statistics are unsupported")

    async def close(self) -> None:
        if self._phase is ProcessPhase.CLOSED:
            return
        if self._task is not None:
            self._task.cancel()
            await asyncio.gather(self._task, return_exceptions=True)
        self._phase = ProcessPhase.CLOSED
        self.events.close()

    def _start_script(self, script: Script) -> str:
        run_id = new_run_id()
        self._active_run = run_id
        self._phase = ProcessPhase.PROMPT_IN_FLIGHT
        self._task = asyncio.create_task(self._execute(run_id, script))
        return run_id

    async def _execute(self, run_id: str, script: Script) -> None:
        if script.wait is not None:
            await script.wait.wait()
        for item in script.items:
            self.events.emit(
                DriverEvent(
                    DriverEventType.OUTPUT,
                    session_id=self._id,
                    run_id=run_id,
                    item=item,
                )
            )
        self._active_run = ""
        if script.blocked is not None:
            self._blocked = script.blocked
            self._phase = ProcessPhase.BLOCKED
            self.events.emit(
                DriverEvent(
                    DriverEventType.BLOCKED,
                    session_id=self._id,
                    run_id=run_id,
                    blocked=script.blocked,
                )
            )
            return
        self._phase = ProcessPhase.ACTIVE
        if script.error is not None:
            self.events.emit(
                DriverEvent(
                    DriverEventType.FAILED,
                    session_id=self._id,
                    run_id=run_id,
                    error=script.error,
                )
            )
            return
        self.events.emit(
            DriverEvent(
                DriverEventType.COMPLETED,
                session_id=self._id,
                run_id=run_id,
                result=script.result,
            )
        )
