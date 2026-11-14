package core

// shared_handoff.go is #633: when a removal takes lmm's file from a path
// another enabled mod of the profile also ships, the path passes to the
// next of those mods in load order in the same operation, instead of
// getting its original back (or being left empty) until the next deploy.
//
// The removals that hand a path on are the ones that take a mod's file
// away for good: an uninstall, a disable, an update or a copy whose new
// version no longer ships the path, and a switch or an apply turning the
// mod off (Installer.uninstallHandingOff, and every Installer.replace). A
// deploy's own undeploy-then-install, a verify repair and a purge do not:
// the mod goes straight back, or every mod goes.
//
// The heir is placed exactly as a deploy would place it - the linker of
// the profile it deploys under, or the profile's overridden copy (#630) -
// and recorded as that mod's, so `lmm conflicts` names it as the owner. The
// original a first deploy set aside (#350/#632) stays set aside under it,
// and goes back when the last mod that ships the path is removed. #445's
// other-game guard and #466's user-file guard are asked as a deploy asks
// them; a path either keeps is left as the removal leaves it today.
//
// What the removals of one flow learn about the heirs - the profile's
// candidates in load order, and each candidate's deployable files - is
// that flow's (heirFlow), so a flow that removes many mods lists each
// candidate's cache entry once, not once per removal. What they hand on is
// reported once, at the flow's drain, naming each path's final owner
// (originalsStore.noteHandOff, Service.takeCaptureWarnings).

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"sync"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
)

// heirSource is how an Installer learns which mods may take a path one of
// its removals takes lmm's file from (#633): Service.pathHeirsFor.
type heirSource struct {
	svc *Service
	// profile is the profile whose mods take the path, and whose record
	// it gets: "" for the removal's own, or a switch's target.
	profile string
	// placer is the Installer that places an heir's file: nil places
	// through the removing Installer itself.
	placer *Installer
	// exclude is the keys of mods the flow turns off or deploys itself.
	exclude map[string]bool
}

// pathHeirsFor is the heirSource for a removal under the removing
// profile, or - heirProfile set - under a switch's target (placer, that
// profile's Installer): the profile's enabled, deployed mods its document
// lists and does not mark off, other than the one going and those in
// exclude, in load order with the winner first. A mod with no deployment
// of its own, an external one (#269) and the merged artifact's synthetic
// mod never take a path.
func (s *Service) pathHeirsFor(heirProfile string, placer *Installer, exclude map[string]bool) *heirSource {
	return &heirSource{svc: s, profile: heirProfile, placer: placer, exclude: exclude}
}

// profileFor is the heir profile for a removal under removalProfile.
func (h *heirSource) profileFor(removalProfile string) string {
	if h.profile != "" {
		return h.profile
	}
	return removalProfile
}

// pathHeirs is the mods one removal may hand a shared path to, winner first.
type pathHeirs struct {
	profile    string
	flow       *heirFlow
	candidates []domain.InstalledMod
}

// heirFlow is one flow's knowledge of the heirs of one game (#633): each
// heir profile's candidates, read once and kept as mods leave, and each
// cache entry's deployable files, listed once. It lives for one mutation
// (Service.beginOp ends it), so nothing a later flow changes is read stale.
type heirFlow struct {
	mu sync.Mutex
	// candidates is each heir profile's candidates, winner first; a
	// profile with no entry is read on its next use.
	candidates map[string][]domain.InstalledMod
	listings   map[heirListingKey]map[domain.DeployPath]deployItem
}

// heirListingHook, when set, runs once per cache entry a flow lists for
// its heirs: the tests' count of the listings a flow makes (#633 review).
var heirListingHook func()

// heirListingKey names one listing: a cache entry, routed by a stated type.
type heirListingKey struct {
	key     cache.Key
	modType string
}

