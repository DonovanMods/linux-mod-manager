# Develop-Branch Workflow Design

**Date:** 2026-07-30
**Status:** Approved
**Supersedes:** the PR-per-story-into-main flow used through v1.26.0

## Goal

Keep `main` stable for longer stretches by accumulating story work on a
`develop` integration branch and releasing batches to `main` at deliberate
release points.

## Branch Model

- **`main`** — released, tagged states only. Remains the repo's default
  branch. Protected by the existing "Protect Main" ruleset (unchanged;
  `~DEFAULT_BRANCH` still resolves to main).
- **`develop`** — integration branch, created from current `main`. All story
  work merges here first.
- **Story branches** (`feat/…`, `fix/…`) — fork from `develop` and merge
  back into it locally with `git merge --no-ff`, no PR (see the 2026-10-30
  amendment below).
- **`release/vX.Y.Z`** — short-lived, one per release; deleted after merge.
- No other long-lived branches; this is git-flow-lite, not full git-flow.

## Story PR Flow (into develop) — superseded, see the amendment below

Identical to the previous flow except for the base branch and versioning:

- Copilot review + triage rounds, merge
  commits — all unchanged.
- **No version bump** in story branches. The `version` variable in
  `cmd/lmm/root.go` stays at the last released version between releases.
- CHANGELOG entries accumulate under `[Unreleased]`.

## Release Flow (develop → main)

At a release point (judged by the user):

1. Cut `release/vX.Y.Z` from `develop` with a single prep commit:
   - bump `version` in `cmd/lmm/root.go`,
   - move `[Unreleased]` to a dated `vX.Y.Z` section and add the comparison
     link,
   - `make man` (the genman test enforces this).
2. **Bump size is judged by the batch:** MINOR if it contains any feature,
   PATCH if fixes-only, MAJOR if breaking (CLI interface or config format).
3. Open a PR `release/vX.Y.Z` → `main` titled `release: vX.Y.Z`. Copilot
   reviews it; merge with a merge commit; tag `vX.Y.Z` on the merge commit
   (unchanged convention: lightweight tag).
4. Fast-forward develop and clean up:

   ```bash
   git push origin main:develop   # ff — develop's head is an ancestor of the merge commit
   git push origin --delete release/vX.Y.Z
   ```

   The ff push uses the repository-admin bypass on the develop ruleset.

## Hotfix Flow

For a critical bug on a released `main` while `develop` carries unreleased
work:

1. Branch from `main`, fix, PR → `main` with its own PATCH bump + CHANGELOG
   section + `make man`; tag on the merge commit.
2. Merge `main` back into `develop`. This is a true merge: use a PR if there
   are conflicts, or a bypass push if trivial.

## GitHub Setup

- Create `develop` from current `main` and push it.
- Add a **"Protect Develop"** ruleset targeting `refs/heads/develop`
  (the PR and Copilot rules it started with were removed on 2026-10-30):
  - deletion blocked,
  - non-fast-forward (force-push) blocked,
  - **bypass: repository admin, always** — the escape hatch.
- "Protect Main" ruleset: no changes.

### The admin bypass

The bypass exists for three uses, in expected order of frequency:

1. Post-release ff-sync of develop (every release).
2. Trivial hotfix back-merges.
3. **Force-push repair of a broken commit on develop — last resort only.**
   Because ruleset bypasses cover the whole ruleset, the admin can
   force-push without toggling protection off and on (no
   "forgot-to-re-enable" failure mode). After any history rewrite on
   develop, in-flight story branches **must be rebased** onto the new
   develop, or their PRs will reintroduce the old commits.

## CI

- `test.yml` already runs on every pull request regardless of base branch,
  so story PRs into develop keep full CI. Add `develop` to its `push`
  trigger (`branches: [main, develop]`) so bypass pushes (ff-sync, trivial
  hotfix back-merges) are also tested.
- `release.yml` triggers on `v*` tags and is unaffected.

## Documentation Updates

- Repo `CLAUDE.md`: rewrite the Versioning section — bump-at-release,
  `[Unreleased]` accumulation, story PRs target develop, release and hotfix
  flows.
- Update the `lmm-repo-conventions` memory so future sessions don't PR
  against main out of habit.

## Out of Scope (YAGNI)

- Pre-release/dev version suffixes on develop builds.
- `release/*` as long-lived stabilization branches.
- Changing the default branch to develop.
- Any CI changes beyond the one-line `test.yml` trigger addition.

## Amendment (2026-10-30): no PRs into develop

Story branches no longer PR into `develop`. The pull request and Copilot
review rules were removed from "Protect Develop"; it keeps deletion and
non-fast-forward protection and the admin bypass. The PR round trip slowed
every story down, and what it bought was Copilot review, which hits its
rate limit in a busy session. A story branch now:

1. passes `make test` on the branch,
2. gets an independent review of its diff (a subagent, in Copilot's place),
3. merges into `develop` locally with `git merge --no-ff` and is pushed;
   CI runs on that push, and a red run is fixed forward on `develop`.

The "Story PR Flow" section above is historical. Releases and hotfixes are
unchanged: they still PR into `main`, which keeps its PR and Copilot rules.
