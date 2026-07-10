# Changelog

## Unreleased

- Replaced package-level `Run`/`Start` with long-lived `Agent` instances.
- Added `NewSession`, `ListSessions`, and `GetSession` with automatic resume.
- Added public `Idle`, `Running`, and `Blocked` session states.
- Added human-aligned `Interrupt` and blocked `Continue` behavior.
- Added persisted Claude Code and Codex session discovery.
- Added per-Agent Codex app-server process sharing.

## v0.1.0 - 2026-07-09

- Added Go facade API: `Run`, `Open`, `Session`, `Stream`, `Result`.
- Added `Start` as the primary session entrypoint; `Open` remains as an alias.
- Added Claude Code driver.
- Added Codex app-server driver.
- Added typed SDK errors in `errs`.
- Added fake driver and E2E test harness.
- Added MIT license.
