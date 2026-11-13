package core

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/db"
)

// overrideEnv is what writing a profile's overrides needs to know beyond
// the game's directory. The zero value knows nothing: no originals store,
// no cache, and no deployed mod file anywhere - what the path-traversal
// unit tests pass.
type overrideEnv struct {
	// originals is #350's originals store: an override written over an
	// EXISTING game file (the common case - an INI the game shipped)
	// replaces content lmm cannot otherwise reconstruct, so the original
	// is copied aside first, under OriginalRootInstallPath. A capture
	// failure is logged by the store's own caller path and never fails the
	// write, for the same reason a failed capture never fails a deploy: a
	// backup that blocks the operation it protects is worse than no backup.
	originals *originalsStore
	// cacheRoots is every directory lmm-owned content for the game lives
	// under (Service.cacheRoots): a symlink into one is a mod file
	// deployed as a link.
	cacheRoots []string
	// modFile reports whether realDest, a resolved path in the game
	// directory, is a file a mod deploys: a deployed_files record of the
	// game names it under the mod path. Nil answers no.
	modFile func(realDest string) bool
	// recordCopy records, on the acting profile's deployed_files record
	// of realDest, the fingerprint of the overridden copy just written
	// there (#630), so every removal and deploy judges it lmm's own. Nil
	// records nothing.
	recordCopy func(realDest string)
}

// applyProfileOverrides writes a profile's configuration overrides to the game install directory.
// Each key in profile.Overrides is a path relative to game.InstallPath; the value is written as file content.
// `lmm deploy` applies them after its deploy loop, so INI tweaks and other overrides take effect.
// Paths that escape the game install directory (e.g. ../../../etc/passwd) are rejected to prevent abuse.
//
// An override on a deployed mod's own file (#630) breaks the mod's link
// into an overridden copy rather than writing through it: the file at the
// path becomes a regular file of its own holding the override, and the
// mod's cached copy - shared by every profile - is never touched. Every
// other override replaces what is at its path the same way, atomically
// (writeOverriddenCopy's temporary-file-and-rename), after #350's capture
// of the game file it replaces.
//
// Each override stands on its own (#621 final review): one that is refused
// is reported in warnings, naming the file and why, and every other one is
// still written.
func applyProfileOverrides(game *domain.Game, profile *domain.Profile, env overrideEnv) (warnings []string, err error) {
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
	// outside it. So the game directory and each destination's parent are
	// resolved through their symlinks and must stay inside the resolved
	// game directory, and the parent is then opened one component at a
	// time from the game directory without following a link (#630), so
	// one swapped in after that check is refused rather than followed.
	realBase, err := resolveExisting(base)
	if err != nil {
		return nil, fmt.Errorf("resolving game path: %w", err)
	}
	// In path order, so the warnings read the same on every run.
	paths := make([]string, 0, len(profile.Overrides))
	for relPath := range profile.Overrides {
		paths = append(paths, relPath)
	}
	sort.Strings(paths)
	for _, relPath := range paths {
		if err := applyOneOverride(base, realBase, relPath, profile, env); err != nil {
			warnings = append(warnings, fmt.Sprintf("override %s skipped: %v", relPath, err))
		}
	}
	return warnings, nil
}

// The reasons an override is refused because of what is at its path. A
// mod's own file is never one of them since #630: it is broken into an
// overridden copy instead.
var (
	// errOverrideThroughLink: a symlink lmm did not deploy leads out of
	// the game directory. Writing through it would change whatever it
	// points at, and replacing it would discard a link of the user's.
	errOverrideThroughLink = errors.New("it is a link to a file outside the game directory that lmm did not deploy, and lmm never writes through a link")
	// errOverrideSharedFile: a file with more than one hard link that no
	// mod deployed shares its data with another name, which a write
	// would change too.
	errOverrideSharedFile = errors.New("it shares its data with another file (a hard link) that lmm did not deploy, and lmm never writes through a link")
)

