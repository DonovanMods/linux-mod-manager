// Package core: this file holds design decision 11's warning (#353; #413
// review F5) - a game that HAS BepInEx but resolves to a different adapter.
//
// `loader:` describes the installation and `adapter:` decides what lmm does
// about an archive, so a game can carry both with the adapter pointing
// elsewhere: an explicit `adapter: generic-files`, a `deploy_mode: compile`
// that selects icarus, or a mod_path off the game root that keeps the
// identity (modPathIsGameRoot). The design keeps that a WARNING rather than
// an error, because a half-configured game is a real state. What it may not
// be is silent: the BepInEx rules never run for such a game, so a
// Thunderstore package's manifest.json and icon.png deploy with its plugins,
// its plugins stay wherever the archive put them, and its BepInEx/config
// files are linked from the shared mod cache instead of copied once.
//
// WHERE it is said follows one rule (#413 re-review M1): a warning must be
// silenceable by the fix it suggests, and a persistent flag is for a
// contradiction, not for a deliberate choice.
//
//	PER ARCHIVE (loaderBypassNote, on archiveLayout's answer): in every
//	bypass case, for an archive the BepInEx rules would actually have laid
//	out - exactly where the harm happens. It reaches the import plan both
//	frontends show before committing, and a download's event stream. Its
//	remedy is only the one that makes lmm lay such an archive out, because
//	that is the only thing that silences it.
//
//	PERSISTENTLY - at load (AdapterConfigWarnings), on the loader report
//	(LoaderStatus.Warnings: `lmm game show`, GET /api/v1/games/{id}, the web
//	loader panel), after a game edit (AdapterConfigWarning) and as verify's
//	loader_adapter_ignored WARNING row, which the web Health count includes -
//	only for a CONTRADICTION: a `loader:` block the adapter ignores, or an
//	installed BepInEx that an IMPLICIT adapter ignores. Its remedies are
//	both ways out: lay BepInEx archives out, or state the choice.
//
//	NOWHERE persistent for an explicit `adapter:` on a game whose BepInEx is
//	merely installed: the key IS the user's acknowledged choice, and a flag
//	nobody can clear is the kind that teaches users to ignore warnings.
//	verify reports no row for it at all, rather than a note-severity one -
//	the web Health card lists every non-ok row, so a note would be the same
//	permanent entry under another name.
//
// "Has BepInEx" is hasBepInEx, the same declared-or-installed test the
// bepinex derivation makes, so the warning and the derivation cannot
// disagree about which games are loader games.
package core

