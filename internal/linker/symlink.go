package linker

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
)

// SymlinkLinker deploys mods using symbolic links
type SymlinkLinker struct{}

// NewSymlink creates a new symlink linker
func NewSymlink() *SymlinkLinker {
	return &SymlinkLinker{}
}

// Deploy creates a symlink from src to dst
func (l *SymlinkLinker) Deploy(src, dst string) error {
	// Ensure destination directory exists
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return fmt.Errorf("creating destination dir: %w", err)
	}

	// Remove existing file/link if present
	if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing existing file: %w", err)
	}

	if err := os.Symlink(src, dst); err != nil {
		return fmt.Errorf("creating symlink: %w", err)
	}

	return nil
}

// Undeploy removes the symlink at dst
func (l *SymlinkLinker) Undeploy(dst string) error {
	info, err := os.Lstat(dst)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // Already removed
		}
		return fmt.Errorf("checking file: %w", err)
	}

	// Only remove if it's a symlink
	if info.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf("not a symlink: %s", dst)
	}

	if err := os.Remove(dst); err != nil {
		return fmt.Errorf("removing symlink: %w", err)
	}

	return nil
}

// IsDeployed checks if dst is a symlink
func (l *SymlinkLinker) IsDeployed(dst string) (bool, error) {
	info, err := os.Lstat(dst)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return info.Mode()&os.ModeSymlink != 0, nil
}

// Method returns the link method
func (l *SymlinkLinker) Method() domain.LinkMethod {
	return domain.LinkSymlink
}
