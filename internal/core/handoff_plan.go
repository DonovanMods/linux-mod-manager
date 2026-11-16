package core

// handoff_plan.go is #638: a plan whose Apply hands a shared path to the
// next mod in load order (#633) names that mod per path, so the CLI's
// confirmation and the web UI's plan view say "Data/foo.dll passes to Mod
// B" before anything moves, rather than only after.
//
// The plan does not keep a second copy of the rule. It walks the flow's
// removals in Apply's order through the very functions Apply's removals
// call - Installer.newHandOff (the paths a copied mod deploys right after
// are its own), handOff.resolve (the candidates: the heir profile's
// enabled, deployed, not-marked-off mods, winner first, less the flow's
// exclusions, and the placer's refusals), pathHeirs.heirFor (the first
// candidate whose cache entry deploys the path) and heirPlaceable (a
// prefix file without the prefix, a path another game records) - with an
// Installer wired exactly as that Apply wires its own (the heirs helpers
// below are the one wiring both call). What it adds is the bookkeeping a
// live flow gets from the database: which paths each removal takes lmm's
// file from (the deployed-file records, then the simulated flow's own
// hand-offs), which mods it has removed so far, and which path a later
// deploy of the flow takes back.
//
// Apply stays authoritative. A plan does not run the checks that need
// the file on disk - #466's "the user changed it" guard and #630's
// overridden copy are asked at Apply - and an update or install whose new
// version is not in the cache yet cannot say which paths it stops
// shipping, so its plan names nothing for it. Apply's ledger
// (originalsStore.noteHandOff, reported by Service.takeCaptureWarnings)
// still reports each path's final owner after the fact, whatever the plan
// said. The plan's own freshness check (installedSnapshot) refuses an
// Apply after a mod was added, removed, enabled, disabled or moved to
// another version; a reorder of the profile between plan and Apply is not
// in it, and there the ledger is what names the real heir.

import (
	"context"
	"slices"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
)

// HandOff is one shared path a plan's removal passes to the next mod in
// load order that ships it (#633/#638): the mod that holds it now gives it
// up, and From is that mod; To is the mod expected to hold it when the
// flow is done - for a path handed on twice in one flow, the last. Apply
// decides the heir again from live state and reports what it did.
type HandOff struct {
	// Path is the file's path relative to Root, in slash form.
	Path string `json:"path"`
	// Root is absent for the mod_path, and names the game's install_path
	// or prefix for a file deployed there (#629/#636).
	Root domain.DeployRoot `json:"root,omitzero"`
	// From is the mod whose removal gives the path up.
	From HandOffMod `json:"from"`
	// To is the mod the path passes to.
	To HandOffMod `json:"to"`
}

// HandOffMod names one side of a HandOff.
type HandOffMod struct {
	SourceID string `json:"source_id"`
	ModID    string `json:"mod_id"`
	// Name is the mod's name, or its source:id key when it has none.
	Name string `json:"name"`
}

// handOffModOf is mod as a HandOffMod.
func handOffModOf(mod *domain.Mod) HandOffMod {
	return HandOffMod{SourceID: mod.SourceID, ModID: mod.ID, Name: modLabel(mod)}
}

// handOffPlanKey is one path under one profile's records.
type handOffPlanKey struct {
	profile string
	path    domain.DeployPath
}

// handOffPlanEntry is one path the simulated flow handed on: from the mod
// whose removal first gave it up, to the mod that holds it now.
type handOffPlanEntry struct {
	at       handOffPlanKey
	from, to HandOffMod
	toKey    string
}

// handOffPlanner walks one plan's removals in Apply's order (#638).
type handOffPlanner struct {
	s    *Service
	game *domain.Game
	// flow is the plan's own listings of its heirs' cache entries, never
	// the running mutation's: a plan runs beside one.
	flow *heirFlow
	// gone is the mods the simulated flow has removed so far, which no
	// later removal of it hands a path to.
	gone map[string]bool
	// owner is who holds a path as the simulated flow leaves it, where
	// that differs from the records: a mod's key, or "" for nobody.
	owner map[handOffPlanKey]string
	// ledger is the hand-offs in the order each path was first handed on,
	// as originalsStore.noteHandOff keeps Apply's.
	ledger []handOffPlanEntry
	// failed stops the walk: a read the plan could not make leaves it
	// naming nothing rather than something wrong.
	failed bool
}

