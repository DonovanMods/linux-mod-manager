package core

import (
	"context"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// --- ImportPlan/ApplyImport (Phase 6b Task 8) ---

// ImportPlan is the pure, displayable result of PlanImport: everything the
// pre-extraction CLI's pre-import preview (doProfileImport :416-478) needs to
// render before a caller decides whether/how to proceed (ApplyImport then
// actually executes one of these). Computed with zero side effects - it
// parses the given data and inspects existing DB/cache/profile state, but
// never writes anything.
type ImportPlan struct {
	// Profile is the parsed-but-not-yet-saved profile (ProfileManager.
	// ParseProfile's result) - its Name/Mods drive both the CLI's preview
	// print and ApplyImport's own save step.
	Profile *domain.Profile `json:"profile,omitempty"`

	// Installed holds every profile mod already installed IN THE PROFILE
	// BEING IMPORTED INTO (a DB row exists for that profile) at the
	// profile's own version (or with no version recorded in the profile at
	// all) AND cached at that exact version - nothing to do for these.
	//
	// AlreadyCached holds mods installed under some OTHER saved profile of
	// the same game (the cross-profile scan below): the bytes are already
	// downloaded, but installed_mods is keyed by profile, so this profile
	// still needs its own row (#371). ApplyImport installs these from the
	// existing cache entry - no fetch, no download, no network at all - and
	// records the row and the profile ref exactly as a fresh install would.
	// An EXTERNAL row (a Steam Workshop item Steam itself installed) lands
	// here too: it has nothing to install, so its row is simply copied.
	//
	// NeedsRedownload holds mods that must be re-fetched: a DB row with no
	// matching cache entry (installed somewhere, cache gone), or - #138's
	// convergence case, mirroring PlanProfileSwitch's #96 drift case - a row
	// installed at a DIFFERENT version than the imported profile records,
	// scheduled for reinstall at the profile's version (downgrades
	// included). Missing holds mods with no DB row anywhere. All four
	// preserve profile.Mods' own order.
	//
	// Independently of which of those four a ref lands in, a row deployed at
	// another version is recorded in priorVersions so the apply Replaces it
	// (see below). Which rows are consulted depends on whether this profile
	// already has the mod: for a ref this profile has a row of its own for,
	// that row - and only that row; for a ref it does not, EVERY other
	// profile's row (liveOtherVersion). The gap between those two, named
	// rather than papered over (P1a re-review F3): a re-import into an
	// existing profile whose own row is not deployed does not consult the
	// other profiles, so a live deployment of another version elsewhere is
	// installed alongside rather than Replaced.
	//
	// The rule for a cross-profile drift entry, settled and implemented
	// (P1a review F7 - this doc comment used to claim the opposite of the
	// code): the deployed tree is GAME-GLOBAL, one directory shared by every
	// profile, so a live older deployment of the very mod being installed is
	// Replaced - its obsolete files removed - rather than installed
	// alongside. Leaving them would put two versions of one mod in the game
	// directory at once, which is the state Replace exists to prevent.
	//
	// Known consequence, not yet addressed: the OTHER profile's own
	// installed_mods row still reads deployed = true over files this import
	// removed, so `lmm verify` reports drift against a profile the user did
	// not touch. Converging that row is a separate change (it is the same
	// game-global-tree bookkeeping gap `profile switch` has), and reporting
	// drift is a strictly better failure than silently serving a mixed
	// deployment.
	Installed       []domain.ModReference `json:"installed"`
	AlreadyCached   []domain.ModReference `json:"already_cached"`
	NeedsRedownload []domain.ModReference `json:"needs_redownload"`
	Missing         []domain.ModReference `json:"missing"`

	// WorkshopCollection describes the Steam Workshop collection this plan
	// was built from, when it was built from one
	// (Service.PlanWorkshopCollectionImport, #269 W2): what the collection
	// is, and what importing it means item by item. Nil - and absent from
	// the document - for every ordinary file import, which is what keeps
	// this addition byte-identical for every existing golden.
	WorkshopCollection *WorkshopCollection `json:"workshop_collection,omitempty"`

	// Exists reports whether a profile with this name is already saved for
	// the game - purely informational (e.g. so a caller can warn before even
	// attempting the save); ApplyImport does not consult it, instead letting
	// ProfileManager.ImportWithOptions' own existence check (driven by
	// ProfileImportOptions.Force) produce the authoritative error.
	Exists bool `json:"exists"`

	// data is the raw import bytes, preserved so ApplyImport can hand them
	// to ProfileManager.ImportWithOptions unchanged - PlanImport parses via
	// ParseProfile purely for preview, without persisting anything.
	data []byte

	// storedFileIDs maps domain.ModKey keys (for NeedsRedownload's cache-miss
	// entries only) to that mod's DB-recorded FileIDs - preserving
	// doProfileImport's :541-552 rule: a redownload uses the INSTALLED row's
	// own FileIDs, never the imported profile YAML's ref.FileIDs (which may
	// be empty or stale, since the row may have been updated - or reinstalled
	// under a different FileIDs set - after that profile was last exported).
	// A #138 version-drift entry is deliberately absent here: the stored
	// FileIDs describe the WRONG (installed) version, while the imported
	// ref's own FileIDs (if any) describe the target one - the same rule
	// PlanProfileSwitch's drift case applies.
	storedFileIDs map[string][]string

	// cachedRows maps domain.ModKey keys (for AlreadyCached entries only) to
	// the installed row this profile's own row is built from - the row
	// belonging to whichever other profile already has the mod (#371).
	// ApplyImport needs the whole row: its Mod (name/version, so nothing has
	// to be fetched to write the row), its FileIDs, and - for an external
	// row - External/ExternalPath. Private, like storedFileIDs: pure
	// plan-to-apply plumbing no preview renders.
	cachedRows map[string]domain.InstalledMod

	// priorVersions maps domain.ModKey keys to the installed row being
	// converged AWAY from - the import twin of SwitchPlan.PriorVersions (see
	// its doc comment): ApplyImport needs it to know whether a LIVE
	// deployment of another version exists that must be replaced (removing
	// files the new version doesn't serve) rather than merely installed
	// over. Private, like storedFileIDs: pure plan-to-apply plumbing no
	// preview renders.
	//
	// It is keyed by what is ON DISK, not by which bucket the ref landed in
	// (#404): an AlreadyCached entry has a live older deployment in the way
	// exactly as often as a NeedsRedownload one does - the buckets differ
	// only in where that entry's own bytes come from. For an entry in THIS
	// profile it is that row; for a cross-profile one it is whichever of the
	// other profiles' rows is deployed at another version (liveOtherVersion),
	// which is a different question from the one pickImportRow answers and
	// so is asked separately.
	priorVersions map[string]domain.InstalledMod

	// snapshot is the installed-mod set (for Profile.Name, the profile being
	// imported into) this plan was computed against (Ruling 5): ApplyImport
	// re-derives it under beginOp and returns ErrStalePlan when it no longer
	// matches, so a plan a frontend held while something else changed that
	// profile's installed mods is refused rather than applied against a
	// world it never saw. Unexported and outside the wire contract on
	// purpose - see InstallPlan.snapshot's doc comment.
	snapshot installedSnapshot `json:"-"`
}

// PlanImport parses data (an exported profile) and categorizes its mods
// against game's current installed/cache state, without saving anything or
// touching the network - mirrors doProfileImport's preview step
// (:411-459) exactly.
func (s *Service) PlanImport(ctx context.Context, game *domain.Game, data []byte) (*ImportPlan, error) {
	pm := s.NewProfileManager()

	profile, err := pm.ParseProfile(data)
	if err != nil {
		return nil, fmt.Errorf("parsing profile: %w", err)
	}

	_, existErr := pm.Get(ctx, game.ID, profile.Name)
	exists := existErr == nil

	// targetRows keeps each mod key's full installed row FOR THE PROFILE
	// BEING IMPORTED INTO (doProfileImport tracked only Version/FileIDs),
	// needed to (a) check the cache at the RIGHT version, (b) preserve the
	// redownload FileIDs rule above, and (c) record the prior row a #138
	// version-drift entry converges away from (priorVersions needs the whole
	// Mod for Installer.Replace).
	installedMods, _ := s.GetInstalledMods(ctx, game.ID, profile.Name)
	// Ruling 5: record the installed set this plan is being computed
	// against, so ApplyImport can refuse it once profile.Name's has moved on
	// - snapshotOf reuses installedMods rather than re-querying (see its own
	// doc comment).
	snapshot, err := s.snapshotOf(game.ID, installedMods)
	if err != nil {
		return nil, err
	}
	targetRows := make(map[string]domain.InstalledMod)
	for _, im := range installedMods {
		key := domain.ModKey(im.SourceID, im.ID)
		targetRows[key] = im
	}

	// Cross-profile scan (:428-438): a mod installed under some OTHER saved
	// profile has its bytes downloaded already - which answers "does this
	// need fetching", and NOT "does this profile have it" (#371):
	// installed_mods is keyed by profile, so such a row leaves the profile
	// being imported into with nothing at all. Kept separate from targetRows
	// for exactly that reason. Errors from List/GetInstalledMods are
	// ignored, matching doProfileImport exactly (a missing/unreadable
	// profile simply contributes nothing).
	//
	// EVERY other profile's row is collected, not just the first (P1a review
	// F1): pm.List is filename order, so keeping the first hit made the
	// answer to "is the version this document names already downloaded?"
	// depend on what the other profiles happen to be CALLED - an imported
	// build of mods you already own, held at a different version by an
	// earlier-sorting profile, fell into NeedsRedownload and, with the
	// source gone, back into #371's zero-row symptom. pickImportRow below
	// picks among the candidates by what they can answer, not by order.
	elsewhereRows := make(map[string][]domain.InstalledMod)
	allProfiles, _ := pm.List(ctx, game.ID)
	for _, p := range allProfiles {
		if p.Name == profile.Name {
			continue
		}
		mods, _ := s.GetInstalledMods(ctx, game.ID, p.Name)
		for _, im := range mods {
			key := domain.ModKey(im.SourceID, im.ID)
			if _, inTarget := targetRows[key]; inTarget {
				continue
			}
			elsewhereRows[key] = append(elsewhereRows[key], im)
		}
	}

	var installed, alreadyCached, needsRedownload, missing []domain.ModReference
	storedFileIDs := make(map[string][]string)
	cachedRows := make(map[string]domain.InstalledMod) // #371 - see ImportPlan.cachedRows
	// pickedElsewhere records which candidate row answered for each
	// cross-profile mod, so the display stamping below describes the SAME
	// row the classification used rather than an arbitrary one.
	pickedElsewhere := make(map[string]domain.InstalledMod)
	var priorVersions map[string]domain.InstalledMod // #138 - see ImportPlan.priorVersions
	gameCache := s.GetGameCache(game)
	for _, ref := range profile.Mods {
		key := domain.ModKey(ref.SourceID, ref.ModID)

		if im, inTarget := targetRows[key]; inTarget {
			switch {
			case im.External:
				// #269: an EXTERNAL row is a Steam Workshop item Steam
				// itself installed. There is no cache entry to look for and
				// nothing to re-download - it is simply present - and its
				// Version is a content id that would send the #138 drift
				// branch below into a reinstall lmm has no way to perform.
				// "Already installed" is the whole truth about it.
				installed = append(installed, ref)
			case ref.Version != "" && im.Version != ref.Version:
				// #138 convergence, mirroring PlanProfileSwitch's #96 drift
				// case: the imported profile names a different version than
				// the installed row - reinstall at the profile's version
				// (downgrades included). ref is passed as-is: its own
				// FileIDs (if any) describe the TARGET version; the
				// installed row's describe the wrong one (so no
				// storedFileIDs entry). The installed row itself is recorded
				// in priorVersions so ApplyImport's install loop can Replace
				// a live older deployment instead of installing over it.
				needsRedownload = append(needsRedownload, ref)
				if priorVersions == nil {
					priorVersions = make(map[string]domain.InstalledMod)
				}
				priorVersions[key] = im
			case gameCache.Exists(game.ID, ref.SourceID, ref.ModID, im.Version):
				// Bare Exists, deliberately, where the cross-profile branch
				// below reads HasFileIDs (P1a re-review N5): the two ask
				// different questions of the cache. There, the answer
				// decides whether ApplyImport DEPLOYS from the entry, so a
				// half-populated one must not qualify. Here it only
				// separates "already installed in this very profile, with
				// its bytes still around" from a redownload - the entry
				// lands in Installed, which the apply skips entirely, and
				// tightening it would schedule a fetch for a mod that is
				// already installed and deployed.
				installed = append(installed, ref)
			default:
				needsRedownload = append(needsRedownload, ref)
				storedFileIDs[key] = im.FileIDs
			}
			continue
		}

		candidates := elsewhereRows[key]
		// The cross-profile branch asks the SAME question ApplyImport's own
		// install loop asks (:501) and must therefore ask it the same way
		// (P1a review F6): HasFileIDs, not bare Exists, because a version
		// directory can exist yet be only PARTIALLY populated by a
		// broken-off download run. Classified on Exists, such an entry
		// reached importCachedMod, which deployed whatever was on disk and
		// wrote a row claiming the whole FileIDs set and deployed = true.
		cached := func(row domain.InstalledMod) bool {
			return gameCache.HasFileIDs(game.ID, ref.SourceID, ref.ModID, row.Version, row.FileIDs)
		}
		im := pickImportRow(candidates, ref, cached)
		if len(candidates) > 0 {
			pickedElsewhere[key] = im
		}

		// The SECOND question, asked of every candidate rather than of the
		// pick (#404, P1a re-review N1/N5). "Where do I get the bytes?" and
		// "is a live deployment of another version in the way?" are
		// different questions about the same rows, and answering both from
		// one pick loses the second whenever they disagree: a profile
		// holding this document's own version with no cache entry outscores
		// a DEPLOYED row at another version, so the entry fell into
		// NeedsRedownload with no prior recorded, installer.Install ran
		// instead of installer.Replace, and the obsolete files stayed -
		// two versions of one mod in a game-global tree, the exact state
		// Replace exists to prevent. Recorded for every bucket below, since
		// a live older deployment is in the way whether this profile's
		// bytes come from the cache (AlreadyCached) or from a fetch
		// (NeedsRedownload).
		if prior, ok := liveOtherVersion(candidates, ref); ok {
			if priorVersions == nil {
				priorVersions = make(map[string]domain.InstalledMod)
			}
			priorVersions[key] = prior
		}

		switch {
		case len(candidates) == 0:
			missing = append(missing, ref)
		case im.External:
			// #371/#269: nothing to fetch and nothing to deploy - this
			// profile needs the same tracking row, copied.
			alreadyCached = append(alreadyCached, ref)
			cachedRows[key] = im
		case ref.Version != "" && im.Version != ref.Version:
			// The other profile holds a DIFFERENT version, so this
			// profile's own version still has to be fetched - #138's
			// convergence. Any live older deployment to converge away from
			// was recorded above, by the candidate scan rather than by this
			// row: the pick is about bytes, not about what is on disk.
			needsRedownload = append(needsRedownload, ref)
		case cached(im):
			// #371: the bytes are here - install this profile's own row
			// from the cache entry rather than calling it "installed" and
			// writing nothing.
			alreadyCached = append(alreadyCached, ref)
			cachedRows[key] = im
		default:
			needsRedownload = append(needsRedownload, ref)
			storedFileIDs[key] = im.FileIDs
		}
	}

	// #365: stamp the display facts every bucket's renderer needs, so no
	// surface has to print a Workshop content id where a version goes.
	displayRows := make(map[string]domain.InstalledMod, len(targetRows)+len(pickedElsewhere))
	for k, im := range pickedElsewhere {
		displayRows[k] = im
	}
	for k, im := range targetRows {
		displayRows[k] = im
	}
	s.stampRefDisplay(installed, displayRows)
	s.stampRefDisplay(alreadyCached, displayRows)
	s.stampRefDisplay(needsRedownload, displayRows)
	s.stampRefDisplay(missing, displayRows)
	s.stampRefDisplay(profile.Mods, displayRows)

	return &ImportPlan{
		Profile:         profile,
		Installed:       installed,
		AlreadyCached:   alreadyCached,
		NeedsRedownload: needsRedownload,
		Missing:         missing,
		Exists:          exists,
		data:            data,
		storedFileIDs:   storedFileIDs,
		cachedRows:      cachedRows,
		priorVersions:   priorVersions,
		snapshot:        snapshot,
	}, nil
}

// pickImportRow chooses which of a mod's rows from the OTHER saved profiles
// answers PlanImport's cross-profile question: "is what this document names
// already downloaded, or does this import have to fetch it?" (P1a review F1).
//
// The scan used to keep whichever row came first in profile-FILENAME order,
// which made the answer depend on what the other profiles were called: a
// profile holding an older version of the same mod, sorting before the one
// holding the version the document names, sent the ref to NeedsRedownload
// over bytes that were sitting in the cache - and with a delisted or offline
// source, straight back to #371's zero-row symptom.
//
// So the pick is by what a row can ANSWER, best first:
//
//  1. an EXTERNAL row - there is nothing to fetch or deploy for it in any
//     profile, so no other candidate can beat it;
//  2. a row at the version this document names, whose cache entry is
//     complete - the AlreadyCached bucket, the whole point of the scan;
//  3. a row at that version with no usable cache entry - a redownload that
//     can at least reuse the row's own FileIDs;
//  4. a DEPLOYED row at some other version - the #138 drift case, whose
//     live deployment the install has to Replace rather than install over;
//  5. anything else.
//
// A ref with no version at all matches every row's version (the drift branch
// cannot fire for it), so it simply prefers a cached row. Ties keep scan
// order, which is itself deterministic - among equally-scoring rows the
// classification is identical, so nothing observable depends on the choice.
//
// cached reports whether row's cache entry is complete; it is a parameter
// rather than a cache lookup here so the rule stays a pure function of the
// rows.
func pickImportRow(rows []domain.InstalledMod, ref domain.ModReference, cached func(domain.InstalledMod) bool) domain.InstalledMod {
	best := domain.InstalledMod{}
	bestScore := -1
	for _, row := range rows {
		versionMatches := ref.Version == "" || row.Version == ref.Version
		score := 0
		switch {
		case row.External:
			score = 4
		case versionMatches && cached(row):
			score = 3
		case versionMatches:
			score = 2
		case row.Deployed:
			score = 1
		}
		if score > bestScore {
			best, bestScore = row, score
		}
	}
	return best
}

// liveOtherVersion answers PlanImport's OTHER question about a mod's rows in
// the other saved profiles - "is a live deployment of a DIFFERENT version in
// the way?" - which is not the question pickImportRow answers and must not be
// derived from its answer (#404).
//
// The deployed tree is game-global: one directory every profile shares. So a
// row that is deployed at a version this document does not name describes
// files that are on disk right now and that the version being installed will
// not serve. ApplyImport hands it to installer.Replace, which removes them;
// without it installer.Install deploys the new version alongside the old one
// and the game reads two versions of one mod at once.
//
// The rules, and why:
//
//   - a ref with no version at all names no version to drift FROM, so
//     nothing here is "another" version and nothing is replaced;
//   - an EXTERNAL row is Steam's own item, whose files lmm did not deploy
//     and must never remove;
//   - first match wins. In a consistent world there is at most one live
//     deployment of a mod to find; two rows deployed at two different
//     versions is already the mixed state this exists to resolve, and
//     resolving one of them is strictly better than resolving neither.
//
// Whether the prior's own cache entry still exists is deliberately NOT asked
// here: Replace needs it to read what to remove, so ApplyImport re-checks it
// at the moment it acts and falls back to a bare Install (see its install
// loop and importCachedMod).
func liveOtherVersion(rows []domain.InstalledMod, ref domain.ModReference) (domain.InstalledMod, bool) {
	if ref.Version == "" {
		return domain.InstalledMod{}, false
	}
	for _, row := range rows {
		if row.Deployed && !row.External && row.Version != ref.Version {
			return row, true
		}
	}
	return domain.InstalledMod{}, false
}

// ProfileImportOptions configures ApplyImport.
type ProfileImportOptions struct {
	// Force mirrors doProfileImport's --force: passed straight through to
	// ProfileManager.ImportWithOptions, allowing the save to overwrite an
	// already-saved profile of the same name instead of failing.
	Force bool
	// NoInstall mirrors --no-install: a hard override that skips the install
	// loop even when Install is set, counting every pending mod in
	// ProfileImportResult.Skipped instead.
	NoInstall bool

	// Install is the caller's decision to actually download and install the
	// plan's pending mods ([NeedsRedownload..., Missing...]). v2 Phase 3
	// Ruling 1: the decision is fully derivable from the plan
	// (ImportPlan.NeedsRedownload/Missing) BEFORE Apply runs, so it is an
	// option the frontend sets rather than a callback core reaches back
	// through - the CLI asks "Download and install mods? [Y/n]" before
	// calling ApplyImport.
	//
	// Left false, the profile is still saved and every pending mod is
	// counted in Skipped - the same outcome a declined prompt produced.
	Install bool
}

// ProfileImportResult reports the outcome of ApplyImport. As with every other
// flow's result type, every field is always recorded - there is no
// verbosity concept in core.
//
//   - Notes holds the install loop's sole --verbose-gated diagnostic (a
//     failed UpsertMod), matching ApplyProfileSwitch's SwitchInstallNote
//     convention; a caller wanting byte-identical pre-extraction output
//     should print each entry to stdout ONLY under --verbose, e.g.
//     `fmt.Printf("    %s\n", n)` (4-space indent).
//   - Warnings holds one "source:mod: reason" entry per failed mod (#131),
//     appended at the same point Failed is bumped - so an outcome-driven
//     caller keeps the reason and any remediation hint it carries
//     (e.g. #95's stored-files-gone message) after the live progress
//     line is gone.
//   - Failures is the same information STRUCTURED (#308): one ItemFailure
//     per failed mod, appended at that same point, so a `--json` consumer
//     does not have to parse the "source:mod: reason" string back apart.
//     Warnings stays for compatibility and for callers that just want a
//     line to print.
//
// Every Notes entry is ALSO reported via the event stream at the exact
// point it is appended (ImportNote - see its DeployPhase doc comment), with
// Detail equal to the slice entry verbatim; likewise every Warnings entry
// has a corresponding ImportModFailed event with Detail equal to the bare
// reason - a live-printing caller (the CLI) must NOT batch-print Warnings
// afterward or it would double-report every failure.
//
// On error (a failed save), the returned result carries any diagnostics
// accumulated before the failure (none, today, since the save is the very
// first step) - callers should surface it alongside the error.
type ProfileImportResult struct {
	ProfileName string   `json:"profile_name"`
	Installed   int      `json:"installed"`
	Failed      int      `json:"failed"`
	Skipped     int      `json:"skipped"`
	Warnings    []string `json:"warnings,omitempty"`
	Notes       []string `json:"notes,omitempty"`
	// Failures carries one entry per failed mod, in the order they failed,
	// with Reason equal to that mod's ImportModFailed event Detail verbatim
	// (#308). omitempty: a clean import carries no key.
	Failures []ItemFailure `json:"failures,omitempty"`
}

// ApplyImport executes a plan produced by PlanImport: saves the profile
// (ProfileManager.ImportWithOptions), then - unless there is nothing to
// download, NoInstall is set, or opts.Install is false - downloads and
// installs every NeedsRedownload/Missing mod, in that order, matching
// doProfileImport exactly (:481-633). Since #138 the install loop also
// carries ApplyProfileSwitch's convergence machinery: a fully-cached target
// version (by per-file completion marker) deploys from cache without
// redownloading, and a version-drift entry with a live prior deployment is
// Replaced rather than installed over. sink may be nil.
//
// plan is executed EXACTLY as given - like PlanProfileSwitch/ApplyProfileSwitch,
// this method never re-plans or re-validates it against current state (see
// that pair's own doc comments for why a speculative plan is cheap enough to
// simply discard and recompute instead, for a caller that wants to guard
// against drift).
func (s *Service) ApplyImport(ctx context.Context, game *domain.Game, plan *ImportPlan, opts ProfileImportOptions, sink EventSink) (*ProfileImportResult, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return &ProfileImportResult{}, err
	}
	defer release()
	return s.applyImport(ctx, game, plan, opts, sink)
}