// heirFlowFor is the running flow's heirFlow for gameID.
func (s *Service) heirFlowFor(gameID string) *heirFlow {
	s.heirFlowsMu.Lock()
	defer s.heirFlowsMu.Unlock()
	if s.heirFlows == nil {
		s.heirFlows = make(map[string]*heirFlow)
	}
	f, ok := s.heirFlows[gameID]
	if !ok {
		f = &heirFlow{}
		s.heirFlows[gameID] = f
	}
	return f
}

// forgetHeirFlows ends every game's heirFlow, at the end of a mutation.
func (s *Service) forgetHeirFlows() {
	if s == nil {
		return
	}
	s.heirFlowsMu.Lock()
	s.heirFlows = nil
	s.heirFlowsMu.Unlock()
}

// candidatesFor is profile's candidates, read the first time a removal of
// the flow asks. The profile document gives the load order and its off
// markers, as a deploy reads them; a document that cannot be read is an
// error, so nothing is handed on rather than handed to the wrong mod.
func (f *heirFlow) candidatesFor(ctx context.Context, s *Service, gameID, profile string) ([]domain.InstalledMod, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if mods, ok := f.candidates[profile]; ok {
		return mods, nil
	}
	doc, err := config.LoadProfile(s.configDir, gameID, profile)
	if err != nil {
		return nil, fmt.Errorf("loading profile %s: %w", profile, err)
	}
	rows, err := s.db.GetInstalledMods(ctx, gameID, profile)
	if err != nil {
		return nil, err
	}
	byKey := make(map[string]domain.InstalledMod, len(rows))
	for _, m := range rows {
		byKey[domain.ModKey(m.SourceID, m.ID)] = m
	}
	var ordered []domain.InstalledMod
	for _, ref := range doc.Mods {
		key := domain.ModKey(ref.SourceID, ref.ModID)
		m, ok := byKey[key]
		delete(byKey, key) // a ref listed twice counts at its first place (#457)
		if !ok || ref.Disabled || !m.Enabled || !m.Deployed || m.External || m.SourceID == domain.SourceMerged {
			continue
		}
		ordered = append(ordered, m)
	}
	mods := make([]domain.InstalledMod, 0, len(ordered))
	for j := len(ordered) - 1; j >= 0; j-- {
		mods = append(mods, ordered[j])
	}
	if f.candidates == nil {
		f.candidates = make(map[string][]domain.InstalledMod)
	}
	f.candidates[profile] = mods
	return mods, nil
}

// left drops key from profile's candidates: the flow took that mod away.
func (f *heirFlow) left(profile, key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	mods, ok := f.candidates[profile]
	if !ok {
		return
	}
	kept := make([]domain.InstalledMod, 0, len(mods))
	for _, m := range mods {
		if domain.ModKey(m.SourceID, m.ID) != key {
			kept = append(kept, m)
		}
	}
	f.candidates[profile] = kept
}

// changed forgets every profile's candidates, and the listings of the
// cache entries of mods: the flow deployed those mods, or another version
// of one, so the next removal reads them again - a reinstall can rewrite a
// cache entry under the same key. Every other listing stays.
func (f *heirFlow) changed(game *domain.Game, mods ...*domain.Mod) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.candidates = nil
	for _, m := range mods {
		k := modCacheKey(game.ID, *m)
		for lk := range f.listings {
			if lk.key == k {
				delete(f.listings, lk)
			}
		}
	}
}

// listing is mod's deployable files by path, listed once per flow.
func (f *heirFlow) listing(placer *Installer, game *domain.Game, mod *domain.InstalledMod) map[domain.DeployPath]deployItem {
	k := heirListingKey{key: modCacheKey(game.ID, mod.Mod), modType: mod.ModType}
	f.mu.Lock()
	defer f.mu.Unlock()
	if files, ok := f.listings[k]; ok {
		return files
	}
	if heirListingHook != nil {
		heirListingHook()
	}
	items, err := deployableFiles(placer.cache, placer.adapter, game, k.key, mod.ModType)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		placer.log.Warn("could not list a mod's files to hand it a shared path", "mod", domain.ModKey(mod.SourceID, mod.ID), "err", err)
	}
	files := make(map[domain.DeployPath]deployItem, len(items))
	for _, it := range items {
		files[it.path] = it
	}
	if f.listings == nil {
		f.listings = make(map[heirListingKey]map[domain.DeployPath]deployItem)
	}
	f.listings[k] = files
	return files
}

