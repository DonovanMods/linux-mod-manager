// Package core: this file holds verify's LOADER tier (#359) - the pass that
// runs only for a game declaring a mod loader.
//
// It is population, not architecture: verify already has the tier vocabulary
// (VerifyTier) and the repair vocabulary (--fix), so this adds findings to an
// existing engine rather than a second one.
//
// Its shape follows from what lmm does and does not do about a loader: FOUR
// checks report and ONE repairs.
//
// The four that only report - loader missing, version drift, an incomplete
// bootstrap, and a loader that has never run - are about the loader
// INSTALLATION, which lmm deliberately does not install and whose Steam
// launch option it deliberately does not write (see loader_status.go). There
// is nothing for --fix to attempt, and each says so in the engine's own
// voice (notFixableLoader).
//
// The fifth, loaderPluginLinkCheck, is repairable and is the reason the tier
// is worth having beyond diagnostics: a mod whose files are all gone from
// BepInEx/plugins/ is invisible to the per-file walk (which asks about the
// CACHE) and to convergeDeployedFiles (which is remove-only), yet it is
// exactly the state that makes every plugin silently stop working. Its
// repair is the ordinary idempotent Installer.Install, not a bespoke
// re-link, so the profile's link method and the deployed-files bookkeeping
// stay the deploy path's own.
package core

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// notFixableLoader is the reason the tier's four REPORTING findings carry -
// loader missing, version drift, incomplete bootstrap, never ran: lmm
// deliberately does not install the loader or write the launch option, so
// there is no repair for --fix to attempt. loaderPluginLinkCheck, the fifth
// check, is repairable and carries loaderUnlinkedRefusal instead.
const notFixableLoader = "lmm does not install the mod loader or write Steam launch options, so there is nothing for --fix to do here - the remedy is the setup `lmm game show` prints"

// loaderPass reports what is wrong with a loader-declaring game's loader, in
// the order a user would work through it: is it there, is it the version you
// said, is its bootstrap the one you said, and did it actually RUN.
//
// The last question is the one worth having the tier for. Everything else
// says the files are in the right places; only BepInEx/LogOutput.log says the
// loader ran, and it is the only honest evidence available without launching
// the game. A user who pasted the launch option wrong otherwise finds out
// from a mod that mysteriously does nothing.
//
// A game that neither declares BepInEx nor has it installed runs none of
// this, so every other game's verify output is exactly what it was.
func (r *verifyRun) loaderPass(installedMods []domain.InstalledMod) {
	gate := bepinexGateFor(r.game)
	if !gate.Gated {
		return
	}

	// #424: the misplaced-deployment check asks about the archive LAYOUT
	// rules, which the gate turns on for a game whose BepInEx lmm can see
	// as well as one that declares it - and the undeclared game is exactly
	// where the misplaced deployments came from. Everything below it is
	// about the DECLARATION (is the installation the version, bootstrap and
	// runtime you said?), which a game that declares nothing has not made,
	// so those checks still run only for a declaring game.
	r.loaderMisplacedDeployCheck(installedMods)
	if !r.game.DeclaresBepInEx() {
		return
	}

	root := r.game.InstallPath
	name := loaderDisplayName(r.game.Loader.Kind)

	if !regularFileAt(root, bepinexPreloaderPath) {
		r.result.Issues++
		r.finding(VerifyFinding{
			Status: "loader_missing",
			Note: fmt.Sprintf("this game declares the %s loader, but %s is not in its install directory - no plugin will load until it is",
				name, bepinexPreloaderPath),
			FixableReason: notFixableLoader,
		}, VerifyEvent{})
		// Every check below asks about an installation that is not there.
		return
	}

	r.loaderVersionCheck(root, name)
	r.loaderBootstrapCheck(root, name)
	r.loaderRanCheck(root, name)
	r.loaderPluginLinkCheck(installedMods)
}

