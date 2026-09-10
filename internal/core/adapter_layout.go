// Package core: this file holds the GAME-ADAPTER seam's core-side half
// (#353) - the tree rewriter that applies an adapter.Layout to an extracted
// directory, and the plan-side rewriter that applies the same Layout to a
// path listing so a plan and its ingest cannot disagree.
//
// The split is the design's load-bearing decision
// (docs/plans/2026-09-10-game-adapter-design.md §1): an adapter supplies the
// rule TABLE (NormalizeArchive returns a Layout and touches no disk); core
// EXECUTES it. So the rename/drop/cleanup logic is written once, a bug in it
// is a bug in one place, and internal/adapter never has to import
// internal/linker.
//
// For a generic-files game - every game lmm managed before #353 - the Layout
// is the zero value, Applies() is false, and both rewriters return their
// input untouched without a single syscall. That is what makes "the entire
// golden set passes with no re-recording" a proof rather than a hope.
package core

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/linker"
)

// archiveLayout asks game's adapter how members should be laid out inside
// the cache entry. modName may be empty when the caller has not derived one
// yet (the download path names the mod from its source, not its archive).
func (s *Service) archiveLayout(game *domain.Game, modName string, members []string) (adapter.Layout, error) {
	a, err := s.AdapterFor(game)
	if err != nil {
		return adapter.Layout{}, err
	}
	return a.NormalizeArchive(adapter.NormalizeRequest{Game: game, ModName: modName, Members: slashMembers(members)})
}

// slashMembers converts a member list to the slash-separated form
// adapter.NormalizeRequest.Members and adapter.FileRouter.RouteFile are
// both documented to receive (#411, M8).
//
// It is THE conversion point: core produces member lists with filepath.Rel
// and filepath.WalkDir, which are OS-separated, and an adapter that
// string-matches a member ("BepInEx/config/") must not have to care which
// platform it is running on. Identical to its input on Linux, which is why
// nothing caught the drift.
func slashMembers(members []string) []string {
	out := make([]string, len(members))
	for i, m := range members {
		out[i] = filepath.ToSlash(m)
	}
	return out
}

// rewriteExtractedTree applies layout to the ALREADY-EXTRACTED tree at root,
// renaming and dropping members on disk, and returns the resulting
// root-relative member list.
//
// The identity Layout returns members unchanged and touches nothing, so the
// only game that pays for this is one whose adapter actually has an opinion.
//
// The whole table is validated first (validateLayoutTable): a rewritten path
// that escapes root is refused exactly as the extractor refuses an escaping
// archive member, and so is a table whose renames would destroy one
// another. An adapter is in-tree code, but the containment rule and the
// executability rule are core's to enforce, because the write is core's -
// and enforcing them up front is what makes a refusal leave the staging
// tree byte-identical.
func rewriteExtractedTree(root string, layout adapter.Layout, members []string) ([]string, error) {
	if !layout.Applies() {
		return members, nil
	}
	// #411 (I2/I3): the WHOLE table is checked before the first rename, so
	// a table that cannot be executed - colliding destinations, a chain, an
	// escaping path - is refused with the staging tree untouched rather
	// than half-applied.
	if err := validateLayoutTable(root, layout, members, caseInsensitiveRoot(root, members)); err != nil {
		return nil, err
	}
	kept := make([]string, 0, len(members))
	// vacated is every member this table moved away or dropped, spelled as
	// it arrived. It is the bound CleanupEmptyDirs needs (#415): the sweep
	// removes only the directories lmm itself emptied, never every empty
	// directory under root.
	var vacated []string
	for _, m := range members {
		dest, keep := layout.Rewrite(m)
		src := filepath.Join(root, filepath.FromSlash(m))
		if !keep {
			if err := os.Remove(src); err != nil && !os.IsNotExist(err) {
				return nil, fmt.Errorf("dropping %s: %w", m, err)
			}
			vacated = append(vacated, m)
			continue
		}
		if dest == m {
			kept = append(kept, m)
			continue
		}
		dst := filepath.Join(root, filepath.FromSlash(dest))
		if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			return nil, fmt.Errorf("preparing %s: %w", dest, err)
		}
		if err := os.Rename(src, dst); err != nil {
			return nil, fmt.Errorf("moving %s to %s: %w", m, dest, err)
		}
		kept = append(kept, dest)
		vacated = append(vacated, m)
	}
	if len(vacated) > 0 {
		// The same sweep every removal path already runs, for the same
		// reason: a rename out of "plugins/" must not leave an empty
		// "plugins/" behind for the linker to deploy as a stray directory.
		// The sweep runs after the whole loop, so a directory several
		// members shared is only empty - and only removed - once the last
		// of them has left it.
		linker.CleanupEmptyDirs(root, vacated)
	}
	slices.Sort(kept)
	return slices.Compact(kept), nil
}

