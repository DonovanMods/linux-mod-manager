// Package bepinex: this file holds the BOOTSTRAP GUIDANCE (#359) - the
// adapter.Guide capability, which is lmm's answer to "what do I still have
// to do to make this game load mods?"
//
// The boundary it respects is the spike's single most important finding
// (docs/plans/2026-09-09-bepinex-spike.md §4): lmm TELLS the user precisely
// and then CHECKS the result, and never does it for them. Both bootstrap
// modes are edits to state lmm does not own - Steam's localconfig.vdf launch
// options, or a Proton prefix's user.reg - whose format is undocumented, has
// changed, and whose bad write loses every launch option for every game in
// the account. run_bepinex.sh itself carries a workaround for an OPEN
// UnityDoorstop issue about Steam's bootstrapper and LD_PRELOAD, so the
// mechanism is not even stable enough to automate blind.
//
// The exact launch string is deliberately NOT repeated here. It is computed
// once, for every game, by internal/core's LoaderStatus - which resolves the
// EFFECTIVE bootstrap (the declaration where it answers, the install
// directory's own Unity markers otherwise) and is the document `lmm game
// show` and the web loader panel both render. A second copy in this package
// would be a second thing to get wrong, and getting it wrong is worse than
// printing nothing: a game with the wrong option launches normally and loads
// no mods, with no error anywhere to read. So the guidance names that
// command instead.
package bepinex

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// Guidance returns the setup notes a BepInEx game still needs, or nil when
// there is nothing to say - which is the healthy case, and the one every
// correctly configured game is in.
//
// Three states, in the order a user meets them:
//
//	the loader is not installed, so nothing will load at all;
//	it is installed but the game does not DECLARE it, so lmm is acting on
//	what it found rather than on the configuration (#424);
//	it is installed and has never written its log, so it has not run - which
//	on Linux is almost always the launch option.
//
// Read-only: three stats, no writes.
func (*Adapter) Guidance(g *domain.Game) []adapter.GuidanceNote {
	if g == nil {
		return nil
	}
	var notes []adapter.GuidanceNote
	if !regularFileAt(g.InstallPath, domain.BepInExPreloaderPath) {
		return append(notes, adapter.GuidanceNote{
			Title: "BepInEx is not installed in this game's directory",
			Body: fmt.Sprintf("Install it yourself - a native Linux build needs the BepInEx_linux_x64 archive from BepInEx's own GitHub releases, while a Proton/Wine game needs the Windows pack (winhttp.dll plus doorstop_config.ini). lmm does not choose or download it, because the wrong build leaves a game that silently loads nothing. `lmm game show %s` reports which one this game looks like it needs.",
				g.ID),
		})
	}
	if !g.DeclaresBepInEx() {
		notes = append(notes, adapter.GuidanceNote{
			Title: "BepInEx is installed here but not declared",
			Body:  undeclaredNotice(g),
		})
	}
	if _, err := os.Stat(filepath.Join(g.InstallPath, filepath.FromSlash(domain.BepInExLogPath))); err != nil {
		notes = append(notes, adapter.GuidanceNote{
			Title: "BepInEx has not run yet",
			Body: fmt.Sprintf("It has never written %s, so nothing it was given has loaded. On Linux that is almost always the Steam launch option: `lmm game show %s` prints the exact string to paste for this game's bootstrap mode.",
				domain.BepInExLogPath, g.ID),
		})
	}
	return notes
}
