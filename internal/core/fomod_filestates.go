package core

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/fomod"
)

// profileFileStates answers a FOMOD's fileDependency conditions for an
// install into ONE profile of a game (#614, design §4) - the profile being
// installed into, never whichever profile happens to be deployed.
//
// A path is PRESENT when a mod of that profile provides it - an enabled
// installed mod other than the one being installed, by its cache entry's
// files - or the game itself does: the game's mod directory holds it and
// no profile's deployment put it there (Skyrim.esm, not another profile's
// plugin). Both compare case-insensitively, as FOMOD paths and Windows do.
//
// A present path is ACTIVE, except a plugin of a game whose adapter manages
// a plugin list (#618, P5): its Active or Inactive is its state in that
// list. A plugin a mod of the profile provides is Inactive when the
// profile's plugins: order turns it off, and Active otherwise (a plugin the
// order does not name is deployed enabled, P3). A plugin of the game's own
// is Active when the game always loads it (its masters, the Creation Club
// list) or when the game's plugin list file marks it active, and Inactive
// when the file leaves it off; when the file cannot be read (no Wine
// prefix), it is Active, the policy before #618. Without a plugin list,
// active and present are the same, a documented policy that beats both
// reference managers' defaults (Vortex fails every fileDependency with no
// host answer; MO2 considers only plugins).
//
// An install-shaped flow that acquires several mods before committing any
// (a collection's, #609) can hide some of the profile's mods and add the
// files of mods it has acquired but not committed: fileStatesOpts.
//
// Everything is read lazily, on the first condition an installer tests,
// and memoised: one evaluation tests a condition many times, and an
// installer that tests no files costs nothing.
type profileFileStates struct {
	load func() (profileFiles, managed map[string]bool, root string)
	// plugins, when set, answers a present plugin's state (#618); nil
	// for a game without a plugin list.
	plugins *pluginFileStates

	loaded   bool
	provided map[string]bool
	managed  map[string]bool
	root     string
	seen     map[string]fomod.FileState
	dirs     map[string][]os.DirEntry
}

// fileStatesOpts adjusts what fileStatesForWith answers from. The zero
// value answers from the profile as saved.
type fileStatesOpts struct {
	// plugins stands in for the profile's plugins: block - an import's
	// document, which is saved only once every mod is acquired.
	plugins *domain.ProfilePlugins
	// hidden reports a mod (by domain.ModKey) whose installed rows are not
	// present to this install: a collection item of the same or a later
	// install phase (#609). Nil hides none.
	hidden func(key string) bool
	// earlier lists more files that are present: the trees of the mods of
	// earlier install phases, acquired but not yet committed (#609). Nil
	// lists none.
	earlier func() []string
}

// fileStatesForWith builds the FileStates for an install of self (a
// domain.ModKey) into profileName of game, under opts (the zero value:
// the profile as saved). A read that fails degrades to what could be read.
func (s *Service) fileStatesForWith(ctx context.Context, game *domain.Game, profileName, self string, opts fileStatesOpts) fomod.FileStates {
	f := &profileFileStates{seen: map[string]fomod.FileState{}, dirs: map[string][]os.DirEntry{}}
	if game == nil {
		f.loaded = true
		return f
	}
	if spec, ok := s.pluginRules(game); ok {
		f.plugins = &pluginFileStates{spec: spec, order: func() domain.ProfilePlugins {
			if opts.plugins != nil {
				return *opts.plugins
			}
			if profileName == "" {
				return domain.ProfilePlugins{}
			}
			p, err := s.NewProfileManager().Get(ctx, game.ID, profileName)
			if err != nil {
				return domain.ProfilePlugins{}
			}
			return p.Plugins
		}, game: func() (map[string]bool, bool) { return s.gamePluginStates(game, spec) }}
	}
	f.load = func() (map[string]bool, map[string]bool, string) {
		provided, managed := map[string]bool{}, map[string]bool{}
		if profileName != "" {
			if rows, err := s.GetInstalledMods(ctx, game.ID, profileName); err == nil {
				gameCache := s.GetGameCache(game)
				for _, row := range rows {
					key := domain.ModKey(row.SourceID, row.ID)
					if !row.Enabled || row.External || key == self || (opts.hidden != nil && opts.hidden(key)) {
						continue
					}
					files, err := gameCache.ListFiles(modCacheKey(game.ID, row.Mod))
					if err != nil {
						continue
					}
					for _, rel := range files {
						provided[normalizeStatePath(rel)] = true
					}
				}
			}
		}
		if opts.earlier != nil {
			for _, rel := range opts.earlier() {
				provided[normalizeStatePath(rel)] = true
			}
		}
		if s.db != nil {
			if profiles, err := s.NewProfileManager().List(ctx, game.ID); err == nil {
				for _, p := range profiles {
					rows, err := s.db.ListDeployedFiles(ctx, game.ID, p.Name)
					if err != nil {
						continue
					}
					for _, r := range rows {
						if !r.Path.IsGameRoot() { // FOMOD paths are mod-root-relative
							managed[normalizeStatePath(r.Path.Rel)] = true
						}
					}
				}
			}
		}
		return provided, managed, game.ModPath
	}
	return f
}

