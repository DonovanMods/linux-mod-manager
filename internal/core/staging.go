package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// stagingDirName is the data-dir subdirectory holding in-flight downloads and
// archive extraction, per the layout in docs/plans/archive/2026-01-22-PRD.md.
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

// NewStagingDir creates a scratch directory under this service's staging
// root and returns its path. The caller OWNS it and must remove it.
//
// It is the exported seam a frontend that stages large content of its own
// needs - `lmm serve`'s archive upload endpoint is its intended consumer,
// which writes a browser-uploaded mod archive here before planning an
// import over it. That content belongs in exactly the place downloads and
// extraction already go, for exactly the reasons in newStagingDir's comment
// (a multi-gigabyte archive must not land in a tmpfs /tmp, and it should
// sit on the same filesystem as the cache it will be committed into), and
// the alternative - a frontend inventing a temp root of its own - would
// silently reintroduce both problems.
//
// pattern is an os.MkdirTemp pattern; a trailing "*" is where the random
// component goes. The directory is created 0700, as is the staging root.
func (s *Service) NewStagingDir(pattern string) (string, error) {
	return newStagingDir(s.stagingRoot(), pattern)
}

// importEntryStagingPrefix starts the directory an archive ingest stages its
// tree in until the flow commits it (#644, ImportOptions.stage).
const importEntryStagingPrefix = "lmm-import-entry-"

// staleImportEntryAge is how old a staged import tree must be before a
// Service opening sweeps it: far longer than any import runs, so another
// lmm process's import in flight is never touched.
const staleImportEntryAge = 24 * time.Hour

// sweepStaleImportEntries removes staged import trees a crashed or killed
// lmm left behind (#644): every flow removes its own, so one older than
// staleImportEntryAge belongs to no running import. Best-effort.
func (s *Service) sweepStaleImportEntries() {
	root := s.stagingRoot()
	if root == "" {
		return
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-staleImportEntryAge)
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), importEntryStagingPrefix) {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		if rerr := os.RemoveAll(filepath.Join(root, e.Name())); rerr != nil {
			s.logger().Debug("sweeping a stale staged import failed", "name", e.Name(), "err", rerr)
		}
	}
}
