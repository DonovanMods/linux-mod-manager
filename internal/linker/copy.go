package linker

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// CopyLinker deploys mods by copying files
type CopyLinker struct{}

// NewCopy creates a new copy linker
func NewCopy() *CopyLinker {
	return &CopyLinker{}
}

// Deploy copies src to dst.
//
// It never writes through what is at dst (#466 review D2): a symlink there
// is refused - it may point anywhere, and whether it is lmm's own to
// replace is the caller's call, which removes it first - and anything else
// is replaced by a rename, so a hard link dst shares with another file
// (the mod's cached copy itself, after a hardlink deployment) keeps its
// content.
func (l *CopyLinker) Deploy(src, dst string) (err error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return fmt.Errorf("creating destination dir: %w", err)
	}
	if info, err := os.Lstat(dst); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("creating destination: %s is a link, and a copy is never written through one", dst)
	}

	srcFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("opening source: %w", err)
	}
	defer func() {
		if cerr := srcFile.Close(); err == nil && cerr != nil {
			err = fmt.Errorf("closing source: %w", cerr)
		}
	}()

	srcInfo, err := srcFile.Stat()
	if err != nil {
		return fmt.Errorf("stat source: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".lmm-*")
	if err != nil {
		return fmt.Errorf("creating destination: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := io.Copy(tmp, srcFile); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("copying file: %w", err)
	}
	if err := tmp.Chmod(srcInfo.Mode().Perm()); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("setting destination mode: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing destination: %w", err)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return fmt.Errorf("creating destination: %w", err)
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
