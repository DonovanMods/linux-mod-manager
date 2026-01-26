package linker

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
)

// CopyLinker deploys mods by copying files
type CopyLinker struct{}

// NewCopy creates a new copy linker
func NewCopy() *CopyLinker {
	return &CopyLinker{}
}

// Deploy copies src to dst
func (l *CopyLinker) Deploy(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return fmt.Errorf("creating destination dir: %w", err)
	}

	srcFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("opening source: %w", err)
	}
	defer srcFile.Close()

	srcInfo, err := srcFile.Stat()
	if err != nil {
		return fmt.Errorf("stat source: %w", err)
	}

	dstFile, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, srcInfo.Mode())
	if err != nil {
		return fmt.Errorf("creating destination: %w", err)
	}
	defer dstFile.Close()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return fmt.Errorf("copying file: %w", err)
	}

	return nil
}

// Undeploy removes the file at dst
func (l *CopyLinker) Undeploy(dst string) error {
	if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing file: %w", err)
	}
	return nil
}

// IsDeployed checks if dst exists
func (l *CopyLinker) IsDeployed(dst string) (bool, error) {
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
func (l *CopyLinker) Method() domain.LinkMethod {
	return domain.LinkCopy
}
