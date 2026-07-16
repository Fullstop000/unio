from __future__ import annotations

import asyncio
import json
from dataclasses import dataclass
from datetime import datetime
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
from ..errors import AgentError, invalid_state, protocol, session_not_found, transport, unsupported
from ..models import (
    AgentKind,
    BlockedKind,
    BlockedReason,
    BlockOption,
    OptionSelection,
    RawSessionData,
    SessionDataFormat,
    TokenStatistics,
    UserInput,
    UserMessage,
)

_ACP_STREAM_LIMIT = 8 * 1024 * 1024


def _object(value: Any) -> dict[str, Any]:
    return cast(dict[str, Any], value) if isinstance(value, dict) else {}


def _resolved_path(value: str | Path) -> Path:
    return Path(value).resolve()


@dataclass(frozen=True, slots=True)
class _Config:
    name: str
    command: str
    alternatives: tuple[str, ...]

    def args(self, spec: AgentSpec) -> list[str]:
        if self.name == "kimi":
            result = ["--work-dir", str(spec.cwd)]
            if spec.model:
                result.extend(("--model", spec.model))
            return [*result, *spec.extra_args, "acp"]
        if self.name == "traex":
            result: list[str] = []
            if spec.model:
                result.extend(("--model", spec.model))
            return [*result, *spec.extra_args, "acp", "serve"]
        return ["acp", "--cwd", str(spec.cwd), *spec.extra_args]


def _config(kind: AgentKind) -> _Config:
    home = Path.home()
    if kind is AgentKind.KIMI:
        return _Config(
            "kimi",
            "kimi-cli",
            ("kimi", str(home / ".local/bin/kimi-cli"), str(home / ".local/bin/kimi")),
        )
    if kind is AgentKind.TRAEX:
        return _Config(
            "traex",
            "traex",
            (
                "trae-cli",
                "coco",
                "traecli",
                str(home / ".local/bin/traex"),
                str(home / ".local/bin/trae-cli"),
                str(home / ".local/bin/coco"),
                str(home / ".local/bin/traecli"),
            ),
        )
    return _Config("opencode", "opencode", (str(home / ".opencode/bin/opencode"),))


class ACPDriver(Driver):
    def __init__(self, kind: AgentKind, spec: AgentSpec) -> None:
        self._spec = spec
        self._config = _config(kind)
        self._executable = ""
        self._process: _ACPProcess | None = None
        self._sessions: set[ACPSession] = set()

    def probe(self) -> None:
        self._executable = resolve_executable(self._config.command, *self._config.alternatives)

    async def list_sessions(self, cwd: str | None) -> list[StoredSession]:
        process = await self._get_process()
        return await process.list_sessions(cwd)

    async def open_session(self, resume_id: str = "", cwd: str = "") -> DriverSession:
        process = await self._get_process()
        spec = AgentSpec(
            _resolved_path(cwd or self._spec.cwd),
            self._spec.model,
            self._spec.system_prompt,
            self._spec.extra_args,
            self._spec.env,
        )
        session = ACPSession(process, self._config, spec, resume_id)
        self._sessions.add(session)
        return session

    async def _get_process(self) -> _ACPProcess:
        if self._process is None:
            self._process = _ACPProcess(self._executable, self._config, self._spec)
        await self._process.start()
        return self._process

    async def close(self) -> None:
        await asyncio.gather(*(session.close() for session in self._sessions))
        if self._process is not None:
            await self._process.close()


