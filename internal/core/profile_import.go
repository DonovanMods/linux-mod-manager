package core

import (
	"context"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
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
// (:411-459) exactly.
func (s *Service) PlanImport(ctx context.Context, game *domain.Game, data []byte) (*ImportPlan, error) {
	pm := s.NewProfileManager()

	profile, err := pm.ParseProfile(data)
	if err != nil {
		return nil, fmt.Errorf("parsing profile: %w", err)
	}

	_, existErr := pm.Get(ctx, game.ID, profile.Name)
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
	allProfiles, _ := pm.List(ctx, game.ID)
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
	profile, err := pm.ImportWithOptions(ctx, plan.data, opts.Force)
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
		installedMod.GameID = game.ID
		if err := s.saveInstalledMod(ctx, installedMod); err != nil {
			fail(fmt.Sprintf("save failed: %v", err))
			continue
		}

		modRef := domain.ModReference{SourceID: mod.SourceID, ModID: mod.ID, Version: mod.Version, FileIDs: downloadedFileIDs}
		// Ruling 16 (A): the DB row and the deployment are already in
		// place, so the profile ref that completes them is written even
		// under a cancelled ctx; the cancellation then ends the run before
		// result.Installed counts this mod or the next one is touched.
		if err := completeProfileWrite(ctx, func(ctx context.Context) error {
			return pm.UpsertMod(ctx, game.ID, profile.Name, modRef)
		}); err != nil {
			if cerr := ctx.Err(); cerr != nil {
				return result, cerr
			}
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
