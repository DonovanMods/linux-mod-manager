package core

import (
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
)

// modCacheKey is the cache entry m's files live in for gameID: its source
// version AND its variant (#614). Every read of an installed mod's entry
// goes through it (or spells the same literal), because two trees of one
// version - different FOMOD choices - are two entries, and keying by the
// version alone would read the wrong one.
func modCacheKey(gameID string, m domain.Mod) cache.Key {
	return cache.Key{GameID: gameID, SourceID: m.SourceID, ModID: m.ID, Version: m.Version, Variant: m.Variant}
}

// previousCacheKey is the entry m's rollback target lives in: its previous
// version and that version's variant.
func previousCacheKey(gameID string, m *domain.InstalledMod) cache.Key {
	return cache.Key{GameID: gameID, SourceID: m.SourceID, ModID: m.ID, Version: m.PreviousVersion, Variant: m.PreviousVariant}
}
