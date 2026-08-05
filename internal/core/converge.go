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

// ConvergeResult is ConvergeDeployedFiles' outcome. Removed lists every path
// convergence acted on (dryRun=false) or would have acted on (dryRun=true) -
// dry-run detection is identical to the real pass, it just skips the mutation.
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
//  2. Sweep pass: walks gameDir for symlinks with no surviving DB row (the
//     row pass already handled every row path, whether or not it acted on
//     it) whose target resolves under this game's effective cache root and
//     whose target is now missing (dangling). Regular files are NEVER
//     touched by the sweep - only the row pass ever removes non-symlink
//     content, and only when a row says so. A symlink pointing outside the
//     cache root, or one whose target still exists (the merged-pak shape:
//     content deliberately left content-addressed with no row), is left
//     alone.
//
// Every per-item failure (an Undeploy or a sweep os.Remove) is collected and
// returned as one joined error after all mods/paths are processed - it does
// not abort the pass, and the returned *ConvergeResult still reflects
// everything that DID succeed (or, under dryRun, everything that WOULD have
// been attempted). ctx is checked between mods during the row pass and
// periodically during the directory walk.
func (s *Service) ConvergeDeployedFiles(ctx context.Context, game *domain.Game, profileName string, dryRun bool) (*ConvergeResult, error) {
	mods, err := s.GetInstalledMods(game.ID, profileName)
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
	for _, m := range mods {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		files, err := deployableFiles(gameCache, game.ID, m.SourceID, m.ID, m.Version)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue // mod's cache entry is gone: it provides nothing
			}
			return nil, fmt.Errorf("listing deployable files for %s: %w", domain.ModKey(m.SourceID, m.ID), err)
		}
		for _, f := range files {
			provided[f] = true
		}
	}

	method, err := s.GetEffectiveLinkMethod(game, profileName)
	if err != nil {
		return nil, fmt.Errorf("resolving effective link method: %w", err)
	}
	lnk := s.GetLinker(method)

	result := &ConvergeResult{}
	handled := make(map[string]bool) // every row path seen, regardless of disposition
	var errs []error

	// --- Row pass ---
	for _, m := range mods {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		rows, err := s.GetDeployedFilesForMod(game.ID, profileName, m.SourceID, m.ID)
		if err != nil {
			errs = append(errs, fmt.Errorf("getting deployed files for %s: %w", domain.ModKey(m.SourceID, m.ID), err))
			continue
		}
		for _, path := range rows {
			handled[path] = true
			if provided[path] {
				continue
			}

			result.Removed = append(result.Removed, ConvergedFile{
				Path:     path,
				Reason:   fmt.Sprintf("no longer provided by %s/%s", m.SourceID, m.ID),
				SourceID: m.SourceID,
				ModID:    m.ID,
			})
			if dryRun {
				continue
			}

			if err := lnk.Undeploy(filepath.Join(game.ModPath, path)); err != nil {
				errs = append(errs, fmt.Errorf("undeploying %s: %w", path, err))
				continue
			}
			if err := s.db.DeleteDeployedFile(game.ID, profileName, path); err != nil {
				errs = append(errs, fmt.Errorf("deleting deployed-file record for %s: %w", path, err))
			}
		}
	}

	// --- Sweep pass ---
	if err := ctx.Err(); err != nil {
		return result, err
	}
	cacheRoot := filepath.Clean(s.GetGameCachePath(game))
	cacheRootPrefix := cacheRoot + string(filepath.Separator)

	checked := 0
	walkErr := filepath.WalkDir(game.ModPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == game.ModPath && errors.Is(err, fs.ErrNotExist) {
				return fs.SkipAll // never deployed: nothing to sweep
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
		if target != cacheRoot && !strings.HasPrefix(target, cacheRootPrefix) {
			return nil // not under this game's cache root: foreign link
		}

		if _, statErr := os.Stat(path); !errors.Is(statErr, fs.ErrNotExist) {
			return nil // target still exists (or a non-ErrNotExist stat failure): leave it
		}

		result.Removed = append(result.Removed, ConvergedFile{
			Path:   rel,
			Reason: "dangling link into lmm cache",
		})
		if dryRun {
			return nil
		}

		if err := os.Remove(path); err != nil {
			errs = append(errs, fmt.Errorf("removing dangling link %s: %w", rel, err))
			return nil
		}
		_ = s.db.DeleteDeployedFile(game.ID, profileName, rel) // best-effort: no row typically exists here

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
