package app

import (
	"sort"
	"sync"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/steam"
)

// RegisterKnownGames gives the Service lmm's game catalog (#606) - exactly
// what Open registers, and the call Open makes. Core uses it for a game's
// `catalog:` (which entry the game is: validated on edit, reported by
// verify when unknown) and for Service.SuggestedAdapter (verify's
// adapter_suggested note and the game page's offer). It never rewrites a
// game.
//
// The catalog is the one detection reads - the embedded steam-games.yaml
// plus the user's configDir/steam-games.yaml - loaded once, on the first
// question, so a command that never asks pays nothing. A catalog that
// cannot be read is an empty one: a suggestion is never worth an error.
func RegisterKnownGames(svc *core.Service, configDir string) {
	var (
		once sync.Once
		list []core.KnownGame
	)
	svc.SetKnownGames(func() []core.KnownGame {
		once.Do(func() {
			known, _ := steam.LoadKnownGames(configDir) //nolint:errcheck // an unreadable catalog is an empty one
			list = make([]core.KnownGame, 0, len(known))
			for appID, info := range known {
				list = append(list, core.KnownGame{Slug: info.Slug, Name: info.Name, SteamAppID: appID, Adapter: info.Adapter})
			}
			sort.Slice(list, func(i, j int) bool { return list[i].SteamAppID < list[j].SteamAppID })
		})
		return list
	})
}
