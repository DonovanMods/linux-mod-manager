package core

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
)

// redownloadModFile re-downloads a single mod file and extracts it to the
// cache, then updates the checksum in the DB. persisted reports whether a
// checksum row was actually written: a download can succeed while yielding no
// checksum to store (e.g. a directory-source mod whose directory holds no
// regular files), and callers must not report "checksum populated" on the
// strength of a nil error alone (#164) - only when persisted is true.
//
// #224 Task 4: ported verbatim from cmd/lmm/verify.go's redownloadModFile
// (pre-refactor, doVerify's --fix repair helper), minus the cmd parameter -
// callers now pass ctx directly, and game/profile come from the run itself
// rather than being threaded in.
func (r *verifyRun) redownloadModFile(ctx context.Context, mod *domain.InstalledMod, fileID string) (persisted bool, err error) {
	files, err := r.svc.GetModFiles(ctx, mod.SourceID, SourceMappedMod(r.game, &mod.Mod))
	if err != nil {
		return false, fmt.Errorf("getting mod files: %w", err)
	}
	var downloadFile *domain.DownloadableFile
	for i := range files {
		if files[i].ID == fileID {
			downloadFile = &files[i]
			break
		}
	}
	if downloadFile == nil {
		return false, fmt.Errorf("file %s not found in mod", fileID)
	}
	// SourceMappedMod on the download too (#228): Service.DownloadMod
	// forwards its mod straight to src.GetDownloadURL with no translation of
	// its own, so passing the raw installed row here - whose GameID is the
	// LMM game ID - resolved the URL against the wrong upstream game on any
	// source with a non-identity SourceIDs mapping, while the GetModFiles
	// lookup directly above was already mapped. The cache side is unaffected
	// either way: every cache path is keyed off game.ID, not mod.GameID.
	result, err := r.svc.downloadMod(ctx, mod.SourceID, r.game, SourceMappedMod(r.game, &mod.Mod), downloadFile, nil)
	if err != nil {
		return false, err
	}
	if result.Checksum == "" {
		return false, nil
	}
	if err := r.svc.saveFileChecksum(ctx, mod.SourceID, mod.ID, r.game.ID, r.profile, fileID, result.Checksum); err != nil {
		return false, fmt.Errorf("saving checksum: %w", err)
	}
	return true, nil
}

// cacheDirExists reports whether a cache mod-version directory exists,
// distinguishing "genuinely absent" (fs.ErrNotExist - a normal state: the
// version was cleared, or never cached) from any OTHER stat failure
// (permission denied, I/O error, a corrupted mount, etc.), which must NOT
// be silently treated as "absent" (epic98 audit Finding 4): proceeding to
// a DB write on the strength of a check that itself failed would violate
// the invariant that no DB write happens unless the cache state was
// actually verified first - the same invariant os.Rename's own failure
// path already protects.
//
// #224 Task 5: ported verbatim from cmd/lmm/verify.go (pre-refactor).
func cacheDirExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return false, err
}

