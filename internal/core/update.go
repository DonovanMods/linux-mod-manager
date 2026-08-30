// Package core: this file holds the update flow - PlanUpdate/ApplyUpdate,
// their options/plan/result types and every private helper they own - moved
// verbatim out of flows.go by v2 Phase 2 Unit I (#289), per the phase plan's
// "flows.go shrinks every unit" constraint. The move commit changed nothing
// but the file the code lives in; PlanUpdate and the lock-state additions to
// CheckGameUpdates that follow it are their own commit.
package core

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// selectUpdateDeployFiles picks the file(s) ApplyUpdate should download to
// reach targetVersion (#143). The update path's target is a version the mod
// is NOT currently on, so - unlike every other flow - its stored file IDs
// describe the version being moved AWAY from and cannot be the primary
// anchor.
//
// The bug this exists to fix: ApplyUpdate resolved files with the
// version-blind selectDeployFiles, whose stored-IDs-win rule matched the
// OLD version's file whenever the source still lists its historical file
// entries (NexusMods routinely does) and the update carried no
// FileIDReplacements chain to remap them (the norm when an author rebuilds
// and re-uploads a mod's files rather than superseding them in place). The
// "update" then re-downloaded and re-deployed the already-installed
// version, EffectiveInstalledVersion honestly stamped that same version
// back onto the row, and the next check re-found the identical update -
// forever, with no error anywhere. See
// TestApplyUpdate_OldFileStillListedUpstream_AdvancesToNewVersion.
//
// So targetVersion wins whenever the listing actually offers it. When it
// does not - a version-less file list, an empty targetVersion, or the
// routine NexusMods case where the mod-level version ("2.0") and its file's
// own version ("2.0b") simply differ (upd.NewVersion is advisory, not a
// guarantee - see the Versions capability comment in internal/source) -
// this falls through to selectDeployFiles with allowFallback=true,
// preserving #95's deliberate update-path fallback verbatim.
//
// The discriminator is per-FILE, not per-update (PR #142 review round 2).
// An earlier version switched the narrowing off whenever the update carried
// ANY FileIDReplacements mapping, which was wrong: nexusmods adds a map
// entry only for stored files that actually have a FileUpdates chain, so a
// PARTIAL map is the norm for any multi-file mod with one superseded-in-
// place file and one rebuilt file - exactly the mods most likely to hit the
// loop. Each stored ID is now classified on its own:
//
//   - a replacement HIT names a NEW file by construction (it cannot be the
//     stale anchor), so it is authoritative and always kept, whatever its
//     own label says. This matters because a superseding file routinely
//     carries its own label rather than the mod-level NewVersion - a patch
//     moving 1.3 -> 1.4 while the mod moves to 2.0.
//   - an uncovered ID still listed upstream under a DIFFERENT label than
//     installedVersion is an unchanged extra: carried over, honouring
//     ApplyUpdate's documented "a miss retains the ORIGINAL id verbatim".
//   - an uncovered ID still listed upstream under EXACTLY installedVersion
//     is label-ambiguous: it is either the stale anchor or a genuine
//     unchanged extra, and nothing upstream distinguishes them. It is
//     replaced by an unselected targetVersion file when one is available
//     (1:1) - that replacement IS the update - and otherwise RETAINED with
//     a warning naming it (see updateAmbiguousFileWarning).
//
// Round 2 ruled this ambiguity must never be silent, and considered
// dropping the unresolvable file instead of retaining it. Retaining is used
// because it is strictly safer: the ruling's rationale was avoiding a
// re-deployed stale primary (duplicate paks, and the loop), and both are
// already prevented independently - the 1:1 replacement above removes the
// stale primary, and guardNoOpUpdateSelection below proves the selection
// advances the record - whereas dropping would silently un-deploy a file
// the mod is still using, which is the round-1 regression this same review
// caught. The user is told either way; only the safer default differs.
//
// installedVersion == "" also switches narrowing off: with no recorded
// version nothing can be classified.
func selectUpdateDeployFiles(files []domain.DownloadableFile, targetVersion, installedVersion string, currentFileIDs, storedFileIDs []string, replacedIDs map[string]bool) ([]*domain.DownloadableFile, []string, error) {
	selected, warnings, err := resolveUpdateSelection(files, targetVersion, installedVersion, storedFileIDs, replacedIDs)
	if err != nil {
		return nil, nil, err
	}
	selected, err = guardNoOpUpdateSelection(files, targetVersion, installedVersion, currentFileIDs, replacedIDs, selected)
	if err != nil {
		return nil, nil, err
	}
	return selected, warnings, nil
}

// updateAmbiguousFileWarning describes a stored file selectUpdateDeployFiles
// could not classify - still listed upstream under the very version being
// updated away from, with no replacement available to take its place.
func updateAmbiguousFileWarning(f *domain.DownloadableFile, installedVersion, targetVersion string) string {
	name := f.Name
	if name == "" {
		name = f.FileName
	}
	return fmt.Sprintf("%q (file ID %s) still reports version %s - the version being updated from - so it could not be matched to anything in %s; it was kept as-is. If it is a stale entry, reinstall the mod or use --file to re-select.",
		name, f.ID, installedVersion, targetVersion)
}

// pairAmbiguousWithReplacement reports which entry of ambiguous the target-
// version file replacement stands in for: the first one sharing its Category
// (compared case-insensitively, like installFileCategoryPriority), else the
// first one sharing its IsPrimary flag, else the first entry, else -1 when
// there is nothing to pair. The candidates are label-identical by
// definition, so Category is the strongest signal when both sides carry a
// matching one (a new MAIN replaces the stale MAIN and leaves an unchanged
// OPTIONAL alone) - but it routinely decides nothing (#144): custom sources
// (directory/manifest/api) never populate Category at all, and CurseForge's
// vocabulary is release types (release/beta/alpha, from releaseTypeName)
// that need not repeat across versions, so a populated category can still
// match no ambiguous entry. IsPrimary is the secondary signal for both
// shapes: a primary replacement stands in for the stale primary (the old
// main) rather than consuming an unchanged extra by list order - and,
// symmetrically, a non-primary replacement leaves a still-primary ambiguous
// file to be retained-and-warned instead of silently displacing it. Only
// when neither signal decides does list order remain.
func pairAmbiguousWithReplacement(ambiguous []*domain.DownloadableFile, replacement *domain.DownloadableFile) int {
	if len(ambiguous) == 0 {
		return -1
	}
	if want := strings.ToUpper(replacement.Category); want != "" {
		for i, a := range ambiguous {
			if strings.ToUpper(a.Category) == want {
				return i
			}
		}
	}
	for i, a := range ambiguous {
		if a.IsPrimary == replacement.IsPrimary {
			return i
		}
	}
	return 0
}