// rewritePlannedPaths is rewriteExtractedTree's PURE twin: the same Layout
// applied to a path listing, for the plan half of an import. Plan and ingest
// share the Layout, so a plan can never promise a path the ingest will
// place somewhere else.
func rewritePlannedPaths(layout adapter.Layout, paths []string) []string {
	if !layout.Applies() {
		return paths
	}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		dest, keep := layout.Rewrite(p)
		if !keep {
			continue
		}
		out = append(out, dest)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// containedIn refuses a rewritten member that would land outside root,
// naming the member that asked for it.
//
// The lexical half (an absolute path, a leading "..") is not enough:
// os.MkdirAll and os.Rename both FOLLOW symlinks, so a destination routed
// through a link already in the staging tree - and an archive can carry
// one, this is not only an adapter's doing - escapes the cache entry
// without a single ".." in its path. So the destination's existing ancestry
// is resolved with filepath.EvalSymlinks and re-checked, which is the
// stronger guarantee the extractor's own sanitizePath already gives.
func containedIn(root, kind, member, rel string) error {
	refuse := func(reason string) error {
		return &AdapterLayoutError{Kind: kind, Reason: reason, Dest: rel, Members: []string{member}}
	}
	// rel arrives canonical (adapter.Layout.Rewrite cleans it, #411 R1), so
	// the unusable shapes are exactly these three: nothing, the cache
	// entry's own root - a rename onto the staging directory itself - and
	// an absolute path.
	if rel == "" || rel == "." || filepath.IsAbs(filepath.FromSlash(rel)) {
		return refuse("unusable destination path")
	}
	cleaned := filepath.Clean(filepath.FromSlash(rel))
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return refuse("destination escaping the cache entry")
	}
	ok, err := resolvesWithin(root, filepath.Dir(cleaned))
	if err != nil {
		return fmt.Errorf("resolving destination %q: %w", rel, err)
	}
	if !ok {
		return refuse("destination escaping the cache entry through a symlink")
	}
	return nil
}

// resolvesWithin reports whether dir - a root-relative directory path that
// need not exist yet - resolves to a location inside root once every
// symlink in its EXISTING prefix is followed.
//
// Only the existing prefix can be resolved, because the rest is what
// os.MkdirAll is about to create; anything MkdirAll creates lands inside
// that prefix by construction, so resolving it is sufficient. The
// destination's own final component is deliberately not resolved: os.Rename
// replaces a symlink at the destination rather than writing through it.
func resolvesWithin(root, dir string) (bool, error) {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false, err
	}
	current := realRoot
	if dir != "." {
		for _, part := range strings.Split(dir, string(filepath.Separator)) {
			next := filepath.Join(current, part)
			if _, lerr := os.Lstat(next); lerr != nil {
				// The rest does not exist; MkdirAll will create it under
				// current, which is already resolved.
				break
			}
			resolved, rerr := filepath.EvalSymlinks(next)
			if rerr != nil {
				return false, rerr
			}
			current = resolved
		}
	}
	within, err := filepath.Rel(realRoot, current)
	if err != nil {
		return false, err
	}
	return within == "." || (within != ".." && !strings.HasPrefix(within, ".."+string(filepath.Separator))), nil
}

