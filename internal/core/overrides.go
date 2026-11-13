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
	// modPathFile answers for realDest, a resolved path in the game
	// directory, when it is a path under the mod path that a
	// deployed_files record of the game, or of a game sharing the mod
	// directory, names: how lmm stands toward the file there, judged as a
	// removal judges it (#630). ok is false for any other path. Nil
	// answers false.
	modPathFile func(realDest string) (mf modPathFile, ok bool)
	// recordCopy records, on the acting profile's deployed_files record
	// of realDest, the fingerprint of the overridden copy just written
	// there (#630), so every removal and deploy judges it lmm's own. Nil
	// records nothing.
	recordCopy func(realDest string)
}

// modPathFile is how lmm stands toward the file at a mod path an override
// lands on (#630), judged as every removal judges a deployed path
// (deployedJudge): only lmm's own file there - its link, its copy, an
// overridden copy it wrote - is replaced without a second thought.
type modPathFile struct {
	// rel is the mod file's deployed path (#629: under the mod path, or
	// under the game root).
	rel domain.DeployPath
	// ours: the file is lmm's own, so replacing it loses nothing.
	ours bool
	// recorded: the acting profile records the path, so the override
	// written there is recorded as that profile's copy - and a removal of
	// it puts back what it replaced.
	recorded bool
	// heldBy names the games sharing the mod directory that record the
	// path: their file, which an override never replaces (#445).
	heldBy []string
	// reason says why a file that is not ours is the user's, as a clause.
	reason string
	// unchecked says why the file could not be judged, or "".
	unchecked string
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
// of the game file it replaces. A file at a mod path that is not lmm's own
// - the user's edit of an overridden copy, or the user's replacement of a
// mod's file - is set aside the same way first (#630 review), so the
// removal that later takes the override away puts it back; one another
// game records is never replaced.
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
		note, err := applyOneOverride(base, realBase, relPath, profile, env)
		switch {
		case err != nil:
			warnings = append(warnings, fmt.Sprintf("override %s skipped: %v", relPath, err))
		case note != "":
			// On the flow's pending list, where it reads after the deploy
			// loop's own notes about the same file ("left in place").
			msg := fmt.Sprintf("override %s: %s", relPath, note)
			if env.originals != nil {
				env.originals.note(msg)
			} else {
				warnings = append(warnings, msg)
			}
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

// applyOneOverride writes one override, or says why it did not. note is
// what a write the user should hear about says: that it set their own file
// aside first.
func applyOneOverride(base, realBase, relPath string, profile *domain.Profile, env overrideEnv) (note string, err error) {
	dest, rel, err := overrideDest(base, relPath)
	if err != nil {
		return "", err
	}
	realDir, err := resolveExisting(filepath.Dir(dest))
	if err != nil {
		return "", fmt.Errorf("override path %q: %w", relPath, err)
	}
	dirRel, ok := dirUnder(realBase, realDir)
	if !ok {
		return "", fmt.Errorf("override path escapes game directory through a symlink: %q", relPath)
	}
	realDest := filepath.Join(realDir, filepath.Base(dest))
	dir, err := openDirBeneath(realBase, dirRel, true)
	if err != nil {
		return "", fmt.Errorf("creating its directory: %w", err)
	}
	defer func() { _ = dir.Close() }()
	name := filepath.Base(dest)
	content := profile.Overrides[relPath]

	st, exists, err := lstatAt(dir, name)
	if err != nil {
		return "", fmt.Errorf("checking it: %w", err)
	}
	var mf modPathFile
	isMod := false
	if exists && env.modPathFile != nil {
		mf, isMod = env.modPathFile(realDest)
	}
	write := func(perm os.FileMode) error {
		if err := replaceAt(dir, name, content, perm); err != nil {
			return fmt.Errorf("writing it: %w", err)
		}
		return nil
	}
	switch {
	case !exists:
		return "", write(0o644)
	case !isRegularOrLink(st):
		// Refused before anything opens it: a FIFO would block the deploy.
		return "", errNotRegularTarget
	case isMod && mf.unchecked != "":
		return "", fmt.Errorf("it could not be checked (%s), so lmm left it", mf.unchecked)
	case isMod && len(mf.heldBy) > 0:
		return "", fmt.Errorf("%s records it too, so lmm left that game's file there", gamesText(mf.heldBy))
	case isMod && mf.ours:
		// lmm's own: its link into the cache (broken into an overridden
		// copy), its hard link or copy, or the overridden copy an earlier
		// deploy wrote. Nothing to preserve - the mod's cache has it.
		if !isRegular(st) || st.Nlink != 1 || !sameContentAt(dir, name, content) {
			if err := write(0o644); err != nil {
				return "", err
			}
		}
		if mf.recorded && env.recordCopy != nil {
			env.recordCopy(realDest)
		}
		return "", nil
	case isLink(st):
		if !linkStaysInside(realDest, realBase, env.cacheRoots) {
			return "", errOverrideThroughLink
		}
		// A link that stays inside the game directory (or points into
		// lmm's cache) is replaced by a regular file; nothing it points at
		// is written.
		return "", write(0o644)
	case st.Nlink > 1 && !isMod:
		return "", errOverrideSharedFile
	}
	// A regular file: a game file, or - at a mod path - the user's own.
	if sameContentAt(dir, name, content) {
		return "", nil
	}
	perm := os.FileMode(st.Mode) & os.ModePerm
	if isMod && mf.recorded {
		// The user's file where the profile's mod deploys (#630 review):
		// set aside where a removal of the mod's file puts it back, then
		// replaced, and recorded as the profile's copy.
		if err := preserveUsersModFile(env.originals, profile.Name, mf.rel, realDest); err != nil {
			return "", err
		}
		if err := write(perm); err != nil {
			return "", err
		}
		if env.recordCopy != nil {
			env.recordCopy(realDest)
		}
		return fmt.Sprintf("%s was yours (%s), so lmm set it aside before writing the override - removing the mod, or `lmm purge`, puts it back",
			mf.rel, mf.reason), nil
	}
	captureOverriddenOriginal(env.originals, profile.Name, rel, realDest)
	return "", write(perm)
}

// preserveUsersModFile sets the user's own file at rel, a mod path, aside
// in the originals store before an override replaces it (#630 review),
// under the mod path's root: the removal that later takes lmm's file there
// away (restoreReplacedOriginal) puts it back. Unlike a game file's
// capture, a failure refuses the override - it is the only copy of the
// user's content - as does an earlier original of the path the store
// already keeps, which it would not replace.
func preserveUsersModFile(originals *originalsStore, profileName string, rel domain.DeployPath, path string) error {
	if originals == nil {
		return errors.New("it is yours, and lmm has nowhere to set it aside first, so it left it")
	}
	root, member := originalRootOf(rel)
	held, err := originals.holds(root, member)
	if err != nil {
		return fmt.Errorf("it is yours, and the originals store could not be read to set it aside first (%v), so lmm left it", err)
	}
	if held {
		return errors.New("it is yours, and lmm already keeps an earlier original of that path, so it could not set this one aside too - move it aside, then deploy again")
	}
	if err := originals.capture(OriginalFile{
		Root: root, RelativePath: member,
		Op: OriginalOpProfileOverride, Profile: profileName,
	}, path); err != nil {
		return fmt.Errorf("it is yours, and it could not be set aside first (%v), so lmm left it", err)
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
				deployedAt := deployedPathAt(game, realMod)
				env.modPathFile = s.modPathJudge(ctx, game, profileName, deployedAt)
				env.recordCopy = func(realDest string) {
					if rel, ok := deployedAt(realDest); ok {
						s.recordOverriddenCopy(ctx, game, profileName, rel, realDest)
					}
				}
			}
		}
	}
	return applyProfileOverrides(game, profile, env)
}

