from __future__ import annotations

import asyncio
import json
import re
from dataclasses import dataclass
from datetime import UTC, datetime
from pathlib import Path
from typing import Any, cast

from .._driver import (
    AgentSpec,
    Driver,
    DriverEvent,
    DriverEventType,
    DriverSession,
    FinishReason,
    OutputItem,
    OutputKind,
    ProcessPhase,
    RunResult,
    StoredSession,
    new_run_id,
    resolve_executable,
)
from ..errors import (
    invalid_state,
    protocol,
    runtime_reported,
    session_not_found,
    transport,
    unsupported,
)
from ..models import (
    RawSessionData,
    SessionDataFormat,
    TokenStatistics,
    TokenUsage,
    UserInput,
    UserMessage,
)


def _json_object(value: Any) -> dict[str, Any]:
    return cast(dict[str, Any], value) if isinstance(value, dict) else {}


def _claude_root() -> Path:
    return Path.home() / ".claude" / "projects"


def _encode_cwd(cwd: Path) -> str:
    return re.sub(r"[^A-Za-z0-9]", "-", str(cwd))


def _resolved_path(path: str | Path) -> Path:
    return Path(path).resolve()


def _find_session_file(session_id: str) -> Path:
    if not session_id:
        raise session_not_found(session_id)
    root = _claude_root()
    if root.exists():
        for path in root.rglob(f"{session_id}.jsonl"):
            if path.is_file():
                return path
    raise session_not_found(session_id)


def _read_stored_sessions(cwd: str | None) -> list[StoredSession]:
    root = _claude_root()
    if not root.exists():
        return []
    sessions: list[StoredSession] = []
    for path in root.rglob("*.jsonl"):
        try:
            info = path.stat()
            session_id = path.stem
            session_cwd = ""
            title = ""
            messages = 0
            with path.open(encoding="utf-8") as source:
                for line in source:
                    try:
                        record = _json_object(json.loads(line))
                    except (json.JSONDecodeError, UnicodeDecodeError):
                        continue
                    session_id = str(record.get("sessionId") or session_id)
                    session_cwd = str(record.get("cwd") or session_cwd)
                    title = str(record.get("lastPrompt") or title)
                    if record.get("type") in {"user", "assistant"}:
                        messages += 1
            if cwd is None or Path(session_cwd).resolve() == Path(cwd).resolve():
                modified = datetime.fromtimestamp(info.st_mtime, UTC)
                sessions.append(
                    StoredSession(
                        session_id=session_id,
                        title=title,
                        cwd=session_cwd,
                        started_at=modified,
                        updated_at=modified,
                        message_count=messages,
                    )
                )
        except OSError:
            continue
    return sorted(
        sessions, key=lambda item: item.started_at or datetime.min.replace(tzinfo=UTC), reverse=True
    )


def _parse_statistics(data: bytes) -> TokenStatistics:
    total = TokenUsage()
    by_message: dict[str, TokenUsage] = {}
    pending_response = False
    for raw_line in data.splitlines():
        if not raw_line.strip():
            continue
        try:
            record = _json_object(json.loads(raw_line))
        except (json.JSONDecodeError, UnicodeDecodeError) as error:
            raise protocol("claude: invalid JSONL session record") from error
        record_type = record.get("type")
        sidechain = bool(record.get("isSidechain"))
        if record_type == "user" and not sidechain:
            pending_response = True
            continue
        if record_type != "assistant":
            continue
        if not sidechain:
            pending_response = False
        message = _json_object(record.get("message"))
        usage = _json_object(message.get("usage"))
        cache_read = int(usage.get("cache_read_input_tokens") or 0)
        cache_write = int(usage.get("cache_creation_input_tokens") or 0)
        item = TokenUsage(
            input_tokens=int(usage.get("input_tokens") or 0) + cache_read + cache_write,
            output_tokens=int(usage.get("output_tokens") or 0),
            cache_read_tokens=cache_read,
            cache_write_tokens=cache_write,
        )
        message_id = str(message.get("id") or "")
        if message_id:
            by_message[message_id] = item
        else:
            total += item
    if pending_response:
        raise protocol("claude: latest turn is not fully persisted yet")
    for item in by_message.values():
        total += item
    return TokenStatistics(
        input_tokens=total.input_tokens,
        output_tokens=total.output_tokens,
        cache_read_tokens=total.cache_read_tokens,
        cache_write_tokens=total.cache_write_tokens,
    )


class ClaudeDriver(Driver):
    def __init__(self, spec: AgentSpec) -> None:
        self._spec = spec
        self._executable = ""
        self._sessions: set[ClaudeSession] = set()

    def probe(self) -> None:
        self._executable = resolve_executable("claude")

    async def list_sessions(self, cwd: str | None) -> list[StoredSession]:
        return await asyncio.to_thread(_read_stored_sessions, cwd)

    async def open_session(self, resume_id: str = "", cwd: str = "") -> DriverSession:
        selected_cwd = _resolved_path(cwd or self._spec.cwd)
        if resume_id:
            expected = _claude_root() / _encode_cwd(selected_cwd) / f"{resume_id}.jsonl"
            if not await asyncio.to_thread(expected.is_file):
                await asyncio.to_thread(_find_session_file, resume_id)
        session = ClaudeSession(self._executable, self._spec, selected_cwd, resume_id)
        self._sessions.add(session)
        return session

    async def close(self) -> None:
        await asyncio.gather(*(item.close() for item in self._sessions))