// resolveUpdateSelection is selectUpdateDeployFiles' classification pass -
// see that function's doc comment for the per-file rules it implements.
func resolveUpdateSelection(files []domain.DownloadableFile, targetVersion, installedVersion string, storedFileIDs []string, replacedIDs map[string]bool) ([]*domain.DownloadableFile, []string, error) {
	if targetVersion == "" || installedVersion == "" || len(storedFileIDs) == 0 {
		sel, _, err := selectDeployFiles(files, storedFileIDs, true)
		return sel, nil, err
	}

	byID := make(map[string]*domain.DownloadableFile, len(files))
	var matches []*domain.DownloadableFile
	for i := range files {
		byID[files[i].ID] = &files[i]
		if files[i].Version == targetVersion {
			matches = append(matches, &files[i])
		}
	}
	if len(matches) == 0 {
		sel, _, err := selectDeployFiles(files, storedFileIDs, true)
		return sel, nil, err
	}

	var selected, ambiguous []*domain.DownloadableFile
	chosen := make(map[string]bool, len(storedFileIDs))
	needsReplacement := false
	for _, id := range storedFileIDs {
		f := byID[id]
		switch {
		case f == nil:
			needsReplacement = true // gone upstream (#95's fallback case)
		case replacedIDs[id]:
			if !chosen[id] {
				selected = append(selected, f)
				chosen[id] = true
			}
		case f.Version == installedVersion:
			ambiguous = append(ambiguous, f)
			needsReplacement = true
		default:
			if !chosen[id] {
				selected = append(selected, f)
				chosen[id] = true
			}
		}
	}

	if needsReplacement {
		var candidates []*domain.DownloadableFile
		for _, m := range matches {
			if !chosen[m.ID] {
				candidates = append(candidates, m)
			}
		}
		if len(candidates) > 0 {
			for _, p := range pickVersionMatch(candidates, nil) {
				if chosen[p.ID] {
					continue
				}
				selected = append(selected, p)
				chosen[p.ID] = true
				// This replacement stands in for ONE ambiguous file - pair
				// them by Category rather than list order (PR #142 review
				// round 3). Order alone is a DB-order coin flip: with a
				// stale MAIN and an unchanged OPTIONAL both label-ambiguous,
				// it could replace the OPTIONAL and retain the stale MAIN,
				// leaving the old main pak deployed beside the new one.
				if i := pairAmbiguousWithReplacement(ambiguous, p); i >= 0 {
					ambiguous = append(ambiguous[:i], ambiguous[i+1:]...)
				}
			}
		}
	}

	// Anything still unresolved is retained rather than lost.
	var warnings []string
	for _, a := range ambiguous {
		if !chosen[a.ID] {
			selected = append(selected, a)
			chosen[a.ID] = true
		}
		warnings = append(warnings, updateAmbiguousFileWarning(a, installedVersion, targetVersion))
	}

	if len(selected) == 0 {
		sel, _, err := selectDeployFiles(files, storedFileIDs, true)
		return sel, nil, err
	}
	return selected, warnings, nil
}

// guardNoOpUpdateSelection is the defense-in-depth backstop (PR #142 review
// round 2, corrected in round 3): whatever path produced selected, catch a
// selection that provably cannot change anything, so a mis-resolution
// surfaces instead of looping forever.
//
// The no-op test is an ID-SET comparison against what is already installed,
// NOT a version-string comparison. Round 2 used the latter and hard-failed a
// whole legitimate class: internal/source/nexusmods reports an update when
// the mod version moved OR a file was superseded, and in the second case
// (hasFileUpdate && !modVersionNewer) it sets NewVersion to the new FILE's
// own version - routinely the SAME string as the installed one, because the
// author re-uploaded a fixed archive under an unchanged label. Real new
// bytes, identical version string; "effective version == installed version"
// says nothing about it. See
// TestApplyUpdate_FileOnlyUpdate_SameVersionStringApplies.
//
// A selection containing any FileIDReplacements hit is likewise never a
// no-op: the source itself stated those files move, so this never
// second-guesses it.
//
// The repair is ADDITIVE, never wholesale (round 3): only the members
// positively identified as the version being left behind are dropped, and
// the target version's file is added alongside whatever else survives -
// round 2 replaced the entire selection, discarding replacement hits and
// carried-over extras with it.
func guardNoOpUpdateSelection(files []domain.DownloadableFile, targetVersion, installedVersion string, currentFileIDs []string, replacedIDs map[string]bool, selected []*domain.DownloadableFile) ([]*domain.DownloadableFile, error) {
	if targetVersion == "" || installedVersion == "" || len(selected) == 0 {
		return selected, nil
	}
	for _, f := range selected {
		if replacedIDs[f.ID] {
			return selected, nil
		}
	}
	if !sameFileIDSet(selected, currentFileIDs) {
		return selected, nil
	}
	var matches []*domain.DownloadableFile
	for i := range files {
		if files[i].Version == targetVersion {
			matches = append(matches, &files[i])
		}
	}
	if len(matches) == 0 || domain.EffectiveInstalledVersion(targetVersion, selected) != installedVersion {
		return selected, nil
	}

	repaired := make([]*domain.DownloadableFile, 0, len(selected)+1)
	kept := make(map[string]bool, len(selected))
	for _, f := range selected {
		if f.Version == installedVersion {
			continue
		}
		repaired = append(repaired, f)
		kept[f.ID] = true
	}
	added := false
	for _, m := range pickVersionMatch(matches, nil) {
		if kept[m.ID] {
			continue
		}
		repaired = append(repaired, m)
		kept[m.ID] = true
		added = true
	}
	// Two distinct failure shapes deserve distinct remedies (#144): when the
	// repair found nothing to add, the user ALREADY holds every file the
	// source offers under the target version, so the update-side "pick a
	// different file" remedy would be misleading - there is no other file to
	// download. That shape is a source-side labelling problem; the one
	// user action that still resolves it is a reinstall that keeps only the
	// wanted file ('lmm install --file'), which undeploys the stale one and
	// re-stamps the record from what remains. Only when something WAS added
	// (and the effective version still refuses to move) does the update-side
	// reinstall/--file remedy make sense.
	if !added {
		// Make the strong "every file already installed" claim only when it
		// is LOCALLY true (every target-version match among currentFileIDs) -
		// not because resolveUpdateSelection's candidate-consumption
		// invariant implies it. The guard exists to backstop that invariant,
		// so its diagnosis must not assume it (PR #148 Copilot round; the
		// unreachable-today shape is pinned by an in-package test).
		curSet := make(map[string]bool, len(currentFileIDs))
		for _, id := range currentFileIDs {
			curSet[id] = true
		}
		allInstalled := true
		for _, m := range matches {
			if !curSet[m.ID] {
				allInstalled = false
				break
			}
		}
		if allInstalled {
			return nil, fmt.Errorf("update to %q would re-install exactly what is already installed (file ID(s): %s): every file the source offers under %q is already installed - likely a source-side file labelling quirk; if an old file is stale, reinstall keeping only the wanted file with 'lmm install --file'", targetVersion, strings.Join(currentFileIDs, ", "), targetVersion)
		}
		return nil, fmt.Errorf("update to %q would re-install exactly what is already installed (file ID(s): %s): no file upstream advances it - reinstall the mod or use --file to pick one explicitly", targetVersion, strings.Join(currentFileIDs, ", "))
	}
	if domain.EffectiveInstalledVersion(targetVersion, repaired) == installedVersion {
		return nil, fmt.Errorf("update to %q would re-install exactly what is already installed (file ID(s): %s): no file upstream advances it - reinstall the mod or use --file to pick one explicitly", targetVersion, strings.Join(currentFileIDs, ", "))
	}
	return repaired, nil
}