class _ACPProcess:
    def __init__(self, executable: str, config: _Config, spec: AgentSpec) -> None:
        self._executable = executable
        self._config = config
        self._spec = spec
        self._process: asyncio.subprocess.Process | None = None
        self._reader: asyncio.Task[None] | None = None
        self._stderr: asyncio.Task[None] | None = None
        self._start_lock = asyncio.Lock()
        self._write_lock = asyncio.Lock()
        self._next_id = 0
        self._pending: dict[int, asyncio.Future[dict[str, Any]]] = {}
        self._sessions: dict[str, ACPSession] = {}
        self.capabilities: dict[str, bool] = {}

    async def start(self) -> None:
        async with self._start_lock:
            if self._process is not None:
                return
            try:
                self._process = await asyncio.create_subprocess_exec(
                    self._executable,
                    *self._config.args(self._spec),
                    cwd=self._spec.cwd,
                    env=self._spec.child_env(),
                    stdin=asyncio.subprocess.PIPE,
                    stdout=asyncio.subprocess.PIPE,
                    stderr=asyncio.subprocess.PIPE,
                    limit=_ACP_STREAM_LIMIT,
                )
            except OSError as error:
                raise transport(f"acp: start {self._config.name}: {error}") from error
            self._reader = asyncio.create_task(self._read_loop())
            self._stderr = asyncio.create_task(self._drain_stderr())
            result = await self.call(
                "initialize",
                {
                    "protocolVersion": 1,
                    "clientCapabilities": {},
                    "clientInfo": {
                        "name": "unio",
                        "title": "unio",
                        "version": "0.1.0",
                    },
                },
            )
            if int(result.get("protocolVersion") or 0) != 1:
                raise unsupported("acp: runtime selected unsupported protocol version")
            agent = _object(result.get("agentCapabilities"))
            session = _object(agent.get("sessionCapabilities"))
            self.capabilities = {
                "load": bool(agent.get("loadSession")),
                "list": session.get("list") is not None,
                "resume": session.get("resume") is not None,
                "close": session.get("close") is not None,
            }

    async def call(self, method: str, params: dict[str, Any]) -> dict[str, Any]:
        _, future = await self.request(method, params)
        response = await future
        error = _object(response.get("error"))
        if error:
            message = error.get("message") or f"error {error.get('code', '')}"
            raise protocol(f"acp {method}: {message}")
        result = response.get("result")
        return _object(result)

    async def request(
        self, method: str, params: dict[str, Any]
    ) -> tuple[int, asyncio.Future[dict[str, Any]]]:
        self._next_id += 1
        request_id = self._next_id
        future = asyncio.get_running_loop().create_future()
        self._pending[request_id] = future
        try:
            await self.write(
                {
                    "jsonrpc": "2.0",
                    "id": request_id,
                    "method": method,
                    "params": params,
                }
            )
        except Exception:
            self._pending.pop(request_id, None)
            raise
        return request_id, future

    async def notify(self, method: str, params: dict[str, Any]) -> None:
        await self.write({"jsonrpc": "2.0", "method": method, "params": params})

    async def respond(self, request_id: Any, result: Any) -> None:
        await self.write({"jsonrpc": "2.0", "id": request_id, "result": result})

    async def write(self, message: dict[str, Any]) -> None:
        process = self._process
        if process is None or process.stdin is None:
            raise transport("acp: runtime is not running")
        payload = json.dumps(message, separators=(",", ":")).encode() + b"\n"
        async with self._write_lock:
            try:
                process.stdin.write(payload)
                await process.stdin.drain()
            except (BrokenPipeError, ConnectionError, OSError) as error:
                raise transport(f"acp: write stdin: {error}") from error

    def register(self, session: ACPSession) -> None:
        self._sessions[session.session_id] = session

    async def list_sessions(self, cwd: str | None) -> list[StoredSession]:
        if not self.capabilities.get("list"):
            raise unsupported("acp: runtime does not support session/list")
        output: list[StoredSession] = []
        cursor = ""
        seen: set[str] = set()
        while True:
            params: dict[str, Any] = {}
            if cwd:
                params["cwd"] = cwd
            if cursor:
                params["cursor"] = cursor
            result = await self.call("session/list", params)
            sessions = result.get("sessions")
            raw_sessions = cast(list[Any], sessions) if isinstance(sessions, list) else []
            for raw in raw_sessions:
                item = _object(raw)
                updated: datetime | None = None
                try:
                    updated = datetime.fromisoformat(
                        str(item.get("updatedAt") or "").replace("Z", "+00:00")
                    )
                except ValueError:
                    pass
                meta = _object(item.get("_meta"))
                output.append(
                    StoredSession(
                        str(item.get("sessionId") or ""),
                        str(item.get("title") or ""),
                        str(item.get("cwd") or ""),
                        updated_at=updated,
                        message_count=int(meta.get("messageCount") or 0),
                    )
                )
            cursor = str(result.get("nextCursor") or "")
            if not cursor:
                return output
            if cursor in seen:
                raise protocol("acp: session/list repeated nextCursor")
            seen.add(cursor)

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
                    if isinstance(request_id, int) and (
                        future := self._pending.pop(request_id, None)
                    ):
                        if not future.done():
                            future.set_result(message)
                elif method == "session/update":
                    params = _object(message.get("params"))
                    session = self._sessions.get(str(params.get("sessionId") or ""))
                    if session:
                        session._update(_object(params.get("update")))
                elif method == "session/request_permission" and request_id is not None:
                    params = _object(message.get("params"))
                    session = self._sessions.get(str(params.get("sessionId") or ""))
                    if session:
                        session._permission(request_id, params)
                    else:
                        await self.respond(request_id, {"outcome": {"outcome": "cancelled"}})
        finally:
            error = transport(f"acp: {self._config.name} runtime closed")
            for future in self._pending.values():
                if not future.done():
                    future.set_exception(error)
            for session in set(self._sessions.values()):
                session._transport_closed()

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
        tasks = [task for task in (self._reader, self._stderr) if task]
        if tasks:
            await asyncio.gather(*tasks, return_exceptions=True)


