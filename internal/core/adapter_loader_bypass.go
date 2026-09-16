// Package core: this file holds design decision 11's warning (#353; #413
// review F5) - a game that HAS BepInEx but resolves to a different adapter.
//
// `loader:` describes the installation and `adapter:` decides what lmm does
// about an archive, so a game can legitimately carry both with the adapter
// pointing elsewhere: an explicit `adapter: generic-files`, or a
// `deploy_mode: compile` that selects icarus. The design keeps that a
// WARNING rather than an error, because a half-configured game is a real
// state. What it may not be is silent: the BepInEx rules never run for such
// a game, so a Thunderstore package's manifest.json and icon.png deploy into
// the game directory, its plugins stay wherever the archive put them, and
// its BepInEx/config files are linked from the shared mod cache instead of
// copied once.
//
// The warning is said in three places, each where a user can act on it:
//
//	at load (AdapterConfigWarnings, printed by internal/app on every open)
//	for a game whose `loader:` block is the thing being ignored;
//
//	on the loader report (LoaderStatus.Warnings) that `lmm game show`, GET
//	/api/v1/games/{id} and the web loader panel render;
//
//	on the archive's own layout (archiveLayout), for an archive the BepInEx
//	rules would actually have laid out - so it reaches the import plan both
//	frontends show before committing, and a download's event stream.
//
// "Has BepInEx" is hasBepInEx, the same declared-or-installed test that
// resolves the bepinex adapter when nothing overrides it, so the warning and
// the derivation cannot disagree about which games are loader games.
package core

import (
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// loaderBypass is a game whose BepInEx lmm is not acting on.
type loaderBypass struct {
	game *domain.Game
	// adapterID is the adapter the game resolves to instead of bepinex.
	adapterID string
	// declared reports that the game declares the loader, as opposed to
	// merely having it installed.
	declared bool
}

// bepinexBypass reports whether game has BepInEx - declared, or installed -
// while resolving to an adapter other than bepinex. It is false in a build
// that ships no bepinex adapter: there is nothing such a game is missing.
//
// Reads disk only for a game that neither declares the loader nor resolves
// to bepinex, and then one stat.
func (s *Service) bepinexBypass(game *domain.Game) (loaderBypass, bool) {
	if game == nil || !s.adapterRegistry().Has(bepinexAdapterID) {
		return loaderBypass{}, false
	}
	name := s.AdapterName(game)
	if name == bepinexAdapterID || !hasBepInEx(game) {
		return loaderBypass{}, false
	}
	return loaderBypass{game: game, adapterID: name, declared: game.DeclaresBepInEx()}, true
}

// AdapterConfigWarnings is design decision 11's load-time warning: one
// sentence for every configured game whose `loader:` block its adapter
// ignores. internal/app prints them when it opens a Service.
//
// It reads no disk, because it runs on every lmm invocation: a game with
// BepInEx installed but not declared has no loader block to ignore, and is
// told at the two places where it matters instead - the loader report and
// the archive's own layout.
func (s *Service) AdapterConfigWarnings() []string {
	var out []string
	for _, game := range s.gamesSnapshot() {
		if !game.DeclaresBepInEx() {
			continue
		}
		if b, ok := s.bepinexBypass(game); ok {
			out = append(out, b.configWarning())
		}
	}
	return out
}

// loaderBypassNote returns the per-archive warning for members, or "" when
// there is nothing to say: the game is not bypassing BepInEx, or the BepInEx
// rules would not have touched this archive anyway (an asset pack into the
// same game has nothing BepInEx-shaped about it).
//
// It asks the bepinex adapter for the layout it WOULD have produced. That is
// a read-only question of an adapter about a game that does have BepInEx,
// which is within the adapter's contract; nothing it answers is applied.
func (s *Service) loaderBypassNote(game *domain.Game, modName string, members []string) string {
	b, ok := s.bepinexBypass(game)
	if !ok {
		return ""
	}
	loader, ok := s.adapterRegistry().Get(bepinexAdapterID)
	if !ok {
		return ""
	}
	would, err := loader.NormalizeArchive(adapter.NormalizeRequest{Game: game, ModName: modName, Members: members})
	if err != nil || !would.Applies() {
		return ""
	}
	return fmt.Sprintf("this archive is laid out for BepInEx (%s), but game %q %s, so lmm deploys it exactly as packaged: %s %s",
		would.Kind, game.ID, b.adapterPhrase(), bypassConsequence, b.remedy())
}

// configWarning is the game-level sentence: the load-time warning and the
// loader report's.
func (b loaderBypass) configWarning() string {
	if b.declared {
		return fmt.Sprintf("game %q declares the BepInEx loader, but %s, so lmm ignores the loader block and deploys BepInEx archives exactly as packaged: %s %s",
			b.game.ID, b.adapterPhrase(), bypassConsequence, b.remedy())
	}
	return fmt.Sprintf("BepInEx is installed in game %q's directory, but %s, so lmm does not act on it and deploys BepInEx archives exactly as packaged: %s %s",
		b.game.ID, b.adapterPhrase(), bypassConsequence, b.remedy())
}

// bypassConsequence is what "exactly as packaged" costs, in the terms a
// user can check in their game directory.
const bypassConsequence = "package metadata such as manifest.json lands in the game directory, a plugin is not moved under BepInEx/, and a BepInEx/config file is linked from the shared mod cache instead of copied once, so editing it edits every profile's copy."

// adapterPhrase names the adapter the game resolves to, and why when games.yaml
// does not say so itself.
func (b loaderBypass) adapterPhrase() string {
	if b.game.Adapter == "" {
		return fmt.Sprintf("its adapter is %q, which `deploy_mode: compile` selects", b.adapterID)
	}
	return fmt.Sprintf("its adapter is %q", b.adapterID)
}

// remedy is the fix that applies to this configuration. A compile game
// cannot simply switch to bepinex - `deploy_mode: compile` needs an adapter
// that compiles, and AdapterFor refuses one that does not - so its fix is
// one key or the other.
func (b loaderBypass) remedy() string {
	id := b.game.ID
	if b.game.DeployMode == domain.DeployCompile {
		drop := "remove `deploy_mode: compile`"
		if b.game.Adapter != "" {
			drop += " and the `adapter:` key"
		}
		msg := fmt.Sprintf("A `deploy_mode: compile` game needs an adapter that compiles, which bepinex is not: if %s does not compile its mods, %s", id, drop)
		if b.declared {
			msg += "; if it does, remove the `loader:` block"
		}
		return msg + "."
	}
	msg := fmt.Sprintf("Run `lmm game edit %s --adapter bepinex` to have lmm lay BepInEx archives out", id)
	if b.declared {
		msg += fmt.Sprintf(", or remove the `loader:` block if %q is the adapter you meant", b.adapterID)
	}
	return msg + "."
}
