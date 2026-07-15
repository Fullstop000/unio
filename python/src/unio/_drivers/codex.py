from __future__ import annotations

import asyncio
import json
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
from ..errors import invalid_state, protocol, runtime_reported, session_not_found, transport
from ..models import (
    BlockedKind,
    BlockedReason,
    BlockOption,
    RawSessionData,
    SessionDataFormat,
    TokenStatistics,
    TokenUsage,
)


def _object(value: Any) -> dict[str, Any]:
    return cast(dict[str, Any], value) if isinstance(value, dict) else {}


def _resolved_path(value: str | Path) -> Path:
    return Path(value).resolve()


def _sessions_root() -> Path:
    return Path.home() / ".codex" / "sessions"


def _find_session_file(session_id: str) -> Path:
    if not session_id:
        raise session_not_found(session_id)
    root = _sessions_root()
    if root.exists():
        for path in root.rglob(f"*-{session_id}.jsonl"):
            if path.is_file():
                return path
    raise session_not_found(session_id)


def _timestamp(value: Any, fallback: datetime) -> datetime:
    if isinstance(value, str):
        try:
            return datetime.fromisoformat(value.replace("Z", "+00:00"))
        except ValueError:
            pass
    return fallback


def _stored_sessions(cwd: str | None) -> list[StoredSession]:
    root = _sessions_root()
    if not root.exists():
        return []
    result: list[StoredSession] = []
    for path in root.rglob("*.jsonl"):
        try:
            modified = datetime.fromtimestamp(path.stat().st_mtime, UTC)
            session_id = ""
            session_cwd = ""
            title = ""
            started = modified
            count = 0
            with path.open(encoding="utf-8") as source:
                for line in source:
                    try:
                        record = _object(json.loads(line))
                    except (json.JSONDecodeError, UnicodeDecodeError):
                        continue
                    payload = _object(record.get("payload"))
                    record_type = record.get("type")
                    if record_type == "session_meta":
                        session_id = str(payload.get("id") or session_id)
                        session_cwd = str(payload.get("cwd") or session_cwd)
                        started = _timestamp(payload.get("timestamp"), started)
                    elif record_type == "event_msg":
                        event_type = payload.get("type")
                        if event_type in {"user_message", "agent_message"}:
                            count += 1
                        if event_type == "user_message" and not title:
                            title = str(payload.get("message") or "")[:120]
                    elif record_type == "response_item":
                        if payload.get("type") == "message" and payload.get("role") in {
                            "user",
                            "assistant",
                        }:
                            count += 1
            if not session_id or (cwd and Path(session_cwd).resolve() != Path(cwd).resolve()):
                continue
            result.append(StoredSession(session_id, title, session_cwd, started, modified, count))
        except OSError:
            continue
    return sorted(
        result, key=lambda item: item.started_at or datetime.min.replace(tzinfo=UTC), reverse=True
    )


def _statistics(data: bytes) -> TokenStatistics:
    total = TokenStatistics()
    pending = 0
    for line in data.splitlines():
        if not line.strip():
            continue
        try:
            record = _object(json.loads(line))
        except (json.JSONDecodeError, UnicodeDecodeError) as error:
            raise protocol("codex: invalid JSONL session record") from error
        if record.get("type") != "event_msg":
            continue
        payload = _object(record.get("payload"))
        event_type = payload.get("type")
        if event_type == "task_started":
            pending += 1
        elif event_type in {"task_complete", "turn_aborted"}:
            pending = max(0, pending - 1)
        elif event_type == "token_count":
            info = _object(payload.get("info"))
            usage = _object(info.get("total_token_usage"))
            total = TokenStatistics(
                input_tokens=int(usage.get("input_tokens") or 0),
                output_tokens=int(usage.get("output_tokens") or 0),
                cache_read_tokens=int(usage.get("cached_input_tokens") or 0),
            )
    if pending:
        raise protocol("codex: latest task is not fully persisted yet")
    return total


class CodexDriver(Driver):
    def __init__(self, spec: AgentSpec) -> None:
        self._spec = spec
        self._executable = ""
        self._process: _CodexProcess | None = None
        self._sessions: set[CodexSession] = set()

    def probe(self) -> None:
        self._executable = resolve_executable("codex")

    async def list_sessions(self, cwd: str | None) -> list[StoredSession]:
        return await asyncio.to_thread(_stored_sessions, cwd)

    async def open_session(self, resume_id: str = "", cwd: str = "") -> DriverSession:
        if resume_id:
            await asyncio.to_thread(_find_session_file, resume_id)
        if self._process is None:
            self._process = _CodexProcess(self._executable, self._spec)
        session = CodexSession(
            self._process, self._spec, _resolved_path(cwd or self._spec.cwd), resume_id
        )
        self._sessions.add(session)
        return session

    async def close(self) -> None:
        await asyncio.gather(*(session.close() for session in self._sessions))
        if self._process is not None:
            await self._process.close()


