package core

import (
	"context"
	"errors"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// SwitchPlan is the pure, displayable diff between the currently-active
// default profile and a target profile - computed by PlanProfileSwitch with
// zero side effects, so a caller can render it (in a print block or a
// confirmation prompt) before deciding whether to call
// ApplyProfileSwitch. This is a behavior-preserving extraction of
// cmd/lmm/profile.go's doProfileSwitch's diff computation (through its
// "Show changes" print block) - see the task report for the exact mapping.
//
// CRITICAL: this mirrors the CLI's OWN diff algorithm. (An older, unused
// ProfileManager.Switch implementation coexisted with it until #60 retired
// it - this flow is the only switch implementation now.)
type SwitchPlan struct {
	GameID string `json:"game_id"`
	From   string `json:"from"`
	To     string `json:"to"`

	ToEnable []domain.InstalledMod `json:"to_enable"` // installed+disabled (or installed under a different profile) -> enable, deployed under To

	// ToDisable is every installed row this switch has to turn off. Each
	// entry names the profile it belongs to in ProfileName, and
	// ApplyProfileSwitch undeploys and clears it under THAT profile - not
	// unconditionally under From, as it did while every entry came from the
	// outgoing profile's own set.
	//
	// Two kinds reach it (#431). The first is the historical one: enabled
	// under From and absent from To, or listed by To with the document's
	// `disabled:` marker set - either way From's own row, undeployed with
	// From's link method. The second is To's own row when the target
	// document marks the mod disabled while that row still says enabled -
	// a pair nothing in lmm writes, but one `profile import --force` and
	// `snapshot restore` both produce by replacing the document under live
	// rows.
	//
	// One mod is one entry even when both rows are present (fix-round F5,
	// which is what a plan preview and the Disabled count both read): the
	// entry kept is the one that owns the live deployment, and
	// ApplyProfileSwitch clears the OTHER profile's row alongside it rather
	// than undeploying the same files twice.
	ToDisable []domain.InstalledMod `json:"to_disable"`
	ToInstall []domain.ModReference `json:"to_install"` // in To but not installed anywhere -> download+install (FileIDs preserved from the installed mod's own record when this is really a cache-miss redeploy - see PlanProfileSwitch)

	// PriorVersions carries, keyed by domain.ModKey(SourceID, ModID), the
	// installed row a ToInstall or ToEnable entry converges AWAY from -
	// review round 1 finding 1: neither list's element type has room for
	// it, but ApplyProfileSwitch needs it to know whether a LIVE deployment
	// of another version exists that must be replaced (removing files the
	// new version doesn't serve) rather than merely installed over,
	// mirroring ApplyUpdate's Installer.Replace semantics.
	//
	// Two kinds of entry get one. A #96 version-drift convergence (the
	// target ref pins a version its row does not hold), and - fix round 2,
	// R7 - any entry whose OUTGOING profile has the mod live at a different
	// version than the one this switch deploys: two profiles can hold one
	// unpinned mod at two versions (an `lmm update` under one of them is
	// enough), and only one of them is on disk. The prior row is then the
	// outgoing one, because its version is what is live. Absent for every
	// other entry (brand-new installs, same-version enables, cache-miss
	// redeploys of the live version).
	PriorVersions map[string]domain.InstalledMod `json:"prior_versions,omitempty"`

	// ExternalUnchanged counts the EXTERNAL mods (#269) this switch leaves
	// exactly as they are because it cannot do anything else: a Steam
	// Workshop item is game-global, lmm profiles are not, and changing what
	// Steam has on disk would mean unsubscribing on the user's behalf
	// (a documented NO-GO). Non-zero means the outgoing and incoming
	// profiles differ in external mods, and ApplyProfileSwitch emits one
	// advisory note saying so - see NoteExternalProfileScope. omitzero.
	ExternalUnchanged int `json:"external_unchanged,omitzero"`

	NoChanges     bool `json:"no_changes"`     // To's mod set matches From's content-wise; only SetDefault is needed
	AlreadyActive bool `json:"already_active"` // To is already the active default profile; nothing to plan

	// snapshot is From's installed-mod set this plan was computed against
	// (Ruling 5): ApplyProfileSwitch re-derives it under beginOp and returns
	// ErrStalePlan when it no longer matches, so a plan a frontend held while
	// something else changed From's installed mods is refused rather than
	// applied against a world it never saw. Unexported and outside the wire
	// contract on purpose - see InstallPlan.snapshot's doc comment. Zero
	// value (unset) on the AlreadyActive early return, whose plan is never
	// passed to ApplyProfileSwitch.
	snapshot installedSnapshot `json:"-"`

	// targetSnapshot is the same precondition for To's own installed set.
	// One snapshot was complete while every row this flow read or wrote
	// belonged to From; it no longer is (#430/#431): the disable loop
	// clears target-profile rows, the enable loop writes them, and
	// ToDisable can name one outright. A plan applied over a target row
	// that moved in between would undeploy or enable a stale identity, so
	// the freshness check covers both profiles. It is a marked snapshot
	// (markedSnapshotOf): To's `disabled:` markers decide what this plan
	// enables.
	targetSnapshot installedSnapshot `json:"-"`
}

// PlanProfileSwitch computes the diff between game's currently-active
// default profile and target, without mutating anything (no DB writes, no
// filesystem changes, no deploys) - callers may call this speculatively (to
// render a confirmation prompt) and discard the result without consequence.
// See SwitchPlan's doc comment. The one exception is #431's backfill: what
// it still owes is settled first (settleOwedProfileBackfill), since the
// plan is decided from the target profile's markers.
func (s *Service) PlanProfileSwitch(ctx context.Context, game *domain.Game, target string) (*SwitchPlan, error) {
	s.settleOwedProfileBackfill(ctx)
	pm := s.NewProfileManager()

	targetProfile, err := pm.Get(ctx, game.ID, target)
	if err != nil {
		return nil, fmt.Errorf("profile not found: %s", target)
	}

	currentProfile, err := pm.GetDefault(ctx, game.ID)
	var currentName string
	if err != nil {
		currentName = "default"
	} else {
		currentName = currentProfile.Name
	}

	if currentName == target {
		return &SwitchPlan{GameID: game.ID, From: currentName, To: target, AlreadyActive: true}, nil
	}

	// currentMods/allMods errors are ignored, matching doProfileSwitch
	// exactly (a missing/unreadable profile's mods are simply treated as
	// empty rather than aborting the plan).
	currentMods, _ := s.GetInstalledMods(ctx, game.ID, currentName)
	// Ruling 5: record the installed set this plan is being computed
	// against, so ApplyProfileSwitch can refuse it once From has moved on -
	// snapshotOf reuses currentMods rather than re-querying (see its own doc
	// comment).
	snapshot, err := s.snapshotOf(game.ID, currentMods)
	if err != nil {
		return nil, err
	}

	currentEnabled := make(map[string]*domain.InstalledMod)
	for i := range currentMods {
		if currentMods[i].Enabled {
			currentEnabled[domain.ModKey(currentMods[i].SourceID, currentMods[i].ID)] = &currentMods[i]
		}
	}

	// A mod listed twice is decided by its first reference, as the target
	// loop below and every other flow decide it (firstRefs).
	targetKeys := firstRefs(targetProfile.Mods)

	// allInstalled merges what's installed under the target profile with
	// what's installed under the current one (current wins on key
	// collision) - doProfileSwitch's "Get all installed mods (any profile)
	// to check what's available", which despite the comment only actually
	// considers these two profiles.
	allInstalled := make(map[string]*domain.InstalledMod)
	allMods, _ := s.GetInstalledMods(ctx, game.ID, target)
	// #430: the merge below is lossy on purpose - it answers "is this mod
	// available anywhere, and at what version?" - so the TARGET's own rows
	// are also kept unmerged. Whether a mod ends the switch enabled is a
	// question about the target profile's row, and reading it off a merged
	// map where the outgoing profile wins is exactly the bug: a mod both
	// profiles list took the "already enabled" path and the target profile
	// never got a row at all.
	targetInstalled := make(map[string]*domain.InstalledMod, len(allMods))
	for i := range allMods {
		targetInstalled[domain.ModKey(allMods[i].SourceID, allMods[i].ID)] = &allMods[i]
		allInstalled[domain.ModKey(allMods[i].SourceID, allMods[i].ID)] = &allMods[i]
	}
	for i := range currentMods {
		allInstalled[domain.ModKey(currentMods[i].SourceID, currentMods[i].ID)] = &currentMods[i]
	}

	var toDisable, toEnable []domain.InstalledMod
	var toInstall []domain.ModReference
	var priorVersions map[string]domain.InstalledMod // #96 - see SwitchPlan.PriorVersions
	// disableKeys is which mods already have a ToDisable entry, so the
	// target loop below does not list one a second time under its own
	// profile (fix-round F5).
	disableKeys := make(map[string]bool)
	// liveOutgoing is the outgoing profile's row for key when that row's
	// files are what is on disk - the version an enable or install for the
	// same mod has to replace (fix round 2, R7). The outgoing profile is the
	// active one, so its enabled-and-deployed claim is the one to trust.
	liveOutgoing := func(key string) *domain.InstalledMod {
		if im, ok := currentEnabled[key]; ok && im.Deployed && !im.External {
			return im
		}
		return nil
	}
	recordPrior := func(key string, prior domain.InstalledMod) {
		if priorVersions == nil {
			priorVersions = make(map[string]domain.InstalledMod)
		}
		priorVersions[key] = prior
	}

	// Deterministic order: iterate currentMods in fromProfile's load order
	// (mods enabled but absent from fromProfile.Mods sort first by key - see
	// orderByProfile), filtered down to currentEnabled's members - not `for
	// key, im := range currentEnabled`, which iterates map order.
	externalUnchanged := 0
	for _, im := range orderByProfile(currentProfile, currentMods) {
		key := domain.ModKey(im.SourceID, im.ID)
		if _, enabled := currentEnabled[key]; !enabled {
			continue
		}
		// #431: absent from the target document and listed there with the
		// off marker mean the same thing for the outgoing deployment -
		// these files must come down either way.
		ref, inTarget := targetKeys[key]
		if !inTarget || ref.Disabled {
			// #269: an external mod is never disabled by a profile switch -
			// lmm cannot unsubscribe it, and marking it disabled while the
			// game still loads it would be a lie. It is counted instead, so
			// the switch can say what it left alone.
			if im.External {
				externalUnchanged++
				continue
			}
			toDisable = append(toDisable, im)
			disableKeys[key] = true
		}
	}

	// Deterministic order: iterate targetProfile.Mods in its own load
	// order - not `for key, ref := range targetKeys`, which iterates map
	// order - keeping the exact per-key classification logic. seenTarget
	// guards the same dedup targetKeys gave for free (a mod repeated in
	// targetProfile.Mods, which shouldn't normally happen, is only
	// classified once).
	seenTarget := make(map[string]bool, len(targetProfile.Mods))
	for _, ref := range targetProfile.Mods {
		key := domain.ModKey(ref.SourceID, ref.ModID)
		if seenTarget[key] {
			continue
		}
		seenTarget[key] = true

		im, installed := allInstalled[key]
		// #430, fix-round F6: every branch below classifies the row that
		// OWNS the state this switch has to make true - the target
		// profile's own, whenever it has one. allInstalled answers a
		// different question ("is this mod available anywhere, and at what
		// version?") and the OUTGOING profile wins its key collisions, so
		// reading a version, a FileID list or a flag off it describes the
		// wrong row: the enable then deployed the outgoing version's files
		// while the write landed on a target row that kept its own.
		row := im
		if tr, ok := targetInstalled[key]; ok {
			row = tr
		}
		// #269: an external mod the target profile lists needs no enable
		// and no install - Steam already has it in place, whatever profile
		// is active. EITHER row saying so is enough (fix-round F8):
		// external is a fact about the mod, not about one profile's copy of
		// it, and the `default:` branch below reads the target row's other
		// two flags without re-checking this one.
		if installed && (im.External || row.External) {
			continue
		}
		if ref.Disabled {
			// #431: the document says this mod is in the profile but off,
			// so the switch neither installs nor enables it. The one thing
			// left to do is converge a target row that still says
			// otherwise - see SwitchPlan.ToDisable for how that row gets
			// there. Fix-round F5: one mod is one entry, so this is skipped
			// when the outgoing loop already listed the same mod; that
			// entry carries the row that owns the live deployment, and
			// ApplyProfileSwitch clears the target profile's row alongside
			// it.
			if tr, ok := targetInstalled[key]; ok && !tr.External && (tr.Enabled || tr.Deployed) && !disableKeys[key] {
				toDisable = append(toDisable, *tr)
				disableKeys[key] = true
			}
			continue
		}

		live := liveOutgoing(key)
		switch {
		case !installed:
			toInstall = append(toInstall, ref)
		case ref.Version != "" && row.Version != ref.Version:
			// #96 convergence: the profile names a different version than
			// the installed row - reinstall at the profile's version
			// (downgrades included). ref is passed as-is: its own FileIDs
			// (if any) describe the TARGET version; the installed row's
			// describe the wrong one. The row being converged away from is
			// recorded in priorVersions (review finding 1) so
			// ApplyProfileSwitch's install loop can Replace a live
			// deployment instead of installing over it - the outgoing row
			// when its files are the live ones (R7), since the target row's
			// own claim says nothing about what is on disk.
			toInstall = append(toInstall, ref)
			prior := *row
			if live != nil {
				prior = *live
			}
			if prior.Version != ref.Version {
				recordPrior(key, prior)
			}
		case !s.GetGameCache(game).Exists(game.ID, row.SourceID, row.ID, row.Version):
			// Cache missing - needs a redownload; preserve the installed
			// mod's own FileIDs (not the profile YAML's, which may be
			// empty or stale).
			refWithFileIDs := ref
			refWithFileIDs.FileIDs = row.FileIDs
			toInstall = append(toInstall, refWithFileIDs)
			if live != nil && live.Version != row.Version {
				recordPrior(key, *live)
			}
		default:
			// Installed at the right version with its bytes in the cache,
			// so the only question left is whether it is already live under
			// the TARGET profile (#430). It is not, unless the target has a
			// row of its own that says both enabled and deployed:
			//
			//   - no row at all - the mod is installed under some other
			//     profile only, and the target needs its own row minted
			//     (ApplyProfileSwitch's ErrModNotFound fallback does that);
			//   - a disabled row - the ordinary enable;
			//   - an enabled row that is not deployed - the flag says on
			//     while the game directory does not, which a switch to this
			//     profile is exactly the moment to fix.
			//
			// This used to ask whether the mod was enabled under the
			// profile being switched AWAY from, which answered "already
			// done" for every mod the two profiles share and left the
			// target profile rowless with the files deployed.
			//
			// R7: nor is it live when the outgoing profile has a DIFFERENT
			// version on disk, whatever the target row claims - only one
			// version can be deployed, and the enable has to replace it.
			tr, hasTargetRow := targetInstalled[key]
			drifted := live != nil && live.Version != row.Version
			if !hasTargetRow || !tr.Enabled || !tr.Deployed || drifted {
				toEnable = append(toEnable, *row)
				if drifted {
					recordPrior(key, *live)
				}
			}
		}
	}

	// Fix-round F9: the target profile's own precondition, built from the
	// set already read above (snapshotOf, not a second query, for the
	// reason its doc comment gives - and so the game adapter has its say
	// about this profile's mods too). It records the target document's
	// markers as well (merge gate G1): the loop above enables every
	// unmarked reference over a disabled row, so a marker written before
	// the Apply - a kept backfill retried in its own slot, or another lmm -
	// makes this plan stale. The From snapshot needs none: the outgoing
	// loop reads only the TARGET's markers.
	targetSnapshot, err := s.markedSnapshotOf(game.ID, allMods, disabledKeysOf(targetProfile))
	if err != nil {
		return nil, err
	}

	return &SwitchPlan{
		GameID: game.ID, From: currentName, To: target,
		ToDisable: toDisable, ToEnable: toEnable, ToInstall: toInstall,
		PriorVersions:     priorVersions,
		ExternalUnchanged: externalUnchanged,
		NoChanges:         len(toDisable) == 0 && len(toEnable) == 0 && len(toInstall) == 0,
		snapshot:          snapshot,
		targetSnapshot:    targetSnapshot,
	}, nil
}

// SwitchResult reports the outcome of ApplyProfileSwitch. As with
// DeployResult/UninstallResult, every entry below is always recorded - there
// is no verbosity concept in core.
//
//   - Notes holds every diagnostic doProfileSwitch only printed under
//     --verbose: failed Uninstall/SetModEnabled during the disable loop and
//     failed Install/SetModEnabled during the enable loop. Each entry
//     already carries its historical "Warning: " prefix, matching
//     doProfileSwitch's exact wording; a caller wanting byte-identical
//     output should print each entry to stdout ONLY under --verbose, e.g.
//     `fmt.Printf("  %s\n", n)`. Every entry is also emitted as an event
//     where it happens (SwitchDisableNote/SwitchEnableNote), so a live
//     renderer never needs this slice.
//   - Warnings holds the diagnostics that must reach the user
//     unconditionally: the install loop's refused UpsertMod
//     ("could not update profile: <err>", #294/Ruling 5's class extension,
//     Task 13b - it used to be a --verbose-only Note, mirroring
//     ProfileApplyResult.Warnings' identical #294 entry exactly), then the
//     end-of-switch merged-pak sync's warnings, or "could not sync merged
//     pak: <err>" when the sync itself failed (#197). No entry carries a
//     prefix; a caller prints each to stderr as `Warning: %s`. The install
//     loop's entry is ALSO emitted as a SwitchInstallWarning event at its
//     point of occurrence (the merged-pak ones are not), so a frontend
//     rendering the stream live must not print this slice as well.
//
// On error, the returned result carries any diagnostics/counts accumulated
// before the failure; callers should surface them alongside the error.
type SwitchResult struct {
	Disabled  int      `json:"disabled"`
	Enabled   int      `json:"enabled"`
	Installed int      `json:"installed"`
	Notes     []string `json:"notes,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
}

// ApplyProfileSwitch executes a plan produced by PlanProfileSwitch: disables
// every ToDisable mod, then enables every ToEnable mod, then downloads and
// installs every ToInstall mod, and finally calls ProfileManager.SetDefault
// to make plan.To the active profile - in that order, matching
// doProfileSwitch exactly. sink may be nil.
//
// doProfileSwitch runs no install/uninstall hooks at all (unlike
// DeployProfile/UninstallMod), so ApplyProfileSwitch doesn't either - there
// is deliberately no hook plumbing in its signature or DeployOptions-style
// options struct, since profile switch takes no CLI flags beyond the target
// profile name.
//
// plan is executed EXACTLY as given - this method never re-plans or
// re-validates it against current state. A caller that computed plan some
// time ago (e.g. to show a user a preview) and only calls this later, after
// showing that preview, accepts whatever has changed in the interim as
// already baked into plan; PlanProfileSwitch's own doc comment documents
// why speculative plans are cheap enough to discard and recompute instead.
func (s *Service) ApplyProfileSwitch(ctx context.Context, game *domain.Game, plan *SwitchPlan, sink EventSink) (*SwitchResult, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return &SwitchResult{}, err
	}
	defer release()
	return s.applyProfileSwitch(ctx, game, plan, sink)
}

// clearRowUnderProfile clears enabled and deployed on mod's installed row
// under profileName, and returns the diagnostics to record as Notes (none,
// on the common path). "There is no such row" is the expected answer, not a
// diagnostic: the caller asks about the OTHER profile's copy of a mod
// without knowing whether one exists, and a profile that never installed it
// has nothing to clear.
func (s *Service) clearRowUnderProfile(ctx context.Context, gameID, profileName string, mod *domain.InstalledMod) []string {
	var notes []string
	if err := s.setModEnabled(ctx, mod.SourceID, mod.ID, gameID, profileName, false); err != nil {
		if errors.Is(err, domain.ErrModNotFound) {
			return nil
		}
		notes = append(notes, fmt.Sprintf("Warning: failed to update %s under %s: %v", mod.Name, profileName, err))
	}
	if err := s.setModDeployed(ctx, mod.SourceID, mod.ID, gameID, profileName, false); err != nil &&
		!errors.Is(err, domain.ErrModNotFound) {
		notes = append(notes, fmt.Sprintf("Warning: could not mark %s as not deployed under %s: %v", mod.Name, profileName, err))
	}
	return notes
}

// switchReplacement returns the prior row a switch entry for key replaces,
// and whether to replace it at all: only when that row's files are live, are
// another version than the one being deployed, and are still in the cache
// for Replace to read. Otherwise the entry is a plain Install - with a
// missing old cache entry the old version's own files cannot be told apart,
// the caveat doProfileApply shares.
func (s *Service) switchReplacement(game *domain.Game, plan *SwitchPlan, key, version string) (domain.InstalledMod, bool) {
	prior, ok := plan.PriorVersions[key]
	if !ok || !prior.Deployed || prior.Version == version {
		return prior, false
	}
	return prior, s.GetGameCache(game).Exists(game.ID, prior.SourceID, prior.ID, prior.Version)
}

// releaseReplacedRow records that prior - a row under a profile OTHER than
// the one being switched to, whose live files a switch entry just replaced -
// no longer has a deployment: its deployed flag and its deployed-file
// ownership rows go (#183's pair). Its enabled flag stays; that profile
// still wants the mod, at its own version, and the next switch back to it
// is what puts that version back (R7). A prior row of the target profile's
// own is left to the entry's own row write. Returns the diagnostics to
// record, none on the common path.
func (s *Service) releaseReplacedRow(ctx context.Context, gameID string, plan *SwitchPlan, prior *domain.InstalledMod) []string {
	profile := prior.ProfileName
	if profile == "" {
		profile = plan.From
	}
	if profile == plan.To {
		return nil
	}
	var notes []string
	if err := s.setModDeployed(ctx, prior.SourceID, prior.ID, gameID, profile, false); err != nil && !errors.Is(err, domain.ErrModNotFound) {
		notes = append(notes, fmt.Sprintf("could not mark %s as not deployed under %s: %v", prior.Name, profile, err))
	}
	if err := s.db.DeleteDeployedFiles(ctx, gameID, profile, prior.SourceID, prior.ID); err != nil {
		notes = append(notes, fmt.Sprintf("could not clear %s's deployed files under %s: %v", prior.Name, profile, err))
	}
	return notes
}

func (s *Service) applyProfileSwitch(ctx context.Context, game *domain.Game, plan *SwitchPlan, sink EventSink) (*SwitchResult, error) {
	result := &SwitchResult{}
	emit := func(e Event) {
		if sink != nil {
			sink(e)
		}
	}

	// Ruling 5: the plan is a contract about a world that may have moved.
	// First statement inside the op (ApplyProfileSwitch took beginOp just
	// above), so nothing this call does can race the re-derivation - a stale
	// plan is refused having changed nothing at all.
	if err := s.checkPlanFresh(ctx, plan.GameID, plan.From, plan.snapshot); err != nil {
		return result, err
	}
	// Fix-round F9: and the target profile's, which this flow both reads
	// (the enabled/deployed predicate) and writes (the enable loop, and the
	// disable loop's cross-profile clear below).
	if err := s.checkPlanFresh(ctx, plan.GameID, plan.To, plan.targetSnapshot); err != nil {
		return result, err
	}

	// #350's opt-in auto-snapshot, of the profile being switched AWAY from
	// - that is the state a user would want back. After the freshness
	// check, before the first mutation; a failure is a warning, never a
	// refusal.
	// Both are recorded, not one or the other: since the prune ruling a
	// successful snapshot can still carry a warning (the prune that could
	// not run), and prependWarning/prependSnapshotNote both no-op on "".
	autoName, autoWarn := s.autoSnapshot(ctx, game, plan.From, OpSwitch)
	result.Warnings = prependWarning(result.Warnings, autoWarn)
	result.Notes = prependSnapshotNote(result.Notes, autoName)

	// #269: said once, up front, so the user reads it before the per-mod
	// lines rather than wondering afterwards why their Workshop items
	// followed them across the switch.
	if plan.ExternalUnchanged > 0 {
		note := fmt.Sprintf(NoteExternalProfileScope, plan.ExternalUnchanged)
		result.Notes = append(result.Notes, note)
		emit(StepEvent{Scope: Scope{Op: OpSwitch}, Phase: DeployExternalSkipped, Detail: note})
	}

	// #81: a switch spans two profiles that may carry different explicit
	// link methods - the disable loop undeploys the FROM profile's
	// deployments (which were made with plan.From's method), while the
	// enable and install loops deploy into plan.To.
	fromInstaller, err := s.getInstallerForProfile(ctx, game, plan.From)
	if err != nil {
		return result, err
	}
	toInstaller, err := s.getInstallerForProfile(ctx, game, plan.To)
	if err != nil {
		return result, err
	}
	pm := s.NewProfileManager()

	totalDisable := len(plan.ToDisable)
	for idx := range plan.ToDisable {
		// Task 6 item d (cancel-then-drain): checked between mods, never
		// mid-file-operation - see DeployProfile's identical check.
		if err := ctx.Err(); err != nil {
			return result, err
		}

		im := plan.ToDisable[idx]
		scope := Scope{Op: OpSwitch, Index: idx + 1, Total: totalDisable, ModName: im.Name, Mod: &domain.ModReference{SourceID: im.SourceID, ModID: im.ID}}

		// #431: the entry names its own profile, because ToDisable can now
		// carry the TARGET's row as well as the outgoing one (see
		// SwitchPlan.ToDisable). An entry with no ProfileName - a plan a
		// caller built by hand - keeps the historical From scoping.
		disableProfile, disableInstaller := plan.From, fromInstaller
		if im.ProfileName == plan.To {
			disableProfile, disableInstaller = plan.To, toInstaller
		}

		if err := disableInstaller.Uninstall(ctx, game, &im.Mod, disableProfile); err != nil {
			msg := fmt.Sprintf("Warning: failed to undeploy %s: %v", im.Name, err)
			result.Notes = append(result.Notes, msg)
			emit(StepEvent{Scope: scope, Phase: SwitchDisableNote, Detail: msg})
		}
		if err := s.setModEnabled(ctx, im.SourceID, im.ID, game.ID, disableProfile, false); err != nil {
			msg := fmt.Sprintf("Warning: failed to update %s: %v", im.Name, err)
			result.Notes = append(result.Notes, msg)
			emit(StepEvent{Scope: scope, Phase: SwitchDisableNote, Detail: msg})
		}
		// #183's pair, the one DisableMod already makes and this loop did
		// not: a row whose files just came down must stop claiming they are
		// deployed. It matters most for the target row #431 admits here,
		// whose profile is the one about to become active - but an outgoing
		// row left saying deployed = true was just as wrong, and `lmm
		// verify` had no way to tell that apart from a real deployment.
		if im.Deployed {
			if err := s.setModDeployed(ctx, im.SourceID, im.ID, game.ID, disableProfile, false); err != nil {
				msg := fmt.Sprintf("Warning: could not mark %s as not deployed: %v", im.Name, err)
				result.Notes = append(result.Notes, msg)
				emit(StepEvent{Scope: scope, Phase: SwitchDisableNote, Detail: msg})
			}
		}
		// Fix-round F5's other half. One mod is one entry, and the entry
		// carries the row that owns the live deployment - the outgoing
		// one. The profile about to become the active default can have a
		// row of its own for the same mod, and leaving it saying
		// enabled/deployed after this loop took the files down is the lie
		// #183 exists to stop: `lmm list` under the new default would show
		// a mod that is not in the game directory, and the next plan would
		// schedule the same disable again.
		if disableProfile != plan.To {
			for _, msg := range s.clearRowUnderProfile(ctx, game.ID, plan.To, &im) {
				result.Notes = append(result.Notes, msg)
				emit(StepEvent{Scope: scope, Phase: SwitchDisableNote, Detail: msg})
			}
		}

		result.Disabled++
		emit(ModEvent{Scope: scope, Phase: SwitchDisabled})
	}

	totalEnable := len(plan.ToEnable)
	for idx := range plan.ToEnable {
		if err := ctx.Err(); err != nil {
			return result, err
		}

		im := plan.ToEnable[idx]
		scope := Scope{Op: OpSwitch, Index: idx + 1, Total: totalEnable, ModName: im.Name, Mod: &domain.ModReference{SourceID: im.SourceID, ModID: im.ID}}

		// R7: another version of this mod is live, so the enable replaces
		// it rather than installing beside it.
		prior, replacing := s.switchReplacement(game, plan, domain.ModKey(im.SourceID, im.ID), im.Version)
		deploy := func() error { return toInstaller.Install(ctx, game, &im.Mod, plan.To) }
		if replacing {
			deploy = func() error { return toInstaller.Replace(ctx, game, &prior.Mod, &im.Mod, plan.To) }
		}
		if err := deploy(); err != nil {
			msg := fmt.Sprintf("Warning: failed to deploy %s: %v", im.Name, err)
			result.Notes = append(result.Notes, msg)
			emit(StepEvent{Scope: scope, Phase: SwitchEnableNote, Detail: msg})
			continue
		}
		if err := s.setModEnabled(ctx, im.SourceID, im.ID, game.ID, plan.To, true); err != nil {
			if errors.Is(err, domain.ErrModNotFound) {
				// im's row lives under a different profile (PlanProfileSwitch
				// admits such mods into ToEnable), so the UPDATE-only
				// SetModEnabled matched nothing; create the target-profile row
				// so the deployment we just made isn't orphaned (#60).
				row := im
				row.ProfileName = plan.To
				row.Enabled = true
				row.Deployed = true
				err = s.saveInstalledMod(ctx, &row)
			}
			if err != nil {
				msg := fmt.Sprintf("Warning: failed to update %s: %v", im.Name, err)
				result.Notes = append(result.Notes, msg)
				emit(StepEvent{Scope: scope, Phase: SwitchEnableNote, Detail: msg})
			}
		}
		// #183's pair, and the mirror of the disable loop's own
		// setModDeployed above (fix-round F3): the files are live now, so
		// the target row has to say so. Nothing else writes it on this
		// path - Installer.Install does not touch the column, and the
		// ErrModNotFound fallback above only runs when the row is absent -
		// so without this the row kept deployed = false forever and
		// PlanProfileSwitch's "enabled AND deployed" predicate re-planned
		// the same enable on every later switch into this profile, while
		// `lmm verify`'s loader-layout repair skipped the mod for having
		// no deployment to re-link.
		if err := s.setModDeployed(ctx, im.SourceID, im.ID, game.ID, plan.To, true); err != nil {
			msg := fmt.Sprintf("Warning: could not mark %s as deployed: %v", im.Name, err)
			result.Notes = append(result.Notes, msg)
			emit(StepEvent{Scope: scope, Phase: SwitchEnableNote, Detail: msg})
		}
		if replacing {
			for _, msg := range s.releaseReplacedRow(ctx, game.ID, plan, &prior) {
				msg = "Warning: " + msg
				result.Notes = append(result.Notes, msg)
				emit(StepEvent{Scope: scope, Phase: SwitchEnableNote, Detail: msg})
			}
		}

		result.Enabled++
		emit(ModEvent{Scope: scope, Phase: SwitchEnabled})
	}

	if totalInstall := len(plan.ToInstall); totalInstall > 0 {
		emit(StepEvent{Scope: Scope{Op: OpSwitch, Total: totalInstall}, Phase: SwitchInstalling})

		for idx, ref := range plan.ToInstall {
			if err := ctx.Err(); err != nil {
				return result, err
			}

			scope := Scope{Op: OpSwitch, Index: idx + 1, Total: totalInstall, Mod: &domain.ModReference{SourceID: ref.SourceID, ModID: ref.ModID}}
			emit(ModEvent{Scope: scope, Phase: SwitchInstallingMod})

			fail := func(reason string) {
				emit(ModEvent{Scope: scope, Phase: SwitchInstallError, Detail: reason})
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

			filesToDownload, err := selectFilesForVersion(files, ref.FileIDs, ref.Version)
			if err != nil {
				fail(err.Error())
				continue
			}

			mod.Version = domain.EffectiveInstalledVersion(mod.Version, filesToDownload) // #94

			downloadedFileIDs := make([]string, 0, len(filesToDownload))
			for _, f := range filesToDownload {
				downloadedFileIDs = append(downloadedFileIDs, f.ID)
			}
			// #96 review finding 2: HasFileIDs (not bare Exists) - a version
			// directory can exist yet be only PARTIALLY populated by a
			// previous download run that broke off partway through a
			// multi-file mod; skipping the download on directory presence
			// alone would silently leave it that way forever. Round 2: the
			// check is by FILE ID (the per-file completion markers
			// commitStagedCacheWithMarker stamps), never by FileName - a
			// cache entry for an extracted archive holds member names that
			// match no DownloadableFile, so a name-based check would miss
			// every archive-based mod and redownload a complete cache.
			var checksums []fileChecksum // #372 - saved after the DB row below
			if !s.GetGameCache(game).HasFileIDs(game.ID, mod.SourceID, mod.ID, mod.Version, downloadedFileIDs) {
				downloadFailed := false
				for _, file := range filesToDownload {
					progressFn := func(e Event) {
						if forwardFetchStep(e, scope, emit) {
							return
						}
						d, ok := e.(DownloadEvent)
						if !ok || d.TotalBytes <= 0 {
							return
						}
						emit(DownloadEvent{Scope: scope, Phase: SwitchDownloading, Percent: d.Percent})
					}
					downloadResult, err := s.downloadMod(ctx, ref.SourceID, game, mod, file, progressFn)
					if err != nil {
						emit(ModEvent{Scope: scope, Phase: SwitchDownloadFailed, Detail: fmt.Sprintf("download failed: %v", err)})
						downloadFailed = true
						break
					}
					checksums = appendChecksum(checksums, file.ID, downloadResult)
				}
				emit(StepEvent{Scope: scope, Phase: SwitchDownloadDone})

				if downloadFailed {
					continue
				}
			}

			// #96 convergence (review finding 1): a version-drift entry
			// whose prior installed row is actually live on disk must be
			// replaced (removing files the new version doesn't serve), not
			// just installed over - mirrors ApplyUpdate's Installer.Replace
			// semantics. prior.Deployed alone isn't enough: only Replace
			// when the OLD version's cache entry is still there for it to
			// read from (a corrupted/missing old cache falls back to a
			// bare Install, same as any other toInstall entry - Replace
			// would otherwise hard-fail with "old mod not in cache" and
			// abort convergence). The caveat, shared with the cmd twin
			// (cmd/lmm/profile.go's doProfileApply): without the old file
			// list, files the new version no longer serves stay behind as
			// stale deployments (`lmm verify` surfaces them) - strictly
			// better than failing to converge at all.
			prior, replacing := s.switchReplacement(game, plan, domain.ModKey(ref.SourceID, ref.ModID), mod.Version)
			if replacing {
				if err := toInstaller.Replace(ctx, game, &prior.Mod, mod, plan.To); err != nil {
					fail(fmt.Sprintf("deploy failed: %v", err))
					continue
				}
			} else if err := toInstaller.Install(ctx, game, mod, plan.To); err != nil {
				fail(fmt.Sprintf("deploy failed: %v", err))
				continue
			}

			// Save to DB. Normalize GameID to the lmm game (not the
			// source-mapped value Service.GetMod may have stamped onto
			// mod.GameID for querying the source) so every DB read, which
			// queries by the lmm game ID, can find this row again.
			installedMod := &domain.InstalledMod{
				Mod:          *mod,
				ProfileName:  plan.To,
				UpdatePolicy: domain.UpdateNotify,
				Enabled:      true,
				Deployed:     true, // review finding 3: Install/Replace above just succeeded
				FileIDs:      downloadedFileIDs,
			}
			installedMod.GameID = game.ID
			if err := s.saveInstalledMod(ctx, installedMod); err != nil {
				fail(fmt.Sprintf("save failed: %v", err))
				continue
			}

			if replacing {
				for _, msg := range s.releaseReplacedRow(ctx, game.ID, plan, &prior) {
					result.Warnings = append(result.Warnings, msg)
					emit(WarningEvent{Scope: scope, Phase: SwitchInstallWarning, Message: msg})
				}
			}

			// #372: the row exists now, so what was downloaded above finally
			// has somewhere to record its checksum - without this the switch
			// left every converged file unverifiable.
			for _, msg := range s.recordFileChecksums(ctx, mod.SourceID, mod.ID, game.ID, plan.To, checksums) {
				result.Warnings = append(result.Warnings, msg)
				emit(WarningEvent{Scope: scope, Phase: SwitchInstallWarning, Message: msg})
			}

			modRef := domain.ModReference{SourceID: mod.SourceID, ModID: mod.ID, Version: mod.Version, FileIDs: downloadedFileIDs}
			if err := completeProfileWrite(ctx, func(ctx context.Context) error {
				return pm.UpsertMod(ctx, game.ID, plan.To, modRef)
			}); err != nil {
				// Ruling 16 (A): the DB row and the deployment are already in
				// place, so the ref that completes them is written even under
				// a cancelled ctx - and the cancellation stays fatal instead
				// of being absorbed into the #294 warning below, which is for
				// a business refusal.
				if cerr := ctx.Err(); cerr != nil {
					return result, cerr
				}
				// #294 (Ruling 5's class extension, Task 13b): a refusal
				// here (today, only a LOCKED ref, #143) leaves the profile
				// ref unwritten while the DB row moved, so it is a Warning
				// - unconditional - not the --verbose-only note this used
				// to be, mirroring ApplyProfileApply/ApplyProfileSync's
				// identical #294 fix.
				msg := fmt.Sprintf("could not update profile: %v", err)
				result.Warnings = append(result.Warnings, msg)
				emit(StepEvent{Scope: scope, Phase: SwitchInstallWarning, Detail: msg})
			}

			result.Installed++
			emit(ModEvent{Scope: scope, Phase: SwitchInstalled})
		}
	}

	if err := pm.SetDefault(ctx, game.ID, plan.To); err != nil {
		return result, fmt.Errorf("setting default profile: %w", err)
	}

	// #197 postsmoke fix: Warnings, not Notes - SwitchResult.Notes is
	// --verbose-gated in the CLI, so a sync failure here used to be
	// silent by default.
	if syncWarnings, syncErr := s.syncMergedPak(ctx, game, plan.To); syncErr != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("could not sync merged pak: %v", syncErr))
	} else {
		result.Warnings = append(result.Warnings, syncWarnings...)
	}

	return result, nil
}
