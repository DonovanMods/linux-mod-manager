// Package core: snapshot_restore.go is `lmm snapshot restore` (#350) - the
// Plan/Apply pair that returns a game to a recorded point.
//
// It composes three engines that already exist rather than adding a fourth:
//
//  1. the shared purge loop (purge.go's purgeMods) takes the current
//     deployment out of the game directory;
//  2. the originals store puts back every file lmm had replaced by the time
//     the snapshot was taken, each verified against its recorded checksum;
//  3. the profile-apply engine converges the INSTALLED SET onto the
//     snapshot's own profile document - which it can do because that
//     document names each mod's version, so downgrades, cache-miss
//     re-downloads and removals all come for free;
//  4. the deploy engine puts that set back on disk - which is also what
//     re-applies the snapshot's profile overrides and re-syncs a compile
//     game's merged artifact.
//
// Step 4 is not redundant with step 3. `lmm profile apply` converges what
// is INSTALLED, not what is deployed; the purge in step 1 leaves every row
// enabled-but-not-deployed, which profile-apply has nothing to say about.
// Without the deploy the restore would end with a correct database and an
// empty game directory.
//
// The ORDER of 2 and 3/4 matters and is not arbitrary. Originals go back
// FIRST, while the game directory is empty of mods: a restore that deployed
// first would have to write originals over freshly deployed mod files,
// which is the same replacing write the store exists to catch, and would
// leave the deployment in a state neither the snapshot nor the current
// profile describes.
//
// WHICH originals go back: every row the store currently holds, not only
// the rows the snapshot recorded. The manifest is append-only (first
// original wins), so the current set is a superset of the snapshot's, and
// the extra rows are precisely the files replaced AFTER the snapshot was
// taken - the ones a restore most needs to undo. Step 4 then re-covers
// whichever of them the snapshot's own mods own. Restoring only the
// snapshot's own list would leave a file replaced last week destroyed.
//
// THE PLAN/APPLY SPLIT. The plan resolves each snapshot mod ref against its
// source at plan time - the identical resolution profile-apply performs -
// so "a version this source can no longer serve" is known BEFORE anything
// is purged, and lands in the plan as a refusal the user sees in the
// confirm prompt. What the plan cannot precompute is the convergence
// itself: the thing profile-apply plans against is the profile FILE, and
// this flow rewrites that file as step 3's precondition. So the apply
// re-plans the convergence at that point, inside its own mutation slot -
// the plan still owns every decision a user is asked to approve.
package core

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
)

// SnapshotOriginalStatus is a plan's verdict on one recorded original.
type SnapshotOriginalStatus string

const (
	// SnapshotOriginalRestorable means the stored copy is present and
	// matches its recorded checksum.
	SnapshotOriginalRestorable SnapshotOriginalStatus = "restorable"
	// SnapshotOriginalUnavailable means the stored copy is missing, or its
	// checksum no longer matches. The restore will skip it and say so
	// rather than write bytes it cannot vouch for.
	SnapshotOriginalUnavailable SnapshotOriginalStatus = "unavailable"
)

// SnapshotRestoreOriginal is one row of a restore plan's originals list.
type SnapshotRestoreOriginal struct {
	// Root/RelativePath identify the file, exactly as the manifest does.
	Root         OriginalRoot `json:"root"`
	RelativePath string       `json:"relative_path"`
	// Status is SnapshotOriginalRestorable or SnapshotOriginalUnavailable.
	Status SnapshotOriginalStatus `json:"status"`
	// Reason is why an unavailable row cannot be restored, empty otherwise.
	Reason string `json:"reason,omitempty"`
}

// SnapshotRestoreMod is one row of a restore plan's convergence preview:
// what the snapshot says should be installed, and whether the source can
// still serve it.
type SnapshotRestoreMod struct {
	SourceID string `json:"source_id"`
	ModID    string `json:"mod_id"`
	// Name is the recorded name, so the preview reads in words rather than
	// ids even for a mod whose source cannot be reached.
	Name string `json:"name,omitempty"`
	// Version is the version the snapshot recorded - what the restore
	// converges TO, including a downgrade.
	Version string `json:"version,omitempty"`
	// Cached is true when the cache already holds this version, so the
	// restore needs no download for it.
	Cached bool `json:"cached"`
	// Error is a plan-time resolution failure, worded as the profile-apply
	// engine words its own ("failed to fetch mod: …", "no downloadable
	// files", a version the file list no longer contains). A row carrying
	// one is a REFUSAL: the restore will not install it, will not pretend
	// it did, and the result names it.
	Error string `json:"error,omitempty"`

	// External marks a row whose files Steam owns (#269). lmm never
	// downloaded, deployed or cached it, so a restore neither fetches nor
	// writes anything for it: the item is already where the game reads it,
	// whatever the snapshot says. The row is still listed - the snapshot
	// recorded it, and a preview that silently dropped it would understate
	// what the profile contains - with Cached false and Error empty,
	// because "not cached" here does not mean "will be downloaded".
	External bool `json:"external,omitzero"`
	// ExternalMissing is set when an External row's recorded Steam
	// directory is no longer on disk - the item has been unsubscribed since
	// the snapshot. It is a FINDING, not a refusal: lmm cannot put a Steam
	// subscription back and never promised to, so the restore proceeds and
	// says what it could not account for. Same judgement `lmm verify` makes
	// (externalContentPresent).
	ExternalMissing bool `json:"external_missing,omitzero"`
	// UpdatedAt is the recorded revision timestamp, carried so a renderer
	// can put a DATE where an external row's version would otherwise print
	// Steam's 19-digit content id - issue 269's approval note, via
	// spa/app/version.js#displayVersion and cmd/lmm's displayModVersion.
	UpdatedAt time.Time `json:"updated_at,omitzero"`
}

