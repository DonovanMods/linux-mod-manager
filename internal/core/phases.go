package core

import "fmt"

// DeployPhase identifies what DeployProfile is doing for the mod named in
// a flow event (or, for DeployPurging, for the purge pass as a whole),
// letting callers render phase-appropriate UI without needing to know how
// a deploy is actually carried out.
type DeployPhase int

const (
	// DeployPurging fires once, before any purge-phase mod is touched -
	// from a deploy --purge pass or from PurgeProfile (#61) - when there
	// is at least one installed mod to purge. Total is the number of mods
	// being purged; Index and ModName are zero/empty.
	DeployPurging DeployPhase = iota
	// DeployBeforeEachSkipped: install.before_each failed for ModName: the
	// mod is skipped (added to DeployResult.Skipped). Detail is the reason.
	DeployBeforeEachSkipped
	// DeployRedownloading: ModName's cache entry is missing; DeployProfile
	// is re-fetching it from source.
	DeployRedownloading
	// DeployDownloading: a file for ModName is downloading. Percent is the
	// 0-100 completion (only reported once the source declares a total
	// size, matching the pre-extraction CLI's progress callback gating).
	DeployDownloading
	// DeployDownloadFailed: a file for ModName failed to download; the mod
	// is skipped. Detail is the reason.
	DeployDownloadFailed
	// DeployDownloadDone fires once, after a cache-miss mod's redownload
	// loop finishes without error, mirroring the pre-extraction CLI's
	// unconditional `fmt.Println() // Clear progress line` immediately
	// after the download loop (git show b2ad559:cmd/lmm/deploy.go) - it
	// terminates DeployDownloading's carriage-returned progress line with a
	// real newline before the mod's own DeployDeployed line prints. Unlike
	// its ApplyProfileSwitch analog (SwitchDownloadDone), which fires on
	// both success and failure since doProfileSwitch's equivalent Println
	// sat unconditionally after its own loop, redeployFromSource's failure
	// path returns immediately via a DeployDownloadFailed event instead (see
	// below) without reaching this point - so this phase covers the
	// success path only.
	DeployDownloadDone
	// DeploySkipped: ModName was skipped for a reason other than a hook or
	// download failure (fetch failure, no files available, file-selection
	// failure, or an outright deploy/install failure). Detail is the reason.
	DeploySkipped
	// DeployDeployed: ModName was (re)deployed successfully.
	DeployDeployed

	// --- Fix wave 1: every remaining Warnings/Notes diagnostic gets an
	// event at its exact point of occurrence, restoring the pre-extraction
	// CLI's console positioning (see DeployResult's doc comment for the
	// full Warnings/Notes -> event mapping and task-3-report.md's "Fix
	// wave 1" entry for the review findings these fix). ---

	// DeployBeforeAllForced fires once, immediately, when install.before_all
	// (a deploy) or uninstall.before_all (a --purge pass) fails and Force is
	// set: the pre-extraction CLI printed this warning as the very first
	// line of output, before anything else (the "Purging..."/"Deploying..."
	// header included) - so this event always precedes DeployPurging and
	// any other event. No mod is in scope (Index/Total/ModName/ModID are
	// zero); Detail matches the DeployResult.Warnings entry verbatim.
	DeployBeforeAllForced
	// DeployNote fires wherever DeployProfile appends an entry to
	// DeployResult.Notes for a specific mod during the main deploy loop
	// (a failed undeploy-before-redeploy, a failed SetModLinkMethod, or a
	// failed SetModDeployed), at the exact point it happens - always
	// before that same mod's own DeployDeployed event, matching the
	// pre-extraction CLI's inline ordering. ModName/ModID identify the
	// mod; for the latter two diagnostics, whose historical text carries
	// no mod identity at all, the event's ModName/ModID are the ONLY way
	// to attribute the diagnostic to a mod.
	DeployNote
	// DeployWarning fires wherever DeployProfile appends an entry to
	// DeployResult.Warnings other than a DeployBeforeAllForced one: a
	// failed install.after_each hook (ModName/ModID set), a failed
	// install.after_all hook, or a failed ApplyProfileOverrides (neither
	// has a mod in scope). The pre-extraction CLI printed the overrides
	// warning immediately once computed, then its batched hook warnings
	// (after_each in mod order, then after_all) right after - so
	// DeployProfile emits the overrides DeployWarning (if any) first, then
	// the after_each/after_all ones, reproducing that print order without
	// changing when each check actually runs (see DeployProfile's body).
	DeployWarning
	// PurgeWarning fires wherever a purge appends an entry to its
	// result's Warnings (DeployResult for deploy --purge, PurgeResult for
	// PurgeProfile): a skipped uninstall.before_each mod (deploy mode
	// only - PurgeProfile reports that skip as PurgeModSkipped instead;
	// fires inline, per mod, as it happens), or a failed
	// uninstall.after_each/after_all hook (fires after the whole purge
	// loop has finished, in mod order then after_all - mirroring the
	// pre-extraction CLIs, which accumulated these and printed them
	// together, after every per-mod line, via printHookWarnings).
	PurgeWarning
	// PurgeNote fires wherever a purge appends a per-mod entry to its
	// result's Notes (a failed undeploy, a failed SetModDeployed(false),
	// or PurgeProfile --uninstall's record-delete/profile-remove
	// failures), inline, immediately after that operation - mirroring the
	// pre-extraction CLIs' --verbose-gated "⚠ "/"Note: " lines.
	PurgeNote
	// PurgeComplete fires once, after a non-empty purge has finished
	// everything (including its own hook warnings) - before DeployProfile
	// moves on to gathering mods to deploy, or as PurgeProfile's terminal
	// event. It carries no data; a deploy --purge caller wanting
	// byte-identical pre-extraction output prints exactly one blank line
	// here - purgeDeployedMods's own final `fmt.Println()`, which the
	// initial extraction had misplaced immediately after the purge header
	// instead of at the end of the purge phase (`lmm purge` prints
	// nothing for it).
	PurgeComplete

	// --- Task 4: ApplyProfileSwitch progress events, extending this same
	// DeployPhase enum (per the task brief: "reuse the progress carrier and
	// its phase-constant pattern - extend, don't fork") rather than
	// introducing a parallel SwitchProgress/SwitchPhase pair.
	//
	// v2 Phase 2 Unit J (#290): ApplyProfileApply emits this same family.
	// Every line doProfileApply printed is worded identically to its
	// doProfileSwitch counterpart, so a duplicate ProfileApply* family
	// would be twelve constants of copied text; Scope.Op (OpProfileApply
	// vs OpSwitch) is what tells the two flows apart on the wire. Changing
	// any wording below therefore changes BOTH flows - which is correct:
	// they are the same lines.
	// ApplyProfileSwitch is a behavior-preserving extraction of
	// cmd/lmm/profile.go's doProfileSwitch;
	// every phase below corresponds to exactly one of doProfileSwitch's
	// fmt.Print* call sites - see the task report for the full mapping.
	// Unlike DeployProfile, doProfileSwitch never printed to stderr at all,
	// so none of these have a Warnings-bucket counterpart: every
	// SwitchResult diagnostic below is a Note (--verbose-gated stdout). ---

	// SwitchDisableNote fires for each of the disable loop's two possible
	// per-mod diagnostics (a failed Uninstall, then a failed SetModEnabled),
	// mirroring doProfileSwitch's "  Warning: failed to undeploy %s: %v" /
	// "  Warning: failed to update %s: %v" - both --verbose-gated stdout
	// prints. Detail carries the historical "Warning: " prefix baked in; a
	// caller wanting byte-identical output prints
	// `if verbose { fmt.Printf("  %s\n", p.Detail) }`.
	SwitchDisableNote
	// SwitchDisabled fires once a mod's disable step has finished
	// (regardless of whether SwitchDisableNote fired for it) -
	// doProfileSwitch always disables the DB row and always prints
	// "  ✓ Disabled: %s" even when the undeploy/DB update above it failed.
	// ModName is set.
	SwitchDisabled
	// SwitchEnableNote mirrors SwitchDisableNote for the enable loop's two
	// diagnostics (a failed Install, then a failed SetModEnabled). Unlike
	// the disable loop, a failed Install is fatal FOR THAT MOD ONLY: the mod
	// is skipped (no SwitchEnabled event follows) - see doProfileSwitch's
	// `continue` after the Install failure branch.
	SwitchEnableNote
	// SwitchEnabled fires once a mod has been successfully deployed (and
	// enabled, or deployed but its SetModEnabled bookkeeping failed - see
	// SwitchEnableNote), mirroring "  ✓ Enabled: %s".
	SwitchEnabled
	// SwitchInstalling fires once, before the install loop, only when there
	// is at least one mod to install (Total = len(SwitchPlan.ToInstall)),
	// mirroring doProfileSwitch's "\nInstalling missing mods...".
	SwitchInstalling
	// SwitchInstallingMod fires once per mod to install, before it is even
	// fetched - SourceID/ModID are the only identity available at this
	// point, mirroring "  Installing %s:%s...".
	SwitchInstallingMod
	// SwitchInstallError fires for any of the install loop's mod-fatal-only
	// failure reasons (fetch, get-files, no-files, file-selection, deploy,
	// or save), each already worded to match its historical text exactly
	// (Detail is printed verbatim as "    Error: %s"). Unlike
	// DeployProfile's DeploySkipped, these are NOT accumulated into any
	// SwitchResult slice - doProfileSwitch never printed a final
	// skipped-count summary for profile switch, so there is nothing to
	// accumulate beyond the live event.
	SwitchInstallError
	// SwitchDownloading mirrors DeployDownloading for the install loop's
	// download progress (Percent set, gated the same way: only once the
	// source declares a total size).
	SwitchDownloading
	// SwitchDownloadFailed fires when a file download fails; Detail is
	// "download failed: %v". A caller wanting byte-identical output prints a
	// blank line then "    Error: %s" with Detail - see SwitchDownloadDone's
	// doc comment for why the blank line isn't included here.
	SwitchDownloadFailed
	// SwitchDownloadDone fires once per install-loop mod after its download
	// loop finishes, on both success and failure - doProfileSwitch's
	// `fmt.Println()` after the loop runs unconditionally either way. When
	// the #96 cache-first guard skips the download entirely, this phase is
	// skipped with it (there is no download readout to terminate). A
	// caller wanting byte-identical output prints a bare blank line here;
	// combined with SwitchDownloadFailed's own leading blank line, a failed
	// download reproduces the original's blank/error/blank sequence, and a
	// successful one reproduces its single trailing blank line.
	SwitchDownloadDone
	// SwitchInstalled fires once a to-be-installed mod has been fetched,
	// downloaded, deployed, and saved to the DB, mirroring "    ✓ Installed:
	// %s". ModName is set (mod.Name, now known).
	SwitchInstalled
	// SwitchInstallNote is retired: #294 (Ruling 5) promoted
	// ApplyProfileApply's UpsertMod refusal to SwitchInstallWarning, and
	// its class extension (Task 13b) did the same for ApplyProfileSwitch's
	// identical refusal. The constant and its wire name are kept so an
	// UnmarshalText of a previously recorded "switch_install_note" still
	// round-trips.
	SwitchInstallNote
	// SwitchInstallWarning is ApplyProfileApply's AND ApplyProfileSwitch's
	// UpsertMod refusal since #294 (Ruling 5; the switch flow followed in
	// Task 13b, closing the apply/sync-only gap Task 13's Scope Call #1
	// flagged) - promoted out of the --verbose-only bucket because a
	// silently unrecorded profile ref is exactly the DB-vs-profile
	// divergence #143 exists to make visible. Detail is the raw text, no
	// "Warning: " prefix; it is ALSO appended to
	// ProfileApplyResult.Warnings / SwitchResult.Warnings, which is where
	// the CLI prints it from (`fmt.Fprintf(os.Stderr, "Warning: %s\n", w)`)
	// - a frontend that renders this event live must therefore not print
	// Warnings too, or every refusal appears twice.
	SwitchInstallWarning

	// --- Phase 5b Task 2: ApplyInstall progress events, restored to
	// byte-for-byte per-path fidelity in Fix wave 1 (see
	// task-2-report.md's "Fix wave 1 (dep-path fidelity)" entry for the full
	// review trace). ApplyInstall reproduces the pre-extraction CLI's own
	// TWO divergent execution engines EXACTLY, gated on
	// len(plan.Dependencies):
	//
	//   - Empty (the STRICT/no-deps path): the primary uses doInstall's own
	//     single-mod code unchanged from Task 2 - Force-gated
	//     before_all/before_each, Install-or-Replace (incl. the
	//     reinstall-cache-transaction for a same-version reinstall),
	//     interactive/--file file selection and the blocking
	//     conflict-confirm prompt are the CALLER's job (plan.Files/
	//     plan.Conflicts), SaveFileChecksum, --skip-verify. See
	//     InstallDownload*/InstallChecksumComputed/InstallExtracting/
	//     InstallDeploying/InstallDone below.
	//   - Non-empty (the BATCH path): EVERY mod in [Dependencies...,
	//     primary] uses batchInstallMods' lenient mechanics IDENTICALLY -
	//     the primary is NOT special-cased at all here, matching the
	//     pre-extraction CLI's own behavior of delegating the WHOLE list,
	//     target included, to batchInstallMods whenever there were
	//     dependencies to install (doInstall's "if len(modsToInstall) > 1"
	//     early return, before any single-mod code - including file
	//     selection and the conflict prompt - ever ran). before_each is
	//     NEVER Force-gated (a failure always just skips that one mod and
	//     continues, primary included), no Replace path (always a fresh
	//     Install; a same-key existing mod is uninstalled+cache-deleted
	//     first), no interactive file selection (always the
	//     primary-or-first file, re-resolved per mod - plan.Files is never
	//     consulted), conflicts are a non-blocking inline warning (never a
	//     prompt). See InstallDepInstalling below onward.
	InstallBeforeAllForced

	// InstallBeforeEachForced fires when the PRIMARY mod's install.before_each
	// hook fails and Force is set (a forced warning, not a fatal error) -
	// mirrors doInstall's own before_each Force-gate exactly. ModName/ModID
	// identify the primary. ONLY fires in the STRICT (no-deps) path - in the
	// BATCH path the primary's before_each is never Force-gated at all (see
	// InstallDepSkipped), matching batchInstallMods exactly.
	InstallBeforeEachForced

	// InstallDepInstalling fires once per mod in the BATCH path's combined
	// [Dependencies..., primary] list - dependency OR primary alike -
	// before before_each even runs, mirroring batchInstallMods' own
	// "\n[%d/%d] Installing: %s v%s\n" byte-for-byte (Fix wave 1 restored
	// the exact text and the primary's participation; Task 2's original
	// design fired this for dependencies only, with different wording -
	// see task-2-report.md). Index/Total count across the WHOLE combined
	// list (len(plan.Dependencies)+1), matching batchInstallMods' shared
	// counter; ModVersion carries the version for the restored "v%s" text.
	InstallDepInstalling
	// InstallDepReinstalling fires, unconditionally (not verbose-gated),
	// when a BATCH-path mod (dependency or primary) already has an existing
	// installed row for (SourceID, ID, Profile) - mirroring
	// batchInstallMods' unconditional "  Removing previous installation...".
	// The existing install is then uninstalled and its cache entry deleted
	// - never a Replace/reinstall-cache-transaction (that mechanism is
	// STRICT-path only).
	InstallDepReinstalling
	// InstallDepFileSelected fires once a BATCH-path mod's downloadable
	// files have been fetched, filtered/sorted, and reduced to the
	// primary-or-first file (never interactive, never --file) - mirroring
	// batchInstallMods' "  File: %s\n". File identifies which, for the
	// CLI's own displayFileLabel call.
	InstallDepFileSelected
	// InstallDepDownloading mirrors batchInstallMods' per-mod download
	// progress readout (Percent only, gated on a known total size - no
	// byte-count fallback line, unlike the STRICT path's
	// InstallDownloading). Fires for a dependency OR the primary alike.
	InstallDepDownloading
	// InstallDepSkipped fires whenever ANY BATCH-path mod (dependency or
	// primary alike) is skipped for any reason (hook failure, fetch/files/
	// download/deploy/save failure) - unconditional, never Force-gated,
	// matching batchInstallMods exactly. Detail already carries the
	// restored, failure-type-specific, fully-prefixed line text verbatim
	// ("Skipped: install.before_each hook failed: %v" for a hook failure;
	// "Error: <reason>" for every other failure type - batchInstallMods
	// used different wording per failure type, never a uniform "Skipped:
	// <name>: <reason>" - see task-2-report.md's Fix wave 1 for the
	// before/after); a caller wanting byte-identical output prints
	// `fmt.Printf("  %s\n", p.Detail)`. Index/Total count across the whole
	// combined list, matching InstallDepInstalling.
	InstallDepSkipped
	// InstallDepDownloadDone fires, unconditionally (success OR failure
	// alike), immediately after a BATCH-path mod's DownloadMod call
	// returns - mirroring batchInstallMods' unconditional `fmt.Println()`
	// right after the download call, which precedes InstallDepSkipped's
	// own restored "\n  Error: download failed: %v\n" leading blank line
	// on failure. A caller wanting byte-identical output prints a bare
	// `fmt.Println()` here.
	InstallDepDownloadDone
	// InstallDepConflictWarning fires when a BATCH-path mod's files
	// (already downloaded/cached at this point) would overwrite files from
	// another installed mod and Force is NOT set - a non-blocking,
	// informational warning only (batchInstallMods never prompts in the
	// BATCH path, primary included - the blocking plan.Conflicts prompt is
	// STRICT-path only). Detail is "%d file conflict(s) - will overwrite".
	InstallDepConflictWarning
	// InstallDepInstalled fires once a BATCH-path mod (dependency or
	// primary) has been fully installed (downloaded, deployed, saved,
	// profile-upserted) - mirroring batchInstallMods' restored
	// "  ✓ Installed (%d files)\n" (Fix wave 1: Task 2's original design
	// used the mod's name instead of its file count - see
	// task-2-report.md). FilesExtracted carries the count.
	InstallDepInstalled

	// InstallDownloadStarted fires once per one of the PRIMARY's selected
	// files (plan.Files) in the STRICT (no-deps) path only, before it
	// begins downloading - mirrors downloadSelectedFiles'
	// "\n[%d/%d] Downloading %s...\n" (or, for a single file,
	// "\nDownloading %s...\n"). File identifies which (for the CLI's own
	// displayFileLabel call); Index/Total count among plan.Files. The BATCH
	// path has no equivalent "starting" event - its download progress
	// begins directly at InstallDepDownloading.
	InstallDownloadStarted
	// InstallDownloading mirrors the STRICT path's primary per-tick
	// download progress - Downloaded/TotalBytes/Percent carry the raw
	// numbers so the CLI can reproduce its exact byte-count/percent
	// readout (see DownloadEvent's doc comment on those fields). The
	// BATCH path's per-mod download progress fires InstallDepDownloading
	// instead (Percent only, no byte-count fallback).
	InstallDownloading
	// InstallDownloadDone fires once a STRICT-path file's download attempt
	// finishes - success OR failure alike, mirroring downloadSelectedFiles'
	// `fmt.Println()` that runs unconditionally right after the download
	// call returns, before branching on its error. The BATCH path's
	// equivalent is InstallDepDownloadDone.
	InstallDownloadDone
	// InstallDownloadFailed fires when a STRICT-path (primary) file
	// download fails; Detail carries "download failed: %v" (the CLI checks
	// Detail for the "third-party downloads" substring itself, mirroring
	// doInstall's own check, to print the manual-install notice using the
	// plan's own Mod.SourceURL/ID - already in the CLI's enclosing scope,
	// so it isn't duplicated onto the event). Always fatal - the BATCH
	// path's equivalent (InstallDepSkipped) never is.
	InstallDownloadFailed
	// InstallChecksumComputed fires once a checksum has been computed and
	// !SkipVerify, for BOTH paths: the STRICT path's primary file(s)
	// (Index/Total/File populated, matching InstallDownloadStarted) and
	// the BATCH path's per-mod checksum (Index/Total/ModName populated
	// instead, File unset - mirroring batchInstallMods' own
	// "  Checksum: %s\n", fired once per mod right after its download
	// succeeds). Detail carries the full (untruncated) checksum either
	// way; the CLI applies its own truncateChecksum.
	InstallChecksumComputed
	// InstallCompiling fires instead of InstallExtracting, once per file,
	// when a DeployCompile game's ".exmodz" file was validated and retained
	// for a later merge (#190 item 1; #197: ingest no longer compiles a
	// per-mod pak - the real merge happens once, batched across the whole
	// profile, via Service.syncMergedPak) - the generic "Extracting to
	// cache..." wording is misleading here either way, since nothing is
	// extracted. File identifies the source file (for displayFileLabel);
	// Detail is unset (there is no per-file compiled output filename left
	// to announce under the merged-only model). The BATCH path never
	// prints this (it has no DeployCompile support and no equivalent
	// status line at all).
	InstallCompiling
	// InstallExtracting mirrors doInstall's unconditional "Extracting to
	// cache..." status line, fired once after the STRICT-path primary's
	// download(s) finish, before Install/Replace - unless every downloaded
	// file was compiled instead (InstallCompiling fires in that case, one
	// event per compiled file, and this is skipped entirely). The BATCH
	// path never prints this (batchInstallMods had no equivalent status
	// line).
	InstallExtracting
	// InstallDeploying mirrors "Deploying to game directory...", fired once
	// right before the STRICT-path primary's Install/Replace. The BATCH
	// path never prints this.
	InstallDeploying
	// InstallDone fires once the STRICT-path primary has been fully
	// installed (deployed, saved, checksum stored, profile upserted). The
	// BATCH path's equivalent (for every mod, primary included) is
	// InstallDepInstalled.
	InstallDone

	// InstallNote fires wherever ApplyInstall appends an entry to
	// InstallResult.Notes (a failed profile-create, UpsertMod,
	// reinstall-cache-transaction commit, old-cache cleanup, or - BATCH
	// path only - a failed Uninstall/cache-Delete while removing a
	// mod's previous installation, see InstallDepReinstalling) - the
	// --verbose-gated stdout bucket, mirroring DeployNote/SwitchInstallNote.
	// Detail equals the Notes entry verbatim; ModName/ModID identify the
	// mod when relevant.
	InstallNote
	// InstallWarning fires wherever ApplyInstall appends an entry to
	// InstallResult.Warnings other than an InstallBeforeAllForced/
	// InstallBeforeEachForced one: a failed SaveFileChecksum (unconditional
	// stderr, matching doInstall exactly - NOT verbose-gated), or an
	// install.after_each/after_all hook failure (deferred - see
	// ApplyInstall's doc comment - emitted after the whole run, mirroring
	// DeployWarning/printHookWarnings' batched timing).
	InstallWarning

	// --- Phase 5b Task 3: ApplyUpdate progress events, extending this same
	// DeployPhase enum (matching Task 2's own "extend, don't fork"
	// precedent). ApplyUpdate is a behavior-preserving extraction of
	// cmd/lmm/update.go's applyUpdate; every phase below corresponds to one
	// of applyUpdate's own console print sites - see the task report for the
	// full mapping. Unlike ApplyInstall, applyUpdate never ran an
	// install.before_all/install.after_all pair at all - each CLI-side
	// update-loop iteration calls applyUpdate once, per mod, with no
	// enclosing before_all/after_all of its own - so there is no
	// UpdateBeforeAllForced counterpart here.

	// UpdateDownloading mirrors applyUpdate's own download-progress readout
	// ("\r  Downloading: %.1f%%", verbose-gated in the pre-extraction CLI) -
	// Percent only, gated on a known total size, matching
	// DeployDownloading/InstallDepDownloading's own gating (no raw
	// byte-count fallback - applyUpdate never printed one).
	UpdateDownloading
	// UpdateDownloadDone fires once, only after EVERY file in the update's
	// download step has downloaded successfully - mirroring applyUpdate's
	// own `if verbose { fmt.Println() }`, which terminates the
	// carriage-returned UpdateDownloading progress line. A download failure
	// returns immediately instead (see ApplyUpdate's doc comment), so -
	// like DeployDownloadDone, and unlike InstallDownloadDone - this covers
	// the success path only. A caller wanting byte-identical pre-extraction
	// output prints this ONLY under --verbose (the historical gate lived on
	// the print itself, not just the progress ticks).
	UpdateDownloadDone
	// UpdateBeforeEachForced fires when EITHER of the update's two
	// Force-gated hooks - uninstall.before_each (old version) or
	// install.before_each (new version) - fails with Force set, mirroring
	// applyUpdate's own two, textually-near-identical (only the hook name
	// differs) "Warning: %s hook failed (forced): %v" unconditional stderr
	// prints. Detail already carries the full, hook-specific message
	// verbatim.
	//
	// Reused, extend-don't-fork (Phase 6b Task 5): ApplyRollback fires this
	// SAME phase for its own two Force-gated before_each hooks -
	// uninstall.before_each (the version being rolled back FROM) and
	// install.before_each (the version being rolled back TO) - mirroring
	// doUpdateRollback's own two near-identical Force checks exactly. The
	// two flows are never in progress at once, so the shared phase carries
	// no ambiguity; Detail alone (plus ModName/ModID) tells a caller which
	// hook and which mod failed.
	UpdateBeforeEachForced
	// UpdateWarning fires for either of the update's two after_each hook
	// failures - uninstall.after_each (old version) or install.after_each
	// (new version) - mirroring applyUpdate's own hookErrors/
	// printHookWarnings pair, fired right after both hooks have run
	// (Replace already succeeded), in hook-run order (uninstall.after_each,
	// then install.after_each) - unlike DeployWarning/InstallWarning's
	// end-of-whole-run deferral, since applyUpdate itself prints these
	// immediately, well before its own DB-update steps below.
	//
	// #143 additionally fires this phase for file-SELECTION warnings (a
	// stored file whose version label left it unresolvable - see
	// updateAmbiguousFileWarning). Those come from a pure decision made
	// BEFORE any download, so unlike the hook failures above they can
	// precede every side effect - the phase no longer implies that Replace
	// or the hooks have run. That early emission is deliberate: the fact is
	// already known, and surfacing it up front means the user sees it even
	// if a later download fails (ApplyUpdate's partial-result convention
	// returns accumulated diagnostics alongside the error either way).
	//
	// Reused (Phase 6b Task 5): ApplyRollback fires this SAME phase for its
	// own two always-non-fatal after_each hooks, in the same
	// uninstall-then-install order, mirroring doUpdateRollback's own
	// hookErrors/printHookWarnings pair exactly.
	UpdateWarning
	// UpdateNote fires when SetModLinkMethod fails after a successful
	// update - the sole --verbose-gated diagnostic in applyUpdate,
	// mirroring "  Warning: could not update link method: %v" (2-space
	// indent, prefix baked into Detail, matching SwitchDisableNote/
	// SwitchEnableNote's own convention).
	//
	// Reused (Phase 6b Task 5): ApplyRollback fires this SAME phase for its
	// own SetModLinkMethod failure, mirroring doUpdateRollback's
	// textually-identical verbose-gated print exactly.
	UpdateNote

	// --- PurgeProfile progress events (#61): the standalone `lmm purge`
	// command's flow, extending this same enum.
	// PurgeProfile also reuses DeployBeforeAllForced, DeployPurging,
	// PurgeNote, PurgeWarning, and PurgeComplete; the two phases below are
	// purge-command-only and NEVER fire during a deploy --purge pass, whose
	// event stream is unchanged. ---

	// PurgeModSkipped fires when a mod's uninstall.before_each hook fails
	// during `lmm purge`: the mod is skipped entirely (stays deployed) and
	// counts toward PurgeResult.Skipped. Index/Total/ModName/ModID are set;
	// Detail carries "uninstall.before_each hook failed: <err>" - the text
	// doPurge printed after "  Skipped <name>: " (the matching Skipped
	// entry is the same Detail behind a "<name>: " prefix). Contrast with
	// deploy --purge, which reports the equivalent skip as a PurgeWarning.
	PurgeModSkipped
	// PurgeModPurged fires when a mod finishes purging - at doPurge's
	// "  ✓ <name>"/succeeded++ point, after that mod's uninstall.after_each
	// attempt. Index/Total/ModName/ModID are set. Note a best-effort
	// undeploy or SetModDeployed failure (PurgeNote) does NOT suppress
	// this; only a before_each skip or an --uninstall record-delete
	// failure does.
	PurgeModPurged

	// --- Phase 6b Task 8: ApplyImport progress events, extending this same
	// DeployPhase enum (matching every prior flow's own "extend, don't
	// fork" precedent). ApplyImport is a behavior-preserving extraction of
	// cmd/lmm/profile.go's doProfileImport; every phase below corresponds to
	// one of its own console print sites - see the task report for the full
	// mapping. Unlike ApplyProfileSwitch's install loop, which gives each
	// failure reason (fetch/get-files/no-files/file-selection/deploy/save) a
	// DISTINCT phase, doProfileImport printed every one of those with the
	// SAME "    Error: %s\n" shape - so ImportModFailed below covers all of
	// them, Detail carrying whichever reason text applies (fidelity forbids
	// sharing ApplyProfileSwitch's own install loop verbatim for exactly
	// this reason - see the task report's sharing-decision entry). ---

	// ImportSaved fires once, immediately after the profile is saved
	// (ProfileManager.ImportWithOptions succeeds), mirroring doProfileImport's
	// "\n✓ Imported profile: %s\n". ModName carries the saved profile's name.
	ImportSaved
	// ImportInstalling fires once, only when the install loop is actually
	// about to run (downloads pending, NoInstall unset and Install set),
	// mirroring "\nDownloading and installing mods...\n".
	// Total is the number of mods about to be attempted (len(toDownload)).
	ImportInstalling
	// ImportModInstalling fires once per mod in the combined
	// [NeedsRedownload..., Missing...] download list, before it is even
	// fetched - mirroring "  Installing %s:%s...\n". SourceID/ModID are the
	// only identity available at this point (ModName is set once the mod is
	// fetched, for every LATER event concerning this same ref); Index/Total
	// count across the whole combined list, matching ApplyProfileSwitch's
	// SwitchInstallingMod.
	ImportModInstalling
	// ImportDownloading mirrors the per-mod download-progress readout ("\r
	// Downloading: %.1f%%") - Percent only, gated on a known total size,
	// matching every other flow's own gating.
	ImportDownloading
	// ImportDownloadDone fires once per mod whose download loop actually
	// ran (success OR failure alike), immediately after that loop finishes
	// - mirroring doProfileImport's own unconditional `fmt.Println()` right
	// after the download loop, which precedes ImportModFailed's own leading
	// blank line on failure (see ImportModFailed). When #138's cache-first
	// guard skips the download entirely (target version already fully
	// marked in cache), this phase is skipped with it - the same shape as
	// SwitchDownloadDone under ApplyProfileSwitch's #96 guard - so there is
	// no download readout to terminate. A caller wanting byte-identical
	// output prints a bare `fmt.Println()` here.
	ImportDownloadDone
	// ImportModFailed fires for ANY of the download loop's mod-skipping
	// failure reasons - a failed GetMod, GetModFiles, an empty file list, a
	// file-selection error, a failed DownloadMod, a failed installer.Install,
	// or a failed SaveInstalledMod - mirroring doProfileImport's uniform "
	// Error: %s\n" (Detail already carries the reason text verbatim: "failed
	// to fetch mod: %v", "failed to get files: %v", "no downloadable files",
	// the file-selection error's own message, "download failed: %v",
	// "deploy failed: %v", or "save failed: %v"). The download-failure
	// variant is preceded by its own extra blank line in the pre-extraction
	// CLI (printed inside the download loop, before the unconditional
	// ImportDownloadDone one after it) - a caller wanting byte-identical
	// output detects this the same way InstallDownloadFailed's own doc
	// comment describes (checking Detail's text, here for a
	// "download failed:" prefix) and prints a bare blank line first. Always
	// non-fatal - the loop always continues to the next ref, matching
	// failedCount++; continue.
	ImportModFailed
	// ImportModInstalled fires once a to-be-installed mod has been fully
	// installed (downloaded, deployed, saved, profile-upserted) - mirroring
	// "    ✓ Installed: %s\n". ModName is set (mod.Name, now known).
	ImportModInstalled
	// ImportNote fires when UpsertMod (recording the profile's FileIDs after
	// a successful install) fails - the sole --verbose-gated diagnostic in
	// the install loop, mirroring "    Warning: could not update profile: %v"
	// (4-space indent, matching ApplyProfileSwitch's own SwitchInstallNote
	// convention).
	ImportNote

	// --- #255: compile-mode deploy readout, extending this same enum
	// (the established "extend, don't fork" convention above). ---

	// DeployMergeSynced fires once per DeployProfile on a DeployCompile
	// game, after the post-loop merged-artifact sync succeeds with a
	// merged artifact in place. It does not fire when the profile has no
	// merge participants (nothing merged, nothing to report) or when the
	// sync itself fails (that path emits a DeployWarning instead). Total
	// carries the number of mods whose content the merged artifact
	// carries; Detail names the artifact file
	// (source.MergeCompiler.MergedArtifactName - the format is the
	// source's business, never core's, #256); RawFallbacks counts
	// participant mods that fell back to an individual raw deploy (failed
	// conversion). No single mod is in scope (Index/ModName/ModID are
	// zero). The same readout is recorded on DeployResult
	// (MergedArtifact/MergedMods/RawFallbacks) for callers with no
	// progress stream.
	DeployMergeSynced

	// --- v2 Phase 2 Unit H (#288): three phases that exist so the BATCH
	// install engine can emit DATA where its two frontends' frozen wordings
	// differ. `lmm install <query>`'s multi-select path (batchInstallMods
	// before the lift) and `lmm install`'s dependency path (doInstallBatch)
	// print the same three facts with different text; core cannot pick one
	// without breaking the other's byte-identity, so each renders its own
	// sentence from the same event. Appended at the end of the enum so no
	// existing phase's numeric value moves. ---

	// InstallLockRefusal fires when a BATCH-path mod is skipped because its
	// profile ref is LOCKED at another version. Detail is the refusal
	// SENTENCE ONLY - LockedRefRefusalError's text minus its ErrModLocked
	// prefix (see lockedRefRefusalMessage) - because the multi-select path
	// prints exactly that ("  Skipped: <sentence>") while the dependency
	// path prints the wrapped error ("  Skipped: mod is locked:
	// <sentence>"). InstallResult.Skipped keeps the FULL wrapped error text
	// either way, so a caller reading the result (rather than the stream)
	// still gets the sentinel-prefixed message every other lock gate
	// produces.
	InstallLockRefusal
	// InstallChecksumSaveFailed fires when a BATCH-path mod's
	// SaveFileChecksum fails - non-fatal, the mod stays installed. Message
	// carries the reason with no prefix at all ("failed to save checksum:
	// ..."), matching InstallResult.Warnings' own no-baked-in-prefix
	// convention: the multi-select path prints it INDENTED ("  Warning:
	// %s") and the dependency path flush ("Warning: %s"). Distinct from
	// InstallWarning purely for that indent - the STRICT path's own
	// checksum failure still uses InstallWarning.
	InstallChecksumSaveFailed
	// InstallMergedPakSyncFailed fires when ApplyInstall's unconditional
	// end-of-install merged-pak sync returns a hard error. Message is the
	// RAW error text, with no leading phrase, because the two frontends
	// word it differently ("Warning: syncing merged pak: %s" on the
	// single-mod/dependency paths, "Warning: could not sync merged pak: %s"
	// on the multi-select one). InstallResult.Warnings keeps its own
	// "syncing merged pak: %v" entry unchanged, and
	// InstallResult.MergedPakSyncFailed records the same fact for callers
	// with no progress stream. The sync's non-fatal WARNINGS (as opposed to
	// a hard failure) still arrive as ordinary InstallWarning events - both
	// frontends print those identically.
	InstallMergedPakSyncFailed

	// --- v2 Phase 2 Unit J (#290): ApplyProfileSync progress events.
	// doProfileSync never printed to stderr for its three per-item loops
	// (only its end-of-apply merged-pak sync did, via ProfileSyncResult.
	// Warnings, exactly like ApplyProfileApply's own), so all three notes
	// below are --verbose-gated stdout, 2-space indent, "Warning: " baked
	// into Detail - a caller wanting byte-identical output prints
	// `if verbose { fmt.Printf("  %s\n", p.Detail) }`. ---

	// SyncAddNote fires when pm.AddMod fails for a ToAdd entry, mirroring
	// doProfileSync's "  Warning: %v" (the bare error, no ref prefix).
	SyncAddNote
	// SyncRemoveNote fires when pm.RemoveMod fails for a ToRemove entry,
	// mirroring doProfileSync's "  Warning: %v" (the bare error, no ref
	// prefix) - same wording as SyncAddNote, kept as its own phase so a
	// caller inspecting the event stream can tell which loop produced it.
	SyncRemoveNote
	// SyncUpdateNote is retired: #294 (Ruling 5) promoted the ToUpdate
	// loop's UpsertMod refusal to SyncUpdateWarning. The constant and its
	// wire name are kept so an UnmarshalText of a previously recorded
	// "sync_update_note" still round-trips.
	SyncUpdateNote
	// SyncUpdateWarning fires when pm.UpsertMod fails for a ToUpdate entry
	// (today only a LOCKED ref, #143) - Detail is the raw
	// "could not update %s:%s: %v" text, no "Warning: " prefix, and it is
	// ALSO appended to ProfileSyncResult.Warnings, which is where the CLI
	// prints it from. Same double-print caveat as SwitchInstallWarning.
	SyncUpdateWarning

	// --- v2 Phase 2 Unit K (#291): ApplyAdoptBackfill/ApplyAdopt progress
	// events. Every Detail below is the printed line MINUS its leading
	// indent, following SyncAddNote's convention, so a byte-identical
	// frontend prints `fmt.Printf("<indent>%s\n", detail)` and nothing else.
	// The indent and the --verbose gating differ per phase; see each
	// constant. ---

	// AdoptBackfillNote fires when one backfill candidate's metadata could
	// not be refreshed - the source fetch failed, or the save did. Detail is
	// "<mod name>: metadata fetch failed: <err>" or "<mod name>: metadata
	// save failed: <err>"; both render the same way (--verbose-gated stdout,
	// 2-space indent), and both leave the row untouched.
	AdoptBackfillNote
	// AdoptBackfilled fires for each row whose metadata was refreshed and
	// saved. Detail is "✓ <mod name>: metadata updated (author: <author>)",
	// --verbose-gated stdout at a 2-space indent.
	AdoptBackfilled
	// AdoptDuplicateSkipped fires when an untracked entry duplicates an
	// already-adopted or already-installed mod. Detail is
	// `⊘ <file>: skipped (duplicate of "<name>")` - unconditional stdout,
	// 2-space indent.
	AdoptDuplicateSkipped
	// AdoptAdopted fires for each successfully adopted entry. Detail is
	// "✓ <mod name>" - unconditional stdout, 2-space indent.
	AdoptAdopted
	// AdoptFailed fires when an entry could not be adopted (its cache write
	// or DB save failed). Detail is "✗ <file>: <err>" - unconditional
	// stdout, 2-space indent.
	AdoptFailed
	// AdoptNote is a per-entry diagnostic that did NOT stop the adoption:
	// the completion marker could not be stamped, the profile ref could not
	// be upserted, or the merged-pak sync itself failed. Detail is
	// "Warning: could not ..." - --verbose-gated stdout at a 4-space indent
	// (one level deeper than the per-entry line it follows).
	AdoptNote
	// AdoptSyncWarning carries one warning produced BY a successful
	// merged-pak sync. Detail is the bare warning text - unconditional
	// STDERR, rendered as "Warning: <detail>". AdoptResult.Warnings holds the
	// same strings for non-streaming callers; a frontend renders one or the
	// other, never both.
	AdoptSyncWarning

	// --- v2 Phase 2 Unit K (#291) Task 19: ImportArchive progress events -
	// `lmm import <archive>`. As above, every Detail is the printed line
	// MINUS its leading indent (and minus any "Warning: " the frontend adds
	// itself), so a byte-identical frontend prints
	// `fmt.Printf("<indent>%s\n", detail)` and nothing else. The archive
	// readout is emitted as text rather than as one structured payload
	// because its eight optional lines sit at two indent levels; the same
	// facts are ALSO on ImportArchiveResult (Mod/LinkedSource/AutoDetected/
	// Deployed) for a frontend that renders a finished result instead of a
	// live stream. The two forced-hook lines reuse InstallBeforeAllForced/
	// InstallBeforeEachForced, whose wording and rendering are already
	// identical here. ---

	// ImportArchiveFetching fires before the --id metadata fetch. Detail is
	// "Fetching metadata from <source>..." - stdout, PRECEDED BY A BLANK
	// LINE, no indent (`fmt.Printf("\n%s\n", detail)`).
	ImportArchiveFetching
	// ImportArchiveDetected opens the mod-detection readout, once the
	// archive is cached and any enrichment has been folded in. Detail is
	// "Mod: <name>" - stdout, preceded by a blank line, no indent.
	ImportArchiveDetected
	// ImportArchiveDetail is one readout field line following
	// ImportArchiveDetected, in emission order: "Source: <id>", "ID: <id>",
	// "Version: <v>" (omitted for the "unknown" sentinel), "Author: <a>"
	// and "URL: <u>" (each omitted when empty), "(auto-detected from
	// filename)" (only when the identity came from the filename pattern),
	// and always "Files: <n>". Stdout at a 2-space indent.
	ImportArchiveDetail
	// ImportArchiveDeploying fires immediately before the deploy step.
	// Detail is "Deploying to game directory..." - stdout, preceded by a
	// blank line, no indent.
	ImportArchiveDeploying
	// ImportArchiveWarning is an unconditional diagnostic: an unmapped
	// source, a failed metadata fetch, a failed source-file resolution, a
	// failed completion-marker stamp, a failed merged-pak sync, or one
	// warning produced BY a successful sync. Detail carries no prefix -
	// STDERR, rendered as "Warning: <detail>". Every entry is ALSO on
	// ImportArchiveResult.Warnings verbatim, so a streaming frontend must
	// render one or the other, never both.
	ImportArchiveWarning
	// ImportArchiveNote is a --verbose-gated diagnostic printed at NO
	// indent: the cache-rename failure and the conflict-check failure.
	// Detail carries its own "Warning: " prefix (matching InstallResult.
	// Notes' convention), so a byte-identical frontend prints
	// `if verbose { fmt.Printf("%s\n", detail) }`. Mirrored on
	// ImportArchiveResult.Notes.
	ImportArchiveNote
	// ImportArchiveProfileNote is the same kind of --verbose-gated note at a
	// 2-space indent - the profile-create and profile-upsert failures, which
	// the pre-lift CLI printed indented under the mod. Detail likewise
	// carries its own "Warning: " prefix; also mirrored on Notes.
	ImportArchiveProfileNote

	// --- ApplyRelinkMod progress events (v2 Phase 3 Task 10, #303):
	// `mod edit`'s re-link fetches metadata from the target source and
	// leaves a couple of non-fatal profile-write diagnostics, mirroring
	// import_archive.go's step/warn/note closures. ---

	// RelinkFetching fires once, before ApplyRelinkMod fetches metadata from
	// a re-link's non-local target source - unconditional stdout, mirroring
	// doModEdit's pre-extraction "Fetching metadata from %s...\n". Detail is
	// the full line ("Fetching metadata from <source>...").
	RelinkFetching
	// RelinkProfileNote is ApplyRelinkMod's --verbose-gated profile-write
	// diagnostic, reused across its three sites (a failed RemoveMod of the
	// old ref, a failed UpsertMod after a re-link, a failed UpsertMod after
	// a version-only edit) - Detail carries its own "Warning: " prefix
	// baked in already, matching doModEdit's own historical text exactly. A
	// caller wanting byte-identical output prints
	// `if verbose { fmt.Printf("%s\n", p.Detail) }`.
	RelinkProfileNote
	// RelinkWarning is ApplyRelinkMod's unconditional stderr diagnostic - a
	// failed metadata fetch, or a merged-pak sync failure/warning - Detail
	// is the raw text (no prefix); a caller wanting byte-identical output
	// prints `fmt.Fprintf(os.Stderr, "Warning: %s\n", p.Detail)`.
	RelinkWarning
)

