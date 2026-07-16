# Changelog

## Unreleased

- Added the initial `unio-py` async SDK for Claude Code, Codex, Kimi, TraeX, and OpenCode.
- Added authenticated real-runtime E2E coverage for Codex, TraeX, and OpenCode.
- Fixed OpenCode ACP sessions whose model catalog exceeds asyncio's default stream limit.
- Replaced `continue_` and raw string submissions with state-aware `run`/`stream`
  accepting `UserMessage` or `OptionSelection`.
