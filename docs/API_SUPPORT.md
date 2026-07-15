# unio SDK Feature Support Matrix

## Support Overview

| Agent | Execution | Session Listing | Session Resume | Interruption | Blocking | Tool Results | Turn Usage | Raw Session Data | Session Token Statistics |
| --- | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: |
| Claude Code | ✅ | ✅ | ✅ | ✅ | ❌ | ❌ | ✅ Includes cache writes, cost, and duration | ✅ JSONL | ✅ |
| Codex | ✅ | ✅ | ✅ | ✅ | ⚠️ Approvals only | ⚠️ Command output only | ⚠️ No cache writes, cost, or duration | ✅ JSONL | ✅ |
| Kimi | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ❌ | ✅ JSONL | ✅ |
| TraeX | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ❌ | ✅ JSONL | ✅ |
| OpenCode | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ❌ | ❌ | ❌ |

| Marker | Meaning |
| --- | --- |
| ✅ | Supported |
| ⚠️ | Partially supported; see notes |
| ❌ | Unsupported |

## Configuration

| Feature | Claude Code | Codex | Kimi | TraeX | OpenCode | Notes |
| --- | :---: | :---: | :---: | :---: | :---: | --- |
| `agent.initialize` | ✅ | ✅ | ✅ | ✅ | ✅ | Checks CLI availability only; authentication errors may surface on the first turn |
| `agent.lifecycle.cancellation` | ✅ | ✅ | ✅ | ✅ | ✅ | Cancels the Agent and every Session derived from it |
| `agent.configure.working_directory` | ✅ | ✅ | ✅ | ✅ | ✅ |  |
| `agent.configure.model` | ✅ | ✅ | ✅ | ✅ | ✅ | OpenCode selects the model through ACP session configuration |
| `agent.configure.system_prompt` | ✅ | ✅ | ✅ | ✅ | ✅ | ACP agents prepend the system prompt to the first user prompt |
| `agent.configure.runtime_arguments` | ✅ | ❌ | ✅ | ✅ | ✅ | Codex app-server arguments are fixed |
| `agent.configure.environment` | ✅ | ✅ | ✅ | ✅ | ✅ |  |

## Agent Lifecycle

| Feature | Claude Code | Codex | Kimi | TraeX | OpenCode | Notes |
| --- | :---: | :---: | :---: | :---: | :---: | --- |
| `session.create` | ✅ | ✅ | ✅ | ✅ | ✅ |  |
| `session.list` | ✅ | ✅ | ✅ | ✅ | ✅ |  |
| `session.list.workspace` | ✅ | ✅ | ✅ | ✅ | ✅ | Filters sessions by working directory |
| `session.list.all` | ✅ | ✅ | ✅ | ✅ | ✅ | Removes the working-directory filter |
| `session.retrieve` | ✅ | ✅ | ✅ | ✅ | ✅ |  |
| `agent.close` | ✅ | ✅ | ✅ | ✅ | ✅ |  |

## Session Lifecycle

| Feature | Claude Code | Codex | Kimi | TraeX | OpenCode | Notes |
| --- | :---: | :---: | :---: | :---: | :---: | --- |
| `session.identity` | ✅ | ✅ | ✅ | ✅ | ✅ | Empty until the first turn starts for a new session |
| `session.state` | ✅ | ✅ | ✅ | ✅ | ✅ | Claude does not enter the blocked state |
| `turn.run` | ✅ | ✅ | ✅ | ✅ | ✅ |  |
| `turn.stream` | ✅ | ✅ | ✅ | ✅ | ✅ |  |
| `turn.interrupt` | ✅ | ✅ | ✅ | ✅ | ✅ | Claude terminates its process and resumes automatically on the next turn |
| `turn.continue` | ❌ | ⚠️ | ✅ | ✅ | ✅ | Codex supports command and file approvals only; ACP uses runtime-provided option IDs |

## Stream Consumption

| Feature | Claude Code | Codex | Kimi | TraeX | OpenCode |
| --- | :---: | :---: | :---: | :---: | :---: |
| `stream.advance` | ✅ | ✅ | ✅ | ✅ | ✅ |
| `stream.current_event` | ✅ | ✅ | ✅ | ✅ | ✅ |
| `stream.collect_result` | ✅ | ✅ | ✅ | ✅ | ✅ |

## Event Types

| Feature | Claude Code | Codex | Kimi | TraeX | OpenCode | Notes |
| --- | :---: | :---: | :---: | :---: | :---: | --- |
| `event.thinking` | ✅ | ✅ | ✅ | ✅ | ✅ | Emission depends on the selected model and runtime configuration |
| `event.text` | ✅ | ✅ | ✅ | ✅ | ✅ |  |
| `event.tool_call` | ✅ | ✅ | ✅ | ✅ | ✅ | Codex maps commands to `shell` and MCP tools to `server/tool` |
| `event.tool_result` | ❌ | ⚠️ | ✅ | ✅ | ✅ | Codex maps command output only, excluding MCP results |

## Turn Result

