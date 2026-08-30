package core

import (
	"context"
	"errors"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
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

	ToEnable  []domain.InstalledMod `json:"to_enable"`  // installed+disabled (or installed under a different profile) -> enable, deployed under To
	ToDisable []domain.InstalledMod `json:"to_disable"` // enabled under From but absent from To -> disable, undeployed under From
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
// See SwitchPlan's doc comment; ctx is accepted for API consistency with the
// rest of Service's methods and future-proofing, even though today's
// algorithm performs no I/O that needs it.
func (s *Service) PlanProfileSwitch(ctx context.Context, game *domain.Game, target string) (*SwitchPlan, error) {
	pm := s.NewProfileManager()

	targetProfile, err := pm.Get(game.ID, target)
	if err != nil {
		return nil, fmt.Errorf("profile not found: %s", target)
	}

	currentProfile, err := pm.GetDefault(game.ID)
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
	snapshot := snapshotOf(currentMods)

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
	for i := range allMods {
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
	for _, im := range orderByProfile(currentProfile, currentMods) {
		key := domain.ModKey(im.SourceID, im.ID)
		if _, enabled := currentEnabled[key]; !enabled {
			continue
		}
		if _, inTarget := targetKeys[key]; !inTarget {
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
		case !im.Enabled:
			toEnable = append(toEnable, *im)
		default:
			// Installed, cached, and enabled - but was it enabled under the
			// CURRENT profile? If not (e.g. it was only ever enabled under
			// some other profile), it still needs an explicit enable pass
			// for the target.
			if _, wasCurrent := currentEnabled[key]; !wasCurrent {
				toEnable = append(toEnable, *im)
			}
		}
	}

	return &SwitchPlan{
		GameID: game.ID, From: currentName, To: target,
		ToDisable: toDisable, ToEnable: toEnable, ToInstall: toInstall,
		PriorVersions: priorVersions,
		NoChanges:     len(toDisable) == 0 && len(toEnable) == 0 && len(toInstall) == 0,
		snapshot:      snapshot,
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

		if err := fromInstaller.Uninstall(ctx, game, &im.Mod, plan.From); err != nil {
			msg := fmt.Sprintf("Warning: failed to undeploy %s: %v", im.Name, err)
			result.Notes = append(result.Notes, msg)
			emit(StepEvent{Scope: scope, Phase: SwitchDisableNote, Detail: msg})
		}
		if err := s.setModEnabled(ctx, im.SourceID, im.ID, game.ID, plan.From, false); err != nil {
			msg := fmt.Sprintf("Warning: failed to update %s: %v", im.Name, err)
			result.Notes = append(result.Notes, msg)
			emit(StepEvent{Scope: scope, Phase: SwitchDisableNote, Detail: msg})
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
			if !s.GetGameCache(game).HasFileIDs(game.ID, mod.SourceID, mod.ID, mod.Version, downloadedFileIDs) {
				downloadFailed := false
				for _, file := range filesToDownload {
					progressFn := func(e Event) {
						d, ok := e.(DownloadEvent)
						if !ok || d.TotalBytes <= 0 {
							return
						}
						emit(DownloadEvent{Scope: scope, Phase: SwitchDownloading, Percent: d.Percent})
					}
					if _, err := s.downloadMod(ctx, ref.SourceID, game, mod, file, progressFn); err != nil {
						emit(ModEvent{Scope: scope, Phase: SwitchDownloadFailed, Detail: fmt.Sprintf("download failed: %v", err)})
						downloadFailed = true
						break
					}
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

			modRef := domain.ModReference{SourceID: mod.SourceID, ModID: mod.ID, Version: mod.Version, FileIDs: downloadedFileIDs}
			if err := pm.UpsertMod(game.ID, plan.To, modRef); err != nil {
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

	if err := pm.SetDefault(game.ID, plan.To); err != nil {
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

	// Installed holds every profile mod already installed (a DB row exists)
	// at the profile's own version (or with no version recorded in the
	// profile at all) AND cached at that exact version - nothing to do for
	// these. NeedsRedownload holds mods that must be re-fetched: a DB row
	// with no matching cache entry (installed somewhere, cache gone), or -
	// #138's convergence case, mirroring PlanProfileSwitch's #96 drift case -
	// a row installed at a DIFFERENT version than the imported profile
	// records, scheduled for reinstall at the profile's version (downgrades
	// included; each such ref also records the row being converged away from
	// in priorVersions). Missing holds mods with no DB row anywhere (checked
	// across EVERY saved profile for the game, not just the one being
	// imported into - doProfileImport's cross-profile scan, :428-438). All
	// three preserve profile.Mods' own order.
	Installed       []domain.ModReference `json:"installed"`
	NeedsRedownload []domain.ModReference `json:"needs_redownload"`
	Missing         []domain.ModReference `json:"missing"`

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

	// priorVersions maps domain.ModKey keys (for NeedsRedownload's #138
	// version-drift entries only) to the installed row being converged AWAY
	// from - the import twin of SwitchPlan.PriorVersions (see its doc
	// comment): ApplyImport's install loop needs it to know whether a LIVE
	// older deployment exists that must be replaced (removing files the new
	// version doesn't serve) rather than merely installed over. Private,
	// like storedFileIDs: pure plan-to-apply plumbing no preview renders.
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
// (:411-459) exactly. ctx is accepted for API consistency with the rest of
// Service's methods (see PlanProfileSwitch's own doc comment for why a
// speculative, side-effect-free plan doesn't need it today).
func (s *Service) PlanImport(ctx context.Context, game *domain.Game, data []byte) (*ImportPlan, error) {
	pm := s.NewProfileManager()

	profile, err := pm.ParseProfile(data)
	if err != nil {
		return nil, fmt.Errorf("parsing profile: %w", err)
	}

	_, existErr := pm.Get(game.ID, profile.Name)
	exists := existErr == nil

	// installedData keeps each mod key's full installed row (doProfileImport
	// tracked only Version/FileIDs), needed to (a) check the cache at the
	// RIGHT version, (b) preserve the redownload FileIDs rule above, and
	// (c) record the prior row a #138 version-drift entry converges away
	// from (priorVersions needs the whole Mod for Installer.Replace).
	installedMods, _ := s.GetInstalledMods(ctx, game.ID, profile.Name)
	// Ruling 5: record the installed set this plan is being computed
	// against, so ApplyImport can refuse it once profile.Name's has moved on
	// - snapshotOf reuses installedMods rather than re-querying (see its own
	// doc comment).
	snapshot := snapshotOf(installedMods)
	installedData := make(map[string]domain.InstalledMod)
	for _, im := range installedMods {
		key := domain.ModKey(im.SourceID, im.ID)
		installedData[key] = im
	}

	// Cross-profile scan (:428-438): a mod installed under some OTHER saved
	// profile still counts as "installed", not "missing". Errors from List/
	// GetInstalledMods are ignored, matching doProfileImport exactly (a
	// missing/unreadable profile simply contributes nothing).
	allProfiles, _ := pm.List(game.ID)
	for _, p := range allProfiles {
		mods, _ := s.GetInstalledMods(ctx, game.ID, p.Name)
		for _, im := range mods {
			key := domain.ModKey(im.SourceID, im.ID)
			if _, exists := installedData[key]; !exists {
				installedData[key] = im
			}
		}
	}

	var installed, needsRedownload, missing []domain.ModReference
	storedFileIDs := make(map[string][]string)
	var priorVersions map[string]domain.InstalledMod // #138 - see ImportPlan.priorVersions
	gameCache := s.GetGameCache(game)
	for _, ref := range profile.Mods {
		key := domain.ModKey(ref.SourceID, ref.ModID)
		im, inDB := installedData[key]
		switch {
		case !inDB:
			missing = append(missing, ref)
		case ref.Version != "" && im.Version != ref.Version:
			// #138 convergence, mirroring PlanProfileSwitch's #96 drift
			// case: the imported profile names a different version than the
			// installed row - reinstall at the profile's version (downgrades
			// included). ref is passed as-is: its own FileIDs (if any)
			// describe the TARGET version; the installed row's describe the
			// wrong one (so no storedFileIDs entry). The installed row
			// itself is recorded in priorVersions so ApplyImport's install
			// loop can Replace a live older deployment instead of
			// installing over it.
			needsRedownload = append(needsRedownload, ref)
			if priorVersions == nil {
				priorVersions = make(map[string]domain.InstalledMod)
			}
			priorVersions[key] = im
		case gameCache.Exists(game.ID, ref.SourceID, ref.ModID, im.Version):
			installed = append(installed, ref)
		default:
			needsRedownload = append(needsRedownload, ref)
			storedFileIDs[key] = im.FileIDs
		}
	}

	return &ImportPlan{
		Profile:         profile,
		Installed:       installed,
		NeedsRedownload: needsRedownload,
		Missing:         missing,
		Exists:          exists,
		data:            data,
		storedFileIDs:   storedFileIDs,
		priorVersions:   priorVersions,
		snapshot:        snapshot,
	}, nil
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
	profile, err := pm.ImportWithOptions(plan.data, opts.Force)
	if err != nil {
		return result, fmt.Errorf("importing profile: %w", err)
	}
	result.ProfileName = profile.Name
	emit(StepEvent{Scope: Scope{Op: OpImport, ModName: profile.Name}, Phase: ImportSaved})

	toDownload := make([]domain.ModReference, 0, len(plan.NeedsRedownload)+len(plan.Missing))
	toDownload = append(toDownload, plan.NeedsRedownload...)
	toDownload = append(toDownload, plan.Missing...)

	if len(toDownload) == 0 {
		return result, nil
	}
	if opts.NoInstall || !opts.Install {
		result.Skipped = len(toDownload)
		return result, nil
	}

	installer, err := s.getInstallerForProfile(ctx, game, profile.Name)
	if err != nil {
		return result, err
	}
	total := len(toDownload)
	emit(StepEvent{Scope: Scope{Op: OpImport, Total: total}, Phase: ImportInstalling})

	for idx, ref := range toDownload {
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
			emit(ModEvent{Scope: scope, Phase: ImportModFailed, Detail: reason})
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
		key := domain.ModKey(ref.SourceID, ref.ModID)
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
		if !s.GetGameCache(game).HasFileIDs(game.ID, mod.SourceID, mod.ID, mod.Version, downloadedFileIDs) {
			downloadFailed := false
			for _, file := range filesToDownload {
				if err := ctx.Err(); err != nil {
					return result, err
				}
				progressFn := func(e Event) {
					d, ok := e.(DownloadEvent)
					if !ok || d.TotalBytes <= 0 {
						return
					}
					emit(DownloadEvent{Scope: scope, Phase: ImportDownloading, Percent: d.Percent})
				}
				if _, err := s.downloadMod(ctx, ref.SourceID, game, mod, file, progressFn); err != nil {
					fail(fmt.Sprintf("download failed: %v", err))
					downloadFailed = true
					break
				}
			}
			emit(StepEvent{Scope: scope, Phase: ImportDownloadDone})

			if downloadFailed {
				continue
			}
		}

		// #138 convergence: a version-drift entry whose prior installed row
		// is actually live on disk must be replaced (removing files the new
		// version doesn't serve), not just installed over - the same gate,
		// with the same caveats, as ApplyProfileSwitch's install loop (see
		// its comment): only Replace when the OLD version's cache entry is
		// still there for it to read from; a corrupted/missing old cache
		// falls back to a bare Install rather than hard-failing convergence.
		if prior, ok := plan.priorVersions[key]; ok && prior.Deployed &&
			s.GetGameCache(game).Exists(game.ID, prior.SourceID, prior.ID, prior.Version) {
			if err := installer.Replace(ctx, game, &prior.Mod, mod, profile.Name); err != nil {
				fail(fmt.Sprintf("deploy failed: %v", err))
				continue
			}
		} else if err := installer.Install(ctx, game, mod, profile.Name); err != nil {
			fail(fmt.Sprintf("deploy failed: %v", err))
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
		installedMod.Mod.GameID = game.ID
		if err := s.saveInstalledMod(ctx, installedMod); err != nil {
			fail(fmt.Sprintf("save failed: %v", err))
			continue
		}

		modRef := domain.ModReference{SourceID: mod.SourceID, ModID: mod.ID, Version: mod.Version, FileIDs: downloadedFileIDs}
		if err := pm.UpsertMod(game.ID, profile.Name, modRef); err != nil {
			msg := fmt.Sprintf("Warning: could not update profile: %v", err)
			result.Notes = append(result.Notes, msg)
			emit(StepEvent{Scope: scope, Phase: ImportNote, Detail: msg})
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
