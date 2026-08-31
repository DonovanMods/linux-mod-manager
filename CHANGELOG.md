# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `lmm serve` starts a local web UI (127.0.0.1:7420 by default, `--addr`
  to change it, `--no-open` to skip auto-opening a browser): a
  server-rendered status dashboard, plus `/mods`, `/mods/{source}/{id}`
  (full prose, changelog, files, versions - #232/#87), `/search`,
  `/updates` (#74), `/profiles`, and `/health` (verify findings + file
  conflicts) read pages, each with a game/profile switcher and disabled
  mutation form shells previewing the routes a later unit wires (#319). A
  read-only `GET /api/v1` JSON API mirrors the same reads -
  `/api/v1/{status,mods,mods/{source}/{id},search,updates,profiles,health,
conflicts}` - each answering exactly the document `lmm <cmd> --json`
  emits for the identical call, with the CLI's `{"error","details"}`
  envelope on failure (#320). Mutations and background jobs land in
  follow-up units of this epic (#321/#322, epic #276).
- `lmm serve` gains its jobs API: `POST /api/v1/plans/{kind}` computes a
  mutation's plan and returns it with a single-use `plan_id`, `POST
/api/v1/jobs` redeems that id and runs the Apply as a background job
  (returning `{"job_id"}`), `GET /api/v1/jobs/{id}` reports its state and
  result-or-error, and `GET /api/v1/jobs/{id}/events` streams the typed
  core progress events as Server-Sent Events - replaying what the job has
  already emitted before going live, so a page opened mid-operation sees
  the whole run. A `/jobs/{id}` page renders the same state, progress and
  result with no JavaScript. A job's Apply runs under the server's own
  context, never the request's, so closing the tab that started an
  operation never interrupts it. `deploy` is the first plan kind wired
  end-to-end; the remaining mutations follow in #322 (epic #276).
- `lmm serve` wires its single-mod mutations. Every `/mods` row's
  Enable/Disable/Uninstall buttons and the Install button on a mod's detail
  page and on `/search` now work. Uninstall and install first show a
  confirmation page rendered from the real plan - the exact files that
  would be removed, the hooks that would run, what would be downloaded -
  and confirming runs the operation as a background job with live progress
  at `/jobs/{id}`. Install's confirmation page offers a version picker
  (from the versions the source reports) and a file picker whenever the
  mod actually has more than one candidate file (#225); uninstall's offers
  keep-the-download, skip-hooks and force (#226). When installing would
  overwrite files another installed mod owns, nothing is changed: the job
  reports the conflicting files and offers Overwrite, which installs
  anyway without downloading a second time. If the profile changes while a
  confirmation page is open, the operation is refused and a freshly
  computed plan is shown instead of applying a stale one. Every action
  works with JavaScript disabled, and a `?sync=1` form target runs the
  operation inline and returns its result for callers that would rather
  wait than watch (#322, epic #276).
- `mod show` displays a mod's changelog when its source can supply one
  (`--json`: `changelog`, additive) - NexusMods now implements the new
  `source.ChangelogProvider` optional capability via its files endpoint's
  changelog field; a source without it, or a failed live fetch, simply
  omits the section rather than failing the command (#87).

## [2.0.0] - 2026-08-30

### v2 migration notes

This release removes the interactive TUI (#273); the CLI is the only
shipped frontend on the v2 line, with a local web UI (`lmm serve`) planned
for later.

**`--json` output changes once, deliberately, in this release.** Every
document is now a type from `internal/core`, `internal/domain`, or
`internal/app` rather than a CLI-only view struct, and the error envelope is
`{"error": "...", "details": {...}}`; see the README's
[JSON output](README.md#json-output) section for the full command →
document table. **`--json` never prompts**: every confirmation now has a
deciding flag (`-y`/`--yes`, `--force`), and a run that would otherwise
prompt fails first with the error envelope instead of reading stdin. A mod
with no source-supplied timestamp now omits `updated_at` entirely instead of
carrying the zero-value `0001-01-01T00:00:00Z`, matching the
not-applicable-is-absent convention `convert_paks`/`auth`/`cache_path`
already use.

A handful of plain-text and event behaviours changed alongside the JSON
contract, each pinned by a re-recorded capture and detailed below: lock
refusals now read one canonical wording per refusal kind (#294); a
multi-source update check's `-v` progress counts globally instead of
restarting per source (#283); a profile export round-trips hook overrides
through import (#296); dry-run merged-artifact lines only print when the
artifact would actually change (Ruling 8); cancellation mid-mutation always
finishes a database write's paired profile write rather than splitting the
two (Ruling 16); a declined import conflict on a reproducible identity
does not restore the entry it overwrote (#310); `lmm game show-default`'s
plain text moves from stderr to stdout, where every other command's plain
text already lands (Ruling 17, #309); and an accepted `import <archive>`
conflict no longer reprints the import readout or mints a second mod ID
(Ruling 18).

Building lmm now requires Go 1.27. Config and data directories now honor
`XDG_CONFIG_HOME`/`XDG_DATA_HOME` by default, falling back to the legacy
paths when they still exist (#274).

The Go module path is now `github.com/DonovanMods/linux-mod-manager/v2`
(Ruling 14, Option A — semantic import versioning). Install with
`go install github.com/DonovanMods/linux-mod-manager/v2/cmd/lmm@latest`; the
unsuffixed `go install github.com/DonovanMods/linux-mod-manager/cmd/lmm@latest`
still resolves to v1.30.1, since Go's module rules do not treat a `v2.0.0+`
tag as a valid version for a module path without the `/v2` suffix. The
decision and its consequences are recorded in
`docs/plans/archive/v2.0.0-release-checklist.md`.

This preamble, the README's v2 architecture and JSON-contract sections, and
`docs/plans/archive/v2.0.0-release-checklist.md` were themselves written as
the phase's docs unit (#306).

### Added

- `--log-level` (off|error|warn|info|debug) writes diagnostics to stderr; default off. (#281)
- `lmm deploy --dry-run` prints what a deploy would do — the mods it would touch in load order,
  the files each would link (and any stale ones it would remove), what a `--purge` pass would
  remove first, the merged-artifact readout on a compile game, and the hooks that would run —
  without changing anything. `--verbose` adds the per-file detail. (#293)
- `lmm uninstall --dry-run` prints what an uninstall would do — which mod the ID resolves to
  (with its source, so a bare ID's first-match rule is visible), how many files would leave the
  game directory, what happens to the cache, and the hooks that would run — without changing
  anything. `--verbose` lists the files. On a compile-mode game it also states what would happen
  to the profile's merged artifact — resynced, or removed once the last merge source goes — and
  says nothing at all when the uninstall would leave the artifact exactly as it is. (#293, #304)
- `lmm purge --dry-run` prints what a purge would do — the mods it would undeploy, what happens
  to their records, and the hooks that would run — without changing anything and without
  prompting. On a compile-mode game with a merged artifact deployed it also states that the
  artifact would be removed too; with nothing merged yet there is nothing to remove and the line
  is absent. (#293, #304)
- `lmm profile switch` and `lmm profile sync` gain `-y`/`--yes` to skip their confirmation
  prompt; `lmm profile import` gains `-y`/`--yes` to answer its "Download and install mods?"
  prompt without a stdin read. `lmm game detect` gains `--all` (select every not-yet-configured
  detected game — the same set the interactive "all" answer selects) and `--select <indices>`
  (the same 1-based indices the prompt accepts, including an already-configured game's index for
  a repair); the two are mutually exclusive and both skip the prompt entirely. As a side effect of
  the same prompt-reading change, `lmm game detect` on a closed/EOF stdin (no `--all`/`--select`)
  now reads that as an empty answer and prints `No games added.` exiting `0`, instead of
  `Error: reading input: EOF` exiting `1`; every other prompting command's EOF behaviour is
  unchanged. (#303)
- **`--json` on every mutating command.** `install`, `import` (archive and scan), `deploy`,
  `uninstall`, `purge`, `profile apply/switch/sync/import/create/delete/reorder`,
  `mod enable/disable/lock/unlock/set-update/convert/edit` and
  `game detect/set-default/clear-default` now emit their core result document; with `--dry-run`
  they emit the plan document instead of its rendering. Progress and per-mod status lines are
  suppressed under `--json`, so stdout holds exactly one document and stderr stays empty apart
  from `--log-level` diagnostics. Two new core documents back this: `core.ProfileResult`
  (`{profile}`) for the profile-management commands and `core.SettingsResult` (`{default_game}`)
  for `game set-default`/`clear-default`. `--json` never prompts: a confirmation with no deciding
  flag fails before mutating anything with the error envelope, and an `install --json` or
  `import <archive> --json` blocked by file conflicts reports them as `details.conflicts` (pass
  `--force` to accept). `lmm mod files` honours `--json` too, emitting `core.ModFilesReport`.
  `install`'s `files_deployed` (and plain-text `Files deployed: N`) now counts the cache entry's
  files once instead of accumulating a per-archive count, which previously over-counted mods
  installed from two or more extracted archives (e.g. 3 → 2). (#303)
- `lmm profile apply`, `lmm profile switch` and `lmm profile sync` gain `--dry-run`: they print
  the same plan preview the live run shows, under a `<Verb> plan for profile "<name>" (dry run)`
  header, and change nothing — including the profile-creating and default-switching writes a
  no-changes run would otherwise still perform. (#303)
- **`--json` on the five remaining commands.** `profile list`, `auth status`, `profile export`,
  `source validate` and `game show-default` now honour `--json` too, closing the last gap left by
  #303: `lmm profile list` emits `core.ProfileListing`; `lmm auth status` emits
  `app.AuthStatusReport`; `lmm profile export` emits `domain.ExportedProfile` (the plain path keeps
  writing the YAML document, unchanged); `lmm source validate` emits `app.SourceValidationReport`,
  with an invalid definition (or a failed `--probe`) still exiting non-zero under an error envelope
  whose `details` carries the report; `lmm game show-default` emits `core.DefaultGame` (see Ruling
  17, above, for its plain-text stream move). (#309)

### Changed

- **The Go module path is now `github.com/DonovanMods/linux-mod-manager/v2`**
  (semantic import versioning, Ruling 14 Option A). Install with
  `go install github.com/DonovanMods/linux-mod-manager/v2/cmd/lmm@latest`;
  every intra-module import path gains the `/v2` segment. The unsuffixed
  path stays pinned to the v1.x line, so existing `@latest` installs keep
  resolving to v1.30.1.
- Building lmm now requires Go 1.27.
- Default config and data directories honor `XDG_CONFIG_HOME` / `XDG_DATA_HOME` (defaults `~/.config/lmm` and `~/.local/share/lmm`). When an XDG variable is set but its lmm directory does not exist and the legacy one does, the legacy directory is used so existing installs keep working. `--config`/`--data` and `cache_path` still override. Bootstrap (paths, directory hardening, source registration) now lives in `internal/app` so every frontend resolves identically. (#270)
- `lmm --config X --data Y` no longer requires `$HOME` to be set. (#277)
- Every I/O method on the core service takes a `context.Context`; long-running loops (downloads, deploys, verify) stop at the next iteration after cancellation, and best-effort recovery paths never inherit the caller's cancellation. (#278)
- Every `--json` document is now a core/domain type marshalled with
  `encoding/json/v2` (deterministic key order, 2-space indent, exactly one
  document on stdout with one trailing newline). The nine commands that
  supported `--json` no longer project their own view structs — those are
  deleted — so the CLI's wire shape is the same contract `internal/{core,domain,app}`'s
  recorded goldens pin, and `lmm serve` will render the same documents. **This
  is a breaking change for scripts written against the 1.x JSON**; it happens
  once, in the v2.0.0 window. The error envelope is now
  `{"error": "...", "details": {...}}`, with `details` present only for typed
  errors that carry data. See the README's "JSON output" section. (#302)

Per command:

- `lmm list` → `core.ModList`. Each `mods[]` row is the whole
  `domain.InstalledMod` plus `locked`/`locked_version`/`convert_paks`:
  `source` → `source_id`, and author, summary, description, game_id,
  category, downloads, picture_url, source_url, files, dependencies,
  updated_at, profile_name, installed_at and manual_download are now present.
- `lmm list --profiles` → `core.ProfileNames` (unchanged shape: `{game_id,
profiles}`).
- `lmm status` → `core.StatusReport`; `lmm status -g <id>` →
  `core.GameStatus`. Each game row is the whole `domain.Game` (source_ids,
  link_method_explicit, hooks, deploy_mode, convert_paks_explicit), and
  `is_default`, `installed_mod_count`, `enabled_mod_count` and
  `conversion_failures` are always present rather than omitted when zero.
  `convert_paks` is present only for `deploy_mode: compile` games, absent
  otherwise — the same tri-state convention `list --json` already used for
  mods. `lmm status -g <id> --json`'s `cache_path` also changes meaning: it
  used to be the _resolved_ cache root and is now the _configured per-game
  override_ (absent when unset); the resolved value moved to the new
  `resolved_cache_path`, which is always present.
- `lmm search` → `core.SearchReport`. Each hit is the whole `domain.Mod`
  plus `installed` (`source` → `source_id`); `warnings` is now
  `[{source_id, error}]` instead of pre-formatted strings and is always
  present; `total_results` (the untruncated count behind a `--limit`-capped
  `mods[]`) and `attempted_count` (how many sources could actually search)
  are new.
- `lmm verify` → `core.VerifyReport`. Findings and counts move under
  `result`: `files` → `result.findings`, plus `result.checked` and
  `result.has_files`. Finding keys are omitted when unset, and a finding may
  now carry `recorded`/`effective`/`version`.
- `lmm conflicts` → `core.ConflictReport`. `owner`, each `also_in` entry and
  `winner` → `load_order_winner` are now `{key, name}` objects, where `key`
  is `"<source_id>:<mod_id>"`, instead of bare display names.
- `lmm mod show` → `core.ModDetail`. The source metadata moves under `mod`:
  `{mod: {...}, installed?: {...}}`. The `installed` block is unchanged.
- `lmm source list` → `[]app.SourceInfo`. Now indented like every other
  document (it was the last compact one). `auth` is the enum
  `none|required|authenticated` (was `n/a|no|yes`) and `capabilities` is a
  string array (was a comma-joined string). An error row (a source that
  failed to construct) now omits the `auth` key entirely, rather than
  carrying it as `""`.
- `lmm game list` → `[]core.GameListEntry`. Each row is the whole
  `domain.Game` plus `default`: `sources` → `source_ids`, and link_method,
  link_method_explicit, cache_path, hooks and convert_paks_explicit are new.
  `convert_paks` is present only for `deploy_mode: compile` games, absent
  otherwise — the same tri-state `list --json` already used for mods.
- `lmm update` (bulk) → `core.UpdateCheckReport`. Each `updates[]` entry is a
  `domain.Update`: the installed mod under `installed_mod` (carrying
  `update_policy`) plus `new_version`, replacing the flat
  mod_id/name/current_version/available_version/update_policy row.
  `reason: "stale_compile"` → `recompile_reason`, carrying core's own wording.
- `lmm update <mod-id>` → `core.UpdateApplyResult`; `lmm update rollback` →
  `core.RollbackResult`. `mod_id` → `mod`, the profile reference
  `{source_id, mod_id, version, locked}`, so a document names its source;
  rollback's `name` → `mod_name`; `to_version` is always present; `warnings`
  and `notes` appear when the operation produced any.
- `core.DeployResult.Skipped`, `core.PurgeResult.Skipped` and
  `core.ProfileApplyResult.Failed` are arrays of objects
  (`{source_id, mod_id, name, version, reason}`) instead of pre-formatted
  `"<name>: <reason>"` strings — JSON carries data, never rendered text. The
  plain-text output is unchanged. (#303)
- `core.RollbackResult` gains `Mod` (a `domain.ModReference`), matching
  `UpdateApplyResult` — without it the rollback document had no way to say
  which mod, from which source, it was reporting on. (#302)
- A `false` boolean or `0` tagged `omitempty` is now emitted rather than
  omitted (`encoding/json/v2` omits only empty JSON values — `""`, `null`,
  `{}`, `[]`); `lmm update --json`'s unlocked `updates[]` entries therefore
  carry `"locked": false` explicitly.
- `encoding/json/v2` no longer HTML-escapes the angle-bracket and ampersand
  characters the way `encoding/json` did; string fields that can carry
  markup (`mod show`'s `description`, `update <mod-id>`'s `changelog`) may
  now contain those characters literally instead of as escape sequences.
  Semantically identical after decoding, but pasting lmm's JSON directly
  into an HTML/script context is no longer incidentally safe.
- `lmm profile import` now asks "Download and install mods?" **before** it saves the profile,
  not after: the prompt (and, on a decline, its "Skipped." line) precede the
  `✓ Imported profile: <name>` line. A save that fails (importing over an existing profile without `--force`) therefore
  prints the prompt first, and a prompt read failure now leaves the profile unsaved. (#303)
- `lmm install` and `lmm import` still ask before overwriting another mod's deployed files, and
  the question still comes after the download/extract step that makes the conflict detectable —
  but it now comes **before** the `install.before_all`/`install.before_each` hooks instead of
  after them, so declining costs no hook run at all. Answering "y" re-runs the operation with the
  cache already warm: `install` re-prints only "Extracting to cache…" (never a second
  "Downloading …"/"Checksum: …" block). An accepted
  conflict re-run does not re-download cached files (a same-version reinstall or a local
  directory source still refreshes its files); hooks run once. A forced hook warning (`--force`
  with a failing `install.before_all`) now prints after the download lines rather than before
  them. `--force` still skips the conflict check entirely. **`lmm import <archive>` does not
  re-run at all** — Ruling 18 below supersedes this entry's import clauses: the conflict question
  is answered from the plan, before anything is cached, so a declined import writes nothing (and
  therefore never overwrites a pre-existing entry at a reproducible identity, the residue tracked
  as #310), and an accepted one neither re-prints its readout nor re-renames its cache entry.
  (#303)
- Under `--json` the CLI never reads stdin (spec §4 / Ruling 2): any command that would otherwise
  prompt for confirmation now fails first with `confirmation required: ...` in the
  `{"error":...}` envelope, naming the flag (`-y`/`--yes`, `--force`, `-s`/`--source`) or
  positional argument that decides it non-interactively instead — never a blocking read. `lmm
game add` and `lmm auth login` have no non-interactive form yet (a flag-driven one is a
  follow-up issue) and reject `--json` outright with `this command is interactive-only and does
not support --json`; `lmm auth logout` and `lmm game detect` (via the new `--all`/`--select`)
  both already have one. (#303)
- Every lock refusal now reads the same way, with one wording per refusal _kind_. `lmm update
<mod>` (an available update, and a compile game's needed recompile), `lmm update --rollback
<mod>` and `lmm mod edit`'s re-link refusal each used to word the refusal themselves; all four
  now print the canonical text the core lock gates return. Those four gates refuse whatever
  version you name, so their remedy is "`<mod>` is locked at v`<version>` in profile
  `<profile>` - unlock with 'lmm mod unlock …' first" — moving the lock would not have helped.
  `lmm update --all`'s combined locked-skip summary reports the same `ApplyUpdate` gate for
  multiple mods at once, so it names no single mod's version and isn't this canonical sentence —
  but it dropped the same no-op "move the lock" clause, down to "`N` locked mod(s) not applied:
  `<mod>`[, `<mod>`...] - unlock to update."
  The gates that _would_ proceed at the locked version (installing, and `lmm mod edit
--version`) keep the two-remedy "move the lock with 'lmm mod lock …' or unlock with 'lmm mod
  unlock …'" wording. Where that "move the lock" remedy survives, its version argument is the
  literal placeholder `<version>`, not the concrete target version the retired "Move the lock:
  lmm mod lock -s … -p … `<mod>` 2.0" line filled in — so it is edit-then-run rather than
  paste-and-run. For the three `lmm update` branches this replaces two lines with two: a
  context line stating what is available ("Update available: 1.0 → 2.0", "Rollback available:
  2.0 → 1.0", "Recompile needed for `<mod>` (base pak updated).") followed by the refusal,
  whose own inline remedy (carrying `-s`/`-p`) supersedes the separate "Move the lock: … |
  Unlock: …" line. Those three print the refusal sentence exactly as quoted above; `lmm mod
edit` prints it as an error, so there it is prefixed with `Error: mod is locked:` the way
  every failing command's message is. The same sentence is also the `refusal` field
  (`json:"refusal,omitempty"`) on `core.UpdatePlan`/`RollbackPlan`/`RelinkPlan`, though no command
  emits those documents directly today — `lmm update --dry-run --json` on a locked mod emits its
  own hand-built result document instead, which carries no `refusal` key. (#294)
- `lmm profile apply`, `lmm profile sync`, and `lmm profile switch` no longer hide a refused or
  failed post-install/`toUpdate` profile write behind `--verbose` — today, a LOCKED profile ref
  (the record in the database moves while the profile ref does not) but also any profile
  load/save failure (e.g. the profile going missing mid-run). The warning now prints
  unconditionally to stderr as `Warning: could not update …`, and is carried on the command's
  `--json` document in `warnings` (stderr stays empty under `--json`). If the run then fails
  fatally, so that there is no result document to carry them, the `--json` error envelope
  carries them instead as `details.warnings`. It was previously a `--verbose`-only stdout note,
  so a default-verbosity run reported success with a silent database-vs-profile divergence.
  (#294)
- `lmm update --verbose`'s per-mod progress line (`n/total: <mod>`) now counts across every
  source's batch instead of restarting at 1 for each source. Checking mods from two sources used
  to print `1/3 … 3/3` then `1/2 … 2/2`; it now prints one unbroken `1/5 … 5/5`. Each source's own
  batch position (`Index`/`Total`) is unchanged internally — `UpdateCheckEvent` gains
  `GlobalIndex`/`GlobalTotal` alongside them — so only the printed numbers move; `--json` is
  unaffected (progress events are suppressed under `--json` regardless). (#283)
- Internal: `core.Service` documents and enforces a concurrency contract — query methods run
  concurrently with each other and with at most one in-flight mutation; mutations are serialized
  service-wide through a one-slot semaphore acquired with the caller's context, so a waiter is
  itself cancellable. The games map is guarded by an RWMutex and `SaveGame` replaces `AddGame`.
  (#279)
- Internal: the four ad-hoc progress mechanisms (`DeployProgress`, `DownloadProgress`, the
  `VerifyEvent` callback and the context-smuggled update-check callback) are replaced by one
  typed event stream (`EventSink`) with a fixed `{"type","data"}` wire envelope pinned by
  goldens. No user-visible change: CLI output is byte-identical. (#280)
- Internal: `internal/domain` and `internal/core`'s Plan/Result types now carry snake_case `json`
  tags, and the int enums (`LinkMethod`, `DeployMode`, `UpdatePolicy`, `VerifyTier`,
  `VerifyEventKind`, `DeployModClass`) marshal as their text names. Each type's wire shape is
  pinned by a golden (`internal/{domain,core}/testdata/json/*.golden`) so a shape change is a
  visible diff. No user-visible change: the CLI's `--json` still emits its own view structs and is
  byte-identical. (#282)
- Internal: dead `Installer.InstallBatch`/`UninstallBatch` and their result types removed;
  `ScanResult` drops its never-populated error pair; `--log-level` is now validated at flag-parse
  time (before `PersistentPreRunE`), so an invalid value is rejected on every path, including
  `--help`/`--version`/`completion`, not just paths that opened a `Service` (#284, #285)
- Internal: `core` resolves hook configuration itself — `DeployOptions`, `UninstallOptions`,
  `PurgeOptions`, `InstallOptions`, `UpdateOptions` and `RollbackOptions` no longer carry
  `Hooks`/`HookRunner`/`HookContext`; every flow resolves the merged game/profile hooks and a
  `HookRunner` from config before its first mutation. No user-visible change: CLI output is
  byte-identical. (#286)
- Internal: the file-selection policy (filter/sort by category, primary-file pick, and
  version-authoritative file resolution) existed three times - once each in `cmd/lmm/install.go`,
  `cmd/lmm/profile.go`/`deploy.go`, and `internal/core/flows.go` - hand-kept byte-identical. It now
  lives once, in `internal/core/selection.go`, exported as `core.FilterAndSortFiles` and
  `core.SelectFilesForVersion` (the primary-file pick is package-internal - see #288); the
  `cmd/lmm` copies are deleted. No user-visible change: CLI output and error text are
  byte-identical. (#287)
- Internal: `lmm install <query>`'s multi-select path no longer has an install engine of its own.
  `batchInstallMods` - 300 lines in `cmd/lmm/install.go` that hand-rolled hooks, lock gating,
  download, deploy, persistence and the merged-pak sync, bypassing `ApplyInstall` entirely - is
  replaced by `core.PlanInstallMany` plus a batch branch of `core.ApplyInstall`, sharing the
  per-mod engine the dependency path already used. `InstallPlan` gains `Batch`
  (`[]*InstallPlanEntry`), and every `ApplyInstall` now re-derives the installed-mod set its plan
  was computed against and refuses a stale one (`core.ErrStalePlan`). The install flow moved out
  of `flows.go` into `internal/core/install.go`. No user-visible change: CLI output and end state
  are byte-identical, pinned by `cmd/lmm/testdata/install_batch_golden/`. Behaviour note: because
  every mod's metadata is now fetched at plan time, `install.before_all`/`install.before_each` run
  after all of a batch's source reads rather than interleaved with them, matching the dependency
  path's existing behaviour. (#288)
- Internal: update and rollback are Plan/Apply — PlanUpdate/PlanUpdateFrom and PlanRollback compute
  lock, pin, recompile, and cache-existence gating in core; CheckGameUpdates entries carry lock
  state (loaded once per call, not once per mod); cmd no longer reads profiles for lock checks
  (#289)
- Internal: `lmm profile reorder`'s "source:modid"/bare mod-ID resolution moves into
  `core.Service.ResolveReorder`, exporting `ErrModNotInProfile`/`ErrAmbiguousModID`; `cmd/lmm`'s
  `doProfileReorder` no longer builds the mod lookup itself, and its no-args listing path reads the
  profile through `core.ProfileManager.Get` instead of `storage/config` directly. No user-visible
  change: CLI output and error text are byte-identical. (#290)
- Internal: `lmm profile apply` is Plan/Apply — the ~360-line converge engine in `cmd/lmm`
  (three-way diff, source resolution, download, deploy/replace, persistence and the merged-pak
  sync) moves into `core.PlanProfileApply`/`core.ApplyProfileApply` with a typed plan whose
  install entries are resolved against their source at plan time, an `ErrStalePlan` freshness
  guard, and progress reported through the event stream. `cmd/lmm` keeps only the prompt and the
  printed lines. No user-visible change: CLI output is byte-identical. (#290)
- Internal: `lmm profile sync` is Plan/Apply — the DB-to-profile diff engine in `cmd/lmm` (the
  add/remove/update bucket classification, the display-name lookups, and the `pm.AddMod`/
  `RemoveMod`/`UpsertMod`/merged-pak-sync application) moves into `core.PlanProfileSync`/
  `core.ApplyProfileSync`, with a missing profile.yaml recorded on the plan (`Missing`) rather than
  created at plan time, and an `ErrStalePlan` freshness guard. `cmd/lmm` keeps only the prompt and
  the printed lines. No user-visible change: CLI output is byte-identical. (#290)
- Internal: `lmm import`'s scan mode is Plan/Apply — the untracked-mod scan, the
  source-matching loop (`tryMatchSources`), the metadata backfill and the adoption engine
  (`importExistingMod`, plus a verbatim duplicate of core's own `copyFileStreaming`) move out of
  `cmd/lmm/import.go` into `core.ScanLocal`/`core.PlanAdopt`/`core.ApplyAdoptBackfill`/
  `core.ApplyAdopt`, with an `ErrStalePlan` freshness guard and progress reported through the
  event stream. The backfill is its own small apply because the pre-lift engine saved it, and
  reported its count, before the confirmation prompt and kept it on a decline. `cmd/lmm` keeps
  only the prompt and the printed lines; `Importer.ScanModPath`/`FindDuplicateMod` are now
  package-internal. No user-visible change: CLI output is byte-identical. (#291)
- Internal: `lmm import`'s archive mode is `core.ImportArchive` — the enrichment/cache-rename tail,
  the `#139` file resolution and completion marker, the `#197` retained-file fold, conflict gating,
  the `install.*` hook quartet, the deploy, the DB row, the profile ref and the merged-pak sync move
  out of `cmd/lmm/import.go` into `internal/core/import_archive.go`, with progress reported through
  the event stream. It is one mutation rather than a Plan/Apply pair because its only decision point
  (the overwrite prompt) needs a cache entry that does not exist until the archive is written, so the
  conflict confirmation stays a callback for Phase 2. `cmd/lmm/hooks.go` is deleted — its last
  callers were this tail's. No user-visible change: CLI output is byte-identical. (#291)
- Internal: `DetectedGame` moves to `internal/domain` (Steam keeps it as a type alias), with Steam
  library scanning exposed through `internal/app.DetectGames` so `internal/core` never imports a
  concrete source. `lmm game detect`'s conversion from a detected game (`gameFromDetected`) and its
  games.yaml + default-profile persistence move out of `cmd/lmm/game.go` into
  `core.GameFromDetected`/`core.ApplyGameDetect`, sharing a new
  `ProfileManager.CreateOrResetDefault` with `lmm game add`'s default-profile creation. No
  user-visible change: CLI output is byte-identical. (#292)
- Internal: `cmd/lmm`'s remaining direct `storage/config`/`source/custom` reads move behind
  `core`/`app` queries — `core.Service`/`core.ServiceConfig` gain `DefaultGame`/`SetDefaultGame`/
  `ClearDefaultGame`, `core.ProfileManager` gains `ListNames` (preserving `list --profiles`'
  tolerance of an unparseable profile that `List` would silently skip), and `SourceDefinition`
  (with its `Type*`/`*Config` types) moves from `internal/source/custom` to `internal/source`
  behind new `app.LoadSourceDefinitions`/`LoadSourceDefinitionFile`/`ConstructSource`/
  `ProbeSource` queries. `cmd/lmm`'s `boundaryAllowList` is now empty. No user-visible change: CLI
  output is byte-identical. (#292)
- Internal: the deploy flow gains a Plan/Apply pair — `core.PlanDeploy` returns a `DeployPlan`
  (per-mod link/remove file sets, the purge set, the hook list and the DeployCompile `MergePlan`)
  and `core.ApplyDeploy` carries it out, refusing a plan whose installed-mod set has moved
  (`ErrStalePlan`). `DeployProfile` stays as the Plan+Apply convenience, the flow moves out of
  `flows.go` into `internal/core/deploy.go`, and `ConvergeDeployedFiles` is unexported (verify's
  `--fix` pass was its only caller). No user-visible change to existing invocations: CLI output is
  byte-identical. (#293)
- Internal: the uninstall and purge flows gain Plan/Apply pairs — `core.PlanUninstall` resolves
  the mod (including the bare-ID first-match rule the CLI used to apply inline) and returns an
  `UninstallPlan`; `core.PlanPurge` returns the installed set the confirmation prompt counts and
  `ApplyPurge` purges that same object; both refuse a stale plan (`ErrStalePlan`).
  `UninstallMod`/`PurgeProfile` stay as the Plan+Apply conveniences, and the flows move out of
  `flows.go` into `internal/core/uninstall.go` and `internal/core/purge.go`. No user-visible change
  to existing invocations: CLI output is byte-identical. (#293)
- Internal: `SwitchPlan`/`ImportPlan` carry the stale-plan snapshot; `UpdateModVersion`/
  `ApplyModUpdate` unexported (Phase 2 close, #272)
- Internal: `core` no longer imports any concrete source package. The `file://` download gate
  (only a directory source may serve a local-file URL) now asserts a new `source.LocalFileServer`
  capability interface instead of the concrete `*custom.Directory` type; core's import-boundary
  test covers every `internal/source/*` package. No behavior change: the same sources are allowed,
  and a refusal reads the same error text. (#300)
- Internal: `Service.GetDownloadURL`, `DownloadModToCache`, `SetModFileIDs`, and `SetModVersion` —
  exported methods with zero callers anywhere in the codebase, including tests — deleted. (#301)
- Internal: core results carry structured data instead of pre-formatted display strings, so a JSON
  frontend can render them without re-parsing English. `InstallResult.Installed/Skipped/Failed`
  become `[]InstalledRef` (source ID, mod ID, name, version, reason), `InstallPlan.DependencyWarnings`
  becomes `[]DependencyWarning`, `UpdateApplyResult.Applied` splits into
  `Mod`/`Name`/`FromVersion`/`ToVersion`/`Changelog` plus a `Status` enum (`UpdateStatus`:
  `updated`, `up_to_date`, `skipped`, `recompiled`, `recompile_available`, `available`,
  `rolled_back`) and a raw `Reason`, and `RollbackResult` adopts the same `Status`/`Reason` pair.
  CLI output is byte-identical. (#301)
- Internal: the read-only commands gain core query types — `internal/core/queries.go` adds
  `ModList`/`ModListing` (`ListMods`), `StatusReport`/`GameSummary` (`Status`),
  `GameStatus`/`ProfileSummary` (`GameStatus`), `SearchReport`/`SearchHit` (`Search`),
  `GameListEntry` (`ListGameEntries`) and `VerifyReport` (`VerifyReport`), and
  `internal/app` adds `SourceInfo`/`SourceInfos` (the source-definition half of `lmm source
list` is only visible to app). Each carries snake_case json tags and a recorded golden. The
  joins `lmm list`, `lmm status`, `lmm search`, `lmm game list`, `lmm source list` and `lmm
verify` used to assemble inside the CLI now live in core, and their plain-text renderers read
  the query types; CLI output — text and JSON — is unchanged. (#301)
- Internal: the last three frontend callbacks leave `core` (spec §4 "no callbacks into the
  frontend from Apply"). `InstallOptions.ConfirmConflicts` and
  `ImportArchiveOptions.ConfirmConflicts` are replaced by `AcceptConflicts bool` (implied by
  `Force`): an unaccepted file conflict is now the typed `*core.ConflictError` (wrapping
  `domain.ErrFileConflict`, carrying `[]core.Conflict`), returned before any deploy, DB write or
  profile write, and the frontend prompts and re-runs Apply. `ProfileImportOptions.ConfirmInstall`
  becomes `Install bool`, decided from `ImportPlan.NeedsRedownload`/`Missing`. New
  `internal/core/errors.go` also adds `ErrConfirmationRequired` and `ErrInteractiveOnly`. (#303)
- Internal: `lmm mod edit`, `lmm mod files`, and `lmm mod lock`/`unlock`/`set-update`/`convert`
  get core flows instead of driving `ProfileManager`/DB writes directly from `cmd/lmm`.
  `core.PlanRelinkMod`/`ApplyRelinkMod` back `mod edit` (a metadata-only edit or a re-link to a
  different source/mod ID); `core.ModFiles` backs `mod files`; `core.Service.SetModLock`/
  `ClearModLock`/`SetModUpdatePolicy`/`SetModConvertPaks` all return a `*core.ModSettingResult`
  (the mod's full post-write lock/policy/pak-conversion snapshot). No user-visible change: CLI
  output is byte-identical. (#303)
- Internal: `internal/core/flows.go` and `flows_test.go` are gone, completing the series of moves CHANGELOG
  entries for Units H, I, J, and M already recorded. The remaining flows move into subject-named
  files: `EnableMod`/`DisableMod` (`mod_toggle.go`), `DeployPhase` and its
  `String`/`MarshalText`/`UnmarshalText` (`phases.go`), `PlanProfileSwitch`/`ApplyProfileSwitch`
  (`switch.go`), `PlanImport`/`ApplyImport` (`profile_import.go`), plus `runHook` (`hooks.go`),
  `sameFileIDSet` (`selection.go`), and `orderByProfile` (`deploy.go`). The seven
  `flows_*_test.go` files are renamed to match: `deploy_compile_readout_test.go`,
  `deploy_selfheal_test.go`, `install_directory_test.go`, `install_test.go`, `rollback_test.go`,
  `update_test.go`, `variant_exclusivity_test.go`. No user-visible change: CLI output is
  byte-identical. (#305)
- Internal: `core.Service`'s fixture-only exports resolved (Ruling 10): `DownloadMod`, `GetInstaller`, and
  `PurgeMergedPak` are unexported — production never called the exported forms, only test
  fixtures did — with `cmd/lmm` tests re-seeded through the real `PlanInstall`/`ApplyInstall`,
  `PlanDeploy`/`ApplyDeploy`, and `PurgeProfile` flows instead. `SaveInstalledMod`, `GetGameCache`,
  `SyncMergedPak`, `SaveFileChecksum`, `AvailableModVersions`, `IsSourceAuthenticated`, `ScanLocal`,
  `Logger`, `SetModLinkMethod`, `SetModDeployed`, and `DeleteInstalledMod` stay exported as
  documented test-seed APIs or frontend-facing queries, each with a doc comment stating why. No
  behavior change. (#305)
- Internal: Every exported identifier in `internal/core`, `internal/domain`, and `internal/app` now carries
  a doc comment, enforced by a `go/ast` test in each package
  (`TestExportedIdentifiersHaveDocComments`) rather than tracked as a one-time count.
  `cmd/lmm`'s import-boundary test drops its allow-list mechanism (empty since Task 1) in favor of
  asserting the hard rule directly; a new `TestDetailsTypesAreCovered` (Unit P review finding M7)
  requires every type implementing the `--json` error envelope's `Details() any` extension point
  to have a named test pinning its wire shape. No behavior change. (#305)

### Fixed

- SQLite pragmas (foreign_keys, WAL, busy_timeout) now apply to every pooled connection via the DSN; `:memory:` databases use a single connection. (#271)
- An invalid `--log-level` value's error message no longer carries the misattributed
  `initializing service:` prefix (rejecting the flag never actually opened a service). It now
  reads `Error: invalid --log-level "<value>": expected off, error, warn, info, or debug`
  identically on every path (#284, #285)
- `lmm profile sync` on a profile with nothing to sync no longer touches the merged pak. Creating
  a missing profile.yaml through an otherwise-empty sync reached the merged-pak sync, whose
  zero-enabled-mods branch uninstalls the game's existing merged pak; it now stops after creating
  the profile, matching `lmm profile apply`'s no-changes behaviour. (#290)
- Profile-level hook overrides survive profile mutations (`config.SaveProfile` now serializes
  `hooks:`) (#295)
- Profile-level hook overrides survive `lmm profile export` → `lmm profile import`. The exported
  document carries a `hooks:` block in the same encoding a profile file uses, so an explicitly
  disabled hook (present but empty) stays disabled instead of coming back as "inherit from the
  game". An export of a profile with no overrides is byte-identical to before, and an export
  recorded by an earlier lmm (no `hooks:` key) still imports. (#296)
- `lmm profile reorder`'s ambiguous-mod-ID error lists the matching `source:modid` candidates
  sorted by source ID, instead of Go's randomized map iteration order. (#298)
- `lmm profile sync`'s add/remove/update buckets are listed in a fixed, deterministic order
  (additions in the order the mods were installed, updates and removals in profile order),
  instead of Go's randomized map iteration order. (#298)
- `lmm status` and `lmm game list` order games by game ID, instead of Go's randomized map
  iteration order. (#299)
- `lmm uninstall --dry-run` and `lmm purge --dry-run` no longer announce a merged-artifact effect
  that would not happen. Both plans now model it (`merged_artifact: {action, path}` under `--json`,
  the key omitted entirely when there is nothing to do — `omitzero`, phase-end review Minor 7;
  previously present as `null`), computed from the merge sources the operation would leave
  behind and whether the artifact is actually deployed, so a compile-game uninstall of a mod that
  contributes nothing to the merge — or a purge with nothing merged yet — prints no artifact line.
  `lmm import <archive> --json`'s `merged_pak_synced` is likewise set from the sync having run and
  succeeded rather than from the game's deploy mode. (#304, #306)
- `lmm status --game X --json` no longer swallows a failure to list the game's profiles into an
  empty-profiles document; it now fails loud, matching the plain-text path (which already did).
  Only reachable when the profiles directory exists but can't be read - a missing directory still
  returns no error. The plain-text path also drops one duplicated `listing profiles:` prefix
  (`ProfileManager.List` already wraps that error with it). (#301)
- Cancellation mid-mutation can no longer leave a mod in the DB but absent from its profile (or
  vice-versa). Every profile-file write that completes an already-applied database mutation —
  install, dependency install, archive import, adopt, profile import, profile switch, uninstall,
  `purge --uninstall`, and `mod edit`'s relink/version paths — now finishes even when the run is
  cancelled, and the run then stops with the cancellation instead of absorbing it into a per-mod
  warning and reporting success, with no exception among the flows above: `adopt`'s own last-match
  case initially missed this (the completing write finished, but the caller still counted the
  cancellation as a per-mod failure and reported success), closed in the same fix wave. The lock
  and profile-list gates that treat an unreadable profile
  as "no lock" / "no profiles" report a cancelled read rather than degrading open, and the lazy
  profile-existence check no longer reports "the profile is fine" for a read it could not answer.
  One non-cancelled-path exception: that same check now also catches a profile YAML that exists
  but cannot be loaded (a parse error or a permissions error), so a batch install into such a
  profile now aborts up front instead of running to completion and reporting success with per-mod
  warnings, and the single-mod/import paths gain a `Warning: could not create profile: …` line
  ahead of the pre-existing `could not update profile` warning. A cancellation between that
  profile-existence check and the DB row it completes is now reported (Skipped/Failed) instead of
  silently dropped, so a first-ever install's dependency can no longer land in the database and
  vanish from the result at once. A cancelled run also now prints `Cancelled.` to stderr in plain
  mode before exiting 2 (`--json` stays silent, as `--json` output is otherwise unaffected). No
  other output changes on any non-cancelled path. (#305)
- `lmm purge --dry-run --json` on an empty profile now emits the `PurgePlan` document instead of
  a `PurgeResult`, matching `import`/`profile switch`/`profile apply`/`profile sync`'s identical
  dry-run/json ordering (phase-end review Important 1). Plain-text `--dry-run` on an empty profile
  is unchanged. (#306)
- `lmm import <archive> --dry-run` previews the import instead of performing it. The archive form
  now has a real Plan/Apply pair (`core.ImportArchivePlan`): the archive's table of contents is
  READ — `archive/zip` natively, `7z l -slt` for `.7z`/`.rar` — never extracted, so a preview
  names the mod, its resolved version, the files it would deploy, any file it would overwrite,
  the merged-artifact effect on a compile game and the hooks that would run, while leaving no DB
  row, no deployed file, no cache entry and nothing in the staging root. `--dry-run --json` emits
  the plan document. Between the close wave and this change the flag was rejected outright with
  an error; scan-mode `--dry-run` is unaffected throughout. Sharing that listing step with the
  ingest also closes a small gap: `.7z`/`.rar` members are now screened for lmm's reserved
  namespace and zip-slip path traversal at plan time, the same as zip members always were —
  previously this check ran only after extraction, and only against reserved names, never path
  traversal. (#314)
- `lmm import <archive>`'s readout prints once per import, and the ID it prints is the ID saved
  (**Ruling 18**). Accepting a file-conflict prompt used to re-run the whole import, reprinting
  the `Fetching metadata…` / `Mod:` / `Source:` / `ID:` / `Version:` / `Files:` block between the
  prompt and `Deploying to game directory…`; for an archive with no `--id`, that re-run also
  minted a _second_ local mod ID, so the ID on screen was not the one written to the database.
  The command now plans once, prompts from the plan, and applies once. No other line changes; a
  forced or conflict-free import is byte-identical. One line does move relative to the readout,
  though: the `-v` `Warning: could not rename cache entry: …` note (raised while applying, not
  while planning) used to print before it and now prints after — old order `Fetching metadata…` /
  `Warning: could not rename cache entry: …` / `Mod: …` / `Warning: could not check conflicts: …`,
  new order `Fetching metadata…` / `Mod: …` / `Warning: could not rename cache entry: …` /
  `Warning: could not check conflicts: …`. (#314)
- The reasons an archive cannot be imported at all now report themselves without the
  `import failed:` prefix, because they are settled while planning rather than mid-ingest
  (**Ruling 18**). Exactly five lines change: `import failed: unsupported archive format: .txt` →
  `unsupported archive format: .txt`; `import failed: <compile-source resolver error>` →
  `<compile-source resolver error>`; `import failed: validating Bear_Mount.exmodz: …` →
  `validating Bear_Mount.exmodz: …`; `import failed: extracting archive: reserved name detected:
…` → `reserved name detected: …` (same for `path traversal detected: …`); and
  `import failed: extracting archive: 7z command not found: install p7zip-full to extract .7z and
.rar files` → the same sentence with neither prefix. Listing a `.7z`/`.rar` adds one new error
  class, `7z listing failed: …`. All of them still exit non-zero and render the `{"error", …}`
  envelope under `--json`. A failure of the ingest itself still reads `import failed: …`. (#314)

### Removed

- The interactive terminal UI (`lmm tui`) and the `internal/tui` package. v2's interfaces are the CLI and, in a later release, a local web UI (`lmm serve`). Design: `docs/plans/2026-08-27-v2-core-refactor-design.md`.

## [1.30.1] - 2026-08-08

### Changed

- Internal: all Unreal/Icarus format knowledge (base-pak location, pak
  fingerprinting, the `.pak` convert test, the native `.exmodz` test,
  merge-source kinds, and the merged/restored artifact filenames) moved
  out of `internal/core` and behind the `source.MergeCompiler` interface,
  which now states the complete contract a second compile-mode game would
  implement (#256). The one user-visible change: rejecting a mixed
  pak+exmodz install selection now names the two colliding files
  (e.g. `Mod_P.pak and Mod.exmodz`) instead of the generic wording.

### Fixed

- TUI: warnings emitted by successful updates in an apply-updates batch are
  now readable — they render as a trailing section (blank separator, one
  line per distinct warning) inside the same "update results" overlay that
  lists each update's ✓/✗ line, instead of being folded into an aggregate
  the overlay never showed. Identical warnings repeated across the batch
  (a merged-pak recompile re-emits the same profile-level asset-conflict
  diagnostics for every update that triggers it) are deduped on exact text,
  and the status line's one-row `(N warnings)` count matches the deduped
  section (#259).
- Deploy output on a compile-mode game (Icarus) no longer presents merged
  mods as individual deployments (#255). The header drops the misleading
  `using <method>` claim, each mod's `✓` line is labeled by how its content
  actually reaches the game directory — `(merged)` for merge participants,
  `(raw)` for a conversion-opted-out pak, unlabeled for ordinary loose-file
  mods — and a post-sync footer finally names the one artifact that really
  deployed (`Merged N mod(s) → zzz_LMM_Merged_P.pak`, with a
  `(N deployed raw)` count when conversions fell back). The TUI's deploy
  status line reports the same readout (`Deployed N mod(s) — merged N → …`).
  `Deployed: N` still counts merge participants, and non-compile deploy
  output is unchanged, byte for byte.
- TUI: a mutation that completes with two or more warnings now auto-opens a
  scrollable overlay listing every warning in full, instead of collapsing
  them to an unreadable `(N warnings)` status suffix — on merged-pak games
  (Icarus) those warnings are the only report of cross-mod asset conflicts
  anywhere in the app. The one-row `(N warnings)` summary stays on the
  status line, and a single warning still renders inline with no overlay
  (#253).
- Icarus: cache pruning no longer deletes a converted pak's deployable
  copy when a sibling file of the same mod+version is downloaded or
  re-ingested (#250). The copy is unclaimed by design while the merged
  pak carries the mod's content, but it is still the designated
  raw-fallback artifact — pruning it left a later conversion opt-out or
  merge failure deploying nothing, silently. Pruning now exempts any
  unclaimed file whose content matches a retained source, and the raw
  fallback additionally self-heals entries already damaged by released
  versions: the deployable copy is restored from the retained source
  (under its original archive name for imports, or a `<mod-id>_P.pak`
  name for catalog downloads, where the original name is unrecoverable)
  and redeployed.
- Uninstalling a mod whose cache entry is absent is now a no-op removal
  (still clearing tracking rows and sweeping empty directories) instead of
  an error. In particular, a DeployCompile profile with zero merge sources
  and no merged-pak cache entry — the steady state after disabling the
  last exmodz/pak mod, or a profile that never merged — no longer fails
  every subsequent sync/deploy/purge with a loud
  `removing merged pak: ... no such file or directory` error (#260).

## [1.30.0] - 2026-08-08

### Added

- Icarus: prebuilt `.pak` mods are now converted into the merged pak at
  merge time — rebased onto the game's current `data.pak` — instead of
  deploying raw and being shadowed by it (#221). Paks that embed a
  `data.EXMOD` manifest convert exactly; others are diff-derived against
  the current base. Irreconcilable paks produce a per-mod error and fall
  back to raw deploy.
- `lmm mod convert <mod-id> <on|off>` and a per-game `convert_paks`
  `games.yaml` setting (both on by default) control conversion — a pak
  ships raw if either is off; the TUI toggles the per-mod setting with
  `m`.
- `lmm verify` reports `conversion_failed` and `needs_reingest` statuses
  (plus `fixed_needs_reingest` once `--fix` repairs the latter); `--fix`
  re-ingests pre-existing pak installs into the conversion pipeline.
- JSON additions: `convert_paks` in `lmm list --json`, `lmm mod show
--json`, and `lmm game list --json`.
- TUI: a Health screen (screen `6`) — a Dashboard `Health: ...` signal
  line summarizing the local-tier verify scan, a full-width
  STATUS/MOD/FILE/VERSION/NOTE table combining verify findings (OK rows
  included, per checked file) with file-conflict rows
  (`CONFLICT`/`STALE CONFLICT`, tinted by severity) with a compact
  detail strip below, an
  explicit full (network) check (`c`), a batch fix (`F`) that runs the
  same repairs as `lmm verify --fix` behind a confirmation summarizing
  what it will attempt, by finding type, and `D` to deploy in place for a
  stale conflict; the standalone Conflicts screen (formerly screen `6`)
  is retired — its file-conflict reporting now lives here (#224).
  The verify engine itself (per-file/per-mod checks, `--fix` repairs, the
  deploy-convergence sweep) is now extracted into `internal/core`, shared
  unchanged between the CLI and this new TUI surface.
- TUI: a mod details view with full `lmm mod show` parity — `enter` on a mod
  in Installed Mods or Search opens it, `esc` returns. Opens instantly from
  data already on screen and fills in description, category, and links from
  the source in the background, so it stays useful offline (#86).

### Changed

- `internal/unrealpak` is now the standalone module
  [`github.com/DonovanMods/go-unrealpak`](https://github.com/DonovanMods/go-unrealpak),
  consumed as a dependency. No behavior change — the package moved verbatim,
  with its history, and gained a `unrealpak` CLI (`info`/`list`/`cat`/
  `extract`/`build`) that lmm does not use. (#170)
- `lmm mod show` now strips HTML from a mod's description before printing
  it, using the same cleaner the update flow and TUI already share — raw
  `<p>` tags and `&amp;` entities no longer reach the terminal (#86).
  `--json` output is unchanged: it stays the raw source markup.

### Fixed

- Pak-manifest reconciliation now claims only the cache member(s) actually
  belonging to the pak file ID being marked — matched by content against
  that file ID's retained source — instead of the entry-wide cache union
  (#241). A mod carrying a second pak-kind file ID (no real mod ships one
  today) would previously have had the first file ID's pak silently
  attributed to the second as well, feeding wrong provenance into
  deploy/prune decisions.
- TUI: the help panel's `+N more` collapse now drops rows by priority
  instead of purely by position, so a screen's headline action (e.g. the
  Search screen's `enter: open mod details` row, formerly last in its
  group and swallowed entirely at a normal 100x30 terminal) stays
  visible however large its group grows (#234). Only the current
  screen's (or pushed content's) group gets this treatment — other
  screens' rows still collapse positionally.
- Source adapters no longer alias `Description` to a copy of `Summary`
  (#235). CurseForge, custom `directory`, and custom `manifest` mods —
  none of which carry a full description — now leave the field empty, so
  `lmm mod show` and the TUI details view stop rendering the identical
  paragraph under both headings; custom `api` sources keep a mapped
  `description` but no longer fall back to the summary when it is
  unmapped. JSON contract change: `lmm mod show --json`'s `description`
  field is now empty for those sources instead of duplicating `summary`.
- Closed the test gap that let #237 ship: a new env-gated golden test
  (`ICARUS_GOLDEN_MOD_DIR`) compiles a real dual-form mod's `.EXMODZ`
  with the full pipeline and asserts the resulting pak's virtual paths
  match the author's own published `_P.pak` — the first check of
  compiled output against an artifact not produced by lmm itself
  (#242). Test-only; skips unless pointed at a local dual-form mod.
- Install-plan dependency resolution now fetches dependencies using the
  LMM game ID (translated through `source_ids` like every other fetch)
  instead of feeding the source's own stamped game ID back into the
  lookup (#230). The old path only worked while no configured LMM game
  ID happened to equal another game's upstream domain (e.g. a game
  literally named `skyrimspecialedition`); on such a collision,
  dependencies were silently looked up in the wrong game and reported
  missing.
- `lmm mod show` and the TUI mod details view no longer report a mod as
  not installed when the installed-state lookup fails outright (e.g. a
  locked or corrupted database) — only a genuine "no such row" omits the
  Installed section; any other database error now surfaces as an error
  (#236).
- `lmm verify --fix` now resolves a missing file's re-download URL against
  the source-mapped game, not the LMM game ID (#228). On a game whose
  `source_ids` maps to a different upstream domain (e.g. NexusMods
  `skyrim-se` → `skyrimspecialedition`), the repair previously queried the
  wrong game and failed. Sources with empty or identity mappings
  (directory, local) were unaffected.
- `lmm verify` no longer skips the deployment-convergence sweep when the
  profile has no installed mods, so stray lmm-deployed files (e.g.
  dangling cache symlinks left after uninstalling everything) are
  reported — and removed with `--fix` (#217).
- `lmm verify --json` now emits `"files": []` instead of `null` in the
  one corner where a filtered (`lmm verify <mod-id>`) run matched no
  rows, matching the empty-profile path's existing empty-array shape
  (#224).

## [1.29.1] - 2026-08-07

### Fixed

- Icarus mods that bundle prebuilt assets in their `.exmodz` now place those
  assets where the game actually looks. Every real `.exmodz` nests its
  `.uasset`/`.uexp` files in a directory named after the mod, and that wrapper
  was being carried into the compiled pak — so each bundled asset landed one
  directory below the base asset it was meant to override and the game
  silently ignored it. A mod whose entire effect is assets (no data-table
  rows) installed, deployed, and verified OK while doing nothing at all. The
  wrapper is now stripped when it matches the manifest name, leaving bundles
  that already store Content-relative paths untouched. Affects both
  single-mod compiles and the merged pak; reinstall or redeploy affected
  Icarus mods to pick up corrected artifacts. (#237)

## [1.29.0] - 2026-08-05

### Added

- Icarus mods that publish both a prebuilt pak and a mergeable `.exmodz`
  now install the `.exmodz` by default everywhere (TUI, `--yes`, batch and
  profile installs, and the CLI chooser's default), with descriptive
  chooser labels. Selecting both variants together is rejected — they are
  alternate forms of the same mod; `--file pak` remains the escape hatch
  for installing the prebuilt pak alone. (#211)
- `lmm game list` — a table of every configured game (ID, name, install path, mod path, deploy mode, and a compact `source:id` rendering of its sources), marking the default game (see `lmm game show-default`) and pointing at `lmm game add`/`lmm game detect` when nothing is configured yet. Supports `--json` like `list`/`search`/`source list` (#205)
- `make build` (and `make`) now stamp `git describe --tags --dirty` into the binary via `-ldflags -X`, so `lmm --version` on a dev build self-identifies (e.g. `1.28.0 (dev: v1.28.0-2-g140e3c6-dirty)`) instead of showing the same plain `1.28.0` as a release build — the confusion this fixed came up during the v1.28.0 cycle. A build exactly on the release tag (clean) still shows the plain version; a plain `go build`/`go test` (no ldflags) is unaffected. The static `version` var and man pages are untouched by construction (#208)
- `lmm verify` now detects stale deployments — game-directory files lmm
  deployed that no installed mod still provides, and dangling symlinks
  left pointing into the mod cache — and `verify --fix` removes them
  (provenance-gated: only lmm-attributed files are ever touched). (#168, #212)

### Changed

- `lmm game detect` now marks a game already present in `games.yaml` as `[configured]` (mirroring `search`'s `[installed]` convention) and excludes it from the default "all" selection, since it needs no re-offering. It stays listed, and naming its number explicitly still selects it — the same re-add/repair path `lmm game add` has always used unconditionally (games.yaml entry + a fresh empty default profile, replacing any existing one) (#205)

### Fixed

- Deploy no longer links cache files that no download manifest claims: stale
  per-mod paks left behind by pre-v1.28 exmodz installs were being deployed
  alongside the merged pak, double-applying their table edits. One
  `lmm deploy` cycle now removes such links, download commits prune the
  stale files, and the conflict scanner ignores them. Legacy cache entries
  without recorded manifests keep their exact previous behavior. (#210)
- Batch installs (dependencies present) now resolve and validate the primary
  mod's `--version`/`--file` pins before the `install.before_all` hook runs,
  so an unhonorable selection can no longer fire user hooks first. (#214)

## [1.28.0] - 2026-08-02

### Added

- CLI output is now colorized by default when stdout is a terminal, extending the existing `colorGreen`/`colorRed`/`colorYellow` accent mechanism (previously only used by `deploy`/`verify`) with a full 4-color palette (green/yellow/red/cyan, plus bold/dim) across `list`, `status`, `search`, `update`, `conflicts`, and `mod show`. Table headers are bold+cyan. `lmm list` tints the whole row identically with or without `-v` (the row-tint decision is a single shared helper keyed on the mod's actual state, not the display flag): green for the common enabled+deployed case, yellow for enabled-but-undeployed, dim for disabled. `search` tints an installed mod's whole row green; `update`'s POLICY column colors per row. `status`/`mod show` color their values, not just the odd count: `lmm status -g <game>`'s active profile and per-profile "(active)" marker are green, mod/profile counts are cyan, Link Method is cyan, Last Deploy is green (or dim when never deployed); `mod show`'s Version fields are cyan and its Update policy is colored per state (green for auto, yellow for pinned); `conflicts`' stale winner suffix is yellow; and the existing `✓`/`✗` success/failure markers extend to `update` and `mod`'s confirmation lines. Detection is TTY-aware (piped/redirected output stays plain) and layers on top of the existing `--no-color` flag and `NO_COLOR` env var (presence-only per no-color.org), which continue to work unchanged; `--json` output is never colored. Table color is applied only to already-tabwriter-padded text (accented headers, whole-row tints, or a table's genuinely last column) — never to interior cell values before they reach `text/tabwriter`, which pads columns by raw byte length and would misalign them (#112, #193)
- **Icarus built-in mod source** (`internal/source/icarus`): a public, unauthenticated Firestore-backed catalog (Project Daedalus) — `lmm search`/`install`/`update` work against it like NexusMods/CurseForge. A `.exmodz` mod file is validated and its row-level table diffs retained via a new, game-agnostic `internal/unrealpak` PAK reader/writer and the new `deploy_mode: compile` game setting (see the merged-pak bullet below for how it actually deploys); a plain `.pak` file from the same catalog is unaffected and deploys through the existing extract/copy pipeline unchanged. Base data tables are read directly from the installed game's own `data.pak`, so a merge always matches the installed game version and works entirely offline; `internal/unrealpak` reads both the stored and the Zlib-compressed entries that pak contains, using only the standard library (#136, #175)
- `lmm game detect` now recognizes Icarus (Steam App ID `1149460`) and generates a complete `games.yaml` entry for it (`deploy_mode: compile`, `sources: {icarus: icarus}`) — no more hand-editing `games.yaml` to get started. The known-games schema (`steam-games.yaml`, built-in or your own override) gained two optional fields, `deploy_mode` and `sources`, generalizing detection beyond NexusMods-only games; every existing entry is unaffected (#177)
- Custom `api` sources' `search` endpoint gains `{category}`/`{tags}` path placeholders, fed from `SearchQuery.Category`/`.Tags` (URL-escaped; multiple tags comma-joined) — previously these were silently dropped with no way for a declarative source to express category/tag filtering. A definition whose `search` path omits the new placeholders is unaffected: the values are computed but never substituted in, matching today's behavior exactly (#120)
- Compiled mods (`deploy_mode: compile`, e.g. Icarus) with more than one enabled `.exmodz` mod now compose correctly instead of silently shadowing each other: every enabled mod's table-row diffs are applied sequentially, in profile load order, into ONE merged `zzz_LMM_Merged_P.pak` per profile (named to mount last, so it always wins over a plain prebuilt `.pak`'s own table override) — two mods patching different fields of the same row, or entirely different rows of the same table, both survive; only a genuine same-field conflict is last-wins, and a bundled-asset path collision (which can't compose) is last-wins with a loud warning. This also fixes "the Friday problem" (a weekly base-pak refresh silently reverting a mod's patched tables, with nothing to notice): the merge regenerates whenever the enabled-mod set, load order, a mod's version, or the base pak itself changes. `lmm update` (CLI and TUI) reports a "recompile needed" row for the profile's merged pak (additive `--json` field `recompile_needed`/`reason`) alongside normal version updates; applying it regenerates and redeploys — pinned mods' diffs recompile normally, and a LOCKED mod's diff still participates in every merge (a lock pins that mod's own version, not the profile's merged pak). Installing/importing a `.exmodz` now only validates and retains it (a per-mod compiled pak is no longer generated or deployed); a plain prebuilt `.pak` mod, and every non-`deploy_mode: compile` game, is completely unaffected. `lmm verify` gains a matching "RECOMPILE NEEDED" warning row (`stale_compile`) for the profile's merged pak (#136, #175, #196, #197)

### Changed

- `lmm list` now displays mods in the profile's load order (the same order `lmm profile reorder` sets and the TUI's mod list already showed) instead of DB install order (`installed_at`) — the visible order is now the order that actually decides merge precedence for a `deploy_mode: compile` game. A mod installed but missing from the load order is still shown, never silently dropped, placed first (lowest priority). README and `docs/configuration.md` gain a "Merge precedence" paragraph explaining that later-in-load-order mods win conflicting table-row _fields_ (a per-field upsert; untouched fields from earlier mods survive) while bundled assets are whole-file last-wins with a warning, and that `lmm profile reorder` regenerates the merged pak immediately (#201)
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

[Unreleased]: https://github.com/DonovanMods/linux-mod-manager/compare/v2.0.0...HEAD
[2.0.0]: https://github.com/DonovanMods/linux-mod-manager/compare/v1.30.1...v2.0.0
[1.30.1]: https://github.com/DonovanMods/linux-mod-manager/compare/v1.30.0...v1.30.1
[1.30.0]: https://github.com/DonovanMods/linux-mod-manager/compare/v1.29.1...v1.30.0
[1.29.1]: https://github.com/DonovanMods/linux-mod-manager/compare/v1.29.0...v1.29.1
[1.29.0]: https://github.com/DonovanMods/linux-mod-manager/compare/v1.28.0...v1.29.0
[1.28.0]: https://github.com/DonovanMods/linux-mod-manager/compare/v1.27.1...v1.28.0
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
