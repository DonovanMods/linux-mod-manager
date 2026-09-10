// Package core: this file holds the loader BOOTSTRAP detection and the
// launch-option guidance (#359 unit 3) - lmm's answer to "how do I actually
// make BepInEx load?", which is to tell the user precisely and then check the
// result, never to do it for them.
//
// That boundary is the spike's single most important finding (docs/plans/
// 2026-09-09-bepinex-spike.md §4). Both bootstrap modes are edits to state
// lmm does not own: Steam's localconfig.vdf launch options, or a Proton
// prefix's user.reg. Those files must be edited with the client closed, their
// format is undocumented and has changed, and a bad write loses every launch
// option for every game in the account - a failure that would surface as
// "Steam ate my settings", not as "lmm has a bug". run_bepinex.sh itself
// carries a workaround for an OPEN UnityDoorstop issue about Steam's
// bootstrapper and LD_PRELOAD, so the mechanism is not even stable enough to
// automate blind.
//
// So: lmm DETECTS which mode a game needs, PRINTS the exact string to paste,
// and VERIFIES afterwards that the loader ran (verify_loader.go). It writes
// no Steam configuration and no Proton prefix, ever.
package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// BepInEx paths lmm reads to answer "is the loader actually installed, and
// is its bootstrap intact for the declared mode?" Every one is
// game-root-relative, which is the whole reason a BepInEx game's mod_path is
// its install path.
const (
	// bepinexPreloaderPath is the file whose presence means BepInEx is
	// installed at all. It is BepInEx's own entry point, so nothing else
	// plausibly puts it there.
	bepinexPreloaderPath = "BepInEx/core/BepInEx.Preloader.dll"
	// bepinexLogPath is written by the loader on every run, which makes it
	// the only honest "did it actually load?" signal available without
	// launching the game.
	bepinexLogPath = "BepInEx/LogOutput.log"
	// bepinexNativeScript and bepinexNativeDoorstop are the native Linux
	// bootstrap: run_bepinex.sh sets up LD_PRELOAD for libdoorstop.so.
	bepinexNativeScript   = "run_bepinex.sh"
	bepinexNativeDoorstop = "libdoorstop.so"
	// bepinexProtonProxy and bepinexProtonConfig are the Proton/Wine
	// bootstrap: the Windows winhttp.dll proxy plus its doorstop config.
	bepinexProtonProxy  = "winhttp.dll"
	bepinexProtonConfig = "doorstop_config.ini"
)

// The two Steam launch options, verbatim. They are the entire user-facing
// output of this file, and getting one wrong is worse than printing nothing:
// a game with the wrong option launches normally and loads no mods, with no
// error anywhere to read.
const (
	// BepInExLaunchOptionNative runs BepInEx's own bootstrap script with the
	// command Steam would otherwise have run. run_bepinex.sh carries an
	// explicit Steam special case, which is why the script - not a manual
	// LD_PRELOAD - is the sanctioned form.
	BepInExLaunchOptionNative = "./run_bepinex.sh %command%"
	// BepInExLaunchOptionProton makes Wine load the winhttp proxy DLL from
	// the game directory ahead of its own builtin. It is the community
	// shorthand for the winecfg library override BepInEx's Proton guide
	// walks through by hand.
	BepInExLaunchOptionProton = `WINEDLLOVERRIDES="winhttp=n,b" %command%`
)

// BepInExLaunchOption is the exact Steam launch option for bootstrap, or ""
// when the bootstrap is not answered.
//
// Empty is a real answer, not a gap to fill with a default: the two modes
// need different BepInEx archives and different options, and printing the
// wrong one leaves a user certain they configured the game correctly.
func BepInExLaunchOption(bootstrap domain.LoaderBootstrap) string {
	switch bootstrap {
	case domain.LoaderBootstrapNative:
		return BepInExLaunchOptionNative
	case domain.LoaderBootstrapProton:
		return BepInExLaunchOptionProton
	default:
		return ""
	}
}