// loaderVersionCheck reports drift between the declared version and the one
// on disk. Reported, never repaired: choosing a BepInEx build is the hard
// part of installing it (a Windows pack is right for Proton and wrong for a
// native build; IL2CPP needs a bleeding-edge build that is not a release at
// all), and lmm getting it wrong leaves a game that silently loads nothing.
//
// An empty declared version means "do not check", and an installation whose
// version lmm cannot read reports nothing rather than guessing at drift.
func (r *verifyRun) loaderVersionCheck(root, name string) {
	declared := strings.TrimSpace(r.game.Loader.Version)
	if declared == "" {
		return
	}
	installed := installedLoaderVersion(root)
	if installed == "" || installed == declared {
		return
	}
	r.result.Issues++
	r.finding(VerifyFinding{
		Status:    "loader_version_mismatch",
		Recorded:  declared,
		Effective: installed,
		Note: fmt.Sprintf("this game declares %s %s, but the installation says %s - update the declaration with `lmm game edit %s --loader-version %s`, or install the version you meant",
			name, declared, installed, r.game.ID, installed),
		FixableReason: notFixableLoader,
	}, VerifyEvent{Recorded: declared, Effective: installed})
}

// loaderBootstrapCheck reports a bootstrap that does not match the declared
// mode - the single most common BepInEx-on-Linux mistake, and one nothing
// else catches: the game launches perfectly and loads nothing.
//
// It only fires for a DECLARED bootstrap. With none declared the report has
// no expectation to compare against, and loader_never_ran below already
// covers the outcome.
func (r *verifyRun) loaderBootstrapCheck(root, name string) {
	var want []string
	switch r.game.Loader.Bootstrap {
	case domain.LoaderBootstrapNative:
		want = []string{bepinexNativeScript, bepinexNativeDoorstop}
	case domain.LoaderBootstrapProton:
		want = []string{bepinexProtonProxy, bepinexProtonConfig}
	default:
		return
	}

	var missing []string
	for _, f := range want {
		if !regularFileAt(root, f) {
			missing = append(missing, f)
		}
	}
	if len(missing) == 0 {
		return
	}
	r.result.Issues++
	r.finding(VerifyFinding{
		Status: "loader_bootstrap_incomplete",
		Note: fmt.Sprintf("this game declares a %s bootstrap, which needs %s in the game directory - %s missing. The two bootstrap modes ship in DIFFERENT %s archives, so this usually means the wrong pack is installed",
			r.game.Loader.Bootstrap, strings.Join(want, " and "), strings.Join(missing, " and ")+" "+isAre(len(missing)), name),
		FixableReason: notFixableLoader,
	}, VerifyEvent{})
}

// loaderRanCheck is the honest "did it actually load?" question, answered
// from BepInEx's own log rather than from anything lmm arranged.
//
// Two outcomes: no log at all means the loader has never run, which is
// almost always a missing or wrong launch option; a log OLDER than the newest
// deployed plugin means it ran, but not since the current set of mods was
// deployed - so nothing here proves THIS set ever loaded.
func (r *verifyRun) loaderRanCheck(root, name string) {
	logPath := filepath.Join(root, filepath.FromSlash(bepinexLogPath))
	info, err := os.Stat(logPath)
	if err != nil {
		option := BepInExLaunchOption(loaderEffectiveBootstrap(r.game))
		note := fmt.Sprintf("%s is installed but has never written %s, so it has not run - the Steam launch option is the usual cause", name, bepinexLogPath)
		if option != "" {
			note += fmt.Sprintf(". Set this game's launch options to: %s", option)
		}
		r.result.Issues++
		r.finding(VerifyFinding{Status: "loader_never_ran", Note: note, FixableReason: notFixableLoader}, VerifyEvent{})
		return
	}

	newest, plugin := newestPluginDeploy(root)
	if plugin == "" || !newest.After(info.ModTime()) {
		return
	}
	r.result.Warnings++
	r.finding(VerifyFinding{
		Status: "loader_stale_log",
		Note: fmt.Sprintf("%s last ran at %s, before %s was deployed - launch the game once to confirm the current mods load",
			name, info.ModTime().UTC().Format(loaderTimeFormat), plugin),
		FixableReason: notFixableLoader,
	}, VerifyEvent{})
}

