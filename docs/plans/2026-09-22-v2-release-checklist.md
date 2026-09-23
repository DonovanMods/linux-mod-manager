# v2.0.0 public release checklist

This is the current release checklist for the v2 line after the September 22
stabilization. It supersedes the premature-cut procedure archived in
[`archive/v2.0.0-release-checklist.md`](archive/v2.0.0-release-checklist.md)
and the earlier `v2.1.0` serve-release assumptions noted in the
[serve design](2026-08-30-serve-design.md). The old tag and unpublished draft
were retracted on September 9; the archive is the record of that retraction,
not a set of steps to repeat.

The chosen Go module path is already
`github.com/DonovanMods/linux-mod-manager/v2`, and the binary version is
already `2.0.0`. This checklist does not change either value or prepare a
release candidate.

## Stabilization evidence (September 22, 2026)

Recorded from the finalized tree `09dded705874663dfbb41fe6d78120db15b8949b`
(the worktree tree is identical at `b8947e5c`):

- `make test` — **PASS**, all packages; log: `/tmp/lmm-v2-verified-test.log`.
- `make vet` — **PASS**.
- `go build -o /tmp/lmm-v2-stabilized ./cmd/lmm` — **PASS**; the binary
  reports `lmm version 2.0.0`.
- Authenticated `trunk check --all --ci` — **PASS**, 1,548 files.
- `make test-race` — **PASS** on the finalized tree; log:
  `/tmp/lmm-v2-verified-race.log`.
- Hosted GitHub CI run
  [#35800961386](https://github.com/DonovanMods/linux-mod-manager/actions/runs/35800961386)
  — **FAIL**. All non-serve packages and four targeted #498 checks passed, but
  `TestE2E_RichText_ShapedInputRendersQuickly` exceeded its 500 ms budget
  (552/559/706 ms) and `TestE2E_TrayEntryExpandsToTheEventStream` timed out
  after 60 seconds. Failure log: `/tmp/lmm-v2-ci-35800961386-failure.log`. This
  is a release blocker; no fixes are part of this documentation task.
- GoReleaser is not installed locally. No release archives or Linux packages
  have been generated or install-smoke-tested in this validation.

### Final gate results

Coordinator final evidence recorded before this checklist was finalized:

| Gate | Result | Evidence |
| --- | --- | --- |
| `make test-race` on the finalized tree | PASS | `/tmp/lmm-v2-verified-race.log` |
| Hosted CI run #35800961386 | FAIL; release blocker | [GitHub Actions](https://github.com/DonovanMods/linux-mod-manager/actions/runs/35800961386); `/tmp/lmm-v2-ci-35800961386-failure.log`; two failing serve E2E checks listed above |

## Owner checks before tagging

- [ ] Review the complete `[Unreleased]` section in `CHANGELOG.md` for the
  v2.0.0 release notes; agree on the release date and final text.
- [ ] Confirm the generated man pages are current (`make man` and the
  `cmd/lmm` man-page drift test); full normal tests passed, but no separate
  release-time regeneration is recorded here.
- [ ] Run the real release packaging workflow or an equivalent GoReleaser
  build. Inspect Linux amd64/arm64 `.tar.gz`, `.deb`, `.rpm`, and `.apk`
  assets, generated shell completions and packaged man pages; install and
  smoke-test the applicable packages on supported Linux systems. These checks
  have not been run locally because GoReleaser is absent.
- [ ] Decide whether the `AUR_KEY` secret is configured and whether this cut
  should publish to AUR. The current config skips AUR upload when the secret
  is empty.
- [ ] Hand-test both frontends on a clean supported Linux setup: CLI startup,
  help/version and representative profile workflow; web UI launch, primary
  navigation, a representative mutation, and keyboard/focus behavior. These
  owner checks remain undone; automated Go tests are not a substitute.
- [ ] Resolve the two hosted CI serve E2E failures above, rerun hosted CI, and
  confirm a green result before proceeding. Local race, normal tests, vet, build,
  and lint passed; that does not waive this hosted release gate.

## Tag and publication sequence

1. After the owner checks and final gates pass, prepare the release commit on
   `v2`: fold `[Unreleased]` into the single existing retracted
   `## [2.0.0] - 2026-08-30` section in `CHANGELOG.md`, re-date that section
   to the actual cut date, and leave a fresh `[Unreleased]` heading. Reuse or
   update its existing `[2.0.0]` comparison reference; do not add a second
   v2.0.0 section or reference. Regenerate man pages if they changed. Keep
   `cmd/lmm/root.go` at `2.0.0` and the module path at `/v2`.
2. Review the release diff, run `make test`, `make vet`, and `make man`, then
   commit the release preparation on `v2`.
3. Create `v2.0.0` on the approved release commit and push the branch and tag
   to the project's release remote. The release workflow runs only for `v*`
   tags; inspect its generated assets and draft release.
4. Publish the GitHub release manually only after reviewing the draft, assets,
   checksums, notes, and any required AUR outcome. `.goreleaser.yaml` currently
   has `draft: true`; this checklist does not change that setting.
5. Keep `main` and `develop` untouched. Promotion from `v2` requires a separate
   decision and workflow.

No tag, publish, release-config change, or owner hand-test is part of issue
[#495](https://github.com/DonovanMods/linux-mod-manager/issues/495).
