// Package core: this file holds the profile-sync flow -
// PlanProfileSync/ApplyProfileSync and the types they own - lifted out of
// cmd/lmm/profile.go's doProfileSync by v2 Phase 2 Unit J (#290).
//
// doProfileSync reconciles profile.yaml against the DB's installed/enabled
// mods (the opposite direction of ApplyProfileApply, which reconciles the
// system to MATCH the profile): a mod enabled in the DB but missing from the
// profile is added, an unmarked mod listed in the profile but not enabled in
// the DB is removed, and a mod present in both but missing its profile-side
// FileIDs is backfilled. An explicitly disabled ref remains the profile's
// record of an intentionally never-downloaded mod. The CLI keeps the prompt
// and every printed line; the diff and the pm.AddMod/RemoveMod/UpsertMod
// execution live here.
package core

import (
	"context"
	"errors"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// ProfileSyncPlan is the pure, displayable diff between the DB's
// installed/enabled mods and profileName's own mod list - computed by
// PlanProfileSync with zero side effects (Missing aside: a missing profile
// FILE is only noted here, never created - see Missing's doc comment).
type ProfileSyncPlan struct {
	GameID  string `json:"game_id"`
	Profile string `json:"profile"`

	// ToAdd is every mod enabled in the DB but absent from the profile's
	// mod list - doProfileSync's "Will add to profile:" bucket.
	ToAdd []domain.ModReference `json:"to_add"`
	// ToRemove is every non-disabled mod listed in the profile that has no
	// installed row under it at all (uninstalled, or never recorded) -
	// doProfileSync's "Will remove from profile:" bucket. A mod that is
	// disabled in the document is NOT here (#444, #479): an installed row
	// may be disabled by a profile switch or disagree with the active
	// profile's document, while a disabled ref with no row means lmm
	// intentionally never fetched that ref. In either case removing it would
	// take its load-order slot, selected files and pinned version with it.
	ToRemove []domain.ModReference `json:"to_remove"`
	// ToUpdate is every mod present in both, where the DB row carries
	// FileIDs the profile's own ref is missing - doProfileSync's "Will
	// update FileIDs for:" bucket.
	ToUpdate []domain.ModReference `json:"to_update"`

	// NoChanges is true when all three buckets are empty - the profile
	// already matches the DB. ApplyProfileSync still creates a Missing
	// profile in that case (the pre-lift engine's pm.Create ran before the
	// diff was even computed) but stops there, without the merged-pak sync:
	// nothing changed, so there is nothing to re-merge, and the sync's
	// zero-sources branch would UNINSTALL an existing merged pak. Mirrors
	// ProfileApplyPlan.NoChanges.
	NoChanges bool `json:"no_changes"`

	// Missing is true when profileName has no profile.yaml on disk yet.
	// PlanProfileSync computes the rest of the diff as if the profile were
	// empty (every enabled DB mod lands in ToAdd, nothing lands in ToRemove
	// or ToUpdate) but does NOT create the file - creation is a mutation,
	// so ApplyProfileSync calls pm.Create before its ToAdd loop, exactly
	// where doProfileSync's own pm.Create call sat.
	Missing bool `json:"missing"`

	// Warnings names every mod the sync kept although its installed row and
	// the document disagree about whether it is on: a mod the ACTIVE
	// profile lists without the `disabled:` marker whose row is disabled
	// and undeployed (#444). The sync changes nothing for it; each entry
	// says which command settles it. No entry carries a prefix; a caller
	// prints each as `Warning: %s`. ApplyProfileSync repeats them first on
	// its result.
	Warnings []string `json:"warnings,omitempty"`

	// Names maps "source:id" (domain.ModKey) to the installed mod's display
	// name for every ToAdd/ToUpdate entry - the two GetInstalledMod lookups
	// doProfileSync made while rendering "Will add to profile:"/"Will update
	// FileIDs for:" (a ToRemove entry never had a name lookup: the profile
	// ref is the only thing doProfileSync had, and it printed the bare
	// "source:id" for those). A key absent from Names means the lookup
	// failed or returned nothing, matching doProfileSync's own "mod != nil"
	// fallback to the bare "source:id" form.
	Names map[string]string `json:"names"`

	// snapshot is ruling 5's staleness precondition: the installed-mod set
	// this plan was computed from. ApplyProfileSync re-derives it and
	// returns ErrStalePlan when it no longer matches.
	snapshot installedSnapshot `json:"-"`
}

// ProfileSyncResult reports the outcome of ApplyProfileSync. Added/Removed/
// Updated count every ToAdd/ToRemove/ToUpdate entry PROCESSED, regardless of
// whether the underlying pm call succeeded - mirroring ApplyProfileApply's
// Disabled/Enabled counts, which increment the same way. A per-item failure
// is never fatal to the loop (Ruling 9): the ToAdd/ToRemove ones are
// swallowed into a --verbose-only StepEvent (SyncAddNote/SyncRemoveNote) at
// their point of occurrence, exactly as doProfileSync printed
// `if verbose { fmt.Printf("  Warning: ...") }` - there is no Result field
// for those; a caller wanting them must observe the event stream, same as a
// live renderer never needing ProfileApplyResult.Notes.
//
// Warnings holds the diagnostics printed unconditionally: the plan's own
// Warnings (#444), then the ToUpdate loop's refused UpsertMod ("could not
// update <source>:<mod>: <err>", #294/Ruling 5 - it used to be a
// --verbose-only SyncUpdateNote, which hid a profile ref the sync silently
// failed to write), then the end-of-apply
// merged-pak sync's own diagnostics (#197) - "could not sync merged pak:
// <err>" when the sync itself failed, or the sync's own warnings otherwise.
// No entry carries a prefix; a caller prints each to stderr as
// `Warning: %s`. The ToUpdate entry is ALSO emitted as a SyncUpdateWarning
// event (the merged-pak ones are not), so a frontend rendering the stream
// live must not print this slice as well. Mirrors
// ProfileApplyResult.Warnings exactly.
type ProfileSyncResult struct {
	Added    int      `json:"added"`
	Removed  int      `json:"removed"`
	Updated  int      `json:"updated"`
	Warnings []string `json:"warnings,omitempty"`
}

// PlanProfileSync computes the diff between game/profileName's DB-recorded
// installed/enabled mods and the profile's own mod list, without mutating
// anything (no DB writes, no filesystem changes) - callers may call it
// speculatively, render it, and discard it.
//
// Ruling 4 (#298): ToAdd is ordered by GetInstalledMods' own deterministic
// order (installed_at) - it has no profile position, being absent from the
// profile entirely. ToUpdate and ToRemove are both subsets of the profile's
// own mod list, so both are ordered by the profile's own mod order. All
// three are built by ranging the installedMods/profile.Mods slices directly
// rather than the byKey/byRef lookup maps below (which exist only for O(1)
// membership checks). Pre-Ruling-4, doProfileSync built all three buckets by
// ranging those maps directly, so the order was whatever that run's Go map
// iteration produced.
//
// Like PlanProfileApply, it first settles what #431's backfill still owes
// (settleOwedProfileBackfill).
func (s *Service) PlanProfileSync(ctx context.Context, game *domain.Game, profileName string) (*ProfileSyncPlan, error) {
	s.settleOwedProfileBackfill(ctx)
	pm := s.NewProfileManager()

	profile, err := pm.Get(ctx, game.ID, profileName)
	missing := false
	if err != nil {
		if !errors.Is(err, domain.ErrProfileNotFound) {
			return nil, fmt.Errorf("loading profile: %w", err)
		}
		missing = true
		profile = &domain.Profile{Name: profileName, GameID: game.ID}
	}

	installedMods, err := s.GetInstalledMods(ctx, game.ID, profileName)
	if err != nil {
		return nil, fmt.Errorf("getting installed mods: %w", err)
	}

	installedRefs := make(map[string]domain.ModReference, len(installedMods))
	// rows is every row, enabled or not. A reference is a leftover only when
	// it has neither a row nor an explicit disabled marker (#431, #444, #479):
	// see ProfileSyncPlan.ToRemove.
	rows := make(map[string]domain.InstalledMod, len(installedMods))
	for _, im := range installedMods {
		rows[domain.ModKey(im.SourceID, im.ID)] = im
		if im.Enabled {
			installedRefs[domain.ModKey(im.SourceID, im.ID)] = domain.ModReference{
				SourceID: im.SourceID,
				ModID:    im.ID,
				Version:  im.Version,
				FileIDs:  im.FileIDs,
			}
		}
	}

	profileRefs := firstRefs(profile.Mods)
	// Only the active profile's enabled flag is a choice (#444).
	flagIsIntent := s.flaggedActiveProfile(game.ID) == profileName

	// #269: no external rule is needed here, and that is a fact worth
	// stating rather than an omission. A sync moves lmm's TRACKING to match
	// the DB, never a file; an external row is Enabled, had its ref written
	// at adopt, and carries no FileIDs, so all three buckets below stay
	// empty for it. A profile that somehow lost the ref gets it back, which
	// is the right answer for a tracked-only mod too - see
	// TestExternal_ProfileSync_LeavesExternalRefsAlone.
	plan := &ProfileSyncPlan{GameID: game.ID, Profile: profileName, Missing: missing, Names: map[string]string{}}

	for _, im := range installedMods {
		if !im.Enabled {
			continue
		}
		key := domain.ModKey(im.SourceID, im.ID)
		if _, exists := profileRefs[key]; !exists {
			plan.ToAdd = append(plan.ToAdd, installedRefs[key])
		}
	}

	// A mod listed twice (a hand edit) is decided by its first reference,
	// as every other flow decides it (firstRefs), and listed once.
	seen := make(map[string]bool, len(profile.Mods))
	for _, mr := range profile.Mods {
		key := domain.ModKey(mr.SourceID, mr.ModID)
		if seen[key] {
			continue
		}
		seen[key] = true
		ref, exists := installedRefs[key]
		if !exists {
			row, installed := rows[key]
			if !installed {
				// A disabled ref with no row is an imported profile's intended
				// never-downloaded mod. It carries desired state (including its
				// load-order position and version), so sync must leave it alone.
				if mr.Disabled {
					continue
				}
				plan.ToRemove = append(plan.ToRemove, mr)
				continue
			}
			// #444: the row is disabled. Under the active profile that is
			// the user's choice - and with the document saying "on" and
			// no files deployed, the two disagree, which the user settles.
			// Anywhere else the flag is a switch's, and the document is
			// already right.
			if flagIsIntent && !mr.Disabled && !row.Deployed {
				name := row.Name
				if name == "" {
					name = row.ID
				}
				plan.Warnings = append(plan.Warnings, fmt.Sprintf(
					"%s is disabled but profile %s lists it as enabled - kept it; run `lmm mod disable -p %s %s` to record that, or `lmm profile apply %s` to enable it",
					name, profileName, profileName, row.ID, profileName))
			}
		} else if len(ref.FileIDs) > 0 && len(mr.FileIDs) == 0 {
			plan.ToUpdate = append(plan.ToUpdate, ref)
		}
	}

	for _, ref := range plan.ToAdd {
		if mod, err := s.GetInstalledMod(ctx, ref.SourceID, ref.ModID, game.ID, profileName); err == nil && mod != nil {
			plan.Names[domain.ModKey(ref.SourceID, ref.ModID)] = mod.Name
		}
	}
	for _, ref := range plan.ToUpdate {
		if mod, err := s.GetInstalledMod(ctx, ref.SourceID, ref.ModID, game.ID, profileName); err == nil && mod != nil {
			plan.Names[domain.ModKey(ref.SourceID, ref.ModID)] = mod.Name
		}
	}

	// #365: stamp the display facts a bare ref carries for its renderers,
	// so a Steam Workshop item that reaches any of the three buckets shows
	// its revision date rather than the 19-digit content id its Version
	// holds. installedByKey is the same set installedRefs was built from,
	// keyed for stampRefDisplay.
	installedByKey := make(map[string]domain.InstalledMod, len(installedMods))
	for _, im := range installedMods {
		installedByKey[domain.ModKey(im.SourceID, im.ID)] = im
	}
	s.stampRefDisplay(plan.ToAdd, installedByKey)
	s.stampRefDisplay(plan.ToRemove, installedByKey)
	s.stampRefDisplay(plan.ToUpdate, installedByKey)

	plan.NoChanges = len(plan.ToAdd) == 0 && len(plan.ToRemove) == 0 && len(plan.ToUpdate) == 0

	// The rows and the document this plan was decided from (see
	// markedSnapshotOf).
	snapshot, err := s.markedSnapshotOf(game.ID, installedMods, disabledKeysOf(profile))
	if err != nil {
		return nil, err
	}
	plan.snapshot = snapshot

	return plan, nil
}

// ApplyProfileSync executes a plan produced by PlanProfileSync: creates the
// profile file first if it was Missing, then applies the ToAdd/ToRemove/
// ToUpdate buckets in that order, then syncs the merged pak - matching
// doProfileSync exactly. sink may be nil.
//
// Ruling 5: the plan is refused with ErrStalePlan when the profile's
// installed-mod set has changed since it was computed.
func (s *Service) ApplyProfileSync(ctx context.Context, game *domain.Game, plan *ProfileSyncPlan, sink EventSink) (*ProfileSyncResult, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return &ProfileSyncResult{}, err
	}
	defer release()
	return s.applyProfileSync(ctx, game, plan, sink)
}