// deployPhaseNames maps each DeployPhase to its wire name (snake_case of
// the constant without the type prefix rules — the constant's own name,
// lower-snake). Keep in declaration order.
var deployPhaseNames = [...]string{
	DeployPurging: "deploy_purging", DeployBeforeEachSkipped: "deploy_before_each_skipped", DeployRedownloading: "deploy_redownloading",
	DeployDownloading: "deploy_downloading", DeployDownloadFailed: "deploy_download_failed", DeployDownloadDone: "deploy_download_done",
	DeploySkipped: "deploy_skipped", DeployDeployed: "deploy_deployed", DeployBeforeAllForced: "deploy_before_all_forced",
	DeployNote: "deploy_note", DeployWarning: "deploy_warning", PurgeWarning: "purge_warning", PurgeNote: "purge_note",
	PurgeComplete: "purge_complete", SwitchDisableNote: "switch_disable_note", SwitchDisabled: "switch_disabled",
	SwitchEnableNote: "switch_enable_note", SwitchEnabled: "switch_enabled", SwitchInstalling: "switch_installing",
	SwitchInstallingMod: "switch_installing_mod", SwitchInstallError: "switch_install_error", SwitchDownloading: "switch_downloading",
	SwitchDownloadFailed: "switch_download_failed", SwitchDownloadDone: "switch_download_done", SwitchInstalled: "switch_installed",
	SwitchInstallNote: "switch_install_note", SwitchInstallWarning: "switch_install_warning",
	InstallBeforeAllForced: "install_before_all_forced", InstallBeforeEachForced: "install_before_each_forced",
	InstallDepInstalling: "install_dep_installing", InstallDepReinstalling: "install_dep_reinstalling", InstallDepFileSelected: "install_dep_file_selected",
	InstallDepDownloading: "install_dep_downloading", InstallDepSkipped: "install_dep_skipped", InstallDepDownloadDone: "install_dep_download_done",
	InstallDepConflictWarning: "install_dep_conflict_warning", InstallDepInstalled: "install_dep_installed", InstallDownloadStarted: "install_download_started",
	InstallDownloading: "install_downloading", InstallDownloadDone: "install_download_done", InstallDownloadFailed: "install_download_failed",
	InstallChecksumComputed: "install_checksum_computed", InstallCompiling: "install_compiling", InstallExtracting: "install_extracting",
	InstallDeploying: "install_deploying", InstallDone: "install_done", InstallNote: "install_note", InstallWarning: "install_warning",
	UpdateDownloading: "update_downloading", UpdateDownloadDone: "update_download_done", UpdateBeforeEachForced: "update_before_each_forced",
	UpdateWarning: "update_warning", UpdateNote: "update_note", PurgeModSkipped: "purge_mod_skipped", PurgeModPurged: "purge_mod_purged",
	ImportSaved: "import_saved", ImportInstalling: "import_installing", ImportModInstalling: "import_mod_installing",
	ImportDownloading: "import_downloading", ImportDownloadDone: "import_download_done", ImportModFailed: "import_mod_failed",
	ImportModInstalled: "import_mod_installed", ImportNote: "import_note", DeployMergeSynced: "deploy_merge_synced",
	InstallLockRefusal: "install_lock_refusal", InstallChecksumSaveFailed: "install_checksum_save_failed",
	InstallMergedPakSyncFailed: "install_merged_pak_sync_failed",
	SyncAddNote:                "sync_add_note", SyncRemoveNote: "sync_remove_note", SyncUpdateNote: "sync_update_note",
	SyncUpdateWarning: "sync_update_warning",
	AdoptBackfillNote: "adopt_backfill_note", AdoptBackfilled: "adopt_backfilled", AdoptDuplicateSkipped: "adopt_duplicate_skipped",
	AdoptAdopted: "adopt_adopted", AdoptFailed: "adopt_failed", AdoptNote: "adopt_note", AdoptSyncWarning: "adopt_sync_warning",
	ImportArchiveFetching: "import_archive_fetching", ImportArchiveDetected: "import_archive_detected",
	ImportArchiveDetail: "import_archive_detail", ImportArchiveDeploying: "import_archive_deploying",
	ImportArchiveWarning: "import_archive_warning", ImportArchiveNote: "import_archive_note",
	ImportArchiveProfileNote: "import_archive_profile_note",
	RelinkFetching:           "relink_fetching", RelinkProfileNote: "relink_profile_note", RelinkWarning: "relink_warning",
}

// String returns the phase's wire name.
func (p DeployPhase) String() string {
	if p >= 0 && int(p) < len(deployPhaseNames) && deployPhaseNames[p] != "" {
		return deployPhaseNames[p]
	}
	return fmt.Sprintf("deploy_phase(%d)", int(p))
}

// MarshalText implements encoding.TextMarshaler.
func (p DeployPhase) MarshalText() ([]byte, error) { return []byte(p.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler.
func (p *DeployPhase) UnmarshalText(b []byte) error {
	for i, n := range deployPhaseNames {
		if n == string(b) {
			*p = DeployPhase(i)
			return nil
		}
	}
	return fmt.Errorf("unknown deploy phase %q", b)
}