// newHandOffPlanner starts a plan's walk over game.
func (s *Service) newHandOffPlanner(game *domain.Game) *handOffPlanner {
	return &handOffPlanner{s: s, game: game, flow: &heirFlow{}, gone: map[string]bool{}, owner: map[handOffPlanKey]string{}}
}

// held is the paths mod's own file is at under profile as the simulated
// flow stands: its deployed-file records under the game's current
// mod_path (a record stranded under another one stays, #451), less what
// the flow took from it, plus what the flow handed to it.
func (p *handOffPlanner) held(ctx context.Context, profile string, mod *domain.Mod) []domain.DeployPath {
	if p.s.db == nil {
		return nil
	}
	key := domain.ModKey(mod.SourceID, mod.ID)
	records, err := p.s.db.DeployedFileRecordsForMod(ctx, p.game.ID, profile, mod.SourceID, mod.ID)
	if err != nil {
		p.failed = true
		return nil
	}
	seen := make(map[domain.DeployPath]bool, len(records))
	var out []domain.DeployPath
	for _, rec := range records {
		if !underCurrentRoot(p.game, rec.ModPath) {
			continue
		}
		if o, ok := p.owner[handOffPlanKey{profile, rec.Path}]; ok && o != key {
			continue
		}
		seen[rec.Path] = true
		out = append(out, rec.Path)
	}
	for at, o := range p.owner {
		if at.profile == profile && o == key && !seen[at.path] {
			out = append(out, at.path)
		}
	}
	slices.SortFunc(out, func(a, b domain.DeployPath) int {
		if a.Root != b.Root {
			return int(a.Root) - int(b.Root)
		}
		return strings.Compare(a.Rel, b.Rel)
	})
	return out
}

// remove is a removal that takes mod away for good under profile through
// i - Installer.uninstallHandingOff(mod, incoming): an uninstall, a
// disable, a switch or an apply turning it off, or a copy replacing it.
func (p *handOffPlanner) remove(ctx context.Context, i *Installer, profile string, mod, incoming *domain.Mod) {
	if p.failed {
		return
	}
	p.handOn(ctx, i, profile, mod, incoming, p.held(ctx, profile, mod))
	p.gone[domain.ModKey(mod.SourceID, mod.ID)] = true
}

// replace is Installer.replace of oldMod, whose records are under
// recordProfile, by a version deploying newFiles, under profile through i:
// each path the old version holds and the new one does not ship is handed
// on, and the new version holds what it ships.
func (p *handOffPlanner) replace(ctx context.Context, i *Installer, profile, recordProfile string, oldMod *domain.Mod, newFiles []deployItem) {
	p.replaceWith(ctx, i, profile, recordProfile, oldMod, func() ([]deployItem, bool) { return newFiles, true })
}

// replaceWith is replace for a new version whose files are listed only
// when they matter - the old version holds a path, or the flow has handed
// one on that the new version may take back - and are not known when
// list says so (a version not in the cache yet).
func (p *handOffPlanner) replaceWith(ctx context.Context, i *Installer, profile, recordProfile string, oldMod *domain.Mod, list func() ([]deployItem, bool)) {
	if p.failed {
		return
	}
	held := p.held(ctx, recordProfile, oldMod)
	if len(held) == 0 && len(p.ledger) == 0 {
		return
	}
	newFiles, ok := list()
	if !ok {
		return
	}
	ships := make(map[domain.DeployPath]bool, len(newFiles))
	for _, f := range newFiles {
		ships[f.path] = true
	}
	var dropped []domain.DeployPath
	for _, path := range held {
		if !ships[path] {
			dropped = append(dropped, path)
		}
	}
	p.handOn(ctx, i, profile, oldMod, nil, dropped)
	p.deployed(profile, oldMod, newFiles)
}

// deployed is a deploy of mod under profile: it holds every path it ships,
// so a path the flow handed on earlier is mod's now, not its heir's.
func (p *handOffPlanner) deployed(profile string, mod *domain.Mod, files []deployItem) {
	key := domain.ModKey(mod.SourceID, mod.ID)
	for _, f := range files {
		p.owner[handOffPlanKey{profile, f.path}] = key
	}
}

