from __future__ import annotations

from typing import TYPE_CHECKING

from ._driver import DriverEventType, EventQueue, FinishReason, OutputKind
from .errors import runtime_reported, transport
from .models import Event, EventKind, Result, SessionState, ToolCall

if TYPE_CHECKING:
    from ._session import Session


class Stream:
    """The async event stream and accumulated result of one turn."""

    def __init__(self, owner: Session, events: EventQueue, run_id: str) -> None:
        self._owner = owner
        self._events = events
        self._run_id = run_id
        self._result = Result()
        self._done = False
        self._error: BaseException | None = None

    def __aiter__(self) -> Stream:
        return self

    async def __anext__(self) -> Event:
        if self._done:
            raise StopAsyncIteration
        try:
            while True:
                event = await self._events.get()
                if event is None:
                    self._finish(error=transport("event stream closed before completion"))
                    raise StopAsyncIteration
                if event.type is DriverEventType.SESSION_ATTACHED:
                    self._owner._set_id(event.session_id)
                    continue
                if event.run_id != self._run_id:
                    continue
                if event.type is DriverEventType.OUTPUT and event.item is not None:
                    if event.item.kind is OutputKind.TURN_END:
                        continue
                    return self._accumulate(event.item)
                if event.type is DriverEventType.COMPLETED:
                    if event.result is not None:
                        self._result.usage = dict(event.result.usage)
                        self._result.duration_ms = event.result.duration_ms
                        self._result.interrupted = (
                            event.result.finish_reason is FinishReason.CANCELLED
                        )
                        if event.result.finish_reason is FinishReason.TRANSPORT_CLOSED:
                            self._finish(error=transport("agent transport closed during turn"))
                            raise StopAsyncIteration
                    self._result.session_id = event.session_id
                    self._owner._set_id(event.session_id)
                    self._finish()
                    raise StopAsyncIteration
                if event.type is DriverEventType.BLOCKED:
                    self._result.session_id = event.session_id
                    self._result.blocked = event.blocked
                    self._finish(state=SessionState.BLOCKED)
                    raise StopAsyncIteration
                if event.type is DriverEventType.FAILED:
                    self._finish(error=event.error or runtime_reported("agent run failed"))
                    raise StopAsyncIteration
        except BaseException as error:
            if not isinstance(error, (StopAsyncIteration, GeneratorExit)) and not self._done:
                await self._owner.interrupt()
                self._finish(error=error)
            raise

    async def result(self) -> Result:
        """Drain remaining events and return the accumulated terminal Result."""
        async for _ in self:
            pass
        if self._error is not None:
            raise self._error
        return self._result

    def _accumulate(self, item: object) -> Event:
        from ._driver import OutputItem

        assert isinstance(item, OutputItem)
        kind = EventKind(item.kind.value)
        if kind is EventKind.TEXT:
            self._result.text += item.text
        elif kind is EventKind.THINKING:
            self._result.thinking += item.text
        elif kind is EventKind.TOOL_CALL:
            self._result.tool_calls.append(ToolCall(item.tool, item.tool_input))
        return Event(kind=kind, text=item.text, tool=item.tool, tool_input=item.tool_input)

    def _finish(
        self,
        *,
        state: SessionState = SessionState.IDLE,
        error: BaseException | None = None,
    ) -> None:
        if self._done:
            return
        self._done = True
        self._error = error
        self._owner._finish_turn(state)