// loaderEffectiveBootstrap is the bootstrap the guidance follows for game:
// the declaration where it answers, the install directory's own markers
// otherwise - the same resolution LoaderStatus reports.
func loaderEffectiveBootstrap(game *domain.Game) domain.LoaderBootstrap {
	if game.Loader != nil && game.Loader.Bootstrap != domain.LoaderBootstrapUnknown {
		return game.Loader.Bootstrap
	}
	_, detected := DetectLoaderTarget(game.InstallPath)
	return detected
}

// isAre keeps the bootstrap finding's sentence grammatical for one file or
// two, which is the whole range it ever reports.
func isAre(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}

// regularFileAt reports whether root/rel (a slash-separated relative path) is
// a regular file.
func regularFileAt(root, rel string) bool {
	info, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
	return err == nil && info.Mode().IsRegular()
}

// loaderVersionLine matches the version BepInEx announces in the first lines
// of its own log, for an installation with no .doorstop_version file.
var loaderVersionLine = regexp.MustCompile(`BepInEx\s+v?(\d+(?:\.\d+)+)`)

// installedLoaderVersion reads the loader version actually installed under
// root, or "" when nothing on disk says.
//
// Two sources, in order of authority: the .doorstop_version file BepInEx's
// own archives ship at the game root, then the version BepInEx announces in
// its log. Neither requires parsing a PE assembly, and "" - "the disk does
// not say" - is a real answer that suppresses the drift check rather than
// inventing a mismatch.
func installedLoaderVersion(root string) string {
	if b, err := os.ReadFile(filepath.Join(root, ".doorstop_version")); err == nil {
		if v := strings.TrimSpace(string(b)); v != "" {
			return v
		}
	}
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(bepinexLogPath)))
	if err != nil {
		return ""
	}
	if m := loaderVersionLine.FindSubmatch(b); m != nil {
		return string(m[1])
	}
	return ""
}

// newestPluginDeploy returns the modification time of the most recently
// changed entry under BepInEx/plugins/ and its game-relative path, or a zero
// time and "" when there are no plugins.
//
// Lstat times, not Stat: a symlink deployment's own mtime is when lmm created
// the link, which is what "when was this deployed" means. Following it would
// report the cache file's time instead, which is when it was downloaded.
func newestPluginDeploy(root string) (time.Time, string) {
	pluginsDir := filepath.Join(root, "BepInEx", "plugins")
	var newest time.Time
	var newestRel string
	_ = filepath.WalkDir(pluginsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // an unreadable entry is not a verify failure
		}
		info, lerr := os.Lstat(path)
		if lerr != nil {
			return nil
		}
		if info.ModTime().After(newest) {
			rel, rerr := filepath.Rel(root, path)
			if rerr != nil {
				return nil
			}
			newest, newestRel = info.ModTime(), filepath.ToSlash(rel)
		}
		return nil
	})
	return newest, newestRel
}

// loaderPluginLinkCheck is the tier's ONE repairable check: an enabled mod
// whose plugin files are not actually in the game directory.
//
// It exists here rather than as a general check because the engine has no
// other one: the per-file walk asks whether the CACHE still holds a mod's
// files, and convergeDeployedFiles is remove-only by design (it reconciles
// rows whose mod is gone, never re-links a mod that is still installed). So
// "the mod is enabled, its cache entry is intact, and nothing is linked into
// BepInEx/plugins/" - a game directory a launcher update or a manual cleanup
// wiped - was invisible to verify, which for a loader game is precisely the
// state that makes every plugin silently stop working.
//
// --fix repairs it by re-deploying that mod through the ordinary installer,
// which is idempotent, so the repair is the same code path a `lmm deploy`
// would take rather than a bespoke re-link.
//
// A seeded BepInEx/config file is deliberately NOT part of the question: it
// is written once and then belongs to the user (#358), so its absence means
// they deleted it, not that a deploy failed.
func (r *verifyRun) loaderPluginLinkCheck(installedMods []domain.InstalledMod) {
	for i := range installedMods {
		mod := &installedMods[i]
		if err := r.ctx.Err(); err != nil {
			return
		}
		if !mod.Enabled || mod.External {
			continue
		}
		if r.opts.ModFilter != "" && mod.ID != r.opts.ModFilter {
			continue
		}
		missing := r.unlinkedLoaderFiles(mod)
		if len(missing) == 0 {
			continue
		}
		r.result.Issues++
		fixing := r.opts.Fix
		r.finding(VerifyFinding{
			ModID: mod.ID, ModName: mod.Name, Status: "loader_plugin_unlinked",
			Note: fmt.Sprintf("%d loader file(s) this mod provides are not in the game directory, starting with %s - nothing will load them",
				len(missing), missing[0]),
			Fixable:       !fixing,
			FixableReason: loaderUnlinkedRefusal(fixing),
		}, VerifyEvent{})
		if !fixing {
			continue
		}
		r.repairUnlinkedLoaderFiles(mod, len(missing))
	}
}

