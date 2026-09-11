// Package thunderstore: this file is the UPDATE CHECK (#360 §3.5), which
// for this source costs nothing upstream at all.
//
// Every package's newest version_number is already in the local index, so
// checking thirty installed mods is thirty map lookups and ZERO requests -
// unlike every other source in lmm, where a check is at best one batched
// round trip and at worst one per mod. What remains is a refresh of the
// index itself, which is the same conditional GET a search makes and which
// upstream usually answers "unchanged" in zero bytes.
package thunderstore

import (
	"context"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

var (
	_ source.RefreshingUpdateChecker = (*Source)(nil)
	_ source.UpdateProgressReporter  = (*Source)(nil)
)

// CheckUpdates implements source.ModSource over the index as it stands.
func (s *Source) CheckUpdates(ctx context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	return s.CheckUpdatesRefreshing(ctx, installed, false, nil)
}

// CheckUpdatesWithProgress implements source.UpdateProgressReporter. The
// check is microseconds, but `lmm update`'s counter reserves a slice of its
// 1..N for every source, and a source that reports nothing leaves a hole in
// the printed sequence.
func (s *Source) CheckUpdatesWithProgress(ctx context.Context, installed []domain.InstalledMod, report source.UpdateProgressFunc) ([]domain.Update, error) {
	return s.CheckUpdatesRefreshing(ctx, installed, false, report)
}

// CheckUpdatesRefreshing implements source.RefreshingUpdateChecker: the
// same check, with refresh forcing the index past its TTL - which is what
// `lmm update --refresh` means here.
//
// The comparison is STRING EQUALITY, not semver ordering. versions[0] is
// authoritative about what "newest" means, because it is PUBLISH order: an
// author who re-publishes an older build is offering it, and a ">" test
// would hide that rather than report it. A version the package does not
// publish at all is not "ahead" either - it is different, and the newest
// published version is what lmm offers.
//
// A package that has LEFT the index is reported as no update, never as an
// error: "delisted" and "out of date" are different facts, and conflating
// them would send the user chasing an update that does not exist. Nor may
// one gone package blind the rest of the batch - the same rule
// ModDescription states, applied by hand because there is no batch call to
// make.
func (s *Source) CheckUpdatesRefreshing(ctx context.Context, installed []domain.InstalledMod, refresh bool, report source.UpdateProgressFunc) ([]domain.Update, error) {
	if len(installed) == 0 {
		// Asked before anything touches the index: a game with no
		// Thunderstore mods in it must not build a 34 MB one to say so.
		return nil, nil
	}

	// Grouped by community rather than assumed to be one, because nothing
	// in the seam promises a caller hands over a single game's worth - and
	// a refresh is per community.
	indexes := make(map[string]*residentIndex, 1)
	var updates []domain.Update
	total := len(installed)
	for i, im := range installed {
		if err := ctx.Err(); err != nil {
			return updates, err
		}
		if report != nil {
			report(i+1, total, im.Name)
		}
		idx, ok := indexes[im.GameID]
		if !ok {
			loaded, err := s.indexFor(ctx, im.GameID, refresh)
			if err != nil {
				return updates, err
			}
			indexes[im.GameID] = loaded
			idx = loaded
		}
		row, found := idx.row(im.ID)
		if !found || row.LatestVersion == "" || row.LatestVersion == im.Version {
			continue
		}
		updates = append(updates, domain.Update{InstalledMod: im, NewVersion: row.LatestVersion})
	}
	return updates, nil
}

// indexFor is the resident index for one community, refreshed first when
// the caller asked - the update check's own entry point into the same
// build/refresh policy Search and the package reads go through.
func (s *Source) indexFor(ctx context.Context, community string, refresh bool) (*residentIndex, error) {
	if err := validateCommunity(community); err != nil {
		return nil, err
	}
	wm, rows, present, err := s.ensureIndex(ctx, community, refresh, nil)
	if !present {
		return nil, err
	}
	// err here is a refresh that failed over an index still worth reading:
	// an old answer beats no answer, and IndexStatus.Stale carries the fact.
	idx, err := s.residentFor(community, wm, rows)
	if err != nil {
		return nil, indexUnavailable(community, err)
	}
	return idx, nil
}
