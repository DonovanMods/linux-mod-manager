# Deploy Convergence (#168+#212) & Batch Hook Ordering (#214) — Design

**Date:** 2026-08-04
**Status:** Draft, awaiting user review
**Issues:** [#214](https://github.com/DonovanMods/linux-mod-manager/issues/214) (ship first),
[#168](https://github.com/DonovanMods/linux-mod-manager/issues/168) + [#212](https://github.com/DonovanMods/linux-mod-manager/issues/212)
(one story), plus branch-deferred polish. All target the v1.29.0 batch
(#210 + #211 already merged).

## Part 1 — #214: batch primary pre-resolution before `install.before_all`

**Problem:** In `ApplyInstall`'s BATCH path (`internal/core/flows.go`), the
`install.before_all` hook fires before the primary's
`TargetVersion`/`TargetFileIDs` pre-resolution block. Any failure there — an
unresolvable `--file` ID (pre-existing) or #211's mixed-variant rejection —
aborts after a user hook with arbitrary side effects already ran. The STRICT
path resolves and validates before all hooks.

**Fix: hoist, don't dry-run.** Move the primary pre-resolution block (pool
fetch → `selectInstallTargetFiles` → `ValidateInstallFileSelection`) above
the BATCH path's `install.before_all` call. The block is read-only (source
fetch + pure selection), so hoisting cannot change what any hook observes on
success; on failure the install now aborts before the hook, matching the
STRICT path's documented "resolve pins up front, before any side effect"
philosophy (#140). The #96/#140 doc comments describing the batch path's
"ONE divergence" ordering are updated to match.

**Tests (TDD):** a batch-path install whose primary pre-resolution fails
(bad `--file` ID, and the #211 mixed rejection) must NOT run
`install.before_all` (hook-recording fixture); success path ordering
unchanged (hook still runs exactly once, before any mod installs).

**Class:** PATCH. Branch `fix/214-batch-preresolve-order`.

## Part 2 — #168 + #212: `verify --fix` converges the deployment

**Problem family:** nothing reconciles the game directory with current
reality after the cache moves on. Two known shapes:

- #168: a directory-source member deleted upstream stays linked until the
  next reinstall (`verify --fix` repairs the cache but never the deployment).
- #212: a pre-#210 stale-pak symlink dangles forever once `PruneUnclaimed`
  removes its cache target before any deploy cycle (post-prune, no
  `ListFiles` union ever names it again).

**Fix: one core convergence pass, provenance-gated.** New core method
(shared seam per [[lmm-cli-tui-parity]]; the TUI has no verify surface
today, but the capability lands in core):

```
func (s *Service) ConvergeDeployedFiles(ctx context.Context, game *domain.Game, profileName string) (*ConvergeResult, error)
```

Two sub-passes, both remove-only, never deploy:

1. **Row-driven (the #168 shape):** for every installed mod in the profile,
   compute the current deployable set (`deployableFiles`, #210) and compare
   against the mod's `deployed_files` DB rows. A row whose relative path is
   NOT in the deployable set is stale: `Undeploy` the game-dir path (the
   linker tolerates already-absent paths) and delete the row. Provenance is
   the DB row itself — lmm recorded that it deployed this path for this
   mod — plus the resolver saying the mod no longer provides it.
2. **Dangling-link sweep (the #212 shape):** walk the game's ModPath for
   symlinks whose target resolves under the lmm cache root(s) for this game
   (global cache dir and any per-game `cache_path`) and whose target does
   not exist. Attribution is the link target itself — only lmm creates
   links into its cache — so these are removed even when no DB row survives
   (the #212 sequence rewrites rows). Regular files and symlinks pointing
   anywhere else are never touched.

Safety rules (hard):

- Remove-only; a convergence never deploys or modifies file content.
- Non-symlink game-dir files are removed ONLY via sub-pass 1 (row-attributed
  — covers copy/hardlink deploys); the sweep in sub-pass 2 is symlink-only.
- Every removal is reported (path + reason + owning mod when known); a
  `--fix` run prints them under the existing verify output conventions and
  counts them in the summary. Errors on individual removals are warnings,
  not aborts (verify's existing per-item tolerance).

**Wiring:** `cmd/lmm/verify.go` calls the method during `--fix` after the
existing cache repairs (so re-ingested caches are judged in their repaired
state). Plain `verify` (no `--fix`) reports what WOULD be removed as
warnings, using the same core pass in dry-run form: the method signature is
`ConvergeDeployedFiles(ctx, game, profileName string, dryRun bool)
(*ConvergeResult, error)` with `ConvergeResult{Removed []ConvergedFile}`
(`ConvergedFile{Path, Reason, SourceID, ModID string}`); with `dryRun=true`
nothing is touched and `Removed` lists the candidates.

**Merged-pak note:** the profile-level `zzz_LMM_Merged_P.pak` link is owned
by the merged-pak subsystem, not per-mod rows; the sweep must treat a
HEALTHY merged-pak link (target exists) as untouchable, and a dangling one
as removable like any other cache-pointing link (the next deploy/recompile
recreates it).

**Class:** MINOR (widens `--fix`'s contract — matches #168's own framing).
Branch `feat/168-212-deploy-convergence`. Closes #168 and #212.

**Polish folded into this branch (final task):**

- Organic compile+redownload prune integration test (deferred from #210 T5).
- CLI chooser test hardening: EOF-after-rejected-selection; validate-runs-on
  single-file/`--yes` paths (deferred from #211 T3).
- `internal/core/deployable.go`: reuse the computed `ModPath` for the
  `HasRetainedSource` call (deferred from #210 T3).
- `docs/configuration.md` compile bullet: replace the stale "compiled into a
  new artifact … produce a deployable `_P.pak`" wording (pre-#197 model)
  with the validate+retain + profile-level merged-pak description.

## Out of scope

- TUI verify surface (tracked by #168's note; capability lands in core).
- Automatic convergence during `deploy` (deploy's Uninstall-union pass
  already self-heals everything with a live cache member; only the
  cache-member-gone shapes need `--fix`).
- #170, #206, and the feature epics.

## Rollout

Two branches off develop, `--base develop`, in order: #214 (PATCH-class)
then #168+#212 (MINOR-class). CHANGELOG under `[Unreleased]`. Close #168,
#212, #214 manually after merges. Then cut `release/v1.29.0`.