func (s *Service) applyImport(ctx context.Context, game *domain.Game, plan *ImportPlan, opts ProfileImportOptions, sink EventSink) (*ProfileImportResult, error) {
	result := &ProfileImportResult{}
	emit := func(e Event) {
		if sink != nil {
			sink(e)
		}
	}

	// Ruling 5: the plan is a contract about a world that may have moved.
	// First statement inside the op (ApplyImport took beginOp just above),
	// so nothing this call does can race the re-derivation - a stale plan is
	// refused having changed nothing at all (before the profile is even
	// saved).
	if err := s.checkPlanFresh(ctx, game.ID, plan.Profile.Name, plan.snapshot); err != nil {
		return result, err
	}

	pm := s.NewProfileManager()
	profile, err := pm.ImportWithOptions(ctx, plan.data, opts.Force)
	if err != nil {
		return result, fmt.Errorf("importing profile: %w", err)
	}
	result.ProfileName = profile.Name
	emit(StepEvent{Scope: Scope{Op: OpImport, ModName: profile.Name}, Phase: ImportSaved})

	// #371: AlreadyCached leads, since those install straight from the cache
	// entry another profile already has - no fetch, no download.
	toInstall := make([]domain.ModReference, 0, len(plan.AlreadyCached)+len(plan.NeedsRedownload)+len(plan.Missing))
	toInstall = append(toInstall, plan.AlreadyCached...)
	toInstall = append(toInstall, plan.NeedsRedownload...)
	toInstall = append(toInstall, plan.Missing...)

	if len(toInstall) == 0 {
		return result, nil
	}
	if opts.NoInstall || !opts.Install {
		result.Skipped = len(toInstall)
		return result, nil
	}

	installer, err := s.getInstallerForProfile(ctx, game, profile.Name)
	if err != nil {
		return result, err
	}
	total := len(toInstall)
	emit(StepEvent{Scope: Scope{Op: OpImport, Total: total}, Phase: ImportInstalling})

	for idx, ref := range toInstall {
		// Task 6 item d (cancel-then-drain): checked between mods, never
		// mid-file-operation - see DeployProfile/ApplyProfileSwitch's
		// identical check.
		if err := ctx.Err(); err != nil {
			return result, err
		}

		scope := Scope{Op: OpImport, Index: idx + 1, Total: total, Mod: &domain.ModReference{SourceID: ref.SourceID, ModID: ref.ModID}}
		emit(ModEvent{Scope: scope, Phase: ImportModInstalling})

		fail := func(reason string) {
			result.Failed++
			result.Warnings = append(result.Warnings, fmt.Sprintf("%s:%s: %s", ref.SourceID, ref.ModID, reason))
			result.Failures = append(result.Failures, ItemFailure{
				SourceID: ref.SourceID, ModID: ref.ModID, Name: scope.ModName, Reason: reason,
			})
			emit(ModEvent{Scope: scope, Phase: ImportModFailed, Detail: reason})
		}

		key := domain.ModKey(ref.SourceID, ref.ModID)

		// #371: a mod another profile already has needs this profile's own
		// row, built from that row and its cache entry. Deliberately ahead
		// of every source call below: an already-downloaded mod must not be
		// re-fetched, and an EXTERNAL one (Steam's own item) has no source
		// call that would even succeed.
		if row, ok := plan.cachedRows[key]; ok {
			scope.ModName = row.Name
			modRef, msgs, err := s.importCachedMod(ctx, game, profile.Name, installer, row, plan.priorVersions[key])
			if err != nil {
				fail(err.Error())
				continue
			}
			for _, msg := range msgs {
				result.Warnings = append(result.Warnings, msg)
				emit(StepEvent{Scope: scope, Phase: ImportNote, Detail: msg})
			}
			if cerr := s.recordImportedRef(ctx, pm, game.ID, profile.Name, modRef, scope, result, emit); cerr != nil {
				return result, cerr
			}
			result.Installed++
			emit(ModEvent{Scope: scope, Phase: ImportModInstalled})
			continue
		}

		mod, err := s.GetMod(ctx, ref.SourceID, game.ID, ref.ModID)
		if err != nil {
			fail(fmt.Sprintf("failed to fetch mod: %v", err))
			continue
		}
		scope.ModName = mod.Name

		files, err := s.GetModFiles(ctx, ref.SourceID, mod)
		if err != nil {
			fail(fmt.Sprintf("failed to get files: %v", err))
			continue
		}
		if len(files) == 0 {
			fail("no downloadable files")
			continue
		}

		// Select files to download - use the DB-stored FileIDs for a
		// redownload, or the imported profile's own FileIDs for a fresh
		// install (:541-552's rule; see ImportPlan.storedFileIDs' doc
		// comment for why this can't just be ref.FileIDs uniformly).
		var fileIDsToUse []string
		if stored, ok := plan.storedFileIDs[key]; ok {
			fileIDsToUse = stored
		} else if len(ref.FileIDs) > 0 {
			fileIDsToUse = ref.FileIDs
		}
		filesToDownload, err := selectFilesForVersion(files, fileIDsToUse, ref.Version)
		if err != nil {
			fail(err.Error())
			continue
		}

		mod.Version = domain.EffectiveInstalledVersion(mod.Version, filesToDownload) // #94

		downloadedFileIDs := make([]string, 0, len(filesToDownload))
		for _, f := range filesToDownload {
			downloadedFileIDs = append(downloadedFileIDs, f.ID)
		}
		// #138: cache-first, by per-file completion marker - the same guard
		// (and the same two review findings) as ApplyProfileSwitch's install
		// loop: HasFileIDs, not bare Exists (a version directory can exist
		// yet be only PARTIALLY populated by a broken-off download run), and
		// by FILE ID, never FileName (an extracted archive's cache entry
		// holds member names that match no DownloadableFile). Deploying from
		// cache matters most for exactly this flow's drift convergence: a
		// downgrade's archived file may have vanished upstream.
		var checksums []fileChecksum // #372 - saved after the DB row below
		if !s.GetGameCache(game).HasFileIDs(game.ID, mod.SourceID, mod.ID, mod.Version, downloadedFileIDs) {
			downloadFailed := false
			for _, file := range filesToDownload {
				if err := ctx.Err(); err != nil {
					return result, err
				}
				progressFn := func(e Event) {
					if forwardFetchStep(e, scope, emit) {
						return
					}
					d, ok := e.(DownloadEvent)
					if !ok || d.TotalBytes <= 0 {
						return
					}
					emit(DownloadEvent{Scope: scope, Phase: ImportDownloading, Percent: d.Percent})
				}
				downloadResult, err := s.downloadMod(ctx, ref.SourceID, game, mod, file, progressFn)
				if err != nil {
					fail(fmt.Sprintf("download failed: %v", err))
					downloadFailed = true
					break
				}
				checksums = appendChecksum(checksums, file.ID, downloadResult)
			}
			emit(StepEvent{Scope: scope, Phase: ImportDownloadDone})

			if downloadFailed {
				continue
			}
		}

		if err := s.deployImportedMod(ctx, game, installer, plan.priorVersions[key], mod, profile.Name); err != nil {
			fail(err.Error())
			continue
		}

		// Save to DB. Normalize GameID to the lmm game (see the comment on
		// ApplyProfileSwitch's own identical save site for why).
		installedMod := &domain.InstalledMod{
			Mod:          *mod,
			ProfileName:  profile.Name,
			UpdatePolicy: domain.UpdateNotify,
			Enabled:      true,
			FileIDs:      downloadedFileIDs,
			Deployed:     true, // installer.Install above just succeeded
		}
		installedMod.GameID = game.ID
		if err := s.saveInstalledMod(ctx, installedMod); err != nil {
			fail(fmt.Sprintf("save failed: %v", err))
			continue
		}

		// #372: the row exists now, so what was downloaded above finally has
		// somewhere to record its checksum.
		for _, msg := range s.recordFileChecksums(ctx, mod.SourceID, mod.ID, game.ID, profile.Name, checksums) {
			result.Warnings = append(result.Warnings, msg)
			emit(StepEvent{Scope: scope, Phase: ImportNote, Detail: msg})
		}

		modRef := domain.ModReference{SourceID: mod.SourceID, ModID: mod.ID, Version: mod.Version, FileIDs: downloadedFileIDs}
		if cerr := s.recordImportedRef(ctx, pm, game.ID, profile.Name, modRef, scope, result, emit); cerr != nil {
			return result, cerr
		}

		result.Installed++
		emit(ModEvent{Scope: scope, Phase: ImportModInstalled})
	}

	// #197 I3 fix: profile import deploys mods (installer.Install above) the
	// same way ApplyInstall/DeployProfile do - without this, an imported
	// profile's exmodz mods (zero per-mod deployment members of their own,
	// Task 2/3) put NO content in the game directory at all until some
	// OTHER flow happens to sync the merged pak.
	if syncWarnings, syncErr := s.syncMergedPak(ctx, game, profile.Name); syncErr != nil {
		msg := fmt.Sprintf("syncing merged pak: %v", syncErr)
		result.Warnings = append(result.Warnings, msg)
		emit(StepEvent{Scope: Scope{Op: OpImport}, Phase: ImportNote, Detail: msg})
	} else {
		for _, w := range syncWarnings {
			result.Warnings = append(result.Warnings, w)
			emit(StepEvent{Scope: Scope{Op: OpImport}, Phase: ImportNote, Detail: w})
		}
	}

	return result, nil
}

