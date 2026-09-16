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
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/linker"
)

// loaderForeignNestedRefusal is the reason a nested tree lmm cannot prove
// is its own carries: the fix is the user's, and it says which one - first
// the one that is lmm's to do, since a second games.yaml entry for the same
// install deploys exactly this shape, and deleting its live deployment by
// hand would leave that entry's records pointing at nothing.
const loaderForeignNestedRefusal = "lmm cannot prove every file in it is a leftover of this game's, so --fix leaves the directory alone - if another games.yaml entry for this install deployed it, purge that entry; otherwise move what belongs to BepInEx up into BepInEx/, or delete the directory"

// nestedLoaderTree is one BepInEx/ directory found inside BepInEx/plugins/.
type nestedLoaderTree struct {
	// rel is the directory, relative to the game's mod_path and
	// slash-separated.
	rel string
	// leftovers are the absolute paths of the links in it that lmm provably
	// created for this game and no game records.
	leftovers []string
	// foreign counts the files in it nothing proves are this game's
	// leftovers - and the parts of it that could not be read.
	foreign int
	// base is BepInEx/plugins/ as the classification found it, so the
	// removal can tell that the tree it is about to change is still the
	// one that was classified.
	base fs.FileInfo
}

// loaderNestedTreeCheck reports every BepInEx/ directory nested inside
// BepInEx/plugins/ of a game the bepinex adapter runs for, and on --fix
// removes the ones lmm provably left behind.
//
// Every removal here FAILS CLOSED (#413 fix round 4): a link is removed
// only on positive proof that it is this game's own leftover, and any doubt
// keeps it. The proof has two halves, and a link needs both:
//
//	it is a symlink into THIS game's own part of lmm's cache
//	(ownCacheSubtrees) - lmm is the only thing that links there, and the
//	whole cache is not enough, because every game shares it: a second
//	games.yaml entry for the same install (the v1+v2 migration shape)
//	links into its own subtree from inside this game's directory;
//
//	no game records it (nestedOwnership) - asked of every configured game
//	whose mod_path holds the file, relative to that mod_path. A link THIS
//	game's profiles record is not a leftover but a deployment the per-mod
//	checks own (a BepInEx/-rooted archive whose own plugins/ holds a
//	BepInEx/ directory deploys exactly there), so it is left out of the
//	question entirely; a link ANOTHER game records is that game's
//	deployment, which this game's --fix has no business touching.
//
// Anything else - a regular file, a link anywhere else, another game's
// record, a lookup that failed, a directory that could not be read - is
// something lmm cannot tell from a user's own content: the tree is still
// reported, because what BepInEx does with it is the same, as a warning
// --fix does not act on. The verdict is all-or-nothing per tree, so a
// repair never leaves half of a directory the user is then told to sort
// out by hand.
//
// A --fix the user scoped to one mod (ModFilter: `lmm verify <mod> --fix`,
// the web UI's per-finding Repair) reports a tree and removes nothing: a
// nested tree belongs to no one mod, and a repair wider than the one asked
// for is not one the user asked for.
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
				Note: fmt.Sprintf("%s/ is a BepInEx/ tree nested inside BepInEx/plugins/, holding %d file(s) lmm cannot prove this game left there - BepInEx loads every plugin anywhere under BepInEx/plugins/, so a plugin in it loads from there (a second time, if it is also installed where it belongs), and BepInEx reads no config in it",
					tree.rel, tree.foreign+len(tree.leftovers)),
				FixableReason: loaderForeignNestedRefusal,
			}, VerifyEvent{})
			continue
		}

		scoped := r.opts.ModFilter != ""
		fixing := r.opts.Fix && !scoped
		r.result.Issues++
		r.finding(VerifyFinding{
			Status: "loader_nested_tree",
			Note: fmt.Sprintf("%s/ is a BepInEx/ tree nested inside BepInEx/plugins/, holding %d link(s) into lmm's mod cache that no profile records - BepInEx loads every plugin anywhere under BepInEx/plugins/, so a plugin in it loads a second time beside the copy deployed where it belongs, and BepInEx reads no config in it",
				tree.rel, len(tree.leftovers)),
			Fixable:       !r.opts.Fix && !scoped,
			FixableReason: loaderNestedRefusal(r.opts.Fix, scoped),
		}, VerifyEvent{})
		if fixing {
			r.removeNestedLeftovers(tree)
		}
	}
}

// loaderNestedRefusal is loaderNestedTreeCheck's reason half, on
// staleCompileRefusal's terms.
func loaderNestedRefusal(fixing, scoped bool) string {
	switch {
	case scoped:
		return loaderNestedScopedRefusal
	case fixing:
		return "this --fix run already removed these links"
	}
	return ""
}

// loaderNestedScopedRefusal is the reason a nested tree carries in a run
// scoped to one mod.
const loaderNestedScopedRefusal = "a --fix limited to one mod leaves this directory alone - run `lmm verify --fix` without naming a mod to remove these links"