// DetectLoaderTarget answers "Mono or IL2CPP" and "native Linux build or
// Proton" from what is on disk under installPath, without launching anything
// and without reading any Steam configuration.
//
// The Steam scan's contribution to this question is the install path itself
// (#206 put it in games.yaml); everything below is read from that directory:
//
//   - RUNTIME: <Game>_Data/il2cpp_data/ is IL2CPP,
//     <Game>_Data/Managed/Assembly-CSharp.dll is Mono. Both are Unity's own
//     layout, and a game has exactly one of them.
//   - BOOTSTRAP: UnityPlayer.so (or a <name>.x86_64 launcher) is a native
//     Linux build; UnityPlayer.dll (or a .exe) is a WINDOWS build, which on
//     Linux runs under Proton or Wine - so it needs the winhttp proxy rather
//     than run_bepinex.sh.
//
// Two markers can disagree, and the tie is broken by EVIDENCE rather than by
// whichever name os.ReadDir happened to return first (which made lexical
// sort decide which BepInEx build a user was told to install):
//
//  1. UnityPlayer.so / UnityPlayer.dll win. They ARE Unity's runtime, so
//     their presence is a statement about the build; .x86_64 and .exe are
//     ordinary extensions that anything may carry.
//  2. Otherwise the launcher extensions decide. Every marker here is
//     matched case-insensitively, .x86_64 as much as .exe: these are
//     filenames out of a depot, and a rule that turns on one character is
//     not a rule.
//  3. NATIVE wins a genuine tie at either level. A depot shipping both
//     builds is one whose Linux build is what actually runs on Linux, and
//     run_bepinex.sh is also the recoverable mistake of the two: it fails
//     visibly, while the winhttp override on a native build silently loads
//     nothing.
//
// Either answer may be Unknown, which is the honest result for a directory
// with no markers, a game that is not Unity, or a path that does not exist -
// never a guess, because a guess here sends the user to the wrong BepInEx
// build and the wrong launch option.
//
// Exported because both frontends want it for a game they are ABOUT to
// configure, before there is a declaration to read.
func DetectLoaderTarget(installPath string) (domain.LoaderRuntime, domain.LoaderBootstrap) {
	entries, err := os.ReadDir(installPath)
	if err != nil {
		return domain.LoaderRuntimeUnknown, domain.LoaderBootstrapUnknown
	}

	runtime := domain.LoaderRuntimeUnknown
	// The whole directory is read before the bootstrap is decided, so the
	// answer does not depend on directory order.
	var unityNative, unityWindows, launcherNative, launcherWindows bool
	for _, e := range entries {
		name := e.Name()
		switch {
		case e.IsDir() && strings.HasSuffix(name, "_Data"):
			if runtime == domain.LoaderRuntimeUnknown {
				runtime = detectUnityRuntime(filepath.Join(installPath, name))
			}
		case strings.EqualFold(name, "UnityPlayer.so"):
			unityNative = true
		case strings.EqualFold(name, "UnityPlayer.dll"):
			unityWindows = true
		case strings.EqualFold(filepath.Ext(name), ".x86_64"), strings.EqualFold(filepath.Ext(name), ".x86"):
			launcherNative = true
		case strings.EqualFold(filepath.Ext(name), ".exe"):
			launcherWindows = true
		}
	}

	bootstrap := domain.LoaderBootstrapUnknown
	switch {
	case unityNative:
		bootstrap = domain.LoaderBootstrapNative
	case unityWindows:
		bootstrap = domain.LoaderBootstrapProton
	case launcherNative:
		bootstrap = domain.LoaderBootstrapNative
	case launcherWindows:
		bootstrap = domain.LoaderBootstrapProton
	}
	return runtime, bootstrap
}

// detectUnityRuntime reads the scripting backend off one <Game>_Data
// directory. IL2CPP is tested first because an IL2CPP build can still carry a
// Managed/ directory with stub assemblies, while a Mono build never carries
// il2cpp_data.
func detectUnityRuntime(dataDir string) domain.LoaderRuntime {
	if info, err := os.Stat(filepath.Join(dataDir, "il2cpp_data")); err == nil && info.IsDir() {
		return domain.LoaderRuntimeIL2CPP
	}
	if info, err := os.Stat(filepath.Join(dataDir, "Managed", "Assembly-CSharp.dll")); err == nil && info.Mode().IsRegular() {
		return domain.LoaderRuntimeMono
	}
	return domain.LoaderRuntimeUnknown
}

