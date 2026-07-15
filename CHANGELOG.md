# Changelog

## Unreleased

## v0.2.0 - 2026-07-15

- Added an error-handling guide covering typed categories, retry decisions,
  context cancellation, blocked turns, and interruption.
- Added a contribution guide covering development gates, runtime testing,
  contract changes, and pull request expectations.
- Added a repository-local `ship` skill for reviewed PR delivery and optional tagged Go module releases.
- Bound Agent and Session lifecycles to the context passed to `New`.
- Simplified driver session creation by removing SDK-generated session keys and
  per-open Agent configuration.
- Added `Session.Raw` for persisted Claude Code, Codex, Kimi, and TraeX session data.
- Added `Session.TokenStatistics` for cumulative Claude Code, Codex, Kimi, and TraeX session usage.
- Distinguished per-turn `Result.Usage` from persisted session statistics.
- You can now use `MaxSessions` to cap results returned by `Agent.ListSessions`.
- Replaced package-level `Run`/`Start` with long-lived `Agent` instances.
- Added `NewSession`, `ListSessions`, and `GetSession` with automatic resume.
- Added public `Idle`, `Running`, and `Blocked` session states.
- Added human-aligned `Interrupt` and blocked `Continue` behavior.
- Added persisted Claude Code and Codex session discovery.
- Added cwd-scoped session listing with explicit cross-workspace listing.
- Added a shared ACP v1 driver for Kimi, TraeX, and OpenCode.
- Added per-Agent Codex app-server process sharing.

## v0.1.0 - 2026-07-09

- Added Go facade API: `Run`, `Open`, `Session`, `Stream`, `Result`.
- Added `Start` as the primary session entrypoint; `Open` remains as an alias.
- Added Claude Code driver.
- Added Codex app-server driver.
- Added typed SDK errors in `errs`.
- Added fake driver and E2E test harness.
- Added MIT license.