// deployedFromCache is deployed for mod's cache entry, when the cache
// holds it; a mod the flow still has to download is not known yet. Its
// files are listed only once the flow has handed a path on: until then
// there is nothing for it to take back.
func (p *handOffPlanner) deployedFromCache(i *Installer, profile string, mod *domain.Mod) {
	if len(p.ledger) == 0 {
		return
	}
	if files, ok := cachedDeployItems(i, p.game, mod); ok {
		p.deployed(profile, mod, files)
	}
}

// cachedDeployItems is mod's deployable files from i's cache, and whether
// the cache holds its entry.
func cachedDeployItems(i *Installer, game *domain.Game, mod *domain.Mod) ([]deployItem, bool) {
	k := modCacheKey(game.ID, *mod)
	if !i.cache.Exists(k) {
		return nil, false
	}
	files, err := deployableFiles(i.cache, i.adapter, game, k, mod.ModType)
	if err != nil {
		return nil, false
	}
	return files, true
}

// handOn hands each of paths, which removing gives up under profile, to
// its heir through Apply's own functions (see the file comment).
func (p *handOffPlanner) handOn(ctx context.Context, i *Installer, profile string, removing, incoming *domain.Mod, paths []domain.DeployPath) {
	if len(paths) == 0 {
		return
	}
	for _, path := range paths {
		p.owner[handOffPlanKey{profile, path}] = ""
	}
	ho := i.newHandOff(removing, incoming, p.game)
	if ho == nil {
		return
	}
	ho.flow, ho.also = p.flow, p.gone
	others, err := i.otherGamesFor(ctx, p.game)
	if err != nil {
		p.failed = true
		return
	}
	heirs := ho.resolve(ctx, i, p.game, profile)
	if heirs == nil {
		return
	}
	from := handOffModOf(removing)
	for _, path := range paths {
		if ho.keep[path] {
			continue
		}
		heir, _, ok := heirs.heirFor(p.game, ho.placer, path)
		if !ok || !heirPlaceable(p.game, others, path, deployTarget(p.game, path)) {
			continue
		}
		heirKey := domain.ModKey(heir.SourceID, heir.ID)
		at := handOffPlanKey{heirs.profile, path}
		p.owner[at] = heirKey
		p.note(handOffPlanEntry{at: at, from: from, to: handOffModOf(&heir.Mod), toKey: heirKey})
	}
}

// note is originalsStore.noteHandOff for the plan: a path handed on again
// keeps the mod that first gave it up and takes the new heir.
func (p *handOffPlanner) note(e handOffPlanEntry) {
	for j := range p.ledger {
		if p.ledger[j].at == e.at {
			p.ledger[j].to, p.ledger[j].toKey = e.to, e.toKey
			return
		}
	}
	p.ledger = append(p.ledger, e)
}

// handOffs is the walk's result: each path handed on that its heir still
// holds when the simulated flow is done - its final owner, as Apply's
// ledger reports it (Service.handOffWarnings) - or nil.
func (p *handOffPlanner) handOffs() []HandOff {
	if p.failed {
		return nil
	}
	var out []HandOff
	for _, e := range p.ledger {
		if p.owner[e.at] != e.toKey {
			continue
		}
		out = append(out, HandOff{Path: e.at.path.Rel, Root: e.at.path.Root, From: e.from, To: e.to})
	}
	return out
}

// planInstaller is the Installer a plan walks a removal through: the one
// Apply builds for profile, so its linker, adapter, refusals and default
// heirs are Apply's. Nil when it cannot be built; the plan then names
// nothing.
func (s *Service) planInstaller(ctx context.Context, game *domain.Game, profile string) *Installer {
	i, err := s.getInstallerForProfile(ctx, game, profile)
	if err != nil {
		return nil
	}
	return i
}

// applyHeirs is the heirs ApplyProfileApply's removals ask (#633): the
// profile's mods the apply leaves deployed - not one it turns off or
// deploys again itself.
func (s *Service) applyHeirs(plan *ProfileApplyPlan) *heirSource {
	return s.pathHeirsFor("", nil, excludeKeys(plan.ToDisable, plan.ToEnable))
}

// switchHeirExclusions is the mods ApplyProfileSwitch's removals never
// hand a path to (#633): every mod it turns off, enables or installs.
func switchHeirExclusions(plan *SwitchPlan) map[string]bool {
	excluded := excludeKeys(plan.ToDisable, plan.ToEnable)
	for _, ref := range plan.ToInstall {
		excluded[domain.ModKey(ref.SourceID, ref.ModID)] = true
	}
	return excluded
}