// --- PlanUpdate (v2 Phase 2 Unit I, #289) ---

// UpdatePlan is the pure, displayable result of PlanUpdate: everything the
// pre-extraction CLI's applySingleUpdate (cmd/lmm/update.go) computed inline
// before deciding which of its four branches to render and, for the
// version-bump branch, whether to apply. See PlanUpdate's doc comment for
// the exact mapping.
type UpdatePlan struct {
	// Mod is the installed mod PlanUpdate was asked about, freshly re-read
	// via GetInstalledMod - its GameID/ProfileName double as the plan's
	// implicit "which profile" identity (mirroring InstallPlan's own
	// GameID/Profile fields, just carried on the embedded domain.Mod/
	// InstalledMod instead of duplicated).
	Mod domain.InstalledMod `json:"mod"`
	// Locked/LockedVersion mirror the profile ref's lock state, read once -
	// same semantics as domain.Update.Locked/LockedVersion (CheckGameUpdates
	// already stamps these on Update when one exists, but PlanUpdate also
	// needs them when there is no update at all, e.g. the pinned branch's
	// "(also locked)" caveat).
	Locked        bool   `json:"locked"`
	LockedVersion string `json:"locked_version,omitempty"`
	// Pinned reports Mod.UpdatePolicy == domain.UpdatePinned.
	Pinned bool `json:"pinned"`
	// Update is the result of checking (Mod.SourceID, Mod.ID) for an update -
	// nil means CheckGameUpdates found nothing for this mod (up to date, or
	// pinned/filtered before the source was ever queried; a DeployCompile
	// game's merged-pak staleness check can still populate this even for a
	// pinned mod - see CheckGameUpdates - matching applySingleUpdate's own
	// pre-lift precedence exactly: the zero-updates check always ran first).
	Update *domain.Update `json:"update,omitempty"`
	// RecompileNeeded mirrors Update.RecompileNeeded when Update != nil,
	// false otherwise - a convenience so a renderer never needs to nil-check
	// Update just to read this one bit.
	RecompileNeeded bool `json:"recompile_needed"`
	// Changelog is CleanChangelog(Update.Changelog) - empty when Update is
	// nil or carries no changelog.
	Changelog string `json:"changelog,omitempty"`
	// Refusal is LockedRefUnlockOnlyRefusalError's SENTENCE half - the
	// refusal without the ErrModLocked sentinel prefix, exactly as
	// RelinkPlan.Refusal carries it - precomputed whenever Locked &&
	// Update != nil. Since #294 (Ruling 5) cmd/lmm's renderer PRINTS this
	// verbatim for both locked branches (an available update and a needed
	// recompile), in place of the two hand-worded lines it used to
	// compose - one wording for every lock refusal of this KIND in the
	// product. The sentence half, because a verbatim print of the wrapped
	// error stuttered: "mod is locked: Mod One is locked at v1.0 ..."
	// (unit Q review, M1). ApplyUpdate's own error keeps the sentinel, so
	// errors.Is(err, ErrModLocked) is unaffected. The unlock-only variant,
	// because ApplyUpdate refuses on the lock alone regardless of version:
	// moving the lock to the target version leaves it refusing, so naming
	// that remedy would be false guidance (unit Q review, I1).
	Refusal string `json:"refusal,omitempty"`
	// snapshot is the installed-mod set this plan was computed against
	// (Ruling 5): ApplyUpdate re-derives it under beginOp and returns
	// ErrStalePlan when it no longer matches. Unexported and outside the
	// wire contract, mirroring InstallPlan.snapshot exactly.
	snapshot installedSnapshot `json:"-"`
}

// PlanUpdate computes what "lmm update <mod-id>" would do for (sourceID,
// modID) in profileName - the pure, read-only half of the pre-extraction
// CLI's applySingleUpdate. See UpdatePlan's doc comment for what each field
// means, and the task report for the exact mapping back to
// applySingleUpdate's four branches.
//
// Network reads (CheckGameUpdates, which delegates to the registered
// source's CheckUpdates) are expected; no DB write, filesystem write, hook
// execution, or download ever happens here.
//
// This is CheckGameUpdates-for-one-mod followed by PlanUpdateFrom, so there
// is exactly one place ("no update" aside) that turns a domain.Update into a
// plan - see PlanUpdateFrom's doc comment for why a caller that already has
// an Update (applyBulkUpdate) should call that instead of this.
func (s *Service) PlanUpdate(ctx context.Context, game *domain.Game, profileName, sourceID, modID string) (*UpdatePlan, error) {
	mod, err := s.GetInstalledMod(ctx, sourceID, modID, game.ID, profileName)
	if err != nil {
		return nil, err
	}

	updates, err := s.CheckGameUpdates(ctx, game, profileName, []domain.InstalledMod{*mod}, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to check update: %w", err)
	}
	if len(updates) == 0 {
		return s.planUpdateBase(ctx, game, profileName, mod)
	}

	return s.planUpdateFrom(ctx, game, profileName, mod, updates[0])
}