// pluginFileStates answers a present plugin's Active or Inactive for a
// game whose adapter manages a plugin list (#618). Both halves are read on
// first use.
type pluginFileStates struct {
	spec adapter.PluginSpec
	// order is the profile's plugins: block.
	order func() domain.ProfilePlugins
	// game is the active state of the plugins the game itself has, by
	// pluginKey, and whether the game's list file could be read.
	game func() (map[string]bool, bool)

	enabled   map[string]bool
	gameState map[string]bool
	gameKnown bool
	loaded    bool
}

// state is key's state, key a present path as normalizeStatePath spells
// it; provided says a mod of the profile provides it, rather than the game.
func (p *pluginFileStates) state(key string, provided bool) fomod.FileState {
	if p == nil || strings.Contains(key, "/") || !hasPluginExt(key, p.spec) || unlisted(key, p.spec) {
		// Not a plugin of the list - or one the game strips from it and
		// loads itself (Starfield's BlueprintShips-*): present is active.
		return fomod.FileActive
	}
	if !p.loaded {
		p.loaded = true
		p.enabled = map[string]bool{}
		for _, e := range p.order().Order {
			if _, seen := p.enabled[pluginKey(e.Name)]; !seen {
				p.enabled[pluginKey(e.Name)] = e.Enabled
			}
		}
		p.gameState, p.gameKnown = p.game()
	}
	if provided {
		if on, listed := p.enabled[key]; listed && !on {
			return fomod.FileInactive
		}
		return fomod.FileActive
	}
	if on, ok := p.gameState[key]; ok && on || !p.gameKnown {
		return fomod.FileActive
	}
	return fomod.FileInactive
}

// gamePluginStates is the active state of the plugins game has of its own
// (#618), by pluginKey: every implicit plugin (the game's masters, the
// first Creation Club file's list) and every implicitly active one is
// active; any other is as the game's plugin list file marks it. known is
// false when the list file cannot be read - no Wine prefix, or an
// unreadable file - and then only the implicit plugins are answered.
func (s *Service) gamePluginStates(game *domain.Game, spec adapter.PluginSpec) (active map[string]bool, known bool) {
	st := s.ResolvePrefix(game)
	folder := appDataFolder(game, spec)
	implicit, _ := implicitPlugins(game, spec, st, folder, pluginDirs{})
	active = make(map[string]bool, len(implicit))
	for key := range implicit {
		active[key] = true
	}
	for _, name := range spec.ImplicitlyActive {
		active[pluginKey(name)] = true
	}
	if st == nil || !st.Resolved {
		return active, false
	}
	c := &pluginComputation{spec: spec, prefix: st, listDir: listDirOf(st, folder)}
	if err := c.readCurrent(); err != nil {
		return active, false
	}
	listed := map[string]bool{}
	for _, e := range c.current.entries {
		key := pluginKey(e.name)
		if listed[key] {
			continue
		}
		listed[key] = true
		if !active[key] {
			active[key] = e.active
		}
	}
	return active, true
}

// normalizeStatePath is a path as fileDependency conditions name it:
// lower-case and '/'-separated, with no leading or trailing separator.
func normalizeStatePath(p string) string {
	return strings.ToLower(strings.Trim(filepath.ToSlash(p), "/"))
}

// State implements fomod.FileStates.
func (f *profileFileStates) State(path string) fomod.FileState {
	key := normalizeStatePath(path)
	if st, ok := f.seen[key]; ok {
		return st
	}
	if !f.loaded {
		f.loaded = true
		if f.load != nil {
			f.provided, f.managed, f.root = f.load()
		}
	}
	st := fomod.FileMissing
	switch {
	case key == "":
	case f.provided[key]:
		st = f.plugins.state(key, true)
	case !f.managed[key] && f.onDisk(key):
		st = f.plugins.state(key, false)
	}
	f.seen[key] = st
	return st
}

// onDisk reports whether root holds key (lower-case, '/'-separated),
// matching each segment case-insensitively. A segment of ".." never
// matches: the condition is about the mod directory, not above it. Each
// directory is listed once per evaluation.
func (f *profileFileStates) onDisk(key string) bool {
	if f.root == "" {
		return false
	}
	dir := f.root
	segs := strings.Split(key, "/")
	for i, seg := range segs {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
		entries, ok := f.dirs[dir]
		if !ok {
			entries, _ = os.ReadDir(dir)
			f.dirs[dir] = entries
		}
		found := ""
		for _, e := range entries {
			if strings.EqualFold(e.Name(), seg) {
				found = e.Name()
				break
			}
		}
		if found == "" {
			return false
		}
		dir = filepath.Join(dir, found)
		if i == len(segs)-1 {
			info, err := os.Stat(dir)
			return err == nil && !info.IsDir()
		}
	}
	return false
}