// routeDeployables drops from files every member the game's adapter routes
// somewhere other than the linker (#353). RouteCopyOnce and RouteSkip files
// stay in the cache entry; they are simply not the linker's to deploy.
//
// An adapter with no FileRouter - which is every adapter U1 ships - routes
// everything RouteLink, so this returns its input unchanged and allocates
// nothing.
func routeDeployables(a adapter.GameAdapter, game *domain.Game, files []string) []string {
	rt, ok := a.(adapter.FileRouter)
	if !ok {
		return files
	}
	out := make([]string, 0, len(files))
	for _, f := range files {
		// The adapter is asked in slash form (M8); the value KEPT is the
		// caller's own, because it goes on to a filepath.Join.
		if rt.RouteFile(game, filepath.ToSlash(f)) == adapter.RouteLink {
			out = append(out, f)
		}
	}
	return out
}

// adapterCopyOnceFiles is routeDeployables' complement: the members the
// adapter wants written as real, never-overwritten files, which is
// applyProfileOverrides' existing semantics reused (design §1, decision 4).
// Empty for every adapter U1 ships.
func adapterCopyOnceFiles(a adapter.GameAdapter, game *domain.Game, files []string) []string {
	rt, ok := a.(adapter.FileRouter)
	if !ok {
		return nil
	}
	var out []string
	for _, f := range files {
		if rt.RouteFile(game, filepath.ToSlash(f)) == adapter.RouteCopyOnce {
			out = append(out, f)
		}
	}
	return out
}

// rewriteStagedExtract is the download path's use of the tree rewriter: it
// lists the pristine extraction directory, asks the adapter for a Layout
// over those members, and applies it in place.
//
// It re-lists rather than taking a member slice because the download path's
// caller walks the tree afterwards anyway; keeping the listing here means
// the adapter and the walk see the same tree, in that order.
func (s *Service) rewriteStagedExtract(game *domain.Game, root string) error {
	members, err := relativeFileMembers(root)
	if err != nil {
		return fmt.Errorf("listing extracted members: %w", err)
	}
	members = slashMembers(members)
	layout, err := s.archiveLayout(game, "", members)
	if err != nil {
		return err
	}
	if !layout.Applies() {
		return nil
	}
	_, err = rewriteExtractedTree(root, layout, members)
	return err
}

// rewriteExtracted is the ARCHIVE-IMPORT path's use of the tree rewriter,
// the twin of Service.rewriteStagedExtract: it runs against the staging
// directory `lmm import <archive>` extracts into, before the mod name is
// derived from that tree.
//
// It lives on Importer rather than Service because an Importer carries its
// game's resolved adapter (a standalone NewImporter carries the identity),
// which is what keeps this path working for the one Importer built without
// service context.
func (i *Importer) rewriteExtracted(game *domain.Game, modName, root string) error {
	members, err := relativeFileMembers(root)
	if err != nil {
		return fmt.Errorf("listing extracted members: %w", err)
	}
	members = slashMembers(members)
	layout, err := i.adapter.NormalizeArchive(adapter.NormalizeRequest{Game: game, ModName: modName, Members: members})
	if err != nil {
		return fmt.Errorf("laying out %s: %w", modName, err)
	}
	if !layout.Applies() {
		return nil
	}
	_, err = rewriteExtractedTree(root, layout, members)
	return err
}

// AdapterLayoutError reports an adapter Layout that core refused to
// EXECUTE - a table whose renames cannot all be performed without one of
// them destroying another's file.
//
// It is raised before a single rename runs, so a refused layout always
// leaves the staging tree exactly as the extractor left it. An adapter is
// in-tree code, so this is a bug in the adapter rather than bad user input;
// it is a typed error because a frontend should be able to say WHICH
// members contend rather than print a wall of paths.
type AdapterLayoutError struct {
	// Kind is the Layout's own diagnostic label ("game-root-relative"),
	// so a refusal names the rule that produced the table.
	Kind string
	// Reason is the rule the table broke, in words: "destinations
	// collide", "rewrite chain - a destination is another member's
	// source", "destination escaping the cache entry", or "unusable
	// destination path".
	Reason string
	// Dest is the destination path the offending members contend for.
	Dest string
	// Members are the offending member paths, sorted.
	Members []string
}