// SnapshotRestorePlan is what `lmm snapshot restore --dry-run` prints and
// what a confirm modal renders - the whole shape of the restore, computed
// without touching anything.
type SnapshotRestorePlan struct {
	GameID string `json:"game_id"`
	// Profile is the profile the restore acts on: the snapshot's own.
	Profile string `json:"profile"`
	// Snapshot/CreatedAt identify the point being restored.
	Snapshot  string    `json:"snapshot"`
	CreatedAt time.Time `json:"created_at"`

	// ToPurge is what is deployed NOW and will be undeployed first, in
	// GetInstalledMods' order. EXTERNAL mods are not in it - see External.
	ToPurge []domain.InstalledMod `json:"to_purge"`

	// ActiveProfile is the game's ACTIVE profile when it is not the
	// snapshot's own, empty otherwise - so a preview can say "this also
	// switches you back to <profile>" before anything is touched.
	//
	// Review finding 2: the snapshots listing is deliberately game-scoped,
	// so a user on profile B is shown profile A's snapshots. A restore that
	// only purged A (nothing of which was on disk) and then deployed A left
	// TWO profiles' files in the game directory with B still nominally
	// active. A snapshot records which profile was active, so the restore
	// puts that back too.
	ActiveProfile string `json:"active_profile,omitempty"`

	// ToPurgeActive is the active profile's installed set, undeployed
	// before the restore's own stages so the game directory holds only what
	// the snapshot describes. Empty when no switch is involved. EXTERNAL
	// mods are not in it either.
	ToPurgeActive []domain.InstalledMod `json:"to_purge_active,omitempty"`

	// External names every EXTERNAL mod (#269) in either purge set - the
	// Steam Workshop items a restore leaves entirely alone. Kept out of
	// ToPurge/ToPurgeActive and listed here for the same reason PurgePlan
	// does it (internal/core/purge.go): a preview that counted them under
	// "will be undeployed" would promise a removal lmm never performs.
	// Names, deduplicated, snapshot profile first.
	External []string `json:"external,omitempty"`

	// Originals is every file the snapshot recorded as replaced, each with
	// the verdict on whether it can go back.
	Originals []SnapshotRestoreOriginal `json:"originals"`

	// Mods is the convergence preview, in the snapshot profile's own load
	// order.
	Mods []SnapshotRestoreMod `json:"mods"`

	// Refusals names every Mods row carrying an Error - the same rows,
	// lifted out, so a frontend can render "3 mods cannot be restored"
	// without filtering the list itself, and so nothing about a partial
	// restore is discoverable only by inspection.
	Refusals []InstalledRef `json:"refusals,omitempty"`

	// ProfileChanged is true when the snapshot's profile document differs
	// from the profile on disk, so a preview can say the profile file will
	// be rewritten. False means the restore only puts files back.
	ProfileChanged bool `json:"profile_changed"`

	// snapshot is Ruling 5's precondition, and doc is the snapshot the
	// plan was computed from - carried on the plan so the apply cannot
	// read a DIFFERENT snapshot than the one the user approved (someone
	// could delete and recreate the name in between).
	snapshot installedSnapshot `json:"-"`
	doc      *Snapshot         `json:"-"`
	// activeSnapshot is Ruling 5's precondition for the OTHER profile this
	// plan touches - the active one it undeploys in stage 1 (re-review
	// finding N7). Empty when no switch is involved. Without it, a mod
	// installed into the active profile between plan and apply was the one
	// input to a restore that nothing re-checked: stage 1 purged the stale
	// ToPurgeActive list and left that mod's files deployed under a profile
	// the restore had just switched away from.
	activeSnapshot installedSnapshot `json:"-"`
	// originals is the manifest the Originals verdicts were computed from,
	// carried so the apply restores exactly the rows the user approved.
	originals []OriginalFile `json:"-"`
}

// SnapshotRestoreOptions is ApplySnapshotRestore's option set.
type SnapshotRestoreOptions struct {
	// SkipHooks suppresses the uninstall.* hooks the purge stage would
	// otherwise run - the global `--no-hooks`.
	SkipHooks bool
	// Force continues past a failing uninstall.before_all hook in the
	// purge stage, recording a warning, exactly as `lmm purge --force`
	// does.
	Force bool
	// NoSafetySnapshot suppresses the automatic snapshot taken of the
	// CURRENT state before the restore overwrites it. That safety copy is
	// on by default and is not the auto_snapshot config key: a restore is
	// the one operation whose whole purpose is to discard the present
	// state, so having a way back from it is the default, not an opt-in.
	NoSafetySnapshot bool
}

