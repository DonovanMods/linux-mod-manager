package core

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"
)

// VerifyTier selects how much of the verify engine's work runs: VerifyLocal
// is entirely offline (cache/DB state only), VerifyFull additionally
// contacts each installed mod's source (version-record and merged-pak
// checks). Later tasks gate the network-touching phases on this.
type VerifyTier int

const (
	VerifyLocal VerifyTier = iota
	VerifyFull
)

// VerifyOptions configures a Verify run.
type VerifyOptions struct {
	Tier      VerifyTier
	Fix       bool
	ModFilter string
}

// VerifyFinding is one reported row - a per-file or per-mod outcome from a
// verify run. FileID/Note are blank when the finding isn't about a single
// file (e.g. a mod-level file-count-mismatch row).
type VerifyFinding struct {
	ModID, ModName, FileID, Status, Note string
}

// VerifyResult is the accumulated outcome of a Verify run.
type VerifyResult struct {
	Findings         []VerifyFinding
	Issues, Warnings int
	Checked          int  // feeds the CLI's "No files found for mod X" gate
	HasFiles         bool // false = the #217 empty-profile path ran
}

// VerifyEventKind identifies what a VerifyEvent carries.
type VerifyEventKind int

const (
	VerifyEvBegin        VerifyEventKind = iota // HasFiles
	VerifyEvFinding                             // Finding + extras; row was appended to Findings
	VerifyEvRepairDetail                        // indented sub-line; Detail pre-formatted, Green tone flag
	VerifyEvSyncWarning                         // stderr-bound merged-pak sync warning (Detail)
	VerifyEvVerbose                             // verbose-gated diagnostic (Detail)
	VerifyEvProgress                            // Full-tier network tick (Index/Total/ModName)
)

// VerifyEvent is emitted via a Verify run's progress callback as the engine
// works, so a caller (the CLI, later the TUI) can render incrementally
// instead of waiting for the final VerifyResult.
type VerifyEvent struct {
	Kind     VerifyEventKind
	HasFiles bool
	Finding  VerifyFinding // valid for VerifyEvFinding

	// Main-line extras the CLI needs beyond the finding row itself:
	Recorded, Effective, Version string // version_mismatch / missing
	ExpectedCount                int    // file_count_mismatch
	Variant                      string // "" | "checksum_populated" (ok main line) | "fixed_green" (fixed_stale_deployment whole-line green - Task 6)

	// Sub-line / progress payload:
	Detail       string
	Green        bool
	Index, Total int
	ModName      string
}

// verifyRun carries the state threaded through Verify's phase methods: the
// service/game/profile/options being verified, the (nil-safe) progress
// sink, and the result being built up. Every phase method appends to
// result via finding/resolveLast so there's a single place that keeps
// Findings and the emitted events in sync.
type verifyRun struct {
	ctx     context.Context
	svc     *Service
	game    *domain.Game
	profile string
	opts    VerifyOptions
	emit    func(VerifyEvent)
	result  *VerifyResult
}

// finding appends f to the result and emits the matching VerifyEvFinding
// event, filling in extras.Kind/Finding. Every finding row MUST be appended
// through this method (not appended to result.Findings directly) so a
// progress listener never observes a row the result doesn't also have.
func (r *verifyRun) finding(f VerifyFinding, extras VerifyEvent) {
	r.result.Findings = append(r.result.Findings, f)
	extras.Kind, extras.Finding = VerifyEvFinding, f
	r.emit(extras)
}

// resolveLast rewrites the most recently appended finding's Status/Note in
// place - used by a --fix repair that resolves the row it just reported
// (e.g. "missing" -> "ok" after a successful re-download).
func (r *verifyRun) resolveLast(status, note string) {
	last := &r.result.Findings[len(r.result.Findings)-1]
	last.Status, last.Note = status, note
}

