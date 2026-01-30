package linker

import (
	"os"
	"path/filepath"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
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

// CleanupEmptyDirs removes all empty directories under the given path.
// It iterates until no more empty directories are found, handling nested empties.
// The basePath itself is never removed.
func CleanupEmptyDirs(basePath string) {
	for {
		found := false
		if err := filepath.Walk(basePath, func(path string, info os.FileInfo, err error) error {
			if err != nil || !info.IsDir() || path == basePath {
				return nil
			}
			entries, err := os.ReadDir(path)
			if err == nil && len(entries) == 0 {
				if err := os.Remove(path); err == nil {
					found = true
				}
			}
			return nil
		}); err != nil {
			return
		}
		if !found {
			break
		}
	}
}
