// Package core: this file holds the profile-apply flow -
// PlanProfileApply/ApplyProfileApply and the types they own - lifted out of
// cmd/lmm/profile.go's doProfileApply by v2 Phase 2 Unit J (#290). The CLI
// keeps the prompt and every printed line; the diff, the source resolution
// and the disable/enable/install execution live here.
//
// Deliberately NOT unified with ApplyProfileSwitch (whose install loop is a
// near twin) or with ApplyInstall: the spec's Phase 2 row for this unit says
// to "lift its semantics faithfully first; unifying with ApplyInstall is a
// separate, named decision". What IS shared is the event vocabulary - every
// line doProfileApply prints has the same wording as its doProfileSwitch
// counterpart, so this flow emits the Switch* DeployPhase family rather than
// forking a byte-identical copy of it; Scope.Op (OpProfileApply) is what
// tells the two flows apart on the wire.
package core

import (
	"context"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
)

// ProfileApplyPlan is the pure, displayable diff between a profile and the
// mods installed under it - computed by PlanProfileApply with zero side
// effects, so a caller can render it (or a confirmation prompt) before
// deciding whether to call ApplyProfileApply.
type ProfileApplyPlan struct {
	GameID  string `json:"game_id"`
	Profile string `json:"profile"`

	// ToDisable is every mod installed AND enabled under Profile that the
	// profile no longer lists: undeploy it and clear its enabled flag.
	ToDisable []domain.InstalledMod `json:"to_disable"`
	// ToEnable is every listed mod that is installed, disabled, and still
	// cached at its installed version: deploy it and set its enabled flag.
	// A disabled mod whose cache entry is GONE cannot be deployed, so it
	// lands in ToInstall instead (carrying the DB row's own FileIDs).
	ToEnable []domain.InstalledMod `json:"to_enable"`

	// ToInstall is the (re)install list, in the order doProfileApply built
	// it: first the entries the installed-mods pass produced - #96
	// version-drift convergences and cache-miss re-downloads, in
	// orderByProfile order - then the profile's own not-installed mods, in
	// profile order. It is ONE ordered list rather than the task brief's
	// separate to_install/to_reinstall buckets precisely because those two
	// groups interleave: a cache-miss re-download can precede a
	// convergence, and the CLI prints (and executes) the whole sequence as
	// a single "Will install N mod(s)" block. Replaces marks the entries
	// that go through Installer.Replace; ProfileApplyResult counts the two
	// kinds separately.
	ToInstall []ProfileApplyInstall `json:"to_install"`

	// NoChanges is true when all three buckets are empty - the system
	// already matches the profile and ApplyProfileApply has nothing to do
	// (the CLI prints "System already matches profile <name>." and stops
	// without even syncing the merged pak, exactly as before).
	NoChanges bool `json:"no_changes"`

	// snapshot is ruling 5's staleness precondition: the installed-mod set
	// this plan was computed from. ApplyProfileApply re-derives it and
	// returns ErrStalePlan when it no longer matches. Unexported and
	// outside the wire contract - a frontend round-trips the plan through
	// its own store, not through JSON.
	snapshot installedSnapshot `json:"-"`
}