class _CodexProcess:
    def __init__(self, executable: str, spec: AgentSpec) -> None:
        self._executable = executable
        self._spec = spec
        self._process: asyncio.subprocess.Process | None = None
        self._reader: asyncio.Task[None] | None = None
        self._stderr: asyncio.Task[None] | None = None
        self._start_lock = asyncio.Lock()
        self._write_lock = asyncio.Lock()
        self._next_id = 0
        self._pending: dict[int, asyncio.Future[dict[str, Any]]] = {}
        self._sessions: dict[str, CodexSession] = {}
        self._turn_threads: dict[str, str] = {}

    async def start(self) -> None:
        async with self._start_lock:
            if self._process is not None:
                return
            try:
                self._process = await asyncio.create_subprocess_exec(
                    self._executable,
                    "app-server",
                    "--listen",
                    "stdio://",
                    cwd=self._spec.cwd,
                    env=self._spec.child_env(),
                    stdin=asyncio.subprocess.PIPE,
                    stdout=asyncio.subprocess.PIPE,
                    stderr=asyncio.subprocess.PIPE,
                )
            except OSError as error:
                raise transport(f"codex: start app-server: {error}") from error
            self._reader = asyncio.create_task(self._read_loop())
            self._stderr = asyncio.create_task(self._drain_stderr())
            await self.request(
                "initialize",
                {
                    "clientInfo": {"name": "unio", "title": "unio", "version": "0.1.0"},
                    "capabilities": {},
                },
            )
            await self.notify("initialized", {})

    async def request(self, method: str, params: dict[str, Any]) -> dict[str, Any]:
        self._next_id += 1
        request_id = self._next_id
        future = asyncio.get_running_loop().create_future()
        self._pending[request_id] = future
        try:
            await self._write({"id": request_id, "method": method, "params": params})
            response = await future
        finally:
            self._pending.pop(request_id, None)
        error = _object(response.get("error"))
        if error:
            data = _object(error.get("data"))
            message = str(data.get("message") or error.get("message") or "unknown error")
            raise protocol(f"codex {method}: {message}")
        return _object(response.get("result"))

    async def notify(self, method: str, params: dict[str, Any]) -> None:
        await self._write({"method": method, "params": params})

    async def respond(self, request_id: Any, result: Any) -> None:
        await self._write({"id": request_id, "result": result})

    async def _write(self, message: dict[str, Any]) -> None:
        process = self._process
        if process is None or process.stdin is None:
            raise transport("codex app-server is not running")
        line = json.dumps(message, separators=(",", ":")).encode() + b"\n"
        async with self._write_lock:
            try:
                process.stdin.write(line)
                await process.stdin.drain()
            except (BrokenPipeError, ConnectionError, OSError) as error:
                raise transport(f"codex: write app-server stdin: {error}") from error

    def register(self, session: CodexSession) -> None:
        self._sessions[session.session_id] = session

    def map_turn(self, turn_id: str, thread_id: str) -> None:
        if turn_id and thread_id:
            self._turn_threads[turn_id] = thread_id

    async def _read_loop(self) -> None:
        process = self._process
        if process is None or process.stdout is None:
            return
        try:
            while line := await process.stdout.readline():
                try:
                    message = _object(json.loads(line))
                except (json.JSONDecodeError, UnicodeDecodeError):
                    continue
                request_id = message.get("id")
                method = message.get("method")
                if request_id is not None and method is None:
                    if isinstance(request_id, int) and (future := self._pending.get(request_id)):
                        if not future.done():
                            future.set_result(message)
                    continue
                if isinstance(method, str):
                    self._route(method, request_id, _object(message.get("params")))
        finally:
            error = transport("codex app-server closed")
            for future in self._pending.values():
                if not future.done():
                    future.set_exception(error)
            for session in set(self._sessions.values()):
                session._transport_closed()

    def _route(self, method: str, request_id: Any, params: dict[str, Any]) -> None:
        thread_id = str(params.get("threadId") or "")
        turn = _object(params.get("turn"))
        turn_id = str(params.get("turnId") or turn.get("id") or "")
        if method == "turn/started" and turn_id:
            active = [key for key, value in self._sessions.items() if value._active_run]
            thread_id = thread_id or (active[0] if len(active) == 1 else "")
            if thread_id:
                self._turn_threads[turn_id] = thread_id
        thread_id = thread_id or self._turn_threads.get(turn_id, "")
        session = self._sessions.get(thread_id)
        if session is None and len(self._sessions) == 1:
            session = next(iter(self._sessions.values()))
        if session is not None:
            session._handle(method, request_id, params)

    async def _drain_stderr(self) -> None:
        process = self._process
        if process is None or process.stderr is None:
            return
        while await process.stderr.readline():
            pass

    async def close(self) -> None:
        process = self._process
        if process is not None and process.returncode is None:
            process.terminate()
            try:
                await asyncio.wait_for(process.wait(), 10)
            except TimeoutError:
                process.kill()
                await process.wait()
        tasks = [task for task in (self._reader, self._stderr) if task is not None]
        if tasks:
            await asyncio.gather(*tasks, return_exceptions=True)


