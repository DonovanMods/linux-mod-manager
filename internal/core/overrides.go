package core

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
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

// applyAdapterCopyOnce writes the RouteCopyOnce files of the mods just
// deployed into the game directory (#353).
//
// It is applyProfileOverrides' semantics reused rather than reinvented,
// which is the design's decision 4: a real file, copied on FIRST deploy,
// never overwritten, and never entered into deployed_files. The reason is
// identical - the user hand-edits the file after the game's first run, and
// a symlink would push that edit back into the shared cache entry, where it
// would leak into every profile using the same mod version.
//
// Nothing routes RouteCopyOnce in U1 (no adapter shipped here implements
// FileRouter), so this returns immediately for every game today. It is
// wired now, with the seam, because the rule "core owns every side effect"
// is only true if the side effect exists in core before an adapter asks for
// it (design §3 U1's call-site table).
func (s *Service) applyAdapterCopyOnce(game *domain.Game, mods []*domain.InstalledMod) error {
	a, err := s.AdapterFor(game)
	if err != nil {
		return err
	}
	if _, routes := a.(adapter.FileRouter); !routes {
		return nil
	}
	gameCache := s.GetGameCache(game)
	for _, mod := range mods {
		if mod.External {
			continue // lmm never owns an external mod's bytes
		}
		if err := seedCopyOnceFiles(gameCache, a, game, mod.SourceID, mod.ID, mod.Version); err != nil {
			return err
		}
	}
	return nil
}

// seedCopyOnceFiles writes ONE mod's RouteCopyOnce members into the game
// directory, and is the single implementation of that write.
//
// It is called from two places, which between them cover every path that
// deploys a mod (#413, the #358/#359 review's F14 carry-in):
//
//	Installer.Install and Installer.replaceWithCaches, so that `lmm
//	install`, `lmm import`, `lmm update`, `lmm update rollback`, a profile
//	switch, a profile apply and `verify --fix`'s re-deploy every seed
//	without each having to remember to - the same reason the BepInEx
//	version of this lived inside the installer's own file loop;
//
//	Service.applyAdapterCopyOnce, the profile-level sweep `lmm deploy` runs
//	beside applyProfileOverrides, which also covers a mod the deploy left
//	in place rather than re-installing.
//
// Both are safe to run together because the write is idempotent by
// definition: copyOnce is a no-op for a file that already exists, which is
// the whole contract - after the first deploy the file belongs to the user.
//
// A cache entry that is not there is not an error: a RouteCopyOnce sweep
// runs over mods a flow may not have cached (an external mod, a row whose
// entry a prune removed), and reporting that here would duplicate the
// per-file walk's own missing-file finding.
func seedCopyOnceFiles(gameCache *cache.Cache, a adapter.GameAdapter, game *domain.Game, sourceID, modID, version string) error {
	files, err := gameCache.ListFiles(game.ID, sourceID, modID, version)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("listing cache files for %s: %w", domain.ModKey(sourceID, modID), err)
	}
	versionDir := gameCache.ModPath(game.ID, sourceID, modID, version)
	for _, rel := range adapterCopyOnceFiles(a, game, files) {
		// M2: the containment guard belongs BESIDE the write, the way
		// applyProfileOverrides has one for an override path. Cache
		// members are sanitised at extraction, so this is not reachable
		// today - but the adapter tree rewriter can now put a path into a
		// cache entry the extractor never saw.
		dest, err := copyOnceDest(game.ModPath, rel)
		if err != nil {
			return fmt.Errorf("writing %s for %s: %w", rel, domain.ModKey(sourceID, modID), err)
		}
		if err := copyOnce(filepath.Join(versionDir, filepath.FromSlash(rel)), dest); err != nil {
			return fmt.Errorf("writing %s for %s: %w", rel, domain.ModKey(sourceID, modID), err)
		}
	}
	return nil
}

// copyOnceDest resolves a cache-entry-relative member against root and
// refuses one that escapes it, returning the absolute destination path.
// Same rule, same wording, as applyProfileOverrides' own check.
func copyOnceDest(root, rel string) (string, error) {
	base, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolving game path: %w", err)
	}
	base = filepath.Clean(base)
	cleaned := filepath.Clean(filepath.FromSlash(rel))
	if cleaned == "" || cleaned == "." || filepath.IsAbs(cleaned) {
		return "", fmt.Errorf("invalid copy-once path: %q", rel)
	}
	dest := filepath.Clean(filepath.Join(base, cleaned))
	within, err := filepath.Rel(base, dest)
	if err != nil {
		return "", fmt.Errorf("copy-once path %q: %w", rel, err)
	}
	if within == ".." || strings.HasPrefix(within, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("copy-once path escapes game directory: %q", rel)
	}
	return dest, nil
}

// copyOnce copies src to dst unless dst already exists. An existing
// destination is left EXACTLY as it is - that is the whole point of the
// route - and is not an error.
//
// M1: the copy goes into a sibling TEMPORARY file and only becomes dst
// once it is complete, so a kill, a full disk or an I/O error mid-copy can
// never leave a truncated file that copy-once's own never-overwrite
// contract would then refuse to repair on every subsequent deploy. The
// link is what publishes it: unlike a rename it FAILS on an existing
// destination, which makes the existence check and the create one
// operation rather than a TOCTOU pair - and losing that race to another
// writer means the file is there, which is exactly the outcome copy-once
// wants.
func copyOnce(src, dst string) error {
	if _, err := os.Lstat(dst); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(dst)+".lmm-*")
	if err != nil {
		return fmt.Errorf("staging %s: %w", dst, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) //nolint:errcheck // best effort; the link below is what matters
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("staging %s: %w", dst, err)
	}
	if err := copyFileStreaming(src, tmpName); err != nil {
		return err
	}
	// CreateTemp made the file 0600 and copyFileStreaming's O_CREATE mode
	// applies only to a file it creates, so the source's mode is carried
	// across explicitly - the pre-M1 write took it from the source too.
	if info, serr := os.Stat(src); serr == nil {
		if cerr := os.Chmod(tmpName, info.Mode().Perm()); cerr != nil {
			return fmt.Errorf("writing %s: %w", dst, cerr)
		}
	}
	if err := os.Link(tmpName, dst); err != nil {
		if errors.Is(err, fs.ErrExist) {
			// Someone wrote it between the Lstat and here. The existing
			// file wins, which is the route's whole contract.
			return nil
		}
		if !errors.Is(err, errors.ErrUnsupported) {
			return fmt.Errorf("writing %s: %w", dst, err)
		}
		// A filesystem with no hard links (rare, but FUSE mounts exist):
		// fall back to a rename, which is still atomic but cannot refuse
		// an existing destination - so re-check first.
		if _, serr := os.Lstat(dst); serr == nil {
			return nil
		}
		if err := os.Rename(tmpName, dst); err != nil {
			return fmt.Errorf("writing %s: %w", dst, err)
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
