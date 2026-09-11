// Package core: this file holds verify's check for a mod DEPLOYED OUTSIDE
// the loader directory (#424) - the loader tier's answer to the installs
// that happened before lmm recognised their archive layout.
//
// It is the other half of the plugin-folder fix. Teaching the normaliser
// shape F stops the bug happening again; it does nothing at all for a
// plugin already sitting in the game root, and nothing else in the engine
// can see one: the per-file walk asks whether the CACHE still holds the
// mod's files (it does), convergeDeployedFiles asks whether any installed
// mod still provides each deployed path (one does - this one), and
// loaderPluginLinkCheck asks whether the mod's BepInEx files are linked
// (it has none). Every check passes, and the plugin loads nothing.
//
// Only the loader tier can ask the question, because only it knows where a
// plugin BELONGS. And the repair has to start at the cache entry, because
// for a BepInEx game the cache entry's layout IS the game directory's
// layout: a plain re-deploy reads the same cache and puts the file straight
// back. So --fix re-lays the entry out through the normaliser and then
// re-deploys through the ordinary installer - and when the entry is a shape
// the normaliser will not rewrite, it says so instead of claiming a repair
// it cannot make.
package core

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// loaderRelayoutRemedy is the reason a row carries when --fix cannot help:
// the cache entry is not a layout the normaliser rewrites, so re-deploying
// it would reproduce exactly the deployment being complained about.
//
// It names what the USER has to change, not what lmm could run again
// (#424 review, finding 2). An archive whose root mixes BepInEx/ with a
// plugin folder is placed this way BY the layout rules, so "re-import the
// archive so the rules run over a fresh copy" re-runs the identical ingest,
// reproduces the identical deployment and leaves the row exactly where it
// was - a remedy that is a dead end reads as worse than none at all.
const loaderRelayoutRemedy = "lmm cannot place this mod's files under BepInEx/ on its own, and re-importing the same archive lays it out the same way - move its files under BepInEx/plugins/ inside the archive (or put them there by hand) and re-import it"

// loaderMisplacedDeployCheck reports an installed mod whose recorded deploy
// paths do not sit under BepInEx/ on a game that has BepInEx.
//
// The recorded rows are the question, not the disk: deployed_files is what
// `lmm mod files` prints and what the owner read when they found Jotunn in
// the game root, and a row is the only durable evidence that lmm put a file
// somewhere rather than the user doing it by hand.
//
// An ASSEMBLY outside BepInEx/ is the test, not merely a file. For a
// BepInEx game mod_path IS the game root, so a mod that legitimately writes
// into the game's own directories - a texture pack under
// <Game>_Data/ - has every one of its files "outside BepInEx/" and is not
// misplaced at all. A `.dll` is the difference: BepInEx loads assemblies
// and only from its own directories, so one sitting anywhere else is a
// plugin nothing will load, whatever else the mod ships.
func (r *verifyRun) loaderMisplacedDeployCheck(installedMods []domain.InstalledMod) {
	for i := range installedMods {
		mod := &installedMods[i]
		if err := r.ctx.Err(); err != nil {
			return
		}
		if mod.External {
			continue
		}
		if r.opts.ModFilter != "" && mod.ID != r.opts.ModFilter {
			continue
		}
		misplaced := r.misplacedLoaderRows(mod)
		if len(misplaced) == 0 {
			continue
		}

		// Asked BEFORE the row is written, so its one sentence is true when
		// the user reads it - the same reason loaderPluginLinkCheck resolves
		// its reason before reporting rather than after.
		repairable := r.cacheRelayoutApplies(mod)
		fixing := r.opts.Fix
		r.result.Issues++
		r.finding(VerifyFinding{
			ModID: mod.ID, ModName: mod.Name, Status: "loader_deployed_outside_loader",
			Note: fmt.Sprintf("%d file(s) this mod deploys sit outside BepInEx/, including an assembly - starting with %s - so this game's loader reads none of them",
				len(misplaced), misplaced[0]),
			Fixable:       repairable && !fixing,
			FixableReason: loaderRelayoutRefusal(repairable, fixing),
		}, VerifyEvent{})
		if !fixing || !repairable {
			continue
		}
		r.repairMisplacedLoaderDeploy(mod, len(misplaced))
	}
}