// ProfileApplyInstall is one entry of ProfileApplyPlan.ToInstall, resolved
// against its source AT PLAN TIME: the plan carries everything the apply
// needs, so ApplyProfileApply performs no lookups of its own (spec §4, "the
// plan is the contract"). Resolution is read-only - a caller may compute a
// plan speculatively and discard it.
type ProfileApplyInstall struct {
	// Ref is the reference the diff produced: the profile's own ref for a
	// new install or a #96 convergence (its Version/FileIDs describe the
	// TARGET version), or a ref rebuilt from the installed row for a
	// cache-miss re-download (the DB's FileIDs describe what was installed,
	// which the profile's may not).
	Ref domain.ModReference `json:"ref"`
	// Mod is the freshly fetched mod, with Version already stamped to the
	// selected file's version (#94's domain.EffectiveInstalledVersion) -
	// the version the DB row and the cache entry will carry. Nil exactly
	// when Error is set.
	Mod *domain.Mod `json:"mod,omitempty"`
	// Files is selectFilesForVersion's pick for Ref (its FileIDs when it
	// has any, else the version/primary heuristics) - what the apply
	// downloads, in order.
	Files []*domain.DownloadableFile `json:"files"`
	// Version is Mod.Version after the #94 stamp, repeated here so a
	// frontend rendering the plan needn't dereference Mod.
	Version string `json:"version"`
	// Cached is true when the cache already holds EVERY selected file's
	// completion marker at Version (#96's cache-first guard, by file ID -
	// never by file name, which an extracted archive's members never
	// match). The apply skips the download step entirely for these.
	Cached bool `json:"cached"`
	// Replaces is the installed row this entry converges away from, set
	// only when that row is genuinely deployed AND its own cache entry is
	// still present: Installer.Replace reads the old entry to work out
	// which files to retire and hard-fails without it, so a pruned old
	// cache falls back to a bare Install (leaving files the new version no
	// longer serves behind for `lmm verify` to surface - strictly better
	// than failing to converge at all).
	Replaces *domain.InstalledMod `json:"replaces,omitempty"`
	// Error is a plan-time resolution failure, already worded exactly as
	// the CLI prints it ("failed to fetch mod: ...", "failed to get files:
	// ...", "no downloadable files", or selectFilesForVersion's own text).
	// The entry keeps its place in ToInstall: ApplyProfileApply reports it
	// at that position and continues with the rest, matching
	// doProfileApply's per-mod continue.
	Error string `json:"error,omitempty"`
}

// profileApplyFailure builds the InstalledRef ApplyProfileApply records for
// an entry it could not install. Identity comes from the plan entry's Ref
// (always present); Name/Version come from the resolved mod when there is
// one - a plan-time resolution failure has no mod to name.
func profileApplyFailure(entry *ProfileApplyInstall, reason string) InstalledRef {
	ref := InstalledRef{SourceID: entry.Ref.SourceID, ModID: entry.Ref.ModID, Reason: reason}
	if entry.Mod != nil {
		ref.Name = entry.Mod.Name
		ref.Version = entry.Version
	}
	return ref
}

// ProfileApplyOptions is ApplyProfileApply's option set. It is deliberately
// EMPTY today: `lmm profile apply` takes no flag the engine reads (--yes
// gates the frontend's own prompt, never core), and unlike
// deploy/install/update the flow runs no hooks at all, so there is no Force
// or SkipHooks to honor - a field nothing reads would be exactly the "flag
// lies" trap #93/#96/#140 keep re-teaching. It exists so options can be
// added without changing every caller.
type ProfileApplyOptions struct{}

// ProfileApplyResult reports the outcome of ApplyProfileApply. As with
// SwitchResult, every entry below is always recorded - there is no
// verbosity concept in core:
//
//   - Notes holds the diagnostics doProfileApply only printed under
//     --verbose: a failed Uninstall/SetModEnabled in the disable loop and a
//     failed Install/SetModEnabled in the enable loop. Each entry carries
//     its historical "Warning: " prefix already; a caller wanting
//     byte-identical output prints it to stdout ONLY under --verbose, as
//     `fmt.Printf("  %s\n", n)`. Every entry is also emitted as an event
//     where it happens, so a live renderer never needs this slice.
//   - Warnings holds the diagnostics that must reach the user
//     unconditionally: the install loop's refused UpsertMod
//     ("could not update profile: <err>", #294/Ruling 5 - it used to be a
//     --verbose-only Note, which hid a real DB-vs-profile divergence), then
//     the end-of-apply merged-pak sync's warnings, or "could not sync
//     merged pak: <err>" when the sync itself failed (#197). No entry
//     carries a prefix; a caller prints each to stderr as `Warning: %s`.
//     The install loop's entry is ALSO emitted as a SwitchInstallWarning
//     event at its point of occurrence (the merged-pak ones are not), so a
//     frontend rendering the stream live must not print this slice as well.
//   - Failed holds one InstalledRef per mod the apply could not install,
//     mirroring the events the loop emitted: the ref's identity plus the
//     reason as data, rather than the pre-formatted
//     "<source>:<mod>: <reason>" line it used to be (spec §4). Name is
//     empty for an entry that failed to resolve at plan time - there is no
//     mod to name yet.
type ProfileApplyResult struct {
	Disabled  int            `json:"disabled"`
	Enabled   int            `json:"enabled"`
	Installed int            `json:"installed"`
	Replaced  int            `json:"replaced"`
	Failed    []InstalledRef `json:"failed,omitempty"`
	Notes     []string       `json:"notes,omitempty"`
	Warnings  []string       `json:"warnings,omitempty"`
}

