package linker

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
)

// HardlinkLinker deploys mods using hard links
type HardlinkLinker struct{}

// NewHardlink creates a new hardlink linker
func NewHardlink() *HardlinkLinker {
	return &HardlinkLinker{}
}

// Deploy creates a hard link from src to dst
func (l *HardlinkLinker) Deploy(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return fmt.Errorf("creating destination dir: %w", err)
	}

	if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing existing file: %w", err)
	}

	if err := os.Link(src, dst); err != nil {
		return fmt.Errorf("creating hardlink: %w", err)
	}

	return nil
}

// Undeploy removes the file at dst
func (l *HardlinkLinker) Undeploy(dst string) error {
	if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing file: %w", err)
	}
	return nil
}

// IsDeployed checks if dst exists (hardlinks are indistinguishable from regular files)
func (l *HardlinkLinker) IsDeployed(dst string) (bool, error) {
	_, err := os.Stat(dst)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// Method returns the link method
func (l *HardlinkLinker) Method() domain.LinkMethod {
	return domain.LinkHardlink
}