// PlanUpdateFrom builds the plan for upd - the same UpdatePlan PlanUpdate
// would return for upd.InstalledMod, computed WITHOUT re-invoking
// CheckGameUpdates (#289 review, Important 1). Every field PlanUpdate would
// have derived from a fresh CheckGameUpdates call (Update itself,
// RecompileNeeded, Changelog) is instead read straight off upd - the exact
// value the caller already found and, in applyBulkUpdate's case, already
// printed to the user. Only local reads happen here: the installed-mod row
// (to notice a version/enabled change since upd was found), the profile's
// lock state, and a fresh installedSnapshot for Ruling 5 - no source is ever
// queried, so a bulk apply of N mods never costs more than the batch check
// that already found them, and the plan being applied can never disagree
// with the update the caller printed.
func (s *Service) PlanUpdateFrom(ctx context.Context, game *domain.Game, profileName string, upd domain.Update) (*UpdatePlan, error) {
	mod, err := s.GetInstalledMod(ctx, upd.InstalledMod.SourceID, upd.InstalledMod.ID, game.ID, profileName)
	if err != nil {
		return nil, err
	}
	return s.planUpdateFrom(ctx, game, profileName, mod, upd)
}

// planUpdateBase builds the fields every UpdatePlan carries regardless of
// whether an Update was found: the Ruling-5 snapshot, Pinned, and
// Locked/LockedVersion (via lockState). PlanUpdate's "no update" branches
// (up to date, pinned) return this directly; planUpdateFrom layers the
// Update-derived fields on top of it.
func (s *Service) planUpdateBase(ctx context.Context, game *domain.Game, profileName string, mod *domain.InstalledMod) (*UpdatePlan, error) {
	// Ruling 5: record the installed set this plan is being computed
	// against, so ApplyUpdate can refuse it once that set has moved on.
	snapshot, err := s.currentInstalledSnapshot(ctx, game.ID, profileName)
	if err != nil {
		return nil, err
	}

	plan := &UpdatePlan{
		Mod:      *mod,
		Pinned:   mod.UpdatePolicy == domain.UpdatePinned,
		snapshot: snapshot,
	}

	// #97: mirrors applySingleUpdate's own pre-lift profile load - a
	// missing/unreadable profile is treated as unlocked (a lock cannot exist
	// in an unloadable profile).
	locked, lockedVersion, err := s.lockState(ctx, game.ID, profileName, mod.SourceID, mod.ID)
	if err != nil {
		return nil, err
	}
	if locked {
		plan.Locked = true
		plan.LockedVersion = lockedVersion
	}

	return plan, nil
}

// planUpdateFrom layers upd's fields onto planUpdateBase's result - the
// shared tail of PlanUpdate (once it has a domain.Update in hand) and the
// exported PlanUpdateFrom.
func (s *Service) planUpdateFrom(ctx context.Context, game *domain.Game, profileName string, mod *domain.InstalledMod, upd domain.Update) (*UpdatePlan, error) {
	plan, err := s.planUpdateBase(ctx, game, profileName, mod)
	if err != nil {
		return nil, err
	}

	updCopy := upd
	plan.Update = &updCopy
	plan.RecompileNeeded = upd.RecompileNeeded
	plan.Changelog = CleanChangelog(upd.Changelog)
	if plan.Locked {
		ref := &domain.ModReference{Version: plan.LockedVersion}
		plan.Refusal = lockedRefUnlockOnlyMessage(mod.Mod, profileName, ref)
	}

	return plan, nil
}

// --- ApplyUpdate (Phase 5b Task 3) ---

// UpdateOptions configures ApplyUpdate. Unlike InstallOptions/DeployOptions,
// there is no before_all/after_all hook plumbing at all - applyUpdate never
// ran that pair (see ApplyUpdate's doc comment) - so Force here gates ONLY
// the two before_each hooks (uninstall.before_each for the old version,
// install.before_each for the new one), matching applyUpdate's own
// near-identical Force checks exactly.
type UpdateOptions struct {
	// Hook plumbing, mirroring UninstallOptions/DeployOptions/InstallOptions:
	// ApplyUpdate resolves the game/profile hooks and a HookRunner itself.
	// Force: continue past a failing uninstall.before_each/install.before_each
	// hook (warn instead of fail), matching applyUpdate's own --force gate.
	Force bool
	// SkipHooks: run no hooks even when hooks are configured (the CLI's --no-hooks).
	SkipHooks bool
}

// UpdateStatus classifies the outcome an UpdateApplyResult reports - the
// core-side counterpart of cmd/lmm/update.go's singleUpdateJSON.Status
// strings (v2 Phase 3 Task 2, #301), so a future --json switch (Unit O) can
// emit UpdateApplyResult directly instead of the CLI's own hand-built
// document. The zero value (UpdateUpdated) is never observed on a result
// whose Name is still empty - see UpdateApplyResult's doc comment.
type UpdateStatus int

// The UpdateStatus values classify what an update operation did or would
// do: UpdateUpdated is a completed normal version-bump update,
// UpdateUpToDate found nothing newer, UpdateSkipped covers a locked/refused
// mod (Reason names why), UpdateRecompiled/UpdateRecompileAvailable are the
// applied/dry-run outcomes of a base-pak recompile (#196 - no version
// change, only the compiled artifact is rebuilt), UpdateAvailable is a
// normal update's `--dry-run` outcome, and UpdateRolledBack marks a
// completed ApplyRollback.
const (
	UpdateUpdated UpdateStatus = iota
	UpdateUpToDate
	UpdateSkipped
	UpdateRecompiled
	UpdateRecompileAvailable
	UpdateAvailable
	UpdateRolledBack
)

