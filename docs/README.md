# unio documentation

Start with the repository [README](../README.md) for installation and a first
successful turn. The remaining documents each have one source-of-truth role:

- [API support matrix](API_SUPPORT.md): current runtime-specific capabilities,
  limitations, and compatibility evidence.
- [Error handling](ERRORS.md): typed error categories, matching, retry guidance,
  context cancellation, blocked turns, and interruption.
- [Behavior specification](SPEC.md): normative, language-neutral API behavior
  and frozen contract values.
- [Go package reference](https://pkg.go.dev/github.com/Fullstop000/unio):
  exported identifiers, fields, and package examples.
- [Contribution guide](../CONTRIBUTING.md): development gates, testing, contract
  changes, bug reports, and pull requests.
- [Security policy](../SECURITY.md): supported versions and private reporting.
- [Changelog](../CHANGELOG.md): released user-visible changes.

## Stability boundary

Version v0.2 is the supported public API documented here. The published v0.1.0
tag remains immutable history, but v0.1 compatibility and migration guidance
are intentionally not maintained.

The root `unio` package and `errs` package are the supported caller-facing Go
surface. Packages under `driver` are importable for adapters and tests but are
pre-1.0 implementation APIs and may change without the same compatibility
guarantees.

`SPEC.md` defines behavior shared by implementations. It does not claim that
every runtime currently implements every optional capability; use
`API_SUPPORT.md` for that information.

Use [GitHub Issues](https://github.com/Fullstop000/unio/issues) for public bug
reports and compatibility questions. Follow [SECURITY.md](../SECURITY.md) for
private vulnerability reports.