// LoaderStatus is the loader document `lmm game show` prints and the web
// game page renders: what the game DECLARES, what the disk SAYS, which of
// the two the guidance follows, the exact launch option for it, and whether
// the loader is installed at all.
//
// It carries both the declared and the detected answer rather than resolving
// them into one, because a disagreement is information: a user who declared
// `native` for a game that ships UnityPlayer.dll has almost certainly
// installed the wrong BepInEx pack, and the report says so (Warnings) instead
// of quietly printing the option their declaration asked for.
type LoaderStatus struct {
	// GameID is the game this report is about.
	GameID string `json:"game_id"`
	// Declared is the game's `loader:` block, or nil when it declares none.
	// A nil declaration still gets a full report - which build and which
	// launch option the game WOULD need is exactly what a user setting one
	// up wants to read.
	Declared *domain.GameLoader `json:"declared,omitzero"`
	// DetectedRuntime and DetectedBootstrap are what DetectLoaderTarget read
	// off the install directory, Unknown when the markers do not say.
	DetectedRuntime   domain.LoaderRuntime   `json:"detected_runtime,omitempty"`
	DetectedBootstrap domain.LoaderBootstrap `json:"detected_bootstrap,omitempty"`
	// EffectiveRuntime and EffectiveBootstrap are what the guidance follows:
	// the declaration where it answers, the detection otherwise.
	EffectiveRuntime   domain.LoaderRuntime   `json:"effective_runtime,omitempty"`
	EffectiveBootstrap domain.LoaderBootstrap `json:"effective_bootstrap,omitempty"`
	// LaunchOption is the exact string to paste into Steam's launch options
	// for EffectiveBootstrap, or empty when the bootstrap is unanswered.
	//
	// lmm never writes it anywhere. See this file's package comment.
	LaunchOption string `json:"launch_option,omitempty"`
	// Installed reports that BepInEx's preloader is present in the game
	// directory - the difference between "configured" and "actually there".
	Installed bool `json:"installed"`
	// LoadedAt is the modification time of BepInEx/LogOutput.log, as an RFC
	// 3339 string, or empty when there is no log. The loader writes it on
	// every run, so it is the only honest evidence the loader RAN without
	// launching the game.
	LoadedAt string `json:"loaded_at,omitempty"`
	// Warnings are the disagreements and gaps worth saying out loud: a
	// declaration the disk contradicts, or a bootstrap nothing answered.
	Warnings []string `json:"warnings,omitempty"`
}

// LoaderStatus builds the loader report for gameID. Read-only: it takes no
// mutation gate, stats a handful of paths and writes nothing.
//
// An unknown game is domain.ErrGameNotFound, the 404 every other
// game-scoped surface answers with.
func (s *Service) LoaderStatus(_ context.Context, gameID string) (*LoaderStatus, error) {
	game, ok := s.game(gameID)
	if !ok {
		return nil, domain.ErrGameNotFound
	}

	status := &LoaderStatus{GameID: game.ID, Declared: game.Loader}
	status.DetectedRuntime, status.DetectedBootstrap = DetectLoaderTarget(game.InstallPath)

	status.EffectiveRuntime, status.EffectiveBootstrap = status.DetectedRuntime, status.DetectedBootstrap
	if game.Loader != nil {
		if game.Loader.Runtime != domain.LoaderRuntimeUnknown {
			status.EffectiveRuntime = game.Loader.Runtime
		}
		if game.Loader.Bootstrap != domain.LoaderBootstrapUnknown {
			status.EffectiveBootstrap = game.Loader.Bootstrap
		}
	}
	status.LaunchOption = BepInExLaunchOption(status.EffectiveBootstrap)

	if info, err := os.Stat(filepath.Join(game.InstallPath, filepath.FromSlash(bepinexPreloaderPath))); err == nil && info.Mode().IsRegular() {
		status.Installed = true
	}
	if info, err := os.Stat(filepath.Join(game.InstallPath, filepath.FromSlash(bepinexLogPath))); err == nil {
		status.LoadedAt = info.ModTime().UTC().Format(loaderTimeFormat)
	}

	status.Warnings = loaderStatusWarnings(status)
	return status, nil
}

// loaderTimeFormat is RFC 3339 to the second - the format every other lmm
// document uses for a timestamp a human reads.
const loaderTimeFormat = "2006-01-02T15:04:05Z"