// resolve is the heirs of a removal of removing under removalProfile.
func (h *heirSource) resolve(ctx context.Context, game *domain.Game, removalProfile string, removing *domain.Mod) (*pathHeirs, error) {
	profile := h.profileFor(removalProfile)
	flow := h.svc.heirFlowFor(game.ID)
	mods, err := flow.candidatesFor(ctx, h.svc, game.ID, profile)
	if err != nil {
		return nil, err
	}
	going := domain.ModKey(removing.SourceID, removing.ID)
	heirs := &pathHeirs{profile: profile, flow: flow}
	for _, m := range mods {
		key := domain.ModKey(m.SourceID, m.ID)
		if key != going && !h.exclude[key] {
			heirs.candidates = append(heirs.candidates, m)
		}
	}
	return heirs, nil
}

// heirFlowChanged tells the running flow's heirs that i is deploying
// mods.
func (i *Installer) heirFlowChanged(game *domain.Game, mods ...*domain.Mod) {
	if i.heirs != nil {
		i.heirs.svc.heirFlowFor(game.ID).changed(game, mods...)
	}
}

// heirLeft tells the running flow's heirs that a removal through i, under
// profileName, took mod away.
func (i *Installer) heirLeft(game *domain.Game, profileName string, mod *domain.Mod) {
	if i.heirs != nil {
		i.heirs.svc.heirFlowFor(game.ID).left(i.heirs.profileFor(profileName), domain.ModKey(mod.SourceID, mod.ID))
	}
}

// handOff is one removal's hand-off state: the mod going, the paths a mod
// deployed right after it will hold anyway, and the heirs once asked for.
type handOff struct {
	removing *domain.Mod
	// keep is the paths a mod about to be deployed in the removed mod's
	// place ships (deployCopiedMod's uninstall-then-install): that deploy
	// takes them, so nothing is handed on there.
	keep map[domain.DeployPath]bool
	// exclude is the keys of mods the same caller removes next (a web
	// batch uninstall's others): a path is never handed to one of them.
	exclude map[string]bool

	resolved bool
	heirs    *pathHeirs
	placer   *Installer
}

// newHandOff is the hand-off state for a removal of mod through i, or nil
// when i has no heirs to ask (an Installer built without a Service).
func (i *Installer) newHandOff(mod *domain.Mod, incoming *domain.Mod, game *domain.Game) *handOff {
	if i.heirs == nil {
		return nil
	}
	ho := &handOff{removing: mod}
	if incoming != nil {
		files, err := deployableFiles(i.cache, i.adapter, game, modCacheKey(game.ID, *incoming), incoming.ModType)
		if err == nil {
			ho.keep = make(map[domain.DeployPath]bool, len(files))
			for _, f := range files {
				ho.keep[f.path] = true
			}
		}
	}
	return ho
}

// resolve asks for the removal's heirs once, the first time a path needs
// one. A failure leaves the removal as it was before #633: no heir, and
// the original goes back.
func (ho *handOff) resolve(ctx context.Context, i *Installer, game *domain.Game, profileName string) *pathHeirs {
	if ho.resolved {
		return ho.heirs
	}
	ho.resolved = true
	heirs, err := i.heirs.resolve(ctx, game, profileName, ho.removing)
	if err != nil {
		i.log.Warn("could not list the mods that may take a shared path; the removal leaves it as before", "err", err)
		return nil
	}
	if len(ho.exclude) > 0 {
		kept := heirs.candidates[:0:0]
		for _, m := range heirs.candidates {
			if !ho.exclude[domain.ModKey(m.SourceID, m.ID)] {
				kept = append(kept, m)
			}
		}
		heirs.candidates = kept
	}
	if len(heirs.candidates) == 0 {
		return nil
	}
	placer := i.heirs.placer
	if placer == nil {
		placer = i
	}
	// A deploy refused by the adapter (#413) or by a moved mod_path
	// (#451) is refused here too: handing a path on is a deploy.
	if placer.refused != nil {
		return nil
	}
	if err := placer.refuseStranded(ctx, game); err != nil {
		return nil
	}
	ho.heirs, ho.placer = heirs, placer
	return heirs
}

