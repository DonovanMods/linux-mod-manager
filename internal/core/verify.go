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

// verifyTierNames maps each VerifyTier to its wire name. Keep in declaration
// order.
var verifyTierNames = [...]string{
	VerifyLocal: "local",
	VerifyFull:  "full",
}

// String returns the tier's wire name.
func (t VerifyTier) String() string {
	if t >= 0 && int(t) < len(verifyTierNames) && verifyTierNames[t] != "" {
		return verifyTierNames[t]
	}
	return fmt.Sprintf("verify_tier(%d)", int(t))
}

// MarshalText implements encoding.TextMarshaler.
func (t VerifyTier) MarshalText() ([]byte, error) { return []byte(t.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler.
func (t *VerifyTier) UnmarshalText(b []byte) error {
	for i, n := range verifyTierNames {
		if n == string(b) {
			*t = VerifyTier(i)
			return nil
		}
	}
	return fmt.Errorf("unknown verify tier %q", b)
}

// VerifyOptions configures a Verify run.
type VerifyOptions struct {
	Tier      VerifyTier `json:"tier"`
	Fix       bool       `json:"fix"`
	ModFilter string     `json:"mod_filter,omitempty"`
}

// VerifyFinding is one reported row - a per-file or per-mod outcome from a
// verify run. FileID/Note are blank when the finding isn't about a single
// file (e.g. a mod-level file-count-mismatch row).
//
// Recorded/Effective/Version mirror the identically-named VerifyEvent extras
// (Recorded/Effective for version_mismatch, Version for missing) - additive
// fields (#224 follow-up) so a caller that only has the final VerifyResult
// (not a progress listener) can still render the recorded/effective
// versions. resolveLast clears them whenever a
// row's Status resolves away from missing/version_mismatch, so a repaired
// row never carries a stale pre-repair value. The CLI's own text/JSON
// rendering is untouched by these fields - it still reads the VerifyEvent
// extras (text mode) or copies VerifyFinding by named field (JSON mode, see
// verifyFileJSON in cmd/lmm/verify.go), never a bare struct copy.
type VerifyFinding struct {
	ModID     string `json:"mod_id,omitempty"`
	ModName   string `json:"mod_name,omitempty"`
	FileID    string `json:"file_id,omitempty"`
	Status    string `json:"status,omitempty"`
	Note      string `json:"note,omitempty"`
	Recorded  string `json:"recorded,omitempty"`
	Effective string `json:"effective,omitempty"`
	Version   string `json:"version,omitempty"`
}

// VerifyResult is the accumulated outcome of a Verify run.
type VerifyResult struct {
	Findings []VerifyFinding `json:"findings"`
	Issues   int             `json:"issues"`
	Warnings int             `json:"warnings"`
	Checked  int             `json:"checked"`   // feeds the CLI's "No files found for mod X" gate
	HasFiles bool            `json:"has_files"` // false = the #217 empty-profile path ran
}

// VerifyEventKind identifies what a VerifyEvent carries.
type VerifyEventKind int

const (
	VerifyEvBegin        VerifyEventKind = iota // HasFiles
	VerifyEvFinding                             // Finding + extras; row was appended to Findings
	VerifyEvRepairDetail                        // indented sub-line; Detail pre-formatted, Fixed tone flag
	VerifyEvSyncWarning                         // stderr-bound merged-pak sync warning (Detail)
	VerifyEvVerbose                             // verbose-gated diagnostic (Detail)
	VerifyEvProgress                            // Full-tier network tick (Scope.Index/Total/ModName)
)

// verifyEventKindNames maps each VerifyEventKind to its wire name. Keep in
// declaration order.
var verifyEventKindNames = [...]string{
	VerifyEvBegin: "begin", VerifyEvFinding: "finding", VerifyEvRepairDetail: "repair_detail",
	VerifyEvSyncWarning: "sync_warning", VerifyEvVerbose: "verbose", VerifyEvProgress: "progress",
}

// String returns k's wire name.
func (k VerifyEventKind) String() string {
	if k >= 0 && int(k) < len(verifyEventKindNames) && verifyEventKindNames[k] != "" {
		return verifyEventKindNames[k]
	}
	return fmt.Sprintf("verify_event_kind(%d)", int(k))
}

// MarshalText implements encoding.TextMarshaler.
func (k VerifyEventKind) MarshalText() ([]byte, error) { return []byte(k.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler.
func (k *VerifyEventKind) UnmarshalText(b []byte) error {
	for i, n := range verifyEventKindNames {
		if n == string(b) {
			*k = VerifyEventKind(i)
			return nil
		}
	}
	return fmt.Errorf("unknown verify event kind %q", b)
}

// VerifyEvent is emitted via a Verify run's EventSink as the engine works,
// so a caller can render incrementally instead of waiting for the final
// VerifyResult. Scope.Op is always OpVerify; Scope.Index/Total/ModName carry
// a VerifyEvProgress tick's position (verifyRun.emitEv fills Op, callers
// fill the rest of Scope where relevant).
type VerifyEvent struct {
	Scope
	Kind     VerifyEventKind `json:"kind"`
	HasFiles bool            `json:"has_files,omitempty"`
	Finding  VerifyFinding   `json:"finding"` // valid for VerifyEvFinding

	// Main-line extras the CLI needs beyond the finding row itself:
	Recorded          string `json:"recorded,omitempty"`           // version_mismatch / missing
	Effective         string `json:"effective,omitempty"`          // version_mismatch
	Version           string `json:"version,omitempty"`            // missing
	ExpectedCount     int    `json:"expected_count,omitempty"`     // file_count_mismatch
	ChecksumPopulated bool   `json:"checksum_populated,omitempty"` // ok main line: a --fix redownload populated a previously-missing checksum (#164)

	// Sub-line payload (VerifyEvRepairDetail):
	Detail string `json:"detail,omitempty"`
	Fixed  bool   `json:"fixed,omitempty"` // this sub-line reports a completed repair
}

// EventType implements Event.
func (VerifyEvent) EventType() string { return "verify" }

// verifyRun carries the state threaded through Verify's phase methods: the
// service/game/profile/options being verified, the (nil-safe) event sink,
// and the result being built up. Every phase method appends to result via
// finding/resolveLast so there's a single place that keeps Findings and the
// emitted events in sync.
type verifyRun struct {
	ctx     context.Context
	svc     *Service
	game    *domain.Game
	profile string
	opts    VerifyOptions
	sink    EventSink
	result  *VerifyResult
}

// emitEv stamps e's Scope.Op as OpVerify and forwards it to the sink, if
// any (a nil sink discards, matching EventSink's own contract).
func (r *verifyRun) emitEv(e VerifyEvent) {
	e.Op = OpVerify
	if r.sink != nil {
		r.sink(e)
	}
}

// finding appends f to the result and emits the matching VerifyEvFinding
// event, filling in extras.Kind/Finding. Every finding row MUST be appended
// through this method (not appended to result.Findings directly) so a
// progress listener never observes a row the result doesn't also have.
func (r *verifyRun) finding(f VerifyFinding, extras VerifyEvent) {
	r.result.Findings = append(r.result.Findings, f)
	extras.Kind, extras.Finding = VerifyEvFinding, f
	r.emitEv(extras)
}

// resolveLast rewrites the most recently appended finding's Status/Note in
// place - used by a --fix repair that resolves the row it just reported
// (e.g. "missing" -> "ok" after a successful re-download). When the new
// status leaves the missing/version_mismatch family entirely (repaired, or
// demoted to no_checksum), the row's Recorded/Effective/Version extras are
// cleared too - they described the pre-repair state, so a resolved row
// (e.g. "ok") must not keep reporting it. A row that stays missing (failed
// retry) or stays version_mismatch (refused/failed repair) keeps its
// extras, since it's still the same unresolved condition.
func (r *verifyRun) resolveLast(status, note string) {
	last := &r.result.Findings[len(r.result.Findings)-1]
	last.Status, last.Note = status, note
	if status != "missing" && status != "version_mismatch" {
		last.Recorded, last.Effective, last.Version = "", "", ""
	}
}

// Verify runs the verify engine for game/profile per opts, reporting
// incremental progress via sink (nil-safe: pass nil to skip events
// entirely).
//
// #224 Task 6 completes the engine: the fix-mode merged-pak resync and the
// deploy-convergence sweep (ConvergeDeployedFiles) that close out every run,
// including the #217 empty-profile path, which now runs nothing BUT that
// sweep. The CLI does not call this yet - it still runs its own doVerify; a
// later task (#224 Task 7) swaps it onto Verify.
func (s *Service) Verify(ctx context.Context, game *domain.Game, profile string, opts VerifyOptions, sink EventSink) (*VerifyResult, error) {
	if opts.Fix {
		release, err := s.beginOp(ctx)
		if err != nil {
			return nil, err
		}
		defer release()
	}
	return s.verify(ctx, game, profile, opts, sink)
}

func (s *Service) verify(ctx context.Context, game *domain.Game, profile string, opts VerifyOptions, sink EventSink) (*VerifyResult, error) {
	result := &VerifyResult{}
	r := &verifyRun{ctx: ctx, svc: s, game: game, profile: profile, opts: opts, sink: sink, result: result}

	files, err := s.GetFilesWithChecksums(ctx, game.ID, profile)
	if err != nil {
		return nil, fmt.Errorf("getting files: %w", err)
	}

	result.HasFiles = len(files) > 0
	r.emitEv(VerifyEvent{Kind: VerifyEvBegin, HasFiles: result.HasFiles})

	if !result.HasFiles {
		// #217: doVerify still runs a deploy-convergence sweep here even
		// with no checksummed files at all (a game dir can hold stray
		// lmm-deployed files after everything is uninstalled). The
		// checksum/version/count passes all have nothing to do here, so
		// this path is entirely the convergence pass - no sync phase (that
		// only ever reacts to a --fix repair that just ran, and nothing
		// ran here to react to).
		r.convergencePass()
		return result, nil
	}

	if err := r.fileCountPrePass(files); err != nil {
		// Cancelled mid-pass: return the partial result already
		// accumulated, same contract Task 3's brief specifies.
		return result, err
	}

	// #97 (Task 8 of the original CLI): load the profile once, up front, so
	// the version pass below can look up each ref's lock state
	// (Profile.FindRef) without reloading the profile per mod. A missing/
	// unreadable profile is treated as unlocked - FindRef's nil-receiver-
	// safe behavior on a nil *domain.Profile does the right thing here
	// without an extra guard.
	prof, _ := config.LoadProfile(s.ConfigDir(), game.ID, profile)

	installedMods, err := s.GetInstalledMods(ctx, game.ID, profile)
	if err != nil {
		return nil, fmt.Errorf("getting installed mods: %w", err)
	}

	r.mergedPakStalenessPass()
	if err := r.conversionOutcomesPass(installedMods); err != nil {
		// Cancelled mid-pass: return the partial result already
		// accumulated, same contract Task 3's brief specifies.
		return result, err
	}

	if err := r.versionPass(installedMods, prof); err != nil {
		// Cancelled mid-pass: return the partial result already
		// accumulated, same contract Task 3's brief specifies.
		return result, err
	}

	if err := r.perFileWalk(files); err != nil {
		// Cancelled mid-pass: return the partial result already
		// accumulated, same contract Task 3's brief specifies.
		return result, err
	}

	// #224 Task 6: --fix's merged-pak resync and the deploy-convergence
	// sweep both close out the run, in that order - ported verbatim from
	// doVerify (originally cmd/lmm/verify.go:811-835). The sync is a --fix-
	// only reaction to repairs the passes above may have just made (a
	// version-mismatch repair or a redownload can change a merge's inputs);
	// convergence then reconciles the game dir against the resulting
	// reality, so it always runs AFTER the sync, in both modes.
	if r.opts.Fix {
		r.syncMergedPakPass()
	}
	r.convergencePass()

	return result, nil
}

// syncMergedPakPass ports cmd/lmm/verify.go's fix-mode merged-pak resync
// call verbatim (originally doVerify lines 811-825): --fix can repair a
// VERSION MISMATCH (repairModVersion moves the cache dir and the recorded
// version) or redownload a file whose content has since changed upstream
// (redownloadModFile) - both are merge-fingerprint inputs with no other
// seam to catch them. SyncMergedPak's own guard already no-ops for a
// non-DeployCompile game, so this dials it unconditionally under --fix.
// Only called when opts.Fix - a plain verify pass makes no changes for a
// sync to react to.
func (r *verifyRun) syncMergedPakPass() {
	syncWarnings, err := r.svc.syncMergedPak(r.ctx, r.game, r.profile)
	if err != nil {
		r.emitEv(VerifyEvent{Kind: VerifyEvSyncWarning, Detail: fmt.Sprintf("could not sync merged pak: %v", err)})
		return
	}
	for _, w := range syncWarnings {
		r.emitEv(VerifyEvent{Kind: VerifyEvSyncWarning, Detail: w})
	}
}

// convergencePass ports cmd/lmm/verify.go's reportConvergencePass verbatim
// (originally cmd/lmm/verify.go:865-916): reconciles the game dir against
// current reality (ConvergeDeployedFiles) - deployed_files rows no longer
// provided by any installed mod, and dangling cache-rooted symlinks with no
// row at all - and reports every stale/dangling path found. dryRun mirrors
// !opts.Fix: a plain verify pass reports candidates without mutating
// anything (each becomes a stale_deployment warning); --fix acts, and each
// path ConvergeDeployedFiles actually removed becomes a fixed_stale_
// deployment row instead - resolved, not outstanding, the same convention
// every other successful --fix repair in this engine follows (it does NOT
// add to Warnings). Runs both in the main flow (after perFileWalk/the
// fix-mode sync above) and, for the #217 empty-profile path (HasFiles
// false), as the entirety of that run beyond the Begin event - see Verify's
// own doc comment.
//
// A fixed_stale_deployment row needs no event field of its own for this: the
// CLI renders the WHOLE main line green for it purely by keying on
// Finding.Status (unlike, say, a version repair, which prints a plain
// finding line with a separate green sub-line) - Task 7 wires that
// rendering contract up on the CLI side.
//
// Per-item convergence failures (an Undeploy or sweep os.Remove that
// failed) are joined into one error by ConvergeDeployedFiles rather than
// aborting the whole pass (ConvergeResult's own doc comment) - surfaced
// here the same way every other per-item problem in this engine is: a
// skipped row plus a warning, not a fatal Verify failure.
func (r *verifyRun) convergencePass() {
	convResult, convErr := r.svc.convergeDeployedFiles(r.ctx, r.game, r.profile, !r.opts.Fix)
	if convResult != nil {
		for _, cf := range convResult.Removed {
			if r.opts.Fix {
				r.finding(VerifyFinding{ModID: cf.ModID, FileID: cf.Path, Status: "fixed_stale_deployment", Note: cf.Reason}, VerifyEvent{})
				continue
			}
			r.result.Warnings++
			r.finding(VerifyFinding{ModID: cf.ModID, FileID: cf.Path, Status: "stale_deployment", Note: cf.Reason}, VerifyEvent{})
		}
	}
	if convErr != nil {
		for _, e := range unwrapJoined(convErr) {
			r.result.Warnings++
			r.finding(VerifyFinding{Status: "skipped", Note: fmt.Sprintf("convergence: %v", e)}, VerifyEvent{})
		}
	}
}

// fileCountPrePass ports cmd/lmm/verify.go's per-mod file-count mismatch
// check verbatim (originally doVerify lines 339-415): report when a mod's
// cache entry exists but is empty (0 files) despite the DB recording more
// than zero expected files. Runs before perFileWalk, matching the CLI's
// current phase order.
func (r *verifyRun) fileCountPrePass(files []DeployedFile) error {
	fileCountByMod := make(map[string]int)
	for _, f := range files {
		key := domain.ModKey(f.SourceID, f.ModID)
		if r.opts.ModFilter != "" && f.ModID != r.opts.ModFilter {
			continue
		}
		fileCountByMod[key]++
	}

	gameCache := r.svc.GetGameCache(r.game)
	reportedMismatch := make(map[string]bool)
	for key, expectedCount := range fileCountByMod {
		if err := r.ctx.Err(); err != nil {
			return err
		}
		if expectedCount == 0 {
			continue
		}
		sourceID, modID, _ := strings.Cut(key, ":")
		mod, err := r.svc.GetInstalledMod(r.ctx, sourceID, modID, r.game.ID, r.profile)
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
	return nil
}

// perFileWalk ports cmd/lmm/verify.go's main per-file loop verbatim
// (originally doVerify lines 678-854): for each checksummed file, report
// unknown-mod as "skipped", an absent cache entry as "missing", a
// stored-but-empty checksum as "no_checksum", and anything else as "ok".
// Checked is incremented once per row considered (after the ModFilter
// check, before any of those outcomes). #224 Task 4 adds the MISSING/NO
// CHECKSUM/NEEDS REINGEST --fix redownload repairs inline at each site;
// Task 5 adds the one repair this walk does NOT own (version_mismatch's,
// in versionPass below).
func (r *verifyRun) perFileWalk(files []DeployedFile) error {
	gameCache := r.svc.GetGameCache(r.game)
	for _, f := range files {
		if err := r.ctx.Err(); err != nil {
			return err
		}
		if r.opts.ModFilter != "" && f.ModID != r.opts.ModFilter {
			continue
		}
		r.result.Checked++

		mod, err := r.svc.GetInstalledMod(r.ctx, f.SourceID, f.ModID, r.game.ID, r.profile)
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
		// cmd/lmm/verify.go's doVerify (originally lines 696-753), including
		// the --fix re-ingest block below (#224 Task 4).
		need, nerr := r.svc.PakNeedsReingest(r.ctx, r.game, mod, f.FileID)
		if nerr != nil {
			// A real check failure (a Stat/ListFiles error, not "nothing
			// ingested yet") - not counted as a warning and not fatal to
			// the rest of this row's checks below, but not silently
			// dropped either: surfaced as a verbose diagnostic event, same
			// as every other soft diagnostic in this codebase. The exact
			// text (sans the CLI's own "  (verbose) " prefix) is a frozen
			// contract - the CLI renderer depends on it verbatim.
			r.emitEv(VerifyEvent{Kind: VerifyEvVerbose, Detail: fmt.Sprintf("could not check pak-reingest status for %s (%s): %v", mod.Name, f.FileID, nerr)})
		} else if need {
			note := "pak predates conversion support - run 'lmm verify --fix' to re-ingest"
			if mod.SourceID == domain.SourceLocal {
				note = "re-import the archive to enable conversion"
			}
			r.result.Warnings++
			r.finding(VerifyFinding{ModID: mod.ID, ModName: mod.Name, FileID: f.FileID, Status: "needs_reingest", Note: note}, VerifyEvent{})
			// #224 Task 4: --fix re-ingests through the same redownload path
			// as MISSING/NO CHECKSUM below - the widened ingest predicate
			// retains the source this time, and a later sync picks it up.
			// Ported verbatim from doVerify (originally lines 729-751).
			// Unlike MISSING/NO CHECKSUM, only the error is branched on -
			// redownloadModFile's persisted flag is irrelevant here (a
			// re-ingest either retains the source or it doesn't reach this
			// far).
			if r.opts.Fix && mod.SourceID != domain.SourceLocal {
				if _, rerr := r.redownloadModFile(r.ctx, mod, f.FileID); rerr != nil {
					r.resolveLast("needs_reingest", fmt.Sprintf("re-ingest failed: %v", rerr))
					r.emitEv(VerifyEvent{Kind: VerifyEvRepairDetail, Detail: fmt.Sprintf("Re-ingest failed: %v", rerr)})
				} else {
					// Same convention as MISSING/NO CHECKSUM's own --fix
					// success path below, and stale_deployment's
					// fixed_stale_deployment: a resolved problem is not left
					// reading as an outstanding one in the SAME run that
					// just fixed it - rewrite the row to a fixed-state
					// status/note and back the count out.
					r.resolveLast("fixed_needs_reingest", "re-ingested with retained source for pak conversion")
					r.result.Warnings--
					r.emitEv(VerifyEvent{Kind: VerifyEvRepairDetail, Detail: "Re-ingested for pak conversion", Fixed: true})
				}
			}
			continue
		}

		cacheExists := gameCache.Exists(r.game.ID, mod.SourceID, mod.ID, mod.Version)
		if !cacheExists {
			r.result.Issues++
			r.finding(VerifyFinding{ModID: mod.ID, ModName: mod.Name, FileID: f.FileID, Status: "missing", Version: mod.Version}, VerifyEvent{Version: mod.Version})
			// #224 Task 4: ported verbatim from doVerify (originally lines
			// 765-799).
			if r.opts.Fix && mod.SourceID != domain.SourceLocal {
				persisted, err := r.redownloadModFile(r.ctx, mod, f.FileID)
				switch {
				case err != nil:
					r.resolveLast("missing", err.Error())
					r.emitEv(VerifyEvent{Kind: VerifyEvRepairDetail, Detail: fmt.Sprintf("Re-download failed: %v", err)})
				case persisted:
					r.resolveLast("ok", "")
					r.result.Issues--
					r.emitEv(VerifyEvent{Kind: VerifyEvRepairDetail, Detail: "Re-downloaded OK", Fixed: true})
				default:
					// The re-download restored the cache - the MISSING
					// issue is genuinely repaired - but no checksum was
					// available to store, so the row remains a NO CHECKSUM
					// warning (#164: don't report "ok" for a write that
					// never happened).
					r.resolveLast("no_checksum", "re-downloaded, but no checksum was available to store")
					r.result.Issues--
					r.result.Warnings++
					r.emitEv(VerifyEvent{Kind: VerifyEvRepairDetail, Detail: "Re-downloaded, but no checksum was available to store - NO CHECKSUM remains"})
				}
			}
			continue
		}

		if f.Checksum == "" {
			// #224 Task 4: --fix's redownload-to-populate-checksum repair
			// REPLACES the plain no_checksum row emission below - ported
			// verbatim from doVerify (originally lines 804-846), including
			// the "ok"+ChecksumPopulated main-line emission on success.
			if r.opts.Fix && mod.SourceID != domain.SourceLocal {
				persisted, err := r.redownloadModFile(r.ctx, mod, f.FileID)
				switch {
				case err != nil:
					r.result.Warnings++
					r.finding(VerifyFinding{ModID: mod.ID, ModName: mod.Name, FileID: f.FileID, Status: "no_checksum", Note: err.Error()}, VerifyEvent{})
					r.emitEv(VerifyEvent{Kind: VerifyEvRepairDetail, Detail: fmt.Sprintf("Re-download to populate checksum failed: %v", err)})
				case persisted:
					r.finding(VerifyFinding{ModID: mod.ID, ModName: mod.Name, FileID: f.FileID, Status: "ok"}, VerifyEvent{ChecksumPopulated: true})
				default:
					// The download succeeded but produced no checksum to
					// store - nothing was written, so the warning stands
					// with an honest reason (#164: "checksum populated" was
					// a lie here, and the summary lied with it).
					r.result.Warnings++
					r.finding(VerifyFinding{ModID: mod.ID, ModName: mod.Name, FileID: f.FileID, Status: "no_checksum", Note: "re-downloaded, but no checksum was available to store"}, VerifyEvent{})
					r.emitEv(VerifyEvent{Kind: VerifyEvRepairDetail, Detail: "Re-downloaded, but no checksum was available to store"})
				}
				continue
			}
			r.result.Warnings++
			r.finding(VerifyFinding{ModID: mod.ID, ModName: mod.Name, FileID: f.FileID, Status: "no_checksum"}, VerifyEvent{})
			continue
		}

		// Cache exists and checksum stored - consider OK.
		r.finding(VerifyFinding{ModID: mod.ID, ModName: mod.Name, FileID: f.FileID, Status: "ok"}, VerifyEvent{})
	}
	return nil
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

	staleUpd, serr := r.svc.CheckMergedPakStaleness(r.ctx, r.game, r.profile)
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
func (r *verifyRun) conversionOutcomesPass(installedMods []domain.InstalledMod) error {
	if r.game.DeployMode != domain.DeployCompile {
		return nil
	}

	modNames := make(map[string]string, len(installedMods))
	for _, m := range installedMods {
		modNames[domain.ModKey(m.SourceID, m.ID)] = m.Name
	}

	outcomes, ok := r.svc.MergedPakOutcomes(r.ctx, r.game, r.profile)
	if !ok {
		return nil
	}
	for _, entry := range outcomes {
		// The check belongs to the loop that EMITS findings, not to the
		// in-memory name-map build above (final-review Minor 2): a pass with
		// no installed mods still walks the outcomes, and this is the loop a
		// reader expects the cancellation guard to protect.
		if err := r.ctx.Err(); err != nil {
			return err
		}
		if entry.Converted {
			continue
		}
		// The fingerprint entry can outlive the mod it names - if the mod
		// was since uninstalled, modNames has no entry and name would be
		// blank. Fall back to the raw entry.ModID, same as the
		// unknown-mod skip in perFileWalk does when a checksum row's mod
		// can't be found - a stable, non-empty identifier beats silence
		// either way.
		name := modNames[domain.ModKey(entry.SourceID, entry.ModID)]
		if name == "" {
			name = entry.ModID
		}
		r.result.Warnings++
		r.finding(VerifyFinding{ModID: entry.ModID, ModName: name, Status: "conversion_failed", Note: entry.FailReason}, VerifyEvent{})
	}
	return nil
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
// not currently rendered by the CLI) and honors ctx cancellation between
// mods: on cancellation the loop stops and returns
// ctx.Err(), leaving the caller (Verify) to return the partial result
// already accumulated rather than a phantom "everything checked out"
// result.
func (r *verifyRun) versionPass(installedMods []domain.InstalledMod, prof *domain.Profile) error {
	if r.opts.Tier != VerifyFull {
		return nil
	}

	for i := range installedMods {
		mod := &installedMods[i]
		r.emitEv(VerifyEvent{Kind: VerifyEvProgress, Scope: Scope{Index: i + 1, Total: len(installedMods), ModName: mod.Name}})
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
			r.finding(VerifyFinding{ModID: mod.ID, ModName: mod.Name, Status: "version_mismatch", Recorded: recorded, Effective: effective}, VerifyEvent{Recorded: recorded, Effective: effective})
			if r.opts.Fix && mod.SourceID != domain.SourceLocal {
				// #97 (Task 8): a locked ref's Version is the lock's
				// TARGET, not a repairable mistake - rewriting it here
				// would silently move what the lock means instead of
				// fixing anything. The mismatch stays reported/counted
				// above (verify stays honest about state); only the
				// repair itself is refused.
				if ref != nil && ref.Locked {
					// #142 round 5: name the source/profile in both remedies -
					// same "copy-paste acts on the wrong target" fix already
					// applied to the core gates (internal/core/flows.go's
					// LockedRefRefusalError) and the sibling-repair warning
					// below - a bare 'lmm mod lock <id> <version>' would
					// resolve against the active profile/an ambiguous source
					// if this mod's lock lives elsewhere.
					refusal := fmt.Sprintf("--fix skipped: %s is locked at v%s in profile %s — the record is the lock's target; move the lock with 'lmm mod lock -s %s -p %s %s <version>' or unlock with 'lmm mod unlock -s %s -p %s %s' instead of rewriting it.", mod.Name, ref.Version, r.profile, mod.SourceID, r.profile, mod.ID, mod.SourceID, r.profile, mod.ID)
					r.emitEv(VerifyEvent{Kind: VerifyEvRepairDetail, Detail: refusal})
					// The Note field already exists on this contract
					// (repair-failure/rename-blocked detail) - this is an
					// additive use of it, not a new field. Kept short
					// ("locked") rather than the full refusal sentence
					// since a --json caller mainly needs the
					// machine-checkable reason; the sentence itself is the
					// text-mode surface. Status stays version_mismatch -
					// the repair was refused, not performed.
					r.resolveLast("version_mismatch", "locked")
					continue
				}
				note, siblingFailures, repairErr := r.repairModVersion(r.ctx, mod, effective)
				// Sibling failures are warnings regardless of how the
				// PRIMARY row's own repair turned out - a failed sibling
				// repair is real, surfaced work left undone, and the
				// warnings counter must reflect it either way, not just
				// when the primary repair also happened to succeed.
				r.result.Warnings += siblingFailures
				if repairErr != nil {
					// The PRIMARY row keeps reporting version_mismatch
					// (status/issues stay as already recorded above), but
					// the repair may have PARTIALLY succeeded: a step-4
					// re-link failure surfaces here after the cache
					// rename and the DB/profile version correction have
					// already landed and are not rolled back (see
					// repairModVersion's doc). The failure reason itself
					// (audit Finding 7) and note can still carry a
					// successful SIBLING repair - see repairModVersion's
					// doc comment - and dropping either here would make
					// them invisible to a --json caller even though they
					// genuinely happened.
					r.emitEv(VerifyEvent{Kind: VerifyEvRepairDetail, Detail: fmt.Sprintf("Repair failed: %v", repairErr)})
					repairNote := "repair failed: " + repairErr.Error()
					if note != "" {
						repairNote += "; " + note
					}
					r.resolveLast("version_mismatch", repairNote)
				} else {
					r.emitEv(VerifyEvent{Kind: VerifyEvRepairDetail, Detail: fmt.Sprintf("Repaired: %s → %s", recorded, effective), Fixed: true})
					if note != "" {
						r.emitEv(VerifyEvent{Kind: VerifyEvRepairDetail, Detail: "Note: " + note})
					}
					r.resolveLast("ok", note)
					r.result.Issues--
				}
			}
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
