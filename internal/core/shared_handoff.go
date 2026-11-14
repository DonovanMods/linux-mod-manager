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

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
)

// heirSource lists the mods that may take a path a removal of removing,
// under profileName, takes lmm's file from (#633) - Service.pathHeirsFor.
type heirSource func(ctx context.Context, game *domain.Game, profileName string, removing *domain.Mod) (*pathHeirs, error)

// pathHeirs is the mods a removal may hand a shared path to, winner first.
type pathHeirs struct {
	// profile is the profile the heir's file deploys under, and whose
	// record it gets: the removal's own, or a switch's target.
	profile string
	// placer is the Installer that places the heir's file: nil places
	// through the removing Installer itself.
	placer     *Installer
	candidates []*heirCandidate
}

// heirCandidate is one mod that may take a path, with the deployable files
// of its cache entry, listed the first time a path is asked about.
type heirCandidate struct {
	mod    domain.InstalledMod
	listed bool
	files  map[domain.DeployPath]deployItem
}

// handOff is one removal's hand-off state: the mod going, the paths a mod
// deployed right after it will hold anyway, and what was handed to whom.
type handOff struct {
	removing *domain.Mod
	// keep is the paths a mod about to be deployed in the removed mod's
	// place ships (deployCopiedMod's uninstall-then-install): that deploy
	// takes them, so nothing is handed on there.
	keep map[domain.DeployPath]bool

	resolved bool
	heirs    *pathHeirs
	placer   *Installer

	// handed is each heir's paths, in the order the heirs first took one.
	handed map[string][]domain.DeployPath
	order  []string
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
	heirs, err := i.heirs(ctx, game, profileName, ho.removing)
	if err != nil {
		i.log.Warn("could not list the mods that may take a shared path; the removal leaves it as before", "err", err)
		return nil
	}
	if heirs == nil || len(heirs.candidates) == 0 {
		return nil
	}
	placer := heirs.placer
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
func (h *pathHeirs) heirFor(game *domain.Game, placer *Installer, file domain.DeployPath) (*heirCandidate, deployItem, bool) {
	for _, c := range h.candidates {
		if !c.listed {
			c.listed = true
			files, err := deployableFiles(placer.cache, placer.adapter, game, modCacheKey(game.ID, c.mod.Mod), c.mod.ModType)
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				placer.log.Warn("could not list a mod's files to hand it a shared path", "mod", domain.ModKey(c.mod.SourceID, c.mod.ID), "err", err)
			}
			c.files = make(map[domain.DeployPath]deployItem, len(files))
			for _, f := range files {
				c.files[f.path] = f
			}
		}
		if item, ok := c.files[file]; ok {
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
	if !p.placeHeir(ctx, game, heirs.profile, others, &heir.mod.Mod, item, dstPath) {
		return false
	}
	name := heir.mod.Name
	if name == "" {
		name = domain.ModKey(heir.mod.SourceID, heir.mod.ID)
	}
	if ho.handed == nil {
		ho.handed = make(map[string][]domain.DeployPath)
	}
	if _, seen := ho.handed[name]; !seen {
		ho.order = append(ho.order, name)
	}
	ho.handed[name] = append(ho.handed[name], file)
	return true
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

// handOffNoteLimit is how many paths one hand-off note names.
const handOffNoteLimit = 5

// reportHandOffs puts one note per heir on the flow's pending list: which
// paths it now provides in place of the removed mod.
func (i *Installer) reportHandOffs(ho *handOff) {
	if ho == nil {
		return
	}
	for _, name := range ho.order {
		i.noteHeld(handOffNote(name, modLabel(ho.removing), ho.handed[name]))
	}
}

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

// pathHeirsFor is the heirSource for a removal under the removing
// profile, or - heirProfile set - under a switch's target (placer, that
// profile's Installer): the profile's enabled, deployed mods other than the
// one going and those in exclude (the ones the same flow turns off or
// redeploys), in load order with the winner first. A mod with no
// deployment of its own, an external one (#269) and the merged artifact's
// synthetic mod never take a path.
func (s *Service) pathHeirsFor(heirProfile string, placer *Installer, exclude map[string]bool) heirSource {
	return func(ctx context.Context, game *domain.Game, profileName string, removing *domain.Mod) (*pathHeirs, error) {
		profile := profileName
		if heirProfile != "" {
			profile = heirProfile
		}
		rows, err := s.db.GetInstalledMods(ctx, game.ID, profile)
		if err != nil {
			return nil, err
		}
		going := domain.ModKey(removing.SourceID, removing.ID)
		var live []domain.InstalledMod
		for _, m := range rows {
			key := domain.ModKey(m.SourceID, m.ID)
			if !m.Enabled || !m.Deployed || m.External || m.SourceID == domain.SourceMerged || key == going || exclude[key] {
				continue
			}
			live = append(live, m)
		}
		if len(live) == 0 {
			return nil, nil
		}
		doc, err := config.LoadProfile(s.configDir, game.ID, profile)
		if err != nil {
			doc = nil
		}
		ordered := orderByProfile(doc, live)
		heirs := &pathHeirs{profile: profile, placer: placer}
		for j := len(ordered) - 1; j >= 0; j-- {
			heirs.candidates = append(heirs.candidates, &heirCandidate{mod: ordered[j]})
		}
		return heirs, nil
	}
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