// loaderStatusWarnings names the gaps and contradictions in a report.
//
// A declaration that contradicts the disk is the one worth shouting about: it
// means the user is following guidance for a bootstrap their game does not
// use, and both halves of that (the BepInEx pack they downloaded and the
// launch option they pasted) are wrong together.
func loaderStatusWarnings(status *LoaderStatus) []string {
	var warnings []string
	declared := status.Declared
	if declared != nil {
		if declared.Bootstrap != domain.LoaderBootstrapUnknown &&
			status.DetectedBootstrap != domain.LoaderBootstrapUnknown &&
			declared.Bootstrap != status.DetectedBootstrap {
			warnings = append(warnings, fmt.Sprintf(
				"this game declares a %s bootstrap, but its install directory looks like a %s one - check that the BepInEx pack you installed matches, because the launch option and the archive have to agree",
				declared.Bootstrap, status.DetectedBootstrap))
		}
		if declared.Runtime != domain.LoaderRuntimeUnknown &&
			status.DetectedRuntime != domain.LoaderRuntimeUnknown &&
			declared.Runtime != status.DetectedRuntime {
			warnings = append(warnings, fmt.Sprintf(
				"this game declares the %s runtime, but its install directory looks like %s - IL2CPP needs a BepInEx 6 bleeding-edge build, and Mono needs a BepInEx 5 release",
				declared.Runtime, status.DetectedRuntime))
		}
	}
	// Only for a game something says is loader-relevant. Appended
	// unconditionally, this told the owner of an Unreal game that neither
	// has nor needs a mod loader to go and configure one, on every `lmm
	// game show`.
	if status.Relevant() && status.EffectiveBootstrap == domain.LoaderBootstrapUnknown {
		warnings = append(warnings, "lmm could not tell whether this is a native Linux build or a Proton one, so it has no launch option to give you; set it with `lmm game edit <game> --loader-bootstrap native|proton`")
	}
	return warnings
}

// Relevant reports whether anything about this game says a mod loader is
// part of the conversation: the game declares one, BepInEx is installed in
// its directory or has left a log there, or the directory carries a marker
// DetectLoaderTarget recognised.
//
// It decides whether a surface shows the loader report at all, and it lives
// here rather than in each frontend so `lmm game show` and the web game
// page's warning cannot disagree. False means "not a loader game as far as
// anything can tell": say nothing, rather than print a section of unknowns
// followed by BepInEx setup advice.
//
// Every piece of evidence LoaderStatus gathers counts, not just the two
// DETECTED enums (re-review R3). A game whose disk says nothing is relevant
// the moment it DECLARES a loader - that is exactly when the unanswered
// questions are worth asking - and a game that declares nothing is relevant
// the moment BepInEx is actually THERE, which is the undeclared, mid-setup
// user the report has the most to say to.
func (l *LoaderStatus) Relevant() bool {
	return l != nil && (l.Declared != nil ||
		l.Installed || l.LoadedAt != "" ||
		l.DetectedRuntime != domain.LoaderRuntimeUnknown ||
		l.DetectedBootstrap != domain.LoaderBootstrapUnknown)
}

// GameDetail is `lmm game show <id>`'s document and the web game page's
// hydrate: one configured game's row, plus the loader report for it.
//
// It embeds GameListEntry rather than nesting it, matching GameSummary and
// GameStatus (Ruling 15's shape for a game document), so a consumer that
// already decodes a game-list row needs no second decoder. The embedded
// domain.Game carries the DECLARATION under "loader"; LoaderStatus is the
// separate question of what is actually on disk, so it lands under
// "loader_status" rather than shadowing it.
type GameDetail struct {
	GameListEntry
	// Loader is the loader report. Always present - a game that declares no
	// loader still gets one, because "here is the build and the launch
	// option you would need" is exactly what someone setting one up reads.
	Loader *LoaderStatus `json:"loader_status,omitzero"`
}

// GameDetail returns the full document for gameID: its `lmm game list` row
// and its loader report. Read-only, so it takes no mutation gate.
//
// An unknown game is domain.ErrGameNotFound, the 404 every other
// game-scoped surface answers with.
func (s *Service) GameDetail(ctx context.Context, gameID string) (*GameDetail, error) {
	game, ok := s.game(gameID)
	if !ok {
		return nil, domain.ErrGameNotFound
	}
	defaultGame, err := s.DefaultGame(ctx)
	if err != nil {
		return nil, err
	}
	status, err := s.LoaderStatus(ctx, gameID)
	if err != nil {
		return nil, err
	}
	return &GameDetail{GameListEntry: newGameListEntry(game, defaultGame), Loader: status}, nil
}
