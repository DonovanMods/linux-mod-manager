package core

import (
	"fmt"
	"slices"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// recordsPass reports what the verified profile's records claim that
// nothing backs (#469):
//
//   - orphaned_record: deployed_files rows of a mod the profile has no
//     installed row for. No purge and no apply looks at them - both work
//     from installed rows - so on the active profile they hold the mod_path
//     refusal with nothing the user can run. --fix drops them there and
//     removes nothing on disk: without an installed row lmm cannot say what
//     the file is. On another profile a recorded-only purge judges them
//     (`lmm purge --profile`), which the row names.
//   - missing_cache: an installed mod with no file record whose cache entry
//     is gone (#469's D2) - the per-file walk checks only mods with a file
//     record, so nothing noticed. It is an issue with no automatic repair:
//     there is no recorded file to download again. A mod that may never
//     have had an entry (adoptedInPlace) is not one.
func (r *verifyRun) recordsPass(installedMods []domain.InstalledMod, files []DeployedFile) error {
	rows, err := r.svc.db.ListDeployedFiles(r.ctx, r.game.ID, r.profile)
	if err != nil {
		return fmt.Errorf("listing deployed files: %w", err)
	}
	installed := make(map[string]bool, len(installedMods))
	for _, m := range installedMods {
		installed[domain.ModKey(m.SourceID, m.ID)] = true
	}
	orphans := make(map[string][]string)
	var order []string
	for _, row := range rows {
		key := domain.ModKey(row.SourceID, row.ModID)
		// The merged artifact's records have no row by design: it belongs
		// to the profile, not to a mod (merged_pak.go).
		if installed[key] || row.SourceID == domain.SourceMerged || (r.opts.ModFilter != "" && row.ModID != r.opts.ModFilter) {
			continue
		}
		if _, seen := orphans[key]; !seen {
			order = append(order, key)
		}
		orphans[key] = append(orphans[key], row.RelativePath)
	}
	if len(order) > 0 {
		if err := r.orphanedRecordFindings(order, orphans); err != nil {
			return err
		}
	}

	withFiles := make(map[string]bool, len(files))
	for _, f := range files {
		withFiles[domain.ModKey(f.SourceID, f.ModID)] = true
	}
	gameCache := r.svc.GetGameCache(r.game)
	for _, mod := range installedMods {
		if err := r.ctx.Err(); err != nil {
			return err
		}
		if mod.External || adoptedInPlace(r.game, mod) || withFiles[domain.ModKey(mod.SourceID, mod.ID)] ||
			(r.opts.ModFilter != "" && mod.ID != r.opts.ModFilter) ||
			gameCache.Exists(r.game.ID, mod.SourceID, mod.ID, mod.Version) {
			continue
		}
		r.result.Issues++
		reason := fmt.Sprintf("reinstall it (`lmm install --id %s --source %s --game %s --profile %s`) or uninstall it: lmm has no recorded file to download again",
			mod.ID, mod.SourceID, r.game.ID, r.profile)
		note := fmt.Sprintf("version %s is not in the cache any more", mod.Version)
		if mod.DisplayVersion != "" {
			note = fmt.Sprintf("its revision of %s is not in the cache any more", mod.DisplayVersion)
		}
		r.finding(VerifyFinding{
			ModID: mod.ID, ModName: mod.Name, Status: "missing_cache", Version: mod.Version,
			DisplayVersion: mod.DisplayVersion, Note: note, FixableReason: reason,
		}, VerifyEvent{Version: mod.Version})
	}
	return nil
}

// adoptedInPlace reports whether mod may never have had a cache entry, so
// that recordsPass has no entry to call gone: a local mod (no source to
// reinstall from - `lmm install --source local` installs nothing), a mod
// with no version, or a mod `lmm import` adopted in place on a game that is
// not in copy mode (adoptScannedMod writes a cache entry for copy mode
// only).
func adoptedInPlace(game *domain.Game, mod domain.InstalledMod) bool {
	return mod.SourceID == domain.SourceLocal || mod.Version == "" ||
		(mod.ManualDownload && game.DeployMode != domain.DeployCopy)
}

// recordsPassTolerant runs recordsPass, reporting a failure as a "skipped"
// row - every other pass's tolerance: a verify that reports nothing because
// one tier could not run is worse than one that says which did not.
func (r *verifyRun) recordsPassTolerant(installedMods []domain.InstalledMod, files []DeployedFile) {
	if err := r.recordsPass(installedMods, files); err != nil && r.ctx.Err() == nil {
		r.result.Warnings++
		r.finding(VerifyFinding{Status: "skipped", Note: fmt.Sprintf("records: %v", err)}, VerifyEvent{})
	}
}

// orphanedRecordFindings reports, and under --fix on the active profile
// drops, the records recordsPass found with no installed row, one finding
// per mod.
func (r *verifyRun) orphanedRecordFindings(order []string, orphans map[string][]string) error {
	live, err := r.svc.liveProfile(r.ctx, r.game.ID)
	active := err == nil && live == r.profile
	for _, key := range order {
		paths := orphans[key]
		slices.Sort(paths)
		sourceID, modID, _ := strings.Cut(key, ":")
		note := fmt.Sprintf("%d deployed-file record(s) of %s with no installed mod: %s", len(paths), key, strings.Join(paths, ", "))
		f := VerifyFinding{ModID: modID, Status: "orphaned_record", Note: note}
		switch {
		case err != nil:
			f.FixableReason = fmt.Sprintf("--fix drops such records only for the active profile, and lmm cannot tell which that is: %v", err)
		case !active:
			f.FixableReason = fmt.Sprintf("%s is not the active profile, so these files are judged by its purge: run `lmm purge --game %s --profile %s`", r.profile, r.game.ID, r.profile)
		case !r.opts.Fix:
			f.Fixable = true
		}
		if !r.opts.Fix || !active {
			r.result.Warnings++
			r.finding(f, VerifyEvent{})
			continue
		}
		var failed []string
		for _, path := range paths {
			if derr := r.svc.db.DeleteDeployedFile(r.ctx, r.game.ID, r.profile, path); derr != nil {
				failed = append(failed, fmt.Sprintf("%s (%v)", path, derr))
			}
		}
		if len(failed) > 0 {
			r.result.Warnings++
			f.FixableReason = "this --fix run could not drop the record(s) of " + strings.Join(failed, ", ")
			r.finding(f, VerifyEvent{})
			continue
		}
		r.finding(VerifyFinding{ModID: modID, Status: "fixed_orphaned_record",
			Note: fmt.Sprintf("dropped %d deployed-file record(s) of %s:%s with no installed mod; the files themselves were left as they are", len(paths), sourceID, modID)}, VerifyEvent{})
	}
	return nil
}
