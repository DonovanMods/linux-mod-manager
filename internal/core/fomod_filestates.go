package core

import (
	"context"
	"os"
	"path/filepath"
	"strings"

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
// A present path is ACTIVE: until a game adapter can report plugin state
// (P5, #618), active and present are the same, a documented policy that
// beats both reference managers' defaults (Vortex fails every
// fileDependency with no host answer; MO2 considers only plugins).
//
// Everything is read lazily, on the first condition an installer tests,
// and memoised: one evaluation tests a condition many times, and an
// installer that tests no files costs nothing.
type profileFileStates struct {
	load func() (profileFiles, managed map[string]bool, root string)

	loaded   bool
	provided map[string]bool
	managed  map[string]bool
	root     string
	seen     map[string]fomod.FileState
	dirs     map[string][]os.DirEntry
}

// fileStatesFor builds the FileStates for an install of self (a
// domain.ModKey) into profileName of game. A read that fails degrades to
// what could be read.
func (s *Service) fileStatesFor(ctx context.Context, game *domain.Game, profileName, self string) fomod.FileStates {
	f := &profileFileStates{seen: map[string]fomod.FileState{}, dirs: map[string][]os.DirEntry{}}
	if game == nil {
		f.loaded = true
		return f
	}
	f.load = func() (map[string]bool, map[string]bool, string) {
		provided, managed := map[string]bool{}, map[string]bool{}
		if profileName != "" {
			if rows, err := s.GetInstalledMods(ctx, game.ID, profileName); err == nil {
				gameCache := s.GetGameCache(game)
				for _, row := range rows {
					if !row.Enabled || row.External || domain.ModKey(row.SourceID, row.ID) == self {
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
		if s.db != nil {
			if profiles, err := s.NewProfileManager().List(ctx, game.ID); err == nil {
				for _, p := range profiles {
					rows, err := s.db.ListDeployedFiles(ctx, game.ID, p.Name)
					if err != nil {
						continue
					}
					for _, r := range rows {
						managed[normalizeStatePath(r.RelativePath)] = true
					}
				}
			}
		}
		return provided, managed, game.ModPath
	}
	return f
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
	if key != "" && (f.provided[key] || (!f.managed[key] && f.onDisk(key))) {
		st = fomod.FileActive
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