// PlanProfileApply computes what it would take to make the mods installed
// under profileName match the profile itself, without mutating anything (no
// DB writes, no filesystem changes, no downloads) - callers may call it
// speculatively, render it, and discard it.
//
// The three buckets are built exactly as doProfileApply built them,
// including their deterministic ordering (orderByProfile for the
// installed-mods pass, profile order for the profile pass - never Go map
// order). Each ToInstall entry is then resolved against its source; a
// resolution failure fails that ONE entry (recorded as Error text) rather
// than the plan, because doProfileApply printed those failures per mod,
// inside its install loop, and carried on.
func (s *Service) PlanProfileApply(ctx context.Context, game *domain.Game, profileName string) (*ProfileApplyPlan, error) {
	pm := s.NewProfileManager()

	profile, err := pm.Get(game.ID, profileName)
	if err != nil {
		return nil, fmt.Errorf("profile not found: %s", profileName)
	}

	installedMods, err := s.GetInstalledMods(ctx, game.ID, profileName)
	if err != nil {
		return nil, fmt.Errorf("getting installed mods: %w", err)
	}

	installedByKey := make(map[string]*domain.InstalledMod, len(installedMods))
	for i := range installedMods {
		installedByKey[domain.ModKey(installedMods[i].SourceID, installedMods[i].ID)] = &installedMods[i]
	}

	profileKeys := make(map[string]domain.ModReference, len(profile.Mods))
	for _, mr := range profile.Mods {
		profileKeys[domain.ModKey(mr.SourceID, mr.ModID)] = mr
	}

	plan := &ProfileApplyPlan{GameID: game.ID, Profile: profileName}
	gameCache := s.GetGameCache(game)

	// Pass 1 - installed mods against the profile. Deterministic order:
	// orderByProfile(profile, installedMods), not a range over
	// installedByKey (which iterates map order); installedByKey stays for
	// the membership lookup in pass 2.
	ordered := orderByProfile(profile, installedMods)
	for i := range ordered {
		im := &ordered[i]
		key := domain.ModKey(im.SourceID, im.ID)

		ref, inProfile := profileKeys[key]
		if !inProfile {
			// Installed but no longer listed - disable it.
			if im.Enabled {
				plan.ToDisable = append(plan.ToDisable, *im)
			}
			continue
		}

		if ref.Version != "" && im.Version != ref.Version {
			// #96 convergence: the profile names a different version than
			// the installed row - reinstall at the profile's version
			// (downgrades included), regardless of enabled state. ref is
			// passed as-is: its own FileIDs (if any) describe the TARGET
			// version; the installed row's describe the wrong one. A live
			// older deployment whose cache entry survives is recorded so
			// the apply can Replace it - see ProfileApplyInstall.Replaces.
			// This Exists check runs at PLAN time, not after the download as
			// doProfileApply's did; only an external process pruning the old
			// cache entry between plan and apply could flip the verdict (no
			// in-tree code path does - downloadModToCache only removes its
			// own temp/stage dirs), and if it did, Installer.Replace would
			// hard-fail with "old mod not in cache" where the old code fell
			// back to a bare Install.
			var replaces *domain.InstalledMod
			if im.Deployed && gameCache.Exists(game.ID, im.SourceID, im.ID, im.Version) {
				prior := *im
				replaces = &prior
			}
			plan.ToInstall = append(plan.ToInstall, ProfileApplyInstall{Ref: ref, Replaces: replaces})
			continue
		}

		if !im.Enabled {
			if gameCache.Exists(game.ID, im.SourceID, im.ID, im.Version) {
				plan.ToEnable = append(plan.ToEnable, *im)
				continue
			}
			// Cache gone - it must be fetched again before it can be
			// deployed. The DB row's FileIDs describe what was installed;
			// the profile's ref may carry none at all.
			plan.ToInstall = append(plan.ToInstall, ProfileApplyInstall{Ref: domain.ModReference{
				SourceID: im.SourceID,
				ModID:    im.ID,
				Version:  im.Version,
				FileIDs:  im.FileIDs,
			}})
		}
	}

	// Pass 2 - profile mods against what is installed. Deterministic order:
	// profile.Mods, not a range over profileKeys; seen reproduces the dedup
	// profileKeys gave that pass for free.
	seen := make(map[string]bool, len(profile.Mods))
	for _, ref := range profile.Mods {
		key := domain.ModKey(ref.SourceID, ref.ModID)
		if seen[key] {
			continue
		}
		seen[key] = true
		if _, installed := installedByKey[key]; !installed {
			plan.ToInstall = append(plan.ToInstall, ProfileApplyInstall{Ref: ref})
		}
	}

	plan.NoChanges = len(plan.ToDisable) == 0 && len(plan.ToEnable) == 0 && len(plan.ToInstall) == 0

	for i := range plan.ToInstall {
		s.resolveProfileApplyInstall(ctx, game, &plan.ToInstall[i])
	}

	snapshot, err := s.currentInstalledSnapshot(ctx, game.ID, profileName)
	if err != nil {
		return nil, err
	}
	plan.snapshot = snapshot

	return plan, nil
}