// heirFor is the first candidate, winner first, whose cache entry deploys
// file, and the item it deploys there.
func (h *pathHeirs) heirFor(game *domain.Game, placer *Installer, file domain.DeployPath) (*domain.InstalledMod, deployItem, bool) {
	for j := range h.candidates {
		c := &h.candidates[j]
		if item, ok := h.flow.listing(placer, game, c)[file]; ok {
			return c, item, true
		}
	}
	return nil, deployItem{}, false
}

// handOffPath hands file, whose lmm file the removal ho is part of has just
// taken away from dstPath, to the next mod in load order that ships it, and
// reports whether one took it. When none does - or placing its file fails
// - the caller puts the original back, as a removal always has.
func (i *Installer) handOffPath(ctx context.Context, game *domain.Game, profileName string, ho *handOff, others *otherGameRecords, file domain.DeployPath, dstPath string) bool {
	if ho == nil || ho.keep[file] {
		return false
	}
	heirs := ho.resolve(ctx, i, game, profileName)
	if heirs == nil {
		return false
	}
	p := ho.placer
	heir, item, ok := heirs.heirFor(game, p, file)
	if !ok {
		return false
	}
	if !p.placeHeir(ctx, game, heirs.profile, others, &heir.Mod, item, dstPath) {
		return false
	}
	i.noteHandOff(handedPath{
		path: file, profile: heirs.profile,
		from: handOffMod{key: domain.ModKey(ho.removing.SourceID, ho.removing.ID), label: modLabel(ho.removing)},
		heir: handOffMod{key: domain.ModKey(heir.SourceID, heir.ID), label: modLabel(&heir.Mod)},
	})
	return true
}

// noteHandOff puts h on the running flow's hand-off ledger, which the
// flow's drain reports (Service.takeCaptureWarnings).
func (i *Installer) noteHandOff(h handedPath) {
	if i.originals != nil {
		i.originals.noteHandOff(h)
	}
}

// placeHeir deploys mod's item at dstPath under profileName, as Install
// deploys one file - another game's file and the user's are left, and what
// is there is preserved first - and records it as mod's. It reports
// whether the file was placed.
func (i *Installer) placeHeir(ctx context.Context, game *domain.Game, profileName string, others *otherGameRecords, mod *domain.Mod, item deployItem, dstPath string) bool {
	file := item.path
	if _, ok := heldElsewhere(others, file, dstPath); ok {
		return false
	}
	check := i.keepOnDeploy(ctx, i.judgeFor(game, profileName, others, nil), file, dstPath)
	if check.keep {
		return false
	}
	saved := i.placing
	i.placing = i.overridesFor(game, profileName)
	defer func() { i.placing = saved }()
	if !i.preserveBeforeDeploy(ctx, game, profileName, file, dstPath, mod, check) {
		return false
	}
	srcPath := i.cache.GetFilePath(cache.Key{GameID: game.ID, SourceID: mod.SourceID, ModID: mod.ID, Version: mod.Version, Variant: mod.Variant}, item.member)
	if err := i.place(game, file, srcPath, dstPath); err != nil {
		i.log.Warn("could not hand a shared path to the next mod in load order", "path", dstPath, "err", err)
		i.noteHeld(fmt.Sprintf("%s: could not deploy %s's file there in place of the removed mod's (%v); the next deploy puts it in place", file, modLabel(mod), err))
		return false
	}
	if i.db != nil {
		if err := i.recordDeployed(ctx, game, profileName, file, mod, i.placedFingerprint(file, dstPath)); err != nil {
			_ = i.unplace(game, file, dstPath)
			i.noteHeld(fmt.Sprintf("%s: could not record %s's file there in place of the removed mod's (%v); the next deploy puts it in place", file, modLabel(mod), err))
			return false
		}
	}
	return true
}