// applyOneOverride writes one override, or says why it did not.
func applyOneOverride(base, realBase, relPath string, profile *domain.Profile, env overrideEnv) error {
	dest, rel, err := overrideDest(base, relPath)
	if err != nil {
		return err
	}
	realDir, err := resolveExisting(filepath.Dir(dest))
	if err != nil {
		return fmt.Errorf("override path %q: %w", relPath, err)
	}
	dirRel, ok := dirUnder(realBase, realDir)
	if !ok {
		return fmt.Errorf("override path escapes game directory through a symlink: %q", relPath)
	}
	realDest := filepath.Join(realDir, filepath.Base(dest))
	dir, err := openDirBeneath(realBase, dirRel, true)
	if err != nil {
		return fmt.Errorf("creating its directory: %w", err)
	}
	defer dir.Close()
	name := filepath.Base(dest)
	content := profile.Overrides[relPath]

	st, exists, err := lstatAt(dir, name)
	if err != nil {
		return fmt.Errorf("checking it: %w", err)
	}
	modFile := exists && env.modFile != nil && env.modFile(realDest)
	perm := os.FileMode(0o644)
	switch {
	case !exists:
	case !isRegularOrLink(st):
		// Refused before anything opens it: a FIFO would block the deploy.
		return errNotRegularTarget
	case isLink(st):
		if !modFile && !linkStaysInside(realDest, realBase, env.cacheRoots) {
			return errOverrideThroughLink
		}
		// lmm's own link into its cache (a mod file deployed as a
		// symlink) is broken into an overridden copy; a link that stays
		// inside the game directory is replaced by a regular file. Either
		// way nothing it points at is written.
	case st.Nlink > 1 && !modFile:
		return errOverrideSharedFile
	case modFile:
		// A mod file deployed as a hard link or a copy, or the overridden
		// copy an earlier deploy wrote: lmm's own, with no stock content
		// to preserve.
	default:
		if sameContentAt(dir, name, content) {
			return nil
		}
		perm = os.FileMode(st.Mode) & os.ModePerm
		captureOverriddenOriginal(env.originals, profile.Name, rel, realDest)
	}
	alreadyCopy := modFile && isRegular(st) && st.Nlink == 1 && sameContentAt(dir, name, content)
	if !alreadyCopy {
		if err := replaceAt(dir, name, content, perm); err != nil {
			return fmt.Errorf("writing it: %w", err)
		}
	}
	if modFile && env.recordCopy != nil {
		env.recordCopy(realDest)
	}
	return nil
}

// linkStaysInside reports whether the symlink at path is one an override
// may replace by a regular file without asking whose it is: it resolves
// inside realBase, the resolved game directory, or it points into one of
// lmm's cache roots (a mod's link, whose record a prune may have lost).
func linkStaysInside(path, realBase string, cacheRoots []string) bool {
	if target, err := os.Readlink(path); err == nil {
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(path), target)
		}
		if underAnyCacheRoot(filepath.Clean(target), cacheRoots) {
			return true
		}
	}
	target, err := filepath.EvalSymlinks(path)
	return err == nil && isWithin(target, realBase)
}

// applyOverrides is applyProfileOverrides for profileName of game, with
// everything the Service knows: the originals store, the cache roots, and
// the deployed_files records that make a path a mod's own file - on the
// acting profile's record of which an overridden copy's fingerprint is
// kept (#630).
func (s *Service) applyOverrides(ctx context.Context, game *domain.Game, profileName string, profile *domain.Profile) ([]string, error) {
	env := overrideEnv{originals: s.originalsStoreFor(game.ID), cacheRoots: s.cacheRoots(game)}
	if s.db != nil && game.ModPath != "" {
		if absMod, err := filepath.Abs(game.ModPath); err == nil {
			if realMod, err := resolveExisting(absMod); err == nil {
				modRel := func(realDest string) (string, bool) { return relUnder(realMod, realDest) }
				env.modFile = func(realDest string) bool {
					rel, ok := modRel(realDest)
					if !ok {
						return false
					}
					states, err := currentStates(ctx, s.db, game, rel)
					return err == nil && len(states) > 0
				}
				env.recordCopy = func(realDest string) {
					if rel, ok := modRel(realDest); ok {
						s.recordOverriddenCopy(ctx, game, profileName, rel, realDest)
					}
				}
			}
		}
	}
	return applyProfileOverrides(game, profile, env)
}

// recordOverriddenCopy re-records profileName's record of rel - a mod file
// under game's mod path - with the fingerprint of the overridden copy now
// at dst (#630): the content lmm wrote there, which is what makes the copy
// lmm's own to every later judge. A profile with no record of the path
// records nothing; a failure is logged, and leaves the copy judged as a
// file lmm cannot vouch for.
func (s *Service) recordOverriddenCopy(ctx context.Context, game *domain.Game, profileName, rel, dst string) {
	states, err := currentStates(ctx, s.db, game, rel)
	if err != nil {
		s.log.Warn("could not read the record of an overridden mod file", "path", dst, "err", err)
		return
	}
	for _, st := range states {
		if st.Profile != profileName {
			continue
		}
		fp, err := fingerprintFile(dst)
		if err != nil {
			s.log.Warn("could not fingerprint an overridden mod file", "path", dst, "err", err)
			return
		}
		if err := s.db.RecordDeployedFile(ctx, db.DeployedFileRecord{
			GameID: game.ID, Profile: profileName, RelativePath: rel,
			SourceID: st.SourceID, ModID: st.ModID,
			ModPath: recordRoot(game), Fingerprint: fp,
		}); err != nil {
			s.log.Warn("could not record an overridden mod file", "path", dst, "err", err)
		}
		return
	}
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