// deployImportedMod puts mod on disk for an import, REPLACING a live
// deployment of another version when the plan recorded one (#138's
// convergence, from PlanImport's priorVersions) and installing plainly when
// it did not. prior is the zero InstalledMod when there is nothing to
// converge away from - the map read the callers hand it produces exactly
// that.
//
// Same gate, with the same caveats, as ApplyProfileSwitch's install loop
// (see its comment): prior.Deployed alone is not enough - only Replace when
// the OLD version's cache entry is still there for it to read the file list
// from, since a corrupted or evicted one would hard-fail with "old mod not
// in cache" and abort the convergence entirely. The fallback's cost is the
// one that flow documents too: files the new version no longer serves stay
// behind as stale deployments, which `lmm verify` surfaces - strictly better
// than not converging at all.
//
// Both of the import's deploy sites go through it (#404): the install loop's
// downloaded mods AND importCachedMod's copies. Which of the two a mod takes
// is a question about where its BYTES are, and a live older deployment is in
// the way either way.
func (s *Service) deployImportedMod(ctx context.Context, game *domain.Game, installer *Installer, prior domain.InstalledMod, mod *domain.Mod, profileName string) error {
	if prior.Deployed && !prior.External &&
		s.GetGameCache(game).Exists(game.ID, prior.SourceID, prior.ID, prior.Version) {
		if err := installer.Replace(ctx, game, &prior.Mod, mod, profileName); err != nil {
			return fmt.Errorf("deploy failed: %v", err)
		}
		return nil
	}
	if err := installer.Install(ctx, game, mod, profileName); err != nil {
		return fmt.Errorf("deploy failed: %v", err)
	}
	return nil
}

