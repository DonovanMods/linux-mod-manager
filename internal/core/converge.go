// Package core provides business logic orchestration for lmm.
// converge.go holds ConvergeDeployedFiles (#168/#212): a remove-only
// reconciliation of a profile's deployed state against current reality. It
// never deploys and never modifies file content - the counterpart deploy
// path (deployableFiles, #210) decides what SHOULD be linked; convergence
// only ever removes things that should no longer be there.
package core

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
)

// ConvergedFile describes one game-directory path convergence found to be
// stale, either because a DB row says a mod owns it but that mod no longer
// provides it (SourceID/ModID set), or because it is a dangling symlink into
// an lmm cache root with no DB row at all (SourceID/ModID empty - a "sweep
// find").
type ConvergedFile struct {
	Path     string // game-dir-relative
	Reason   string // "no longer provided by <source>/<mod>" | "dangling link into lmm cache"
	SourceID string // owning mod when known ("" for sweep finds with no row)
	ModID    string
}

// ConvergeResult is ConvergeDeployedFiles' outcome. Removed's meaning
// depends on dryRun: with dryRun=true it lists every candidate detection
// would act on (no mutation happened, so nothing can have failed); with
// dryRun=false it lists ONLY paths that were SUCCESSFULLY removed - a path
// whose Undeploy or os.Remove failed is never added here, even though it
// was a genuine candidate. Callers that report counts ("removed N") must
// read Removed's length, not the number of candidates detection found;
// per-item failures are surfaced via the returned joined error instead.
type ConvergeResult struct {
	Removed []ConvergedFile
}

// ConvergeDeployedFiles reconciles gameDir/profileName's on-disk state
// against current reality in two passes, remove-only:
//
//  1. Row pass: every deployed_files row whose path is no longer in the
//     UNION of every installed mod's (enabled AND disabled) current
//     deployableFiles set is stale and gets undeployed + its row deleted.
//     The union guard is deliberate: a path stays untouched as long as ANY
//     installed mod still claims it, even if the row's own owning mod no
//     longer does - this protects in-flight ownership churn (a file that
//     changed hands between mods, or is about to on the next deploy) from
//     being yanked out from under a still-valid claim.
//
//     A mod whose cache entry is WHOLLY absent (deployableFiles returns
//     fs.ErrNotExist, not "zero files") is a special case (fix round 2
//     Finding 2): it is UNKNOWN provenance, never "provides nothing" - the
//     same principle as #144's bare-marker rule. Judging it "provides
//     nothing" would delete a still-working copy/hardlink deployment (a
//     regular file, not a symlink - the row pass is the only thing that
//     could ever remove it) exactly when verify couldn't repair the cache
//     that would let it prove otherwise. Such a mod's rows are therefore
//     skipped entirely in the row pass - never undeployed, never marked
//     handled, and never judged by bookkeeping (provenance being unknown
//     means there is nothing to judge THEM by). Deliberately NOT marked
//     handled (round 2 correction: an earlier draft of this fix did mark
//     them handled, which wrongly let a still-dangling symlink among these
//     rows survive forever): each such path instead falls through
//     unclaimed into the sweep pass below, which judges it purely by
//     PHYSICAL evidence instead - a dangling cache-pointing symlink is
//     itself its own provenance and gets swept (best-effort cleaning up
//     its now-orphaned row too), while a regular file (a copy/hardlink
//     deployment) is never a sweep candidate at all and simply survives,
//     row intact.
//
//  2. Sweep pass: walks gameDir for symlinks with no CLAIMED DB row - "no
//     surviving row" (a row the row pass judged and kept, or deleted) OR
//     "an unknown-provenance mod's row, deliberately left unclaimed" (the
//     round-2 Finding 2 case above) - whose target resolves under this
//     game's effective cache root(s) and whose target is now missing
//     (dangling). Regular files are NEVER touched by the sweep - only the
//     row pass ever removes non-symlink content, and only when a row says
//     so. A symlink pointing outside every cache root, or one whose target
//     still exists (the merged-pak shape: content deliberately left
//     content-addressed with no row), is left alone. "Cache root(s)" is
//     plural (fix round 2 Finding 1): a game with a per-game CachePath
//     override still keeps globally-cached content in the GLOBAL cache
//     root too - CachePath augments, it never migrates existing content -
//     so a target is cache-pointing if it falls under EITHER root.
//
// Every per-item failure (an Undeploy or a sweep os.Remove) is collected and
// returned as one joined error after all mods/paths are processed - it does
// not abort the pass, and the returned *ConvergeResult.Removed reflects
// exactly what succeeded (see ConvergeResult's doc comment: a failed item is
// never added to Removed, only to the joined error). ctx is checked between
// mods during the row pass and periodically during the directory walk.
func (s *Service) ConvergeDeployedFiles(ctx context.Context, game *domain.Game, profileName string, dryRun bool) (*ConvergeResult, error) {
	if !dryRun {
		release, err := s.beginOp(ctx)
		if err != nil {
			return nil, err
		}
		defer release()
	}
	return s.convergeDeployedFiles(ctx, game, profileName, dryRun)
}