// updateStatusNames maps each UpdateStatus to its wire name. Keep in
// declaration order.
var updateStatusNames = [...]string{
	UpdateUpdated:            "updated",
	UpdateUpToDate:           "up_to_date",
	UpdateSkipped:            "skipped",
	UpdateRecompiled:         "recompiled",
	UpdateRecompileAvailable: "recompile_available",
	UpdateAvailable:          "available",
	UpdateRolledBack:         "rolled_back",
}

// String returns the status's wire name.
func (s UpdateStatus) String() string {
	if s >= 0 && int(s) < len(updateStatusNames) && updateStatusNames[s] != "" {
		return updateStatusNames[s]
	}
	return fmt.Sprintf("update_status(%d)", int(s))
}

// MarshalText implements encoding.TextMarshaler.
func (s UpdateStatus) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler.
func (s *UpdateStatus) UnmarshalText(b []byte) error {
	for i, n := range updateStatusNames {
		if n == string(b) {
			*s = UpdateStatus(i)
			return nil
		}
	}
	return fmt.Errorf("unknown update status %q", b)
}

// UpdateApplyResult reports the outcome of ApplyUpdate. As with
// DeployResult/UninstallResult/SwitchResult/InstallResult, every entry below
// is always recorded - there is no verbosity concept in core.
//
//   - Mod/Name/FromVersion/ToVersion/Changelog/Status identify what was
//     applied - populated together, atomically, once the WHOLE sequence -
//     download, hooks, Replace, and all three DB/profile writes - has
//     succeeded (v2 Phase 3 Task 2, #301: structured replacement for the
//     pre-extraction CLI's single "<name> <old version> → <new version>"
//     joined string). Name stays empty on any failure; ApplyUpdate applies
//     exactly one domain.Update per call (the CLI's own update loop calls
//     it once per mod), so a caller checking "did anything apply" should
//     check Name, not Status (whose zero value, UpdateUpdated, is itself a
//     valid status).
//   - Warnings holds diagnostics applyUpdate printed unconditionally:
//     uninstall.before_each/install.before_each (when forced), and
//     uninstall.after_each/install.after_each hook failures (always
//     non-fatal). Callers should print each entry to stderr,
//     unconditionally, e.g. `fmt.Fprintf(os.Stderr, "Warning: %v\n", w)`.
//   - Notes holds the sole diagnostic applyUpdate only printed under
//     --verbose: a failed SetModLinkMethod, with the historical "Warning: "
//     prefix baked into the text already (matching applyUpdate's exact
//     wording); a caller wanting byte-identical output should print it to
//     stdout ONLY under --verbose, e.g. `fmt.Printf("  %s\n", n)`.
//
// Every entry in both slices is ALSO reported via the event stream at
// the exact point it is appended (UpdateBeforeEachForced/UpdateWarning/
// UpdateNote - see each DeployPhase constant's doc comment), with Detail
// equal to the slice entry verbatim.
//
// On error, the returned result carries any diagnostics accumulated before
// the failure; callers should surface them alongside the error.
type UpdateApplyResult struct {
	Mod         domain.ModReference `json:"mod"`
	Name        string              `json:"name"`
	FromVersion string              `json:"from_version"`
	ToVersion   string              `json:"to_version"`
	Changelog   string              `json:"changelog,omitempty"`
	Status      UpdateStatus        `json:"status"`
	Reason      string              `json:"reason,omitempty"`
	Warnings    []string            `json:"warnings,omitempty"`
	Notes       []string            `json:"notes,omitempty"`
}

// ErrModLocked reports an update apply refused because the profile ref is
// locked (#97). Callers branch with errors.Is.
var ErrModLocked = errors.New("mod is locked")

// LockedRefRefusalError builds the ErrModLocked-wrapping refusal a lock
// gate returns when mod's profile ref is locked AND the gate would only have
// proceeded at a different version - install gating (lockedInstallRefusal /
// applyInstallBatchMod) and ApplyRelinkMod's metadata-only --version guard -
// factored into one function specifically so the call sites can never drift
// apart in wording. A gate that refuses on the lock alone takes
// LockedRefUnlockOnlyRefusalError instead (exported since #146 so cmd/lmm
// can reuse the exact same refusal instead of hand-copying it;
// PR #142 Copilot round-4: the prior hand-duplicated
// version named no source/profile in its remedies, so a user running the
// refused operation against a non-active profile, or a mod ID that exists
// under more than one source, would copy-paste a remedy that resolved
// against the wrong target - the active profile / an ambiguous source -
// the same "copy-paste acts on the wrong target" class already fixed for
// verify's sibling-repair warning). Both remedies now carry the mod's
// actual source (-s) and the profile actually holding the lock (-p) -
// modCmd's real, registered flags (cmd/lmm/mod.go: `modCmd.PersistentFlags
// ().StringVarP(&modSource, "source", "s", ...)` /
// `StringVarP(&modProfile, "profile", "p", ...)`), so a copy-pasted remedy
// always resolves against the SAME ref this error is actually about,
// regardless of which profile/source the caller had active.
func LockedRefRefusalError(mod domain.Mod, profileName string, ref *domain.ModReference) error {
	return fmt.Errorf("%w: %s", ErrModLocked, lockedRefRefusalMessage(mod, profileName, ref))
}

// LockedRefUnlockOnlyRefusalError is LockedRefRefusalError's sibling for the
// gates that refuse on ref.Locked ALONE, regardless of version: ApplyUpdate,
// ApplyRollback and ApplyRelinkMod's re-link branch (#146). Moving the lock
// to the target version leaves all three still refusing, so their refusal
// offers only the remedy that works - "unlock with '...' first" - and never
// names 'lmm mod lock' at all (unit Q review, I1: the unified wording named a
// remedy the gate does not honour, which is worse guidance than the
// hand-worded sentences Ruling 5 replaced).
//
// Both constructors go through lockedRefSentence, so Ruling 5's actual
// rationale still holds: one wording per refusal KIND, and the call sites of
// a kind can never drift apart. Use this one when the gate ignores the
// version; use LockedRefRefusalError when the gate compares versions
// (lockedInstallRefusal, applyInstallBatchMod, ApplyRelinkMod's
// metadata-only --version guard), where moving the lock genuinely unblocks
// the operation.
func LockedRefUnlockOnlyRefusalError(mod domain.Mod, profileName string, ref *domain.ModReference) error {
	return fmt.Errorf("%w: %s", ErrModLocked, lockedRefUnlockOnlyMessage(mod, profileName, ref))
}

