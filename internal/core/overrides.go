package core

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

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
func applyProfileOverrides(game *domain.Game, profile *domain.Profile, originals *originalsStore) (warnings []string, err error) {
	if len(profile.Overrides) == 0 {
		return nil, nil
	}
	// filepath.Abs("") is the working directory: never write there.
	if game.InstallPath == "" {
		return nil, fmt.Errorf("game %q has no install path to write profile overrides into", game.ID)
	}
	base, err := filepath.Abs(game.InstallPath)
	if err != nil {
		return nil, fmt.Errorf("resolving game path: %w", err)
	}
	base = filepath.Clean(base)
	// Containment by path text alone is not enough (#621 review N2): a
	// directory symlink inside the game directory would carry the write
	// outside it, and a mod file deployed as a symlink (the default link
	// method) or a hardlink would be written through into the shared mod
	// cache. So the game directory and each destination's parent are
	// resolved through their symlinks and must stay inside the resolved
	// game directory, and a destination is never written through a link.
	realBase, err := resolveExisting(base)
	if err != nil {
		return nil, fmt.Errorf("resolving game path: %w", err)
	}
	// Each override stands on its own (#621 final review): one that is
	// refused or skipped is reported, naming the file and why, and every
	// other one is still written. In path order, so the warnings read the
	// same on every run.
	paths := make([]string, 0, len(profile.Overrides))
	for relPath := range profile.Overrides {
		paths = append(paths, relPath)
	}
	sort.Strings(paths)
	for _, relPath := range paths {
		if err := applyOneOverride(base, realBase, relPath, profile, originals); err != nil {
			warnings = append(warnings, fmt.Sprintf("override %s skipped: %v", relPath, err))
		}
	}
	return warnings, nil
}

// errModFileOverride is the reason an override of a deployed mod's own
// file is skipped: writing it would go through the link into lmm's shared
// mod cache. Breaking the link to hold the override instead is #630.
var errModFileOverride = errors.New("this file belongs to a deployed mod; lmm cannot override a mod's own files yet")

// applyOneOverride writes one override, or says why it did not.
func applyOneOverride(base, realBase, relPath string, profile *domain.Profile, originals *originalsStore) error {
	dest, rel, err := overrideDest(base, relPath)
	if err != nil {
		return err
	}
	realDest, replaceLink, err := containedOverrideDest(realBase, dest, relPath)
	if err != nil {
		return err
	}
	dir := filepath.Dir(realDest)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating its directory: %w", err)
	}
	if replaceLink {
		if err := os.Remove(realDest); err != nil {
			return fmt.Errorf("replacing the link there: %w", err)
		}
	} else {
		captureOverriddenOriginal(originals, profile.Name, rel, realDest)
	}
	// O_NOFOLLOW: a symlink swapped in after the checks above is refused
	// by the open itself rather than followed. No O_TRUNC: the open file's
	// own link count is checked first, so a hardlink made after the checks
	// is refused before anything in its shared data changes.
	f, err := os.OpenFile(realDest, os.O_WRONLY|os.O_CREATE|syscall.O_NOFOLLOW, 0o644)
	if err != nil {
		return fmt.Errorf("writing it: %w", err)
	}
	if info, serr := f.Stat(); serr == nil {
		if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Nlink > 1 {
			_ = f.Close()
			return errModFileOverride
		}
	}
	if terr := f.Truncate(0); terr != nil {
		_ = f.Close()
		return fmt.Errorf("writing it: %w", terr)
	}
	if _, werr := f.Write(profile.Overrides[relPath]); werr != nil {
		_ = f.Close()
		return fmt.Errorf("writing it: %w", werr)
	}
	if cerr := f.Close(); cerr != nil {
		return fmt.Errorf("writing it: %w", cerr)
	}
	return nil
}