@dataclass(slots=True)
class _ToolInput:
    name: str
    parts: list[str]


class ClaudeSession(DriverSession):
    def __init__(self, executable: str, spec: AgentSpec, cwd: Path, resume_id: str) -> None:
        super().__init__()
        self._executable = executable
        self._spec = spec
        self._cwd = cwd
        self._id = resume_id
        self._resume_id = resume_id
        self._phase = ProcessPhase.IDLE
        self._process: asyncio.subprocess.Process | None = None
        self._reader_task: asyncio.Task[None] | None = None
        self._stderr_task: asyncio.Task[None] | None = None
        self._active_run = ""
        self._streamed = False
        self._interrupted = False
        self._tools: dict[int, _ToolInput] = {}

    @property
    def session_id(self) -> str:
        return self._id

    @property
    def phase(self) -> ProcessPhase:
        return self._phase

    async def start(self) -> None:
        if self._phase is ProcessPhase.CLOSED:
            raise invalid_state("claude session is closed")
        if self._process is not None:
            return
        self._phase = ProcessPhase.STARTING
        args = [
            "-p",
            "--output-format",
            "stream-json",
            "--input-format",
            "stream-json",
            "--verbose",
            "--include-partial-messages",
        ]
        if self._spec.model:
            args.extend(("--model", self._spec.model))
        if self._spec.system_prompt:
            args.extend(("--append-system-prompt", self._spec.system_prompt))
        if self._resume_id:
            args.extend(("--resume", self._resume_id))
        args.extend(self._spec.extra_args)
        try:
            self._process = await asyncio.create_subprocess_exec(
                self._executable,
                *args,
                cwd=self._cwd,
                env=self._spec.child_env(),
                stdin=asyncio.subprocess.PIPE,
                stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.PIPE,
            )
        except OSError as error:
            self._phase = ProcessPhase.FAILED
            raise transport(f"claude: start process: {error}") from error
        self._reader_task = asyncio.create_task(self._reader_loop())
        self._stderr_task = asyncio.create_task(self._drain_stderr())

    async def send(self, value: UserInput) -> str:
        process = self._process
        if process is None or process.stdin is None:
            raise transport("claude: send before start")
        if self._phase not in {ProcessPhase.STARTING, ProcessPhase.ACTIVE}:
            raise invalid_state("claude session is not active")
        if not isinstance(value, UserMessage):
            raise invalid_state("claude: a new turn requires UserMessage")
        run_id = new_run_id()
        self._active_run = run_id
        self._streamed = False
        self._phase = ProcessPhase.PROMPT_IN_FLIGHT
        payload = {
            "type": "user",
            "message": {"role": "user", "content": value.text},
        }
        try:
            process.stdin.write(json.dumps(payload, separators=(",", ":")).encode() + b"\n")
            await process.stdin.drain()
        except (BrokenPipeError, ConnectionError, OSError) as error:
            self._phase = ProcessPhase.CLOSED
            raise transport(f"claude: write stdin: {error}") from error
        return run_id

    async def respond(self, value: UserInput) -> str:
        raise unsupported("claude: no blocked turn")

    async def interrupt(self) -> None:
        if self._phase is not ProcessPhase.PROMPT_IN_FLIGHT:
            return
        self._interrupted = True
        await self._terminate()

    async def raw(self) -> RawSessionData:
        path = await asyncio.to_thread(_find_session_file, self._id or self._resume_id)
        try:
            data = await asyncio.to_thread(path.read_bytes)
        except OSError as error:
            raise protocol(f"claude: read session data: {error}") from error
        return RawSessionData(SessionDataFormat.JSONL, data)

    async def token_statistics(self) -> TokenStatistics:
        raw = await self.raw()
        return await asyncio.to_thread(_parse_statistics, raw.data)

    async def close(self) -> None:
        if self._phase is ProcessPhase.CLOSED:
            return
        await self._terminate()
        self._phase = ProcessPhase.CLOSED
        self.events.close()

    async def _terminate(self) -> None:
        process = self._process
        if process is not None and process.returncode is None:
            process.terminate()
        if self._reader_task is not None and self._reader_task is not asyncio.current_task():
            try:
                await asyncio.wait_for(self._reader_task, 10)
            except TimeoutError:
                if process is not None and process.returncode is None:
                    process.kill()
                await self._reader_task
        if self._stderr_task is not None:
            await asyncio.gather(self._stderr_task, return_exceptions=True)

    async def _drain_stderr(self) -> None:
        process = self._process
        if process is None or process.stderr is None:
            return
        while await process.stderr.readline():
            pass

    async def _reader_loop(self) -> None:
        process = self._process
        if process is None or process.stdout is None:
            return
        try:
            while line := await process.stdout.readline():
                self._handle_line(line)
            await process.wait()
        finally:
            if self._active_run:
                run_id = self._active_run
                self._active_run = ""
                finish = (
                    FinishReason.CANCELLED if self._interrupted else FinishReason.TRANSPORT_CLOSED
                )
                self.events.emit(
                    DriverEvent(
                        DriverEventType.COMPLETED,
                        session_id=self._id,
                        run_id=run_id,
                        result=RunResult(finish),
                    )
                )
            self._interrupted = False
            self._phase = ProcessPhase.CLOSED

    def _handle_line(self, line: bytes) -> None:
        try:
            message = _json_object(json.loads(line))
        except (json.JSONDecodeError, UnicodeDecodeError):
            return
        message_type = message.get("type")
        if message_type == "system" and message.get("subtype") == "init":
            session_id = str(message.get("session_id") or "")
            if session_id:
                self._id = session_id
                self.events.emit(
                    DriverEvent(DriverEventType.SESSION_ATTACHED, session_id=session_id)
                )
                if not self._active_run:
                    self._phase = ProcessPhase.ACTIVE
            return
        if message_type == "stream_event":
            self._handle_stream_event(_json_object(message.get("event")))
            return
        if message_type == "assistant" and not self._streamed:
            self._handle_complete_message(_json_object(message.get("message")))
            return
        if message_type == "result":
            self._finish_result(message)

    def _handle_stream_event(self, event: dict[str, Any]) -> None:
        event_type = event.get("type")
        index = int(event.get("index") or 0)
        if event_type == "content_block_start":
            block = _json_object(event.get("content_block"))
            if block.get("type") == "tool_use":
                self._tools[index] = _ToolInput(str(block.get("name") or ""), [])
            return
        if event_type == "content_block_delta":
            delta = _json_object(event.get("delta"))
            delta_type = delta.get("type")
            if delta_type == "thinking_delta":
                self._emit(OutputItem(OutputKind.THINKING, text=str(delta.get("thinking") or "")))
                self._streamed = True
            elif delta_type == "text_delta":
                self._emit(OutputItem(OutputKind.TEXT, text=str(delta.get("text") or "")))
                self._streamed = True
            elif delta_type == "input_json_delta" and index in self._tools:
                self._tools[index].parts.append(str(delta.get("partial_json") or ""))
            return
        if event_type == "content_block_stop" and (tool := self._tools.pop(index, None)):
            raw = "".join(tool.parts)
            try:
                tool_input: Any = json.loads(raw) if raw else None
            except json.JSONDecodeError:
                tool_input = raw
            self._emit(OutputItem(OutputKind.TOOL_CALL, tool=tool.name, tool_input=tool_input))
            self._streamed = True

    def _handle_complete_message(self, message: dict[str, Any]) -> None:
        content = message.get("content")
        if not isinstance(content, list):
            return
        for raw in cast(list[Any], content):
            block = _json_object(raw)
            kind = block.get("type")
            if kind == "text":
                self._emit(OutputItem(OutputKind.TEXT, text=str(block.get("text") or "")))
            elif kind == "thinking":
                self._emit(OutputItem(OutputKind.THINKING, text=str(block.get("thinking") or "")))
            elif kind == "tool_use":
                self._emit(
                    OutputItem(
                        OutputKind.TOOL_CALL,
                        tool=str(block.get("name") or ""),
                        tool_input=block.get("input"),
                    )
                )

    def _finish_result(self, message: dict[str, Any]) -> None:
        run_id = self._active_run
        if not run_id:
            return
        session_id = str(message.get("session_id") or self._id)
        if session_id:
            self._id = session_id
        if bool(message.get("is_error")):
            self.events.emit(
                DriverEvent(
                    DriverEventType.FAILED,
                    session_id=self._id,
                    run_id=run_id,
                    error=runtime_reported(str(message.get("result") or "claude turn failed")),
                )
            )
        else:
            usage = _json_object(message.get("usage"))
            token_usage = TokenUsage(
                input_tokens=int(usage.get("input_tokens") or 0),
                output_tokens=int(usage.get("output_tokens") or 0),
                cache_read_tokens=int(usage.get("cache_read_input_tokens") or 0),
                cache_write_tokens=int(usage.get("cache_creation_input_tokens") or 0),
                cost_usd=float(message.get("total_cost_usd") or 0),
            )
            has_usage = any(
                (
                    token_usage.input_tokens,
                    token_usage.output_tokens,
                    token_usage.cache_read_tokens,
                    token_usage.cache_write_tokens,
                    token_usage.cost_usd,
                )
            )
            model = self._spec.model or "claude"
            self.events.emit(
                DriverEvent(
                    DriverEventType.COMPLETED,
                    session_id=self._id,
                    run_id=run_id,
                    result=RunResult(
                        FinishReason.NATURAL,
                        {model: token_usage} if has_usage else {},
                        int(message.get("duration_ms") or 0),
                    ),
                )
            )
        self._active_run = ""
        self._phase = ProcessPhase.ACTIVE

    def _emit(self, item: OutputItem) -> None:
        self.events.emit(
            DriverEvent(
                DriverEventType.OUTPUT,
                session_id=self._id,
                run_id=self._active_run,
                item=item,
            )
        )
