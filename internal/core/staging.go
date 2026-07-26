package core

import (
	"fmt"
	"os"
	"path/filepath"
)

// stagingDirName is the data-dir subdirectory holding in-flight downloads and
// archive extraction, per the layout in docs/PRD.md.
const stagingDirName = "downloads"

// stagingRoot returns where this service stages downloads and extraction, or ""
// when no data dir is configured (newStagingDir then falls back to $TMPDIR).
func (s *Service) stagingRoot() string {
	if s.dataDir == "" {
		return ""
	}
	return filepath.Join(s.dataDir, stagingDirName)
}

// newStagingDir creates a scratch directory under root, creating root itself if
// needed. An empty root falls back to the OS temp dir.
//
// Staging under the data dir rather than $TMPDIR matters for large mods: /tmp is
// tmpfs on most modern distros, so a multi-GB archive would be downloaded and
// extracted in RAM. The data dir is also on the same filesystem as the cache the
// content is ultimately committed into.
//
// Callers own the returned directory and must remove it.
func newStagingDir(root, pattern string) (string, error) {
	if root != "" {
		// 0700, not 0755: in-flight downloads and extracted mod trees live here.
		// Inheriting privacy from the data dir is not enough — an install predating
		// the data dir being tightened may still be 0755.
		if err := os.MkdirAll(root, 0700); err != nil {
			return "", fmt.Errorf("creating staging directory: %w", err)
		}
		if err := os.Chmod(root, 0700); err != nil {
			return "", fmt.Errorf("restricting staging directory: %w", err)
		}
	}

	dir, err := os.MkdirTemp(root, pattern)
	if err != nil {
		return "", fmt.Errorf("creating staging directory: %w", err)
	}
	return dir, nil
}