// loaderRelayoutRefusal is the row's reason half: an entry the normaliser
// will not rewrite names the remedy that does work, and an entry it will
// says the repair has already run (staleCompileRefusal's convention - by
// the time a --fix row is read, it is past being actionable).
func loaderRelayoutRefusal(repairable, fixing bool) string {
	if !repairable {
		return loaderRelayoutRemedy
	}
	if fixing {
		return "this --fix run already re-laid out and re-deployed this mod"
	}
	return ""
}

// misplacedLoaderRows lists the mod's recorded deploy paths that are not
// under BepInEx/, slash-separated and in row order - or nothing at all when
// none of them is an assembly, which is loaderMisplacedDeployCheck's test.
//
// All of them once one is, not just the assemblies: a plugin's .pdb, .xml
// and asset bundle are as misplaced as the .dll beside them and move with
// it, so the count the finding reports is the count the repair moves.
//
// A row under a directory the GAME itself owns is not misplaced at all and
// never counts - not even towards the assembly test (#424 review, finding
// 1). <Game>_Data/Managed/Assembly-CSharp.dll is an assembly outside
// BepInEx/ and is exactly where it belongs: the game's own engine reads
// that directory and BepInEx never will. It is bepinexGameOwnedRoot that
// decides, the same rule and the same disk test shape F is gated on, so
// this check and the re-layout it offers cannot disagree about what the
// game owns.
//
// A row-lookup failure yields nothing: verify does not turn a DB read error
// into a layout accusation.
func (r *verifyRun) misplacedLoaderRows(mod *domain.InstalledMod) []string {
	rows, err := r.svc.GetDeployedFilesForMod(r.ctx, r.game.ID, r.profile, mod.SourceID, mod.ID)
	if err != nil {
		return nil
	}
	slashed := make([]string, 0, len(rows))
	for _, p := range rows {
		slashed = append(slashed, filepath.ToSlash(p))
	}
	// Memoised per root: the rule walks the game's directory, and a mod
	// with many rows under one root would otherwise walk it once per row.
	gameOwned := make(map[string]bool)
	var misplaced []string
	assembly := false
	for _, slash := range slashed {
		if strings.HasPrefix(slash, bepinexDirName+"/") {
			continue
		}
		root, _, nested := strings.Cut(slash, "/")
		if nested {
			owned, asked := gameOwned[root]
			if !asked {
				owned = bepinexGameOwnedRoot(r.game.InstallPath, root, slashed)
				gameOwned[root] = owned
			}
			if owned {
				continue
			}
		}
		if strings.EqualFold(path.Ext(slash), ".dll") {
			assembly = true
		}
		misplaced = append(misplaced, slash)
	}
	if !assembly {
		return nil
	}
	return misplaced
}

// cacheRelayoutApplies is the repair's precondition, asked as a pure DRY RUN
// over the cache entry's own member list: would the normaliser rewrite this
// entry, and would everything it produced land under BepInEx/?
//
// Both halves matter. "Would rewrite" rules out the entry a re-deploy could
// only reproduce; "lands under BepInEx/" rules out a rewrite that moves
// files without answering the complaint. Together they are what lets --fix
// promise something it can deliver.
//
// It reads the same member list normalizeBepInExTree will (relativeFileMembers),
// so the classification and the mutation cannot disagree about what is in
// the entry.
func (r *verifyRun) cacheRelayoutApplies(mod *domain.InstalledMod) bool {
	members, err := relativeFileMembers(r.cacheEntryPath(mod))
	if err != nil || len(members) == 0 {
		return false
	}
	slash := make([]string, len(members))
	for i, m := range members {
		slash[i] = filepath.ToSlash(m)
	}
	layout, err := bepinexNormalise(slash, mod.Name, true, r.game.InstallPath)
	if err != nil || !layout.Applies() {
		return false
	}
	moved := false
	for _, m := range slash {
		dest, kept := layout.Rewrite(m)
		if !kept {
			continue
		}
		if !strings.HasPrefix(dest, bepinexDirName+"/") {
			return false
		}
		if dest != m {
			moved = true
		}
	}
	return moved
}

// cacheEntryPath is the directory holding mod's cached files for this game.
func (r *verifyRun) cacheEntryPath(mod *domain.InstalledMod) string {
	return r.svc.GetGameCache(r.game).ModPath(r.game.ID, mod.SourceID, mod.ID, mod.Version)
}

