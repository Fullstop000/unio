from __future__ import annotations

import asyncio
import os
import shutil
import uuid
from abc import ABC, abstractmethod
from dataclasses import dataclass, field
from datetime import datetime
from enum import StrEnum
from pathlib import Path
from typing import Any

from .errors import AgentError, not_installed
from .models import BlockedReason, RawSessionData, TokenStatistics, TokenUsage


class ProcessPhase(StrEnum):
    IDLE = "idle"
    STARTING = "starting"
    ACTIVE = "active"
    PROMPT_IN_FLIGHT = "prompt_in_flight"
    BLOCKED = "blocked"
    CLOSED = "closed"
    FAILED = "failed"


class DriverTransport(StrEnum):
    FAKE = "fake"
    ACP_NATIVE = "acp_native"
    CODEX_APP_SERVER = "codex_app_server"
    CLAUDE_STREAM_JSON = "claude_stream_json"


class DriverEventType(StrEnum):
    LIFECYCLE = "lifecycle"
    SESSION_ATTACHED = "session_attached"
    OUTPUT = "output"
    BLOCKED = "blocked"
    COMPLETED = "completed"
    FAILED = "failed"


class OutputKind(StrEnum):
    THINKING = "thinking"
    TEXT = "text"
    TOOL_CALL = "tool_call"
    TOOL_RESULT = "tool_result"
    TURN_END = "turn_end"


class FinishReason(StrEnum):
    NATURAL = "natural"
    CANCELLED = "cancelled"
    TRANSPORT_CLOSED = "transport_closed"


@dataclass(frozen=True, slots=True)
class AgentSpec:
    cwd: Path
    model: str = ""
    system_prompt: str = ""
    extra_args: tuple[str, ...] = ()
    env: dict[str, str] = field(default_factory=lambda: {})

    def child_env(self) -> dict[str, str]:
        return {**os.environ, **self.env}


@dataclass(frozen=True, slots=True)
class StoredSession:
    session_id: str
    title: str = ""
    cwd: str = ""
    started_at: datetime | None = None
    updated_at: datetime | None = None
    message_count: int = 0


@dataclass(frozen=True, slots=True)
class OutputItem:
    kind: OutputKind
    text: str = ""
    tool: str = ""
    tool_input: Any = None


@dataclass(frozen=True, slots=True)
class RunResult:
    finish_reason: FinishReason = FinishReason.NATURAL
    usage: dict[str, TokenUsage] = field(default_factory=lambda: {})
    duration_ms: int = 0


@dataclass(frozen=True, slots=True)
class DriverEvent:
    type: DriverEventType
    session_id: str = ""
    run_id: str = ""
    item: OutputItem | None = None
    result: RunResult | None = None
    error: AgentError | None = None
    blocked: BlockedReason | None = None


_TERMINAL_EVENTS = {
    DriverEventType.BLOCKED,
    DriverEventType.COMPLETED,
    DriverEventType.FAILED,
}


class EventQueue:
    """Bounded, drop-on-full queue that preserves terminal events."""

    def __init__(self, capacity: int = 256) -> None:
        self._queue: asyncio.Queue[DriverEvent | None] = asyncio.Queue(capacity)
        self._closed = False
        self.dropped = 0

    def emit(self, event: DriverEvent) -> None:
        if self._closed:
            self.dropped += 1
            return
        try:
            self._queue.put_nowait(event)
            return
        except asyncio.QueueFull:
            if event.type not in _TERMINAL_EVENTS:
                self.dropped += 1
                return
        try:
            self._queue.get_nowait()
            self.dropped += 1
        except asyncio.QueueEmpty:
            pass
        try:
            self._queue.put_nowait(event)
        except asyncio.QueueFull:
            self.dropped += 1

    async def get(self) -> DriverEvent | None:
        return await self._queue.get()

    def close(self) -> None:
        if self._closed:
            return
        self._closed = True
        try:
            self._queue.put_nowait(None)
        except asyncio.QueueFull:
            try:
                self._queue.get_nowait()
            except asyncio.QueueEmpty:
                pass
            self._queue.put_nowait(None)


class DriverSession(ABC):
    def __init__(self) -> None:
        self.events = EventQueue()

    @property
    @abstractmethod
    def session_id(self) -> str: ...

    @property
    @abstractmethod
    def phase(self) -> ProcessPhase: ...

    @abstractmethod
    async def start(self) -> None: ...

    @abstractmethod
    async def prompt(self, text: str) -> str: ...

    @abstractmethod
    async def continue_(self, value: str) -> str: ...

    @abstractmethod
    async def interrupt(self) -> None: ...

    @abstractmethod
    async def raw(self) -> RawSessionData: ...

    @abstractmethod
    async def token_statistics(self) -> TokenStatistics: ...

    @abstractmethod
    async def close(self) -> None: ...


class Driver(ABC):
    @abstractmethod
    def probe(self) -> None: ...

    @abstractmethod
    async def list_sessions(self, cwd: str | None) -> list[StoredSession]: ...

    @abstractmethod
    async def open_session(self, resume_id: str = "", cwd: str = "") -> DriverSession: ...

    @abstractmethod
    async def close(self) -> None: ...


def resolve_executable(primary: str, *alternatives: str) -> str:
    for command in (primary, *alternatives):
        if path := shutil.which(command):
            return path
    raise not_installed(primary)


def new_run_id() -> str:
    return str(uuid.uuid4())
