package core

import (
	"context"
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

// SuggestedAdapter returns the adapter lmm's game catalog names for game
// when the game does not use it, or "".
//
// It only ever suggests for a game with no `adapter:` key that resolves to
// the identity: an explicit adapter, `generic-files` included, is the
// user's choice, and a derived one (BepInEx from a loader) already says
// what the game is. The catalog entry is the one the game's `catalog:`
// names, else the one for its Steam app id (launch.steam_app_id, a numeric
// steamworkshop mapping, then the Steam library holding the install path),
// and the suggestion must be an adapter this lmm registers.
func (s *Service) SuggestedAdapter(game *domain.Game) string {
	if game == nil || game.Adapter != "" || s.KnownGames() == nil {
		return ""
	}
	if s.AdapterName(game) != adapter.GenericID {
		return ""
	}
	k, ok := s.knownGameFor(game)
	if !ok || k.Adapter == "" || k.Adapter == adapter.GenericID || !slices.Contains(s.ListAdapters(), k.Adapter) {
		return ""
	}
	return k.Adapter
}

// gameSteamAppID is game's Steam app id as the launcher finds it, or "".
// What games.yaml states (launch.steam_app_id, then a numeric steamworkshop
// mapping) answers first; the launcher's library scan, which reads Steam's
// app manifests, is the last resort.
func (s *Service) gameSteamAppID(game *domain.Game) string {
	if game.Launch != nil && game.Launch.SteamAppID != "" {
		return game.Launch.SteamAppID
	}
	if id := game.SourceIDs["steamworkshop"]; id != "" && strings.Trim(id, "0123456789") == "" {
		return id
	}
	if l := s.launcher(); l != nil {
		return l.steamApp(game).AppID
	}
	return ""
}

// AdapterSwitchWarning says what moving game onto adapter `to` means for
// the mods already installed for it (review fix 1, #606): an adapter's
// archive layout applies when a mod is ingested, so every mod installed
// before the switch keeps the layout it was installed with, and only mods
// installed or updated afterwards get the new one. It returns "" when the
// switch changes nothing (the game already resolves to `to`) or when no mod
// is installed in any profile. One sentence, shared by every place that
// offers or makes the switch: verify's adapter_suggested note, the game
// page (GameStatus.SuggestedAdapterWarning) and `lmm game edit --adapter`.
func (s *Service) AdapterSwitchWarning(ctx context.Context, game *domain.Game, to string) (string, error) {
	if game == nil {
		return "", nil
	}
	after := *game
	after.Adapter = to
	if s.AdapterName(&after) == s.AdapterName(game) {
		return "", nil
	}
	// Between two layout-neutral adapters (#635: the identity and the
	// mod-type adapters) no installed mod is laid out or deployed any
	// differently: there is nothing to reinstall and nothing that moves.
	if s.layoutNeutral(game) && s.layoutNeutral(&after) {
		return "", nil
	}
	n, err := s.db.CountGameMods(ctx, game.ID)
	if err != nil || n == 0 {
		return "", err
	}
	routesRoots := false
	if a, err := s.AdapterFor(&after); err == nil {
		_, routesRoots = a.(adapter.RootRouter)
	}
	return adapterSwitchNote(n, s.AdapterName(&after), routesRoots), nil
}

// layoutNeutral reports whether game's adapter resolves and is
// adapter.LayoutNeutral.
func (s *Service) layoutNeutral(game *domain.Game) bool {
	a, err := s.AdapterFor(game)
	return err == nil && adapter.IsLayoutNeutral(a)
}

// adapterSwitchNote is AdapterSwitchWarning's sentence for n installed mods.
// An adapter that routes some files to the game folder (adapter.RootRouter,
// #629) does that at deploy time, not at install: the next deploy moves an
// installed mod's game-folder files there, so only the cache layout needs a
// reinstall.
func adapterSwitchNote(n int, to string, routesRoots bool) string {
	note := fmt.Sprintf("%d mod(s) already installed for this game keep the layout they were installed with; "+
		"only mods installed or updated after the switch get the %s adapter's layout. "+
		"To lay an installed mod out again, uninstall it (from every profile that has it) and install it again", n, to)
	if routesRoots {
		note += fmt.Sprintf(". Files the %s adapter places in the game folder itself rather than the mod folder "+
			"(a script extender's loader, an ENB) move there at the next deploy, with no reinstall", to)
	}
	return note
}

// adapterSuggestionPass adds the adapter_suggested note to a verify run.
func (r *verifyRun) adapterSuggestionPass() {
	name := r.svc.SuggestedAdapter(r.game)
	if name == "" {
		return
	}
	note := fmt.Sprintf("lmm's game catalog sets this game up with `adapter: %s`, and it uses %s; "+
		"the %s adapter knows how this game lays out and loads its mods", name, adapter.GenericID, name)
	suggested := *r.game
	suggested.Adapter = name
	if r.svc.layoutNeutral(&suggested) {
		// #635: an adapter that changes nothing for an untyped mod says what
		// it is for instead.
		note = fmt.Sprintf("lmm's game catalog sets this game up with `adapter: %s`, and it uses %s; "+
			"the %s adapter knows where this game's mods of a stated type (as a NexusMods collection records them) deploy, "+
			"and lays out and deploys every other mod as it is now", name, adapter.GenericID, name)
	}
	if warning, err := r.svc.AdapterSwitchWarning(r.ctx, r.game, name); err == nil && warning != "" {
		note += ". " + warning
	}
	r.finding(VerifyFinding{
		Status:        VerifyStatusAdapterSuggested,
		Note:          note,
		Recorded:      adapter.GenericID,
		Effective:     name,
		FixableReason: fmt.Sprintf("lmm never changes a game's adapter by itself; to use it, run: lmm game edit %s --adapter %s", r.game.ID, name),
	}, VerifyEvent{Recorded: adapter.GenericID, Effective: name})
}