class CodexSession(DriverSession):
    def __init__(self, process: _CodexProcess, spec: AgentSpec, cwd: Path, resume_id: str) -> None:
        super().__init__()
        self._process = process
        self._spec = spec
        self._cwd = cwd
        self._id = resume_id
        self._resume_id = resume_id
        self._phase = ProcessPhase.IDLE
        self._active_run = ""
        self._turn_id = ""
        self._approval_id: Any = None
        self._usage: TokenUsage | None = None

    @property
    def session_id(self) -> str:
        return self._id

    @property
    def phase(self) -> ProcessPhase:
        return self._phase

    async def start(self) -> None:
        if self._phase is ProcessPhase.CLOSED:
            raise invalid_state("codex session is closed")
        if self._id and self._phase is not ProcessPhase.IDLE:
            return
        self._phase = ProcessPhase.STARTING
        await self._process.start()
        if self._resume_id:
            params: dict[str, Any] = {"threadId": self._resume_id}
            if self._spec.system_prompt:
                params["developerInstructions"] = self._spec.system_prompt
            result = await self._process.request("thread/resume", params)
        else:
            params = {"cwd": str(self._cwd)}
            if self._spec.model:
                params["model"] = self._spec.model
            if self._spec.system_prompt:
                params["developerInstructions"] = self._spec.system_prompt
            result = await self._process.request("thread/start", params)
        thread = _object(result.get("thread"))
        self._id = str(thread.get("id") or self._resume_id)
        if not self._id:
            self._phase = ProcessPhase.FAILED
            raise protocol("codex: thread response did not include an id")
        self._process.register(self)
        self._phase = ProcessPhase.ACTIVE
        self.events.emit(DriverEvent(DriverEventType.SESSION_ATTACHED, session_id=self._id))

    async def prompt(self, text: str) -> str:
        if self._phase is not ProcessPhase.ACTIVE:
            raise invalid_state("codex session is not active")
        run_id = new_run_id()
        self._active_run = run_id
        self._phase = ProcessPhase.PROMPT_IN_FLIGHT
        try:
            result = await self._process.request(
                "turn/start",
                {"threadId": self._id, "input": [{"type": "text", "text": text}]},
            )
        except Exception:
            self._active_run = ""
            self._phase = ProcessPhase.ACTIVE
            raise
        self._turn_id = str(_object(result.get("turn")).get("id") or self._turn_id)
        self._process.map_turn(self._turn_id, self._id)
        return run_id

    async def continue_(self, value: str) -> str:
        if self._phase is not ProcessPhase.BLOCKED or self._approval_id is None:
            raise invalid_state("codex session is not blocked")
        decision = {"allow_once": "accept", "deny": "decline", "cancel": "cancel"}.get(value)
        if decision is None:
            raise invalid_state(f"codex: invalid approval option {value!r}")
        run_id = new_run_id()
        self._active_run = run_id
        approval_id = self._approval_id
        self._approval_id = None
        self._phase = ProcessPhase.PROMPT_IN_FLIGHT
        await self._process.respond(approval_id, decision)
        return run_id

    async def interrupt(self) -> None:
        if self._phase not in {ProcessPhase.PROMPT_IN_FLIGHT, ProcessPhase.BLOCKED}:
            return
        if self._turn_id:
            await self._process.request(
                "turn/interrupt", {"threadId": self._id, "turnId": self._turn_id}
            )

    async def raw(self) -> RawSessionData:
        path = await asyncio.to_thread(_find_session_file, self._id or self._resume_id)
        try:
            data = await asyncio.to_thread(path.read_bytes)
        except OSError as error:
            raise protocol(f"codex: read session data: {error}") from error
        return RawSessionData(SessionDataFormat.JSONL, data)

    async def token_statistics(self) -> TokenStatistics:
        raw = await self.raw()
        return await asyncio.to_thread(_statistics, raw.data)

    async def close(self) -> None:
        self._phase = ProcessPhase.CLOSED
        self.events.close()

    def _handle(self, method: str, request_id: Any, params: dict[str, Any]) -> None:
        turn = _object(params.get("turn"))
        if method == "turn/started":
            self._turn_id = str(turn.get("id") or self._turn_id)
        elif method == "item/agentMessage/delta":
            delta = params.get("delta")
            text = (
                str(_object(delta).get("value") or "")
                if isinstance(delta, dict)
                else str(delta or "")
            )
            self._emit(OutputItem(OutputKind.TEXT, text=text))
        elif method == "item/reasoning/summaryTextDelta":
            self._emit(OutputItem(OutputKind.THINKING, text=str(params.get("delta") or "")))
        elif method == "item/commandExecution/outputDelta":
            self._emit(
                OutputItem(
                    OutputKind.TOOL_RESULT,
                    text=str(params.get("output") or params.get("delta") or ""),
                )
            )
        elif method == "item/completed":
            item = _object(params.get("item"))
            if item.get("type") == "commandExecution":
                self._emit(
                    OutputItem(
                        OutputKind.TOOL_CALL,
                        tool="shell",
                        tool_input={"command": item.get("command"), "cwd": item.get("cwd")},
                    )
                )
            elif item.get("type") == "mcpToolCall":
                self._emit(
                    OutputItem(
                        OutputKind.TOOL_CALL,
                        tool=f"{item.get('server', '')}/{item.get('tool', '')}",
                        tool_input=item.get("arguments"),
                    )
                )
        elif method == "thread/tokenUsage/updated":
            usage = _object(params.get("tokenUsage"))
            bucket = _object(usage.get("last")) or _object(usage.get("total"))
            self._usage = TokenUsage(
                input_tokens=int(bucket.get("inputTokens") or 0),
                output_tokens=int(bucket.get("outputTokens") or 0),
                cache_read_tokens=int(bucket.get("cachedInputTokens") or 0),
            )
        elif method in {"item/commandExecution/requestApproval", "item/fileChange/requestApproval"}:
            self._approval_id = request_id
            self._phase = ProcessPhase.BLOCKED
            kind = (
                BlockedKind.TOOL_APPROVAL
                if "commandExecution" in method
                else BlockedKind.PERMISSION
            )
            self.events.emit(
                DriverEvent(
                    DriverEventType.BLOCKED,
                    session_id=self._id,
                    run_id=self._active_run,
                    blocked=BlockedReason(
                        kind,
                        "Command requires approval"
                        if kind is BlockedKind.TOOL_APPROVAL
                        else "File change requires approval",
                        (
                            BlockOption("allow_once", "Allow once"),
                            BlockOption("deny", "Deny"),
                            BlockOption("cancel", "Cancel"),
                        ),
                    ),
                )
            )
            self._active_run = ""
        elif method == "turn/completed":
            self._finish(turn)

    def _finish(self, turn: dict[str, Any]) -> None:
        run_id = self._active_run
        if not run_id:
            return
        self._emit(OutputItem(OutputKind.TURN_END))
        status = str(turn.get("status") or "completed")
        if status == "failed":
            message = str(_object(turn.get("error")).get("message") or "turn failed")
            self.events.emit(
                DriverEvent(
                    DriverEventType.FAILED, self._id, run_id, error=runtime_reported(message)
                )
            )
        else:
            finish = FinishReason.CANCELLED if status == "interrupted" else FinishReason.NATURAL
            usage = {self._spec.model or "codex": self._usage} if self._usage else {}
            self.events.emit(
                DriverEvent(
                    DriverEventType.COMPLETED, self._id, run_id, result=RunResult(finish, usage)
                )
            )
        self._active_run = ""
        self._turn_id = ""
        self._usage = None
        self._phase = ProcessPhase.ACTIVE

    def _emit(self, item: OutputItem) -> None:
        self.events.emit(DriverEvent(DriverEventType.OUTPUT, self._id, self._active_run, item=item))

    def _transport_closed(self) -> None:
        if self._active_run:
            self.events.emit(
                DriverEvent(
                    DriverEventType.COMPLETED,
                    self._id,
                    self._active_run,
                    result=RunResult(FinishReason.TRANSPORT_CLOSED),
                )
            )
            self._active_run = ""
        self._phase = ProcessPhase.CLOSED
