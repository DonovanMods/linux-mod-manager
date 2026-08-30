// Package core: this file holds the local-scan + adopt flow - ScanLocal,
// PlanAdopt, ApplyAdoptBackfill/ApplyAdopt and the types they own - lifted
// out of cmd/lmm/import.go's runImportScan (plus its tryMatchSources,
// importExistingMod and duplicated copyFileStreaming helpers) by v2 Phase 2
// Unit K (#291).
//
// "Adopt" is the naming this flow uses for what the CLI still calls `lmm
// import` scan mode: bringing an UNTRACKED mod that is already sitting in
// the game's mod_path under lmm's management. "Import" stays reserved for
// the profile-import flow (PlanImport/ApplyImport) and for archive import
// (Task 19). The CLI command name is unchanged.
//
// Shape note (why there are two Apply entry points, not one). The pre-lift
// engine performed the metadata backfill - a mutation - BEFORE printing its
// outcome ("Updated metadata for N existing mod(s)"), and both of those
// happen BEFORE the "Import these mods? [y/N]" confirmation that gates the
// adoption itself; declining the prompt kept the backfill. A single Apply
// could not reproduce that: called after the prompt it would lose the
// backfill on a decline (and on the "All mods are already tracked!" early
// return), and called before it would have to move the prompt. So the
// backfill is its own small Apply - ApplyAdoptBackfill - which the frontend
// runs while rendering the scan, and ApplyAdopt is the adoption loop that
// runs after the decision. Both share one AdoptPlan and one staleness
// precondition.
package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
)

// LocalScan is the pure, displayable result of scanning a game's mod_path
// for files lmm does not track yet - the facts the CLI's leading caveat
// note and its "Found %d files, %d untracked" line are rendered from.
type LocalScan struct {
	// Tracked is every scanned entry lmm already knows about (a symlink it
	// deployed, or a name matching an installed mod). Tracked+Untracked is
	// the scan's total.
	Tracked []ScanResult `json:"tracked"`
	// Untracked is every scanned entry an adopt would take on, in scan
	// order. PlanAdopt upgrades these entries in place when a source match
	// is found, so Untracked[i] and AdoptPlan.Matches[i] describe the same
	// entry.
	Untracked []ScanResult `json:"untracked"`

	// Backfill is every installed mod whose row is source-linked (a real
	// SourceID, not local and not empty) but is missing its Author or
	// SourceURL - the candidates ApplyAdoptBackfill re-fetches metadata
	// for, and the count the CLI's "Backfilling metadata for %d mod(s)..."
	// line reports. ScanLocal reports every candidate; PlanAdopt drops them
	// all when AdoptOptions.SkipMatch is set, so a plan's Scan.Backfill is
	// exactly what ApplyAdoptBackfill will process. A candidate whose
	// source has no game-ID mapping is still counted here (the pre-lift
	// engine counted it too) and then silently skipped by the apply.
	Backfill []domain.InstalledMod `json:"backfill"`

	// ExtractModeWarning is true for a game that is not in copy mode, where
	// adoption tracks mods in place without caching them - the caveat the
	// CLI prints before the scan.
	//
	// The CLI does NOT render from this field: its caveat must print BEFORE
	// the scan can fail (see runImportScan), and this field only exists once
	// the scan has already succeeded, so cmd derives the same fact from
	// game.DeployMode itself. The field carries the rule for a frontend that
	// renders a finished LocalScan rather than a live stream - `lmm serve`
	// is its intended consumer; nothing in-tree reads it today.
	ExtractModeWarning bool `json:"extract_mode_warning"`
}