// repairModVersion repairs a VERSION MISMATCH row for --fix (issue #94):
// the recorded Version doesn't match what the stored file ID(s) actually
// are upstream (effective), so the cache entry, DB row, and profile record
// are all keyed by the wrong version. Fix sequence:
//
//  1. Cache re-key: if the cache dir for the recorded version exists and
//     the one for effective does not, rename it. If the effective-version
//     dir already exists, leave the cache alone and return a note instead
//     of clobbering it via os.Rename - the existing deployment (if any)
//     still points at the intact recorded-version dir, so step 4 below is
//     skipped in this case rather than re-linking into the unvetted
//     pre-existing one. If the rename itself fails, OR either stat check
//     itself fails for a reason other than "doesn't exist" (audit Finding
//     4), return immediately with no DB write - the row stays
//     version_mismatch and the DB/cache never disagree in a new way.
//  2. Profile: upsert the corrected version into the active profile's YAML
//     record. Runs BEFORE step 3's DB write (audit Finding 3, swapped from
//     the original order): the DB row is the signal versionPass's own
//     mismatch detection reads (GetInstalledMods, not the profile YAML),
//     so it must be the LAST of these two writes to succeed - if the
//     upsert fails here, the DB is untouched, so a retry still detects
//     version_mismatch and can converge. The original order (DB first)
//     let a profile-upsert failure leave the DB already at effective, so
//     retries never re-detected the mismatch and the stale YAML ref could
//     never self-heal.
//  3. DB: SetModVersion - a plain version-column update (audit Finding 1),
//     deliberately NOT SaveInstalledMod's full-row upsert, which always
//     re-keys installed_mod_files (DELETE + a checksum-less re-INSERT)
//     even though FileIDs is completely unchanged here, silently wiping
//     every stored checksum for the mod on every repair. Also deliberately
//     not UpdateModVersion, which would shift the wrong value into
//     PreviousVersion and poison rollback - this is a correction, not an
//     update.
//  4. Re-link: skipped when step 1 left the cache alone (note != "" - the
//     current symlinks already point at valid content, so there's nothing
//     to fix). Otherwise, if the mod is Deployed via a symlink, the
//     game-dir symlinks point INTO the cache dir renamed in step 1 and are
//     now dangling - re-run the installer to refresh them, built from the
//     PROFILE-effective link method and recording it on the row
//     (relinkDeployedRow, #152) - not the game-level method, which would
//     "repair" a profile-level copy/hardlink override back to the game's.
//     Hardlink/copy deployments are untouched by the cache rename, so
//     they're left alone regardless. On failure, SetModDeployed (not
//     SaveInstalledMod - audit Finding 1 again) clears the flag without
//     touching file IDs.
//  5. Sibling profiles: runs when step 1 renamed the cache THIS run, OR
//     (audit Finding 2) when the cache already lives under the effective
//     version from an EARLIER, partially-failed run - old path absent,
//     new path present, and no blocked-rename note (which would mean the
//     new dir was pre-existing/unvetted content, not this repair's own
//     prior success). Without this, a retry after a run that renamed the
//     cache but then failed before completing would see oldPath already
//     gone, renamed=false, and skip siblings entirely - even though the
//     shared cache dir has, in fact, already moved out from under them.
//     Every OTHER profile's installed_mods row for the same (SourceID,
//     ModID, GameID) whose Version still equals the OLD recorded version
//     is corrected the same way (DB + profile YAML), and re-linked if
//     Deployed via symlink - see repairSiblingProfiles. Runs
//     unconditionally once triggered, even if step 4 above (the PRIMARY
//     row's own re-link) failed - the primary's re-link outcome has no
//     bearing on whether siblings were orphaned by the same rename.
//
// siblingFailures is the count of per-sibling failures/declines from step
// 5 (a pm.List failure counts as one), structurally propagated - not
// string-matched out of note - so the caller can add it straight into its
// own warnings counter. It's independent of err: siblingFailures can be
// non-zero whether or not the PRIMARY row's own repair (this function's
// err) succeeded, since a sibling's fate has no bearing on the primary's.
//
// mod.Version is mutated in place only once step 3's DB write has actually
// succeeded, so a failure partway through never leaves the in-memory row
// claiming a fix that didn't happen. If step 4 itself fails (e.g. a
// read-only game dir), the DB/profile version correction from steps 2-3 is
// NOT rolled back - retrying it would just repeat the same
// rename-already-done state - but Deployed is cleared and saved before
// returning the error: effective == mod.Version by then, so a later
// `verify --fix` no longer sees a version_mismatch to retry, and leaving
// Deployed true would falsely claim a working deployment that isn't
// there. Clearing it at least keeps that one field honest so other
// surfaces (status displays, a future `lmm deploy`, which redeploys every
// enabled mod regardless of its recorded Deployed value) aren't misled.
//
// #224 Task 5: ported verbatim from cmd/lmm/verify.go's repairModVersion
// (pre-refactor) - the cmd/svc/game/profile parameters are replaced by the
// verifyRun receiver (svc/game/profile come from r), and every inline
// fmt.Printf (previously gated on !jsonOutput) is now an unconditional
// VerifyEvRepairDetail emission: the CLI's own renderer decides whether to
// print it, same as every other repair site ported in Task 4.
func (r *verifyRun) repairModVersion(ctx context.Context, mod *domain.InstalledMod, effective string) (note string, siblingFailures int, err error) {
	recorded := mod.Version
	gameCache := r.svc.GetGameCache(r.game)
	oldPath := gameCache.ModPath(r.game.ID, mod.SourceID, mod.ID, recorded)
	newPath := gameCache.ModPath(r.game.ID, mod.SourceID, mod.ID, effective)

	oldExists, statErr := cacheDirExists(oldPath)
	if statErr != nil {
		return "", 0, fmt.Errorf("checking cache entry for %s: %w", recorded, statErr)
	}
	newExists, statErr := cacheDirExists(newPath)
	if statErr != nil {
		return "", 0, fmt.Errorf("checking cache entry for %s: %w", effective, statErr)
	}

	var renamed bool
	if oldExists {
		if newExists {
			note = fmt.Sprintf("cache entry for %s already exists; left %s in place", effective, recorded)
		} else if renameErr := os.Rename(oldPath, newPath); renameErr != nil {
			return "", 0, fmt.Errorf("renaming cache entry: %w", renameErr)
		} else {
			renamed = true
		}
	}

	pm := r.svc.NewProfileManager()
	if err := pm.UpsertMod(ctx, r.game.ID, r.profile, domain.ModReference{
		SourceID: mod.SourceID,
		ModID:    mod.ID,
		Version:  effective,
		FileIDs:  mod.FileIDs,
	}); err != nil {
		return note, 0, fmt.Errorf("updating profile record: %w", err)
	}

	if err := r.svc.setModVersion(ctx, mod.SourceID, mod.ID, mod.GameID, r.profile, effective); err != nil {
		return note, 0, fmt.Errorf("saving installed mod: %w", err)
	}
	mod.Version = effective

	// A blocked rename (note != "") leaves the existing deployment pointed
	// at the still-intact recorded-version cache dir - nothing dangles, so
	// re-linking would only repoint a working deployment into the
	// unvetted pre-existing effective-version dir for no reason.
	var relinkErr error
	var relinkNotes []string
	if note == "" && mod.Deployed && mod.LinkMethod == domain.LinkSymlink {
		installErr, recordErr, undeployErr := r.relinkDeployedRow(ctx, r.profile, mod)
		if undeployErr != nil {
			// Non-fatal (see relinkDeployedRow's doc) - but surfaced in
			// BOTH output modes (PR #154 Copilot) - text here, and folded
			// into the row's note below (after the sibling pass, which
			// overwrites note wholesale), so a --json caller sees the
			// partial cleanup a human would be shown.
			relinkNotes = append(relinkNotes, fmt.Sprintf("undeploy before re-link: %v", undeployErr))
			r.emitEv(VerifyEvent{Kind: VerifyEvRepairDetail, Detail: fmt.Sprintf("Warning: undeploy %s: %v", mod.Name, undeployErr)})
		}
		if installErr != nil {
			mod.Deployed = false
			if saveErr := r.svc.setModDeployed(ctx, mod.SourceID, mod.ID, mod.GameID, r.profile, false); saveErr != nil {
				relinkErr = fmt.Errorf("relinking deployed files: %w (also failed to clear deployed flag: %v)", installErr, saveErr)
			} else {
				relinkErr = fmt.Errorf("relinking deployed files: %w", installErr)
			}
		} else if recordErr != nil {
			// Non-fatal, like DeployProfile's own SetModLinkMethod failure
			// handling: the deployment itself is fixed; only the recorded
			// method is stale. Same two-mode surfacing as the undeploy
			// warning above.
			msg := fmt.Sprintf("could not record link method: %v", recordErr)
			relinkNotes = append(relinkNotes, msg)
			r.emitEv(VerifyEvent{Kind: VerifyEvRepairDetail, Detail: "Warning: " + msg})
		}
	}

	// Sibling repair runs regardless of the primary re-link's own outcome
	// (above) - a sibling row's orphaning is caused by the cache rename,
	// not by anything that happens to the primary row's deployment.
	var siblingCancelErr error
	if renamed || (!oldExists && newExists && note == "") {
		note, siblingFailures, siblingCancelErr = r.repairSiblingProfiles(ctx, mod, recorded, effective)
	}

	if len(relinkNotes) > 0 {
		joined := strings.Join(relinkNotes, "; ")
		if note != "" {
			note += "; " + joined
		} else {
			note = joined
		}
	}

	// A cancellation outranks the re-link failure: the run is over either
	// way, and only the cancellation tells the caller why.
	if siblingCancelErr != nil {
		return note, siblingFailures, siblingCancelErr
	}

	if relinkErr != nil {
		return note, siblingFailures, relinkErr
	}

	return note, siblingFailures, nil
}

