from __future__ import annotations

from enum import StrEnum


class ErrorKind(StrEnum):
    TRANSPORT = "transport"
    PROTOCOL = "protocol"
    TIMEOUT = "timeout"
    RUNTIME_REPORTED = "runtime_reported"
    UNSUPPORTED = "unsupported"
    NOT_INSTALLED = "not_installed"
    INVALID_STATE = "invalid_state"
    SESSION_NOT_FOUND = "session_not_found"


class AgentError(Exception):
    """A stable error category with a runtime-specific diagnostic message."""

    def __init__(self, kind: ErrorKind, message: str = "") -> None:
        self.kind = kind
        self.message = message
        super().__init__(f"{kind}: {message}" if message else str(kind))


def kind_of(error: BaseException) -> ErrorKind | None:
    current: BaseException | None = error
    while current is not None:
        if isinstance(current, AgentError):
            return current.kind
        current = current.__cause__ or current.__context__
    return None


def transport(message: str) -> AgentError:
    return AgentError(ErrorKind.TRANSPORT, message)


def protocol(message: str) -> AgentError:
    return AgentError(ErrorKind.PROTOCOL, message)


def runtime_reported(message: str) -> AgentError:
    return AgentError(ErrorKind.RUNTIME_REPORTED, message)


def unsupported(message: str) -> AgentError:
    return AgentError(ErrorKind.UNSUPPORTED, message)


def not_installed(command: str) -> AgentError:
    return AgentError(ErrorKind.NOT_INSTALLED, f"{command} executable not found")


def invalid_state(message: str) -> AgentError:
    return AgentError(ErrorKind.INVALID_STATE, message)


def session_not_found(session_id: str) -> AgentError:
    return AgentError(ErrorKind.SESSION_NOT_FOUND, f"session {session_id!r} not found")