// resolveProfileApplyInstall fills in entry's Mod/Files/Version/Cached from
// its source, or records the failure in entry.Error - doProfileApply's
// fetch/get-files/select sequence, verbatim, with each failure worded as it
// printed it.
func (s *Service) resolveProfileApplyInstall(ctx context.Context, game *domain.Game, entry *ProfileApplyInstall) {
	mod, err := s.GetMod(ctx, entry.Ref.SourceID, game.ID, entry.Ref.ModID)
	if err != nil {
		entry.Error = fmt.Sprintf("failed to fetch mod: %v", err)
		return
	}

	files, err := s.GetModFiles(ctx, entry.Ref.SourceID, mod)
	if err != nil {
		entry.Error = fmt.Sprintf("failed to get files: %v", err)
		return
	}
	if len(files) == 0 {
		entry.Error = "no downloadable files"
		return
	}

	// Ref.FileIDs is the single selection input on both paths: a cache-miss
	// re-download carries the DB row's IDs, every other entry the profile's
	// own (empty for an unpinned ref, which selectFilesForVersion then
	// resolves by version/primary).
	selected, err := selectFilesForVersion(files, entry.Ref.FileIDs, entry.Ref.Version)
	if err != nil {
		entry.Error = err.Error()
		return
	}

	mod.Version = domain.EffectiveInstalledVersion(mod.Version, selected) // #94

	entry.Mod = mod
	entry.Files = selected
	entry.Version = mod.Version
	// #96 cache-first, by FILE ID (the per-file completion markers
	// commitStagedCacheWithMarker stamps), never by bare directory presence
	// (a partially-populated version directory would be skipped forever) or
	// by file name (an extracted archive's members match no
	// DownloadableFile, so every archive-based mod would redownload).
	entry.Cached = s.GetGameCache(game).HasFileIDs(game.ID, mod.SourceID, mod.ID, mod.Version, profileApplyFileIDs(selected))
}

// profileApplyFileIDs is the ID list of the files an entry will download -
// the cache's completion-marker keys and the FileIDs the installed row and
// the profile ref both end up recording.
func profileApplyFileIDs(files []*domain.DownloadableFile) []string {
	ids := make([]string, 0, len(files))
	for _, f := range files {
		ids = append(ids, f.ID)
	}
	return ids
}

// ApplyProfileApply executes a plan produced by PlanProfileApply: disables
// every ToDisable mod, then enables every ToEnable mod, then downloads and
// deploys every ToInstall entry, then syncs the merged pak - in that order,
// matching doProfileApply exactly. sink may be nil.
//
// Ruling 5: the plan is refused with ErrStalePlan when the profile's
// installed-mod set has changed since it was computed. Beyond that the plan
// is executed exactly as given - it already carries each entry's resolved
// mod, file selection and cache verdict.
//
// doProfileApply runs no install/uninstall hooks at all (unlike
// DeployProfile/ApplyInstall), so this doesn't either - see
// ProfileApplyOptions.
func (s *Service) ApplyProfileApply(ctx context.Context, game *domain.Game, plan *ProfileApplyPlan, opts ProfileApplyOptions, sink EventSink) (*ProfileApplyResult, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return &ProfileApplyResult{}, err
	}
	defer release()
	return s.applyProfileApply(ctx, game, plan, opts, sink)
}