// Verify runs the verify engine for game/profile per opts, reporting
// incremental progress via progress (nil-safe: pass nil to skip progress
// events entirely).
//
// #224 Task 3 adds the merged-pak staleness/conversion-outcome checks, the
// Full-tier per-mod version-record pass (network-touching, gated on
// opts.Tier == VerifyFull), and the per-file loop's needs_reingest branch -
// every READ-side status the engine reports except --fix's repairs and the
// deploy-convergence sweep (both later tasks). The CLI does not call this
// yet - it still runs its own doVerify; a later task swaps it onto Verify.
func (s *Service) Verify(ctx context.Context, game *domain.Game, profile string, opts VerifyOptions, progress func(VerifyEvent)) (*VerifyResult, error) {
	if progress == nil {
		progress = func(VerifyEvent) {}
	}

	result := &VerifyResult{}
	r := &verifyRun{ctx: ctx, svc: s, game: game, profile: profile, opts: opts, emit: progress, result: result}

	files, err := s.GetFilesWithChecksums(game.ID, profile)
	if err != nil {
		return nil, fmt.Errorf("getting files: %w", err)
	}

	result.HasFiles = len(files) > 0
	progress(VerifyEvent{Kind: VerifyEvBegin, HasFiles: result.HasFiles})

	if !result.HasFiles {
		// #217: doVerify still runs a deploy-convergence sweep here even
		// with no checksummed files at all (a game dir can hold stray
		// lmm-deployed files after everything is uninstalled). That
		// convergence wiring lands in a later task - until then this is
		// genuinely an empty result, matching this task's brief.
		return result, nil
	}

	r.fileCountPrePass(files)

	// #97 (Task 8 of the original CLI): load the profile once, up front, so
	// the version pass below can look up each ref's lock state
	// (Profile.FindRef) without reloading the profile per mod. A missing/
	// unreadable profile is treated as unlocked - FindRef's nil-receiver-
	// safe behavior on a nil *domain.Profile does the right thing here
	// without an extra guard.
	prof, _ := config.LoadProfile(s.ConfigDir(), game.ID, profile)

	installedMods, err := s.GetInstalledMods(game.ID, profile)
	if err != nil {
		return nil, fmt.Errorf("getting installed mods: %w", err)
	}

	r.mergedPakStalenessPass()
	r.conversionOutcomesPass(installedMods)

	if err := r.versionPass(installedMods, prof); err != nil {
		// Cancelled mid-pass: return the partial result already
		// accumulated, same contract Task 3's brief specifies.
		return result, err
	}

	r.perFileWalk(files)

	return result, nil
}

// fileCountPrePass ports cmd/lmm/verify.go's per-mod file-count mismatch
// check verbatim (originally doVerify lines 339-415): report when a mod's
// cache entry exists but is empty (0 files) despite the DB recording more
// than zero expected files. Runs before perFileWalk, matching the CLI's
// current phase order.
func (r *verifyRun) fileCountPrePass(files []DeployedFile) {
	fileCountByMod := make(map[string]int)
	for _, f := range files {
		key := f.SourceID + ":" + f.ModID
		if r.opts.ModFilter != "" && f.ModID != r.opts.ModFilter {
			continue
		}
		fileCountByMod[key]++
	}

	gameCache := r.svc.GetGameCache(r.game)
	reportedMismatch := make(map[string]bool)
	for key, expectedCount := range fileCountByMod {
		if expectedCount == 0 {
			continue
		}
		sourceID, modID, _ := strings.Cut(key, ":")
		mod, err := r.svc.GetInstalledMod(sourceID, modID, r.game.ID, r.profile)
		if err != nil {
			// A not-installed mod (an orphaned checksum row - perFileWalk
			// below reports this exact case itself, as "skipped") is a
			// normal, silent skip here; reporting it again in this
			// pre-pass would just duplicate that warning. Any OTHER lookup
			// error (a genuine DB failure) is NOT a normal skip and must
			// not be swallowed (epic98 audit Finding 5).
			if !errors.Is(err, domain.ErrModNotFound) {
				r.result.Warnings++
				r.finding(VerifyFinding{ModID: modID, Status: "skipped", Note: err.Error()}, VerifyEvent{})
			}
			continue
		}
		if r.opts.ModFilter != "" && mod.ID != r.opts.ModFilter {
			continue
		}
		// #197 I4 fix: a DeployCompile game's ".exmodz" mod is ingested as
		// validate+retain ONLY - it has zero deployment members of its own
		// by design (the shared merged pak, checked separately, is what
		// actually deploys), so ListFiles == 0 here is correct, healthy
		// state, not a mismatch.
		if r.game.DeployMode == domain.DeployCompile && HasRetainedCompileSource(gameCache, r.game.ID, mod.SourceID, mod.ID, mod.Version, mod.FileIDs) {
			continue
		}
		cacheExists := gameCache.Exists(r.game.ID, mod.SourceID, mod.ID, mod.Version)
		if !cacheExists {
			continue
		}
		cachedFiles, err := gameCache.ListFiles(r.game.ID, mod.SourceID, mod.ID, mod.Version)
		if err != nil {
			// A real filesystem error walking the cache dir (permission
			// denied, I/O error) is not the same as "cache is empty" - the
			// FILE COUNT MISMATCH case just below - and must be surfaced,
			// not silently treated as "nothing to report" (audit Finding 5).
			if !reportedMismatch[key] {
				r.result.Warnings++
				r.finding(VerifyFinding{ModID: mod.ID, ModName: mod.Name, Status: "skipped", Note: err.Error()}, VerifyEvent{})
				reportedMismatch[key] = true
			}
			continue
		}
		actualCount := len(cachedFiles)
		if expectedCount > 0 && actualCount == 0 {
			if !reportedMismatch[key] {
				r.result.Warnings++
				r.finding(VerifyFinding{ModID: mod.ID, ModName: mod.Name, Status: "file_count_mismatch"}, VerifyEvent{ExpectedCount: expectedCount})
				reportedMismatch[key] = true
			}
		}
	}
}