@dataclass(slots=True)
class _Tool:
    id: str
    name: str
    input: Any


class ACPSession(DriverSession):
    def __init__(
        self, process: _ACPProcess, config: _Config, spec: AgentSpec, resume_id: str
    ) -> None:
        super().__init__()
        self._process = process
        self._config = config
        self._spec = spec
        self._resume_id = resume_id
        self._id = resume_id
        self._phase = ProcessPhase.IDLE
        self._active_run = ""
        self._permission_id: Any = None
        self._permission_options: set[str] = set()
        self._prompt_task: asyncio.Task[None] | None = None
        self._first_turn = True
        self._interrupted = False
        self._tools: list[_Tool] = []

    @property
    def session_id(self) -> str:
        return self._id

    @property
    def phase(self) -> ProcessPhase:
        return self._phase

    async def start(self) -> None:
        if self._phase is ProcessPhase.CLOSED:
            raise invalid_state("acp session is closed")
        if self._phase is not ProcessPhase.IDLE:
            return
        self._phase = ProcessPhase.STARTING
        params: dict[str, Any] = {"cwd": str(self._spec.cwd), "mcpServers": []}
        method = "session/new"
        if self._resume_id:
            params["sessionId"] = self._resume_id
            if self._process.capabilities.get("resume"):
                method = "session/resume"
            elif self._process.capabilities.get("load"):
                method = "session/load"
            else:
                raise unsupported("acp: runtime cannot resume sessions")
        try:
            result = await self._process.call(method, params)
        except Exception as error:
            if self._resume_id and "not found" in str(error).lower():
                raise session_not_found(self._resume_id) from error
            raise
        self._id = str(result.get("sessionId") or self._resume_id)
        if not self._id:
            raise protocol(f"acp: {method} returned no sessionId")
        if self._spec.model and self._config.name == "opencode":
            await self._process.call(
                "session/set_config_option",
                {"sessionId": self._id, "configId": "model", "value": self._spec.model},
            )
        self._process.register(self)
        self._phase = ProcessPhase.ACTIVE
        self.events.emit(DriverEvent(DriverEventType.SESSION_ATTACHED, session_id=self._id))

    async def send(self, value: UserInput) -> str:
        if self._phase is not ProcessPhase.ACTIVE:
            raise invalid_state("acp session is not active")
        if not isinstance(value, UserMessage):
            raise invalid_state("acp: a new turn requires UserMessage")
        text = value.text
        if self._first_turn and self._spec.system_prompt:
            text = f"{self._spec.system_prompt}\n\n{text}"
        self._first_turn = False
        run_id = new_run_id()
        self._active_run = run_id
        self._phase = ProcessPhase.PROMPT_IN_FLIGHT
        _, future = await self._process.request(
            "session/prompt",
            {"sessionId": self._id, "prompt": [{"type": "text", "text": text}]},
        )
        self._prompt_task = asyncio.create_task(self._finish_prompt(future))
        return run_id

    async def respond(self, value: UserInput) -> str:
        if self._phase is not ProcessPhase.BLOCKED or self._permission_id is None:
            raise invalid_state("acp session is not blocked")
        if not isinstance(value, OptionSelection):
            raise invalid_state("acp: blocked permission requires OptionSelection")
        if self._permission_options and value.value not in self._permission_options:
            raise invalid_state(f"acp: invalid permission option {value.value!r}")
        run_id = new_run_id()
        self._active_run = run_id
        request_id = self._permission_id
        self._permission_id = None
        self._phase = ProcessPhase.PROMPT_IN_FLIGHT
        await self._process.respond(
            request_id,
            {"outcome": {"outcome": "selected", "optionId": value.value}},
        )
        return run_id

    async def interrupt(self) -> None:
        if self._phase not in {ProcessPhase.PROMPT_IN_FLIGHT, ProcessPhase.BLOCKED}:
            return
        self._interrupted = True
        if self._permission_id is not None:
            await self._process.respond(self._permission_id, {"outcome": {"outcome": "cancelled"}})
            self._permission_id = None
        await self._process.notify("session/cancel", {"sessionId": self._id})
        if self._prompt_task is not None:
            await self._prompt_task

    async def raw(self) -> RawSessionData:
        path = await asyncio.to_thread(self._find_raw)
        try:
            data = await asyncio.to_thread(path.read_bytes)
        except OSError as error:
            raise protocol(f"acp: read session data: {error}") from error
        return RawSessionData(SessionDataFormat.JSONL, data)

    async def token_statistics(self) -> TokenStatistics:
        raw = await self.raw()
        return await asyncio.to_thread(self._statistics, raw.data)

    async def close(self) -> None:
        if self._phase is ProcessPhase.CLOSED:
            return
        await self.interrupt()
        if self._id and self._process.capabilities.get("close"):
            await self._process.call("session/close", {"sessionId": self._id})
        self._phase = ProcessPhase.CLOSED
        self.events.close()

    async def _finish_prompt(self, future: asyncio.Future[dict[str, Any]]) -> None:
        try:
            response = await future
            error = _object(response.get("error"))
            if error and not self._interrupted:
                raise protocol(f"acp session/prompt: {error.get('message') or 'failed'}")
            result = _object(response.get("result"))
            self._drain_tools()
            self._emit(OutputItem(OutputKind.TURN_END))
            cancelled = self._interrupted or result.get("stopReason") == "cancelled"
            self.events.emit(
                DriverEvent(
                    DriverEventType.COMPLETED,
                    self._id,
                    self._active_run,
                    result=RunResult(FinishReason.CANCELLED if cancelled else FinishReason.NATURAL),
                )
            )
        except Exception as error:
            self.events.emit(
                DriverEvent(
                    DriverEventType.FAILED,
                    self._id,
                    self._active_run,
                    error=error if isinstance(error, AgentError) else protocol(str(error)),
                )
            )
        finally:
            self._active_run = ""
            self._interrupted = False
            if self._phase is not ProcessPhase.CLOSED:
                self._phase = ProcessPhase.ACTIVE

    def _permission(self, request_id: Any, params: dict[str, Any]) -> None:
        tool_call = _object(params.get("toolCall"))
        options: list[BlockOption] = []
        valid: set[str] = set()
        raw_options = params.get("options")
        options_list = cast(list[Any], raw_options) if isinstance(raw_options, list) else []
        for raw in options_list:
            item = _object(raw)
            value = str(item.get("optionId") or "")
            if value:
                valid.add(value)
                options.append(
                    BlockOption(value, str(item.get("name") or item.get("kind") or value))
                )
        self._permission_id = request_id
        self._permission_options = valid
        self._phase = ProcessPhase.BLOCKED
        self.events.emit(
            DriverEvent(
                DriverEventType.BLOCKED,
                self._id,
                self._active_run,
                blocked=BlockedReason(
                    BlockedKind.TOOL_APPROVAL,
                    str(tool_call.get("title") or "Tool requires approval"),
                    tuple(options),
                ),
            )
        )
        self._active_run = ""

    def _update(self, update: dict[str, Any]) -> None:
        kind = str(update.get("sessionUpdate") or update.get("kind") or update.get("type") or "")
        if kind in {"agent_message_chunk", "agentMessageChunk"}:
            text = self._text(update)
            if text:
                self._emit(OutputItem(OutputKind.TEXT, text=text))
        elif kind in {"agent_thought_chunk", "agentThoughtChunk"}:
            text = self._text(update)
            if text:
                self._emit(OutputItem(OutputKind.THINKING, text=text))
        elif kind in {"tool_call", "toolCall"}:
            self._tools.append(
                _Tool(
                    str(update.get("toolCallId") or ""),
                    str(update.get("toolName") or update.get("title") or ""),
                    update.get("rawInput", update.get("args", update.get("input"))),
                )
            )
        elif kind in {"tool_call_update", "toolCallUpdate"}:
            tool_id = str(update.get("toolCallId") or "")
            matches = [tool for tool in self._tools if tool.id == tool_id or not tool_id]
            if update.get("status") in {"completed", "failed"}:
                for tool in matches:
                    self._emit(
                        OutputItem(OutputKind.TOOL_CALL, tool=tool.name, tool_input=tool.input)
                    )
                    self._tools.remove(tool)
            text = self._tool_text(update.get("content"))
            if text:
                self._emit(OutputItem(OutputKind.TOOL_RESULT, text=text))

    def _drain_tools(self) -> None:
        for tool in self._tools:
            self._emit(OutputItem(OutputKind.TOOL_CALL, tool=tool.name, tool_input=tool.input))
        self._tools.clear()

    def _emit(self, item: OutputItem) -> None:
        self.events.emit(DriverEvent(DriverEventType.OUTPUT, self._id, self._active_run, item=item))

    @staticmethod
    def _text(update: dict[str, Any]) -> str:
        if value := update.get("chunk") or update.get("text"):
            return str(value)
        return str(_object(update.get("content")).get("text") or "")

    @staticmethod
    def _tool_text(value: Any) -> str:
        if isinstance(value, str):
            return value
        if not isinstance(value, list):
            return ""
        items = cast(list[Any], value)
        return "\n".join(
            str(
                _object(item).get("text") or _object(_object(item).get("content")).get("text") or ""
            )
            for item in items
        )

    def _find_raw(self) -> Path:
        session_id = self._id or self._resume_id
        home = Path.home()
        if self._config.name == "kimi":
            index = home / ".kimi-code/session_index.jsonl"
            if index.exists():
                for line in index.read_text(encoding="utf-8").splitlines():
                    try:
                        item = _object(json.loads(line))
                    except json.JSONDecodeError:
                        continue
                    if item.get("sessionId") == session_id:
                        path = Path(str(item.get("sessionDir") or "")) / "agents/main/wire.jsonl"
                        if path.is_file():
                            return path
            names = {session_id, session_id.removeprefix("ses_")}
            for root in (home / ".kimi-code/sessions", home / ".kimi/sessions"):
                if root.exists():
                    for path in root.rglob("wire.jsonl"):
                        if any(name in path.parts for name in names):
                            return path
        elif self._config.name == "traex":
            for root in (home / ".trae/cli/sessions", home / ".trae/sessions"):
                if root.exists():
                    for path in root.rglob(f"*-{session_id}.jsonl"):
                        return path
        else:
            raise unsupported("acp: raw session data are not supported by opencode")
        raise session_not_found(session_id)

    def _statistics(self, data: bytes) -> TokenStatistics:
        if self._config.name == "opencode":
            raise unsupported("acp: session token statistics are not supported by opencode")
        if self._config.name == "kimi":
            return self._kimi_statistics(data)
        return self._traex_statistics(data)

    @staticmethod
    def _add_kimi_usage(total: TokenStatistics, usage: dict[str, Any]) -> TokenStatistics:
        cache_read = int(usage.get("inputCacheRead") or usage.get("input_cache_read") or 0)
        cache_write = int(usage.get("inputCacheCreation") or usage.get("input_cache_creation") or 0)
        other = int(usage.get("inputOther") or usage.get("input_other") or 0)
        return TokenStatistics(
            total.input_tokens + other + cache_read + cache_write,
            total.output_tokens + int(usage.get("output") or 0),
            total.cache_read_tokens + cache_read,
            total.cache_write_tokens + cache_write,
        )

    def _kimi_statistics(self, data: bytes) -> TokenStatistics:
        total = TokenStatistics()
        open_steps: dict[str, set[str]] = {}
        for line in data.splitlines():
            if not line.strip():
                continue
            try:
                record = _object(json.loads(line))
            except (json.JSONDecodeError, UnicodeDecodeError) as error:
                raise protocol("acp: parse Kimi session data: invalid JSONL record") from error
            event = _object(record.get("event"))
            event_type = event.get("type")
            step_id = str(event.get("uuid") or "")
            turn_id = json.dumps(event.get("turnId"), separators=(",", ":"))
            if (
                record.get("type") == "context.append_loop_event"
                and event_type == "step.begin"
                and step_id
            ):
                open_steps.setdefault(turn_id, set()).add(step_id)
            elif (
                record.get("type") == "context.append_loop_event"
                and event_type == "step.end"
                and step_id
            ):
                for key, steps in list(open_steps.items()):
                    steps.discard(step_id)
                    if not steps:
                        open_steps.pop(key, None)
            elif record.get("type") == "turn.cancel":
                cancelled = json.dumps(record.get("turnId"), separators=(",", ":"))
                open_steps.pop(cancelled, None)
            if record.get("type") == "usage.record" and record.get("usageScope") == "turn":
                total = self._add_kimi_usage(total, _object(record.get("usage")))
            message = _object(record.get("message"))
            if message.get("type") == "StatusUpdate":
                payload = _object(message.get("payload"))
                total = self._add_kimi_usage(total, _object(payload.get("token_usage")))
        if open_steps:
            raise protocol("acp: latest Kimi step is not fully persisted yet")
        return total

    @staticmethod
    def _traex_statistics(data: bytes) -> TokenStatistics:
        total = TokenStatistics()
        pending = 0
        for line in data.splitlines():
            if not line.strip():
                continue
            try:
                record = _object(json.loads(line))
            except (json.JSONDecodeError, UnicodeDecodeError) as error:
                raise protocol("acp: parse TraeX session data: invalid JSONL record") from error
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
                usage = _object(info.get("last_token_usage"))
                total = TokenStatistics(
                    total.input_tokens + int(usage.get("input_tokens") or 0),
                    total.output_tokens + int(usage.get("output_tokens") or 0),
                    total.cache_read_tokens + int(usage.get("cached_input_tokens") or 0),
                    total.cache_write_tokens + int(usage.get("cache_creation_input_tokens") or 0),
                )
        if pending:
            raise protocol("acp: latest task is not fully persisted yet")
        return total

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