// AdoptMatch is one untracked entry's source-matching outcome. There is one
// per LocalScan.Untracked entry, in the same order, whether or not any
// lookup was performed (AdoptOptions.SkipMatch leaves Mod/File/Error all
// zero).
type AdoptMatch struct {
	// Untracked is the entry AS PLANNED: the scanned ScanResult with the
	// match already folded in (Mod's identity fields, MatchedSource and
	// ResolvedFile), which is what ApplyAdopt adopts. It is the same value
	// as the corresponding LocalScan.Untracked entry.
	Untracked ScanResult `json:"untracked"`
	// Mod is the source hit itself - the first searchable source's first
	// result - kept as the match's provenance. nil means no match.
	Mod *domain.Mod `json:"mod,omitempty"`
	// File is the matched source's own file this archive corresponds to
	// (#139), resolved by EXACT FileName match only (no version fallback: a
	// name-search match is not strong enough evidence for a guess). nil when
	// nothing resolved.
	File *domain.DownloadableFile `json:"file,omitempty"`
	// Error is set only when EVERY searchable source errored - the
	// tryMatchSources semantics this flow preserves: any source that
	// responds at all, even with zero results, makes "no match" the honest
	// outcome rather than a stale error from an unrelated source.
	Error string `json:"error,omitempty"`
	// FileError is the reason File could not be resolved, when the source's
	// own file listing failed. Non-fatal and distinct from Error: the match
	// stands, the adoption is just marker-less (the CLI reports it as a
	// --verbose-only "could not resolve source file" line).
	FileError string `json:"file_error,omitempty"`
}

// AdoptPlan is the pure, displayable plan for adopting a game's untracked
// mod_path entries: the scan, each entry's source-matching outcome, and the
// duplicates an adopt would skip. Computing it performs source reads but no
// writes, so a caller may compute it speculatively, render it, and discard
// it.
type AdoptPlan struct {
	GameID  string `json:"game_id"`
	Profile string `json:"profile"`

	// Scan is the underlying LocalScan, with Untracked already upgraded by
	// whatever matching ran.
	Scan *LocalScan `json:"scan"`
	// Matches has one entry per Scan.Untracked entry, in the same order.
	Matches []AdoptMatch `json:"matches"`
	// Duplicates names every untracked entry (by file name) whose detected
	// mod name normalizes onto an ALREADY-INSTALLED mod's name - the
	// plan-time preview of what ApplyAdopt will skip. ApplyAdopt re-checks
	// as it goes, against a set that grows with each adoption, so it also
	// catches duplicates that exist only WITHIN the batch; those cannot be
	// known here, which is why this is a preview and not the contract.
	Duplicates []string `json:"duplicates"`
	// SkipMatch records the option the plan was computed with, so a reader
	// can tell "no source matched" from "no lookup was attempted".
	SkipMatch bool `json:"skip_match"`

	// snapshot is ruling 5's staleness precondition: the installed-mod set
	// this plan was computed from. Both applies re-derive it and return
	// ErrStalePlan when it no longer matches. Unexported and outside the
	// wire contract - a frontend round-trips the plan through its own
	// store, not through JSON.
	snapshot installedSnapshot `json:"-"`
}

// AdoptOptions configures PlanAdopt. Ruling 6: the confirmation the CLI
// prompts for is not here - the frontend inspects the plan, decides, and
// simply does not call ApplyAdopt when the answer is no.
type AdoptOptions struct {
	// SkipMatch suppresses BOTH source matching and the metadata backfill,
	// exactly as `lmm import --skip-match` does.
	SkipMatch bool
	// DryRun is passed through to the scan. It does not make the flow dry
	// by itself: a dry run is a caller that plans and never applies.
	DryRun bool
}

// AdoptBackfillResult reports ApplyAdoptBackfill's outcome. Backfilled
// counts rows whose metadata was fetched AND saved; a fetch failure, a save
// failure and an unmapped source all leave it unchanged.
type AdoptBackfillResult struct {
	Backfilled int `json:"backfilled"`
}

// AdoptResult reports ApplyAdopt's outcome. Adopted/Skipped/Failed count
// untracked entries, and every entry with a detected mod lands in exactly
// one of them.
//
// Warnings holds the per-mod merged-pak sync's own diagnostics (#197). They
// are ALSO emitted as AdoptSyncWarning events at their point of occurrence,
// which is how the CLI renders them (interleaved with the per-mod lines,
// exactly as the pre-lift engine did) - a streaming frontend must therefore
// NOT print Warnings as well, or every warning appears twice. The field is
// for callers that take the result and never watched the stream.
type AdoptResult struct {
	Adopted  int      `json:"adopted"`
	Skipped  int      `json:"skipped"`
	Failed   int      `json:"failed"`
	Warnings []string `json:"warnings,omitempty"`
}

