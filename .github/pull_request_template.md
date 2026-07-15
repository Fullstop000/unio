## What changed

Describe the user-visible problem and the smallest coherent solution.

## Verification

- [ ] `gofmt` is clean
- [ ] `go mod tidy -diff` is clean
- [ ] `go vet ./...` passes
- [ ] `go test -race ./...` passes
- [ ] Relevant real E2E runtimes and exact CLI versions are listed below, or the reason they were not run is stated

Real E2E evidence:

## Contract and release impact

- [ ] GoDoc, runnable examples, and user docs are updated where required
- [ ] `docs/SPEC.md` and `docs/API_SUPPORT.md` are updated for observable contract or capability changes
- [ ] `CHANGELOG.md` includes the user-visible change
- [ ] Compatibility risk is described below

Compatibility notes:

Do not attach credentials, authenticated CLI state, or unredacted raw session data.
