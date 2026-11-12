package app

import (
	"sync"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/steam"
)

// RegisterCatalogAdapters gives the Service the Steam game catalog's
// `adapter:` per app id (#606) - exactly what Open registers, and the call
// Open makes. Core suggests that adapter for a game already in games.yaml
// that does not use it (Service.SuggestedAdapter): verify's
// adapter_suggested note and the game page's offer. It never rewrites a
// game.
//
// The catalog is the one detection reads - the embedded list plus the
// user's configDir/steam-games.yaml - loaded once, on the first question,
// so a command that never asks pays nothing. A catalog that cannot be read
// suggests nothing: a suggestion is never worth an error.
func RegisterCatalogAdapters(svc *core.Service, configDir string) {
	var (
		once  sync.Once
		known map[string]steam.GameInfo
	)
	svc.SetCatalogAdapters(func(appID string) string {
		once.Do(func() {
			known, _ = steam.LoadKnownGames(configDir) //nolint:errcheck // an unreadable catalog suggests nothing
		})
		return known[appID].Adapter
	})
}