func (s *Service) applyProfileSync(ctx context.Context, game *domain.Game, plan *ProfileSyncPlan, sink EventSink) (*ProfileSyncResult, error) {
	result := &ProfileSyncResult{}
	if err := s.checkPlanFresh(ctx, plan.GameID, plan.Profile, plan.snapshot); err != nil {
		return result, err
	}
	result.Warnings = append(result.Warnings, plan.Warnings...)

	emit := func(e Event) {
		if sink != nil {
			sink(e)
		}
	}
	scopeOf := func(ref domain.ModReference) Scope {
		return Scope{
			Op:      OpProfileSync,
			Mod:     &domain.ModReference{SourceID: ref.SourceID, ModID: ref.ModID},
			ModName: plan.Names[domain.ModKey(ref.SourceID, ref.ModID)],
		}
	}
	note := func(ref domain.ModReference, phase DeployPhase, msg string) {
		emit(StepEvent{Scope: scopeOf(ref), Phase: phase, Detail: msg})
	}
	// warn is note's unconditional sibling (#294): the diagnostic also lands
	// on Warnings, which the CLI prints to stderr regardless of --verbose.
	// msg carries no "Warning: " prefix - the caller renders one.
	warn := func(ref domain.ModReference, phase DeployPhase, msg string) {
		result.Warnings = append(result.Warnings, msg)
		emit(StepEvent{Scope: scopeOf(ref), Phase: phase, Detail: msg})
	}

	pm := s.NewProfileManager()

	if plan.Missing {
		if _, err := pm.Create(ctx, plan.GameID, plan.Profile); err != nil {
			return result, fmt.Errorf("creating profile: %w", err)
		}
	}

	// The profile file is created above even with nothing to sync (the
	// pre-lift engine's pm.Create ran before the diff), but an empty diff
	// stops there: syncing the merged pak would be work no mod-set change
	// asked for, and its zero-sources branch uninstalls whatever merged pak
	// the game already has. Same guard ApplyProfileApply has for NoChanges.
	if plan.NoChanges {
		return result, nil
	}

	for _, ref := range plan.ToAdd {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if err := pm.AddMod(ctx, plan.GameID, plan.Profile, ref); err != nil {
			// v2 Phase 3 Task 18: AddMod's own ctx.Err() guard can now be
			// what fails this call - a cancellation stays fatal here, same
			// as this loop's top-of-iteration check, rather than the
			// swallowed-note path below.
			if ctxErr := ctx.Err(); ctxErr != nil {
				return result, ctxErr
			}
			note(ref, SyncAddNote, fmt.Sprintf("Warning: %v", err))
		}
		result.Added++
	}

	for _, ref := range plan.ToRemove {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if err := pm.RemoveMod(ctx, plan.GameID, plan.Profile, ref.SourceID, ref.ModID); err != nil {
			// v2 Phase 3 Task 18: same cancellation-stays-fatal guard as
			// the ToAdd loop above.
			if ctxErr := ctx.Err(); ctxErr != nil {
				return result, ctxErr
			}
			note(ref, SyncRemoveNote, fmt.Sprintf("Warning: %v", err))
		}
		result.Removed++
	}

	for _, ref := range plan.ToUpdate {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if err := pm.UpsertMod(ctx, plan.GameID, plan.Profile, ref); err != nil {
			// v2 Phase 3 Task 18: UpsertMod's own ctx.Err() guard can now be
			// what fails this call, so a cancellation must stay fatal here
			// exactly as it would at this loop's top-of-iteration check -
			// not fall into the #294 warning path below, which is for a
			// business refusal (a locked ref), or the run would report
			// success despite never finishing.
			if ctxErr := ctx.Err(); ctxErr != nil {
				return result, ctxErr
			}
			// #294 (Ruling 5), the Phase 3 behaviour fix ruling 9 deferred:
			// a refusal here (today, only a LOCKED ref, #143) leaves the
			// profile ref unwritten, so it is a Warning - unconditional -
			// not the --verbose-only note doProfileSync used to swallow it
			// into.
			warn(ref, SyncUpdateWarning, fmt.Sprintf("could not update %s:%s: %v", ref.SourceID, ref.ModID, err))
		}
		result.Updated++
	}

	// #197: the sync's diagnostics are Warnings, not per-item notes - a
	// failure here used to be silent by default.
	if syncWarnings, syncErr := s.syncMergedPak(ctx, game, plan.Profile); syncErr != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("could not sync merged pak: %v", syncErr))
	} else {
		result.Warnings = append(result.Warnings, syncWarnings...)
	}

	return result, nil
}
