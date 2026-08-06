package core

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
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
// #224 Task 2 implements only the local per-file walk: the file-count
// pre-pass and the per-file loop's non-fix, non-reingest branches, ported
// identically from cmd/lmm/verify.go's doVerify (counting rules included).
// The CLI does not call this yet - later tasks extend verifyRun with the
// remaining phases (version-record check, merged-pak staleness, pak
// reingest, --fix repair, deploy convergence, Full-tier network checks)
// and swap the CLI onto it.
func (s *Service) Verify(ctx context.Context, game *domain.Game, profile string, opts VerifyOptions, progress func(VerifyEvent)) (*VerifyResult, error) {
	if progress == nil {
		progress = func(VerifyEvent) {}
	}

	result := &VerifyResult{}
	r := &verifyRun{svc: s, game: game, profile: profile, opts: opts, emit: progress, result: result}

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

		// #221 lazy migration (PakNeedsReingest) lands in Task 3.

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
