// Package core: this file holds verify's check for a BepInEx/ tree NESTED
// inside BepInEx/plugins/ (#413 final review F4).
//
// A BepInEx/-rooted archive deployed into the plugins directory as packaged
// - which is what a v1 games.yaml with mod_path <install>/BepInEx/plugins
// asks for - becomes BepInEx/plugins/BepInEx/{plugins,config}/. When that
// game later moves its mod_path to the game root without purging first,
// lmm's records are reinterpreted against the new mod_path, so they name
// the CORRECT paths and the old copy stays behind: live, and recorded
// nowhere. BepInEx scans plugins/ recursively, so every plugin in it loads a
// second time beside the one deployed where it belongs, and nothing reads a
// config in it. Every other check passes - the per-file walk asks about the
// cache, convergence's sweep only takes DANGLING links, and the
// misplaced-deployment check reads the records - so without this one the
// state is invisible.
package core

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/linker"
)

// loaderForeignNestedRefusal is the reason a nested tree lmm cannot prove
// is its own carries: the fix is the user's, and it says which one.
const loaderForeignNestedRefusal = "lmm cannot tell that every file in it is its own, so --fix leaves the directory alone - move what belongs to BepInEx up into BepInEx/, or delete the directory"

// nestedLoaderTree is one BepInEx/ directory found inside BepInEx/plugins/.
type nestedLoaderTree struct {
	// rel is the directory, relative to the game's mod_path and
	// slash-separated.
	rel string
	// leftovers are the absolute paths of the links in it that lmm provably
	// created and no profile records.
	leftovers []string
	// foreign counts the files in it nothing proves lmm placed.
	foreign int
}

// loaderNestedTreeCheck reports every BepInEx/ directory nested inside
// BepInEx/plugins/ of a game the bepinex adapter runs for, and on --fix
// removes the ones lmm provably left behind.
//
// "Provably lmm's" is the proof convergeDeployedFiles' sweep already acts
// on: a symlink whose target lies inside lmm's own mod cache. lmm is the
// only thing that links into that directory, and a link a profile still
// RECORDS is not a leftover at all but a deployment the per-mod checks own
// (a BepInEx/-rooted archive whose own plugins/ holds a BepInEx/ directory
// deploys exactly there), so it is left out of the question entirely. A
// regular file, or a link anywhere else, is something lmm cannot tell from
// a user's own hand-extracted archive: the tree is still reported - what
// BepInEx does with it is the same - as a warning, and --fix does not touch
// it. An all-or-nothing verdict per tree, so a repair never leaves half of
// a directory the user is then told to sort out by hand.
//
// A tree with no files in it is not reported: nothing in it loads, and
// nothing in it is misread.
func (r *verifyRun) loaderNestedTreeCheck() {
	for _, tree := range r.nestedLoaderTrees() {
		if err := r.ctx.Err(); err != nil {
			return
		}
		if len(tree.leftovers) == 0 && tree.foreign == 0 {
			continue
		}
		if tree.foreign > 0 {
			r.result.Warnings++
			r.finding(VerifyFinding{
				Status: "loader_foreign_nested_tree",
				Note: fmt.Sprintf("%s/ is a BepInEx/ tree nested inside BepInEx/plugins/, holding %d file(s) lmm has no record of placing - BepInEx loads every plugin anywhere under BepInEx/plugins/, so a plugin in it loads from there (a second time, if it is also installed where it belongs), and BepInEx reads no config in it",
					tree.rel, tree.foreign+len(tree.leftovers)),
				FixableReason: loaderForeignNestedRefusal,
			}, VerifyEvent{})
			continue
		}

		fixing := r.opts.Fix
		r.result.Issues++
		r.finding(VerifyFinding{
			Status: "loader_nested_tree",
			Note: fmt.Sprintf("%s/ is a BepInEx/ tree nested inside BepInEx/plugins/, holding %d link(s) into lmm's mod cache that no profile records - BepInEx loads every plugin anywhere under BepInEx/plugins/, so a plugin in it loads a second time beside the copy deployed where it belongs, and BepInEx reads no config in it",
				tree.rel, len(tree.leftovers)),
			Fixable:       !fixing,
			FixableReason: loaderNestedRefusal(fixing),
		}, VerifyEvent{})
		if fixing {
			r.removeNestedLeftovers(tree)
		}
	}
}

