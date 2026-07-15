---
name: ship
description: "Ship unio changes through a reviewed GitHub PR, or cut a tagged Go or Python SDK release after merge. Use when asked to ship, land, merge, release, cut a version, or publish unio."
---

# Ship unio

Land one branch through one PR to `master`. Optionally turn the merged result
into a tagged Go module release or a PyPI Python release.

Go uses `vX.Y.Z` tags and the root `CHANGELOG.md`. Python uses the version in
`python/pyproject.toml`, `python-vX.Y.Z` tags, `python/CHANGELOG.md`, and the
Trusted Publishing workflow in `.github/workflows/python-release.yml`.

## Rules

- Define the terminal state before mutating release metadata: PR only, or PR plus a version tag. If the user did not explicitly request a release, ask after the PR is ready and suggest a version.
- Keep the feature, changelog entry, and optional version preparation in one PR. Never create a follow-up release PR for the same shipment.
- Never push directly to `master`.
- Never merge, enable auto-merge, or push a tag without explicit human approval.
- Use squash merge only. Ask whether to approve the squash merge, never which merge method to use.
- Stop on a failed gate, secret hit, unresolved review finding, or red CI. Fix the cause and rerun the affected gates.
- Preserve unrelated user changes. Do not stash, discard, or include them without permission.
- Tag the exact commit merged into `origin/master`, never the pre-merge branch tip.
- Never treat a Go version as a Python version (or vice versa); identify the
  release target before editing metadata.

## 1. Establish scope and state

State concrete success criteria for the change and inspect the repository before acting:

```bash
git status --short
git branch --show-current
git fetch origin
git log --oneline --decorate origin/master..HEAD
git diff --stat origin/master...HEAD
```

Include uncommitted in-scope changes in the review; do not require a clean tree before they are committed. If HEAD is detached or is `master`, create a `codex/<slug>` branch at the current commit. Rebase on `origin/master` only when it will not overwrite unrelated work; resolve conflicts deliberately.

For a release-only request with no feature branch, create a release branch from `origin/master`. Do not tag `master` before the changelog PR merges.

## 2. Verify the implementation

Run the repository's actual local and CI gates:

```bash
git diff --check
unformatted=$(find . -name '*.go' -not -path './vendor/*' -exec gofmt -l {} +)
test -z "$unformatted"
go mod tidy -diff
go vet ./...
go test -race ./...
```

When `python/` or the shared contract changes, also run:

```bash
cd python
python -m pip install -e '.[dev]'
ruff format --check .
ruff check .
pyright
pytest
python -m build
twine check dist/*
```

New or changed behavior needs tests that prove the user-visible outcome, not merely coverage. `go test -race ./...` also compiles the packages and examples; do not add a redundant build gate without a concrete need.

Real E2E tests invoke authenticated agent CLIs and may consume tokens:

```bash
go test -tags e2e_real ./tests/...
```

Ask before running them. Recommend them when driver integration, session lifecycle, streaming, interruption, blocking/continuation, or runtime discovery changes. For docs-only or isolated internal changes, explain why they are unnecessary rather than pretending they ran.

## 3. Review and scan

Review `git diff origin/master...HEAD` plus any in-scope uncommitted diff for bugs, regressions, API breakage, concurrency errors, and missing tests. Use an available independent review tool when practical. Fix substantive findings and rerun the relevant gates; record a concise reason for dismissing any finding.

Scan the complete proposed diff before pushing. Prefer `gitleaks` when installed. At minimum run:

```bash
base=$(git merge-base origin/master HEAD)
git diff --binary "$base" | grep -nE 'sk-[A-Za-z0-9-]{24,}|gh[op]_[A-Za-z0-9]{20,}|AKIA[0-9A-Z]{16}|BSA[A-Za-z0-9]{20,}'
```

Exit 1 from `grep` means no regex match. Any plausible credential hit means stop, remove it from the entire branch history as needed, and rescan. Also inspect newly added configuration and fixtures that a regex can miss.

Because `git diff` omits untracked files, inspect every path reported by `git status --short`. Repeat the scan after the intended files are committed and before the first push; at that point the diff from `base` must contain the entire proposed change.

## 4. Prepare the changelog and PR

Add one concise, user-facing bullet under `## Unreleased` for each meaningful
change: use root `CHANGELOG.md` for Go/shared changes and
`python/CHANGELOG.md` for Python package changes. Match the current flat-bullet
style; do not invent category headings or PR-link syntax. Skip a changelog entry
only for changes with no user-facing or release-operational effect.

`.github/pull_request_template.md` is the canonical PR-body schema. Read it
before creating or updating a PR, then fill it with the observed state:

- replace the prompt under `What changed` with the user-visible summary;
- mark a verification checkbox only after that exact gate passes;
- record every real E2E runtime and exact CLI version, or the explicit reason it
  was not run;
- select exactly one release target and name the version when applicable;
- mark documentation, contract, changelog, compatibility, and tag-collision
  items only when they apply and are complete; and
- replace the compatibility placeholder with concrete risks or `None`.

Do not maintain a second, skill-specific PR-body format. Extra context such as a
regression found by CI may be added under the matching template section.

Commit the coherent change with a conventional subject, then push and open one
PR. Render a completed body from the repository template and pass it with
`--body` or `--body-file`; do not submit the untouched template:

