// Package bepinex: this file holds the adapter's VERIFY pass (#359) - the
// checks on the loader INSTALLATION that internal/core cannot make, moved
// out of core's verify_loader.go in U3 (#413).
//
// The split follows the design's rule, "adapters report, core repairs"
// (§1, decision 6), and the line falls exactly where the evidence put it:
//
//	HERE, because only the adapter knows what BepInEx's own files look like
//	and what the declaration is supposed to match - is the preloader there,
//	is it the version games.yaml says, are the bootstrap files the ones the
//	declared mode needs. None is fixable: lmm deliberately does not install
//	the loader or choose its build (a Windows pack is right for Proton and
//	wrong for a native build; IL2CPP needs a bleeding-edge build that is not
//	a release at all), so each row is an instruction to the user and says so.
//
//	IN CORE, because they REPAIR: an enabled mod whose plugins are not
//	linked into the game directory, and a mod deployed outside BepInEx/
//	entirely (#424). Both re-deploy through the ordinary installer, which is
//	core's. Core also keeps the two questions that belong to the `loader:`
//	block's own report rather than to this adapter - did the loader ever run
//	(BepInEx/LogOutput.log), and is that log older than the newest deployed
//	plugin - because their remedy is the Steam launch option, which is
//	LoaderStatus's surface and is computed for every game, adapter or not.
package bepinex

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// Paths lmm reads to answer "is the loader actually installed, and is its
// bootstrap intact for the declared mode?" Every one is game-root-relative,
// which is the whole reason a BepInEx game's mod_path is its install path.
const (
	// PreloaderPath is the file whose presence means BepInEx is installed
	// at all. It is BepInEx's own entry point, so nothing else plausibly
	// puts it there.
	//
	// Exported because internal/core reads it for one question that is not
	// a verify check: which ADAPTER a game gets. A game with BepInEx
	// actually installed is a BepInEx game whatever games.yaml says (#424),
	// and that resolution has to happen before any adapter is in hand.
	PreloaderPath = dirName + "/core/BepInEx.Preloader.dll"
	// LogPath is written by the loader on every run, which makes it the
	// only honest "did it actually load?" signal available without
	// launching the game. Exported for the same reason as PreloaderPath:
	// core's own loader tier reads it.
	LogPath = dirName + "/LogOutput.log"
	// nativeScript and nativeDoorstop are the native Linux bootstrap:
	// run_bepinex.sh sets up LD_PRELOAD for libdoorstop.so.
	nativeScript   = "run_bepinex.sh"
	nativeDoorstop = "libdoorstop.so"
	// protonProxy and protonConfig are the Proton/Wine bootstrap: the
	// Windows winhttp.dll proxy plus its doorstop config.
	protonProxy  = "winhttp.dll"
	protonConfig = "doorstop_config.ini"
)

// notFixable is the reason every finding in this file carries. lmm
// deliberately does not install the mod loader or write Steam launch
// options, so there is no repair for --fix to attempt.
const notFixable = "lmm does not install the mod loader or write Steam launch options, so there is nothing for --fix to do here - the remedy is the setup `lmm game show` prints"