// loaderNestedRefusal is loaderNestedTreeCheck's reason half, on
// staleCompileRefusal's terms.
func loaderNestedRefusal(fixing bool) string {
	if !fixing {
		return ""
	}
	return "this --fix run already removed these links"
}

// nestedLoaderTrees walks <mod_path>/BepInEx/plugins/ for directories named
// BepInEx - in any case, as the layout rules fold it - and classifies each
// outermost one's files. An unreadable directory is skipped rather than
// failing verify, the tolerance every other disk walk in this engine has.
func (r *verifyRun) nestedLoaderTrees() []nestedLoaderTree {
	plugins := filepath.Join(r.game.ModPath, loaderContentRoot, "plugins")
	var trees []nestedLoaderTree
	_ = filepath.WalkDir(plugins, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() && path != plugins {
				return fs.SkipDir
			}
			return nil //nolint:nilerr // an unreadable entry is not a verify failure
		}
		if !d.IsDir() || path == plugins || !strings.EqualFold(d.Name(), loaderContentRoot) {
			return nil
		}
		trees = append(trees, r.classifyNestedTree(path))
		return fs.SkipDir
	})
	return trees
}

// classifyNestedTree sorts every file under dir into lmm's untracked
// leftovers and everything else, leaving out what a profile records.
func (r *verifyRun) classifyNestedTree(dir string) nestedLoaderTree {
	rel, _ := filepath.Rel(r.game.ModPath, dir)
	tree := nestedLoaderTree{rel: filepath.ToSlash(rel)}
	roots := r.svc.cacheRoots(r.game)
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // an unreadable entry is not a verify failure
		}
		fileRel, relErr := filepath.Rel(r.game.ModPath, path)
		if relErr != nil {
			return nil
		}
		if owned, ownErr := r.svc.db.AnyProfileOwnsFile(r.ctx, r.game.ID, filepath.ToSlash(fileRel)); ownErr == nil && owned {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 && linksIntoCache(path, roots) {
			tree.leftovers = append(tree.leftovers, path)
			return nil
		}
		tree.foreign++
		return nil
	})
	return tree
}

// linksIntoCache reports whether the symlink at path points inside one of
// roots - resolved against the link's own directory when relative, and
// whether or not the target still exists.
func linksIntoCache(path string, roots []string) bool {
	target, err := os.Readlink(path)
	if err != nil {
		return false
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(path), target)
	}
	return underAnyCacheRoot(filepath.Clean(target), roots)
}

// removeNestedLeftovers removes tree's leftover links and the directories
// they leave empty, then resolves the row it just reported - or, when a
// removal fails, keeps it an issue and says what failed.
func (r *verifyRun) removeNestedLeftovers(tree nestedLoaderTree) {
	var errs []error
	var removed []string
	roots := r.svc.cacheRoots(r.game)
	for _, path := range tree.leftovers {
		// Re-checked at the moment of removal: only ever a link, and only
		// ever one into lmm's cache.
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&fs.ModeSymlink == 0 || !linksIntoCache(path, roots) {
			continue
		}
		if err := os.Remove(path); err != nil {
			errs = append(errs, err)
			continue
		}
		removed = append(removed, path)
	}
	linker.CleanupEmptyDirs(filepath.Join(r.game.ModPath, loaderContentRoot, "plugins"), removed)

	if err := errors.Join(errs...); err != nil {
		last := &r.result.Findings[len(r.result.Findings)-1]
		last.FixableReason = fmt.Sprintf("this --fix run could not remove every link: %v", err)
		r.emitEv(VerifyEvent{Kind: VerifyEvRepairDetail, Detail: fmt.Sprintf("--fix could not clear %s/: %v", tree.rel, err)})
		return
	}
	r.result.Issues--
	r.resolveLast("fixed_loader_nested_tree", fmt.Sprintf("removed %d untracked link(s) from %s/", len(removed), tree.rel))
	r.emitEv(VerifyEvent{Kind: VerifyEvRepairDetail, Fixed: true, Detail: fmt.Sprintf("cleared %s/", tree.rel)})
}