// wireSwitchHeirs points a switch's two Installers at the target
// profile's heirs, as ApplyProfileSwitch does (#633): a path a disabled
// mod gives up passes to the next mod in the TARGET's load order, placed
// by the target's Installer and recorded under the target.
func (s *Service) wireSwitchHeirs(plan *SwitchPlan, fromInstaller, toInstaller *Installer) {
	excluded := switchHeirExclusions(plan)
	fromInstaller.heirs = s.pathHeirsFor(plan.To, toInstaller, excluded)
	toInstaller.heirs = s.pathHeirsFor(plan.To, nil, excluded)
}

// planUninstallHandOffs is what ApplyUninstall's removal of mod hands on.
func (s *Service) planUninstallHandOffs(ctx context.Context, game *domain.Game, profile string, mod *domain.Mod) []HandOff {
	i := s.planInstaller(ctx, game, profile)
	if i == nil {
		return nil
	}
	p := s.newHandOffPlanner(game)
	p.remove(ctx, i, profile, mod, nil)
	return p.handOffs()
}

// planReplaceHandOffs is what a replace of oldMod by newMod - an update or
// a rollback - hands on, newFiles being what newMod deploys.
func (s *Service) planReplaceHandOffs(ctx context.Context, game *domain.Game, profile string, oldMod *domain.Mod, newFiles []deployItem) []HandOff {
	i := s.planInstaller(ctx, game, profile)
	if i == nil {
		return nil
	}
	p := s.newHandOffPlanner(game)
	p.replace(ctx, i, profile, profile, oldMod, newFiles)
	return p.handOffs()
}

// planRollbackHandOffs is what ApplyRollback's ReplaceForUpdate hands on:
// the previous version's files, narrowed as the replace narrows a
// same-version, file-only rollback (resolveSharedDirUpdate).
func (s *Service) planRollbackHandOffs(ctx context.Context, game *domain.Game, mod *domain.InstalledMod) []HandOff {
	i := s.planInstaller(ctx, game, mod.ProfileName)
	if i == nil {
		return nil
	}
	prev := mod.Mod
	prev.Version, prev.Variant = mod.PreviousVersion, mod.PreviousVariant
	newFiles, ok := cachedDeployItems(i, game, &prev)
	if !ok {
		return nil
	}
	if keep, _, narrowed := resolveSharedDirUpdate(game.ID, i.cache, i.cache, &mod.Mod, &prev, mod.FileIDs, mod.PreviousFileIDs, membersOf(newFiles)); narrowed {
		newFiles = slices.DeleteFunc(newFiles, func(f deployItem) bool { return !keep[f.member] })
	}
	return s.planReplaceHandOffs(ctx, game, mod.ProfileName, &mod.Mod, newFiles)
}

// planCopyHandOffs fills each entry's Handoffs: what ApplyCopyMod with
// Replace hands on when it replaces the destination's live row - only in
// the game's active profile, whose mods are the ones on disk.
func (s *Service) planCopyHandOffs(ctx context.Context, game *domain.Game, plan *CopyModPlan) {
	if !plan.DestinationActive {
		return
	}
	i := s.planInstaller(ctx, game, plan.To)
	if i == nil {
		return
	}
	gameCache := s.GetGameCache(game)
	p := s.newHandOffPlanner(game)
	for j := range plan.Mods {
		e := &plan.Mods[j]
		prior, err := s.GetInstalledMod(ctx, e.SourceID, e.ModID, game.ID, plan.To)
		if err != nil || !prior.Deployed {
			continue
		}
		row, err := s.GetInstalledMod(ctx, e.SourceID, e.ModID, game.ID, plan.From)
		if err != nil {
			continue
		}
		mod := row.Mod
		before := len(p.ledger)
		switch {
		case e.Disabled:
			p.remove(ctx, i, plan.To, &prior.Mod, nil)
		case prior.Version == mod.Version:
			p.deployedFromCache(i, plan.To, &mod)
		case gameCache.Exists(cache.Key{GameID: game.ID, SourceID: prior.SourceID, ModID: prior.ID, Version: prior.Version, Variant: prior.Variant}):
			p.replaceWith(ctx, i, plan.To, plan.To, &prior.Mod, func() ([]deployItem, bool) { return cachedDeployItems(i, game, &mod) })
		default:
			p.remove(ctx, i, plan.To, &prior.Mod, &mod)
			delete(p.gone, domain.ModKey(mod.SourceID, mod.ID))
			p.deployedFromCache(i, plan.To, &mod)
		}
		for _, h := range p.handOffs() {
			if slices.ContainsFunc(p.ledger[before:], func(e handOffPlanEntry) bool {
				return e.at.path.Rel == h.Path && e.at.path.Root == h.Root
			}) {
				e.Handoffs = append(e.Handoffs, h)
			}
		}
	}
}