| Feature | Claude Code | Codex | Kimi | TraeX | OpenCode | Notes |
| --- | :---: | :---: | :---: | :---: | :---: | --- |
| `result.text` | ✅ | ✅ | ✅ | ✅ | ✅ |  |
| `result.thinking` | ✅ | ✅ | ✅ | ✅ | ✅ |  |
| `result.tool_calls` | ✅ | ✅ | ✅ | ✅ | ✅ |  |
| `result.session_identity` | ✅ | ✅ | ✅ | ✅ | ✅ |  |
| `turn.usage` | ✅ | ⚠️ | ❌ | ❌ | ❌ | Usage reported for the completed turn; independent of session token statistics |
| `result.duration` | ✅ | ❌ | ❌ | ❌ | ❌ | Zero when the runtime does not report duration |
| `result.interrupted` | ✅ | ✅ | ✅ | ✅ | ✅ |  |
| `result.blocked` | ❌ | ⚠️ | ✅ | ✅ | ✅ | Codex supports tool and file approvals; ACP supports tool approval |

## Turn Token Usage

| Feature | Claude Code | Codex | Kimi | TraeX | OpenCode |
| --- | :---: | :---: | :---: | :---: | :---: |
| `turn.usage.input_tokens` | ✅ | ✅ | ❌ | ❌ | ❌ |
| `turn.usage.output_tokens` | ✅ | ✅ | ❌ | ❌ | ❌ |
| `turn.usage.cache_read_tokens` | ✅ | ✅ | ❌ | ❌ | ❌ |
| `turn.usage.cache_write_tokens` | ✅ | ❌ | ❌ | ❌ | ❌ |
| `turn.usage.cost` | ✅ | ❌ | ❌ | ❌ | ❌ |

## Raw Session Data

| Feature | Claude Code | Codex | Kimi | TraeX | OpenCode | Notes |
| --- | :---: | :---: | :---: | :---: | :---: | --- |
| `session.raw_data` | ✅ | ✅ | ✅ | ✅ | ❌ | Returns the complete runtime-owned persisted session data |
| `session.raw_data.jsonl` | ✅ | ✅ | ✅ | ✅ | ❌ | Exposed as `SessionDataJSONL` |

## Session Token Statistics

| Feature | Claude Code | Codex | Kimi | TraeX | OpenCode | Notes |
| --- | :---: | :---: | :---: | :---: | :---: | --- |
| `session.token_statistics` | ✅ | ✅ | ✅ | ✅ | ❌ | Parses the complete session from `session.raw_data` |
| `session.token_statistics.input_tokens` | ✅ | ✅ | ✅ | ✅ | ❌ | Includes cached input tokens |
| `session.token_statistics.output_tokens` | ✅ | ✅ | ✅ | ✅ | ❌ |  |
| `session.token_statistics.cache_read_tokens` | ✅ | ✅ | ✅ | ✅ | ❌ |  |
| `session.token_statistics.cache_write_tokens` | ✅ | ❌ | ✅ | ✅ | ❌ | Codex does not persist this field |
| `session.token_statistics.cost` | ❌ | ❌ | ❌ | ❌ | ❌ | Session JSONL does not persist cost |

## Session Metadata

| Feature | Claude Code | Codex | Kimi | TraeX | OpenCode | Notes |
| --- | :---: | :---: | :---: | :---: | :---: | --- |
| `session.metadata.identity` | ✅ | ✅ | ✅ | ✅ | ✅ |  |
| `session.metadata.title` | ✅ | ✅ | ✅ | ✅ | ✅ |  |
| `session.metadata.working_directory` | ✅ | ✅ | ✅ | ✅ | ✅ |  |
| `session.metadata.started_at` | ⚠️ | ✅ | ❌ | ❌ | ❌ | Claude uses the history file modification time; ACP does not map this field |
| `session.metadata.updated_at` | ✅ | ✅ | ✅ | ✅ | ✅ |  |
| `session.metadata.message_count` | ✅ | ✅ | ✅ | ✅ | ✅ | ACP reads runtime `_meta.messageCount` |

## Blocking Reasons

| Feature | Claude Code | Codex | Kimi | TraeX | OpenCode |
| --- | :---: | :---: | :---: | :---: | :---: |
| `blocking.user_input` | ❌ | ❌ | ❌ | ❌ | ❌ |
| `blocking.tool_approval` | ❌ | ✅ | ✅ | ✅ | ✅ |
| `blocking.permission` | ❌ | ✅ | ❌ | ❌ | ❌ |
| `blocking.authentication` | ❌ | ❌ | ❌ | ❌ | ❌ |
| `blocking.external` | ❌ | ❌ | ❌ | ❌ | ❌ |

## Cross-Language Contracts

- Agent kinds: `Claude`, `Codex`, `Kimi`, `TraeX`, `OpenCode`
- Session states: `idle`, `running`, `blocked`
- Event kinds: `thinking`, `text`, `tool_call`, `tool_result`
- Blocking reasons: `user_input`, `tool_approval`, `permission`, `authentication`, `external`
- Error kinds: `transport`, `protocol`, `timeout`, `runtime_reported`, `unsupported`, `not_installed`, `invalid_state`, `session_not_found`
- Session data formats: `jsonl`

See [ERRORS.md](ERRORS.md) for error meanings, matching examples, and caller
recovery guidance.
