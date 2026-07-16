from __future__ import annotations

import json
from pathlib import Path
from typing import cast

import unio
from unio._driver import DriverEventType, DriverTransport, FinishReason, ProcessPhase


def _contract() -> dict[str, list[str] | str]:
    path = Path(__file__).parents[2] / "docs" / "contract-v0.7.json"
    return cast(dict[str, list[str] | str], json.loads(path.read_text(encoding="utf-8")))


def test_frozen_values_match_shared_contract() -> None:
    contract = _contract()
    assert contract["spec_version"] == "0.7.0"
    assert [str(item) for item in unio.AgentKind] == contract["agent_kind"]
    assert [str(item) for item in unio.SessionState] == contract["session_state"]
    assert [str(item) for item in unio.BlockedKind] == contract["blocked_kind"]
    assert [str(item) for item in unio.EventKind] == contract["event_kind"]
    assert [str(item) for item in unio.ErrorKind] == contract["error_kind"]
    assert [str(item) for item in unio.SessionDataFormat] == contract["session_data_format"]
    assert [str(item) for item in DriverTransport] == contract["driver_transport"]
    assert [str(item) for item in ProcessPhase] == contract["driver_lifecycle"]
    assert [str(item) for item in DriverEventType] == contract["driver_event_type"]
    assert [str(item) for item in FinishReason] == contract["finish_reason"]