import (
	"fmt"
	"slices"
	"strings"

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
// that ships no bepinex adapter (there is nothing such a game is missing),
// and for a game every flow refuses (#413 re-review L2): "lmm deploys BepInEx
// archives exactly as packaged" is false for a game lmm deploys nothing to,
// and AdapterFor's refusal already names the fix.
//
// Reads disk only for a game that declares neither the loader nor an
// adapter that settles the question, and then one stat.
func (s *Service) bepinexBypass(game *domain.Game) (loaderBypass, bool) {
	if game == nil || !s.adapterRegistry().Has(bepinexAdapterID) {
		return loaderBypass{}, false
	}
	name := s.AdapterName(game)
	if name == bepinexAdapterID || !hasBepInEx(game) {
		return loaderBypass{}, false
	}
	if _, err := s.adapterForName(game, name); err != nil {
		return loaderBypass{}, false
	}
	return loaderBypass{game: game, adapterID: name, declared: game.DeclaresBepInEx()}, true
}

// persistent reports whether the bypass is a contradiction the persistent
// surfaces flag: the game declares the loader, or nobody chose its adapter.
func (b loaderBypass) persistent() bool {
	return b.declared || b.game.Adapter == ""
}

// AdapterConfigWarnings is design decision 11's load-time warning: one
// sentence for every configured game whose `loader:` block its adapter
// ignores, except the games named in except. internal/app prints them when
// it opens a Service, and except is how a command that reports a game's
// warning itself - `lmm game show`, `lmm verify`, `lmm game edit` - keeps
// the sentence from reaching the user twice (#413 re-review M2).
//
// It reads no disk, because it runs on every lmm invocation: the other
// persistent case - an installed BepInEx an implicit adapter ignores - needs
// a stat per game to find, and is flagged on the loader report and by
// verify instead.
func (s *Service) AdapterConfigWarnings(except ...string) []string {
	var out []string
	for _, game := range s.gamesSnapshot() {
		if !game.DeclaresBepInEx() || slices.Contains(except, game.ID) {
			continue
		}
		if w := s.adapterConfigWarning(game); w != "" {
			out = append(out, w)
		}
	}
	return out
}

// AdapterConfigWarning is the persistent warning for one game, or "" when
// its configuration contradicts nothing - the sentence a frontend prints
// after writing a game, so the edit that creates a contradiction says so at
// that moment and the edit that removes one says nothing. Unlike
// AdapterConfigWarnings it reads the game's install directory, so it covers
// an installed-but-undeclared BepInEx too.
func (s *Service) AdapterConfigWarning(gameID string) string {
	game, ok := s.game(gameID)
	if !ok {
		return ""
	}
	return s.adapterConfigWarning(game)
}

// adapterConfigWarning is AdapterConfigWarning for a resolved game.
func (s *Service) adapterConfigWarning(game *domain.Game) string {
	b, ok := s.bepinexBypass(game)
	if !ok || !b.persistent() {
		return ""
	}
	return b.configWarning()
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
	subject := fmt.Sprintf("game %q's adapter", game.ID)
	return fmt.Sprintf("this archive is laid out for BepInEx (%s), but %s, so lmm deploys it exactly as packaged: %s %s.",
		would.Kind, b.adapterPhrase(subject), b.consequence(), b.enableRemedy())
}

// configWarning is the game-level sentence the persistent surfaces say.
func (b loaderBypass) configWarning() string {
	opening := fmt.Sprintf("BepInEx is installed in game %q's directory, but %s, so lmm does not act on it and deploys BepInEx archives exactly as packaged",
		b.game.ID, b.adapterPhrase("its adapter"))
	if b.declared {
		opening = fmt.Sprintf("game %q declares the BepInEx loader, but %s, so lmm ignores the loader block and deploys BepInEx archives exactly as packaged",
			b.game.ID, b.adapterPhrase("its adapter"))
	}
	return fmt.Sprintf("%s: %s %s; %s.", opening, b.consequence(), b.enableRemedy(), b.acknowledgement())
}

// adapterPhrase says "<subject> is <adapter>", and why when games.yaml does
// not name it: subject is "its adapter" inside a sentence already about the
// game, or "game X's adapter" in one about an archive.
func (b loaderBypass) adapterPhrase(subject string) string {
	phrase := fmt.Sprintf("%s is %q", subject, b.adapterID)
	switch {
	case b.game.Adapter != "":
		return phrase
	case b.game.DeployMode == domain.DeployCompile:
		return phrase + ", which `deploy_mode: compile` selects"
	default:
		// The one other way a loader game misses the derivation.
		return phrase + fmt.Sprintf(", because its mod_path (%s) is not its install path and a BepInEx layout is relative to the game root", b.game.ModPath)
	}
}

// consequence is what "exactly as packaged" costs, in the terms a user can
// check in their game directory. The second form is for a mod_path off the
// game root, where an archive's own BepInEx/ tree is the thing misplaced.
func (b loaderBypass) consequence() string {
	if modPathIsGameRoot(b.game) {
		return "package metadata such as manifest.json lands in the game directory, a plugin is not moved under BepInEx/, and a BepInEx/config file is linked from the shared mod cache instead of copied once, so editing it edits every profile's copy."
	}
	return fmt.Sprintf("every path in an archive is joined onto %s, so package metadata such as manifest.json lands there too, and an archive's own BepInEx/ tree - its BepInEx/config files included - is nested under it, where BepInEx does not read it.", b.game.ModPath)
}

// enableRemedy is the fix that makes lmm lay BepInEx archives out for this
// game - the only fix that silences the per-archive warning, so the only one
// that warning offers. It is every change between this configuration and
// one the bepinex derivation selects, in the order they can be made.
//
// A compile game cannot simply switch to bepinex - `deploy_mode: compile`
// needs an adapter that compiles, and AdapterFor refuses one that does not -
// so its remedy is conditional on the game not compiling its mods.
func (b loaderBypass) enableRemedy() string {
	id := b.game.ID
	if modPathIsGameRoot(b.game) && b.game.Adapter != "" && b.game.DeployMode != domain.DeployCompile {
		return fmt.Sprintf("Run `lmm game edit %s --adapter bepinex` to have lmm lay BepInEx archives out", id)
	}
	lead := bepinexEnableLead
	if b.game.DeployMode == domain.DeployCompile {
		lead = fmt.Sprintf("`deploy_mode: compile` needs an adapter that compiles, which bepinex is not; if %s does not compile its mods, then to have lmm lay BepInEx archives out, ", id)
	}
	return lead + strings.Join(bepinexEnableSteps(b.game), ", then ")
}

// bepinexEnableLead opens a remedy built from bepinexEnableSteps.
const bepinexEnableLead = "To have lmm lay BepInEx archives out, "

// bepinexEnableSteps lists, in the only order that works, the changes that
// take game to a configuration the bepinex adapter lays out: the steps
// enableRemedy offers, and the ones AdapterFor's refusal of an explicit
// `adapter: bepinex` off the game root gives (#413 final review F4) - one
// list, so the two can never disagree about the order.
//
// The purge comes FIRST whenever the mod_path moves. lmm records a
// deployed file relative to the mod_path it was deployed under, so a purge
// after the move looks for every file in the wrong place and leaves the
// real ones behind, live and unrecorded - and a BepInEx/-rooted archive's
// copy under the old mod_path is a second, nested BepInEx/ tree that
// BepInEx scans for plugins.
func bepinexEnableSteps(game *domain.Game) []string {
	id := game.ID
	compile := game.DeployMode == domain.DeployCompile
	explicit := game.Adapter != ""
	gameRoot := modPathIsGameRoot(game)

	var steps []string
	if !gameRoot {
		steps = append(steps, fmt.Sprintf("run `lmm purge --game %s`", id))
	}
	var yamlEdits []string
	if compile {
		drop := "remove `deploy_mode: compile`"
		if explicit {
			drop += " and the `adapter:` key"
		}
		yamlEdits = append(yamlEdits, drop)
	}
	where := " from games.yaml"
	if !gameRoot {
		yamlEdits = append(yamlEdits, fmt.Sprintf("set its mod_path to %s", game.InstallPath))
		where = " in games.yaml"
	}
	if len(yamlEdits) > 0 {
		steps = append(steps, strings.Join(yamlEdits, " and ")+where)
	}
	if !compile && explicit && game.Adapter != bepinexAdapterID {
		// After the mod_path edit: AdapterFor refuses bepinex off the game
		// root, so the other order fails.
		steps = append(steps, fmt.Sprintf("run `lmm game edit %s --adapter bepinex`", id))
	}
	if !gameRoot {
		// What was imported for the old mod_path is cached exactly as
		// packaged; verify's misplaced-deployment repair re-lays it out
		// once the game resolves to bepinex.
		steps = append(steps, fmt.Sprintf("run `lmm deploy --game %s` and `lmm verify --fix --game %s`, which moves what is already imported under BepInEx/", id, id))
	}
	return steps
}

// acknowledgement is the other way out of a contradiction: keep the game on
// its adapter and say so, which is an explicit `adapter:` key and no
// `loader:` block - the one bypass the persistent surfaces do not flag.
// It does not silence the per-archive warning, so only the persistent
// sentence offers it.
func (b loaderBypass) acknowledgement() string {
	id := b.game.ID
	unload := fmt.Sprintf("remove the `loader:` block (`lmm game edit %s --loader \"\"`)", id)
	pin := fmt.Sprintf("pin the adapter (`lmm game edit %s --adapter %s`)", id, b.adapterID)
	switch {
	case b.game.Adapter != "":
		return fmt.Sprintf("or, if %q is the adapter you meant, %s", b.adapterID, unload)
	case b.declared:
		return fmt.Sprintf("or, to keep this game on %q, %s and %s", b.adapterID, unload, pin)
	default:
		return fmt.Sprintf("or, to keep this game on %q, %s", b.adapterID, pin)
	}
}
