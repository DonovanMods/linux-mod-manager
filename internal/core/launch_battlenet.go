package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
)

// battleNetClients are where a prefix's Battle.net client may be, in the
// order they are tried: the launcher stub omarchy-launch-battlenet and
// Lutris' installer start, then the client itself - both take
// --exec="launch <code>" - under each Program Files.
var battleNetClients = []string{
	"drive_c/Program Files (x86)/Battle.net/Battle.net Launcher.exe",
	"drive_c/Program Files (x86)/Battle.net/Battle.net.exe",
	"drive_c/Program Files/Battle.net/Battle.net Launcher.exe",
	"drive_c/Program Files/Battle.net/Battle.net.exe",
}

// umuGameID is umu-launcher's GAMEID for Battle.net: it selects umu's
// protonfixes for the client, and it is the id omarchy-launch-battlenet
// uses, so a game lmm starts gets the same window class (steam_app_battlenet)
// the user's window rules already match.
const umuGameID = "umu-battlenet"

// BattleNetLaunchMethod is the `battlenet` launch method (#545): ask the
// Battle.net client in launch.battlenet.prefix to start the game. The
// composition root registers it (app.RegisterLaunchers); it applies to a
// game whose games.yaml has a `launch.battlenet:` block, which detection
// prefills for a Battle.net game.
//
// A Proton prefix - one umu-launcher or Steam made, with a pfx folder and a
// version file - runs through umu-run, with WINEPREFIX, GAMEID,
// PROTON_VERB and (when set) PROTONPATH in the game's environment, the way
// omarchy-launch-battlenet runs it. Any other prefix runs through wine,
// with WINEPREFIX. launch.battlenet.runner overrides the choice; without
// it, a Proton prefix with no umu-run on PATH is refused rather than run
// with a plain wine, which would rewrite the prefix.
//
// With launch.battlenet.product - Battle.net's launch code, "WoW" for
// World of Warcraft - the client is asked to launch the game; without it,
// the client opens and the user presses Play there. Either way Battle.net
// only accepts the request (Requests).
func BattleNetLaunchMethod() LaunchMethod {
	return LaunchMethod{Name: domain.LaunchViaBattleNet, Requests: true, Resolve: resolveBattleNetLaunch}
}

// resolveBattleNetLaunch is BattleNetLaunchMethod's Resolve.
func resolveBattleNetLaunch(req *LaunchRequest) ([]string, bool, error) {
	if req.Game.Launch == nil || req.Game.Launch.BattleNet == nil {
		return nil, false, nil
	}
	bn := req.Game.Launch.BattleNet
	prefix := config.ExpandPath(bn.Prefix)
	exe := ""
	for _, rel := range battleNetClients {
		if p := filepath.Join(prefix, filepath.FromSlash(rel)); isRegularFile(p) {
			exe = p
			break
		}
	}
	if exe == "" {
		return nil, true, fmt.Errorf("launch.battlenet.prefix %s has no Battle.net client in it (looked for %s); is Battle.net installed in that prefix?", prefix, strings.Join(battleNetClients, ", "))
	}
	args := []string{exe}
	if bn.Product != "" {
		args = append(args, "--exec=launch "+bn.Product)
	}

	protonRoot, isProton := battleNetProtonRoot(prefix)
	runner := bn.Runner
	if runner == "" {
		runner = domain.BattleNetRunnerWine
		if isProton {
			runner = domain.BattleNetRunnerUmu
		}
	}
	if runner == domain.BattleNetRunnerWine {
		wine, err := req.LookPath("wine")
		if err != nil {
			return nil, true, errors.New("launch.battlenet runs Battle.net with wine, but wine is not on PATH")
		}
		req.Env = []string{"WINEPREFIX=" + prefix}
		return append([]string{wine}, args...), true, nil
	}
	umu, err := req.LookPath("umu-run")
	if err != nil {
		return nil, true, fmt.Errorf("%s is a Proton prefix, which lmm runs with umu-run (umu-launcher), but umu-run is not on PATH; install umu-launcher, or set launch.battlenet.runner: wine to run it with wine", prefix)
	}
	if !isProton {
		protonRoot = prefix
	}
	req.Env = []string{"WINEPREFIX=" + protonRoot, "GAMEID=" + umuGameID, "PROTON_VERB=run"}
	if bn.Proton != "" {
		req.Env = append(req.Env, "PROTONPATH="+bn.Proton)
	}
	return append([]string{umu}, args...), true, nil
}

// battleNetProtonRoot reports whether prefix is a Proton prefix, and the
// directory umu-run takes as its WINEPREFIX: the one holding Proton's pfx
// folder and version file - prefix itself for one umu made (pfx -> .),
// its parent for Steam's compatdata/<id>/pfx.
func battleNetProtonRoot(prefix string) (string, bool) {
	for _, dir := range []string{prefix, filepath.Dir(prefix)} {
		if isRegularFile(filepath.Join(dir, "version")) {
			if _, err := os.Stat(filepath.Join(dir, "pfx")); err == nil {
				return dir, true
			}
		}
	}
	return "", false
}

// isRegularFile reports whether path is a regular file (through symlinks).
func isRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