// perFileWalk ports cmd/lmm/verify.go's main per-file loop's non-fix,
// non-reingest branches verbatim (originally doVerify lines 678-854, minus
// the --fix repair blocks and PakNeedsReingest - both later tasks): for
// each checksummed file, report unknown-mod as "skipped", an absent cache
// entry as "missing", a stored-but-empty checksum as "no_checksum", and
// anything else as "ok". Checked is incremented once per row considered
// (after the ModFilter check, before any of those outcomes).
func (r *verifyRun) perFileWalk(files []DeployedFile) {
	gameCache := r.svc.GetGameCache(r.game)
	for _, f := range files {
		if r.opts.ModFilter != "" && f.ModID != r.opts.ModFilter {
			continue
		}
		r.result.Checked++

		mod, err := r.svc.GetInstalledMod(f.SourceID, f.ModID, r.game.ID, r.profile)
		if err != nil {
			r.result.Warnings++
			r.finding(VerifyFinding{ModID: f.ModID, FileID: f.FileID, Status: "skipped"}, VerifyEvent{})
			continue
		}

		// #221 lazy migration: a convert-eligible pak whose cache entry
		// predates pak retention (deployable pak present, no retained
		// source) needs re-ingesting before it can ever participate in a
		// merge - PakNeedsReingest is the one place that kind/retained
		// detection lives (verify must not reimplement it). Ported from
		// cmd/lmm/verify.go's doVerify (originally lines 696-753, minus the
		// --fix re-ingest block - Task 5).
		need, nerr := r.svc.PakNeedsReingest(r.game, mod, f.FileID)
		if nerr != nil {
			// A real check failure (a Stat/ListFiles error, not "nothing
			// ingested yet") - not counted as a warning and not fatal to
			// the rest of this row's checks below, but not silently
			// dropped either: surfaced as a verbose diagnostic event, same
			// as every other soft diagnostic in this codebase. The exact
			// text (sans the CLI's own "  (verbose) " prefix) is a frozen
			// contract - the CLI renderer depends on it verbatim.
			r.emit(VerifyEvent{Kind: VerifyEvVerbose, Detail: fmt.Sprintf("could not check pak-reingest status for %s (%s): %v", mod.Name, f.FileID, nerr)})
		} else if need {
			note := "pak predates conversion support - run 'lmm verify --fix' to re-ingest"
			if mod.SourceID == domain.SourceLocal {
				note = "re-import the archive to enable conversion"
			}
			r.result.Warnings++
			r.finding(VerifyFinding{ModID: mod.ID, ModName: mod.Name, FileID: f.FileID, Status: "needs_reingest", Note: note}, VerifyEvent{})
			// --fix's re-ingest repair (fixed_needs_reingest) lands in Task 5.
			continue
		}

		cacheExists := gameCache.Exists(r.game.ID, mod.SourceID, mod.ID, mod.Version)
		if !cacheExists {
			r.result.Issues++
			r.finding(VerifyFinding{ModID: mod.ID, ModName: mod.Name, FileID: f.FileID, Status: "missing"}, VerifyEvent{Version: mod.Version})
			continue
		}

		if f.Checksum == "" {
			r.result.Warnings++
			r.finding(VerifyFinding{ModID: mod.ID, ModName: mod.Name, FileID: f.FileID, Status: "no_checksum"}, VerifyEvent{})
			continue
		}

		// Cache exists and checksum stored - consider OK.
		r.finding(VerifyFinding{ModID: mod.ID, ModName: mod.Name, FileID: f.FileID, Status: "ok"}, VerifyEvent{})
	}
}