// modLabel is mod's name, or its key when it has none.
func modLabel(mod *domain.Mod) string {
	if mod.Name != "" {
		return mod.Name
	}
	return domain.ModKey(mod.SourceID, mod.ID)
}

// handOffMod is one side of a hand-off: a mod's key, and the name a note
// gives it.
type handOffMod struct {
	key, label string
}

// handedPath is one path a removal of the running flow handed on: under
// which profile, from the mod whose removal first gave it up, to the mod
// that holds it now.
type handedPath struct {
	path    domain.DeployPath
	profile string
	from    handOffMod
	heir    handOffMod
}

// handOffNotes is the notes for the hand-offs a flow made, one per heir
// and removed mod, in the order each pair first appeared - keyed by the
// mods' keys, not their names, which two mods may share.
func handOffNotes(handed []handedPath) []string {
	type pair struct{ heir, from string }
	var order []pair
	paths := make(map[pair][]domain.DeployPath)
	labels := make(map[pair][2]string)
	for _, h := range handed {
		p := pair{h.heir.key, h.from.key}
		if _, seen := paths[p]; !seen {
			order = append(order, p)
			labels[p] = [2]string{h.heir.label, h.from.label}
		}
		paths[p] = append(paths[p], h.path)
	}
	notes := make([]string, 0, len(order))
	for _, p := range order {
		notes = append(notes, handOffNote(labels[p][0], labels[p][1], paths[p]))
	}
	return notes
}

// handOffNoteLimit is how many paths one hand-off note names.
const handOffNoteLimit = 5

// handOffNote is the note for heir taking paths from removed.
func handOffNote(heir, removed string, paths []domain.DeployPath) string {
	names := make([]string, 0, handOffNoteLimit)
	for j, p := range paths {
		if j == handOffNoteLimit {
			break
		}
		names = append(names, p.String())
	}
	list := strings.Join(names, ", ")
	if extra := len(paths) - len(names); extra > 0 {
		list += fmt.Sprintf(" and %d more", extra)
	}
	if len(paths) == 1 {
		return fmt.Sprintf("%s: %s's file is deployed there now in place of %s's, as the next mod in load order that ships it", list, heir, removed)
	}
	return fmt.Sprintf("%s's files are deployed now in place of %s's at %s, as the next mod in load order that ships them", heir, removed, list)
}

// handOffWarnings is the running flow's hand-off notes for gameID, each
// path reported only while its record still names the mod it was handed to
// - its final owner. A path a later step of the flow took back (an update
// undone after a later failure) or released is not reported.
func (s *Service) handOffWarnings(gameID string, handed []handedPath) []string {
	if len(handed) == 0 {
		return nil
	}
	kept := handed[:0:0]
	for _, h := range handed {
		owner, err := s.db.GetFileOwner(context.Background(), gameID, h.profile, h.path)
		if err != nil || owner == nil || domain.ModKey(owner.SourceID, owner.ModID) != h.heir.key {
			continue
		}
		kept = append(kept, h)
	}
	return handOffNotes(kept)
}

// refKeys is the keys of refs, or nil for none.
func refKeys(refs []domain.ModReference) map[string]bool {
	if len(refs) == 0 {
		return nil
	}
	out := make(map[string]bool, len(refs))
	for _, r := range refs {
		out[domain.ModKey(r.SourceID, r.ModID)] = true
	}
	return out
}

// excludeKeys is the keys of mods, for pathHeirsFor's exclude.
func excludeKeys(groups ...[]domain.InstalledMod) map[string]bool {
	out := make(map[string]bool)
	for _, g := range groups {
		for _, m := range g {
			out[domain.ModKey(m.SourceID, m.ID)] = true
		}
	}
	return out
}