// Verify reports what is wrong with a BepInEx game's loader installation,
// in the order a user would work through it: is it there, is it the version
// you said, is its bootstrap the one you said.
//
// A game that does not DECLARE the loader gets none of this. That is not
// the same question as "is this a BepInEx game" - core resolves this adapter
// for a game whose install directory simply HAS BepInEx (#424), and every
// check below compares the installation against a declaration such a user
// has not made. They get the archive-layout rules, which is what they came
// for, and a notice naming the command that makes the declaration (see
// UndeclaredNotice).
//
// Read-only, and cheap: a handful of stats plus at most two small file
// reads. It writes nothing to the game directory, which is the Verifier
// contract.
func (*Adapter) Verify(ctx context.Context, req adapter.VerifyRequest) ([]adapter.Finding, error) {
	game := req.Game
	if game == nil || !game.DeclaresBepInEx() {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	root := game.InstallPath
	if !regularFileAt(root, PreloaderPath) {
		return []adapter.Finding{{
			Status: "loader_missing",
			Note: fmt.Sprintf("this game declares the BepInEx loader, but %s is not in its install directory - no plugin will load until it is",
				PreloaderPath),
			FixableReason: notFixable,
		}}, nil
	}

	var findings []adapter.Finding
	if f, ok := versionDrift(game, root); ok {
		findings = append(findings, f)
	}
	if f, ok := bootstrapGap(game, root); ok {
		findings = append(findings, f)
	}
	return findings, nil
}

// versionDrift reports a disagreement between the declared loader version
// and the one on disk. Reported, never repaired: choosing a BepInEx build
// is the hard part of installing it, and lmm getting it wrong leaves a game
// that silently loads nothing.
//
// An empty declared version means "do not check", and an installation whose
// version lmm cannot read reports nothing rather than guessing at drift.
func versionDrift(game *domain.Game, root string) (adapter.Finding, bool) {
	declared := strings.TrimSpace(game.Loader.Version)
	if declared == "" {
		return adapter.Finding{}, false
	}
	installed := installedVersion(root)
	if installed == "" || installed == declared {
		return adapter.Finding{}, false
	}
	return adapter.Finding{
		Status:    "loader_version_mismatch",
		Recorded:  declared,
		Effective: installed,
		Note: fmt.Sprintf("this game declares BepInEx %s, but the installation says %s - update the declaration with `lmm game edit %s --loader-version %s`, or install the version you meant",
			declared, installed, game.ID, installed),
		FixableReason: notFixable,
	}, true
}

// bootstrapGap reports a bootstrap that does not match the declared mode -
// the single most common BepInEx-on-Linux mistake, and one nothing else
// catches: the game launches perfectly and loads nothing.
//
// It only fires for a DECLARED bootstrap. With none declared the report has
// no expectation to compare against, and core's own loader_never_ran check
// already covers the outcome.
func bootstrapGap(game *domain.Game, root string) (adapter.Finding, bool) {
	var want []string
	switch game.Loader.Bootstrap {
	case domain.LoaderBootstrapNative:
		want = []string{nativeScript, nativeDoorstop}
	case domain.LoaderBootstrapProton:
		want = []string{protonProxy, protonConfig}
	default:
		return adapter.Finding{}, false
	}

	var missing []string
	for _, f := range want {
		if !regularFileAt(root, f) {
			missing = append(missing, f)
		}
	}
	if len(missing) == 0 {
		return adapter.Finding{}, false
	}
	return adapter.Finding{
		Status: "loader_bootstrap_incomplete",
		Note: fmt.Sprintf("this game declares a %s bootstrap, which needs %s in the game directory - %s missing. The two bootstrap modes ship in DIFFERENT BepInEx archives, so this usually means the wrong pack is installed",
			game.Loader.Bootstrap, strings.Join(want, " and "), strings.Join(missing, " and ")+" "+isAre(len(missing))),
		FixableReason: notFixable,
	}, true
}

// isAre keeps the bootstrap finding's sentence grammatical for one file or
// two, which is the whole range it ever reports.
func isAre(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}

// regularFileAt reports whether root/rel (a slash-separated relative path)
// is a regular file.
func regularFileAt(root, rel string) bool {
	info, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
	return err == nil && info.Mode().IsRegular()
}

// versionLine matches the version BepInEx announces in the first lines of
// its own log, for an installation with no .doorstop_version file.
var versionLine = regexp.MustCompile(`BepInEx\s+v?(\d+(?:\.\d+)+)`)

// installedVersion reads the loader version actually installed under root,
// or "" when nothing on disk says.
//
// Two sources, in order of authority: the .doorstop_version file BepInEx's
// own archives ship at the game root, then the version BepInEx announces in
// its log. Neither requires parsing a PE assembly, and "" - "the disk does
// not say" - is a real answer that suppresses the drift check rather than
// inventing a mismatch.
func installedVersion(root string) string {
	if b, err := os.ReadFile(filepath.Join(root, ".doorstop_version")); err == nil {
		if v := strings.TrimSpace(string(b)); v != "" {
			return v
		}
	}
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(LogPath)))
	if err != nil {
		return ""
	}
	if m := versionLine.FindSubmatch(b); m != nil {
		return string(m[1])
	}
	return ""
}
