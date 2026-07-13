---
name: ship
description: "Ship unio changes through a reviewed GitHub PR, or cut a tagged Go module release after merge. Use when asked to ship, land, merge, release, cut a version, or publish unio."
---

# Ship unio

Land one branch through one PR to `master`. Optionally turn the merged result into a SemVer-tagged Go module release.

unio has no version file and no binary Release workflow. The latest `vX.Y.Z` Git tag is the released version; `CHANGELOG.md` holds pending entries under `## Unreleased`.

## Rules

- Define the terminal state before mutating release metadata: PR only, or PR plus a version tag. If the user did not explicitly request a release, ask after the PR is ready and suggest a version.
- Keep the feature, changelog entry, and optional version preparation in one PR. Never create a follow-up release PR for the same shipment.
- Never push directly to `master`.
- Never merge, enable auto-merge, or push a tag without explicit human approval.
- Never assume squash merge approval. Ask which GitHub merge method to use if the approval does not name one.
- Stop on a failed gate, secret hit, unresolved review finding, or red CI. Fix the cause and rerun the affected gates.
- Preserve unrelated user changes. Do not stash, discard, or include them without permission.
- Tag the exact commit merged into `origin/master`, never the pre-merge branch tip.

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

Add one concise, user-facing bullet under `## Unreleased` for each meaningful change. Match unio's current flat-bullet style; do not invent category headings or PR-link syntax. Skip a changelog entry only for changes with no user-facing or release-operational effect.

Commit the coherent change with a conventional subject, then push and open one PR:

```bash
git push -u origin HEAD
gh pr create --base master --title "<concise title>" --body "<summary, verification, and E2E decision>"
gh pr checks --watch
```

Do not claim readiness until every required check is green. If CI fails, diagnose the root cause, fix it on the same branch, rerun local gates, push, and watch again.

## 5. Decide whether to release

Skip this question only when the user already explicitly requested either PR-only shipping or a release.

Find the latest release and inspect all commits since it:

```bash
last_tag=$(git describe --tags --abbrev=0 --match 'v[0-9]*' 2>/dev/null || true)
git log "${last_tag:+$last_tag..}HEAD" --pretty='%h %s'
```

Suggest the next SemVer version from the aggregate release contents, not only the current commit:

- incompatible public API change or `BREAKING CHANGE`: major, except that a pre-1.0 release normally increments the minor version unless the user is deliberately declaring 1.0;
- backward-compatible user-facing capability: minor;
- compatible fix, docs, maintenance, or internal change: patch.

Ask with the exact suggestion: “PR is green. Keep these entries under `Unreleased`, or prepare `vX.Y.Z` and tag the merged result?” The suggestion does not authorize a bump.

### PR only

Leave the entries under `## Unreleased`. Do not create or change a version elsewhere; unio has no such file.

### Release

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

## 6. Obtain merge approval

Report the exact state and wait for explicit approval:

- PR URL and green checks;
- local gates and whether real E2E ran;
- unresolved risks, if any;
- either “entries remain under `Unreleased`” or “prepared `vX.Y.Z`”.

Ask for the merge method if it was not specified. Then use the approved method, for example:

```bash
gh pr merge <number> --squash
```

Substitute `--merge` or `--rebase` only when that is what the human approved. Do not use `--auto`. Avoid `--delete-branch`: unio is commonly used through linked worktrees, where local branch cleanup can conflict with a branch checked out elsewhere. Delete the remote branch separately only if requested.

## 7. Tag an approved release

For PR-only shipping, stop after confirming the merge landed on `origin/master`.

For a release, fetch and resolve the merge commit from GitHub rather than assuming the local branch commit is releasable:

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

## Failure boundaries

- If GitHub authentication, permissions, branch protection, or CI blocks progress, report the exact failing command and state. Do not bypass the control.
- If release contents imply a different SemVer level than requested, surface the conflict and ask; do not silently choose.
- If the post-merge changelog or commit differs from the approved release state, do not tag it.
- If any verification was skipped or could not be observed, say so explicitly in the final report.
