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
// It used to walk the WHOLE tree under basePath and remove every empty
// directory it found (#415). For a game whose mod root is its install root
// (`mod_path: ""` - Cyberpunk 2077, hearts-of-iron-iv,
// euro-truck-simulator-2, call-of-duty-black-ops-6) that made every
// uninstall and every purge sweep the game's own empty directories away,
// including the ones its loaders expect to exist (CET's bin/x64/plugins,
// redscript's r6/scripts, archive/pc/mod, tools/redmod/mods). Steam's
// verify-integrity does not restore an empty directory, so it did not heal
// on its own. Bounding the walk to what lmm removed is strictly smaller
// behaviour for every game.
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
