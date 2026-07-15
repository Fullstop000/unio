from __future__ import annotations

from dataclasses import dataclass, field
from datetime import datetime
from enum import StrEnum
from typing import Any


class AgentKind(StrEnum):
    CLAUDE = "claude"
    CODEX = "codex"
    KIMI = "kimi"
    TRAEX = "traex"
    OPENCODE = "opencode"


class SessionState(StrEnum):
    IDLE = "idle"
    RUNNING = "running"
    BLOCKED = "blocked"


class BlockedKind(StrEnum):
    USER_INPUT = "user_input"
    TOOL_APPROVAL = "tool_approval"
    PERMISSION = "permission"
    AUTHENTICATION = "authentication"
    EXTERNAL = "external"


class EventKind(StrEnum):
    THINKING = "thinking"
    TEXT = "text"
    TOOL_CALL = "tool_call"
    TOOL_RESULT = "tool_result"


class SessionDataFormat(StrEnum):
    JSONL = "jsonl"


@dataclass(frozen=True, slots=True)
class BlockOption:
    value: str
    label: str


@dataclass(frozen=True, slots=True)
class BlockedReason:
    """Why a turn paused and the runtime-advertised response options."""

    kind: BlockedKind
    message: str = ""
    options: tuple[BlockOption, ...] = ()


@dataclass(frozen=True, slots=True)
class Event:
    """One public stream event."""

    kind: EventKind
    text: str = ""
    tool: str = ""
    tool_input: Any = None


@dataclass(frozen=True, slots=True)
class ToolCall:
    name: str
    input: Any = None


@dataclass(frozen=True, slots=True)
class TokenUsage:
    input_tokens: int = 0
    output_tokens: int = 0
    cache_read_tokens: int = 0
    cache_write_tokens: int = 0
    cost_usd: float = 0.0

    def __add__(self, other: TokenUsage) -> TokenUsage:
        return TokenUsage(
            input_tokens=self.input_tokens + other.input_tokens,
            output_tokens=self.output_tokens + other.output_tokens,
            cache_read_tokens=self.cache_read_tokens + other.cache_read_tokens,
            cache_write_tokens=self.cache_write_tokens + other.cache_write_tokens,
            cost_usd=self.cost_usd + other.cost_usd,
        )


@dataclass(slots=True)
class Result:
    """Accumulated output and terminal metadata for one SDK correlation run."""

    text: str = ""
    thinking: str = ""
    tool_calls: list[ToolCall] = field(default_factory=lambda: [])
    session_id: str = ""
    usage: dict[str, TokenUsage] = field(default_factory=lambda: {})
    duration_ms: int = 0
    interrupted: bool = False
    blocked: BlockedReason | None = None


@dataclass(frozen=True, slots=True)
class TokenStatistics:
    input_tokens: int = 0
    output_tokens: int = 0
    cache_read_tokens: int = 0
    cache_write_tokens: int = 0
    cost_usd: float = 0.0


@dataclass(frozen=True, slots=True)
class RawSessionData:
    format: SessionDataFormat
    data: bytes


@dataclass(frozen=True, slots=True)
class SessionInfo:
    """Best-effort metadata for one persisted runtime conversation."""

    id: str
    title: str = ""
    cwd: str = ""
    started_at: datetime | None = None
    updated_at: datetime | None = None
    message_count: int = 0
