# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- CLI output is now colorized by default when stdout is a terminal, extending the existing `colorGreen`/`colorRed`/`colorYellow` accent mechanism (previously only used by `deploy`/`verify`) with a full 4-color palette (green/yellow/red/cyan, plus bold/dim) across `list`, `status`, `search`, `update`, `conflicts`, and `mod show`. Table headers are bold+cyan. `lmm list` tints the whole row identically with or without `-v` (the row-tint decision is a single shared helper keyed on the mod's actual state, not the display flag): green for the common enabled+deployed case, yellow for enabled-but-undeployed, dim for disabled. `search` tints an installed mod's whole row green; `update`'s POLICY column colors per row. `status`/`mod show` color their values, not just the odd count: `lmm status -g <game>`'s active profile and per-profile "(active)" marker are green, mod/profile counts are cyan, Link Method is cyan, Last Deploy is green (or dim when never deployed); `mod show`'s Version fields are cyan and its Update policy is colored per state (green for auto, yellow for pinned); `conflicts`' stale winner suffix is yellow; and the existing `✓`/`✗` success/failure markers extend to `update` and `mod`'s confirmation lines. Detection is TTY-aware (piped/redirected output stays plain) and layers on top of the existing `--no-color` flag and `NO_COLOR` env var (presence-only per no-color.org), which continue to work unchanged; `--json` output is never colored. Table color is applied only to already-tabwriter-padded text (accented headers, whole-row tints, or a table's genuinely last column) — never to interior cell values before they reach `text/tabwriter`, which pads columns by raw byte length and would misalign them (#112, #193)
- **Icarus built-in mod source** (`internal/source/icarus`): a public, unauthenticated Firestore-backed catalog (Project Daedalus) — `lmm search`/`install`/`update` work against it like NexusMods/CurseForge. A `.exmodz` mod file is validated and its row-level table diffs retained via a new, game-agnostic `internal/unrealpak` PAK reader/writer and the new `deploy_mode: compile` game setting (see the merged-pak bullet below for how it actually deploys); a plain `.pak` file from the same catalog is unaffected and deploys through the existing extract/copy pipeline unchanged. Base data tables are read directly from the installed game's own `data.pak`, so a merge always matches the installed game version and works entirely offline; `internal/unrealpak` reads both the stored and the Zlib-compressed entries that pak contains, using only the standard library (#136, #175)
- `lmm game detect` now recognizes Icarus (Steam App ID `1149460`) and generates a complete `games.yaml` entry for it (`deploy_mode: compile`, `sources: {icarus: icarus}`) — no more hand-editing `games.yaml` to get started. The known-games schema (`steam-games.yaml`, built-in or your own override) gained two optional fields, `deploy_mode` and `sources`, generalizing detection beyond NexusMods-only games; every existing entry is unaffected (#177)
- Custom `api` sources' `search` endpoint gains `{category}`/`{tags}` path placeholders, fed from `SearchQuery.Category`/`.Tags` (URL-escaped; multiple tags comma-joined) — previously these were silently dropped with no way for a declarative source to express category/tag filtering. A definition whose `search` path omits the new placeholders is unaffected: the values are computed but never substituted in, matching today's behavior exactly (#120)
- Compiled mods (`deploy_mode: compile`, e.g. Icarus) with more than one enabled `.exmodz` mod now compose correctly instead of silently shadowing each other: every enabled mod's table-row diffs are applied sequentially, in profile load order, into ONE merged `zzz_LMM_Merged_P.pak` per profile (named to mount last, so it always wins over a plain prebuilt `.pak`'s own table override) — two mods patching different fields of the same row, or entirely different rows of the same table, both survive; only a genuine same-field conflict is last-wins, and a bundled-asset path collision (which can't compose) is last-wins with a loud warning. This also fixes "the Friday problem" (a weekly base-pak refresh silently reverting a mod's patched tables, with nothing to notice): the merge regenerates whenever the enabled-mod set, load order, a mod's version, or the base pak itself changes. `lmm update` (CLI and TUI) reports a "recompile needed" row for the profile's merged pak (additive `--json` field `recompile_needed`/`reason`) alongside normal version updates; applying it regenerates and redeploys — pinned mods' diffs recompile normally, and a LOCKED mod's diff still participates in every merge (a lock pins that mod's own version, not the profile's merged pak). Installing/importing a `.exmodz` now only validates and retains it (a per-mod compiled pak is no longer generated or deployed); a plain prebuilt `.pak` mod, and every non-`deploy_mode: compile` game, is completely unaffected. `lmm verify` gains a matching "RECOMPILE NEEDED" warning row (`stale_compile`) for the profile's merged pak (#136, #175, #196, #197)

### Changed

- An unrecognized, non-empty `link_method` (`games.yaml`, profile files, imported profiles) or `deploy_mode` (`games.yaml`; also `lmm game detect`'s `steam-games.yaml`) is now a load-time error naming the field, the offending value, the owning game/profile, and the valid options — instead of silently falling back to the default (`symlink`/`extract`). **Breaking for configs that were already silently misbehaving:** a typo like `deploy_mode: compil` previously ran as `extract` with no warning; it now refuses to load until fixed. An empty/absent value is unaffected and keeps today's default exactly (#172)

### Fixed

- `lmm uninstall` left behind the now-empty per-mod cache directory (`<source>-<modID>/`) after removing a mod's last cached version — cosmetic litter that accumulated indefinitely. `Cache.Delete` now removes the container too, but only when it's actually empty (a mod with other cached versions, or `--keep-cache`, is unaffected) (#190)
- `lmm game detect` printed every stale-library warning twice on Linux installs where `~/.steam/steam` is a symlink to `~/.local/share/Steam` (or the reverse): both existed as candidate Steam roots, so the whole library scan — and every warning it produced — ran twice against the identical real directory. `FindSteamRoots` now dedups candidates by their resolved (symlink-followed) real path, keeping only the first; a genuinely separate second root is unaffected (#190)
- `internal/unrealpak`'s unsupported-compression-method refusal named a blank `CompressionMethods` table slot as `compression method "" (index N)` — an in-range but never-named slot, distinct from a genuinely out-of-range index. It now names the slot itself (`unnamed method N`), so the refusal is actionable instead of pointing at an empty string (#190)
- `lmm install` (and the TUI's equivalent progress line) announced a `.exmodz` compile step as "Extracting to cache..." — actively misleading, since compiling never extracts anything. It now prints "Compiling `<source>` → `<output>`..." instead, naming the actual archive and the compiled `_P.pak` output; a plain extract/copy install is unaffected and keeps today's exact "Extracting to cache..." text (#190)
- `Service.GetEffectiveLinkMethod` — the profile > game > global resolution behind every deploy/install/import/status/verify operation — no longer silently swallows an invalid profile `link_method`: since #172, `config.LoadProfile` fails loud on an unrecognized value, but `GetEffectiveLinkMethod` treated ANY profile-load error, including that one, as "no explicit override" and fell back to the game/global default with nothing surfaced — meaning a hand-edited profile with a typo'd `link_method` could deploy with the wrong method, no error, no warning. It now distinguishes that case (`errors.Is(err, domain.ErrInvalidLinkMethod)`) from a missing/unreadable profile file — which still degrades silently by design, since profiles are optional — and returns the validation error instead, propagated through every call site (`DeployProfile`, `EnableMod`/`DisableMod`, `ApplyInstall`/`ApplyUpdate`/`ApplyRollback`/`ApplyImport`/`ApplyProfileSwitch`, `lmm deploy`/`import`/`install`/`profile apply`/`verify --fix`). Narrow in practice — both save and load paths validate now, so only a profile hand-edited after the fact can trigger it — but it closes the one place #172's fail-loud contract didn't reach (#189)
- `lmm import` of a local `.exmodz` file for a `deploy_mode: compile` game (Icarus) now routes through the same compile step as a download: it resolves the game's mapped `source.Compiler`-capable source from the registry and compiles the archive against the installed base pak, caching the resulting `_P.pak` the same way `DownloadModToCache` does. Previously the import path extracted/copied `.exmodz` files as-is, landing an uncompiled archive in the cache instead of a deployable pak. A missing compiler-capable source or missing base pak now fails loud with an actionable error rather than silently caching the uncompiled file; non-`.exmodz` imports are unaffected (#173)
- `lmm mod disable` undeployed a mod's files and cleared `enabled`, but never cleared `deployed` — `lmm list -v` kept showing DEPLOYED yes after disable. The disable flow now clears `deployed` unconditionally after the undeploy attempt, even when the undeploy itself only partially succeeds (already a non-fatal, Note-reported condition), so the flag always reflects disable-intent rather than lagging behind a best-effort file cleanup. The symmetric enable path had the same gap — enabling a disabled mod re-deployed its files without ever setting `deployed` back to true — and is fixed the same way. Both `SetModDeployed` calls follow the same non-fatal Note convention already used by `DeployProfile`/`PurgeProfile` for this same setter: a failure to record the flag doesn't block the primary enable/disable outcome (#183)
- `lmm install`'s multi-select path (`batchInstallMods`, used whenever more than one search result is selected) never synced the profile's merged pak for a `deploy_mode: compile` game (Icarus): installing `.exmodz` mods this way validated and cached them but left the merged pak undeployed, silently, until a later `lmm update` self-healed it. A seam audit of every mutation entry point that can change a profile's enabled-exmodz set/order/versions found and closed 4 more gaps with the same shape — `lmm profile apply`/`profile sync`, `lmm mod edit --version`, and `lmm verify --fix` also never reached the merged-pak sync. All five now call the same shared `Service.SyncMergedPak` entry point the rest of the flows already used, instead of bypassing it. Separately, several flows that _did_ call the sync already (including single-mod `lmm install`) only recorded a failure in a diagnostic field their own CLI caller never read back — a sync failure could be completely silent, not even `--verbose`-gated. Sync failures now surface unconditionally on stderr everywhere the merged pak is synced (CLI and TUI). Also corrected three misleading messages this bug produced along the way: `lmm install`/`import` say "Installed (merged pak updated)" instead of a false "(0 files)" for a compile-mode mod (deploying zero files of its own is correct, not a failure); `lmm mod files` explains that such a mod participates in the profile's merged pak instead of suggesting it "may need to be redeployed"; and `lmm verify`'s "RECOMPILE NEEDED" row now names the real cause (a changed base pak vs. an artifact simply missing from disk) and points at `lmm update --all`, since this row is never auto-applied by a bare `lmm update` (#197)

## [1.27.1] - 2026-07-30

### Fixed

- Re-ingesting a directory-source mod into an existing cache entry for the same (source, mod, version) — `verify --fix`'s checksum repair, or any install that re-downloads into a retained entry — no longer keeps files that were deleted from the source directory: the ingest now replaces the cache entry outright instead of overlaying the source onto a copy of the old entry, so removed members disappear from the committed cache (and from what gets deployed) instead of persisting indefinitely. Safe because directory sources serve exactly one synthetic file (`main`) per mod, so there are never sibling-file members to preserve. The `main` marker's member manifest and the member-set digest are now derived from the staged copy that actually gets committed, which also folds in-root symlinks (materialized as regular files by the ingest's dereferencing copy) into the manifest/digest consistently with what is cached and deployed — previously such files were cached but unattributed. Note: for sources containing in-root symlinks, the stored digest changes shape once (the next re-ingest records the new value and converges) (#166)
- `lmm verify --fix` no longer reports "OK (checksum populated)" — and `--json` no longer flips the row to `"ok"` — for a repair that persisted nothing: success is only claimed when a checksum was actually written, and a re-download that yields no checksum to store keeps the NO CHECKSUM warning (counted in the summary) with an honest note saying why; the MISSING repair path gets the same honesty one step removed. The root cause is also fixed: local ingests now produce a checksum for the install/verify paths to record — the MD5 of the source file for file/archive ingests (matching the download path's MD5-of-archive), and a deterministic member-set digest (sorted relative path + content MD5 per member) for directory ingests, so re-ingesting an unchanged source reproduces the stored value and directory-source mods (file ID `main`) converge to a clean `verify` instead of warning NO CHECKSUM — and being redundantly re-copied into the cache — on every run, forever. Install-time recording benefits automatically, so newly installed directory-source mods start with checksums. Latent hardening: the checksum DB write now errors when it matches no installed-file row instead of silently no-opping (#164)

## [1.27.0] - 2026-07-30

### Added

- Bulk `lmm update --json` now marks a locked mod's `updates[]` entry with `"locked": true` (omitted when unlocked), the JSON sibling of the table's `[locked@<version>]` POLICY marker, so a JSON consumer can tell that a reported update will not be applied — even under auto policy or `--all` — until the lock moves or clears. Additive JSON-contract change (#143)
- `lmm status -g <game> --json` gains `effective_link_method` and `link_method_source` (`profile`/`game`/`global`): the JSON twin of the text output's effective Link Method line (profile > game > global, PR #151), which the JSON document previously had no way to express — a profile-level `link_method` override made the two outputs disagree. The existing `link_method` key deliberately keeps its game-level meaning (game override or global default) for contract stability. Additive JSON contract change — makes the release containing it MINOR (#155)

### Changed

- Development now flows through a `develop` integration branch (git-flow-lite): story PRs target `develop`, versions are bumped once per release batch, and `main` holds only released, tagged states. CI's test workflow additionally runs on pushes to `develop`

### Fixed

- A deploy-time cache-miss heal (stored file IDs gone upstream, re-resolved to the recorded version) now persists the healed FileIDs onto the installed row via the targeted `SetModFileIDs` setter, so `profile export` emits the live IDs instead of the dead ones and later cache misses resolve them directly instead of re-healing every time. The write is skipped when the resolved set is unchanged, preserving the rows' recorded checksums (#139)
- Source-linked imports (`lmm import --id`, and scan imports whose source match resolves the archive by exact filename) now resolve the archive against the source's file listing, stamp the cache entry's per-file completion marker (with its member manifest) and record the real FileIDs on the DB row and profile ref - so imported entries pass the file-granular cache-first guards instead of eating one redundant redownload, and provenance-based undeploy narrowing can attribute their members. Resolution is strictly non-fatal (offline/ambiguous keeps today's marker-less import), and never fires for local/unmatched imports (#139)
- TUI: per-mod import failures now survive into the completion outcome's warnings ("source:mod: reason", parity with profile switch) instead of appearing only as a transient live-progress line the completion message replaces — the reason and any remediation hint (e.g. the stored-files-gone message from #95) stay readable after the import finishes. Core's `ProfileImportResult.Warnings` (previously always empty, internal contract only — no JSON involved) now carries the lines; the CLI is unaffected, it already prints each failure live (#131)
- `lmm verify`'s VERSION UNVERIFIABLE hint now offers the same remedies as the deploy-time stored-files-gone error it shares a root cause with: "reinstall the mod or run 'lmm update' to adopt the current version" instead of just "reinstall to refresh the recorded version" (#131)
- `lmm install --file` is no longer silently ignored when the named mod has dependencies: the dependency (batch) install path now honors `--file` for the named mod — alone or combined with `--version`, resolving the ID(s) against the version's matches when both are given — downloading and recording every pinned file, while dependencies still auto-select their own primary file at latest. A file ID that doesn't resolve aborts the whole install up front, before any dependency is touched, matching `--version`'s existing loudness — never a silent fallback to the auto-picked file (#140)
- `--version` (and the new `--file` pin) is now resolved inside core on the no-dependencies install path too, instead of relying on a CLI-side override of the planned file list: the CLI's interactive/`--file` sub-selection within the version still wins unchanged, but a core caller that only sets the target version — e.g. a future TUI version picker — now gets that version installed rather than a silent latest, and the up-front mod-lock gate judges the version the resolved install actually records, so installing a locked mod at exactly its locked version via `--version` converges instead of being refused (#140)
- `lmm update rollback` on a locked mod no longer prints the optimistic "Rolling back..." header before the lock refusal surfaces: a CLI pre-check (mirroring the single-mod update path's) now refuses up front as a skip, naming both move-the-lock/unlock remedies, and `--json` emits the single-mod document with `status: "skipped", reason: "locked"` — parity with a locked `lmm update <mod-id>` — instead of the `{"error": ...}` shape and non-zero exit the core gate's error produced. The TUI's rollback key (`<`) likewise refuses a locked mod on the status line, pointing at the TUI's own `L` key, instead of opening a confirm modal whose action the core gate would then refuse (#143)
- TUI: when a locked mod's lock target no longer appears in the source's version list (removed or archived upstream), the `L` version picker no longer silently pre-selects the newest version with nothing marked — it pre-selects the trailing `unlock` row and notes "locked v\<version\> no longer listed" on it, so the vanished target is signalled instead of papered over (#143)
- TUI: the "Apply N update(s)?" batch modal now marks rows the apply will refuse with the CLI bulk table's own `[locked@<version>]` marker (lock state is now projected onto update rows by both providers), and a locked row's per-mod failure line now says "locked at v\<version\> — unlock or move the lock (L) to update" instead of leaking the core gate's CLI remedy commands (`lmm mod lock ...`) into an interface with its own `L` key (#143)
- `lmm profile import` now converges already-installed mods at import time: a mod installed at a different version than the imported profile records was previously classified as "already installed" and left deployed at the old version until the next `profile apply`/`switch` — it is now scheduled for reinstall at the profile's recorded version (downgrades included), replacing a live older deployment so files the new version doesn't serve are removed, and deploying straight from cache (skipping the download) when the recorded version is already fully cached. A version lock imported from a shared profile therefore takes effect — converged and recorded — without a second command (#138)
- `lmm mod edit` can no longer slip past a mod lock: re-linking a locked mod (`--source`/`--source-id`) previously deleted the locked profile entry and appended a fresh, unlocked one — silently dropping the lock — and `--version` on a locked mod wrote the database row before the profile-side refusal fired, leaving the database and profile silently diverged behind success output. Both shapes are now refused up front, before any state moves — `--version` with the same move-the-lock/unlock remedies the install/update gates give, re-linking with the unlock remedy (moving the lock can't help there, since a re-link would replace the locked entry). `--version` at exactly the locked version (realigning a diverged record with the lock) and metadata-only edits (`--name`/`--author`) still work (#146)
- `lmm verify --fix` re-links now honor a profile-level `link_method`: the two repair paths that re-create a symlink deployment orphaned by a version-record cache re-key (the verified profile's own row, and sibling-profile rows sharing the same cache dir) previously rebuilt their installer from the game-level method only, so a profile whose explicit `link_method` differs from the game's would be "repaired" back to the wrong method — re-introducing exactly the drift verify exists to fix. Each re-link now resolves profile > game > global for the profile it repairs (a sibling uses its own profile's method, not the verified one's) and records the effective method on the repaired row, the same as a deploy would (#152)
- A profile-level `link_method` is now honored during deploys instead of being silently ignored: every path that deploys into (or undeploys from) a profile — `lmm deploy`, `lmm profile switch`/`apply`, install/update/rollback, enable/disable/uninstall, redeploy, and `lmm import` — resolves the link method as profile > game > global default, with an explicit `--method` flag still beating all three. A profile switch spans two profiles and now uses each side's own method: the outgoing profile's for undeploying its mods, the target's for deploying. `lmm status <game>` shows the effective method with a `(per-profile)` marker alongside the existing `(per-game)`/`(global default)` ones (#81). **Upgrade note:** profiles saved before v1.14.1 may carry an unintended `link_method: symlink` line (a save bug wrote it into every profile file); it now takes effect and will silently override a per-game `hardlink`/`copy` setup. If `lmm status <game>` reports an unexpected `(per-profile)` method, delete the `link_method` line from `~/.config/lmm/games/<game-id>/profiles/<profile>.yaml`
- `lmm update rollback` (and the TUI's rollback) of a same-version file-only update no longer leaves the rolled-back-from file's contents deployed beside the restored one: the old and new files share one version-keyed cache directory, so the rollback's replace step deployed their union even after the forward update learned to narrow (#144). The rollback now applies the same member-manifest ownership rules with the file-ID transition reversed — current file IDs back to the pre-update ones — including the compensation path when a mid-rollback write fails. Cache entries from before member manifests existed keep the previous union behavior, silently (#150)
- A file-only update whose version string does not change (an author re-uploads a rebuilt file under the same version label, superseding the old file entry) no longer leaves the superseded file's contents deployed beside its replacement: the old and new files share one version-keyed cache directory, so the replace step deployed their union. Each downloaded file now records which cache members it contributed (a manifest inside its existing completion marker), and the update undeploys members owned solely by the superseded file — members the replacement also ships survive, and the deployed-file tracking rows follow. Cache entries from before this release carry no manifests and silently keep the previous union behavior until their next re-download; a same-version update that fails mid-write compensates precisely, restoring the superseded members and removing the uncommitted replacement's members (#144)
- `lmm install` (CLI and TUI alike) can no longer silently move a locked mod's version: installing a locked mod at any version other than its locked one — an explicit `--version`, or a plain reinstall that would land on a newer latest — is refused up front, before any hook, download, or deploy, with the same move-the-lock/unlock remedies the update gate gives; `ProfileManager.UpsertMod` itself now backstops the invariant by refusing (wrapping `ErrModLocked`) to record a different version onto a locked profile ref, and batch installs (dependency lists and multi-select installs alike) skip a locked mod before removing its previous installation or downloading anything, instead of deploying it and leaving the profile unrecorded. Installing at exactly the locked version (reinstall/repair) still works and preserves the lock (#143)
- Batch installs (multi-select and dependency lists) no longer remove a mod's previous installation before its file list has been fetched: a fetch failure now skips the mod with its existing deployment, cache entry, and database row intact, instead of uninstalling first and then failing (#143)
- Updating a multi-file mod from a source without file categories — every custom source (directory/manifest/api) — no longer pairs the update's replacement file with a stored file by listing order, a coin flip that could retain the stale main file deployed beside its replacement; when categories decide nothing, the primary flag now pairs a new main with the old main and leaves unchanged extras alone. Sources that mark only one file primary per listing (CurseForge) keep the previous list-order pairing when their category labels also fail to match (#144)
- The update no-op backstop — the loud error that stops an update which provably cannot advance the record from looping forever — now distinguishes its two shapes: when every file the source offers under the target version is already installed, the error names the likely source-side file labelling quirk instead of misleadingly suggesting `--file` when there is nothing else to pick; the reinstall/`--file` remedy remains for the case where other files exist. The branch also gains its first test coverage so a refactor cannot silently neuter it (#144)
- File selection for a version offering no primary file now picks the best file by category priority (MAIN, then OPTIONAL, UPDATE, MISCELLANEOUS) instead of whichever file the source happened to list first, in deploy-class flows and the update path alike; listings without categories keep their previous first-listed behavior (#144)
- `lmm update` (and the TUI's update-apply) no longer re-installs the version a mod is already on when the source still lists that version's file entries alongside the new ones and offers no file-supersede mapping — common on NexusMods when an author uploads rebuilt files rather than replacing them in place. File selection now resolves against the update's target version, with the mod's stored file IDs acting only as a tie-break among that version's files; sources that report no per-file versions, or whose file version legitimately differs from the mod-level version, keep the existing primary-file fallback. Previously the update re-downloaded and re-deployed the installed version, recorded that same version back onto the row (leaving `previous_version` equal to `version`), and the next check re-reported the identical update — repeating indefinitely with no error shown

## [1.26.0] - 2026-07-29

### Added

- `lmm mod lock <mod-id> [version]` locks a mod's entry in the current profile to an exact version — with no version argument, locks at the currently recorded version; with one, the version is resolved and validated against the source before the lock is written. Requires a source that can resolve versions; version-less sources are refused with a pointer to `lmm mod set-update --pin`. Locking is a metadata write, not a deploy: convergence happens on the next `profile apply`/`deploy`, and the success message says so when the locked version differs from what's installed. `lmm mod unlock <mod-id>` clears only the lock marker — the recorded version is left untouched, since it's the record, not the lock (#97)
- TUI: `L` on Installed Mods opens an async version picker (fetched from the source) to lock the selected mod or move an existing lock; picking a version confirms immediately, and a locked mod's picker gains a trailing "unlock" entry. The row flags column shows `lck` for a locked mod, outranking `pin` when both apply
- Lock state surfaces everywhere a mod's version does: `lmm list -v` gains a `LOCKED` column (the locked version, or `-`); `lmm mod show` gains an Installed section (version, profile, update policy, lock state); `lmm update`'s table appends `[locked@<version>]` to a locked mod's `POLICY` cell. `--json` output for `list` and `mod show` carries the same information additively (`locked`, `locked_version`); single-mod `lmm update --json` instead reports a refused apply as `status:"skipped", reason:"locked"`
- `lmm update` refuses to apply to a locked mod: a locked mod is still checked ("locked but informed") and reports available updates, but a single-mod `lmm update <id>` on a locked mod is refused with remedies (move the lock or unlock first; `--json` reports `status:"skipped", reason:"locked"`); `--all` and auto-policy mods skip locked mods and report them in a distinct summary line instead of applying to them
- `lmm verify` is lock-aware: a locked mod's version-record mismatch is still reported, but `--fix` refuses to rewrite a locked mod's record (other, unlocked mods in the same run are still fixed) since the record is the lock's target, not drift to repair; when a locked mod's installed version hasn't yet converged to the lock, `verify` prints an informational "lock pending convergence" note instead of treating it as an issue

### Changed

- `--pin` (`lmm mod set-update --pin`) is reframed everywhere — CLI help, TUI, README — as a check-mute rather than a version freeze: it stops `lmm update` from asking the source about a mod, but does not hold a version. To hold an exact version, lock it (`lmm mod lock`). `--pin` remains the only freeze available on sources that cannot resolve versions

## [1.25.0] - 2026-07-29

### Added

- `lmm install --version <version>` installs an exact-match version — archived/old files are searched automatically, no `--show-archived` needed — instead of always latest (closes #93)
- Version→file resolution (`Service.ResolveModVersion`), which resolves a named version to its file(s) whenever the mod's returned files actually carry version info (decided dynamically per call, not by a capability flag) — plus a new `versions` source capability that advertises `mod_files` endpoint support for display (`lmm source list`); mods/sources without file-level version data keep the existing file-ID behavior
- `lmm profile apply` and `profile switch` now converge each installed mod to the profile's recorded version — including downgrades — instead of only healing missing files; `profile import` honors the recorded version for mods it installs or redownloads, with drift on an already-installed mod converging on the next apply/switch

### Fixed

- Deploy-shaped flows (deploy, profile switch/import, `profile apply`) whose stored file ID(s) are gone upstream now heal to the mod's recorded version when that version is still available, instead of always hard-failing; the hard-fail from #95 now fires only when the recorded version itself is gone too, and its error distinguishes "file IDs gone upstream" from "installed files don't match the recorded version" — the latter points at `lmm verify --fix`
- Profile switch replaces a live older deployment when converging a mod to a newer or older version, instead of leaving the stale files on disk alongside the new ones; convergence preserves the mod's `Deployed` flag and update policy
- `lmm update`'s apply step now records the version of the file it actually installed, not the mod's overall "latest" version — `lmm verify` no longer flags a freshly-updated mod as a version mismatch
- `lmm profile apply` no longer fails to converge a version-drifted mod whose previous version's cache entry has been pruned — it now falls back to a plain install instead of erroring with "old mod not in cache", matching `profile switch`

### Security

- Archive extraction now rejects mod archives containing members under lmm's reserved `.lmm-` cache namespace (at any path depth, including members nested under a `.lmm-`-prefixed directory). Such a member could forge another file's cache-completion marker — making the cache-first guard skip a download that never happened and deploy the mod with a genuinely missing file, silently — or hide itself from deployment entirely. Consistent with the existing zip-slip and symlink-containment guards, the extraction fails and names the offending member

### Internal

- File-granular cache verification (`Cache.HasFileIDs`) replaces an existence-only check before deploying from cache, preventing a partial cache entry (missing one or more of a multi-file mod's files) from being silently deployed as if complete. Completeness is tracked with a zero-byte `.lmm-file-<fileID>` marker committed atomically alongside each downloaded file, so the check works for extracted archives (whose cached member names bear no relation to the downloaded file's name) instead of redundantly redownloading them. Cache entries created before this release carry no markers and are redownloaded once

## [1.24.1] - 2026-07-29

### Fixed

- Reinstalling or importing over an already-installed mod no longer silently resets its update policy to `notify` — a policy set via `lmm mod set-update` (`--pin`/`--auto`) now survives every reinstall/import path, since the database upsert preserves the existing policy when updating an existing record (#134)

## [1.24.0] - 2026-07-28

### Changed

- `lmm deploy`, profile switch, profile import, and `lmm profile apply` now **fail a mod** whose stored file ID(s) are no longer available upstream, instead of silently substituting the primary file — the error names the missing file ID(s) and points at the fix ("reinstall the mod or run `lmm update` to adopt the current version"). The failure is per-mod: other mods in the same operation continue. The "using primary file" fallback-warning lines are gone from both CLI and TUI output. Applying an update intentionally keeps the old fallback behavior — falling back to the new version's primary file is correct when a source has pruned the old file IDs (#95)

## [1.23.1] - 2026-07-28

### Fixed

- Database writes (delete/policy/enable/deploy/version/link-method updates on installed mods) no longer misreport a driver failure while checking affected rows as "mod not found" — the error is now returned wrapped with its operation's context instead of being read as zero rows affected (extends the `SetModVersion` fix from PR #128 to the remaining six sites in `internal/storage/db/mods.go`, and aligns that site's error wording with the sweep)

## [1.23.0] - 2026-07-28

### Fixed

- Installed mods now record the version of the file that was actually selected and downloaded, not the mod's overall "latest" version, across every install path (primary install, batch/dependency installs, profile switch, import, and the CLI's batch-install and profile-apply flows) — the mod cache is keyed to match, so a file pinned to an older release no longer gets stamped or cached under a newer version it isn't (#94)

### Added

- `lmm verify` now also checks each installed mod's recorded version against what its stored file ID(s) actually report upstream, surfacing `version_mismatch` (issue, fixable) and `version_unverifiable` (warning, not fixable — reinstall instead) statuses alongside the existing file checks
- `lmm verify --fix` repairs a `version_mismatch` by re-keying the cache entry to the source-reported version, correcting the DB row and active profile record, and re-linking symlink deployments (a blocked rename, when a cache entry already exists under the target version, is left alone and reported via a new additive `note` field rather than clobbered); since the mod cache is shared across profiles, a successful rename also corrects any other profile's stale record for the same mod (DB, profile record, and re-linking if deployed), surfaced as its own output line per profile in text mode or folded into the `note` field in `--json`
- `--json` verify output gains the optional `note` field carrying repair/skip detail — blocked cache renames, sibling-profile repair outcomes, and repair/re-download failure reasons

## [1.22.0] - 2026-07-28

### Changed

- **The last built-in special cases are gone** (#76, PR 2 of 2 — completes the source-registry design):
  - `lmm deploy -s` no longer defaults to `nexusmods` — like search/update, a game's sole configured source is used automatically and several prompt for a choice
  - `lmm import` scan-matching consults **every** configured source that can search (ID-sorted; `curseforge` still sorts first for typical setups) instead of CurseForge only, and reports which source matched; `--id` without `--source` resolves the same way instead of preferring CurseForge
  - `lmm game add` builds its menu from the registered sources instead of a fixed two-item list — sources with a game catalog (CurseForge) get the interactive search flow, everything else (NexusMods, custom sources) gets manual identifier entry; custom sources are now usable at game-creation time. An unauthenticated catalog source fails fast with the standard "authentication required" hint
  - The multi-source game prompt renders registry display names (`Nexus Mods (nexusmods)`)
- **Source listings are scoped to the active game** (#75): `lmm source list` shows the game's configured sources by default; `--all` shows the full registry with an `IN USE` column. With no game configured the full list is unchanged, and broken-definition rows stay visible in every view. The TUI Sources screen scopes the same way — `a` toggles between the game's sources and the full registry, and the panel title says which you're looking at
- `lmm auth status` now distinguishes a stored token whose source no longer declares auth from one whose source isn't registered at all

### Added

- `lmm source list --all --json` rows carry `"in_use": true` for the active game's sources (additive)

### Internal

- `Service.SourcesForGame` — the one game-to-registered-sources intersection, now backing aggregate search, import matching, and both listing surfaces

## [1.21.0] - 2026-07-28

### Changed

- **Built-in sources (NexusMods, CurseForge) now register and describe themselves through the same path custom sources use** (#76, PR 1 of 2). Every fact the CLI used to hard-code about them — display names, API-key env vars, auth setup instructions, live key validation, type labels, capabilities — now comes from the source itself via small optional interfaces; the hard-coded auth/instructions/validation/type-label/capability switches are gone (the remaining identity-aware sites — the multi-source game prompt's display names, deploy's default source, import matching, and `game add`'s menu — are PR 2's normalization scope). Zero configuration changes: legacy env vars (`NEXUSMODS_API_KEY`, `CURSEFORGE_API_KEY`), stored tokens, and `games.yaml` behave exactly as before.
- The interactive `lmm auth login`/`logout` picker now offers **every** registered source that declares auth — including custom sources — instead of only the two built-ins (previously custom sources had to be named explicitly)
- `lmm auth status` lines now render the display name alongside the ID, e.g. `Nexus Mods (nexusmods): not authenticated (run: lmm auth login nexusmods)`, uniformly for built-in and custom sources. Auth display names generally now come from each source's own `Name()` — NexusMods renders as "Nexus Mods" (previously "NexusMods") in login/logout/picker text

### Internal

- New `internal/source` metadata interfaces (`EnvKeyProvider`, `KeyValidator`, `AuthInstructionsProvider`, `GameCatalog`, `TypeLabeler` + `TypeLabelOf`) with conformance tests across all five source types; built-ins now declare capabilities explicitly instead of relying on the all-true default
- One registration pipeline in `cmd/lmm/root.go`; the hand-synced CLI/TUI type-label switches are gone

## [1.20.1] - 2026-07-27

### Added

- **CI test workflow** (`.github/workflows/test.yml`): gofmt check, `go vet`, and the full suite under the race detector on every push to main and every pull request — the repo previously had no CI test run at all. A matching `make test-race` target runs the same suite locally.

### Internal

- Coverage backfill across the layers furthest from their targets — all tests pin existing behavior, no production code changed: domain enum parsers/String methods (78→100%), linker deploy lifecycle incl. Undeploy/IsDeployed for all three strategies, CleanupEmptyDirs, and the deliberate deploy-over-existing-file overwrite semantics (40→77%), storage config Save/DeleteGame/ListProfiles/DeleteProfile (66→80%), db mutation setters (69→76%), cache Size (63→81%), steam VDF parsing/library discovery/DetectGames (41→81%), CurseForge and NexusMods client endpoints via mocked HTTP (63→84%, 68→74%), and `lmm conflicts` driven end-to-end through the real command path incl. `--json`
- Fixed a latent test-order landmine: a cancelled context cached on the Cobra root-command singleton by one test poisoned every later bare `Execute()` call in the binary, failing context-sensitive tests under `-shuffle=on`

## [1.20.0] - 2026-07-27

### Added

- **Man pages are now generated from the CLI's own help text** via a hidden `gen-man` command and a `make man` target, with a test that fails whenever help text changes without regeneration — ending the drift that had left every hand-written page stale since v1.0. Coverage grew from 13 pages to 49: first-ever pages for `tui`, `import`, `source`, `auth`, and `uninstall`, plus every `game`/`mod`/`profile`/`update` subcommand and the shell-completion family
- Release archives now include the man pages (`docs/man/man1/`)

### Changed

- **CLI help-text overhaul** — `--help` output was rewritten to man-page quality across every command: `lmm --help` now documents exit codes (0 success, 1 error, 2 cancelled) and file locations; `lmm update --help` documents both `--json` document shapes (bulk check vs single-mod/rollback) and the non-zero exit on a failed check; `conflicts` documents the winner/stale output and JSON fields; `verify` documents all five output states and the exact `--fix` scope; `auth login`/`logout` document that any source declaring `auth` works positionally (the interactive picker remains built-ins only); `install` documents the `--id`+`--file` skip-search workflow, `--show-archived`, and `--version`'s not-yet-supported guard; `search` documents `--category`/`--tag` source support honestly; the root `--json` flag help now lists all eight JSON-capable commands

### Fixed

- Command tests no longer corrupt the shared command tree: ~36 test sites reparented real subcommands onto throwaway roots without restoring them (Cobra's `AddCommand` never detaches from the previous parent), and one test permanently poisoned `status`'s cached inherited-flags set with a duplicate `--game` flag — invisible until the man-page generator became the first consumer to depend on the tree's integrity

### Documentation

- README currency pass: TUI search described as infinite scroll (`n`/`p` skip a paneful — the old "next/previous page" wording predated v1.19.0), dependency auto-install marked shipped in the Roadmap (contradicted the Features list), architecture tree gained `internal/tui` and the `custom`/`steam`/`httpclient` source packages, the commands table gained `import`, `game add`, `game detect`, `mod edit`, and the missing `install`/`uninstall` flags, plus new Exit Codes and Import sections, file locations for `sources/*.yaml` and the `downloads/` staging dir, and a CHANGELOG link
- `docs/configuration.md`: profile-level `link_method` is now documented as parsed-but-inert at deploy time (#81) — effective precedence is game-level then global — and the file gained a Custom Sources cross-reference it always claimed to have
- The original PRD moved to `docs/plans/archive/2026-01-22-PRD.md` with a historical banner (OAuth, a Settings screen, and its `games.yaml` schema never shipped as written)
- Repo `CLAUDE.md` architecture/file-location guidance brought current

## [1.19.0] - 2026-07-27

### Changed

- **TUI search results are now a single infinite-scrolling list — no pages.** Each search asks every source for enough rows to fill the visible results pane by itself (minimum 10 — never fewer than before — capped at 50 per source, a limit verified against the live NexusMods API); scrolling near the end quietly fetches more from every source, and keeps doing so until each is exhausted. The footer reports load status instead of a page number — "X of Y loaded" for a single source with a known total, otherwise "X loaded · more available" while more can still be fetched or "all X shown" once everything has been. `n`/`p` jump a paneful at a time instead of turning a page. The per-search fetch size is fixed at submit time; a terminal resize applies from the next search.

## [1.18.3] - 2026-07-27

### Fixed

- **Test isolation (cmd/lmm)**: a package-level `TestMain` now points the `configDir`/`dataDir` globals at throwaway temp dirs before any test runs, so every command test is hermetic in any run order or `-run` subset (#115). Previously, tests that never assigned the globals (e.g. `TestInstallCmd_NoGame`, `TestListCmd_NoGame`) fell back to the user's real `~/.config/lmm` — a subset run resolved the real default game and stalled on the interactive source-picker prompt; the full suite passed only through accidental ordering. `TestPackageGlobals_HermeticDefaults` pins the guard.

## [1.18.2] - 2026-07-27

### Fixed

- `lmm install --version` now fails with a clear "not yet supported" error (pointing at the version-resolver work) instead of silently installing the latest version
- The TUI nav bar now compresses at narrow widths — number cells, with the current screen keeping its label, then numbers only below ~44 columns — instead of truncating the last screen entry into an ellipsis at 80 columns

## [1.18.1] - 2026-07-27

### Fixed

- **Directory sources now follow symlinked mod directories.** A symlinked subdirectory used to be silently invisible to both the scanner and cache ingest (classified by raw dirent type, which never reports "directory" for a symlink); both now stat through the link. A symlink cycle is now caught and reported as a clear error instead of recursing forever, and a stat failure other than a dangling symlink propagates instead of silently dropping the entry from the scan.
- Mod names ending in a real "V" are no longer over-trimmed — `ModV-1.0` now parses as name `ModV`, version `1.0` (previously the trailing "V" was eaten and the name became `Mod`); names like `MyMod-v1.0`, where the "v" genuinely belongs to the version, still parse as before
- `ModInfo.xml` is now found case-insensitively, in both directory and archive mods
- Dependency-resolution failures during install planning (a real fetch failure, not just a source lacking the capability) are now surfaced as warnings — on the CLI's stderr and in the TUI's install-confirm modal — instead of silently degrading to "no dependencies". **A plan carrying only these warnings (no actual dependencies or conflicts) now prompts for confirmation unless `--yes` is given**, where it previously proceeded silently
- Local imports now cache files under their declared filename rather than the local path's own (often temporary) basename
- A failed staging copy during install/import no longer leaves stale `.staging` debris behind in the mod cache
- `lmm source list` reports each broken source definition once instead of twice (an init-time warning plus its own table row said the same thing twice); `--json` now emits `[]` for an empty result instead of `null`
- All-sources search pagination now stops at genuine exhaustion instead of offering a reachable, empty next page from summed per-source totals, and the aggregate pager no longer shows a misleading total-page count
- A game whose configured sources all lack search capability now says so plainly, on both the CLI and the TUI, instead of the generic "No mods found."; under `--json` the notice goes to stderr so stdout stays a single document
- Demo (`--prototype`) search now matches this same honesty: canned all-sources pages carry the same exhaustion/attempted signals as a real search, and show a sample per-source warning
- The TUI's and CLI's no-sources-configured wording now match exactly

## [1.18.0] - 2026-07-26

### Added

- **The Installed Mods list now marks mods actually updated this session.** A `*` appears in the flags column next to any mod a confirmed update apply brought current — not merely checked — distinguishing "confirmed and applied" from "checked, still pending, or failed partway through." A pinned mod that was also updated this session (a manual apply overrides the policy for one run) shows both flags together as `pin *`
- Every dashboard layout now shows a real **Last Deploy** row instead of a placeholder: never-deployed profiles read "never," a recent deploy reads as a relative age ("3h ago"), and anything a week or older falls back to an absolute date
- `lmm status <game>` gains a `Last Deploy:` line in its text output, and `--json` gains a matching `last_deploy` field — omitted entirely (not `null`) when the profile has never been deployed, so existing consumers that don't expect the key see no change

### Fixed

- **The root `--no-color` flag (and the `NO_COLOR` environment variable) now reach the TUI.** Previously only the CLI's plain-text output honored them; launching the TUI ignored both and always rendered in color
- The nav bar now marks the current screen with a `•` marker instead of relying on color alone to distinguish it — needed once `--no-color` could actually disable color in the TUI, but also a plain accessibility improvement on its own
- A picker choice (e.g. the update-policy picker) made while another action is already running, or while a confirmation modal is pending, used to vanish with no feedback; it now leaves a muted "busy — choice ignored" status hint, as long as nothing more important already owns the status line
- An input modal's error message (e.g. an invalid profile name) now clears the moment the user resumes typing, instead of lingering stale against newly-typed text until the next submit attempt
- Two status-line writes — profile purge's "nothing to do" and profile delete's "refused" messages — could previously stomp a running action's own live status or progress text; both now defer to it instead of overwriting it

## [1.17.1] - 2026-07-26

### Fixed

- **The TUI no longer overflows short terminals.** Every screen now renders within its height budget at any terminal size: dashboard panels clamp overflowing content with a `+N more` tail, and the installed-mods, profiles, sources, search-results, and conflicts lists scroll to follow the selection (with `↑/↓ N more` indicators) instead of letting the highlighted row walk invisibly off-screen while the detail pane kept tracking it
- Dashboard panels and the sources list truncate over-wide rows instead of letting long game, profile, or mod names wrap and silently grow the rendered height past the terminal
- Error and empty states (load failure, search failure, zero search results) clamp long error messages and queries for the same reason
- The search detail pane fits its fixed fields into short panes, tolerates pathologically narrow panes with zero-width value columns, and styles an `installed` status to match the results list

### Changed

- The TUI snapshot harness (`UPDATE_TUI_SNAPSHOTS=1`) now captures every screen — including a populated search — instead of only the dashboard, under the new naming `{theme}-{screen}-{width}x{height}.ansi`

## [1.17.0] - 2026-07-26

### Fixed

- **`lmm update <mod-id>` and `lmm update rollback <mod-id>` now honor `--json`.** Previously every success path printed human text and only failures produced JSON, so piping the single-mod form to a parser worked only when the command failed. Both forms now emit exactly one JSON document on stdout: `{mod_id, name, from_version, to_version, changelog, status, reason}` with `status` one of `updated`, `up_to_date`, `skipped` (with `reason` `pinned` or `local`), `available` (dry-run), or `rolled_back`. The `changelog` field is HTML-stripped like the human output, but untruncated. Failures still write no document — the error is emitted as the sole `{"error": ...}` with a non-zero exit, preserving the one-document invariant. This is distinct from the bulk `lmm update --json` document, which reports a check over many mods rather than a single applied event
- `--json --verbose` no longer leaks download/progress text onto stdout ahead of the JSON document in the single-mod update and rollback paths
- `lmm update --json` with zero installed mods printed `No mods installed.` as plain text. The bulk form now emits the standard check document with an empty `updates` array; the single-mod form reports the standard mod-not-found error as JSON. Plain-text output is unchanged

## [1.16.0] - 2026-07-25

### Changed

- **`lmm update` now exits non-zero when the update check itself fails.** Previously it exited 0 even when every source errored, so a script could not tell a completed check from a failed one. Partial results are still printed and auto-updates are still applied before the non-zero exit — the change is the exit status, not the work done. **If you script `lmm update`, check your error handling**: a run that used to appear to succeed during an outage will now fail
- `lmm update --json` gains an `error` field, set only when the check failed. The document is otherwise unchanged, and stdout still contains exactly one JSON document in the failure case

### Added

- `lmm update --json` gains a `skipped` block reporting how many installed mods were never checked, by reason (`pinned`, `local`). An empty `updates` array previously meant both "nothing to update" and "nothing was looked at", with no way to tell them apart
- The TUI's update check reports skipped mods as a `Not checked:` warning, matching what the CLI prints

### Fixed

- `lmm update` printed "All mods are up to date" when every installed mod was a local import. Local mods have no remote source and are filtered before any source is queried, so nothing was ever compared. Local and pinned mods are now reported separately, since the remedies differ — a pin can be lifted, a local mod can never be checked
- `lmm update <mod-id>` failed with "mod X not found in profile Y" (exit 1) for a local mod that _was_ in the profile. The lookup matched on source as well as ID, and the resolved source is the game's configured remote, so a local mod could never match. It now says the mod is local and exits 0; a mod genuinely absent from the profile is still an error. A mod belonging to a different configured source now names the source to retry with, instead of claiming the mod is missing
- The TUI reported a local mod as "already up to date" for the same reason, and now says it is local

## [1.15.0] - 2026-07-25

### Added

- `lmm list --verbose` gains a `POLICY` column showing each mod's update policy (`notify`, `auto`, or `pinned`), and `lmm list --json` gains a matching `update_policy` field. The field is emitted unconditionally rather than gated on `--verbose`, since a JSON consumer has no other way to see that a mod is held back from updates
- The TUI's Installed Mods rows gain a flags column marking pinned mods with `pin`. Pin state was previously discoverable only by opening the `P` policy picker on each mod one at a time

### Fixed

- `lmm update <mod-id>` reported a pinned mod as "already up to date". Pinned mods are filtered out before their source is ever queried, so no version comparison happened and a newer version may well have existed. It now says the mod is pinned, at which version, and names the command that unpins it. The TUI's equivalent message had the same problem and the same fix
- `lmm update` no longer prints "All mods are up to date" when every installed mod was skipped as pinned, and now reports how many pinned mods it skipped when there were any

## [1.14.1] - 2026-07-25

### Security

- `~/.local/share/lmm/lmm.db` is now created owner-only (`0600`), along with its `-wal`/`-shm` sidecars, and the data directory is created `0700` instead of `0755`. The database stores auth tokens in plaintext, and SQLite created it world-readable under a typical umask, so any other local user could read your NexusMods/CurseForge API keys. Permissions are re-applied on every open, so existing installs are fixed the next time `lmm` runs. This is a mitigation, not a replacement for encrypting the tokens themselves (#79)

### Fixed

- Profiles no longer accumulate a `link_method: symlink` line the user never set. `LinkMethod.String()` never returns an empty string, so the `omitempty` tag never fired and every profile mutation (create, add/remove mod, reorder, set-default, import) rewrote the file with a phantom override; exported profiles carried it to other machines too. The key is now written only when explicitly set. Note the profile-level override still has no effect at deploy time — that remains #81
- Downloads and local-archive imports are staged under `~/.local/share/lmm/downloads` (the location the PRD has always specified) instead of `$TMPDIR`. `/tmp` is tmpfs on most modern distributions, so large mod archives were being downloaded and extracted in RAM

### Changed

- Dropped the `mod_cache` table (schema v11). It was created by the v1 migration and never read or written — the cache is keyed entirely by directory layout, with no database mirror to fall out of sync

## [1.14.0] - 2026-07-24

### Added

- TUI conflicts screen (key `6`, in the screen-jump/nav rotation): lists every game-directory file two or more enabled mods provide, with the current owner, the load-order winner, every other providing mod, and a "stale" marker when the deployed copy no longer matches the winner; selecting a row shows a resolution hint. `D` deploys the active profile directly from this screen. The Dashboard's conflict count now reflects real detection instead of a placeholder
- Inline load-order reorder on Installed Mods (`J`/`K`, also `ctrl+down`/`ctrl+up`): swaps the selected mod with its neighbor and persists the new order immediately; a hint reads "order changed — deploy (`D`) to apply" until you redeploy, and the list itself now renders in load order
- Update rollback in the TUI (`<` on Installed Mods), behind a confirmation prompt — the TUI equivalent of `lmm update rollback`; a mod with no previous version is refused on the status line instead
- A changelog viewer in the update flow (`v` on the apply-updates confirmation modal): opens a scrollable overlay of the selected update's changelog, or a "View changelog" picker naming each `<mod> <from> → <to>` first when several updates are pending. `v` also works on an Installed Mods row after a check, showing that mod's changelog from the most recent results
- A per-mod "update results" overlay after a confirmed update batch finishes: one `✓ <mod> <from> → <to>` or `✗ <mod>: <error>` line per update, mirroring the CLI's per-mod output, so the applied set is visible beyond the status-line count
- Profile import in the TUI (`I` on Profiles): a "path to YAML" input, followed by a categorized preview (new mods, already-installed mods, overwrite/cross-game warnings), then downloads and installs as needed with an optional immediate switch to the imported profile
- Profile export in the TUI (`E` on Profiles): a "path to save" input prefilled with a default filename; refuses to overwrite an existing file
- `lmm conflicts` now prints a `Winner:` line per conflict (the load-order winner, with a stale marker and redeploy hint when it disagrees with the current owner); `--json` gains matching `winner`/`stale` fields
- Info overlays (deployed-files panel, changelog viewer) now scroll, instead of clipping content taller than the panel
- Core: `GetProfileConflicts`, `ApplyRollback`, `PlanImport`/`ApplyImport`, `CleanChangelog`, and `OrderByProfile` extracted into `internal/core` so the TUI and CLI share the same logic; CLI behavior is unchanged (byte-identical output) aside from the `conflicts` sorting/`Winner:` additions above

### Changed

- Multi-mod deploy paths (profile apply/switch, purge order) now iterate mods in profile load order deterministically, instead of an unspecified order — reordering and redeploying now reliably flips which mod wins a file conflict. This is a user-visible behavior change for any profile with unresolved file conflicts
- `lmm conflicts` output is now sorted by file path

### Fixed

- `lmm conflicts` could never actually detect a conflict: ownership was tracked in a single-owner deployed-files table, so a per-mod "which files does this mod provide" query could never collide with another mod's. Conflict detection now sources each enabled mod's provided files from its cache manifest, while ownership still comes from the deploy records

## [1.13.1] - 2026-07-23

### Security

- Profile names and game IDs are now validated before any filesystem access: values that are empty (or whitespace-only), contain path separators, or contain ".." are rejected with a clear "invalid profile name" / "invalid game ID" error. Previously a name like `../../../evil` passed to `lmm profile create`/`delete` — or a crafted `name`/`game_id` in a `profile import` file — would be joined into the profile path unchecked, letting profile save/delete write or remove `.yaml` files outside `~/.config/lmm/games/<game>/profiles/`. The guard lives in the storage layer, so the CLI, TUI, and profile import all share it.

### Fixed

- TUI: the new-profile modal now rejects names containing path separators or `..` inline (matching the config layer's validation), instead of only surfacing the failure after submit

## [1.13.0] - 2026-07-23

### Added

- TUI purge behind a confirmation view (`X` on Dashboard/Installed Mods), streaming per-mod progress via the shared `core.PurgeProfile` flow (#61); an empty profile short-circuits with a "no mods installed" message. The `purge --uninstall` variant deliberately remains CLI-only
- Per-mod deployed-files panel (`f` on Installed Mods), listing the files a mod has placed in the game directory
- Update-policy editing (`P` on Installed Mods): a notify/auto/pin picker with the mod's current policy marked
- In-TUI game switcher (`g`, any screen): pick from configured games to rebind the session (data providers, active profile, sources) and reload; `--prototype` demo mode gets a second canned game to switch to
- Profile create (`c`) and delete (`d`) on the Profiles screen; delete refuses the active profile, and create validates duplicate names inline
- Help overlay (`?`) restructured into per-screen key groups, with the current screen's group listed first and a height-capped "+N more" tail; the Dashboard's `enter` action is now documented

### Fixed

- TUI: an in-flight data load racing a game or profile switch could momentarily repopulate the screen with the prior game's or profile's data; loads are now generation-checked so a stale load can no longer overwrite a newer one

## [1.12.3] - 2026-07-23

### Fixed

- Flagless CLI commands now operate on the game's active profile (the one set by `lmm profile switch`) instead of always using the profile literally named "default" (#66). Affected every command that takes `-p/--profile`: `list`, `deploy`, `install`, `uninstall`, `update`, `update rollback`, `mod enable/disable/set-update/files/edit`, `import`, `search`, `conflicts`, `verify`, `purge`, and `profile reorder`. An explicit `-p` still wins, and a fresh setup with no profiles keeps the "default" convention. The TUI already resolved the active profile correctly and now shares the same resolver.

### Changed

- `--profile` flag help text now uniformly reads "(default: active profile)"; several commands previously documented the literal "default" fallback

## [1.12.2] - 2026-07-23

### Fixed

- Profile switch: enabling a mod whose install record lives under a different profile (reachable via `profile export`/`import` or hand-edited profile lists) now creates the record under the target profile, so a later switch away undeploys it — previously the files were deployed but invisible to the target profile, leaving an orphaned deployment and a spurious "failed to update … mod not found" warning (#60)

### Removed

- Dead code: `ProfileManager.Switch` and its rollback helpers — an older, unused second switch implementation superseded by `PlanProfileSwitch`/`ApplyProfileSwitch` (#60)

## [1.12.1] - 2026-07-23

### Changed

- Core: the `lmm purge` command's logic is extracted into `core.PurgeProfile`, and `deploy --purge` and `lmm purge` now share a single purge implementation (previously three slightly-diverged copies, one dead) — CLI output is byte-identical, pinned by output-fidelity tests (#61). This also readies purge for the TUI's Phase 6 confirmation view (#37).

### Fixed

- `lmm purge` and `lmm deploy --purge` now honor cancellation (Ctrl-C) between mods and report the partial result, instead of always purging the full list

### Removed

- Dead code: `purgeDeployedMods` (superseded by the core deploy flow in v1.11.0, zero call sites since)

## [1.12.0] - 2026-07-21

### Added

- TUI install-from-search (`i`): plans the selected search result first (files, resolved dependencies, conflicts, size), then confirms and streams download/extract/deploy progress; a successful install re-runs the current search so the result's installed marker updates immediately; unlike the CLI, a file conflict never blocks the TUI's single confirmation — it auto-proceeds and reports each overwritten file as a warning instead; a dependency-having install whose PRIMARY mod fails reports the shortfall (e.g. "Installed 1 of 2 mod(s)") instead of a false "Installed" success
- TUI update checking and batch apply (`u` on Dashboard/Installed Mods): checks every checkable installed mod, then confirms and applies all eligible updates sequentially with per-update download progress; the Dashboard's Updates count populates with the real number after a check
- Profile switches that need mods not yet installed now work from the TUI instead of being refused — the confirmation modal discloses what will be downloaded, and confirming downloads and installs them as part of applying the switch
- Core: install/update flows extracted (`PlanInstall`/`ApplyInstall`/`ApplyUpdate`) so the TUI and CLI share the same logic; CLI behavior preserved byte-for-byte, including the file-conflict overwrite prompt's exact position (fires after download/extract, before deploy) and decline behavior (one negligible exception: a rare batch-install checksum warning lost its leading indent)

### Changed

- TUI: quitting while an action is running now cancels it immediately but waits for the current step to finish (bounded to a few seconds) instead of exiting mid-download
- TUI: a capability gap or missing authentication now names the CLI command appropriate to the action that hit it (install vs. update), instead of always suggesting install
- TUI: hook configuration is now cached for the session and re-read on profile switch or restart; previously every action re-read hooks.yaml and config.yaml, so hand-edits mid-session had no effect until explicitly refreshing

### Fixed

- `lmm mod disable --verbose` again prints the undeploy-failure warning, dropped in v1.11.0's extraction of `DisableMod`, restored byte-identical to its pre-1.11.0 wording
- `lmm mod enable`/`lmm mod disable --verbose` no longer drop diagnostics accumulated before a later fatal error (e.g. an undeploy warning followed by a database failure)
- TUI: a data race between an in-flight search and a completing profile switch, both reading/writing the session's active profile concurrently
- TUI: a profile switch that partially fails while downloading mods now surfaces the per-mod failures as warnings instead of reporting a plain "Switched to X" success

## [1.11.0] - 2026-07-20

### Added

- TUI mutating actions behind confirmation prompts: enable/disable (`e`), uninstall (`x`), and deploy (`D`) on Installed Mods (`D` also works from the Dashboard), and profile switch (`enter` on a non-active Profiles row) with a plan preview — every action opens a confirmation modal (`y`/`enter` confirms, `n`/`esc` cancels) and reports its outcome on a status line, including a warning count when the underlying flow reported any
- `--prototype` mode demos all of the above against simulated data, including a canned profile that demonstrates the profile-switch needs-downloads refusal
- Core: mutation flows extracted from the CLI into `internal/core` (`EnableMod`/`DisableMod`, `UninstallMod`, `DeployProfile`, `PlanProfileSwitch`/`ApplyProfileSwitch`) so the TUI and CLI share the same logic; CLI behavior is unchanged

### Changed

- `lmm deploy --purge`'s per-mod `uninstall.before_each` hook-skip diagnostic now prints to stderr with the mod's name attached (was an unattributed line on stdout) — the one intentional CLI output change from this phase's core extraction

### Fixed

- TUI quit now cancels any in-flight search or action context instead of leaving it running past program exit (#42 lifecycle item)
- TUI: screen-cycling (`tab`/`shift+tab`, `↑↓`/`h`/`l`, and the direct screen-jump keys) no longer auto-focuses the search input when it lands on Search — only `/`, `3`, and selecting "Search Archives" from the Dashboard menu do (all three are explicit search intent); `Esc` still blurs. Corrects 1.10.0's Changed entry below, which described the old always-focus behavior as intentional
- TUI: Installed Mods and Profiles rows no longer misalign on long names — both now derive proportional column widths from the panel width and hard-truncate every field instead of a fixed-width column with no truncation
- TUI: the footer's opaque `e/x/D: mutate` hint is now explicit (`e: enable/disable · x: uninstall · D: deploy`), and no longer risks the footer line overflowing the terminal by one row on narrow widths

## [1.10.0] - 2026-07-18

### Added

- Aggregate search: `lmm search` without `--source` now queries every source configured for the game concurrently, with per-source failures reported as warnings
- Search results (CLI and TUI) show each mod's source; the TUI search defaults to "All sources"
- TUI Sources screen (key `5`) mirroring `lmm source list`

### Changed

- TUI: every entry path into the Search screen (`3`, tab-cycling, the dashboard menu, and `/`) now focuses the search input immediately, so typing can start right away; `Esc` unfocuses it so screen-level keys (`s` source cycling, navigation, `n`/`p` paging) work again

### Fixed

- TUI search: a long per-source warning on a zero-results all-sources search no longer wraps inside the results panel and grows it past its height budget
- `lmm search --limit -1` (or any negative value) no longer panics; a non-positive `--limit` now shows all results instead of crashing (or, for `0`, silently returning none)
- `lmm search --limit N` now requests `N` results from each source instead of always requesting a source's internal default (20) and only truncating downward; a query with more than 20 true matches was previously stuck at the first 20 with no way to see more, regardless of `--limit`

## [1.9.0] - 2026-07-15

### Added

- API source type: describe a GET+JSON REST API declaratively (endpoint templates + dot-path mappings) and use it as a mod source — search, install (including install-by-ID-only definitions), and update checks
- `lmm source validate --probe` — live smoke test for a definition (directory scan, manifest fetch, or API call; `--id` probes get_mod for search-less API definitions)

### Fixed

- API source search and file-listing endpoints no longer error on a JSON `null` list (e.g. `{"results": null}`, the standard zero-hits shape for Go-backed APIs) — it's now treated as an empty result set

## [1.8.0] - 2026-07-15

### Added

- `lmm auth status` reports auth-capable custom sources (stored token or env var, masked)
- `lmm auth status` lists stored tokens whose source is no longer registered (e.g. a removed custom-source definition file), with a `lmm auth logout` hint to remove them

### Fixed

- `lmm auth logout` works for sources whose definition file was removed
- Update checks translate installed mods to each source's mapped game ID (fixes NexusMods update checks for games whose lmm ID differs from the Nexus domain)
- Remote manifest fetches are bounded by a 30-second timeout and no longer block other operations on the same source

### Security

- Header- and query-mode manifest API keys are only sent to file downloads on the manifest's own scheme+host (a manifest pointing files at a third-party CDN no longer leaks its key there, in either form)
- Header-mode API keys are stripped before a file download follows a redirect off the original request's scheme+host (Go's HTTP client otherwise forwards custom headers across redirects)
- `maskAPIKey` fully masks keys of 8 characters or fewer instead of 7, so short keys no longer expose most of their characters

### Changed

- Download checksums (MD5 + SHA-256) are computed in a single streaming pass

## [1.7.0] - 2026-07-14

### Added

- Manifest source type: publish a JSON/YAML mod list (https URL or local file) and use it as a full source — search, install, within-source dependencies, and update checks
- Declared `sha256` checksums in manifests are verified on download
- API-key authentication for custom sources (`auth.api_key` in the definition; `LMM_<ID>_API_KEY` env var or `lmm auth login <id>`)

### Fixed

- **Manifest source installs no longer orphaned**: mods found through a source mapped to a non-empty, different value under a game's `sources:` block (the README-documented pattern for manifest `game_ids` filtering, e.g. `my-repo: skyrim`) now save with the correct game ID, so `lmm list`/`update`/`uninstall` can see them after install
- **Query-mode API keys no longer leak into error output**: a manifest source or file download using query-string authentication (`auth.api_key.in: query`) could print the API key in plain text as part of a network-failure error message; failures now redact the key
- **Honest `lmm auth login` feedback for custom sources**: custom sources no longer show a fabricated "Validating... done" (they have no generic validation endpoint) — they now report "Stored (validated on first use)." instead. The rejection for an unrecognized source name now also mentions that a registered custom source with auth declared is an option

## [1.6.0] - 2026-07-14

### Added

- `lmm source list` — list built-in and user-defined mod sources
- `lmm source validate <file>` — validate a user-defined source definition
- User-defined source definitions loaded from `~/.config/lmm/sources/*.yaml`
- Directory source type: use a local folder of mods as a first-class source

### Fixed

- **Directory source installs no longer orphaned**: mods found through a directory source mapped with the README-documented empty value (`sources: {my-mods: ""}`) now carry the correct game ID end-to-end, so `lmm list`/`update`/`uninstall` can see them after install
- **`lmm source list` shows every definition**: a definition whose source fails to construct (e.g. missing directory path) now shows as an `error` row instead of being silently dropped, and a definition colliding with a built-in source ID no longer relabels the built-in's row — the built-in stays `built-in` and the collision gets its own `error` row
- **Hidden entries ignored by directory sources**: dot-prefixed entries (`.git`, etc.) under a directory source's path are no longer listed as installable mods
- **Local file ingest restricted to directory sources**: a `file://` download URL is now only trusted from directory sources, closing a path where any other source returning `file://` could pull arbitrary local files into the cache
- Negative `page` values in directory source search no longer panic; they clamp to the first page
- **Archive mods now read embedded `ModInfo.xml` metadata**: a `.zip`/`.jar` mod in a directory source (e.g. `donovan-aio.zip` containing `donovan-aio/ModInfo.xml`) now resolves name/version/summary/author from the archived `ModInfo.xml` instead of saving an empty record with only a filename-derived name and version

## [1.5.0] - 2026-07-13

### Added

- **TUI search** — Real source search from the TUI: query input with focus-aware key routing (`/` to focus on the Search screen, or jump there from other screens; `enter` to execute the search), per-source search with cancellation (stale results discarded), installed-result markers, detail panel for the selected result, CLI-parity pagination (`n`/`p` navigation), source cycling (`s` to switch between a game's configured sources), and first-class auth-required guidance (displays the login command needed).

### Changed

- **DataProvider boundary v2** — Single-fetch `Overview`, `Sources`, and `Search(ctx, source, query, page)` replace the Phase 3 read-only methods; the Bubble Tea program context now threads into all data loads.

## [1.4.0] - 2026-07-13

### Added

- **`lmm tui` (read-only)**: The TUI now runs against real app data — Dashboard, Installed Mods, and Profiles views load the configured game, default profile, and installed mods through a narrow read-only provider. Search shows an honest placeholder until source search is wired in. `--prototype` remains as a side-effect-free demo mode.
- **TUI theme snapshots**: Committed ANSI captures of all four themes at 80x24 and 120x36 under `docs/assets/tui/`, regenerable with `UPDATE_TUI_SNAPSHOTS=1 go test ./internal/tui -run TestGenerateThemeSnapshots`.
- **Dashboard enter-to-open**: The dashboard menu is defined once, and Enter opens the selected entry's screen.

### Changed

- **TUI internals**: Key handling flows through the shared KeyMap; status text styles live in the theme; the TUI uses the terminal's alternate screen.

## [1.3.10] - 2026-04-26

### Changed

- **TODO comments converted to issues**: Two in-code `TODO`s now have tracking issues — fuzzy-matching for local imports (#27) and CurseForge batch-mods endpoint (#28). The comments themselves are reduced to one-line pointers
- **Test isolation fix**: `TestUninstallCmd_NoGame`, `TestSearchCmd_NoGame`, `TestUpdateCmd_NoGame`, and `TestUpdateRollbackCmd_NoGame` now set `configDir = t.TempDir()` so they pass in isolation. They previously relied on a previous test having already pointed `configDir` away from the user's real `~/.config/lmm`, which would otherwise leak a default-game and skew the assertion

### Notes

- Phase 6b (move per-command flag globals to scoped structs) is intentionally skipped: the existing `var (foo string; bar bool)` + `init()` binding pattern is idiomatic Cobra, and converting it would not fix the persistent root-flag globals (`gameID`, `configDir`, `dataDir`, etc.) that drive the test-isolation issue. Better to revisit if a concrete pain point appears

## [1.3.9] - 2026-04-26

### Changed

- **Shared source HTTP client**: New `internal/source/httpclient` package centralises auth-header injection (`apikey` for NexusMods, `x-api-key` for CurseForge), 401 → `domain.ErrAuthRequired` mapping, JSON decoding, and bounded error-body reads. Both source clients now compose this rather than each maintaining their own near-identical `doRequest`. Source-specific status handling (CurseForge's 403 disambiguation and 404 → `ErrModNotFound`) plugs in via the optional `ErrorMapper` callback. Recorded-response tests pass without modification, proving no behavioural change

## [1.3.8] - 2026-04-26

### Changed

- **`runInstall` decomposed**: 608-line `runInstall` split into focused helpers — `searchAndSelectMods` (paginated interactive search), `selectInstallFiles` (file picker with --file / --yes / interactive), `downloadSelectedFiles` (per-file progress + checksum), and `confirmInstallConflicts` (overwrite prompt). Top-level `doInstall` now orchestrates these in ~320 lines instead of inlining everything
- **Profile commands wrapped with `withGameService`**: All 9 `runProfileX` functions (list, create, delete, switch, export, import, sync, reorder, apply) now extract their bodies into `doProfileX` helpers and run through the lifecycle middleware. Resolves the `install.go` / `profile.go` carve-out left over from Phase 1b
- **Game subcommands**: `runGameSetDefault` now uses `withService`. Saves the same `requireGame` / `initService` boilerplate that was already removed from the rest of `cmd/lmm/`

### Notes

- Deferred: moving profile-switch / -apply / -import orchestration into `internal/core/profile.go`. Even after the wrap, the three biggest profile commands are 228–271 lines because they interleave UI prompts with state mutation; a clean split needs a designed core API and is out of scope for this phase

## [1.3.7] - 2026-04-26

### Changed

- **Service boundary tightened**: Removed the `Service.DB()` and `Service.Registry()` accessors that let CLI code reach into `*db.DB` / `*source.Registry` directly. Replaced 28+ external usages with focused service methods (`SaveInstalledMod`, `DeleteInstalledMod`, `SetModEnabled`, `SetModDeployed`, `GetDeployedFilesForMod`, `GetFileOwner`, `GetFilesWithChecksums`, `SaveFileChecksum`) and factory helpers (`Service.GetInstaller`, `Service.NewInstallerWithLinker`, `Service.NewProfileManager`, `Service.NewUpdater`). `GetFileOwner` now returns `(sourceID, modID string, found bool, err error)` so callers no longer import the `db` package
- **`core.DeployedFile`**: New service-boundary view type returned by `GetFilesWithChecksums`, replacing the leaked `db.FileWithChecksum`

## [1.3.6] - 2026-04-25

### Changed

- **Composite errors stay inspectable**: Multi-cause errors (primary + rollback / cleanup) now use a typed `domain.DeployError` whose `Unwrap() []error` exposes every cause to `errors.Is` / `errors.As`. Replaces 9 `fmt.Errorf("...%w; ...%v")` sites in `internal/core/installer.go` plus the `joinSwitchErr` helper in `profile.go` and the database-close compound in `service.go`. Output stays close to the prior format (`...; rollback failed: ...`, `...; cleanup failed: ...`)

## [1.3.5] - 2026-04-25

### Changed

- **Context propagation**: Cobra commands now run under a signal-aware context (SIGINT/SIGTERM cancel in-flight I/O). Replaced 18 mid-stack `context.Background()` calls with `cmd.Context()` so cancellation reaches all source/storage/installer calls
- **CLI lifecycle middleware**: Extracted `withService` and `withGameService` helpers in `cmd/lmm/helpers.go`, removing repeated `requireGame` + `initService` + `defer Close` boilerplate from auth, conflicts, deploy, game-add, import, list, mod, mod-edit, purge, search, status, uninstall, update, and verify commands. `install.go` and `profile.go` are deferred to a follow-up that decomposes their large RunE handlers
- **Single auth-prompt source**: New `authPromptError` helper replaces the 5 duplicated `errors.Is(err, domain.ErrAuthRequired)` formatting blocks across `search`, `install`, and `update`

## [1.3.4] - 2026-04-22

### Changed

- **List output defaults**: `lmm list` now shows a narrower default table (`ID`, `NAME`, `VERSION`, `AUTHOR`) and keeps operational fields like source, enabled state, deployment state, and link method behind verbose output

## [1.3.3] - 2026-04-21

### Fixed

- **NexusMods filename normalization**: Path-like `file_name` values from NexusMods are now sanitized to a safe basename before they reach the CLI, cache, or installer, avoiding bogus nested relative paths in file selection and cache writes
- **Archive detection without extensions**: Downloaded archives are now identified by file signature as well as filename extension, so ZIP/7z/RAR downloads still extract correctly even when the source filename is missing or malformed
- **Import path handling**: The CLI import streaming-copy helper now also creates destination parent directories before writing, matching the cache/import behavior used elsewhere

## [1.3.1] - 2026-02-12

### Changed

- **Consolidated version comparison**: `CompareVersions`, `IsNewerVersion`, and `parseVersionParts` moved to `domain/mod.go` as the single source of truth, eliminating duplication between `core/updater.go` and `source/nexusmods/nexusmods.go`
- **ModKey helper**: Added `domain.ModKey()` to replace ad-hoc `sourceID + ":" + modID` string concatenation across 6+ files
- **Deduplicated install logic**: Extracted `batchInstallMods` to replace ~400 lines of near-identical code in `installMultipleMods` and `installModsWithDeps`; extracted `selectPrimaryFile`, `truncateChecksum`, and `runInstallHook` helpers
- **Nil-safe hook accessors**: Added 10 nil-safe getter methods on `ResolvedHooks` (e.g., `GetInstallBeforeAll()`) to simplify verbose nil-check patterns in CLI code
- **Transaction safety**: Wrapped `SaveInstalledMod`, `SwapModVersions`, and `replaceModFileIDs` in database transactions to prevent partial writes
- **Streaming file copy**: `copyDir` now uses streaming I/O (`copyFileStreaming`) instead of `os.ReadFile`/`os.WriteFile` to avoid memory spikes on large mod archives
- **Reused Downloader/Extractor**: `Downloader` and `Extractor` are now persistent fields on `Service` instead of being recreated per download, improving HTTP connection reuse

## [1.3.0] - 2026-02-11

### Added

- **Search pagination**: Search results are now paginated (10 per page) with `[n] Next page` and `[p] Previous page` navigation
- **Cancel search**: Press `q` during mod selection to cancel without selecting (works in both single and multi-select modes)
- **Total result count**: CurseForge searches show total available results; NexusMods gracefully handles unknown totals

### Changed

- `ModSource.Search()` now returns `SearchResult` struct with pagination metadata (`TotalCount`, `Page`, `PageSize`)
- `Service.SearchMods()` accepts `page` and `pageSize` parameters for paginated queries

## [1.2.0] - 2026-02-10

### Added

- **Mod edit command**: `lmm mod edit <id>` to manually correct mod name, version, author after import
- **Source re-linking**: Re-link local mods to CurseForge/NexusMods with `--source` and `--source-id` (auto-fetches metadata)
- **Expanded metadata**: Store and display author, summary, and source URL for mods
- **Import with --id**: `lmm import <file> --id 12345` fetches metadata from source (defaults to CurseForge)
- **Manual download guidance**: When CurseForge blocks API downloads, shows direct URL and import command
- **Verbose list**: `lmm list -v` now includes author column

### Changed

- `--id` flag on import no longer requires `--source` (defaults to curseforge)
- `lmm mod info` now shows source URL when available

## [1.1.0] - 2026-02-05

### Added

- **Automatic source detection**: Commands auto-detect which mod source to use from game config
  - Single source configured: Uses it automatically (no `--source` flag needed)
  - Multiple sources configured: Prompts for selection
  - `-y` flag on install auto-selects first source (for scripting)
  - `--source` flag still works to explicitly specify a source

- **Better CurseForge search ranking**: Search results now prioritize name matches over description/tag matches, then sort by download count

- **Interactive game add**: `lmm game add` searches CurseForge for games by name and guides you through configuration (no need to manually find game IDs)

- **CurseForge Support**: New mod source alongside NexusMods
  - Search, download, install, and update mods from CurseForge
  - `lmm auth login curseforge` to authenticate with API key
  - `CURSEFORGE_API_KEY` environment variable support
  - Dependency detection from CurseForge file metadata
  - Configure in games.yaml: `sources: { curseforge: "432" }` (numeric game IDs or slugs like "minecraft")

## [1.0.0] - 2026-01-29

### Added

- **Steam game auto-detection**: `lmm game detect` scans Steam libraries and offers to add known moddable games to `games.yaml`
  - Parses `libraryfolders.vdf` and `appmanifest_*.acf`
  - Known games map for Skyrim SE, Starfield, Fallout 4, Elden Ring, Witcher 3, and others
  - Prompts to add selected games (e.g. 1,2 or all or none); creates default profile per game
- **JSON output**: `--json` flag on `list`, `status`, and `search` for scriptable output
- **CLI polish**: Exit codes 0=success, 1=error, 2=user cancelled; `--no-color` and `NO_COLOR` env support; `ErrCancelled` for cancelled operations
- **Verify --fix**: `lmm verify --fix --game <id>` re-downloads missing cached mod files and updates checksums (skips local mods)
- **Documentation**: Man pages (`docs/man/man1/`), configuration reference (`docs/configuration.md`), CONTRIBUTING.md

### Changed

- Verify command `--fix` now implements re-download for missing files (was placeholder)

## [0.12.0] - 2026-01-29

### Added

- **Installation Hooks**: Run user-defined scripts before/after mod operations
  - Configure hooks per-game in `games.yaml` with optional per-profile overrides
  - Hook points: `install.before_all`, `install.before_each`, `install.after_each`, `install.after_all`
  - Same pattern for `uninstall.*` hooks
  - Environment variables: `LMM_GAME_ID`, `LMM_GAME_PATH`, `LMM_MOD_PATH`, `LMM_MOD_ID`, `LMM_MOD_NAME`, `LMM_MOD_VERSION`, `LMM_HOOK`
  - Contextual failure handling: `before_*` hooks abort (unless `--force`), `after_*` hooks warn only
  - `--no-hooks` global flag to disable all hooks at runtime
  - Configurable timeout via `hook_timeout` in `config.yaml` (default 60s)
- **Batch Install/Uninstall**: New `InstallBatch` and `UninstallBatch` methods in installer

### Changed

- All mod commands now support hooks: `install`, `uninstall`, `deploy`, `purge`, `update`, `update rollback`, `import`
- Commands now have `--force` flag to continue despite hook failures

## [0.11.0] - 2026-01-28

### Added

- **Automatic dependency installation**: `lmm install` now resolves and installs mod dependencies automatically
  - Shows install plan with dependencies in topological order
  - Warns about dependencies not available on source (external deps like SKSE)
  - `--no-deps` flag to skip dependency installation
  - `-y` flag auto-confirms dependency installation

## [0.10.0] - 2026-01-28

### Added

- **Local Mod Import**: `lmm import <archive-path>` imports mods from local archive files
  - Supports ZIP, 7z, and RAR archive formats
  - Auto-detects NexusMods naming pattern (ModName-ModID-Version.ext) for update linking
  - Extracts to mod cache and deploys to game directory
  - Flags: `--profile`, `--source`, `--id`, `--force`
  - Local mods tracked with source ID "local"
- **NexusMods Filename Parsing**: Automatically extracts mod ID and version from filenames like `SkyUI-12604-5-2SE.zip`
  - Strips trailing timestamps from version strings
  - Normalizes version format (replaces dashes with dots)
  - Falls back to UUID for mods without recognizable patterns

### Changed

- `lmm list` now shows "(local)" for mods imported from local files
- Update checker skips local mods (no remote source to check)

## [0.9.0] - 2026-01-28

### Added

- **Conflict Detection**: Warns when installing mods that overwrite files from existing mods
  - Tracks file ownership in database (per profile)
  - Shows conflicts before install with prompt to continue or cancel
  - `--force` flag to skip conflict prompts: `lmm install --force`
  - Database migration V7 adds `deployed_files` table
- **Mod Files Command**: `lmm mod files <mod-id>` lists all files deployed by a mod
  - Useful for debugging and understanding mod contents
  - Shows files tracked in database (mods installed with 0.9.0+)
- **Conflicts Command**: `lmm conflicts` shows all file conflicts in current profile
  - Lists each conflicting file path
  - Shows which mod owns the file vs which mods also want it
  - Helps identify and resolve mod conflicts

### Changed

- Installer now tracks deployed files per mod in database
- Uninstall now removes file tracking records

## [0.8.0] - 2026-01-28

### Added

- **Checksum Verification**: MD5 checksums are calculated during download and stored
  - Checksums computed during download using TeeReader (zero extra I/O)
  - Stored per-file in database for integrity verification
  - Displayed after each file download (truncated for readability)
  - `--skip-verify` flag on install to skip checksum storage
- **Verify Command**: `lmm verify` checks integrity of cached mod files
  - Verifies all cached mods: `lmm verify --game skyrim-se`
  - Verify specific mod: `lmm verify 12345 --game skyrim-se`
  - Shows OK, MISSING, or NO CHECKSUM status for each file
  - `--fix` flag placeholder for future re-download functionality

### Changed

- Database schema V6: Added `checksum` column to `installed_mod_files` table
- `DownloadMod` now returns `DownloadModResult` with checksum information

## [0.7.8] - 2026-01-28

### Added

- **Deployed State Tracking**: Separate `enabled` and `deployed` states for mods
  - `enabled` = user intent (wants this mod active)
  - `deployed` = current state (files are in game directory)
  - `lmm list` now shows both ENABLED and DEPLOYED columns
  - Purge sets `deployed=false` while preserving `enabled` state
  - Deploy sets `deployed=true` without changing `enabled` state
  - Allows purging and redeploying without losing user's enabled/disabled preferences
- **Deploy All Flag**: `lmm deploy --all` deploys all mods including disabled ones
  - Useful after a purge when you want to deploy everything
  - Without `--all`, only enabled mods are deployed

### Changed

- Database schema V5: Added `deployed` column to track deployment state separately from enabled state

## [0.7.7] - 2026-01-28

### Added

- **Purge Command**: `lmm purge` removes all deployed mods from a game directory
  - Resets the game directory back to its pre-modded state
  - Mod records are preserved by default; use `lmm deploy` to restore
  - `--uninstall` flag also removes mod records from database
  - `--yes` flag skips confirmation prompt
  - Useful when mods get out of sync or you want to start fresh
- **Deploy Purge Flag**: `lmm deploy --purge` clears all deployed mods before deploying
  - Ensures a clean slate before deploying mods
  - Useful for switching deployment methods or fixing sync issues
- **Renamed Command**: `lmm redeploy` is now `lmm deploy`

## [0.7.6] - 2026-01-28

### Fixed

- **Multi-File Install**: When installing multiple files from the same mod, only the first file was being downloaded and extracted. The remaining files were skipped because the cache directory already existed. Now all selected files are properly downloaded and extracted to the cache.

## [0.7.5] - 2026-01-27

### Added

- **UpsertMod API**: New `ProfileManager.UpsertMod()` method for atomic add-or-update operations
  - Updates mod in place if it exists (preserves load order position)
  - Appends to end if mod is new
  - Single save operation instead of two
  - Replaces error-prone `RemoveMod` + `AddMod` pattern

### Changed

- **Profile Operations**: Install, update, sync, switch, apply, and import now use `UpsertMod`
  - More reliable FileID updates during re-installs
  - Cleaner code with centralized profile modification logic

## [0.7.4] - 2026-01-27

### Fixed

- **FileIDs Persistence**: FileIDs were not being written to profile YAML files
  - Added `file_ids` field to profile config format
  - Profile save/load now properly handles FileIDs

## [0.7.3] - 2026-01-27

### Added

- **Auto-Sync FileIDs**: Profile YAML now automatically stays in sync with actual file selections
  - Install: Profile updated with downloaded FileIDs
  - Update: Preserves file selections, updates both DB and profile
  - Profile switch/import/apply: Auto-sync FileIDs after installing mods
  - Profile sync: Updates FileIDs for existing mods that are missing them

## [0.7.1] - 2026-01-27

### Fixed

- **Re-install Cleanup**: When re-installing a mod (e.g., to change file selection), old files are now properly removed from the game directory before installing new files
- **Cache Cleanup**: Old cache is deleted before downloading new files during re-install, ensuring a clean slate
- **Profile Export/Import FileIDs**: File IDs are now preserved when exporting and importing profiles
  - `lmm profile export` now includes FileIDs for each mod in the exported YAML
  - `lmm profile import` uses FileIDs from the imported profile to restore exact file selections
  - `lmm profile apply` and `lmm profile switch` also respect FileIDs for new installs and re-downloads

## [0.7.0] - 2026-01-26

### Added

- **Multi-File Install**: `lmm install` now supports selecting multiple files per mod
  - Select files like mods: `1,3,5` or `1-3` or `1,3-5`
  - Useful for mods with main file + optional patches or multiple optional files
  - `--file` flag accepts comma-separated file IDs for scripting
- **File ID Tracking**: Mods now track which specific file(s) were downloaded from the source
  - When re-downloading (cache missing), restores the exact file(s) the user originally installed
  - Supports multiple files per mod (e.g., main file + optional patches)
  - New database table `installed_mod_files` stores file IDs per mod
  - Falls back to primary file if stored IDs are no longer available on the source
- **Cache-Missing Re-download**: Mods that exist in the database but are missing from cache are now automatically re-downloaded
  - `lmm profile import` - Shows "cache missing" category separately, re-downloads as needed
  - `lmm profile apply` - Detects cache-missing mods when enabling, triggers download
  - `lmm profile switch` - Detects cache-missing mods, triggers download
  - `lmm redeploy` - Re-downloads from source instead of failing when cache is missing
  - Useful when cache directory changes, files are deleted, or profile is imported on a new machine

### Changed

- **Database Schema**: Migration V4 adds `installed_mod_files` table for tracking downloaded file IDs

## [0.6.9] - 2026-01-26

### Added

- **Cache-Missing Re-download** (superseded by 0.7.0): Initial implementation without file ID tracking

## [0.6.8] - 2026-01-25

### Added

- **GitHub Releases**: Automated release builds via GitHub Actions and GoReleaser
  - Creates Linux amd64 and arm64 binaries on tag push
  - Archives include README, LICENSE, and CHANGELOG
  - Checksums provided for verification
- **Go Install Support**: `go install github.com/DonovanMods/linux-mod-manager/cmd/lmm@latest`

### Changed

- **Module Path**: Changed from `lmm` to `github.com/DonovanMods/linux-mod-manager` to enable go install

## [0.6.7] - 2026-01-24

### Added

- **Status Default Game**: `lmm status` marks the default game with `(default)` in the game list

## [0.6.6] - 2026-01-24

### Added

- **Enhanced Status Output**: More detailed game information in `lmm status`
  - Shows installed mod count per game and total across all games
  - Shows link method with indicator for per-game overrides
  - Shows cache path with indicator for per-game vs global default
  - Shows source mappings in verbose mode
  - Summary view (`lmm status -v`) shows game ID, path, and link method columns
  - `*` suffix indicates per-game overrides in summary view
- **Cache Path in List**: `lmm list -v` shows cache path in verbose header

## [0.6.5] - 2026-01-24

### Added

- **Configurable Cache Path**: Set `cache_path` in `config.yaml` to store downloaded mods in a custom location
  - Supports `~` expansion for home directory paths
  - Useful for storing mods on a separate drive or faster storage
  - Defaults to `~/.local/share/lmm/cache/` if not set
- **Per-Game Cache Path**: Override the global cache path for individual games in `games.yaml`
  - Set `cache_path` per game to store that game's mods in a custom location
  - Priority: per-game `cache_path` > global `cache_path` > default

## [0.6.4] - 2026-01-24

### Added

- **Search Command**: Shows `[installed]` marker for mods already in your profile
  - Matches the existing behavior in the install command
  - Optional `--profile` flag to check against a specific profile

## [0.6.3] - 2026-01-24

### Added

- **Deployment Method Tracking**: `lmm list` now shows a DEPLOY column indicating how each mod was deployed (symlink, hardlink, or copy)
  - Link method is saved per-mod when installing, updating, or redeploying
  - Helps identify which mods use which deployment strategy

### Changed

- Database schema V3: Added `link_method` column to track deployment method per mod

## [0.6.2] - 2026-01-24

### Fixed

- **Per-Game Link Method**: Install, update, and redeploy commands now correctly use per-game `link_method` from `games.yaml`
  - If a game specifies `link_method` in `games.yaml`, that method is used
  - If not specified, falls back to global `default_link_method` from `config.yaml`
  - Different games can now use different deployment methods (symlink, hardlink, copy)
  - Affects: `lmm install`, `lmm update`, `lmm update rollback`, and `lmm redeploy`

## [0.6.1] - 2026-01-24

### Added

- **Redeploy Command**: `lmm redeploy` to re-deploy mods from cache to game directory
  - Re-deploy all enabled mods: `lmm redeploy`
  - Re-deploy specific mod: `lmm redeploy <mod-id>`
  - `--method` flag to try different link methods (symlink, hardlink, copy)
  - Useful when changing deployment methods or refreshing mod files

## [0.6.0] - 2026-01-24

### Added

- **Profile as Source of Truth**: Profiles now fully track mod state
  - Installing a mod automatically adds it to the current profile
  - Uninstalling a mod removes it from the current profile
- **Profile Sync Command**: `lmm profile sync` updates profile YAML to match installed mods
  - Use when profile gets out of sync or migrating from pre-profile installs
- **Profile Apply Command**: `lmm profile apply` makes system match profile
  - Installs missing mods, enables/disables as needed
  - Use after manually editing a profile YAML
- **Enhanced Profile Switch**: Switching profiles now installs missing mods
  - Shows preview of changes (disable/enable/install)
  - Prompts for confirmation before making changes
- **Enhanced Profile Import**: Importing profiles can install missing mods
  - `--force` flag to overwrite existing profiles
  - `--no-install` flag to skip installing missing mods
  - Shows preview of which mods need to be downloaded

### Changed

- Profile switch now shows detailed diff before switching
- Profile import shows summary of installed vs missing mods

## [0.5.3] - 2026-01-24

### Added

- **Multi-Select Install**: Select multiple mods from search results using range syntax
  - `1,3,5` - Select specific mods by number
  - `1-3` or `1..3` - Select a range of mods
  - `1,3-5,8` - Mix both formats
  - Each mod is installed sequentially with automatic file selection

## [0.5.2] - 2026-01-24

### Added

- **Install Command**: Search results now show `[installed]` marker for mods already in your profile

## [0.5.0] - 2026-01-24

### Added

- **Mod Enable/Disable**: New commands to enable or disable mods without uninstalling
  - `lmm mod enable <mod-id>` - Redeploy mod files from cache to game directory
  - `lmm mod disable <mod-id>` - Remove mod files from game directory, keep in cache
  - Disabled mods show as "no" in the ENABLED column of `lmm list`
  - Re-enabling a mod does not require re-downloading

## [0.4.0] - 2026-01-24

### Added

- **NexusMods Update Detection**: `CheckUpdates` now queries NexusMods API for current mod versions and compares against installed versions using semantic version parsing
- **Mod Dependency Fetching**: `GetDependencies` queries NexusMods GraphQL API for mod requirements, returning dependencies as `ModReference` entries

### Fixed

- **Uninstall Cleanup**: `lmm uninstall` now properly undeploys mod files from the game directory and cleans up the mod cache (unless `--keep-cache` is specified)

### Changed

- **NexusMods OAuth**: Clarified that NexusMods uses API key authentication, not OAuth. The `ExchangeToken` method now returns a clear error message directing users to use `SetAPIKey()` or the `NEXUSMODS_API_KEY` environment variable

## [0.3.0] - 2026-01-23

### Added

- **Update Management**: Complete update workflow with policies and rollback
  - `lmm update --game <game>` - Check all mods for updates, apply auto-updates
  - `lmm update <mod-id> --game <game>` - Update specific mod
  - `lmm update --dry-run` - Preview what would update
  - `lmm update --all` - Apply all available updates (not just auto-updates)
  - `lmm update rollback <mod-id>` - Rollback to previous version
  - `lmm mod set-update <mod-id> --auto|--notify|--pin` - Set per-mod update policy
- **Per-mod Update Policies**:
  - `notify` (default) - Show available updates, require manual approval
  - `auto` - Automatically apply updates when checking
  - `pinned` - Never update this mod automatically
- **Rollback Support**: Previous version preserved in cache after updates
- **Database Migration V2**: Added `previous_version` column for rollback tracking

### Changed

- **CLI**: `lmm update` now shows update policy column in output
- **CLI**: Auto-updates are applied immediately when checking for updates

## [0.2.0] - 2026-01-23

### Added

- **Mod Download**: Complete download pipeline for installing mods
  - `lmm install "query"` - Search for mods by name and install interactively
  - `lmm install --id <mod-id>` - Install directly by mod ID (for scripting)
  - `lmm install -y` - Auto-select first/primary options (no prompts)
  - Download progress bar with size tracking
  - Archive extraction: ZIP (native Go), 7z/RAR (via system `7z` command)
  - Mod file caching with version-aware storage
  - Automatic deployment to game directory via symlinks
- **NexusMods API**: File listing and download URL generation
  - `GetModFiles()` - List available download files for a mod
  - `GetDownloadURL()` - Get CDN download URL for a specific file
- **Domain Types**: `DownloadableFile` type for files available from mod sources
- **Core Components**:
  - `Downloader` - HTTP file download with progress tracking and atomic writes
  - `Extractor` - Archive extraction with zip-slip protection
- **Authentication**: NexusMods API key authentication
  - `lmm auth login` - Authenticate with NexusMods using personal API key
  - `lmm auth logout` - Remove stored credentials
  - `lmm auth status` - Show authentication status
  - Secure token storage in SQLite database
  - Support for `NEXUSMODS_API_KEY` environment variable
  - Automatic token loading on startup
- **CLI**: Helpful error messages when authentication is required

### Fixed

- **CLI**: NexusMods source now properly registered on startup (search was failing with "source not found")
- **CLI**: Removed downloads column from search output (GraphQL doesn't return this data)

### Changed

- **CLI**: `lmm install` now accepts search query instead of mod ID (use `--id` for direct ID)
- **NexusMods**: Search now uses GraphQL v2 API for proper server-side search (no auth required for basic searches)
- **NexusMods**: REST API v1 still used for mod details, files, and downloads (requires API key)

### Removed

- **TUI**: Removed terminal UI to focus on CLI functionality first (see BACKLOG.md)

## [0.1.0] - 2026-01-23

### Added

#### Core Infrastructure

- Domain types: `Mod`, `InstalledMod`, `Game`, `Profile`, `ModReference`
- SQLite database with migrations for mod metadata and auth tokens
- YAML configuration for games and profiles
- Mod file cache with version-aware storage

#### Mod Sources

- `ModSource` interface for abstracting mod repositories
- Source registry for managing multiple mod sources
- NexusMods REST API v1 client with mod fetching and browse functionality

#### Mod Management

- Service facade orchestrating all mod operations
- Installer with download, extract, and deploy functionality
- Updater with semantic version comparison
- Dependency resolver with cycle detection (topological sort)
- Profile manager with create, delete, switch, export, and import

#### Deployment

- Linker interface with three strategies:
  - Symlink (default) - symbolic links to cached files
  - Hardlink - hard links for same-filesystem deployments
  - Copy - full file copies for maximum compatibility

#### Terminal UI (TUI)

- Bubble Tea application shell with view routing
- Game selector view with navigation
- Mod browser with search input and results display
- Installed mods view with enable/disable and reorder
- Profile management view with create/delete/switch/export
- Settings view with cycling options
- Configurable keybindings (vim and standard modes)

#### Command Line Interface (CLI)

- Cobra command structure with global flags
- `lmm` - Launch interactive TUI (default)
- `lmm search <query>` - Search for mods
- `lmm install <mod-id>` - Install a mod
- `lmm uninstall <mod-id>` - Uninstall a mod
- `lmm update [mod-id]` - Check for updates
- `lmm list` - List installed mods
- `lmm status` - Show current status
- `lmm profile list|create|delete|switch|export|import` - Profile management

### Technical Details

- Pure Go implementation (no CGO required)
- ~2500 lines of Go code
- Comprehensive test coverage for core components
- MIT License

[Unreleased]: https://github.com/DonovanMods/linux-mod-manager/compare/v1.27.1...HEAD
[1.27.1]: https://github.com/DonovanMods/linux-mod-manager/compare/v1.27.0...v1.27.1
[1.27.0]: https://github.com/DonovanMods/linux-mod-manager/compare/v1.26.0...v1.27.0
[1.26.0]: https://github.com/DonovanMods/linux-mod-manager/compare/v1.25.0...v1.26.0
[1.25.0]: https://github.com/DonovanMods/linux-mod-manager/compare/v1.24.1...v1.25.0
[1.24.1]: https://github.com/DonovanMods/linux-mod-manager/compare/v1.24.0...v1.24.1
[1.24.0]: https://github.com/DonovanMods/linux-mod-manager/compare/v1.23.1...v1.24.0
[1.23.1]: https://github.com/DonovanMods/linux-mod-manager/compare/v1.23.0...v1.23.1
[1.23.0]: https://github.com/DonovanMods/linux-mod-manager/compare/v1.22.0...v1.23.0
[1.22.0]: https://github.com/DonovanMods/linux-mod-manager/compare/v1.21.0...v1.22.0
[1.21.0]: https://github.com/DonovanMods/linux-mod-manager/compare/v1.20.1...v1.21.0
[1.20.1]: https://github.com/DonovanMods/linux-mod-manager/compare/v1.20.0...v1.20.1
[1.20.0]: https://github.com/DonovanMods/linux-mod-manager/compare/v1.19.0...v1.20.0
[1.19.0]: https://github.com/DonovanMods/linux-mod-manager/compare/v1.18.3...v1.19.0
[1.18.3]: https://github.com/DonovanMods/linux-mod-manager/compare/v1.18.2...v1.18.3
[1.18.2]: https://github.com/DonovanMods/linux-mod-manager/compare/v1.18.1...v1.18.2
[1.18.1]: https://github.com/DonovanMods/linux-mod-manager/compare/v1.18.0...v1.18.1
[1.18.0]: https://github.com/DonovanMods/linux-mod-manager/compare/v1.17.1...v1.18.0
[1.17.1]: https://github.com/DonovanMods/linux-mod-manager/compare/v1.17.0...v1.17.1
[1.17.0]: https://github.com/DonovanMods/linux-mod-manager/compare/v1.16.0...v1.17.0
[1.16.0]: https://github.com/DonovanMods/linux-mod-manager/compare/v1.15.0...v1.16.0
[1.15.0]: https://github.com/DonovanMods/linux-mod-manager/compare/v1.14.1...v1.15.0
[1.14.1]: https://github.com/DonovanMods/linux-mod-manager/compare/v1.14.0...v1.14.1
[1.14.0]: https://github.com/DonovanMods/linux-mod-manager/compare/v1.13.1...v1.14.0
[1.13.1]: https://github.com/DonovanMods/linux-mod-manager/compare/v1.13.0...v1.13.1
[1.13.0]: https://github.com/DonovanMods/linux-mod-manager/compare/v1.12.3...v1.13.0
[1.12.3]: https://github.com/DonovanMods/linux-mod-manager/compare/v1.12.2...v1.12.3
[1.12.2]: https://github.com/DonovanMods/linux-mod-manager/compare/v1.12.1...v1.12.2
[1.12.1]: https://github.com/DonovanMods/linux-mod-manager/compare/v1.12.0...v1.12.1
[1.12.0]: https://github.com/DonovanMods/linux-mod-manager/compare/v1.11.0...v1.12.0
[1.11.0]: https://github.com/DonovanMods/linux-mod-manager/compare/v1.10.0...v1.11.0
[1.10.0]: https://github.com/DonovanMods/linux-mod-manager/compare/v1.9.0...v1.10.0
[1.9.0]: https://github.com/DonovanMods/linux-mod-manager/compare/v1.8.0...v1.9.0
[1.8.0]: https://github.com/DonovanMods/linux-mod-manager/compare/v1.7.0...v1.8.0
[1.7.0]: https://github.com/DonovanMods/linux-mod-manager/compare/v1.6.0...v1.7.0
[1.6.0]: https://github.com/DonovanMods/linux-mod-manager/compare/v1.5.0...v1.6.0
[1.5.0]: https://github.com/DonovanMods/linux-mod-manager/compare/v1.4.0...v1.5.0
[1.4.0]: https://github.com/DonovanMods/linux-mod-manager/compare/v1.3.10...v1.4.0
[1.3.10]: https://github.com/DonovanMods/linux-mod-manager/compare/v1.3.9...v1.3.10
[1.3.9]: https://github.com/DonovanMods/linux-mod-manager/compare/v1.3.8...v1.3.9
[1.3.8]: https://github.com/DonovanMods/linux-mod-manager/compare/v1.3.7...v1.3.8
[1.3.7]: https://github.com/DonovanMods/linux-mod-manager/compare/v1.3.6...v1.3.7
[1.3.6]: https://github.com/DonovanMods/linux-mod-manager/compare/v1.3.5...v1.3.6
[1.3.5]: https://github.com/DonovanMods/linux-mod-manager/compare/v1.3.4...v1.3.5
[1.3.4]: https://github.com/DonovanMods/linux-mod-manager/compare/v1.3.3...v1.3.4
[1.3.3]: https://github.com/DonovanMods/linux-mod-manager/compare/v1.3.1...v1.3.3
[1.3.1]: https://github.com/DonovanMods/linux-mod-manager/compare/v1.3.0...v1.3.1
[1.3.0]: https://github.com/DonovanMods/linux-mod-manager/compare/v1.2.0...v1.3.0
[1.2.0]: https://github.com/DonovanMods/linux-mod-manager/compare/v1.1.0...v1.2.0
[1.0.0]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.12.0...v1.0.0
[0.12.0]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.11.0...v0.12.0
[0.11.0]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.10.0...v0.11.0
[0.10.0]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.9.0...v0.10.0
[0.9.0]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.8.0...v0.9.0
[0.8.0]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.7.8...v0.8.0
[0.7.8]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.7.7...v0.7.8
[0.7.7]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.7.6...v0.7.7
[0.7.6]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.7.5...v0.7.6
[0.7.5]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.7.4...v0.7.5
[0.7.4]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.7.3...v0.7.4
[0.7.3]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.7.2...v0.7.3
[0.7.1]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.7.0...v0.7.1
[0.7.0]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.6.9...v0.7.0
[0.6.9]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.6.8...v0.6.9
[0.6.8]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.6.7...v0.6.8
[0.6.7]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.6.6...v0.6.7
[0.6.6]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.6.5...v0.6.6
[0.6.5]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.6.4...v0.6.5
[0.6.4]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.6.3...v0.6.4
[0.6.3]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.6.2...v0.6.3
[0.6.2]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.6.1...v0.6.2
[0.6.1]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.6.0...v0.6.1
[0.6.0]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.5.3...v0.6.0
[0.5.3]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.5.2...v0.5.3
[0.5.2]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.5.0...v0.5.2
[0.5.0]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/DonovanMods/linux-mod-manager/releases/tag/v0.1.0