// ScanLocal scans game's mod_path for entries lmm does not track yet and
// reports them split into Tracked/Untracked, alongside the installed rows
// whose source metadata is incomplete (Backfill) and whether this game's
// deploy mode makes adoption an in-place, uncached affair. The profile
// scanned against is opts.ProfileName.
//
// It is side-effect-free: no DB writes, no filesystem changes, no source
// calls.
//
// It stays EXPORTED with no production caller: PlanAdopt reads through its
// own unexported scanLocal twin below (Task 18 review Minor 3, #291) so the
// precondition snapshot and the reported scan can share one read, which
// leaves ScanLocal itself called by tests only today. It is the scan-only
// query a non-mutating frontend needs - `lmm serve` is its intended
// consumer, same reasoning as LocalScan.ExtractModeWarning above.
func (s *Service) ScanLocal(ctx context.Context, game *domain.Game, opts ScanOptions) (*LocalScan, error) {
	scan, _, err := s.scanLocal(ctx, game, opts)
	return scan, err
}

// scanLocal is ScanLocal's internal twin: it ALSO returns the installed-mod
// set the scan was computed against, so PlanAdopt can build its duplicate
// preview and its staleness snapshot from the same read instead of asking
// the DB three times for three views that could disagree (Task 18 review,
// minor 3). The pre-lift engine likewise read the set exactly once.
func (s *Service) scanLocal(ctx context.Context, game *domain.Game, opts ScanOptions) (*LocalScan, []domain.InstalledMod, error) {
	installedMods, err := s.GetInstalledMods(ctx, game.ID, opts.ProfileName)
	if err != nil {
		return nil, nil, fmt.Errorf("getting installed mods: %w", err)
	}

	results, err := s.newImporter(game).scanModPath(ctx, game, installedMods, opts)
	if err != nil {
		return nil, nil, fmt.Errorf("scanning mod_path: %w", err)
	}

	scan := &LocalScan{ExtractModeWarning: game.DeployMode != domain.DeployCopy}
	for _, r := range results {
		if r.AlreadyTracked {
			scan.Tracked = append(scan.Tracked, r)
			continue
		}
		scan.Untracked = append(scan.Untracked, r)
	}

	for _, im := range installedMods {
		if im.SourceID == domain.SourceLocal || im.SourceID == "" {
			continue
		}
		if im.Author != "" && im.SourceURL != "" {
			continue
		}
		scan.Backfill = append(scan.Backfill, im)
	}

	return scan, installedMods, nil
}

// PlanAdopt scans game's mod_path and resolves every untracked entry
// against the game's configured sources, producing the plan both applies
// consume. It performs source READS (search, and one file listing per
// match) but writes nothing.
func (s *Service) PlanAdopt(ctx context.Context, game *domain.Game, profileName string, opts AdoptOptions) (*AdoptPlan, error) {
	scan, installedMods, err := s.scanLocal(ctx, game, ScanOptions{ProfileName: profileName, DryRun: opts.DryRun})
	if err != nil {
		return nil, err
	}
	if opts.SkipMatch {
		// --skip-match suppresses the backfill as well as the matching, so
		// the plan carries nothing for ApplyAdoptBackfill to do.
		scan.Backfill = nil
	}

	plan := &AdoptPlan{
		GameID:    game.ID,
		Profile:   profileName,
		Scan:      scan,
		SkipMatch: opts.SkipMatch,
		Matches:   make([]AdoptMatch, len(scan.Untracked)),
	}

	for i := range scan.Untracked {
		if !opts.SkipMatch {
			s.matchUntracked(ctx, game, &scan.Untracked[i], &plan.Matches[i])
		}
		plan.Matches[i].Untracked = scan.Untracked[i]
	}

	// Both the duplicate preview and the staleness snapshot read the SAME
	// installed-mod set the scan classified against - one read, one view.
	importer := s.newImporter(game)
	for _, r := range scan.Untracked {
		if r.Mod == nil {
			continue
		}
		if dup := importer.findDuplicateMod(r.Mod.Name, installedMods); dup != nil {
			plan.Duplicates = append(plan.Duplicates, r.FileName)
		}
	}
	plan.snapshot = snapshotOf(installedMods)

	return plan, nil
}

