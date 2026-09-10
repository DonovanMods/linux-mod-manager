package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// applyProfileOverrides writes a profile's configuration overrides to the game install directory.
// Each key in profile.Overrides is a path relative to game.InstallPath; the value is written as file content.
// Used on deploy and profile switch so INI tweaks and other overrides are applied.
// Paths that escape the game install directory (e.g. ../../../etc/passwd) are rejected to prevent abuse.
//
// Unexported since #350: it grew an originals-store parameter, which is a
// core primitive no caller outside this package can construct, and it had
// no caller outside this package anyway.
//
// originals, when non-nil, is #350's originals store: an override written
// over an EXISTING game file (the common case - an INI the game shipped)
// replaces content lmm cannot otherwise reconstruct, so the original is
// copied aside first, under OriginalRootInstallPath. A capture failure is
// logged by the store's own caller path and never fails the write, for the
// same reason a failed capture never fails a deploy: a backup that blocks
// the operation it protects is worse than no backup. nil disables capture,
// which is what the path-traversal unit tests pass.
//
// The store is a PARAMETER rather than resolved inside because this is a
// package-level function with no Service in scope - the deploy flow that
// calls it has one, and hands its own store down.
func applyProfileOverrides(game *domain.Game, profile *domain.Profile, originals *originalsStore) error {
	if len(profile.Overrides) == 0 {
		return nil
	}
	base, err := filepath.Abs(game.InstallPath)
	if err != nil {
		return fmt.Errorf("resolving game path: %w", err)
	}
	base = filepath.Clean(base)
	for relPath, content := range profile.Overrides {
		// Reject absolute or empty paths
		cleaned := filepath.Clean(filepath.FromSlash(relPath))
		if cleaned == "" || filepath.IsAbs(cleaned) {
			return fmt.Errorf("invalid override path: %q", relPath)
		}
		dest := filepath.Join(base, cleaned)
		dest = filepath.Clean(dest)
		// Ensure dest is under base (no path traversal)
		rel, err := filepath.Rel(base, dest)
		if err != nil {
			return fmt.Errorf("override path %q: %w", relPath, err)
		}
		if strings.HasPrefix(rel, "..") || rel == ".." {
			return fmt.Errorf("override path escapes game directory: %q", relPath)
		}
		dir := filepath.Dir(dest)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("creating override dir %s: %w", dir, err)
		}
		captureOverriddenOriginal(originals, profile.Name, rel, dest)
		if err := os.WriteFile(dest, content, 0644); err != nil {
			return fmt.Errorf("writing override %s: %w", relPath, err)
		}
	}
	return nil
}

// captureOverriddenOriginal preserves the game file an override is about to
// replace. Split out of the loop so the "nil store, or nothing there"
// early-outs read as one decision rather than four lines inside a write
// path. rel is already the cleaned, validated path relative to the game's
// install directory (the traversal check above produced it).
func captureOverriddenOriginal(originals *originalsStore, profileName, rel, dest string) {
	if originals == nil {
		return
	}
	if info, err := os.Lstat(dest); err != nil || !info.Mode().IsRegular() {
		// Nothing there, or a link rather than a file: there is no stock
		// content to lose. The store makes the same judgement itself; this
		// avoids the manifest read for the ordinary case.
		return
	}
	if err := originals.capture(OriginalFile{
		Root: OriginalRootInstallPath, RelativePath: filepath.ToSlash(rel),
		Op: OriginalOpProfileOverride, Profile: profileName,
	}, dest); err != nil {
		originals.log.Warn("could not preserve the game file this profile override replaces; it will not be restorable from a snapshot",
			"path", dest, "err", err)
		originals.noteFailure(fmt.Sprintf(
			"could not preserve %s before writing a profile override over it; it will not be restorable from a snapshot: %v", dest, err))
	}
}