func (s *Service) applyProfileApply(ctx context.Context, game *domain.Game, plan *ProfileApplyPlan, _ ProfileApplyOptions, sink EventSink) (*ProfileApplyResult, error) {
	result := &ProfileApplyResult{}
	if err := s.checkPlanFresh(ctx, plan.GameID, plan.Profile, plan.snapshot); err != nil {
		return result, err
	}

	// doProfileApply returned before the merged-pak sync when all three
	// buckets were empty; the CLI still gets that today because it checks
	// plan.NoChanges itself and never calls Apply at all (cmd/lmm/profile.go),
	// but this guard keeps the rule here too so a future frontend calling
	// Apply unconditionally doesn't get a sync the CLI never performed.
	if plan.NoChanges {
		return result, nil
	}

	emit := func(e Event) {
		if sink != nil {
			sink(e)
		}
	}
	note := func(scope Scope, phase DeployPhase, msg string) {
		result.Notes = append(result.Notes, msg)
		emit(StepEvent{Scope: scope, Phase: phase, Detail: msg})
	}
	// warn is note's unconditional sibling (#294): the diagnostic lands on
	// Warnings, which the CLI prints to stderr regardless of --verbose. msg
	// carries no "Warning: " prefix - the caller renders one.
	warn := func(scope Scope, phase DeployPhase, msg string) {
		result.Warnings = append(result.Warnings, msg)
		emit(StepEvent{Scope: scope, Phase: phase, Detail: msg})
	}

	installer, err := s.getInstallerForProfile(ctx, game, plan.Profile)
	if err != nil {
		return result, err
	}
	pm := s.NewProfileManager()

	totalDisable := len(plan.ToDisable)
	for idx := range plan.ToDisable {
		// Cancellation is checked between mods, never mid-file-operation -
		// the convention every other flow follows.
		if err := ctx.Err(); err != nil {
			return result, err
		}

		im := plan.ToDisable[idx]
		scope := Scope{Op: OpProfileApply, Index: idx + 1, Total: totalDisable, ModName: im.Name,
			Mod: &domain.ModReference{SourceID: im.SourceID, ModID: im.ID}}

		if err := installer.Uninstall(ctx, game, &im.Mod, plan.Profile); err != nil {
			note(scope, SwitchDisableNote, fmt.Sprintf("Warning: failed to undeploy %s: %v", im.Name, err))
		}
		if err := s.setModEnabled(ctx, im.SourceID, im.ID, game.ID, plan.Profile, false); err != nil {
			note(scope, SwitchDisableNote, fmt.Sprintf("Warning: failed to update %s: %v", im.Name, err))
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
		scope := Scope{Op: OpProfileApply, Index: idx + 1, Total: totalEnable, ModName: im.Name,
			Mod: &domain.ModReference{SourceID: im.SourceID, ModID: im.ID}}

		if err := installer.Install(ctx, game, &im.Mod, plan.Profile); err != nil {
			// Unlike the disable loop's, a failed deploy is fatal FOR THIS
			// MOD: it is skipped without its enabled flag being set.
			note(scope, SwitchEnableNote, fmt.Sprintf("Warning: failed to deploy %s: %v", im.Name, err))
			continue
		}
		if err := s.setModEnabled(ctx, im.SourceID, im.ID, game.ID, plan.Profile, true); err != nil {
			note(scope, SwitchEnableNote, fmt.Sprintf("Warning: failed to update %s: %v", im.Name, err))
		}

		result.Enabled++
		emit(ModEvent{Scope: scope, Phase: SwitchEnabled})
	}

	if totalInstall := len(plan.ToInstall); totalInstall > 0 {
		emit(StepEvent{Scope: Scope{Op: OpProfileApply, Total: totalInstall}, Phase: SwitchInstalling})

		for idx := range plan.ToInstall {
			if err := ctx.Err(); err != nil {
				return result, err
			}

			entry := &plan.ToInstall[idx]
			scope := Scope{Op: OpProfileApply, Index: idx + 1, Total: totalInstall,
				Mod: &domain.ModReference{SourceID: entry.Ref.SourceID, ModID: entry.Ref.ModID}}
			emit(ModEvent{Scope: scope, Phase: SwitchInstallingMod})

			fail := func(reason string) {
				result.Failed = append(result.Failed, profileApplyFailure(entry, reason))
				emit(ModEvent{Scope: scope, Phase: SwitchInstallError, Detail: reason})
			}

			if entry.Error != "" {
				// A plan-time resolution failure, reported at this mod's
				// position in the loop - the frontend prints it under the
				// "Installing <source>:<mod>..." line it just rendered.
				fail(entry.Error)
				continue
			}

			mod := entry.Mod
			scope.ModName = mod.Name
			fileIDs := profileApplyFileIDs(entry.Files)

			if !entry.Cached {
				downloadFailed := false
				for _, file := range entry.Files {
					progressFn := func(e Event) {
						d, ok := e.(DownloadEvent)
						if !ok || d.TotalBytes <= 0 {
							return
						}
						emit(DownloadEvent{Scope: scope, Phase: SwitchDownloading, Percent: d.Percent})
					}
					if _, err := s.downloadMod(ctx, entry.Ref.SourceID, game, mod, file, progressFn); err != nil {
						emit(ModEvent{Scope: scope, Phase: SwitchDownloadFailed, Detail: fmt.Sprintf("download failed: %v", err)})
						// Cannot use fail(): SwitchDownloadFailed above already
						// renders this mod's Error line; fail() would emit a
						// second SwitchInstallError and print a duplicate one.
						result.Failed = append(result.Failed,
							profileApplyFailure(entry, fmt.Sprintf("download failed: %v", err)))
						downloadFailed = true
						break
					}
				}
				// Fires on success AND failure: doProfileApply's own
				// unconditional Println after the download loop, which
				// terminates the carriage-returned progress line.
				emit(StepEvent{Scope: scope, Phase: SwitchDownloadDone})

				if downloadFailed {
					continue
				}
			}

			replaced := false
			if prior := entry.Replaces; prior != nil {
				if err := installer.Replace(ctx, game, &prior.Mod, mod, plan.Profile); err != nil {
					fail(fmt.Sprintf("deploy failed: %v", err))
					continue
				}
				replaced = true
			} else if err := installer.Install(ctx, game, mod, plan.Profile); err != nil {
				fail(fmt.Sprintf("deploy failed: %v", err))
				continue
			}

			// Normalize GameID to the lmm game (not the source-mapped value
			// Service.GetMod may have stamped onto mod.GameID for querying
			// the source) so every DB read, which queries by the lmm game
			// ID, can find this row again.
			installedMod := &domain.InstalledMod{
				Mod:          *mod,
				ProfileName:  plan.Profile,
				UpdatePolicy: domain.UpdateNotify,
				Enabled:      true,
				Deployed:     true, // the Install/Replace above just succeeded
				FileIDs:      fileIDs,
			}
			installedMod.GameID = game.ID // the embedded Mod's field
			if err := s.saveInstalledMod(ctx, installedMod); err != nil {
				fail(fmt.Sprintf("save failed: %v", err))
				continue
			}

			modRef := domain.ModReference{SourceID: mod.SourceID, ModID: mod.ID, Version: mod.Version, FileIDs: fileIDs}
			if err := pm.UpsertMod(game.ID, plan.Profile, modRef); err != nil {
				// #294 (Ruling 5), the Phase 3 behaviour fix ruling 9
				// deferred: a refusal here (today, only a LOCKED ref, #143)
				// leaves the profile ref unwritten while the DB row moved,
				// so it is a Warning - unconditional - not the
				// --verbose-only note doProfileApply used to swallow it
				// into.
				warn(scope, SwitchInstallWarning, fmt.Sprintf("could not update profile: %v", err))
			}

			if replaced {
				result.Replaced++
			} else {
				result.Installed++
			}
			emit(ModEvent{Scope: scope, Phase: SwitchInstalled})
		}
	}

	// #197: the sync's diagnostics are Warnings, not Notes - a failure here
	// used to be silent by default.
	if syncWarnings, syncErr := s.syncMergedPak(ctx, game, plan.Profile); syncErr != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("could not sync merged pak: %v", syncErr))
	} else {
		result.Warnings = append(result.Warnings, syncWarnings...)
	}

	return result, nil
}