// matchUntracked resolves one untracked entry against the game's sources,
// folding a hit into r (identity fields, MatchedSource, ResolvedFile) and
// recording the outcome in m. Every failure is recorded, never returned:
// the pre-lift engine treated a lookup failure and a file-listing failure
// alike as non-fatal, keeping the entry adoptable as a local mod.
func (s *Service) matchUntracked(ctx context.Context, game *domain.Game, r *ScanResult, m *AdoptMatch) {
	if r.Mod == nil {
		return
	}

	matched, err := s.matchScannedMod(ctx, game, r.Mod.Name)
	if err != nil {
		m.Error = err.Error()
		return
	}
	if matched == nil {
		return
	}

	m.Mod = matched
	r.Mod.ID = matched.ID
	r.Mod.SourceID = matched.SourceID
	r.Mod.Name = matched.Name
	r.Mod.Author = matched.Author
	r.Mod.Summary = matched.Summary
	r.Mod.SourceURL = matched.SourceURL
	r.Mod.PictureURL = matched.PictureURL
	r.Mod.GameID = matched.GameID
	r.MatchedSource = matched.SourceID

	// #139: resolve which of the matched source's files this is. Exact
	// FileName match only - a name-search match is not strong enough
	// evidence for a version-based guess. Non-fatal: failure keeps the
	// marker-less adoption.
	file, ferr := s.resolveImportedFile(ctx, matched.SourceID, matched, r.FileName, r.Mod.Version, false)
	if ferr != nil {
		m.FileError = ferr.Error()
		return
	}
	if file != nil {
		m.File = file
		r.ResolvedFile = file
	}
}

// matchScannedMod searches every source configured for game that declares
// search capability, in SourcesForGame's ID-sorted order (design §4.2:
// "curseforge" before "nexusmods" alphabetically, so typical two-built-in
// setups keep today's outcome), and returns the first source whose search
// turns up a result - the "first non-empty result wins" acceptance rule
// scan matching has always used (tighter scoring tracked in #27). A
// per-source search failure does not abort the round; remaining sources are
// still tried.
//
// Error semantics (PR #124 review round 1): a single source failing does
// not make the overall round a failure - any source that responds at all,
// even with zero results, proves a real search happened and "no match" is
// the honest outcome (nil, nil), not a stale error from an unrelated source
// that happened to fail first. An error is returned only when EVERY
// searchable source failed - lastErr then reports the most recent one. No
// search-capable sources configured is likewise a clean no-match, not an
// error (the loop never runs, so anySucceeded stays false but so does
// lastErr).
func (s *Service) matchScannedMod(ctx context.Context, game *domain.Game, modName string) (*domain.Mod, error) {
	sources, err := s.SourcesForGame(game.ID)
	if err != nil {
		return nil, err
	}

	var lastErr error
	anySucceeded := false
	for _, src := range sources {
		if !source.CapabilitiesOf(src).Search {
			continue
		}

		searchResult, err := s.SearchMods(ctx, src.ID(), game.ID, modName, "", nil, 0, 0)
		if err != nil {
			lastErr = err
			continue
		}
		anySucceeded = true
		if len(searchResult.Mods) > 0 {
			// Return the first (best) match. Tighter scoring tracked in #27.
			return &searchResult.Mods[0], nil
		}
	}

	if anySucceeded {
		return nil, nil
	}
	return nil, lastErr
}