// deployedPathAt resolves a real path in game to the deployed path a mod
// file there has: relative to realMod, the resolved mod path, or - for a
// game with a game root of its own (#629) - the install_path key of its
// path relative to the resolved install_path. A path under the mod path is
// the mod path's, whatever else contains it.
func deployedPathAt(game *domain.Game, realMod string) func(realDest string) (domain.DeployPath, bool) {
	realInstall := ""
	if routesToGameRoot(game) {
		if abs, err := filepath.Abs(game.InstallPath); err == nil {
			if r, err := resolveExisting(abs); err == nil {
				realInstall = r
			}
		}
	}
	return func(realDest string) (domain.DeployPath, bool) {
		if rel, ok := relUnder(realMod, realDest); ok {
			return domain.ModPathFile(rel), true
		}
		if realInstall != "" {
			if rel, ok := relUnder(realInstall, realDest); ok {
				return domain.GameRootFile(rel), true
			}
		}
		return domain.DeployPath{}, false
	}
}

// modPathJudge is overrideEnv.modPathFile for profileName of game, whose
// deployed paths deployedAt resolves: the deployedJudge every removal asks,
// acting for the profile, plus the games sharing the mod directory.
func (s *Service) modPathJudge(ctx context.Context, game *domain.Game, profileName string, deployedAt func(string) (domain.DeployPath, bool)) func(string) (modPathFile, bool) {
	others, othersErr := s.otherGamesRecording(ctx, game)
	jd := deployedJudge{db: s.db, game: game, profile: profileName, cacheRoots: s.cacheRoots(game), others: others}
	return func(realDest string) (modPathFile, bool) {
		rel, ok := deployedAt(realDest)
		if !ok {
			return modPathFile{}, false
		}

		mf := modPathFile{rel: rel}
		if othersErr != nil {
			mf.unchecked = othersErr.Error()
			return mf, true
		}
		states, err := currentStates(ctx, s.db, game, rel)
		if err != nil {
			mf.unchecked = err.Error()
			return mf, true
		}
		mf.heldBy = others.recording(rel)
		if len(states) == 0 && len(mf.heldBy) == 0 {
			return modPathFile{}, false
		}
		for _, st := range states {
			if st.Profile == profileName {
				mf.recorded = true
			}
		}
		j := jd.judge(ctx, rel, realDest)
		switch {
		case j.unchecked:
			mf.unchecked = j.reason
		case j.verdict == deployedOurs:
			mf.ours = true
		default:
			mf.reason = j.reason
			if mf.reason == "" {
				mf.reason = "lmm cannot prove it deployed what is there"
			}
		}
		return mf, true
	}
}

// recordOverriddenCopy re-records profileName's record of rel - a mod file
// under game's mod path - with the fingerprint of the overridden copy now
// at dst (#630): the content lmm wrote there, which is what makes the copy
// lmm's own to every later judge. A profile with no record of the path
// records nothing; a failure is logged, and leaves the copy judged as a
// file lmm cannot vouch for.
func (s *Service) recordOverriddenCopy(ctx context.Context, game *domain.Game, profileName string, rel domain.DeployPath, dst string) {
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
			GameID: game.ID, Profile: profileName, Path: rel,
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