// nestedLoaderTrees walks <mod_path>/BepInEx/plugins/ for directories named
// BepInEx - in any case, as the layout rules fold it - and classifies each
// outermost one's files. An unreadable directory OUTSIDE every such tree is
// skipped rather than failing verify, the tolerance every other disk walk
// in this engine has: nothing unseen is ever removed. Inside a tree it is
// the opposite (classifyNestedTree).
func (r *verifyRun) nestedLoaderTrees() []nestedLoaderTree {
	if r.svc.nestedTreeHook != nil {
		r.svc.nestedTreeHook("classify")
	}
	plugins := filepath.Join(r.game.ModPath, loaderContentRoot, "plugins")
	base, err := os.Stat(plugins)
	if err != nil {
		return nil
	}
	scopes := r.svc.ownerScopes(r.game)
	own := r.svc.ownCacheSubtrees(r.game)
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
		tree := r.classifyNestedTree(path, scopes, own)
		tree.base = base
		trees = append(trees, tree)
		return fs.SkipDir
	})
	return trees
}

// classifyNestedTree sorts every file under dir into this game's untracked
// leftovers and everything else, leaving out what this game records.
//
// A walk error anywhere in the tree - a directory that cannot be read, an
// entry that vanished mid-walk - is counted as a foreign file: what lmm
// could not see, it cannot say is its own, and an all-or-nothing verdict
// over a tree it saw only part of would remove the part it saw.
func (r *verifyRun) classifyNestedTree(dir string, scopes []ownerScope, own []string) nestedLoaderTree {
	rel, _ := filepath.Rel(r.game.ModPath, dir)
	tree := nestedLoaderTree{rel: filepath.ToSlash(rel)}
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			tree.foreign++
			if d != nil && d.IsDir() && path != dir {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		switch owner, ownErr := r.svc.nestedOwnership(r.ctx, path, scopes); {
		case ownErr != nil || owner == ownedElsewhere:
			tree.foreign++
			return nil
		case owner == ownedHere:
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 && linksInto(path, own) {
			tree.leftovers = append(tree.leftovers, path)
			return nil
		}
		tree.foreign++
		return nil
	})
	return tree
}

// ownCacheSubtrees is every directory THIS game's cached mods can live
// under: its subtree of the global cache, which a game with a per-game
// cache_path still has content in from before that key was set
// (cacheRoots), and the cache_path itself. Not the global root: the other
// games' subtrees are under it too.
func (s *Service) ownCacheSubtrees(game *domain.Game) []string {
	subtrees := []string{filepath.Join(filepath.Clean(s.GlobalCacheDir()), game.ID)}
	if game.CachePath != "" {
		subtrees = append(subtrees, filepath.Clean(game.CachePath))
	}
	return subtrees
}

// linksInto reports whether the symlink at path points strictly inside one
// of dirs - whether or not the target still exists.
func linksInto(path string, dirs []string) bool {
	target, err := os.Readlink(path)
	if err != nil {
		return false
	}
	return targetInside(target, filepath.Dir(path), dirs)
}