// repairMisplacedLoaderDeploy re-lays out the cache entry and re-deploys the
// mod in every profile that has it.
//
// The order is undeploy, re-lay out, re-deploy - not the relinkDeployedRow
// shape of undeploy-then-install around one unchanged entry - because the
// thing being repaired is the entry itself. Undeploying FIRST is what lets
// the ordinary installer see the paths it is removing (it reads them from
// the cache), so the misplaced files and their rows go away through the
// normal deploy path, and the directories they emptied are pruned by the
// prune that already belongs to an uninstall (#415).
//
// Every profile, not just this one: the cache entry is shared by every
// profile of the game holding this version, so moving its files out from
// under a sibling's deployment would leave that profile's rows pointing at
// paths nothing provides any more. The profile list is read FIRST and a
// failure to read it refuses the repair outright, because the alternative
// is mutating the entry without knowing who else is standing on it.
func (r *verifyRun) repairMisplacedLoaderDeploy(mod *domain.InstalledMod, count int) {
	holders, err := r.profilesDeploying(mod)
	if err != nil {
		r.failRelayout(mod, fmt.Sprintf("could not enumerate this game's profiles: %v", err))
		return
	}

	// Undeploy every holder while the entry still names the paths they
	// actually deployed. An Uninstall failure is not fatal, on
	// DeployProfile's own precedent: a path it could not remove surfaces
	// again as the re-deploy's own failure, or as a stale_deployment row.
	for _, holder := range holders {
		if err := holder.installer.Uninstall(r.ctx, r.game, &holder.mod.Mod, holder.profile); err != nil {
			r.emitEv(VerifyEvent{Kind: VerifyEvRepairDetail,
				Detail: fmt.Sprintf("undeploying %s from profile %s: %v", mod.Name, holder.profile, err)})
		}
	}

	if err := r.relayoutCacheEntry(mod); err != nil {
		// The entry is back as it was (relayoutCacheEntry's own undo), so
		// putting every holder back is putting back exactly what was there.
		r.redeployHolders(holders)
		r.failRelayout(mod, err.Error())
		return
	}

	var failures []string
	for _, holder := range holders {
		if err := holder.installer.Install(r.ctx, r.game, &holder.mod.Mod, holder.profile); err != nil {
			failures = append(failures, fmt.Sprintf("%s (%v)", holder.profile, err))
			continue
		}
		r.recordHolderLinkMethod(holder)
	}
	if len(failures) > 0 {
		r.failRelayout(mod, "re-deploy failed for profile(s) "+strings.Join(failures, ", "))
		return
	}

	r.result.Issues--
	r.resolveLast("fixed_loader_deployed_outside_loader",
		fmt.Sprintf("re-laid out %d file(s) under BepInEx/ and re-deployed them", count))
	r.emitEv(VerifyEvent{Kind: VerifyEvRepairDetail, Fixed: true,
		Detail: fmt.Sprintf("re-laid out and re-deployed %s", mod.Name)})
}

// redeployHolders puts every holder back after an abandoned repair. Best
// effort and silent about per-holder failures: the caller is already
// reporting the failure that got here, and a mod left undeployed surfaces
// as loaderPluginLinkCheck's own finding on the next run.
func (r *verifyRun) redeployHolders(holders []relayoutHolder) {
	for _, holder := range holders {
		_ = holder.installer.Install(r.ctx, r.game, &holder.mod.Mod, holder.profile)
	}
}

// recordHolderLinkMethod keeps a re-deployed row's recorded link method
// honest, exactly as relinkDeployedRow does: the files were just written
// with the profile's EFFECTIVE method, and a row still claiming the old one
// is a new record-vs-reality drift. A failure here is not fatal - the
// deployment is fixed, only the bookkeeping is stale.
func (r *verifyRun) recordHolderLinkMethod(holder relayoutHolder) {
	if holder.method == holder.mod.LinkMethod {
		return
	}
	if err := r.svc.setModLinkMethod(r.ctx, holder.mod.SourceID, holder.mod.ID,
		r.game.ID, holder.profile, holder.method); err != nil {
		r.emitEv(VerifyEvent{Kind: VerifyEvRepairDetail,
			Detail: fmt.Sprintf("recording the link method for profile %s: %v", holder.profile, err)})
		return
	}
	holder.mod.LinkMethod = holder.method
}

