package linker

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// Linker deploys and undeploys mod files to game directories
type Linker interface {
	Deploy(src, dst string) error
	Undeploy(dst string) error
	IsDeployed(dst string) (bool, error)
	Method() domain.LinkMethod
}

// New creates a linker for the given method
func New(method domain.LinkMethod) Linker {
	switch method {
	case domain.LinkHardlink:
		return NewHardlink()
	case domain.LinkCopy:
		return NewCopy()
	default:
		return NewSymlink()
	}
}

// CleanupEmptyDirs removes the directories that became empty BECAUSE lmm
// removed the given files, and nothing else.
//
// removedFiles are the paths lmm actually removed - relative to basePath,
// or absolute under it. For each one the walk starts at its parent
// directory and goes up, removing while the directory is empty, and stops
// at the first directory that is not empty, cannot be removed, or IS
// basePath. basePath itself is never removed, and a directory that was not
// on a removed file's ancestor chain is never even looked at.
//
// A SYMLINKED directory anywhere on the chain stops the walk before it
// removes anything: os.ReadDir and os.Remove both follow a symlink, while
// filepath.Dir and the prefix check are string operations, so without this
// guard a mod folder the user symlinked onto another drive would have been
// read through and removed on the far side - outside basePath entirely -
// and then unlinked on the way back up. The symlink is left in place, and
// so is everything it points at.
//
// It used to walk the WHOLE tree under basePath and remove every empty
// directory it found (#415). For a game whose mod root is its install root
// (`mod_path: ""` - Cyberpunk 2077, hearts-of-iron-iv,
// euro-truck-simulator-2, call-of-duty-black-ops-6) that made every
// uninstall and every purge sweep the game's own empty directories away,
// including the ones its loaders expect to exist (CET's bin/x64/plugins,
// redscript's r6/scripts and r6/tweaks, archive/pc/mod,
// tools/redmod/mods). Steam's verify-integrity does not restore an empty
// directory, so it did not heal on its own. Bounding the walk to what lmm
// removed is smaller behaviour for every game: every directory this removes
// is empty, strictly under basePath, and reachable from basePath through
// real directories only, so the old sweep's fixpoint removed it too.
//
// Two exceptions to "smaller", neither of them a new harm class. The far
// side of a symlink is not under basePath and filepath.Walk, which Lstats,
// never descended into one - so the symlink guard above is not narrowing
// anything the old sweep did. And when basePath ITSELF is a symlink the old
// sweep removed NOTHING at all (Walk Lstats its own root), while this prunes
// normally inside it - deliberate, because a Steam library on a symlinked
// mount must still be tidied, and the bound is the same removal chain as
// everywhere else.
//
// Strictly smaller cuts both ways, deliberately. lmm has several other
// places that remove deployed files and have never called this - core's
// convergeDeployedFiles, replaceWithCaches' obsolete-file loop,
// restoreOldFiles/rollbackDeploy and the snapshot restore - and the old
// whole-tree sweep used to tidy their leftovers away as a side effect of
// the next unrelated uninstall or purge. It does not any more, so an empty
// directory one of those leaves behind now persists. That is the trade:
// leaving a stray empty directory is cosmetic, and removing a directory
// the game shipped is not.
func CleanupEmptyDirs(basePath string, removedFiles []string) {
	if basePath == "" {
		return
	}
	base := filepath.Clean(basePath)
	// Every ancestor step must stay strictly INSIDE base: that is what
	// keeps basePath itself, and anything outside it, out of reach even if
	// a caller hands over a path that does not belong to this game.
	prefix := base + string(os.PathSeparator)
	for _, file := range removedFiles {
		path := filepath.Clean(file)
		if !filepath.IsAbs(path) {
			path = filepath.Join(base, path)
		}
		if !realDirChain(prefix, filepath.Dir(path)) {
			continue
		}
		for dir := filepath.Dir(path); strings.HasPrefix(dir, prefix); dir = filepath.Dir(dir) {
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) > 0 {
				break
			}
			if err := os.Remove(dir); err != nil {
				break
			}
		}
	}
}

// realDirChain reports whether every step from dir up to (but not
// including) the base prefix is a REAL directory - Lstat, so a symlink is
// not one.
//
// The check runs before anything is removed, and covers the whole chain
// rather than one directory at a time, because the walk goes bottom-up: by
// the time it reached a symlink at `<base>/link` it would already have
// ReadDir'd and removed `<base>/link/sub`, which lives on the far side.
// One symlink on the chain therefore puts that whole chain out of reach,
// which is the conservative answer - lmm did not create the symlink and
// cannot know what else depends on what is behind it.
func realDirChain(prefix, dir string) bool {
	for ; strings.HasPrefix(dir, prefix); dir = filepath.Dir(dir) {
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() {
			return false
		}
	}
	return true
}