// Error renders the refusal, naming the rule, the members and the
// destination they contend for.
func (e *AdapterLayoutError) Error() string {
	return fmt.Sprintf("adapter layout %q: %s: %s -> %q",
		e.Kind, e.Reason, strings.Join(e.Members, ", "), e.Dest)
}

// validateLayoutTable checks the WHOLE rewrite table before rewriteExtractedTree
// performs its first rename, so a table that cannot be executed is refused
// rather than half-applied (#411, I2/I3).
//
// Three rules, each of which a member-by-member os.Rename loop silently got
// wrong:
//
//   - two members may not share a destination - the second rename clobbers
//     the first, and sorting the surviving members hides the loss;
//   - a destination may not be another member's SOURCE - "A->B, B->C"
//     destroys B's bytes and leaves C holding A's, which is worse than
//     loss because the resulting paths look right;
//   - when the staging filesystem is case-insensitive, two destinations
//     differing only in case are the same file, so they collide too. On
//     ext4 they are two distinct files and the table is executable, which
//     is why the caller probes rather than assuming.
//
// Containment is checked here as well, for the same reason: an escaping
// destination discovered halfway down the table would otherwise leave the
// members before it already moved.
func validateLayoutTable(root string, layout adapter.Layout, members []string, caseInsensitive bool) error {
	sources := make(map[string]bool, len(members))
	for _, m := range members {
		sources[m] = true
	}

	// dest -> the members claiming it, in member order.
	claims := make(map[string][]string, len(members))
	var order []string
	for _, m := range members {
		dest, keep := layout.Rewrite(m)
		if !keep || dest == m {
			continue
		}
		if err := containedIn(root, layout.Kind, m, dest); err != nil {
			return err
		}
		// A destination that is some OTHER member's source is a chain: the
		// rename overwrites a file this same table is still going to read.
		if sources[dest] && dest != m {
			return &AdapterLayoutError{
				Kind:    layout.Kind,
				Reason:  "rewrite chain - a destination is another member's source",
				Dest:    dest,
				Members: sortedPair(m, dest),
			}
		}
		key := dest
		if caseInsensitive {
			key = strings.ToLower(dest)
		}
		if _, seen := claims[key]; !seen {
			order = append(order, key)
		}
		claims[key] = append(claims[key], m)
	}

	for _, key := range order {
		claimants := claims[key]
		if len(claimants) < 2 {
			continue
		}
		reason := "destinations collide"
		if caseInsensitive {
			reason = "destinations collide (the staging filesystem is case-insensitive)"
		}
		claimed := slices.Clone(claimants)
		slices.Sort(claimed)
		dest, _ := layout.Rewrite(claimants[0])
		return &AdapterLayoutError{
			Kind:    layout.Kind,
			Reason:  reason,
			Dest:    dest,
			Members: claimed,
		}
	}
	return nil
}

// sortedPair returns a and b sorted, for an error message whose member list
// does not depend on map iteration order.
func sortedPair(a, b string) []string {
	pair := []string{a, b}
	slices.Sort(pair)
	return pair
}

// caseInsensitiveRoot reports whether root's filesystem folds case, probed
// READ-ONLY against a member that is already on disk: a case-flipped name
// that stats to the very same file is the definition of a case-insensitive
// filesystem. A member with no cased letter, or a flipped name that resolves
// to a DIFFERENT file (two genuinely distinct members on ext4), answers
// false.
func caseInsensitiveRoot(root string, members []string) bool {
	for _, m := range members {
		flipped := strings.ToUpper(m)
		if flipped == m {
			flipped = strings.ToLower(m)
		}
		if flipped == m {
			continue
		}
		self, err := os.Lstat(filepath.Join(root, filepath.FromSlash(m)))
		if err != nil {
			continue
		}
		other, err := os.Lstat(filepath.Join(root, filepath.FromSlash(flipped)))
		if err != nil {
			return false
		}
		return os.SameFile(self, other)
	}
	return false
}