// ApplyAdoptBackfill re-fetches and saves the source metadata missing from
// plan.Scan.Backfill's rows (Author/Summary/SourceURL), leaving every other
// field alone. Every per-row failure is non-fatal and reported as a
// --verbose-only note; only a stale plan or a cancelled context aborts.
// sink may be nil.
//
// See this file's package comment for why the backfill is its own Apply.
func (s *Service) ApplyAdoptBackfill(ctx context.Context, game *domain.Game, plan *AdoptPlan, sink EventSink) (*AdoptBackfillResult, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return &AdoptBackfillResult{}, err
	}
	defer release()

	result := &AdoptBackfillResult{}
	if err := s.checkPlanFresh(ctx, plan.GameID, plan.Profile, plan.snapshot); err != nil {
		return result, err
	}

	emit := func(e Event) {
		if sink != nil {
			sink(e)
		}
	}
	note := func(im domain.InstalledMod, phase DeployPhase, msg string) {
		emit(StepEvent{
			Scope: Scope{
				Op:      OpAdopt,
				Mod:     &domain.ModReference{SourceID: im.SourceID, ModID: im.ID},
				ModName: im.Name,
			},
			Phase:  phase,
			Detail: msg,
		})
	}

	for _, im := range plan.Scan.Backfill {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		// A source with no game-ID mapping cannot be queried at all; the
		// pre-lift engine skipped it silently, without a note.
		sourceGameID, ok := game.SourceIDs[im.SourceID]
		if !ok {
			continue
		}

		mod, err := s.GetMod(ctx, im.SourceID, sourceGameID, im.ID)
		if err != nil {
			note(im, AdoptBackfillNote, fmt.Sprintf("%s: metadata fetch failed: %v", im.Name, err))
			continue
		}

		updated := im
		if im.Author == "" && mod.Author != "" {
			updated.Author = mod.Author
		}
		if im.Summary == "" && mod.Summary != "" {
			updated.Summary = mod.Summary
		}
		if im.SourceURL == "" && mod.SourceURL != "" {
			updated.SourceURL = mod.SourceURL
		}
		if err := s.saveInstalledMod(ctx, &updated); err != nil {
			note(im, AdoptBackfillNote, fmt.Sprintf("%s: metadata save failed: %v", im.Name, err))
			continue
		}

		result.Backfilled++
		note(im, AdoptBackfilled, fmt.Sprintf("✓ %s: metadata updated (author: %s)", im.Name, mod.Author))
	}

	return result, nil
}

// ApplyAdopt adopts every untracked entry in plan that has a detected mod:
// for a copy-mode game it copies the file into the cache (stamping the
// resolved file's completion marker when the plan resolved one), then saves
// the DB row as a manual-download install, upserts the profile ref, and
// syncs the merged pak. sink may be nil.
//
// A duplicate of an already-installed mod is skipped, and the skip set
// grows as the loop adopts, so two files that normalize to the same mod
// name adopt only once. A per-entry failure is counted and reported, never
// fatal.
//
// Ruling 5: the plan is refused with ErrStalePlan when the profile's
// installed-mod set has changed since it was computed.
func (s *Service) ApplyAdopt(ctx context.Context, game *domain.Game, plan *AdoptPlan, sink EventSink) (*AdoptResult, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return &AdoptResult{}, err
	}
	defer release()
	return s.applyAdopt(ctx, game, plan, sink)
}

func (s *Service) applyAdopt(ctx context.Context, game *domain.Game, plan *AdoptPlan, sink EventSink) (*AdoptResult, error) {
	result := &AdoptResult{}
	if err := s.checkPlanFresh(ctx, plan.GameID, plan.Profile, plan.snapshot); err != nil {
		return result, err
	}

	emit := func(e Event) {
		if sink != nil {
			sink(e)
		}
	}
	step := func(r ScanResult, phase DeployPhase, msg string) {
		scope := Scope{Op: OpAdopt}
		if r.Mod != nil {
			scope.Mod = &domain.ModReference{SourceID: r.Mod.SourceID, ModID: r.Mod.ID}
			scope.ModName = r.Mod.Name
		}
		emit(StepEvent{Scope: scope, Phase: phase, Detail: msg})
	}

	linkMethod, err := s.GetEffectiveLinkMethod(ctx, game, plan.Profile)
	if err != nil {
		return result, err
	}

	// The duplicate set starts from what is installed now and grows with
	// each adoption, so a batch cannot adopt the same mod twice.
	currentMods, _ := s.GetInstalledMods(ctx, plan.GameID, plan.Profile)
	importer := s.newImporter(game)

	for _, m := range plan.Matches {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		r := m.Untracked
		if r.Mod == nil {
			continue
		}

		if dup := importer.findDuplicateMod(r.Mod.Name, currentMods); dup != nil {
			step(r, AdoptDuplicateSkipped, fmt.Sprintf("⊘ %s: skipped (duplicate of \"%s\")", r.FileName, dup.Name))
			result.Skipped++
			continue
		}

		if err := s.adoptScannedMod(ctx, game, r, plan.Profile, linkMethod, result, step); err != nil {
			step(r, AdoptFailed, fmt.Sprintf("✗ %s: %v", r.FileName, err))
			result.Failed++
			continue
		}

		step(r, AdoptAdopted, fmt.Sprintf("✓ %s", r.Mod.Name))
		result.Adopted++
		currentMods = append(currentMods, domain.InstalledMod{Mod: *r.Mod})
	}

	return result, nil
}