// planApplyHandOffs is what ApplyProfileApply hands on, in its order: the
// mods it turns off, then the ones it enables, then its installs - a
// replace of the live version when the target version is cached.
func (s *Service) planApplyHandOffs(ctx context.Context, game *domain.Game, plan *ProfileApplyPlan) []HandOff {
	i := s.planInstaller(ctx, game, plan.Profile)
	if i == nil {
		return nil
	}
	i.heirs = s.applyHeirs(plan)
	p := s.newHandOffPlanner(game)
	for j := range plan.ToDisable {
		p.remove(ctx, i, plan.Profile, &plan.ToDisable[j].Mod, nil)
	}
	for j := range plan.ToEnable {
		p.deployedFromCache(i, plan.Profile, &plan.ToEnable[j].Mod)
	}
	for _, entry := range plan.ToInstall {
		if entry.Mod == nil || !entry.Cached {
			continue
		}
		if prior := entry.Replaces; prior != nil {
			p.replaceWith(ctx, i, plan.Profile, plan.Profile, &prior.Mod, func() ([]deployItem, bool) { return cachedDeployItems(i, game, entry.Mod) })
		} else {
			p.deployedFromCache(i, plan.Profile, entry.Mod)
		}
	}
	return p.handOffs()
}

// planSwitchHandOffs is what ApplyProfileSwitch hands on, in its order:
// the mods it turns off, each under its own profile, then its enables -
// a replace of another live version where switchReplacement says so.
// Its installs download first, so they name nothing.
func (s *Service) planSwitchHandOffs(ctx context.Context, game *domain.Game, plan *SwitchPlan) []HandOff {
	fromInstaller := s.planInstaller(ctx, game, plan.From)
	toInstaller := s.planInstaller(ctx, game, plan.To)
	if fromInstaller == nil || toInstaller == nil {
		return nil
	}
	s.wireSwitchHeirs(plan, fromInstaller, toInstaller)
	p := s.newHandOffPlanner(game)
	for j := range plan.ToDisable {
		im := &plan.ToDisable[j]
		profile, i := plan.From, fromInstaller
		if im.ProfileName == plan.To {
			profile, i = plan.To, toInstaller
		}
		p.remove(ctx, i, profile, &im.Mod, nil)
	}
	for j := range plan.ToEnable {
		im := plan.ToEnable[j]
		if im.ProfileName != plan.To {
			im.ModType = statedType(s.recordedRef(ctx, game.ID, plan.To, im.SourceID, im.ID))
		}
		prior, replacing := s.switchReplacement(game, plan, domain.ModKey(im.SourceID, im.ID), im.Version, im.Variant, im.ModType)
		if !replacing {
			p.deployedFromCache(toInstaller, plan.To, &im.Mod)
			continue
		}
		recordProfile := prior.ProfileName
		if recordProfile == "" {
			recordProfile = plan.To
		}
		p.replaceWith(ctx, toInstaller, plan.To, recordProfile, &prior.Mod, func() ([]deployItem, bool) { return cachedDeployItems(toInstaller, game, &im.Mod) })
	}
	return p.handOffs()
}

// planArchiveUpdateHandOffs is what ApplyUpdateFromArchive's replace of
// mod hands on, files being the archive's members as the ingest caches
// them (archiveContents.files): routed as deployableFilesFromListing routes
// a fresh entry, which has no manifest narrowing it yet.
func (s *Service) planArchiveUpdateHandOffs(ctx context.Context, game *domain.Game, profile string, mod *domain.Mod, files []string) []HandOff {
	i := s.planInstaller(ctx, game, profile)
	if i == nil {
		return nil
	}
	items, err := deployItemsForType(i.adapter, game, mod.ModType, files)
	if err != nil {
		return nil
	}
	keyed := make(itemIndex, len(items))
	for _, it := range items {
		keyed[it.member] = it
	}
	return s.planReplaceHandOffs(ctx, game, profile, mod, keyed.of(routeDeployables(i.adapter, game, files)))
}
