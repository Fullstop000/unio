from __future__ import annotations

from .._driver import AgentSpec, Driver
from ..models import AgentKind


def create_driver(kind: AgentKind, spec: AgentSpec) -> Driver:
    if kind is AgentKind.CLAUDE:
        from .claude import ClaudeDriver

        return ClaudeDriver(spec)
    if kind is AgentKind.CODEX:
        from .codex import CodexDriver

        return CodexDriver(spec)
    if kind in {AgentKind.KIMI, AgentKind.TRAEX, AgentKind.OPENCODE}:
        from .acp import ACPDriver

        return ACPDriver(kind, spec)
    raise ValueError(f"unknown agent kind: {kind}")