// mergedPakStalenessPass ports cmd/lmm/verify.go's merged-pak staleness
// check verbatim (originally doVerify lines 449-474): for a DeployCompile
// game, compares the profile's merged pak's recorded fingerprint against
// the game's CURRENT enabled-mod set/order/versions/base pak. Entirely
// local/offline - runs regardless of opts.Tier. Independent of ModFilter:
// the merged pak is profile-scoped, not per-mod, so a single-mod verify
// still checks it.
func (r *verifyRun) mergedPakStalenessPass() {
	if r.game.DeployMode != domain.DeployCompile {
		return
	}

	staleUpd, serr := r.svc.CheckMergedPakStaleness(r.game, r.profile)
	if serr != nil {
		r.result.Warnings++
		r.finding(VerifyFinding{Status: "skipped", Note: fmt.Sprintf("could not check merged pak staleness: %v", serr)}, VerifyEvent{})
	}
	r.result.Checked++
	if staleUpd != nil {
		r.result.Warnings++
		r.finding(VerifyFinding{ModID: staleUpd.InstalledMod.ID, ModName: staleUpd.InstalledMod.Name, Status: "stale_compile", Note: staleUpd.RecompileReason}, VerifyEvent{})
	}
}

// conversionOutcomesPass ports cmd/lmm/verify.go's per-mod pak-conversion
// outcome report verbatim (originally doVerify lines 484-514): reports
// every non-Converted entry recorded on the merged pak's own fingerprint
// (MergedPakOutcomes) - a mod whose pak failed to convert stays
// raw-deployed, and the user needs to know why. Independent of the
// staleness check above: a merge can be perfectly up to date while still
// recording a PRIOR conversion failure for one of its contributing mods. A
// warning, not an issue - deploying raw is a documented, working fallback,
// not corruption.
func (r *verifyRun) conversionOutcomesPass(installedMods []domain.InstalledMod) {
	if r.game.DeployMode != domain.DeployCompile {
		return
	}

	modNames := make(map[string]string, len(installedMods))
	for _, m := range installedMods {
		modNames[m.SourceID+":"+m.ID] = m.Name
	}

	outcomes, ok := r.svc.MergedPakOutcomes(r.game, r.profile)
	if !ok {
		return
	}
	for _, entry := range outcomes {
		if entry.Converted {
			continue
		}
		// The fingerprint entry can outlive the mod it names - if the mod
		// was since uninstalled, modNames has no entry and name would be
		// blank. Fall back to the raw entry.ModID, same as the
		// unknown-mod skip in perFileWalk does when a checksum row's mod
		// can't be found - a stable, non-empty identifier beats silence
		// either way.
		name := modNames[entry.SourceID+":"+entry.ModID]
		if name == "" {
			name = entry.ModID
		}
		r.result.Warnings++
		r.finding(VerifyFinding{ModID: entry.ModID, ModName: name, Status: "conversion_failed", Note: entry.FailReason}, VerifyEvent{})
	}
}

