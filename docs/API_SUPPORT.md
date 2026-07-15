# unio SDK Feature Support Matrix

This document records runtime-specific support in Go v0.2 and Python v0.1. A ✅
means both SDKs implement the mapping; upstream CLI capabilities can still vary
by version and configuration. ACP session discovery and resume are explicitly
capability-negotiated and therefore marked ⚠️.

## Release compatibility evidence

The minimum Go version is 1.23. On 2026-07-15, CI passed on
`ubuntu-latest`, and the v0.2 release candidate passed the local race suite on
macOS arm64 with Go 1.23.2. Native Windows has not been verified; treat it as
unsupported until a release gate covers it.

The Python implementation supports Python 3.11–3.14. On 2026-07-15, its
deterministic protocol suite, strict type check, package build, metadata check,
and clean-wheel import passed locally on macOS arm64 with Python 3.12.13. No
Python real-runtime E2E has been run yet, so the runtime statuses below remain
experimental for Python even where the Go adapter has stronger evidence.

| Agent | Executable discovery | Evidence on 2026-07-15 | Status |
| --- | --- | --- | --- |
| Claude Code | `claude` | Real E2E exists but was not run in the release environment | Experimental compatibility |
| Codex | `codex` | CLI 0.144.2 detected on macOS arm64; real E2E pending explicit token approval | Release candidate |
| Kimi | `kimi-cli`, `kimi` | Not installed in the release environment | Experimental compatibility |
| TraeX | `traex`, `trae-cli`, `coco`, `traecli` | Not installed in the release environment | Experimental compatibility |
| OpenCode | `opencode` | Not installed in the release environment | Experimental compatibility |

"Experimental compatibility" means the adapter and deterministic protocol
tests are present, but this release has no claim of live compatibility with a
specific CLI version. Report the exact CLI version with compatibility issues.

## Support Overview

| Agent | Execution | Session Listing | Session Resume | Interruption | Blocking | Tool Results | Turn Usage | Raw Session Data | Session Token Statistics |
| --- | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: |
| Claude Code | ✅ | ✅ | ✅ | ✅ | ❌ | ❌ | ✅ Includes cache writes, cost, and duration | ✅ JSONL | ✅ |
| Codex | ✅ | ✅ | ✅ | ✅ | ⚠️ Approvals only | ⚠️ Command output only | ⚠️ No cache writes, cost, or duration | ✅ JSONL | ✅ |
| Kimi | ✅ | ⚠️ Runtime capability | ⚠️ Runtime capability | ✅ | ✅ | ✅ | ❌ | ✅ JSONL | ✅ |
| TraeX | ✅ | ⚠️ Runtime capability | ⚠️ Runtime capability | ✅ | ✅ | ✅ | ❌ | ✅ JSONL | ✅ |
| OpenCode | ✅ | ⚠️ Runtime capability | ⚠️ Runtime capability | ✅ | ✅ | ✅ | ❌ | ❌ | ❌ |

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

## Session Discovery

| Feature | Claude Code | Codex | Kimi | TraeX | OpenCode | Notes |
| --- | :---: | :---: | :---: | :---: | :---: | --- |
| `session.create` | ✅ | ✅ | ✅ | ✅ | ✅ |  |
| `session.list` | ✅ | ✅ | ⚠️ | ⚠️ | ⚠️ | ACP requires the runtime to advertise `session/list` |
| `session.list.workspace` | ✅ | ✅ | ⚠️ | ⚠️ | ⚠️ | Filters sessions by working directory when listing is advertised |
| `session.list.all` | ✅ | ✅ | ⚠️ | ⚠️ | ⚠️ | Removes the working-directory filter when listing is advertised |
| `session.retrieve` | ✅ | ✅ | ⚠️ | ⚠️ | ⚠️ | ACP requires `session/resume` or `session/load` |
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

## Related contracts

The frozen cross-language values live only in the [behavior
specification](SPEC.md). See [ERRORS.md](ERRORS.md) for error meanings, matching
examples, and caller recovery guidance.