// failRelayout corrects the row's reason in place - it was written before
// the attempt and has just stopped being true - and leaves the finding an
// issue. repairUnlinkedLoaderFiles' own convention.
func (r *verifyRun) failRelayout(mod *domain.InstalledMod, detail string) {
	last := &r.result.Findings[len(r.result.Findings)-1]
	last.FixableReason = fmt.Sprintf("this --fix run could not re-lay out %s: %s", mod.Name, detail)
	r.emitEv(VerifyEvent{Kind: VerifyEvRepairDetail,
		Detail: fmt.Sprintf("--fix could not re-lay out %s: %s", mod.Name, detail)})
}

// relayoutHolder is one profile that has mod installed at the version whose
// cache entry is about to be re-laid out: that profile's own row, the
// effective link method its deploys use (profile > game > global, #81), and
// an installer bound to it.
type relayoutHolder struct {
	profile   string
	mod       *domain.InstalledMod
	method    domain.LinkMethod
	installer *Installer
}

// profilesDeploying lists every profile of this game holding mod at the same
// version - the profiles whose deployments the cache re-layout invalidates,
// and therefore the ones the repair must undeploy and put back.
//
// The verifying profile is always first, so the row this run reported is
// repaired before any sibling.
//
// The link method is resolved HERE rather than at deploy time, for
// relinkDeployedRow's reason: a method that cannot be resolved (#189 - an
// invalid profile link_method) must refuse the whole repair before the
// cache entry is touched, not strand it half-deployed afterwards.
func (r *verifyRun) profilesDeploying(mod *domain.InstalledMod) ([]relayoutHolder, error) {
	pm := r.svc.NewProfileManager()
	profiles, err := pm.List(r.ctx, r.game.ID)
	if err != nil {
		return nil, err
	}
	holder := func(name string, installed *domain.InstalledMod) (relayoutHolder, error) {
		method, merr := r.svc.GetEffectiveLinkMethod(r.ctx, r.game, name)
		if merr != nil {
			return relayoutHolder{}, fmt.Errorf("resolving the link method for profile %s: %w", name, merr)
		}
		return relayoutHolder{
			profile: name, mod: installed, method: method,
			installer: r.svc.newInstallerWithLinker(r.game, r.svc.getLinker(method)),
		}, nil
	}

	first, err := holder(r.profile, mod)
	if err != nil {
		return nil, err
	}
	holders := []relayoutHolder{first}
	for _, p := range profiles {
		if p.Name == r.profile {
			continue
		}
		sibling, err := r.svc.GetInstalledMod(r.ctx, mod.SourceID, mod.ID, r.game.ID, p.Name)
		if err != nil {
			if errors.Is(err, domain.ErrModNotFound) {
				continue
			}
			return nil, fmt.Errorf("checking profile %s: %w", p.Name, err)
		}
		if sibling.Version != mod.Version {
			continue // a different cache entry entirely
		}
		h, err := holder(p.Name, sibling)
		if err != nil {
			return nil, err
		}
		holders = append(holders, h)
	}
	return holders, nil
}

// relayoutCacheEntry runs the archive-root normaliser over the mod's cache
// entry, atomically.
//
// Atomically because a half-moved cache entry is worse than the misplaced
// one it replaces: the work happens in a SIBLING directory reached by one
// rename, and only a second rename makes it the entry. A failure anywhere
// in between renames the original back, so the entry a later run sees is
// either the old layout or the new one and never a mixture.
//
// A sibling rather than a copy: rename within the cache directory is atomic
// and costs nothing, where a copy of a multi-gigabyte entry is neither. The
// entry's reserved bookkeeping files ride along untouched -
// relativeFileMembers excludes them from the member list, so the normaliser
// never sees them.
func (r *verifyRun) relayoutCacheEntry(mod *domain.InstalledMod) error {
	entry := r.cacheEntryPath(mod)
	staging := entry + ".relayout"
	if err := os.RemoveAll(staging); err != nil {
		return fmt.Errorf("preparing %s: %w", staging, err)
	}
	if err := os.Rename(entry, staging); err != nil {
		return fmt.Errorf("moving the cache entry aside: %w", err)
	}
	if _, err := normalizeBepInExTree(staging, mod.Name, true, r.game.InstallPath); err != nil {
		if undo := os.Rename(staging, entry); undo != nil {
			return fmt.Errorf("re-laying out the cache entry: %w (and putting it back failed: %v)", err, undo)
		}
		return fmt.Errorf("re-laying out the cache entry: %w", err)
	}
	if err := os.Rename(staging, entry); err != nil {
		return fmt.Errorf("putting the re-laid-out cache entry back: %w", err)
	}
	return nil
}