// fileIDsEqual reports whether two FileID sets are equal, ignoring order.
// Used by repairSiblingProfiles (epic98 audit Finding 6) to gate
// auto-repair on a sibling actually recording the SAME file selection as
// the primary, not just the same (wrong) version string - two profiles can
// legitimately record the same wrong version for DIFFERENT files (e.g. a
// different optional file chosen at install time), and blindly stamping
// the sibling with the primary's effective version would be wrong for
// that sibling's own files.
//
// #224 Task 5: ported verbatim from cmd/lmm/verify.go (pre-refactor).
func fileIDsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[string]int, len(a))
	for _, id := range a {
		counts[id]++
	}
	for _, id := range b {
		counts[id]--
	}
	for _, c := range counts {
		if c != 0 {
			return false
		}
	}
	return true
}

// repairSiblingProfiles corrects every OTHER profile's installed_mods row
// for the same (SourceID, ModID, GameID) that still records the OLD
// (pre-repair) version, after repairModVersion has renamed the shared
// cache dir out from under them - cache.ModPath has no profile segment
// (internal/storage/cache/cache.go:29), so a rename done for one profile's
// row silently orphans any sibling profile still pointing at the old
// version, until that sibling is itself verified. Only called when the
// cache has actually moved to the effective version (this run's rename,
// or an earlier run's - see repairModVersion's step 5 doc); a blocked or
// never-attempted rename never orphans anything, so siblings are left
// alone in that case.
//
// A profile that never had the mod installed at all (GetInstalledMod
// returning domain.ErrModNotFound) is a normal, silent skip - it was never
// a repair candidate. Any OTHER lookup error (a genuine DB failure) is
// deliberately NOT treated the same way: it means we couldn't even tell
// whether that profile needed repair, which is surfaced as a per-sibling
// failure below rather than silently looking identical to "not a
// candidate".
//
// Sibling rows whose Version differs from recorded are a different state
// entirely (not something this repair caused) and are left untouched. A
// sibling whose Version MATCHES but whose FileIDs do NOT (audit Finding 6,
// via fileIDsEqual) is also left untouched - the recorded version is wrong
// for that sibling too, but auto-stamping it with the PRIMARY's effective
// version would be wrong when the underlying files differ - instead it's
// surfaced as its own warning pointing at the right manual fix
// (`verify --fix -p <profile>`).
//
// A sibling whose ref is LOCKED in its OWN profile (checked fresh per
// sibling, since the lock lives in that sibling's profile YAML, not the
// primary's) is likewise left untouched, for the same reason the primary
// row's own repair refuses a locked ref (repairModVersion's caller,
// verify.go:362-377): the record IS that sibling's lock target, and
// rewriting it here would silently move what the lock means just as surely
// as rewriting the primary would - circumventing verify's own primary-row
// refusal by going around it through a sibling. Surfaced as its own
// warning naming the sibling profile, distinct from the "differs" decline.
// The warning names the MOD as locked (matching the primary refusal's own
// wording), not the profile - a profile isn't "locked", a ref in it is -
// and gives its remedies with an explicit -p <sibling> flag, since
// `lmm mod lock`/`lmm mod unlock` without one resolve against the
// active/-p profile (the PRIMARY here), and copy-pasting an unflagged
// remedy would move/clear the lock in the wrong profile. When the sibling
// was Deployed, the primary repair has already renamed the shared cache
// dir out from under it (repairSiblingProfiles' own doc above) and this
// decline bypasses the re-link that would otherwise follow - so the
// warning also says the sibling's deployment may be broken until the lock
// is moved or cleared, since `verify --fix -p <sibling>` will decline the
// same way until then.
//
// Like the primary path (repairModVersion), the profile YAML is upserted
// BEFORE the DB version is set (audit Finding 3): the DB row is what a
// LATER verify run's mismatch detection reads for that sibling too, so if
// the upsert fails, the DB must stay at the old, still-detectable version
// rather than silently converging to "no mismatch, nothing to retry".
// SetModVersion/SetModDeployed (never SaveInstalledMod's full-row upsert,
// which would wipe stored checksums - audit Finding 1) do the writes.
//
// A sibling Deployed via symlink has its links re-created through the
// installer exactly like the primary row's would be - built from that
// sibling's OWN profile-effective link method (relinkDeployedRow, #152),
// since a link_method override lives per-profile and the sibling's may
// differ from both the game's and the primary profile's; on a per-sibling
// re-link failure, that row's Deployed flag alone is cleared (matching the
// primary row's own re-link-failure handling), but - unlike the DB/profile
// steps, which did succeed - this is reported as a FAILURE, not folded
// into "repaired": the version record is correct, but the deployment
// itself is still broken, so printing the same green "Repaired" line
// would misreport a partial fix as a clean success. Processing continues
// with the remaining siblings rather than aborting the whole pass.
//
// Per DEV.md's "never swallow errors without logging/context": a failure
// to correct one sibling (pm.List itself, or a given sibling's
// UpsertMod/SetModVersion/re-link) is NOT silently skipped - it's printed
// as a warning in text mode and folded into the returned summary, since a
// write or re-link failure here leaves that profile in exactly the
// orphaned/dangling state this whole repair exists to fix,
// indistinguishable from "was never a candidate" if left unreported. The
// loop still continues past a single sibling's failure - best-effort, not
// all-or-nothing.
//
// Returns a human-readable summary of which profiles were repaired,
// failed, and/or declined (for differing file selection or a lock - empty
// if none), for the caller to fold into repairModVersion's own note - the --json
// contract's vehicle for surfacing this, per the primary row's existing
// "note" field - and, structurally (not by string-matching the note
// text), the combined count of per-sibling failures and declines, so the
// caller can add it straight into its own warnings counter: both are
// real, surfaced work left undone and must count as a warning regardless
// of how the PRIMARY row's own repair turned out. A pm.List failure
// counts as one.
//
// #224 Task 5: ported verbatim from cmd/lmm/verify.go's
// repairSiblingProfiles (pre-refactor) - the cmd/svc/game parameters are
// replaced by the verifyRun receiver, currentProfile by r.profile, and
// every inline fmt.Printf/fmt.Println (previously gated on !jsonOutput) is
// now an unconditional VerifyEvRepairDetail emission, matching
// repairModVersion's own port.
//
// cancelErr is non-nil only when the run was cancelled part-way through the
// sibling sweep (v2 Phase 3 Ruling 16): a cancelled profile read or write
// would otherwise repeat identically for every remaining sibling, turning one
// Ctrl-C into N "could not repair profile X" warnings and a non-zero
// failedCount. The note and count accumulated up to that point are still
// returned, so the caller reports the work that really happened.
func (r *verifyRun) repairSiblingProfiles(ctx context.Context, mod *domain.InstalledMod, recorded, effective string) (note string, failedCount int, cancelErr error) {
	pm := r.svc.NewProfileManager()
	profiles, listErr := pm.List(ctx, r.game.ID)
	if listErr != nil {
		if cerr := ctx.Err(); cerr != nil {
			return "", 0, cerr
		}
		msg := fmt.Sprintf("could not enumerate profiles to check for shared-cache siblings: %v", listErr)
		r.emitEv(VerifyEvent{Kind: VerifyEvRepairDetail, Detail: "Warning: " + msg})
		return "sibling repair check FAILED: " + msg, 1, nil
	}

	var repaired, failed, differs, locked, methodNotes, undeployWarns []string
	for _, p := range profiles {
		if p.Name == r.profile {
			continue
		}

		sibling, err := r.svc.GetInstalledMod(ctx, mod.SourceID, mod.ID, r.game.ID, p.Name)
		if err != nil {
			if !errors.Is(err, domain.ErrModNotFound) {
				// A genuine lookup failure (DB error, corruption, etc.) is
				// NOT the same as "this profile never had the mod" - the
				// latter is a normal, silent skip; the former means we
				// couldn't even tell whether this profile needed repair,
				// which must be surfaced the same as any other per-sibling
				// failure rather than looking indistinguishable from "not a
				// candidate".
				failed = append(failed, fmt.Sprintf("%s (checking sibling: %v)", p.Name, err))
				r.emitEv(VerifyEvent{Kind: VerifyEvRepairDetail, Detail: fmt.Sprintf("Warning: could not repair profile %s: %v", p.Name, err)})
			}
			continue
		}
		if sibling.Version != recorded {
			continue
		}
		if !fileIDsEqual(sibling.FileIDs, mod.FileIDs) {
			differs = append(differs, p.Name)
			r.emitEv(VerifyEvent{Kind: VerifyEvRepairDetail, Detail: fmt.Sprintf("Warning: profile %s records the same version but differs in file selection; run verify --fix -p %s", p.Name, p.Name)})
			continue
		}

		// #97: a sibling ref locked in ITS OWN profile refuses the same
		// rewrite the primary row's own repair refuses (verify.go:362-377) -
		// the record is that sibling's lock target, and rewriting it here
		// would silently move what the lock means, just as surely as
		// rewriting the primary would. Loaded fresh per-sibling since the
		// lock lives in that sibling's own profile YAML, not the primary's.
		if siblingProfile, perr := pm.Get(ctx, r.game.ID, p.Name); perr == nil {
			if ref := siblingProfile.FindRef(sibling.SourceID, sibling.ID); ref != nil && ref.Locked {
				locked = append(locked, p.Name)
				// #142 round 5: also name -s (in addition to the -p this
				// warning already carried) - resolveSource must otherwise
				// disambiguate on its own whenever more than one source
				// is configured, which a copy-pasted remedy shouldn't
				// depend on.
				msg := fmt.Sprintf("%s is locked at v%s in profile %s; run 'lmm mod lock -s %s -p %s %s <version>' or unlock with 'lmm mod unlock -s %s -p %s %s' instead of rewriting it", sibling.Name, ref.Version, p.Name, sibling.SourceID, p.Name, sibling.ID, sibling.SourceID, p.Name, sibling.ID)
				if sibling.Deployed {
					msg += "; its deployment may be broken until the lock is moved or cleared"
				}
				r.emitEv(VerifyEvent{Kind: VerifyEvRepairDetail, Detail: "Warning: " + msg})
				continue
			}
		} else if cerr := ctx.Err(); cerr != nil {
			// Ruling 16 (C): a lock gate that cannot read the profile
			// reports "no lock" - correct for a missing or corrupt file,
			// wrong for a cancellation, which would degrade this gate open
			// for every remaining sibling.
			cancelErr = cerr
			break
		}
		// (A missing/unreadable sibling profile falls through - matches
		// ApplyUpdate/ApplyRollback's own precedent: a lock cannot exist in
		// an unloadable profile.)

		if err := pm.UpsertMod(ctx, r.game.ID, p.Name, domain.ModReference{
			SourceID: sibling.SourceID,
			ModID:    sibling.ID,
			Version:  effective,
			FileIDs:  sibling.FileIDs,
		}); err != nil {
			// Ruling 16 (B): this write PRECEDES its DB half
			// (setModVersion below), so UpsertMod itself failing or being
			// cancelled HERE leaves nothing half-committed - unlike the
			// completing writes, which finish under context.WithoutCancel.
			if cerr := ctx.Err(); cerr != nil {
				cancelErr = cerr
				break
			}
			failed = append(failed, fmt.Sprintf("%s (%v)", p.Name, err))
			r.emitEv(VerifyEvent{Kind: VerifyEvRepairDetail, Detail: fmt.Sprintf("Warning: could not repair profile %s: %v", p.Name, err)})
			continue
		}

		// If UpsertMod above SUCCEEDED and the cancellation instead lands
		// HERE, the sibling's profile ref is already on the new version
		// while its DB row still holds the old one - the same drift
		// completeDBWrite exists to prevent, on the (B) side of the pair.
		// Pre-existing (UpsertMod was ctx-less and always ran at 1d68cd0,
		// so this window is not new) and self-healing: `verify --fix` is
		// convergent, so the next run repairs it (task 18 re-review round
		// 2, NEW-3).
		if err := r.svc.setModVersion(ctx, sibling.SourceID, sibling.ID, sibling.GameID, p.Name, effective); err != nil {
			failed = append(failed, fmt.Sprintf("%s (%v)", p.Name, err))
			r.emitEv(VerifyEvent{Kind: VerifyEvRepairDetail, Detail: fmt.Sprintf("Warning: could not repair profile %s: %v", p.Name, err)})
			continue
		}
		sibling.Version = effective

		if sibling.Deployed && sibling.LinkMethod == domain.LinkSymlink {
			installErr, recordErr, undeployErr := r.relinkDeployedRow(ctx, p.Name, sibling)
			if undeployErr != nil {
				// Non-fatal (see relinkDeployedRow's doc), but surfaced in
				// both output modes (PR #154 Copilot) via its own parts
				// entry below - reported BEFORE the install-failure branch
				// so it reaches the note either way. Not counted in
				// failedCount: with a successful install nothing is left
				// broken, and a failed one already counts itself.
				undeployWarns = append(undeployWarns, fmt.Sprintf("%s (%v)", p.Name, undeployErr))
				r.emitEv(VerifyEvent{Kind: VerifyEvRepairDetail, Detail: fmt.Sprintf("Warning: undeploy %s in profile %s: %v", sibling.Name, p.Name, undeployErr)})
			}
			if installErr != nil {
				if saveErr := r.svc.setModDeployed(ctx, sibling.SourceID, sibling.ID, sibling.GameID, p.Name, false); saveErr != nil {
					failed = append(failed, fmt.Sprintf("%s (relinking deployed files: %v; also failed to clear deployed flag: %v)", p.Name, installErr, saveErr))
				} else {
					failed = append(failed, fmt.Sprintf("%s (relinking deployed files: %v)", p.Name, installErr))
				}
				r.emitEv(VerifyEvent{Kind: VerifyEvRepairDetail, Detail: fmt.Sprintf("Warning: could not repair profile %s: %v", p.Name, installErr)})
				// The version correction (DB + profile, above) stands, but
				// the deployment itself is still broken - report this as a
				// failure, not a clean "Repaired" success.
				continue
			}
			if recordErr != nil {
				// Non-fatal (DeployProfile's own precedent): the sibling's
				// deployment is fixed; only its recorded method is stale.
				// Not counted in failedCount - unlike the failures above,
				// nothing here is left broken on disk - but still surfaced
				// in both output modes via its own parts entry below.
				methodNotes = append(methodNotes, fmt.Sprintf("%s (%v)", p.Name, recordErr))
				r.emitEv(VerifyEvent{Kind: VerifyEvRepairDetail, Detail: fmt.Sprintf("Warning: could not record link method for profile %s: %v", p.Name, recordErr)})
			}
		}

		repaired = append(repaired, p.Name)
		r.emitEv(VerifyEvent{Kind: VerifyEvRepairDetail, Detail: fmt.Sprintf("Repaired (profile %s): %s → %s", p.Name, recorded, effective), Fixed: true})
	}

	var parts []string
	if len(repaired) > 0 {
		parts = append(parts, fmt.Sprintf("also repaired in profile(s): %s", strings.Join(repaired, ", ")))
	}
	if len(failed) > 0 {
		parts = append(parts, fmt.Sprintf("repair FAILED in profile(s): %s", strings.Join(failed, ", ")))
	}
	if len(differs) > 0 {
		parts = append(parts, fmt.Sprintf("differs in file selection in profile(s): %s (run verify --fix -p <profile>)", strings.Join(differs, ", ")))
	}
	if len(locked) > 0 {
		parts = append(parts, fmt.Sprintf("locked in profile(s): %s (move the lock or unlock instead)", strings.Join(locked, ", ")))
	}
	if len(methodNotes) > 0 {
		parts = append(parts, fmt.Sprintf("could not record link method in profile(s): %s", strings.Join(methodNotes, ", ")))
	}
	if len(undeployWarns) > 0 {
		parts = append(parts, fmt.Sprintf("undeploy warning in profile(s): %s", strings.Join(undeployWarns, ", ")))
	}
	return strings.Join(parts, "; "), len(failed) + len(differs) + len(locked), cancelErr
}

