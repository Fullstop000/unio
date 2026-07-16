## What changed

Describe the user-visible problem and the smallest coherent solution.

## Verification

- [ ] `git diff --check` passes
- [ ] `gofmt` is clean
- [ ] `go mod tidy -diff` is clean
- [ ] `go vet ./...` passes
- [ ] `go test -race ./...` passes
- [ ] Python gates pass when `python/` changes: Ruff format/check, Pyright, Pytest, build, and Twine check
- [ ] The complete branch diff was scanned for credentials
- [ ] Relevant real E2E runtimes and exact CLI versions are listed below, or the reason they were not run is stated

Real E2E evidence:

## Contract and release impact

- [ ] PR only; changelog entries remain under `Unreleased`
- [ ] Go release: `vX.Y.Z` is prepared in `CHANGELOG.md`
- [ ] Python release: `python-vX.Y.Z` matches `python/pyproject.toml` and is prepared in `python/CHANGELOG.md`
- [ ] The requested release tag is absent locally and remotely, or this is PR-only
- [ ] GoDoc/Python docstrings, runnable examples, and user docs are updated, or no update is required
- [ ] `docs/SPEC.md` and `docs/API_SUPPORT.md` are updated, or no observable contract/capability changed
- [ ] The affected SDK changelog includes the user-visible change (`CHANGELOG.md` for Go, `python/CHANGELOG.md` for Python), or no changelog update is required
- [ ] Compatibility risk is described below, including `None` when applicable

Compatibility notes:

Do not attach credentials, authenticated CLI state, or unredacted raw session data.
