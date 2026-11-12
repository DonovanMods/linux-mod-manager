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
// install into one profile of a game (#614, design §4).
//
// A path is PRESENT when the profile's deployed-files ledger names it - a
// mod deployed in the profile provides it - or the game's mod directory
// holds it, which covers the game's own files (Skyrim.esm). Both compare
// case-insensitively, as FOMOD paths and Windows do. A present path is
// ACTIVE: until a game adapter can report plugin state (P5, #618), active
// and present are the same, a documented policy that beats both reference
// managers' defaults (Vortex fails every fileDependency with no host
// answer; MO2 considers only plugins).
//
// Answers are memoised per path: one evaluation tests a condition many
// times, and the directory does not change under it.
type profileFileStates struct {
	root     string
	deployed map[string]bool
	seen     map[string]fomod.FileState
}

// fileStatesFor builds the FileStates for an install into profileName of
// game. A ledger that cannot be read degrades to the directory alone.
func (s *Service) fileStatesFor(ctx context.Context, game *domain.Game, profileName string) fomod.FileStates {
	f := &profileFileStates{deployed: map[string]bool{}, seen: map[string]fomod.FileState{}}
	if game != nil {
		f.root = game.ModPath
		if profileName != "" && s.db != nil {
			if rows, err := s.db.ListDeployedFiles(ctx, game.ID, profileName); err == nil {
				for _, r := range rows {
					f.deployed[strings.ToLower(filepath.ToSlash(r.RelativePath))] = true
				}
			}
		}
	}
	return f
}

// State implements fomod.FileStates.
func (f *profileFileStates) State(path string) fomod.FileState {
	key := strings.ToLower(strings.Trim(filepath.ToSlash(path), "/"))
	if st, ok := f.seen[key]; ok {
		return st
	}
	st := fomod.FileMissing
	if key != "" && (f.deployed[key] || f.onDisk(key)) {
		st = fomod.FileActive
	}
	f.seen[key] = st
	return st
}

// onDisk reports whether root holds key (lower-case, '/'-separated),
// matching each segment case-insensitively. A segment of ".." never
// matches: the condition is about the mod directory, not above it.
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
		entries, err := os.ReadDir(dir)
		if err != nil {
			return false
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