// lockedRefRefusalMessage is LockedRefRefusalError's human-readable half:
// the refusal sentence WITHOUT the ErrModLocked sentinel prefix. Split out
// (#288) because the BATCH install engine's InstallLockRefusal event carries
// the sentence alone - `lmm install <query>`'s multi-select path has always
// printed it unwrapped, while the dependency path prints the wrapped error -
// and splitting it here is what keeps the two from drifting apart in wording
// the way the four hand-copied refusals LockedRefRefusalError replaced did.
func lockedRefRefusalMessage(mod domain.Mod, profileName string, ref *domain.ModReference) string {
	return lockedRefSentence(mod, profileName, ref, lockedRefRemedyMoveOrUnlock)
}

// lockedRefUnlockOnlyMessage is LockedRefUnlockOnlyRefusalError's sentence
// half, on the same terms as lockedRefRefusalMessage. UpdatePlan.Refusal,
// RollbackPlan.Refusal and RelinkPlan.Refusal all carry THIS - the sentence
// without the sentinel - so cmd/lmm prints plan data verbatim without a
// "mod is locked: mod is locked:" stutter (unit Q review, M1).
func lockedRefUnlockOnlyMessage(mod domain.Mod, profileName string, ref *domain.ModReference) string {
	return lockedRefSentence(mod, profileName, ref, lockedRefRemedyUnlockOnly)
}

// lockedRefRemedy selects which remedy clause lockedRefSentence appends.
type lockedRefRemedy int

const (
	// lockedRefRemedyMoveOrUnlock offers both 'lmm mod lock <version>' and
	// 'lmm mod unlock' - correct only where the gate compares the ref's
	// locked version against a target version.
	lockedRefRemedyMoveOrUnlock lockedRefRemedy = iota
	// lockedRefRemedyUnlockOnly offers only 'lmm mod unlock' - for gates
	// that refuse on the lock alone, where moving it changes nothing.
	lockedRefRemedyUnlockOnly
)

// lockedRefSentence is the ONE builder behind every canonical lock refusal:
// a shared "<name> is locked at v<version> in profile <profile>" head plus
// exactly one of two remedy clauses. Both clauses carry the mod's actual
// source (-s) and the profile actually holding the lock (-p), so a
// copy-pasted remedy always resolves against the SAME ref the refusal is
// about (PR #142 Copilot round-4).
func lockedRefSentence(mod domain.Mod, profileName string, ref *domain.ModReference, remedy lockedRefRemedy) string {
	unlock := fmt.Sprintf("unlock with 'lmm mod unlock -s %s -p %s %s'", mod.SourceID, profileName, mod.ID)
	clause := unlock + " first"
	if remedy == lockedRefRemedyMoveOrUnlock {
		clause = fmt.Sprintf("move the lock with 'lmm mod lock -s %s -p %s %s <version>' or %s",
			mod.SourceID, profileName, mod.ID, unlock)
	}
	return fmt.Sprintf("%s is locked at v%s in profile %s - %s", mod.Name, ref.Version, profileName, clause)
}

// ApplyUpdate applies upd to the installed mod it references
// (upd.InstalledMod), following cmd/lmm/update.go's pre-extraction
// applyUpdate ordering exactly: GetMod (the new version) -> GetModFiles ->
// resolve FileIDReplacements -> download -> hooks ->
// installer.ReplaceForUpdate (Replace at extraction time; it has since
// gained the file-ID transition for #144 item 4's same-version shape) ->
// ApplyModUpdate -> SetModLinkMethod -> UpsertMod. This is a
// behavior-preserving extraction - see the task report for the full mapping.
//
// FileIDReplacements resolution mirrors applyUpdate exactly: each of the
// installed mod's own FileIDs is looked up in upd.FileIDReplacements; a hit
// substitutes the new (superseding) file ID, a miss retains the ORIGINAL id
// verbatim (never silently dropped). Those IDs are then handed to
// selectUpdateDeployFiles, for which they are only a tie-break WITHIN
// upd.NewVersion's own files - see that function's doc comment (#143) for
// why the update path, alone among the flows, cannot let stored IDs
// outrank the target version, and for when its selectDeployFiles
// primary-file fallback (#95) still applies.
//
// A download failure returns immediately - before any hook runs, before
// Replace, before any DB/profile write - so the old version is left
// deployed and every row untouched, matching applyUpdate's own bare early
// return. Installer.Replace never touches the cache (only the game
// directory and deployed-file tracking - see installer.go), so the OLD
// version's cache entry always survives an update; ApplyModUpdate records
// PreviousVersion/PreviousFileIDs before overwriting version/FileIDs - both
// preconditions `lmm update rollback` (doUpdateRollback, NOT extracted by
// this task - see the task report) depends on.
//
// Hook failure semantics mirror applyUpdate's own two, independently
// Force-gated before_each hooks (uninstall.before_each for the OLD mod,
// install.before_each for the NEW mod: fatal unless Force is set, in which
// case a Warning is recorded and the update proceeds) and its two always-
// non-fatal after_each hooks (uninstall.after_each, install.after_each -
// both recorded as Warnings regardless of Force, printed immediately after
// Replace, well before the DB/profile writes below - see UpdateWarning's
// doc comment).
//
// A failure to write ApplyModUpdate or UpsertMod triggers the same
// best-effort compensating actions applyUpdate itself performed (a reverse
// Installer.ReplaceForUpdate - the file-ID transition reversed - to restore
// the old deployment, plus - for UpsertMod - a RollbackModVersion to undo
// the DB version swap first); a failure to write
// SetModLinkMethod is NOT rolled back, matching applyUpdate exactly (it only
// ever produced a --verbose-gated Note).
//
// sink may be nil. On error, the returned result carries any
// diagnostics accumulated before the failure - callers should surface them
// alongside the error (see UpdateApplyResult's doc comment).
func (s *Service) ApplyUpdate(ctx context.Context, game *domain.Game, plan *UpdatePlan, opts UpdateOptions, sink EventSink) (*UpdateApplyResult, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return &UpdateApplyResult{}, err
	}
	defer release()
	return s.applyUpdate(ctx, game, plan, opts, sink)
}

