package core

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/db"
)

// metaProfileDisabledBackfill is the db_meta key under which #431's
// profile-document backfill records that it has run. Only its presence is
// meaningful; the value is the UTC timestamp it ran, kept for the same
// reason the credential scrub's marker keeps one - a marker found in a bug
// report is worth being able to date.
const metaProfileDisabledBackfill = "profile_disabled_backfill_done"

// BackfillProfileDisabledMarkers records #431's `disabled:` marker on every
// profile reference whose installed row already says the mod is switched
// off, ONCE per installation, and reports how many references it marked.
//
// It closes the upgrade gap the marker itself cannot: a mod disabled before
// the key existed recorded that intent in installed_mods and nowhere else,
// because the profile document had no way to say it. Every converge flow
// reads the document, so without this the first `lmm profile switch` or
// `lmm profile apply` after the upgrade would switch each of those mods
// back on - and `lmm profile sync` would go further and delete the
// reference outright, load-order position and pinned version included.
//
// Why a one-time migration and not a rule. Teaching the plans to prefer the
// row over an unmarked reference would fix the same symptom, but it inverts
// the invariant the whole design rests on - the document is the desired
// state, and a missing `disabled` key means enabled - and it would make
// ProfileApplyPlan.ToEnable unreachable, so `lmm profile apply` could never
// switch a mod ON again. Recording the intent explicitly, once, leaves
// every flow's reading of the document exactly as documented, and the
// result is a document the user can inspect and edit: after this runs, a
// mod that is off says so in the file.
//
// It is deliberately conservative:
//
//   - a reference the document does not carry (domain.ErrModNotFound) or a
//     profile that does not exist (domain.ErrProfileNotFound) is skipped,
//     not an error - an installed row its profile never listed has no
//     desired-state entry to mark, and no converge pass will look for one;
//   - an EXTERNAL row is skipped (#269): lmm cannot switch a Steam Workshop
//     item off, so a marker there would read as an intent the user could not
//     have expressed through lmm at all;
//   - a reference that already carries the marker is left alone, and
//     ProfileManager.SetModDisabled does not rewrite a file whose ref
//     already says what it should - so an installation with nothing to
//     record does not move a byte of any profile file;
//   - the obligation is discharged by a durable db_meta marker rather than
//     by what the documents now look like, so a user who later edits a
//     marker away by hand does not have it written back on the next open.
//
// It takes the mutation slot only once it knows there is a profile file to
// write, and everything before that point is a read. That matters because
// the caller is app.Open: since #317 the slot's second half is a
// cross-process lock a running `lmm serve` takes for each mutation, and
// OPENING an installation must never wait on one - so every open after the
// first costs one indexed lookup and takes no lock at all. If the one open
// that does owe work finds the lock held, it reports that and the next open
// finishes the job.
func (s *Service) BackfillProfileDisabledMarkers(ctx context.Context) (int, error) {
	done, err := s.db.GetMeta(ctx, metaProfileDisabledBackfill)
	if err != nil {
		return 0, err
	}
	if done != "" {
		return 0, nil
	}

	rows, err := s.db.DisabledModRows(ctx)
	if err != nil {
		return 0, err
	}
	// #269: lmm cannot switch a Steam Workshop item off, so a marker there
	// would read as an intent the user could not have expressed through lmm
	// at all. Dropped here so an installation whose only disabled rows are
	// external ones discharges the obligation without taking any lock.
	work := make([]db.DisabledModRow, 0, len(rows))
	for _, row := range rows {
		if !row.External {
			work = append(work, row)
		}
	}
	if len(work) == 0 {
		// Nothing to record - the ordinary case for a fresh installation.
		// The db_meta row is the only write, and it needs no mutation slot:
		// it serializes nothing and completes nothing.
		return 0, s.db.SetMeta(ctx, metaProfileDisabledBackfill, backfillStamp())
	}

	release, err := s.beginOp(ctx)
	if err != nil {
		return 0, err
	}
	defer release()

	// Re-read under the slot: another process may have finished the job
	// while this one waited for the lock.
	if done, err := s.db.GetMeta(ctx, metaProfileDisabledBackfill); err != nil {
		return 0, err
	} else if done != "" {
		return 0, nil
	}

	pm := s.NewProfileManager()
	marked := 0
	// One profile document is loaded once, however many of its mods are
	// off: DisabledModRows orders by (game, profile), so the refs of the
	// profile being examined are always the ones just loaded.
	var (
		loadedKey string
		refs      map[string]bool // ModKey -> already marked
	)
	for _, row := range work {
		if err := ctx.Err(); err != nil {
			return marked, err
		}
		key := row.GameID + "/" + row.ProfileName
		if key != loadedKey {
			loadedKey, refs = key, nil
			profile, err := pm.Get(ctx, row.GameID, row.ProfileName)
			if err != nil {
				if errors.Is(err, domain.ErrProfileNotFound) {
					continue
				}
				return marked, fmt.Errorf("loading profile %q: %w", row.ProfileName, err)
			}
			refs = make(map[string]bool, len(profile.Mods))
			for _, ref := range profile.Mods {
				refs[domain.ModKey(ref.SourceID, ref.ModID)] = ref.Disabled
			}
		}
		alreadyMarked, listed := refs[domain.ModKey(row.SourceID, row.ModID)]
		if !listed || alreadyMarked {
			continue
		}
		if err := pm.SetModDisabled(ctx, row.GameID, row.ProfileName, row.SourceID, row.ModID, true); err != nil {
			if errors.Is(err, domain.ErrModNotFound) || errors.Is(err, domain.ErrProfileNotFound) {
				continue
			}
			return marked, fmt.Errorf("recording %s as disabled in profile %q: %w",
				domain.ModKey(row.SourceID, row.ModID), row.ProfileName, err)
		}
		marked++
	}

	// Discharged only after every write above succeeded: a failure part way
	// through leaves the marker unset, so the next open finishes the job
	// (the writes are idempotent - a ref that already says off is skipped).
	if err := s.db.SetMeta(ctx, metaProfileDisabledBackfill, backfillStamp()); err != nil {
		return marked, err
	}
	return marked, nil
}

// backfillStamp is the value recorded beside the obligation marker: the UTC
// time it was discharged. Only the key's presence is load-bearing.
func backfillStamp() string { return time.Now().UTC().Format(time.RFC3339) }