// adoptScannedMod registers one already-deployed mod in lmm: cache entry
// (copy mode only), DB row, profile ref, merged-pak sync - the order the
// pre-lift importExistingMod used. Only the cache write and the DB save are
// fatal to the entry; the profile upsert and the merged-pak sync are
// reported and moved past.
func (s *Service) adoptScannedMod(ctx context.Context, game *domain.Game, r ScanResult, profileName string, linkMethod domain.LinkMethod, result *AdoptResult, step func(ScanResult, DeployPhase, string)) error {
	// #139: an exact-filename source match carries the file's own version -
	// adopt it before the cache write so the entry, DB row, and marker all
	// agree with what future source-side resolutions will report.
	if r.ResolvedFile != nil && r.ResolvedFile.Version != "" {
		r.Mod.Version = r.ResolvedFile.Version
	}

	// For deploy_mode: copy, the mod is already in place - all that is
	// missing is a cache entry pointing at the same bytes.
	if game.DeployMode == domain.DeployCopy {
		gameCache := s.GetGameCache(game)
		cachePath := gameCache.ModPath(game.ID, r.Mod.SourceID, r.Mod.ID, r.Mod.Version)

		if err := os.MkdirAll(cachePath, 0755); err != nil {
			return fmt.Errorf("creating cache: %w", err)
		}

		destPath := filepath.Join(cachePath, r.FileName)
		if err := copyFileStreaming(r.FilePath, destPath); err != nil {
			return fmt.Errorf("copying to cache: %w", err)
		}

		// #139: stamp the resolved file's completion marker onto the entry
		// just written. Non-fatal - a missing marker only costs the one
		// redundant redownload marker-less adoptions always paid.
		if r.ResolvedFile != nil {
			if err := s.markImportedFileComplete(ctx, game, r.Mod, r.ResolvedFile.ID); err != nil {
				step(r, AdoptNote, fmt.Sprintf("Warning: could not mark cache entry complete: %v", err))
			}
		}
	}

	// FileIDs are recorded whenever the source file was resolved - even in
	// extract mode, where no cache entry is written: the row's file identity
	// is real either way (#139).
	fileIDs := []string{}
	if r.ResolvedFile != nil {
		fileIDs = []string{r.ResolvedFile.ID}
	}

	installedMod := &domain.InstalledMod{
		Mod:            *r.Mod,
		ProfileName:    profileName,
		UpdatePolicy:   domain.UpdateNotify,
		Enabled:        true,
		Deployed:       true,
		LinkMethod:     linkMethod,
		ManualDownload: true, // Adopted mods require manual download
		FileIDs:        fileIDs,
	}
	if err := s.saveInstalledMod(ctx, installedMod); err != nil {
		return fmt.Errorf("saving to database: %w", err)
	}

	pm := s.NewProfileManager()
	modRef := domain.ModReference{
		SourceID: r.Mod.SourceID,
		ModID:    r.Mod.ID,
		Version:  r.Mod.Version,
		FileIDs:  fileIDs,
	}
	// Ruling 16 (A): the DB row is already saved, so the profile ref that
	// completes it is written even under a cancelled ctx; the cancellation
	// itself is then fatal rather than a Note.
	if err := completeProfileWrite(ctx, func(ctx context.Context) error {
		return pm.UpsertMod(ctx, game.ID, profileName, modRef)
	}); err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		// Non-fatal (ruling 9: today this can only be a LOCKED ref, #143).
		step(r, AdoptNote, fmt.Sprintf("Warning: could not update profile: %v", err))
	}

	// #197 I3 fix: mirrors the archive import's tail - an adopted mod is a
	// mod-set change for whatever profile it's registered into.
	if syncWarnings, syncErr := s.syncMergedPak(ctx, game, profileName); syncErr != nil {
		step(r, AdoptNote, fmt.Sprintf("Warning: could not sync merged pak: %v", syncErr))
	} else {
		for _, w := range syncWarnings {
			result.Warnings = append(result.Warnings, w)
			step(r, AdoptSyncWarning, w)
		}
	}

	return nil
}