func (s *Service) applyUpdate(ctx context.Context, game *domain.Game, plan *UpdatePlan, opts UpdateOptions, sink EventSink) (*UpdateApplyResult, error) {
	result := &UpdateApplyResult{}
	emit := func(e Event) {
		if sink != nil {
			sink(e)
		}
	}

	// Ruling 5: the plan is a contract about a world that may have moved.
	// First statement inside the op (ApplyUpdate took beginOp just above),
	// before any lock check, hook, or side effect - a stale plan is refused
	// having changed nothing at all, mirroring applyInstall's own placement.
	if err := s.checkPlanFresh(ctx, plan.Mod.GameID, plan.Mod.ProfileName, plan.snapshot); err != nil {
		return result, err
	}
	if plan.Update == nil {
		return result, fmt.Errorf("update plan has no update to apply")
	}
	profileName := plan.Mod.ProfileName
	upd := *plan.Update

	// #286 review (Important 1): resolved before the download loop below,
	// applyUpdate's first mutation - mirroring every other flow
	// (uninstallMod/deployProfile/purgeProfile/applyInstall/applyRollback
	// all resolve hooks before their own first mutation).
	hooks, err := s.resolvedHooks(ctx, game, profileName)
	if err != nil {
		return result, err
	}
	runner, err := s.hookRunner(ctx)
	if err != nil {
		return result, err
	}
	hookCtx := hookContextFor(game)

	mod := upd.InstalledMod // local, addressable copy - distinct from upd.InstalledMod
	newVersion := upd.NewVersion
	scope := Scope{Op: OpUpdate, ModName: mod.Name, Mod: &domain.ModReference{SourceID: mod.SourceID, ModID: mod.ID}}

	// #97: a locked ref refuses update-apply entirely - the lock's whole
	// contract. Checked before any network or hook side effect.
	if prof, err := s.NewProfileManager().Get(ctx, game.ID, profileName); err == nil {
		if ref := prof.FindRef(mod.SourceID, mod.ID); ref != nil && ref.Locked {
			return result, LockedRefUnlockOnlyRefusalError(mod.Mod, profileName, ref)
		}
	} else if cerr := ctx.Err(); cerr != nil {
		// Ruling 16 (C): the fall-through below is for a profile that
		// cannot hold a lock; a cancelled read is a profile we never got to
		// ask, and letting it through would update a locked mod.
		return result, cerr
	}
	// (A missing/unreadable profile falls through - matches
	// PlanProfileSwitch's ignore-errors precedent for profile loads: a lock
	// cannot exist in an unloadable profile.)

	newMod, err := s.GetMod(ctx, mod.SourceID, game.ID, mod.ID)
	if err != nil {
		return result, fmt.Errorf("fetching new version: %w", err)
	}

	files, err := s.GetModFiles(ctx, mod.SourceID, newMod)
	if err != nil {
		return result, fmt.Errorf("getting mod files: %w", err)
	}
	if len(files) == 0 {
		return result, fmt.Errorf("no downloadable files available")
	}

	// replacedIDs records which of the resulting IDs came from an actual
	// FileIDReplacements HIT, so selectUpdateDeployFiles can treat those as
	// authoritative per-file rather than inferring anything from the map's
	// mere presence (a partial map is the norm - see its doc comment).
	effectiveFileIDs := mod.FileIDs
	var replacedIDs map[string]bool
	if len(upd.FileIDReplacements) > 0 {
		effectiveFileIDs = make([]string, len(mod.FileIDs))
		replacedIDs = make(map[string]bool, len(upd.FileIDReplacements))
		for i, fid := range mod.FileIDs {
			if newID, ok := upd.FileIDReplacements[fid]; ok {
				effectiveFileIDs[i] = newID
				replacedIDs[newID] = true
			} else {
				effectiveFileIDs[i] = fid
			}
		}
	}
	filesToDownload, selectionWarnings, err := selectUpdateDeployFiles(files, newVersion, mod.Version, mod.FileIDs, effectiveFileIDs, replacedIDs)
	if err != nil {
		return result, fmt.Errorf("selecting files to download: %w", err)
	}
	for _, w := range selectionWarnings {
		result.Warnings = append(result.Warnings, w)
		emit(WarningEvent{Scope: scope, Phase: UpdateWarning, Message: w})
	}

	// #96/#94: record what is actually being installed, not the mod-level
	// NewVersion - update-apply was the last recording flow stamping the
	// mod-level string verbatim, which made verify's version-record check
	// flag freshly-updated mods whose file version differs from the mod
	// version. effectiveVersion keys the cache (via newMod.Version), the DB
	// row, and the profile ref below, matching every install flow.
	effectiveVersion := domain.EffectiveInstalledVersion(newVersion, filesToDownload)
	newMod.Version = effectiveVersion

	var downloadedFileIDs []string
	for _, file := range filesToDownload {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		progressFn := func(e Event) {
			d, ok := e.(DownloadEvent)
			if !ok || d.TotalBytes <= 0 {
				return
			}
			emit(DownloadEvent{Scope: scope, Phase: UpdateDownloading, Percent: d.Percent})
		}
		if _, err := s.downloadMod(ctx, mod.SourceID, game, newMod, file, progressFn); err != nil {
			return result, fmt.Errorf("downloading update: %w", err)
		}
		downloadedFileIDs = append(downloadedFileIDs, file.ID)
	}
	emit(StepEvent{Scope: scope, Phase: UpdateDownloadDone})

	// Task 6 item d (cancel-then-drain): checked between the download step
	// above and the hook/deploy (Replace) steps below, at minimum - a
	// cancelled ctx aborts here, before running any before_each hook or
	// touching the deployed files, leaving the OLD version fully deployed
	// and untouched (the partial-result convention - see this function's
	// doc comment).
	if err := ctx.Err(); err != nil {
		return result, err
	}

	hookCtx.ModID, hookCtx.ModName, hookCtx.ModVersion = mod.ID, mod.Name, mod.Version
	if err := runHook(ctx, opts.SkipHooks, runner, &hookCtx, "uninstall.before_each", hooks.GetUninstallBeforeEach()); err != nil {
		if !opts.Force {
			return result, fmt.Errorf("uninstall.before_each hook failed: %w", err)
		}
		msg := fmt.Sprintf("uninstall.before_each hook failed (forced): %v", err)
		result.Warnings = append(result.Warnings, msg)
		emit(HookEvent{Scope: scope, Phase: UpdateBeforeEachForced, Stage: "uninstall.before_each", Detail: msg})
	}

	linkMethod, err := s.GetEffectiveLinkMethod(ctx, game, profileName)
	if err != nil {
		return result, err
	}
	installer := s.newInstallerWithLinker(game, s.getLinker(linkMethod))

	hookCtx.ModID, hookCtx.ModName, hookCtx.ModVersion = newMod.ID, newMod.Name, newMod.Version
	if err := runHook(ctx, opts.SkipHooks, runner, &hookCtx, "install.before_each", hooks.GetInstallBeforeEach()); err != nil {
		if !opts.Force {
			return result, fmt.Errorf("install.before_each hook failed: %w", err)
		}
		msg := fmt.Sprintf("install.before_each hook failed (forced): %v", err)
		result.Warnings = append(result.Warnings, msg)
		emit(HookEvent{Scope: scope, Phase: UpdateBeforeEachForced, Stage: "install.before_each", Detail: msg})
	}

	// #144 item 4: the mod's file-ID transition (installed set -> downloaded
	// set) rides along so the degenerate same-version shape - old and new
	// files sharing ONE version-keyed cache dir, where the plain union
	// replace could never undeploy a superseded file's members - can narrow
	// the deploy set to what the new IDs actually own. The compensation
	// paths below pass the transition REVERSED: an update that failed to
	// commit must restore the old IDs' members and remove the uncommitted
	// new file's sole members, not leave both deployed. See
	// Installer.ReplaceForUpdate / resolveSharedDirUpdate.
	if err := installer.ReplaceForUpdate(ctx, game, &mod.Mod, newMod, profileName, mod.FileIDs, downloadedFileIDs); err != nil {
		return result, fmt.Errorf("deploying update: %w", err)
	}

	hookCtx.ModID, hookCtx.ModName, hookCtx.ModVersion = mod.ID, mod.Name, mod.Version
	if err := runHook(ctx, opts.SkipHooks, runner, &hookCtx, "uninstall.after_each", hooks.GetUninstallAfterEach()); err != nil {
		msg := fmt.Sprintf("uninstall.after_each hook failed: %v", err)
		result.Warnings = append(result.Warnings, msg)
		emit(WarningEvent{Scope: scope, Phase: UpdateWarning, Message: msg})
	}
	hookCtx.ModID, hookCtx.ModName, hookCtx.ModVersion = newMod.ID, newMod.Name, newMod.Version
	if err := runHook(ctx, opts.SkipHooks, runner, &hookCtx, "install.after_each", hooks.GetInstallAfterEach()); err != nil {
		msg := fmt.Sprintf("install.after_each hook failed: %v", err)
		result.Warnings = append(result.Warnings, msg)
		emit(WarningEvent{Scope: scope, Phase: UpdateWarning, Message: msg})
	}

	if err := s.applyModUpdate(ctx, mod.SourceID, mod.ID, game.ID, profileName, effectiveVersion, downloadedFileIDs); err != nil {
		// recovery must not inherit the caller's cancellation (v2 Phase 1 Task 3 C1 class)
		if rerr := installer.ReplaceForUpdate(context.WithoutCancel(ctx), game, newMod, &mod.Mod, profileName, downloadedFileIDs, mod.FileIDs); rerr != nil {
			s.logger().Warn("rollback after failed install also failed", "step", "replace_for_update", "err", rerr)
		}
		return result, fmt.Errorf("updating database: %w", err)
	}

	if err := s.setModLinkMethod(ctx, mod.SourceID, mod.ID, game.ID, profileName, linkMethod); err != nil {
		msg := fmt.Sprintf("Warning: could not update link method: %v", err)
		result.Notes = append(result.Notes, msg)
		emit(StepEvent{Scope: scope, Phase: UpdateNote, Detail: msg})
	}

	pm := s.NewProfileManager()
	modRef := domain.ModReference{SourceID: mod.SourceID, ModID: mod.ID, Version: effectiveVersion, FileIDs: downloadedFileIDs}
	if err := pm.UpsertMod(ctx, game.ID, profileName, modRef); err != nil {
		// recovery must not inherit the caller's cancellation (v2 Phase 1 Task 3 C1 class)
		rctx := context.WithoutCancel(ctx)
		if rerr := s.rollbackModVersion(rctx, mod.SourceID, mod.ID, game.ID, profileName); rerr != nil {
			s.logger().Warn("rollback after failed install also failed", "step", "rollback_mod_version", "err", rerr)
		}
		if rerr := installer.ReplaceForUpdate(rctx, game, newMod, &mod.Mod, profileName, downloadedFileIDs, mod.FileIDs); rerr != nil {
			s.logger().Warn("rollback after failed install also failed", "step", "replace_for_update", "err", rerr)
		}
		return result, fmt.Errorf("updating profile: %w", err)
	}

	result.Mod = modRef
	result.Name = mod.Name
	result.FromVersion = mod.Version
	result.ToVersion = effectiveVersion
	result.Changelog = upd.Changelog
	result.Status = UpdateUpdated

	// #197 postsmoke fix: also emit UpdateWarning - appending to
	// result.Warnings alone is not loud enough, since applyUpdate
	// (cmd/lmm/update.go) discards ApplyUpdate's result entirely
	// (`_, err := ...`) and drives its console output purely from live
	// progress events, exactly the plumbing gap the ApplyInstall fix
	// closed for install.
	if syncWarnings, syncErr := s.syncMergedPak(ctx, game, profileName); syncErr != nil {
		msg := fmt.Sprintf("syncing merged pak: %v", syncErr)
		result.Warnings = append(result.Warnings, msg)
		emit(WarningEvent{Scope: Scope{Op: OpUpdate}, Phase: UpdateWarning, Message: msg})
	} else {
		for _, w := range syncWarnings {
			result.Warnings = append(result.Warnings, w)
			emit(WarningEvent{Scope: Scope{Op: OpUpdate}, Phase: UpdateWarning, Message: w})
		}
	}

	return result, nil
}