// relinkDeployedRow re-runs the installer for a row recorded as a symlink
// deployment whose links dangle after a cache re-key, building the installer
// from profileName's EFFECTIVE link method (profile > game > global, #81) -
// not the game-level one (#152): a profile-level copy/hardlink override
// would otherwise be "repaired" back to the game's method, re-introducing
// exactly the drift verify exists to fix. The method is resolved once (the
// same single-resolve shape as DeployProfile, internal/core/flows.go) and,
// on a successful install, recorded on the row via SetModLinkMethod exactly
// like DeployProfile records its own deploys - the row was just re-linked
// with the effective method, and leaving it claiming the old one would be a
// new record-vs-reality drift. The write is skipped when the method didn't
// change; on success mod.LinkMethod is updated in place.
//
// The helper does no reporting of its own - the caller owns it all, since
// the primary and sibling sites surface things differently. installErr is
// the Install failure (the caller also owns clearing Deployed);
// undeployErr is an Uninstall failure, which is deliberately NOT fatal
// (DeployProfile's own precedent): one that still lets Install succeed
// left nothing broken - every file was rewritten - and one that does break
// Install surfaces through installErr too, so undeployErr can accompany
// installErr as context. recordErr is a SetModLinkMethod failure after a
// SUCCESSFUL install, likewise non-fatal: the deployment itself is fixed,
// only the recorded method is stale, and failing the whole repair over it
// would misreport a fixed deployment as broken. installErr and recordErr
// are mutually exclusive. A GetEffectiveLinkMethod failure (#189: an invalid
// profile link_method) is reported as installErr too - it happens before
// anything is touched, same as any other reason no method could be
// resolved to install with.
//
// #224 Task 5: ported verbatim from cmd/lmm/verify.go's relinkDeployedRow
// (pre-refactor) - the cmd parameter is replaced by an explicit ctx
// (redownloadModFile's own precedent), and svc/game come from the
// verifyRun receiver.
func (r *verifyRun) relinkDeployedRow(ctx context.Context, profileName string, mod *domain.InstalledMod) (installErr, recordErr, undeployErr error) {
	method, err := r.svc.GetEffectiveLinkMethod(ctx, r.game, profileName)
	if err != nil {
		return err, nil, nil
	}
	installer := r.svc.newInstallerWithLinker(r.game, r.svc.getLinker(method))
	// Undeploy-then-install, the same shape DeployProfile uses
	// (internal/core/flows.go) and for the same reason: dst still holds
	// whatever the previous deployment left behind (here, the dangling
	// symlinks the cache re-key orphaned), and only the symlink and
	// hardlink linkers' Deploy clear an existing dst themselves - the copy
	// linker's OpenFile would follow (or trip over) a stale symlink instead
	// of replacing it.
	undeployErr = installer.Uninstall(ctx, r.game, &mod.Mod, profileName)
	if err := installer.Install(ctx, r.game, &mod.Mod, profileName); err != nil {
		return err, nil, undeployErr
	}
	if method != mod.LinkMethod {
		if err := r.svc.setModLinkMethod(ctx, mod.SourceID, mod.ID, mod.GameID, profileName, method); err != nil {
			return nil, err, undeployErr
		}
		mod.LinkMethod = method
	}
	return nil, nil, undeployErr
}
