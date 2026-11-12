package core

import (
	"fmt"
	"slices"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// VerifyStatusAdapterSuggested is the verify row for a game the curated
// game catalog would set up with an adapter it does not use (#606): a
// Steam-detected Skyrim SE added before the catalog named `adapter:
// bethesda`, say. It is a note, counted towards neither tally, and never
// fixable: lmm does not rewrite a game's adapter, it says how to.
const VerifyStatusAdapterSuggested = "adapter_suggested"

// CatalogAdapterLookup answers the curated game catalog's `adapter:` for a
// Steam app id, or "" when the catalog names none.
type CatalogAdapterLookup func(steamAppID string) string

// SetCatalogAdapters registers how this Service reads the curated game
// catalog's adapter for a Steam app id. The composition root calls it
// (internal/app), because the catalog lives in a concrete source core may
// not import. Until it does, SuggestedAdapter suggests nothing.
func (s *Service) SetCatalogAdapters(lookup CatalogAdapterLookup) {
	if lookup == nil {
		s.catalogAdapters.Store(nil)
		return
	}
	s.catalogAdapters.Store(&lookup)
}

// SuggestedAdapter returns the adapter the curated catalog names for game's
// Steam app id when the game does not use it, or "".
//
// It only ever suggests for a game with no `adapter:` key that resolves to
// the identity: an explicit adapter, `generic-files` included, is the
// user's choice, and a derived one (BepInEx from a loader) already says
// what the game is. The app id is the one the launcher reads -
// launch.steam_app_id, a numeric steamworkshop mapping, or the Steam
// library holding the install path - and the suggestion must be an adapter
// this lmm registers.
func (s *Service) SuggestedAdapter(game *domain.Game) string {
	p := s.catalogAdapters.Load()
	if p == nil || game == nil || game.Adapter != "" {
		return ""
	}
	if s.AdapterName(game) != adapter.GenericID {
		return ""
	}
	appID := s.gameSteamAppID(game)
	if appID == "" {
		return ""
	}
	name := (*p)(appID)
	if name == "" || name == adapter.GenericID || !slices.Contains(s.ListAdapters(), name) {
		return ""
	}
	return name
}

// gameSteamAppID is game's Steam app id as the launcher finds it, or "".
func (s *Service) gameSteamAppID(game *domain.Game) string {
	if l := s.launcher(); l != nil {
		return l.steamApp(game).AppID
	}
	if game.Launch != nil && game.Launch.SteamAppID != "" {
		return game.Launch.SteamAppID
	}
	if id := game.SourceIDs["steamworkshop"]; id != "" && strings.Trim(id, "0123456789") == "" {
		return id
	}
	return ""
}

// adapterSuggestionPass adds the adapter_suggested note to a verify run.
func (r *verifyRun) adapterSuggestionPass() {
	name := r.svc.SuggestedAdapter(r.game)
	if name == "" {
		return
	}
	r.finding(VerifyFinding{
		Status: VerifyStatusAdapterSuggested,
		Note: fmt.Sprintf("lmm's game catalog sets this game up with `adapter: %s`, and it uses %s; "+
			"the %s adapter knows how this game lays out and loads its mods", name, adapter.GenericID, name),
		Recorded:      adapter.GenericID,
		Effective:     name,
		FixableReason: fmt.Sprintf("lmm never changes a game's adapter by itself; to use it, run: lmm game edit %s --adapter %s", r.game.ID, name),
	}, VerifyEvent{Recorded: adapter.GenericID, Effective: name})
}
