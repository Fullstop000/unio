# Contributing to unio

Thanks for helping improve unio. The project is a pre-1.0 Go and Python SDK
family that presents one public API over several independently evolving
coding-agent CLIs. Changes
should stay small, prove their user-visible behavior, and keep runtime-specific
differences explicit.

## Prerequisites

- Go 1.23 or newer.
- Python 3.11 or newer for Python SDK changes.
- Git.
- An agent CLI is not required for unit tests.
- Real end-to-end tests require the relevant CLI to be installed and
  authenticated. They may consume tokens or incur provider cost.

Start by creating a branch from the latest `master`. Keep one coherent change
per pull request; include its implementation, tests, documentation, and
changelog entry together.

## Local verification

Run the project gates before opening a pull request:

```sh
unformatted=$(find . -name '*.go' -not -path './vendor/*' -exec gofmt -l {} +)
test -z "$unformatted"
go mod tidy -diff
go vet ./...
go test -race ./...
```

`go test -race ./...` also compiles the packages and examples.

For Python SDK changes:

```sh
cd python
python -m venv .venv
. .venv/bin/activate
python -m pip install -e '.[dev]'
ruff format --check .
ruff check .
pyright
pytest
python -m build
twine check dist/*
```

Real runtime tests are opt-in:

```sh
go test -tags e2e_real ./tests/...
cd python
UNIO_RUN_REAL_E2E=1 pytest -s tests/e2e_real
```

Run the relevant real tests when changing runtime discovery, protocols,
session lifecycle, streaming, interruption, blocking, blocked responses, or session
history parsing. In the pull request, list the runtimes and CLI versions tested.
If real tests were not run, say so explicitly and explain why.

## Change requirements

### Public API or behavior

- Add tests for the observable result, including failure and concurrency paths
  where relevant.
- Add or update GoDoc for every exported identifier and field affected.
- Update `README.md` or a runnable example when the normal caller workflow
  changes.
- Add one concise, user-facing bullet under `## Unreleased` in the affected SDK
  changelog: root `CHANGELOG.md` for Go, `python/CHANGELOG.md` for Python, or
  both for a shared change.

### Cross-language contract

`docs/SPEC.md` is the normative behavior contract. Changing a frozen state, event,
blocked reason, error kind, data format, or other observable behavior requires:

- a specification version bump;
- matching implementation and tests;
- a matching update to `docs/contract.json`, including its `spec_version`, and
  passing contract tests in both SDKs;
- an update to `docs/API_SUPPORT.md` when runtime support differs; and
- a clear compatibility note in the pull request and changelog when existing
  callers must change. A dedicated migration document is added only when the
  maintainers decide to support that upgrade path.

Do not change a frozen string value only to make its Go name look cleaner.

### Runtime drivers

- Keep protocol and transport details out of the root `unio` package.
- Treat runtime capabilities as dynamic unless the protocol guarantees them.
- Test malformed responses, process closure, cancellation, and repeated or
  concurrent operations when the changed path can encounter them.
- Record the exact CLI version used for a real compatibility check.

### Documentation

- Prefer links to the source of truth over copying behavior tables.
- Keep `README.md` focused on the first successful user journey.
- Keep [docs/README.md](docs/README.md) as the documentation index and stability
  boundary.
- Keep `docs/API_SUPPORT.md` focused on runtime capability differences.
- Keep language-specific parameter, field, zero-value, and error semantics in
  GoDoc or Python docstrings and type annotations.
- Use [docs/ERRORS.md](docs/ERRORS.md) for caller-facing error guidance.

## Pull requests

A pull request should include:

- a concise explanation of the user-visible problem and solution;
- tests proving the behavior;
- the local commands that passed;
- real E2E coverage or an explicit statement that it was not run;
- documentation and changelog updates when required; and
- any compatibility or rollout risk reviewers should know.

Avoid unrelated cleanup, generated noise, and speculative compatibility code.
Never commit credentials, authenticated CLI state, raw session histories, or
logs that may contain prompts, source code, paths, command output, or tokens.

Maintainers use reviewed pull requests and squash merge into `master`. Do not
enable auto-merge or split one user-visible change across multiple pull
requests unless a maintainer has agreed to that plan.

## Reporting bugs

Include enough information to reproduce the problem without including secrets:

- unio version or commit;
- SDK language/version, language runtime version, and operating system;
- selected `AgentKind`;
- agent CLI name and exact version;
- the operation that failed and the Session state;
- the error kind from `errs.KindOf`, plus a sanitized message; and
- a minimal reproducer or protocol fixture when possible.

For compatibility failures after a CLI upgrade, include the last known working
CLI version. Do not attach `Session.Raw` output without reviewing and redacting
its full contents.

## Releases

Maintainers publish the Go module by creating a SemVer Git tag on the exact
commit merged into `master`. A changelog heading alone is not a release. Tags
are never moved or recreated after publication.

Python versions are independent from Go versions. A `python-vX.Y.Z` tag must
match `python/pyproject.toml` and `python/CHANGELOG.md`; the Python release
workflow builds and publishes the `unio-py` distribution through PyPI Trusted
Publishing. Configure that PyPI project with repository `Fullstop000/unio`,
workflow `python-release.yml`, and environment `pypi` before creating the first
tag. TestPyPI may be used for the first dry run with a temporary
workflow/environment, but its artifact must not be reused for the production
release.