// loaderUnlinkedRefusal is loaderPluginLinkCheck's reason half, on
// staleCompileRefusal's terms: a plain run reports something --fix would
// repair, so on a --fix run the row is already past being actionable.
func loaderUnlinkedRefusal(fixing bool) string {
	if !fixing {
		return ""
	}
	return "this --fix run already re-deployed this mod"
}

// unlinkedLoaderFiles lists the mod's deployable BepInEx files that are not
// present at their deploy path, in cache-listing order.
//
// A cache entry that cannot be listed yields nothing: the per-file walk owns
// that failure and reporting it twice would be noise. The same tolerance
// covers a game whose adapter will not resolve (#353): adapterPass already
// reports that as its own skipped row, and answering this question through
// the identity routing - which is what every adapter U1 ships does anyway -
// is better than reporting nothing.
func (r *verifyRun) unlinkedLoaderFiles(mod *domain.InstalledMod) []string {
	gameAdapter, err := r.svc.AdapterFor(r.game)
	if err != nil {
		gameAdapter = adapter.Generic{}
	}
	files, err := deployableFiles(r.svc.GetGameCache(r.game), gameAdapter, r.game, mod.SourceID, mod.ID, mod.Version)
	if err != nil {
		return nil
	}
	var missing []string
	for _, f := range files {
		slash := filepath.ToSlash(f)
		if !strings.HasPrefix(slash, "BepInEx/") || isBepInExConfigMember(slash) {
			continue
		}
		if _, err := os.Lstat(filepath.Join(r.game.ModPath, f)); err != nil {
			missing = append(missing, slash)
		}
	}
	return missing
}

// repairUnlinkedLoaderFiles re-deploys mod, and resolves the row it just
// reported either way - a failed repair keeps the finding as an issue with
// the failure named, which is the convention every other repair in this
// engine follows.
func (r *verifyRun) repairUnlinkedLoaderFiles(mod *domain.InstalledMod, count int) {
	installer, err := r.svc.getInstallerForProfile(r.ctx, r.game, r.profile)
	if err == nil {
		err = installer.Install(r.ctx, r.game, &domain.Mod{
			ID: mod.ID, SourceID: mod.SourceID, Version: mod.Version, Name: mod.Name, GameID: r.game.ID,
		}, r.profile)
	}
	if err != nil {
		// The row's reason was written before the attempt ("this --fix run
		// already re-deployed this mod") and has just stopped being true.
		// Correct it in place rather than leaving the one sentence a user
		// reads asserting a re-deploy that did not happen - resolveLast's
		// own rule, applied to a repair that failed instead of one that
		// was refused.
		last := &r.result.Findings[len(r.result.Findings)-1]
		last.FixableReason = fmt.Sprintf("this --fix run could not re-deploy %s: %v", mod.Name, err)
		r.emitEv(VerifyEvent{Kind: VerifyEvRepairDetail, Detail: fmt.Sprintf("--fix could not re-deploy %s: %v", mod.Name, err)})
		return
	}
	r.result.Issues--
	r.resolveLast("fixed_loader_plugin_unlinked", fmt.Sprintf("re-deployed %d loader file(s)", count))
	r.emitEv(VerifyEvent{Kind: VerifyEvRepairDetail, Fixed: true, Detail: fmt.Sprintf("re-deployed %s", mod.Name)})
}