// SnapshotRestoreResult reports what a restore actually did.
type SnapshotRestoreResult struct {
	Snapshot string `json:"snapshot"`
	Profile  string `json:"profile"`

	// SafetySnapshot names the snapshot taken of the pre-restore state,
	// empty when one was suppressed or could not be taken (the latter is
	// also a Warnings entry).
	SafetySnapshot string `json:"safety_snapshot,omitempty"`

	// SwitchedFrom names the profile that was active before the restore,
	// when the restore had to switch away from it (review finding 2).
	// Empty when the snapshot's profile was already the active one.
	SwitchedFrom string `json:"switched_from,omitempty"`

	// Purged is how many mods were undeployed. It counts BOTH the
	// snapshot profile's set and, when the restore carries a switch, the
	// profile it switched away from.
	Purged int `json:"purged"`
	// OriginalsRestored is how many replaced files were put back.
	OriginalsRestored int `json:"originals_restored"`
	// OriginalsSkipped names every original that could NOT be put back,
	// with the reason as data. A restore that skipped one is a PARTIAL
	// restore and says so here rather than reporting plain success.
	OriginalsSkipped []SnapshotRestoreOriginal `json:"originals_skipped,omitempty"`

	// Disabled/Enabled/Installed/Replaced are the convergence stage's own
	// counters, straight from the profile-apply engine.
	Disabled  int `json:"disabled"`
	Enabled   int `json:"enabled"`
	Installed int `json:"installed"`
	Replaced  int `json:"replaced"`
	// Deployed is how many mods the closing deploy put back on disk. It is
	// reported separately from Installed because they answer different
	// questions: a restore that installs nothing (everything was already
	// present at the right version) still has to deploy everything, since
	// the purge emptied the game directory.
	Deployed int `json:"deployed"`

	// LeftInstalled names every mod the restore disabled and undeployed
	// because the snapshot's profile does not list it (#386). Their
	// downloads and their installed_mods rows are deliberately KEPT - a
	// restore is not an uninstall - which means `lmm list` counts them and
	// the restored profile does not, so the difference is reported here
	// rather than left for the user to notice. Not a refusal and not a
	// failure: the restore did exactly what it should.
	LeftInstalled []InstalledRef `json:"left_installed,omitempty"`

	// Refused is one entry per mod the restore could not put back - a
	// version the source can no longer serve, or an install that failed.
	// Never a silent partial.
	Refused []InstalledRef `json:"refused,omitempty"`

	// Notes are verbose-gated diagnostics; Warnings are unconditional.
	// Both follow DeployResult's display contract.
	Notes    []string `json:"notes,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// SnapshotRestorePartialError reports a restore that failed partway
// through: Result still names exactly what had been done when Err stopped
// it, so a --json envelope can say how far it got instead of only that it
// failed. Mirrors GameDetectPartialError - Unwrap for errors.Is/As,
// Details() any for the envelope writer.
//
// This matters more here than anywhere else in the product: a restore that
// dies between "purged everything" and "converged" leaves a game directory
// that matches nothing, and the user's next move depends entirely on which
// stage stopped.
type SnapshotRestorePartialError struct {
	Err    error
	Result *SnapshotRestoreResult
}

// Error returns the wrapped failure's own message, so plain text and the
// envelope's "error" field are unchanged by the wrapping.
func (e *SnapshotRestorePartialError) Error() string { return e.Err.Error() }

// Unwrap exposes the wrapped failure for errors.Is/errors.As.
func (e *SnapshotRestorePartialError) Unwrap() error { return e.Err }

// Details returns the partial result for the --json / /api/v1 envelope's
// "details" member.
func (e *SnapshotRestorePartialError) Details() any {
	return snapshotRestorePartialDetails{Result: e.Result}
}

// snapshotRestorePartialDetails is the wire shape: a named type, not a
// map, so "result" is part of the contract and carries the SAME document a
// successful restore returns.
type snapshotRestorePartialDetails struct {
	Result *SnapshotRestoreResult `json:"result"`
}

// PlanSnapshotRestore computes what restoring name would do, without
// touching anything - no writes, no downloads. Callers may compute it
// speculatively, render it, and discard it.
//
// Every snapshot mod ref is resolved against its source here, which is
// what makes "this version can no longer be served" a fact the user sees
// BEFORE the purge rather than a surprise afterwards.
func (s *Service) PlanSnapshotRestore(ctx context.Context, game *domain.Game, name string) (*SnapshotRestorePlan, error) {
	doc, err := s.LoadSnapshot(ctx, game.ID, name)
	if err != nil {
		return nil, err
	}
	if doc.ProfileDocument == nil {
		return nil, fmt.Errorf("snapshot %s records no profile document and cannot be restored", name)
	}

	profileName := doc.Profile
	installed, err := s.GetInstalledMods(ctx, game.ID, profileName)
	if err != nil {
		return nil, fmt.Errorf("getting installed mods: %w", err)
	}

	// #269: the external half of the profile is set aside here, exactly as
	// PlanPurge does it - a Workshop item is not undeployed, so it must not
	// be previewed as one. The freshness snapshot below is still taken over
	// the FULL set: Ruling 5 is about whether the world moved, and an
	// external row appearing or vanishing absolutely is a move.
	toPurge, external := partitionExternal(installed)

	snapshot, err := s.snapshotOf(game.ID, installed)
	if err != nil {
		return nil, err
	}
	plan := &SnapshotRestorePlan{
		GameID: game.ID, Profile: profileName,
		Snapshot: doc.Name, CreatedAt: doc.CreatedAt,
		ToPurge:  toPurge,
		External: external,
		snapshot: snapshot,
		doc:      doc,
	}

	// Review finding 2: if another profile is active, its deployment is
	// part of what stands between the game directory and the recorded
	// state, so it is planned as well. An unreadable/absent default is not
	// an error - it means "default", which is either this profile or a
	// profile with nothing installed.
	if active, err := s.NewProfileManager().GetDefault(ctx, game.ID); err == nil && active != nil && active.Name != profileName {
		activeMods, _ := s.GetInstalledMods(ctx, game.ID, active.Name)
		activeToPurge, activeExternal := partitionExternal(activeMods)
		plan.ActiveProfile = active.Name
		plan.ToPurgeActive = activeToPurge
		plan.External = appendUnseen(plan.External, activeExternal)
		// The freshness precondition for the active profile too, over the
		// FULL set for the same reason the snapshot profile's is (an
		// external row appearing or vanishing is a move).
		if plan.activeSnapshot, err = s.snapshotOf(game.ID, activeMods); err != nil {
			return nil, err
		}
	}

	// The store's CURRENT manifest, not the snapshot's recorded list - see
	// the package comment: the manifest only grows, so the extra rows are
	// exactly the files replaced since the snapshot, which are the ones a
	// restore most needs to put back.
	store := s.originalsStoreFor(game.ID)
	var manifest []OriginalFile
	if store != nil {
		if manifest, err = store.list(); err != nil {
			return nil, err
		}
	}
	plan.originals = manifest
	plan.Originals = make([]SnapshotRestoreOriginal, 0, len(manifest))
	for _, row := range manifest {
		entry := SnapshotRestoreOriginal{
			Root: row.Root, RelativePath: row.RelativePath,
			Status: SnapshotOriginalRestorable,
		}
		if err := store.verify(row); err != nil {
			entry.Status, entry.Reason = SnapshotOriginalUnavailable, err.Error()
		}
		plan.Originals = append(plan.Originals, entry)
	}

	plan.Mods = make([]SnapshotRestoreMod, 0, len(doc.ProfileDocument.Mods))
	recorded := recordedModsByKey(doc)
	installedByKey := make(map[string]domain.InstalledMod, len(installed))
	for _, im := range installed {
		installedByKey[domain.ModKey(im.SourceID, im.ID)] = im
	}
	gameCache := s.GetGameCache(game)
	for _, ref := range doc.ProfileDocument.Mods {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		key := domain.ModKey(ref.SourceID, ref.ModID)
		row := SnapshotRestoreMod{SourceID: ref.SourceID, ModID: ref.ModID, Version: ref.Version}
		if im, ok := recorded[key]; ok {
			row.Name = im.Name
			if row.Version == "" {
				row.Version = im.Version
			}
		}

		// #269: an EXTERNAL row short-circuits everything below it. Steam
		// owns its files where they sit, so there is nothing to fetch and
		// nothing to deploy - and resolving it against its source would ask
		// a Tier-1 Workshop source for a file list it does not serve
		// (source.ErrNotSupported), turning a perfectly restorable snapshot
		// into a preview full of refusals for mods the restore was never
		// going to touch. The one fact worth previewing is whether Steam
		// still has the item on disk, which is a FINDING, not a refusal.
		if im, ok := externalRecord(recorded, installedByKey, key); ok {
			row.External = true
			row.UpdatedAt = im.UpdatedAt
			row.ExternalMissing = !externalContentPresent(im.ExternalPath)
			plan.Mods = append(plan.Mods, row)
			continue
		}

		// A mod that is ALREADY installed at the snapshot's version, with
		// its cache entry intact, needs no source at all: the restore
		// redeploys it from the cache. Asking the source about it would
		// make an offline restore of a game nothing had changed report
		// every mod as unrestorable, and would cost a network round trip
		// per mod for a plan that is going to do nothing.
		if im, ok := installedByKey[key]; ok &&
			(row.Version == "" || im.Version == row.Version) &&
			gameCache.Exists(game.ID, im.SourceID, im.ID, im.Version) {
			row.Cached = true
			plan.Mods = append(plan.Mods, row)
			continue
		}

		// Everything else has to be fetched, so it gets the SAME
		// resolution the profile-apply engine performs - what the plan
		// promises and what the convergence can actually do cannot
		// disagree.
		entry := ProfileApplyInstall{Ref: ref}
		s.resolveProfileApplyInstall(ctx, game, &entry)
		if entry.Error != "" {
			row.Error = entry.Error
			plan.Refusals = append(plan.Refusals, InstalledRef{
				SourceID: ref.SourceID, ModID: ref.ModID, Name: row.Name,
				Version: row.Version, Reason: entry.Error,
			})
		} else {
			row.Cached = entry.Cached
			if entry.Version != "" {
				row.Version = entry.Version
			}
		}
		plan.Mods = append(plan.Mods, row)
	}

	plan.ProfileChanged = s.profileDiffersFromSnapshot(game.ID, doc)
	return plan, nil
}

// recordedModsByKey indexes a snapshot's installed rows, so the plan can
// name a mod even when its source cannot be reached.
func recordedModsByKey(doc *Snapshot) map[string]domain.InstalledMod {
	byKey := make(map[string]domain.InstalledMod, len(doc.Installed))
	for _, im := range doc.Installed {
		byKey[domain.ModKey(im.SourceID, im.ID)] = im
	}
	return byKey
}

// externalRecord answers "is this mod one Steam owns" for a restore, and
// returns the row that says so.
//
// The SNAPSHOT's record wins: a restore's target state is what the snapshot
// says, and its ExternalPath is the directory that snapshot recorded. The
// live installed row is the fallback, for the one case the snapshot cannot
// speak to - a mod adopted from the Workshop AFTER the snapshot was taken.
// Treating that as external too is the conservative direction: the wrong
// answer there would have lmm download and deploy a second copy of
// something Steam already loads, which is exactly what
// CheckExternalInstallExclusivity exists to prevent.
func externalRecord(recorded, installed map[string]domain.InstalledMod, key string) (domain.InstalledMod, bool) {
	if im, ok := recorded[key]; ok && im.External {
		return im, true
	}
	if im, ok := installed[key]; ok && im.External {
		return im, true
	}
	return domain.InstalledMod{}, false
}

// appendUnseen appends the entries of add that dst does not already hold,
// preserving order. The restore's External list spans two profiles and a
// game-global Workshop item is legitimately in both.
func appendUnseen(dst, add []string) []string {
	seen := make(map[string]bool, len(dst))
	for _, v := range dst {
		seen[v] = true
	}
	for _, v := range add {
		if seen[v] {
			continue
		}
		seen[v] = true
		dst = append(dst, v)
	}
	return dst
}

// profileDiffersFromSnapshot reports whether the profile on disk differs
// from the one the snapshot recorded, for the plan's ProfileChanged
// preview. A profile that cannot be read counts as "differs" - the restore
// is going to write it either way, and claiming "unchanged" about a file
// lmm could not read would be a guess.
func (s *Service) profileDiffersFromSnapshot(gameID string, doc *Snapshot) bool {
	current, err := config.LoadProfile(s.configDir, gameID, doc.Profile)
	if err != nil {
		return true
	}
	a, aErr := config.ExportProfile(current)
	b, bErr := config.ExportProfile(config.ProfileFromExported(doc.ProfileDocument))
	if aErr != nil || bErr != nil {
		return true
	}
	return string(a) != string(b)
}

// ApplySnapshotRestore carries out plan under the mutation slot: safety
// snapshot, purge, originals, profile, converge, DB settings.
//
// Ruling 5: the plan is refused with ErrStalePlan when the profile's
// installed-mod set has changed since it was computed.
//
// A failure after the purge has begun comes back as
// *SnapshotRestorePartialError carrying the result so far - the one flow
// where "it failed" is not enough information to act on.
//
// sink may be nil.
func (s *Service) ApplySnapshotRestore(ctx context.Context, game *domain.Game, plan *SnapshotRestorePlan, opts SnapshotRestoreOptions, sink EventSink) (*SnapshotRestoreResult, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return &SnapshotRestoreResult{}, err
	}
	defer release()
	if plan == nil {
		return &SnapshotRestoreResult{}, errors.New("snapshot restore plan is nil: call PlanSnapshotRestore first")
	}
	return s.applySnapshotRestore(ctx, game, plan, opts, sink)
}

func (s *Service) applySnapshotRestore(ctx context.Context, game *domain.Game, plan *SnapshotRestorePlan, opts SnapshotRestoreOptions, sink EventSink) (*SnapshotRestoreResult, error) {
	result := &SnapshotRestoreResult{Snapshot: plan.Snapshot, Profile: plan.Profile}
	if err := s.checkPlanFresh(ctx, plan.GameID, plan.Profile, plan.snapshot); err != nil {
		return result, err
	}
	// Re-review finding N7: the active profile is undeployed by stage 1, so
	// its installed set is an input to this apply and gets the same
	// precondition.
	if plan.ActiveProfile != "" {
		if err := s.checkPlanFresh(ctx, plan.GameID, plan.ActiveProfile, plan.activeSnapshot); err != nil {
			return result, err
		}
	}
	doc := plan.doc
	if doc == nil {
		return result, errors.New("snapshot restore plan carries no snapshot: call PlanSnapshotRestore first")
	}

	emit := func(e Event) {
		if sink != nil {
			sink(e)
		}
	}
	warn := func(msg string) {
		result.Warnings = append(result.Warnings, msg)
		emit(WarningEvent{Scope: Scope{Op: OpSnapshotRestore}, Phase: SnapshotWarning, Message: msg})
	}
	note := func(msg string) {
		result.Notes = append(result.Notes, msg)
		emit(StepEvent{Scope: Scope{Op: OpSnapshotRestore}, Phase: SnapshotNote, Detail: msg})
	}
	// partial wraps a fatal error with everything achieved so far. Used
	// from the purge onwards - a failure BEFORE the purge changed nothing,
	// so it needs no partial-result treatment.
	partial := func(err error) error {
		return &SnapshotRestorePartialError{Err: err, Result: result}
	}

	// A restore discards the present state on purpose, so the way back
	// from it is on by default rather than opt-in. It records the ACTIVE
	// profile, not the snapshot's: "the current state" is what is deployed
	// and which profile is selected, so restoring the safety copy is what
	// puts BOTH back (review finding 2).
	if !opts.NoSafetySnapshot {
		safetyProfile := plan.Profile
		if plan.ActiveProfile != "" {
			safetyProfile = plan.ActiveProfile
		}
		safety, err := s.createSnapshot(ctx, game, safetyProfile, AutoSnapshotName(OpSnapshotRestore, time.Now()), true)
		if err != nil {
			warn(fmt.Sprintf("could not record a snapshot of the current state before restoring: %v", err))
		} else {
			result.SafetySnapshot = safety.Name
			note(fmt.Sprintf("the current state was recorded as %s", safety.Name))
		}
	}

	// --- 1. purge --------------------------------------------------------
	// The ACTIVE profile first, when the restore is switching away from it
	// (review finding 2): its files are on disk and the snapshot does not
	// describe them, so leaving them would end the restore with two
	// profiles deployed at once.
	if plan.ActiveProfile != "" {
		result.SwitchedFrom = plan.ActiveProfile
		if len(plan.ToPurgeActive) > 0 {
			purge, err := s.purgeForRestore(ctx, game, plan.ActiveProfile, plan.ToPurgeActive, opts, emit)
			result.Purged += purge.Purged
			result.Notes = append(result.Notes, purge.Notes...)
			result.Warnings = append(result.Warnings, purge.Warnings...)
			result.Refused = append(result.Refused, purge.Skipped...)
			if err != nil {
				return result, partial(fmt.Errorf("undeploying the active profile %s: %w", plan.ActiveProfile, err))
			}
		}
		note(fmt.Sprintf("the active profile changes from %s to %s", plan.ActiveProfile, plan.Profile))
	}
	if len(plan.ToPurge) > 0 {
		purge, err := s.purgeForRestore(ctx, game, plan.Profile, plan.ToPurge, opts, emit)
		result.Purged += purge.Purged
		result.Notes = append(result.Notes, purge.Notes...)
		result.Warnings = append(result.Warnings, purge.Warnings...)
		result.Refused = append(result.Refused, purge.Skipped...)
		if err != nil {
			return result, partial(fmt.Errorf("purging the current deployment: %w", err))
		}
	}

	// --- 2. originals ----------------------------------------------------
	if err := s.restoreOriginals(ctx, game, plan, result, emit); err != nil {
		return result, partial(err)
	}

	// --- 3. the profile document ----------------------------------------
	if err := s.writeSnapshotProfile(game.ID, doc); err != nil {
		return result, partial(err)
	}
	// And, when the restore carries a switch, WHICH profile is active -
	// through ProfileManager.SetDefault, the same call `lmm profile switch`
	// ends with, so the flag is cleared on every other profile exactly once.
	if plan.ActiveProfile != "" {
		if err := s.NewProfileManager().SetDefault(ctx, game.ID, plan.Profile); err != nil {
			return result, partial(fmt.Errorf("making %s the active profile again: %w", plan.Profile, err))
		}
	}

	// --- 4. converge -----------------------------------------------------
	emit(StepEvent{Scope: Scope{Op: OpSnapshotRestore}, Phase: SnapshotConverging})
	// Re-planned HERE, not at plan time: what profile-apply plans against
	// is the profile file, and step 3 just rewrote it. Every decision the
	// user approved is still the plan's - this only turns the approved
	// target state into the concrete steps that reach it.
	applyPlan, err := s.PlanProfileApply(ctx, game, plan.Profile)
	if err != nil {
		return result, partial(fmt.Errorf("planning the convergence back to %s: %w", plan.Snapshot, err))
	}
	if !applyPlan.NoChanges {
		applyResult, err := s.applyProfileApply(ctx, game, applyPlan, ProfileApplyOptions{}, sink)
		// #386: ToDisable is exactly "installed and enabled here, but the
		// restored profile does not list it" - the rows the restore leaves
		// behind. Recorded AFTER the apply and bounded by what it says it
		// disabled (P1a review F9): the disable loop walks ToDisable in
		// order and counts one per mod, so a restore that stopped part-way
		// - a cancellation between mods - names what it left behind rather
		// than every candidate it never reached.
		if applyResult != nil {
			for i := 0; i < len(applyPlan.ToDisable) && i < applyResult.Disabled; i++ {
				im := &applyPlan.ToDisable[i]
				result.LeftInstalled = append(result.LeftInstalled, InstalledRef{
					SourceID: im.SourceID, ModID: im.ID, Name: im.Name, Version: im.Version,
					Reason: "not listed in the restored profile; its download is kept",
				})
			}
			result.Disabled, result.Enabled = applyResult.Disabled, applyResult.Enabled
			result.Installed, result.Replaced = applyResult.Installed, applyResult.Replaced
			result.Refused = append(result.Refused, applyResult.Failed...)
			result.Notes = append(result.Notes, applyResult.Notes...)
			result.Warnings = append(result.Warnings, applyResult.Warnings...)
		}
		if err != nil {
			return result, partial(fmt.Errorf("converging back to %s: %w", plan.Snapshot, err))
		}
	}

	// --- 4b. the recorded enabled/disabled state -------------------------
	// BEFORE the deploy, not after it (review finding 3). A profile
	// document has no enabled flag - domain.ModReference carries none - so
	// stage 4's convergence re-enables every mod it lists. Applied after
	// the deploy, the flag would say "disabled" over files that were on
	// disk: a wrong restore, and exactly the enabled=false/deployed=true
	// desync #183's self-heal exists to clean up. Applied here, nothing is
	// deployed yet, so it costs a DB write and the deploy simply skips
	// what the snapshot recorded as off.
	s.restoreRecordedEnablement(ctx, game, doc, result, note)

	// --- 4c. the external mods the restore cannot account for ------------
	// #269: a Workshop item is Steam's. The restore neither purged nor
	// deployed it, and its row is still tracked - so the ONE thing left to
	// say is whether Steam still has the item on disk. It is a FINDING and
	// not a refusal: lmm cannot put a subscription back and never claimed
	// it would, so the restore proceeds and names what it could not
	// account for, exactly as `lmm verify`'s external_missing does.
	s.reportMissingExternals(ctx, plan, warn)

	// --- 5. deploy -------------------------------------------------------
	// The purge in stage 1 left every row enabled but not deployed, which
	// the convergence has nothing to say about - so without this the
	// restore would end with a correct database and an empty game
	// directory. This is also what re-applies the snapshot's profile
	// overrides and re-syncs a compile game's merged artifact.
	//
	// Purge is FALSE: stage 1 already did it, and doing it again would
	// undeploy what stage 4 just installed.
	deployResult, err := s.deployProfile(ctx, game, plan.Profile, DeployOptions{
		SkipHooks: opts.SkipHooks, Force: opts.Force,
	}, sink)
	if deployResult != nil {
		result.Deployed = deployResult.Deployed
		result.Notes = append(result.Notes, deployResult.Notes...)
		result.Warnings = append(result.Warnings, deployResult.Warnings...)
		result.Refused = append(result.Refused, deployResult.Skipped...)
	}
	if err != nil {
		return result, partial(fmt.Errorf("redeploying %s: %w", plan.Profile, err))
	}

	// --- 6. the DB settings a profile document cannot express ------------
	s.restoreRecordedSettings(ctx, game, doc, note)
	return result, nil
}

// reportMissingExternals emits one warning per external mod whose recorded
// Steam directory is no longer there.
//
// Re-checked here rather than trusted from the plan: the plan's own
// ExternalMissing is what the user was SHOWN before approving, and this is
// what is true at the moment the restore ran. They agree in every ordinary
// case, and when they do not, the later reading is the one a result should
// carry. It costs one stat per external mod.
func (s *Service) reportMissingExternals(ctx context.Context, plan *SnapshotRestorePlan, warn func(string)) {
	doc := plan.doc
	byKey := recordedModsByKey(doc)
	for _, row := range plan.Mods {
		if err := ctx.Err(); err != nil {
			return
		}
		if !row.External {
			continue
		}
		im, ok := byKey[domain.ModKey(row.SourceID, row.ModID)]
		if !ok || externalContentPresent(im.ExternalPath) {
			continue
		}
		name := row.Name
		if name == "" {
			name = row.ModID
		}
		warn(fmt.Sprintf(
			"%s is a Steam Workshop item and Steam no longer has it at %s - lmm never held a copy, so it cannot put it back; re-subscribe in the Steam client",
			name, im.ExternalPath))
	}
}

// purgeForRestore runs the shared purge loop over one profile's mod set,
// stamped with this flow's Op so a live stream can tell a restore's purge
// from `lmm purge`.
//
// It takes the profile and the mods explicitly because a restore may purge
// TWO sets: the profile it is switching away from, then the snapshot's own
// (review finding 2). Hooks are resolved per profile, so each set runs the
// hooks its own profile configures.
//
// Uninstall is deliberately FALSE: the rows and profile entries are about
// to be rewritten by the convergence, and deleting them here would throw
// away the very state (lock, policy, previous version) the restore is
// putting back.
func (s *Service) purgeForRestore(ctx context.Context, game *domain.Game, profileName string, mods []domain.InstalledMod, opts SnapshotRestoreOptions, emit func(Event)) (*PurgeResult, error) {
	result := &PurgeResult{}
	hooks, err := s.resolvedHooks(ctx, game, profileName)
	if err != nil {
		return result, err
	}
	runner, err := s.hookRunner(ctx)
	if err != nil {
		return result, err
	}
	err = s.purgeMods(ctx, game, profileName, mods, purgeSpec{
		op:      OpSnapshotRestore,
		hooks:   hooks,
		runner:  runner,
		hookCtx: hookContextFor(game),
		force:   opts.Force,
		skip:    opts.SkipHooks,
		emit:    emit,

		warnings: &result.Warnings,
		notes:    &result.Notes,
		skipped:  &result.Skipped,
		purged:   &result.Purged,
	})
	return result, err
}

// restoreOriginals puts back every file the snapshot recorded as replaced.
//
// A row whose stored copy is missing or no longer matches its checksum is
// SKIPPED and recorded, never written: a stored original is the only
// surviving copy of stock content, and writing bytes lmm cannot vouch for
// would turn "lmm kept your original" into "lmm broke your install". Only
// an error that means the whole stage is unusable is fatal.
func (s *Service) restoreOriginals(ctx context.Context, game *domain.Game, plan *SnapshotRestorePlan, result *SnapshotRestoreResult, emit func(Event)) error {
	if len(plan.Originals) == 0 {
		return nil
	}
	store := s.originalsStoreFor(game.ID)
	if store == nil {
		return errors.New("this service has no originals store, so the recorded originals cannot be put back")
	}
	byKey := make(map[string]OriginalFile, len(plan.originals))
	for _, row := range plan.originals {
		byKey[string(row.Root)+"\x00"+row.RelativePath] = row
	}

	emit(StepEvent{Scope: Scope{Op: OpSnapshotRestore, Total: len(plan.Originals)}, Phase: SnapshotRestoringOriginals})
	for idx, entry := range plan.Originals {
		if err := ctx.Err(); err != nil {
			return err
		}
		scope := Scope{Op: OpSnapshotRestore, Index: idx + 1, Total: len(plan.Originals)}
		skip := func(reason string) {
			entry.Status, entry.Reason = SnapshotOriginalUnavailable, reason
			result.OriginalsSkipped = append(result.OriginalsSkipped, entry)
			emit(StepEvent{Scope: scope, Phase: SnapshotOriginalSkipped,
				Detail: fmt.Sprintf("%s: %s", entry.RelativePath, reason)})
		}
		if entry.Status != SnapshotOriginalRestorable {
			skip(entry.Reason)
			continue
		}
		row, ok := byKey[string(entry.Root)+"\x00"+entry.RelativePath]
		if !ok {
			skip("the originals manifest no longer has a row for it")
			continue
		}
		dest, err := originalDestination(game, row)
		if err != nil {
			skip(err.Error())
			continue
		}
		if err := store.restore(row, dest); err != nil {
			skip(err.Error())
			continue
		}
		result.OriginalsRestored++
		emit(StepEvent{Scope: scope, Phase: SnapshotOriginalRestored, Detail: entry.RelativePath})
	}
	return nil
}

// originalDestination resolves a manifest row to the absolute path it goes
// back to, refusing a root the game has no directory for - a game with no
// install_path cannot take back an install-rooted original, and joining
// onto "" would write into the process's working directory.
func originalDestination(game *domain.Game, row OriginalFile) (string, error) {
	var root string
	switch row.Root {
	case OriginalRootModPath:
		root = game.ModPath
	case OriginalRootInstallPath:
		root = game.InstallPath
	default:
		return "", fmt.Errorf("unknown root %q", row.Root)
	}
	if root == "" {
		return "", fmt.Errorf("this game has no %s to restore it to", row.Root)
	}
	rel, err := cleanOriginalRelPath(row.RelativePath)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, filepath.FromSlash(rel)), nil
}

// writeSnapshotProfile puts the recorded profile document back on disk -
// the convergence's precondition, since the profile-apply engine converges
// the installed set onto the profile FILE.
//
// IsDefault is preserved from the live profile: it is local state, not part
// of an exported document, and a restore must not silently demote a
// profile the user had set as default.
func (s *Service) writeSnapshotProfile(gameID string, doc *Snapshot) error {
	restored := config.ProfileFromExported(doc.ProfileDocument)
	if restored == nil {
		return fmt.Errorf("snapshot %s records no profile document", doc.Name)
	}
	// The document's own name/game are authoritative for WHERE it is
	// written, but a snapshot copied between machines could carry a
	// different game id; the game being restored wins.
	restored.GameID = gameID
	restored.Name = doc.Profile
	if current, err := config.LoadProfile(s.configDir, gameID, doc.Profile); err == nil {
		restored.IsDefault = current.IsDefault
	}
	if err := config.SaveProfile(s.configDir, restored); err != nil {
		return fmt.Errorf("restoring the profile %s: %w", doc.Profile, err)
	}
	return nil
}

// restoreRecordedSettings puts back the per-mod database state a profile
// document cannot express: the update policy and the pak-conversion flag.
// The enabled flag used to be here too; review finding 3 moved it earlier,
// to restoreRecordedEnablement, because it is about DEPLOYMENT rather than
// settings and so cannot be applied after the files are already on disk.
//
// Best-effort by design. These are settings, not deployment: a policy that
// could not be written is worth a note, but it is not a reason to report a
// restore that put every file back as a failure. A row the convergence did
// not (re)install is skipped silently - there is nothing to set it on.
func (s *Service) restoreRecordedSettings(ctx context.Context, game *domain.Game, doc *Snapshot, note func(string)) {
	for _, recorded := range doc.Installed {
		if err := ctx.Err(); err != nil {
			return
		}
		current, err := s.db.GetInstalledMod(ctx, recorded.SourceID, recorded.ID, game.ID, doc.Profile)
		if err != nil || current == nil {
			continue
		}
		if current.UpdatePolicy != recorded.UpdatePolicy {
			if err := s.setModUpdatePolicy(ctx, recorded.SourceID, recorded.ID, game.ID, doc.Profile, recorded.UpdatePolicy); err != nil {
				note(fmt.Sprintf("could not restore the update policy for %s: %v", recorded.Name, err))
			}
		}
		if current.ConvertPaks != recorded.ConvertPaks {
			if err := s.setModConvertPaks(ctx, recorded.SourceID, recorded.ID, game.ID, doc.Profile, recorded.ConvertPaks); err != nil {
				note(fmt.Sprintf("could not restore the pak-conversion setting for %s: %v", recorded.Name, err))
			}
		}
	}
}

// restoreRecordedEnablement puts back each mod's recorded enabled flag,
// between the convergence and the deploy.
//
// A DISABLE goes through disableMod rather than the bare setModEnabled
// setter, so the flag and the deployment stay in step (#183): at this point
// in the restore nothing is deployed, so it is the cheap path through the
// same code an ordinary `lmm mod disable` takes, and it clears the deployed
// flag the purge left behind as well. An ENABLE is only the flag - stage 5
// is what deploys it, and calling enableMod here would deploy it twice.
//
// Best-effort, like the settings beside it: a flag that could not be
// written is a note, not a reason to report a restore that put every file
// back as a failure.
func (s *Service) restoreRecordedEnablement(ctx context.Context, game *domain.Game, doc *Snapshot, result *SnapshotRestoreResult, note func(string)) {
	for _, recorded := range doc.Installed {
		if err := ctx.Err(); err != nil {
			return
		}
		current, err := s.db.GetInstalledMod(ctx, recorded.SourceID, recorded.ID, game.ID, doc.Profile)
		if err != nil || current == nil {
			continue
		}
		if current.Enabled == recorded.Enabled {
			continue
		}
		if recorded.Enabled {
			if err := s.setModEnabled(ctx, recorded.SourceID, recorded.ID, game.ID, doc.Profile, true); err != nil {
				note(fmt.Sprintf("could not restore the enabled state for %s: %v", recorded.Name, err))
				continue
			}
			result.Enabled++
			continue
		}
		if _, err := s.disableMod(ctx, game, doc.Profile, recorded.SourceID, recorded.ID); err != nil {
			note(fmt.Sprintf("could not restore the disabled state for %s: %v", recorded.Name, err))
			continue
		}
		result.Disabled++
	}
}