```bash
git push -u origin HEAD
gh pr create --base master --title "<concise title>" --body-file <completed-pr-body>
gh pr checks --watch
```

For an existing PR, use the same completed template with `gh pr edit --body` or
`--body-file` so later release preparation and verification do not leave the PR
body stale.

Do not claim readiness until every required check is green. If CI fails, diagnose the root cause, fix it on the same branch, rerun local gates, push, and watch again.

## 5. Decide whether to release

Skip this question only when the user already explicitly requested either PR-only shipping or a release.

Find the latest release for the requested SDK and inspect all commits since it.
For Go:

```bash
last_tag=$(git describe --tags --abbrev=0 --match 'v[0-9]*' 2>/dev/null || true)
git log "${last_tag:+$last_tag..}HEAD" --pretty='%h %s'
```

For Python, use `python-v[0-9]*` instead and inspect `python/CHANGELOG.md` and
`python/pyproject.toml`.

Suggest the next SemVer version from the aggregate release contents, not only the current commit:

- incompatible public API change or `BREAKING CHANGE`: major, except that a pre-1.0 release normally increments the minor version unless the user is deliberately declaring 1.0;
- backward-compatible user-facing capability: minor;
- compatible fix, docs, maintenance, or internal change: patch.

Ask with the exact suggestion: “PR is green. Keep these entries under `Unreleased`, or prepare `vX.Y.Z` and tag the merged result?” The suggestion does not authorize a bump.

### PR only

Leave the entries under `## Unreleased`. Do not create or change a version elsewhere; unio has no such file.

### Go release

Before merge, verify the requested tag does not exist locally or remotely. Edit only `CHANGELOG.md` for release metadata:

```bash
git rev-parse --verify --quiet "refs/tags/vX.Y.Z"
git ls-remote --exit-code --tags origin "refs/tags/vX.Y.Z"
```

Both commands must report no existing tag. Then edit the changelog:

1. Keep a new empty `## Unreleased` section at the top.
2. Move all currently pending entries into `## vX.Y.Z - YYYY-MM-DD`.
3. Ensure the section accurately summarizes everything shipped since the previous tag.

Commit this on the same PR as `chore(release): prepare vX.Y.Z`, push, rerun the local gates affected by the edit, and reconfirm CI is green. The changelog heading does not make a release; the post-merge tag does.

### Python release

Verify `python-vX.Y.Z` is absent locally and remotely. Set the exact version in
`python/pyproject.toml`, keep a new `## Unreleased` section, and move pending
entries to `## X.Y.Z - YYYY-MM-DD` in `python/CHANGELOG.md`. Confirm the PyPI
pending Trusted Publisher and protected GitHub `pypi` environment exist before
merge. Commit release preparation on the same PR and rerun all Python gates.

## 6. Obtain merge approval

Report the exact state and wait for explicit approval:

- PR URL and green checks;
- local gates and whether real E2E ran;
- unresolved risks, if any;
- either “entries remain under `Unreleased`” or the exact prepared Go/Python
  release tag.

Ask for explicit squash-merge approval. After approval, run:

```bash
gh pr merge <number> --squash
```

Do not use `--merge`, `--rebase`, or `--auto`. Avoid `--delete-branch`: unio is commonly used through linked worktrees, where local branch cleanup can conflict with a branch checked out elsewhere. Delete the remote branch separately only if requested.

## 7. Tag an approved release

For PR-only shipping, stop after confirming the merge landed on `origin/master`.

For a release, fetch and resolve the merge commit from GitHub rather than
assuming the local branch commit is releasable. For Go:

```bash
git fetch origin master --tags
merged=$(gh pr view <number> --json mergeCommit --jq '.mergeCommit.oid')
git merge-base --is-ancestor "$merged" origin/master
git show "${merged}:CHANGELOG.md" | grep -F "## vX.Y.Z -"
git ls-remote --exit-code --tags origin "refs/tags/vX.Y.Z"
```

The final command must report no existing tag (exit 2). After explicit tag approval, create an annotated tag on the resolved merge commit and push only that tag:

```bash
git tag -a vX.Y.Z "$merged" -m "vX.Y.Z"
git push origin vX.Y.Z
```

Verify the remote tag resolves to the intended commit (peel the annotated tag) and that the module is addressable:

```bash
git ls-remote origin "refs/tags/vX.Y.Z" "refs/tags/vX.Y.Z^{}"
go list -m github.com/Fullstop000/unio@vX.Y.Z
```

The Go proxy can lag briefly; retry verification, but never move or recreate a published tag. Report the tag URL and be honest if proxy verification is still pending. Do not create a GitHub Release or binaries unless the user separately requests them; the repository currently publishes neither.

For Python, resolve and verify the squash merge identically, then confirm the
merged `python/pyproject.toml` version and dated changelog heading. After
explicit tag approval, create and push annotated `python-vX.Y.Z` on that merge
commit. Watch `.github/workflows/python-release.yml` through the publish job and
verify the PyPI project reports the new version. Never upload manually, reuse a
TestPyPI artifact, or move a tag after the workflow starts.

## Failure boundaries

- If GitHub authentication, permissions, branch protection, or CI blocks progress, report the exact failing command and state. Do not bypass the control.
- If release contents imply a different SemVer level than requested, surface the conflict and ask; do not silently choose.
- If the post-merge changelog or commit differs from the approved release state, do not tag it.
- If any verification was skipped or could not be observed, say so explicitly in the final report.
