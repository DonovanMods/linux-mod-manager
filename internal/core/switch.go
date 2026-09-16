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
	// rows. Both rows can be present for one mod, and then both are listed:
	// each carries its own profile's enabled flag, and Installer.Uninstall
	// is idempotent (#260), so the second undeploy of the same files is a
	// no-op rather than a failure.
	ToDisable []domain.InstalledMod `json:"to_disable"`
	ToInstall []domain.ModReference `json:"to_install"` // in To but not installed anywhere -> download+install (FileIDs preserved from the installed mod's own record when this is really a cache-miss redeploy - see PlanProfileSwitch)

	// PriorVersions carries, for each ToInstall entry that is really a #96
	// version-drift convergence (keyed by domain.ModKey(SourceID, ModID)),
	// the installed row being converged AWAY from - review round 1 finding
	// 1: ToInstall's own element type (domain.ModReference) has no room for
	// this, but ApplyProfileSwitch's install loop needs it to know whether
	// a LIVE older deployment exists that must be replaced (removing files
	// the new version doesn't serve) rather than merely installed over -
	// mirroring ApplyUpdate's Installer.Replace semantics. Absent for every
	// other ToInstall entry (brand-new installs, cache-miss redeploys).
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
}

// PlanProfileSwitch computes the diff between game's currently-active
// default profile and target, without mutating anything (no DB writes, no
// filesystem changes, no deploys) - callers may call this speculatively (to
// render a confirmation prompt) and discard the result without consequence.
// See SwitchPlan's doc comment.
func (s *Service) PlanProfileSwitch(ctx context.Context, game *domain.Game, target string) (*SwitchPlan, error) {
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

	targetKeys := make(map[string]domain.ModReference)
	for _, mr := range targetProfile.Mods {
		targetKeys[domain.ModKey(mr.SourceID, mr.ModID)] = mr
	}

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
		// #269: an external mod the target profile lists needs no enable
		// and no install - Steam already has it in place, whatever profile
		// is active.
		if installed && im.External {
			continue
		}
		if ref.Disabled {
			// #431: the document says this mod is in the profile but off,
			// so the switch neither installs nor enables it. The one thing
			// left to do is converge a target row that still says
			// otherwise - see SwitchPlan.ToDisable for how that row gets
			// there and why it is listed even when From's row already is.
			if tr, ok := targetInstalled[key]; ok && !tr.External && (tr.Enabled || tr.Deployed) {
				toDisable = append(toDisable, *tr)
			}
			continue
		}

		switch {
		case !installed:
			toInstall = append(toInstall, ref)
		case ref.Version != "" && im.Version != ref.Version:
			// #96 convergence: the profile names a different version than
			// the installed row - reinstall at the profile's version
			// (downgrades included). ref is passed as-is: its own FileIDs
			// (if any) describe the TARGET version; the installed row's
			// describe the wrong one. The installed row itself is recorded
			// in priorVersions (review finding 1) so ApplyProfileSwitch's
			// install loop can Replace a live older deployment instead of
			// installing over it.
			toInstall = append(toInstall, ref)
			if priorVersions == nil {
				priorVersions = make(map[string]domain.InstalledMod)
			}
			priorVersions[key] = *im
		case !s.GetGameCache(game).Exists(game.ID, im.SourceID, im.ID, im.Version):
			// Cache missing - needs a redownload; preserve the installed
			// mod's own FileIDs (not the profile YAML's, which may be
			// empty or stale).
			refWithFileIDs := ref
			refWithFileIDs.FileIDs = im.FileIDs
			toInstall = append(toInstall, refWithFileIDs)
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
			tr, hasTargetRow := targetInstalled[key]
			if !hasTargetRow || !tr.Enabled || !tr.Deployed {
				toEnable = append(toEnable, *im)
			}
		}
	}

	return &SwitchPlan{
		GameID: game.ID, From: currentName, To: target,
		ToDisable: toDisable, ToEnable: toEnable, ToInstall: toInstall,
		PriorVersions:     priorVersions,
		ExternalUnchanged: externalUnchanged,
		NoChanges:         len(toDisable) == 0 && len(toEnable) == 0 && len(toInstall) == 0,
		snapshot:          snapshot,
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

		if err := toInstaller.Install(ctx, game, &im.Mod, plan.To); err != nil {
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
			key := domain.ModKey(ref.SourceID, ref.ModID)
			if prior, ok := plan.PriorVersions[key]; ok && prior.Deployed &&
				s.GetGameCache(game).Exists(game.ID, prior.SourceID, prior.ID, prior.Version) {
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