func (s *Service) convergeDeployedFiles(ctx context.Context, game *domain.Game, profileName string, dryRun bool) (*ConvergeResult, error) {
	mods, err := s.GetInstalledMods(ctx, game.ID, profileName)
	if err != nil {
		return nil, fmt.Errorf("getting installed mods: %w", err)
	}

	gameCache := s.GetGameCache(game)

	// provided is the union of every installed mod's current deployable set
	// (#210's resolver - the same provenance source deploy itself uses),
	// enabled AND disabled: a disabled mod's rows may still linger (disable
	// undeploys files but doesn't always clear every row), and its cache
	// entry may still legitimately claim a path some OTHER mod's row names.
	provided := make(map[string]bool)
	// unknownProvenance holds every mod (by ModKey) whose cache entry is
	// wholly absent - see the row-pass doc above (Finding 2): such a mod's
	// rows must be skipped, not judged "no longer provided".
	unknownProvenance := make(map[string]bool)
	for _, m := range mods {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		files, err := deployableFiles(gameCache, game.ID, m.SourceID, m.ID, m.Version)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				unknownProvenance[domain.ModKey(m.SourceID, m.ID)] = true
				continue // absent cache entry: unknown provenance, not "provides nothing"
			}
			return nil, fmt.Errorf("listing deployable files for %s: %w", domain.ModKey(m.SourceID, m.ID), err)
		}
		for _, f := range files {
			provided[f] = true
		}
	}

	method, err := s.GetEffectiveLinkMethod(ctx, game, profileName)
	if err != nil {
		return nil, fmt.Errorf("resolving effective link method: %w", err)
	}
	lnk := s.GetLinker(method)

	result := &ConvergeResult{}
	handled := make(map[string]bool) // every row path the row pass actually judged (kept or removed)
	var errs []error

	// --- Row pass ---
	for _, m := range mods {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		rows, err := s.GetDeployedFilesForMod(ctx, game.ID, profileName, m.SourceID, m.ID)
		if err != nil {
			errs = append(errs, fmt.Errorf("getting deployed files for %s: %w", domain.ModKey(m.SourceID, m.ID), err))
			continue
		}
		unknown := unknownProvenance[domain.ModKey(m.SourceID, m.ID)]
		for _, path := range rows {
			// A deployed_files row is bookkeeping, not truth: a corrupted or
			// hand-edited relative_path (absolute, or escaping the game dir
			// via "..") must never steer a removal outside game.ModPath.
			// IsLocal is the exact contract needed: relative, no escape, no
			// absolute. The row itself is deliberately left alone here -
			// deleting records based on corrupt data is its own hazard; the
			// error surfaces it to the user as a verify warning via the
			// joined-error path instead.
			if !filepath.IsLocal(path) {
				errs = append(errs, fmt.Errorf("skipping unsafe deployed-file record %q for %s/%s", path, m.SourceID, m.ID))
				continue
			}
			if unknown {
				// Finding 2 (round 2): unknown provenance is never judged by
				// bookkeeping - deliberately NOT marked handled, so this path
				// falls through to the sweep pass, which judges it by
				// physical evidence instead (see the function doc above).
				continue
			}
			handled[path] = true
			if provided[path] {
				continue
			}

			cf := ConvergedFile{
				Path:     path,
				Reason:   fmt.Sprintf("no longer provided by %s/%s", m.SourceID, m.ID),
				SourceID: m.SourceID,
				ModID:    m.ID,
			}
			if dryRun {
				result.Removed = append(result.Removed, cf)
				continue
			}

			if err := lnk.Undeploy(filepath.Join(game.ModPath, path)); err != nil {
				errs = append(errs, fmt.Errorf("undeploying %s: %w", path, err))
				continue
			}
			result.Removed = append(result.Removed, cf)
			if err := s.db.DeleteDeployedFile(ctx, game.ID, profileName, path); err != nil {
				errs = append(errs, fmt.Errorf("deleting deployed-file record for %s: %w", path, err))
			}
		}
	}

	// --- Sweep pass ---
	if err := ctx.Err(); err != nil {
		return result, err
	}
	// cacheRoots (fix round 2 Finding 1): the global cache dir ALWAYS
	// applies, and game.CachePath is added on top when set - a per-game
	// override augments the global root rather than replacing it as a
	// valid home for lmm-owned content, so a target under EITHER root is
	// cache-pointing.
	cacheRoots := []string{filepath.Clean(s.GlobalCacheDir())}
	if game.CachePath != "" {
		cacheRoots = append(cacheRoots, filepath.Clean(game.CachePath))
	}

	checked := 0
	walkErr := filepath.WalkDir(game.ModPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == game.ModPath && errors.Is(err, fs.ErrNotExist) {
				return fs.SkipAll // never deployed: nothing to sweep
			}
			// Fix round 2 Finding 4: a directory read error (e.g. permission
			// denied) must not abort the ENTIRE sweep - record it and skip
			// just that subtree, so a dangling link elsewhere still gets
			// found. WalkDir invokes the callback with d describing the
			// directory itself when its ReadDir call is what failed; any
			// other error (e.g. a non-directory Lstat failure) keeps the
			// prior abort-the-walk behavior.
			if d != nil && d.IsDir() {
				errs = append(errs, fmt.Errorf("reading directory %s: %w", path, err))
				return fs.SkipDir
			}
			return err
		}

		checked++
		if checked%200 == 0 {
			if cerr := ctx.Err(); cerr != nil {
				return cerr
			}
		}

		if d.IsDir() || d.Type()&fs.ModeSymlink == 0 {
			return nil // sweep only ever considers symlinks
		}

		rel, relErr := filepath.Rel(game.ModPath, path)
		if relErr != nil {
			return nil
		}
		if handled[rel] {
			return nil // row pass already decided this path's fate
		}

		target, readErr := os.Readlink(path)
		if readErr != nil {
			return nil
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(path), target)
		}
		target = filepath.Clean(target)
		if !underAnyCacheRoot(target, cacheRoots) {
			return nil // not under any of this game's cache roots: foreign link
		}

		if _, statErr := os.Stat(path); !errors.Is(statErr, fs.ErrNotExist) {
			return nil // target still exists (or a non-ErrNotExist stat failure): leave it
		}

		cf := ConvergedFile{
			Path:   rel,
			Reason: "dangling link into lmm cache",
		}
		if dryRun {
			result.Removed = append(result.Removed, cf)
			return nil
		}

		if err := os.Remove(path); err != nil {
			errs = append(errs, fmt.Errorf("removing dangling link %s: %w", rel, err))
			return nil
		}
		result.Removed = append(result.Removed, cf)
		_ = s.db.DeleteDeployedFile(ctx, game.ID, profileName, rel) // best-effort: no row typically exists here

		return nil
	})
	if walkErr != nil {
		if errors.Is(walkErr, context.Canceled) || errors.Is(walkErr, context.DeadlineExceeded) {
			return result, walkErr
		}
		errs = append(errs, fmt.Errorf("sweeping %s: %w", game.ModPath, walkErr))
	}

	if len(errs) > 0 {
		return result, errors.Join(errs...)
	}
	return result, nil
}

// underAnyCacheRoot reports whether target (already filepath.Clean'd) falls
// under one of roots - either equal to a root, or nested inside it (a
// separator-suffixed prefix match, so "/cache2" is never mistaken for a
// match against root "/cache"). Supports fix round 2 Finding 1: a game with
// a per-game CachePath override must still recognize the GLOBAL cache root
// as its own.
func underAnyCacheRoot(target string, roots []string) bool {
	for _, root := range roots {
		if target == root || strings.HasPrefix(target, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
