from importlib.metadata import PackageNotFoundError, version

from ._agent import Agent
from ._session import Session
from ._stream import Stream
from .errors import AgentError, ErrorKind, kind_of
from .models import (
    AgentKind,
    BlockedKind,
    BlockedReason,
    BlockOption,
    Event,
    EventKind,
    OptionSelection,
    RawSessionData,
    Result,
    SessionDataFormat,
    SessionInfo,
    SessionState,
    TokenStatistics,
    TokenUsage,
    ToolCall,
    UserInput,
    UserMessage,
)

try:
    __version__ = version("unio-py")
except PackageNotFoundError:
    __version__ = "0.0.0+unknown"

Claude = AgentKind.CLAUDE
Codex = AgentKind.CODEX
Kimi = AgentKind.KIMI
TraeX = AgentKind.TRAEX
OpenCode = AgentKind.OPENCODE

Idle = SessionState.IDLE
Running = SessionState.RUNNING
Blocked = SessionState.BLOCKED

__all__ = [
    "Agent",
    "AgentError",
    "AgentKind",
    "BlockOption",
    "Blocked",
    "BlockedKind",
    "BlockedReason",
    "Claude",
    "Codex",
    "ErrorKind",
    "Event",
    "EventKind",
    "Idle",
    "Kimi",
    "OpenCode",
    "OptionSelection",
    "RawSessionData",
    "Result",
    "Running",
    "Session",
    "SessionDataFormat",
    "SessionInfo",
    "SessionState",
    "Stream",
    "TokenStatistics",
    "TokenUsage",
    "ToolCall",
    "TraeX",
    "UserInput",
    "UserMessage",
    "kind_of",
]
