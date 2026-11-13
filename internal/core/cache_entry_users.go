package core

// cache_entry_users.go is the one rule every removal of a mod version's
// cache entry follows (#445 gate 2, G2-2): the entry goes only when no
// other installed row uses it. A local mod's cache entry is its only copy,
// and since `lmm profile apply` deploys a mod another profile has cached
// (cachedRowElsewhere), two rows on one entry are the ordinary state - so
// an uninstall that deleted the entry left the other profile with a mod it
// could never deploy again.

import (
	"context"
	"fmt"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
)

// cacheEntryUsers names the installed rows, other than profile's own in
// game, that use game's cache entry for sourceID/modID at version: a row of
// game's other profiles, or of another game whose cache holds the entry in
// the same directory (a shared cache_path), that has that version, or rolls
// back to it (rollback needs it cached). A row of a game lmm no longer has
// configured names no cache directory lmm can compare, so it counts too.
// Own game's rows come first, each list in profile order.
//
// k is the entry, k.GameID game's: the version AND the variant (#614), so
// a row with another tree of the same version does not hold it.
func (s *Service) cacheEntryUsers(ctx context.Context, game *domain.Game, profile string, k cache.Key) ([]string, error) {
	sourceID, modID, version, variant := k.SourceID, k.ModID, k.Version, k.Variant
	rows, err := s.db.ModVersionRows(ctx, sourceID, modID)
	if err != nil {
		return nil, fmt.Errorf("listing what else uses the cache entry of %s at %s: %w", domain.ModKey(sourceID, modID), version, err)
	}
	dir := s.GetGameCache(game).ModPath(k)
	var own, others []string
	for _, row := range rows {
		if row.GameID == game.ID && row.Profile == profile {
			continue
		}
		var why string
		switch {
		case row.Version == version && row.Variant == variant:
		case row.PreviousVersion != "" && row.PreviousVersion == version && row.PreviousVariant == variant:
			why = " (to roll back to)"
		default:
			continue
		}
		if row.GameID == game.ID {
			own = append(own, "profile "+row.Profile+why)
			continue
		}
		other, err := s.GetGame(row.GameID)
		if err != nil {
			others = append(others, fmt.Sprintf("game %s's profile %s%s (%s is not configured)", row.GameID, row.Profile, why, row.GameID))
			continue
		}
		otherKey := k
		otherKey.GameID = other.ID
		if samePath(dir, s.GetGameCache(other).ModPath(otherKey)) {
			others = append(others, fmt.Sprintf("game %s's profile %s%s", row.GameID, row.Profile, why))
		}
	}
	return append(own, others...), nil
}

// removeCacheEntry deletes game's cache entry k unless another row uses it (cacheEntryUsers, profile's own row aside):
// then it deletes nothing and returns those rows. A read that fails deletes
// nothing either.
func (s *Service) removeCacheEntry(ctx context.Context, game *domain.Game, profile string, k cache.Key) ([]string, error) {
	usedBy, err := s.cacheEntryUsers(ctx, game, profile, k)
	if err != nil {
		return nil, err
	}
	if len(usedBy) > 0 {
		return usedBy, nil
	}
	return nil, s.GetGameCache(game).Delete(k)
}

// cacheKeptNote reports a cache entry removeCacheEntry kept, and for whom.
func cacheKeptNote(sourceID, modID, version string, usedBy []string) string {
	verb := "uses"
	if len(usedBy) > 1 {
		verb = "use"
	}
	return fmt.Sprintf("Kept the cache entry of %s at %s: %s still %s it",
		domain.ModKey(sourceID, modID), version, strings.Join(usedBy, ", "), verb)
}
