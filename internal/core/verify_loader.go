// Package core: this file holds what is left of verify's LOADER tier (#359)
// after U3 (#413) moved the reporting half into
// internal/adapter/bepinex.Verify: the two checks that REPAIR, plus the two
// that belong to the `loader:` block's own report.
//
// The split is the design's own rule - adapters report, core repairs (§1,
// decision 6) - and the line falls where the evidence put it:
//
//	IN THE ADAPTER, because only it knows what the loader's own files look
//	like and what the declaration is meant to match: is the preloader there,
//	is it the version games.yaml says, are the bootstrap files the ones the
//	declared mode needs. None of the three is fixable, and each says so.
//
//	HERE, because they REPAIR: loaderPluginLinkCheck (a mod whose files are
//	all gone from BepInEx/plugins/ is invisible to the per-file walk, which
//	asks about the CACHE, and to convergeDeployedFiles, which is remove-only
//	- yet it is exactly the state that makes every plugin silently stop
//	working) and loaderMisplacedDeployCheck (#424, verify_loader_layout.go).
//	Both re-deploy through the ordinary idempotent Installer.Install, so the
//	profile's link method and the deployed-files bookkeeping stay the deploy
//	path's own.
//
//	ALSO HERE, because their remedy is the Steam launch option: "did the
//	loader ever run", and "is its log older than the newest deployed
//	plugin". The exact launch string is computed once, for every game, by
//	LoaderStatus (loader_status.go) - which resolves the EFFECTIVE bootstrap
//	from the declaration or the install directory's own Unity markers, and
//	which the design keeps on domain.Game rather than on an adapter
//	(decision 11). A second copy of that string in an adapter would be a
//	second thing to get wrong, and getting it wrong leaves a game that
//	launches perfectly and loads nothing.
package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// notFixableLoader is the reason this file's two REPORTING findings carry -
// loader never ran, and a log older than the newest deploy: lmm deliberately
// does not install the loader or write the launch option, so there is no
// repair for --fix to attempt. The adapter's own findings carry the same
// sentence for the same reason (internal/adapter/bepinex/verify.go's
// notFixable), because the user is in the same position whichever half
// noticed. loaderPluginLinkCheck, which IS repairable, carries
// loaderUnlinkedRefusal instead.
const notFixableLoader = "lmm does not install the mod loader or write Steam launch options, so there is nothing for --fix to do here - the remedy is the setup `lmm game show` prints"

// loaderPass runs the loader tier's core-owned half: the two repairs, and
// the "did it actually RUN?" question.
//
// That last one is the one worth having the tier for. Everything the
// adapter reports says the files are in the right places; only
// BepInEx/LogOutput.log says the loader ran, and it is the only honest
// evidence available without launching the game. A user who pasted the
// launch option wrong otherwise finds out from a mod that mysteriously does
// nothing.
//
// A game that neither declares a loader nor has one installed runs none of
// this, so every other game's verify output is exactly what it was. The
// gate is hasBepInEx - the same two-source test the bepinex derivation
// makes (Service.AdapterName), so the tier and the adapter cannot disagree
// about whether this is a loader game. A loader game the derivation still
// does not resolve to bepinex (an explicit adapter, a compile game, a
// mod_path off the game root) is the bypass the first check below reports.
func (r *verifyRun) loaderPass(installedMods []domain.InstalledMod) {
	if !hasBepInEx(r.game) {
		return
	}

	// Design decision 11 (#413 review F5): a game with BepInEx whose
	// adapter is another one gets none of the adapter's rules or checks,
	// and verify is where a user looks to find out why. A WARNING - the
	// state is allowed - and not fixable, because the fix is a games.yaml
	// edit lmm does not make on the user's behalf. Only for a
	// contradiction: an explicit adapter on a game whose BepInEx is merely
	// installed is the user's choice, and gets no row at all
	// (adapter_loader_bypass.go says why not a note). The rest of the tier
	// still runs: the loader is there, so whether it ran is still worth
	// asking.
	if w := r.svc.adapterConfigWarning(r.game); w != "" {
		r.result.Warnings++
		r.finding(VerifyFinding{
			Status:        "loader_adapter_ignored",
			Note:          w,
			FixableReason: "the fix is an edit to this game's configuration, which --fix does not make",
		}, VerifyEvent{})
	}

	// #424: the misplaced-deployment check asks about the archive LAYOUT
	// rules, which apply to a game whose BepInEx lmm can see as well as one
	// that declares it - and the undeclared game is exactly where the
	// misplaced deployments came from. But only to a game that RESOLVES to
	// bepinex: its question is where that adapter's layout puts a file, and
	// its repair re-lays the cache entry out through it. Asked of a game on
	// another adapter it had no layout to compare against - a v1 game whose
	// mod_path is BepInEx/plugins records every plugin relative to that
	// directory, so each one read as "outside BepInEx/" (#413 re-review
	// P-b) - and the bypass row above already says why nothing is laid out.
	//
	// Everything below it is about the DECLARATION, which a game that
	// declares nothing has not made, so those checks still run only for a
	// declaring game.
	if r.svc.AdapterName(r.game) == bepinexAdapterID {
		r.loaderMisplacedDeployCheck(installedMods)
	}
	if !r.game.DeclaresBepInEx() {
		return
	}

	root := r.game.InstallPath
	name := loaderDisplayName(r.game.Loader.Kind)

	// An installation that is not there: the adapter has already reported
	// it (loader_missing), and every question below asks about files it
	// would have had to write.
	if !regularFileAt(root, domain.BepInExPreloaderPath) {
		return
	}

	r.loaderRanCheck(root, name)
	r.loaderPluginLinkCheck(installedMods)
}

// loaderRanCheck is the honest "did it actually load?" question, answered
// from BepInEx's own log rather than from anything lmm arranged.
//
// Two outcomes: no log at all means the loader has never run, which is
// almost always a missing or wrong launch option; a log OLDER than the newest
// deployed plugin means it ran, but not since the current set of mods was
// deployed - so nothing here proves THIS set ever loaded.
func (r *verifyRun) loaderRanCheck(root, name string) {
	logPath := filepath.Join(root, filepath.FromSlash(domain.BepInExLogPath))
	info, err := os.Stat(logPath)
	if err != nil {
		option := BepInExLaunchOption(loaderEffectiveBootstrap(r.game))
		note := fmt.Sprintf("%s is installed but has never written %s, so it has not run - the Steam launch option is the usual cause", name, domain.BepInExLogPath)
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

// regularFileAt reports whether root/rel (a slash-separated relative path) is
// a regular file.
func regularFileAt(root, rel string) bool {
	info, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
	return err == nil && info.Mode().IsRegular()
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
	// deployableFiles has already asked the adapter's FileRouter, so a
	// seeded config is not in this list at all - which is the answer this
	// check wants (#358): it was written once and then became the user's,
	// so its absence is a choice, not a failed deploy.
	files, err := deployableFiles(r.svc.GetGameCache(r.game), gameAdapter, r.game, mod.SourceID, mod.ID, mod.Version)
	if err != nil {
		return nil
	}
	var missing []string
	for _, f := range files {
		slash := filepath.ToSlash(f)
		if !strings.HasPrefix(slash, loaderContentRoot+"/") {
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