// resolveExisting is path with its deepest existing ancestor resolved
// through every symlink (filepath.EvalSymlinks), and the part that does not
// exist yet joined back on: the real place a write under path lands,
// whether or not path exists.
func resolveExisting(path string) (string, error) {
	existing, rest := filepath.Clean(path), ""
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			break
		}
		rest = filepath.Join(filepath.Base(existing), rest)
		existing = parent
	}
	real, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", err
	}
	return filepath.Join(real, rest), nil
}

// containedOverrideDest resolves dest's parent through its symlinks and
// refuses one that leaves realBase, the resolved game directory. A dest
// that is itself a link is never written through: a symlink whose target
// stays inside the game directory is replaced by a regular file
// (replaceLink); a symlink leading outside it - a mod file deployed as a
// symlink into the mod cache, or a link of the user's own - and a file
// with more than one hard link (a mod file deployed as a hardlink) are
// errModFileOverride.
func containedOverrideDest(realBase, dest, relPath string) (realDest string, replaceLink bool, err error) {
	realDir, err := resolveExisting(filepath.Dir(dest))
	if err != nil {
		return "", false, fmt.Errorf("override path %q: %w", relPath, err)
	}
	if !isWithin(realDir, realBase) {
		return "", false, fmt.Errorf("override path escapes game directory through a symlink: %q", relPath)
	}
	realDest = filepath.Join(realDir, filepath.Base(dest))
	info, err := os.Lstat(realDest)
	if err != nil {
		return realDest, false, nil
	}
	if info.Mode()&os.ModeSymlink == 0 {
		// A file with more than one hard link shares its data with another
		// name - with the default-off hardlink method, the mod cache's copy.
		// Writing it would change that copy too.
		if st, ok := info.Sys().(*syscall.Stat_t); ok && info.Mode().IsRegular() && st.Nlink > 1 {
			return "", false, errModFileOverride
		}
		return realDest, false, nil
	}
	target, err := filepath.EvalSymlinks(realDest)
	if err != nil || !isWithin(target, realBase) {
		// A link out of the game directory: a mod file deployed as a
		// symlink into the mod cache (or a link of the user's own, which
		// is never written through either).
		return "", false, errModFileOverride
	}
	return realDest, true, nil
}

// overrideDest resolves an override key against base, the game's install
// directory, refusing an absolute or empty path and one that escapes base.
// It returns the destination and the cleaned path relative to base.
func overrideDest(base, relPath string) (dest, rel string, err error) {
	cleaned := filepath.Clean(filepath.FromSlash(relPath))
	if cleaned == "" || filepath.IsAbs(cleaned) {
		return "", "", fmt.Errorf("invalid override path: %q", relPath)
	}
	dest = filepath.Clean(filepath.Join(base, cleaned))
	// Ensure dest is under base (no path traversal)
	rel, err = filepath.Rel(base, dest)
	if err != nil {
		return "", "", fmt.Errorf("override path %q: %w", relPath, err)
	}
	if strings.HasPrefix(rel, "..") || rel == ".." {
		return "", "", fmt.Errorf("override path escapes game directory: %q", relPath)
	}
	return dest, rel, nil
}

// checkOverridePath is overrideDest's rule with no game directory at hand:
// what a collection import refuses at plan time (#621 review S6), so an
// override the deploy would refuse never reaches a profile. The path must
// name a file inside the directory it is joined to.
func checkOverridePath(relPath string) error {
	if _, rel, err := overrideDest(string(filepath.Separator)+"game", relPath); err != nil {
		return err
	} else if rel == "." {
		return fmt.Errorf("invalid override path: %q", relPath)
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
		if err := seedCopyOnceFiles(gameCache, a, game, modCacheKey(game.ID, mod.Mod)); err != nil {
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
//
// k names the entry, variant included (#614).
func seedCopyOnceFiles(gameCache *cache.Cache, a adapter.GameAdapter, game *domain.Game, k cache.Key) error {
	sourceID, modID := k.SourceID, k.ModID
	files, err := gameCache.ListFiles(k)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("listing cache files for %s: %w", domain.ModKey(sourceID, modID), err)
	}
	versionDir := gameCache.ModPath(k)
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