// targetInside reports whether a link in linkDir whose target is target
// points strictly inside one of dirs. A relative target is resolved against
// linkDir's REAL directory, which is what the kernel resolves it against;
// one that cannot be resolved proves nothing.
func targetInside(target, linkDir string, dirs []string) bool {
	if !filepath.IsAbs(target) {
		dir, err := filepath.EvalSymlinks(linkDir)
		if err != nil {
			return false
		}
		target = filepath.Join(dir, target)
	}
	target = filepath.Clean(target)
	for _, d := range dirs {
		if strings.HasPrefix(target, d+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// nestedOwner is nestedOwnership's answer.
type nestedOwner int

const (
	// ownedByNoGame: no configured game records the path.
	ownedByNoGame nestedOwner = iota
	// ownedHere: the game being verified records it.
	ownedHere
	// ownedElsewhere: another configured game records it.
	ownedElsewhere
)

// ownerScope is one configured game's claim on the directory tree: the ID
// its deployed_files rows carry, and every spelling its mod_path has - as
// written, and as the directory it resolves to.
type ownerScope struct {
	gameID string
	roots  []string
}

// ownerScopes lists game first, then every other configured game with a
// mod_path.
func (s *Service) ownerScopes(game *domain.Game) []ownerScope {
	scopes := []ownerScope{ownerScopeOf(game)}
	for _, g := range s.ListGames() {
		if g.ID != game.ID && g.ModPath != "" {
			scopes = append(scopes, ownerScopeOf(g))
		}
	}
	return scopes
}

// ownerScopeOf is one game's ownerScope.
func ownerScopeOf(game *domain.Game) ownerScope {
	roots := []string{filepath.Clean(game.ModPath)}
	if real, err := filepath.EvalSymlinks(game.ModPath); err == nil && real != roots[0] {
		roots = append(roots, real)
	}
	return ownerScope{gameID: game.ID, roots: roots}
}

// nestedOwnership asks every game in scopes whose mod_path holds path
// whether any of its profiles records it, relative to that mod_path. The
// path is asked about as written and as the file it resolves to, so a
// mod_path spelled through a symlink is not a way around the question.
//
// An error is never an answer: the caller must treat it as "owned by
// someone" (#413 fix round 4, F4), because the lookup is the proof a
// removal rests on.
func (s *Service) nestedOwnership(ctx context.Context, path string, scopes []ownerScope) (nestedOwner, error) {
	dir, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return ownedElsewhere, fmt.Errorf("resolving %s: %w", filepath.Dir(path), err)
	}
	candidates := []string{path}
	if real := filepath.Join(dir, filepath.Base(path)); real != path {
		candidates = append(candidates, real)
	}
	for i, scope := range scopes {
		for _, root := range scope.roots {
			for _, c := range candidates {
				rel, ok := localTo(root, c)
				if !ok {
					continue
				}
				owned, err := s.db.AnyProfileOwnsFile(ctx, scope.gameID, rel)
				if err != nil {
					return ownedElsewhere, err
				}
				if !owned {
					continue
				}
				if i == 0 {
					return ownedHere, nil
				}
				return ownedElsewhere, nil
			}
		}
	}
	return ownedByNoGame, nil
}

// localTo returns path relative to root, slash-separated, when path lies
// strictly inside root.
func localTo(root, path string) (string, bool) {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// removeNestedLeftovers removes tree's leftover links and the directories
// they leave empty, then resolves the row it just reported - or, when a
// removal was refused or failed, keeps it an issue and says why.
//
// Everything the classification established is established AGAIN here,
// immediately before each removal, because another process (the CLI while
// `lmm serve` deploys) can change any of it in between:
//
//   - BepInEx/plugins/ is still the directory that was classified, and the
//     removal is made through a handle on it (os.Root), which no symlink
//     can lead out of;
//   - every directory between it and the link is a real directory - a
//     directory swapped for a symlink would make the same path name a file
//     somewhere else (F5);
//   - the path is still a symlink into this game's own cache;
//   - no game records it (F4), and the lookup succeeded.
//
// A path that stopped being a leftover (replaced by a file, re-pointed,
// recorded, gone) is left where it is without comment: it is no longer
// this repair's business. A path the checks could not clear - a lookup
// that failed, a directory that is no longer real - is kept AND reported.
func (r *verifyRun) removeNestedLeftovers(tree nestedLoaderTree) {
	if r.svc.nestedTreeHook != nil {
		r.svc.nestedTreeHook("remove")
	}
	plugins := filepath.Join(r.game.ModPath, loaderContentRoot, "plugins")
	var errs []error
	var removed []string
	root, err := r.openClassifiedBase(plugins, tree.base)
	if err != nil {
		errs = append(errs, err)
	} else {
		defer root.Close()
		scopes := r.svc.ownerScopes(r.game)
		own := r.svc.ownCacheSubtrees(r.game)
		for _, path := range tree.leftovers {
			rel, err := filepath.Rel(plugins, path)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			still, err := r.stillALeftover(root, plugins, rel, path, scopes, own)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if !still {
				continue
			}
			if err := root.Remove(rel); err != nil {
				errs = append(errs, err)
				continue
			}
			removed = append(removed, path)
		}
	}
	linker.CleanupEmptyDirs(plugins, removed)

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

// openClassifiedBase opens plugins as an os.Root, refusing when it is no
// longer the directory the classification saw.
func (r *verifyRun) openClassifiedBase(plugins string, classified fs.FileInfo) (*os.Root, error) {
	root, err := os.OpenRoot(plugins)
	if err != nil {
		return nil, err
	}
	now, err := root.Stat(".")
	if err != nil || classified == nil || !os.SameFile(now, classified) {
		_ = root.Close()
		return nil, fmt.Errorf("%s is no longer the directory verify checked; nothing in it was removed", plugins)
	}
	return root, nil
}

// stillALeftover re-establishes, through root (which is plugins), that
// rel is still one of this game's unrecorded leftover links. false with no
// error is "no longer a leftover"; an error is a doubt the caller must
// report.
func (r *verifyRun) stillALeftover(root *os.Root, plugins, rel, path string, scopes []ownerScope, own []string) (bool, error) {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for i := 1; i < len(parts); i++ {
		dir := filepath.Join(parts[:i]...)
		info, err := root.Lstat(dir)
		if err != nil {
			return false, fmt.Errorf("%s: %w", path, err)
		}
		if !info.IsDir() {
			return false, fmt.Errorf("%s: %s is no longer a real directory", path, filepath.Join(plugins, dir))
		}
	}
	info, err := root.Lstat(rel)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	if info.Mode()&fs.ModeSymlink == 0 {
		return false, nil
	}
	target, err := root.Readlink(rel)
	if err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	if !targetInside(target, filepath.Dir(path), own) {
		return false, nil
	}
	owner, err := r.svc.nestedOwnership(r.ctx, path, scopes)
	if err != nil {
		return false, fmt.Errorf("%s: could not tell whether a game records it: %w", path, err)
	}
	return owner == ownedByNoGame, nil
}