// versionPass ports cmd/lmm/verify.go's per-mod version-record check
// verbatim (originally doVerify lines 516-676, minus the --fix repair
// branch - Task 5): for each source-backed installed mod with recorded
// FileIDs, compares the recorded Version against what the source currently
// reports for those FileIDs (issue #94's detection half). Gated on
// opts.Tier == VerifyFull - this is the engine's one network-touching
// phase, and the only one skipped entirely under VerifyLocal.
//
// Emits VerifyEvProgress at the top of every mod's iteration (a new event
// the CLI ignores and the TUI's status line consumes) and honors ctx
// cancellation between mods: on cancellation the loop stops and returns
// ctx.Err(), leaving the caller (Verify) to return the partial result
// already accumulated rather than a phantom "everything checked out"
// result.
func (r *verifyRun) versionPass(installedMods []domain.InstalledMod, prof *domain.Profile) error {
	if r.opts.Tier != VerifyFull {
		return nil
	}

	for i := range installedMods {
		mod := &installedMods[i]
		r.emit(VerifyEvent{Kind: VerifyEvProgress, Index: i + 1, Total: len(installedMods), ModName: mod.Name})
		if err := r.ctx.Err(); err != nil {
			return err
		}

		if r.opts.ModFilter != "" && mod.ID != r.opts.ModFilter {
			continue
		}
		// Nothing to check against: local imports and manual downloads have
		// no source to query, and a mod with no recorded file IDs predates
		// even the buggy stamping this check exists to catch.
		if mod.SourceID == domain.SourceLocal || mod.ManualDownload || len(mod.FileIDs) == 0 {
			continue
		}

		ref := prof.FindRef(mod.SourceID, mod.ID)

		sourceFiles, err := r.svc.GetModFiles(r.ctx, mod.SourceID, SourceMappedMod(r.game, &mod.Mod))
		if err != nil {
			r.result.Warnings++
			r.finding(VerifyFinding{ModID: mod.ID, ModName: mod.Name, Status: "skipped", Note: fmt.Sprintf("could not check version: %v", err)}, VerifyEvent{})
			continue
		}

		var matched []*domain.DownloadableFile
		for _, id := range mod.FileIDs {
			for j := range sourceFiles {
				if sourceFiles[j].ID == id {
					matched = append(matched, &sourceFiles[j])
					break
				}
			}
		}

		if len(matched) == 0 {
			r.result.Warnings++
			r.finding(VerifyFinding{ModID: mod.ID, ModName: mod.Name, Status: "version_unverifiable"}, VerifyEvent{})
			continue
		}

		// When every matched file reports an empty Version (custom sources
		// whose mappings carry no per-file versions), the fallback inside
		// EffectiveInstalledVersion returns mod.Version and this comparison
		// passes vacuously - a deliberate quiet OK, not a missed
		// VERSION UNVERIFIABLE: install-time stamping applies the same
		// fallback, so a per-file version mis-stamp cannot exist for
		// versionless sources.
		effective := domain.EffectiveInstalledVersion(mod.Version, matched)
		if effective != mod.Version {
			recorded := mod.Version
			r.result.Issues++
			r.finding(VerifyFinding{ModID: mod.ID, ModName: mod.Name, Status: "version_mismatch"}, VerifyEvent{Recorded: recorded, Effective: effective})
			// --fix's repair (including the locked-ref refusal) lands in
			// Task 5.
			continue
		}

		// Recorded version matches what the source reports - OK, but not
		// reported as its own row (same quiet-ok convention as the file
		// loop) - UNLESS the mod is locked and the DB version hasn't yet
		// converged to the lock's target (ref.Version): that's expected
		// drift pending a `profile apply`, not corruption, so it gets its
		// own informational note instead of pure silence. Never counted in
		// issues or warnings - it isn't a problem.
		if ref != nil && ref.Locked && ref.Version != mod.Version {
			convergenceNote := fmt.Sprintf("lock pending convergence (installed v%s, locked v%s)", mod.Version, ref.Version)
			r.finding(VerifyFinding{ModID: mod.ID, ModName: mod.Name, Status: "ok", Note: convergenceNote}, VerifyEvent{})
		}
		r.result.Checked++
	}

	return nil
}