// importCachedMod writes profileName's own installed_mods row for a mod that
// is already installed under some OTHER saved profile of the same game
// (#371's AlreadyCached bucket). row is that other profile's row: it carries
// everything this one needs, so nothing is fetched and nothing is downloaded
// - the deploy reads the cache entry the plan already confirmed.
//
// An EXTERNAL row is copied without deploying anything: Steam put the item
// where the game reads it, and lmm only tracks it (see PlanImport's own
// external branch).
//
// #372 reaches here too, by copying rather than by downloading (P1a review
// F2): nothing is fetched, so there is no DownloadModResult to record, but
// the new row describes the SAME BYTES as the row it was cloned from - so it
// carries the same checksums. Without them one profile's copy verified and
// the other reported NO CHECKSUM for a file lmm never touched. Returns the
// profile ref the caller records, plus any checksum-copy failures as
// messages the caller surfaces with its other diagnostics (never fatal - the
// files are installed and correct either way, exactly as
// recordFileChecksums' own failures are). Both ends of the copy report: a
// failed READ of the source row's checksum as well as a failed write of the
// new one (P1a re-review N2).
func (s *Service) importCachedMod(ctx context.Context, game *domain.Game, profileName string, installer *Installer, row, prior domain.InstalledMod) (domain.ModReference, []string, error) {
	mod := row.Mod
	// Normalize GameID to the lmm game (see ApplyProfileSwitch's own
	// identical save site for why).
	mod.GameID = game.ID

	if !row.External {
		// #404: the same convergence gate the install loop uses. Bytes
		// already in the cache say nothing about what is LIVE, so an entry
		// that needs no download can still have a live deployment of
		// another version to replace.
		if err := s.deployImportedMod(ctx, game, installer, prior, &mod, profileName); err != nil {
			return domain.ModReference{}, nil, err
		}
	}

	// Read the source row's checksums BEFORE the save below: saveInstalledMod
	// rewrites installed_mod_files, and recordFileChecksums must run after
	// it - the same ordering rule every downloading flow follows.
	var checksums []fileChecksum
	var msgs []string
	for _, fileID := range row.FileIDs {
		checksum, err := s.db.GetFileChecksum(ctx, row.SourceID, row.ID, game.ID, row.ProfileName, fileID)
		if err != nil {
			// A genuine DB failure, which is NOT the same thing as the
			// empty answer below (P1a re-review N2): GetFileChecksum
			// reports ("", nil) for a row that simply has no checksum, so
			// an error here means the read itself failed. Reported, never
			// swallowed - exactly as the WRITE half of this copy
			// (recordFileChecksums) reports its own failures - and
			// non-fatal for the same reason: the files are installed and
			// correct either way, the row is merely unverifiable until
			// something rewrites it.
			msgs = append(msgs, fmt.Sprintf("Warning: could not read checksum for %s/%s file %s: %v",
				row.SourceID, row.ID, fileID, err))
			continue
		}
		if checksum == "" {
			// A row with no checksum of its own has nothing to copy - a
			// legacy install, or a source that hashes nothing. Honest
			// emptiness, not a failure.
			continue
		}
		checksums = append(checksums, fileChecksum{fileID: fileID, checksum: checksum})
	}

	installedMod := &domain.InstalledMod{
		Mod:          mod,
		ProfileName:  profileName,
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		FileIDs:      row.FileIDs,
		Deployed:     true, // installer.Install just succeeded, or Steam has it
		External:     row.External,
		ExternalPath: row.ExternalPath,
	}
	if err := s.saveInstalledMod(ctx, installedMod); err != nil {
		return domain.ModReference{}, nil, fmt.Errorf("save failed: %v", err)
	}

	msgs = append(msgs, s.recordFileChecksums(ctx, mod.SourceID, mod.ID, game.ID, profileName, checksums)...)

	return domain.ModReference{SourceID: mod.SourceID, ModID: mod.ID, Version: mod.Version, FileIDs: row.FileIDs}, msgs, nil
}

// recordImportedRef writes the profile ref completing an installed row the
// import loop just saved, turning a refusal into the flow's --verbose-gated
// note (ProfileImportResult.Notes + ImportNote).
//
// Ruling 16 (A): the DB row and the deployment are already in place, so the
// profile ref that completes them is written even under a cancelled ctx
// (completeProfileWrite). A write that FAILED under a cancelled ctx failed
// because of the cancellation, so it is returned - fatal to the loop - rather
// than swallowed into a note; anything else is the note. A write that
// SUCCEEDED returns nil whatever the ctx says, so the caller counts the mod
// it genuinely finished and stops at its next top-of-loop check.
func (s *Service) recordImportedRef(ctx context.Context, pm *ProfileManager, gameID, profileName string, ref domain.ModReference, scope Scope, result *ProfileImportResult, emit func(Event)) error {
	err := completeProfileWrite(ctx, func(ctx context.Context) error {
		return pm.UpsertMod(ctx, gameID, profileName, ref)
	})
	if err == nil {
		return nil
	}
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	msg := fmt.Sprintf("Warning: could not update profile: %v", err)
	result.Notes = append(result.Notes, msg)
	emit(StepEvent{Scope: scope, Phase: ImportNote, Detail: msg})
	return nil
}
