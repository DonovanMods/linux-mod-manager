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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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

// slashMembers normalises a member list into the form
// adapter.NormalizeRequest.Members and adapter.FileRouter.RouteFile are both
// documented to receive: slash-separated (#411, M8) and SORTED (#411, R3).
//
// It is THE normalisation point, for both halves of the same reason. Core
// produces member lists two ways - importDeployablePaths sorts a flat path
// list for the plan, relativeFileMembers walks the tree directory-first for
// the ingest - so an archive holding "a.txt" beside "a/b.txt" reached the
// adapter as [a.txt a/b.txt] from one side and [a/b.txt a.txt] from the
// other, and an adapter whose Layout depends on order would lay the two
// halves out differently. Sorting here gives the two sides one order without
// either caller having to know about the other. The slash conversion is the
// same argument on the separator: an adapter that string-matches a member
// ("BepInEx/config/") must not have to care which platform it runs on.
//
// It returns a new slice, so the caller's own listing keeps its order.
func slashMembers(members []string) []string {
	out := make([]string, len(members))
	for i, m := range members {
		out[i] = filepath.ToSlash(m)
	}
	slices.Sort(out)
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
//
// Validation cannot predict EVERY reason a rename fails, though - a
// destination that is an existing directory, or one nested under another
// destination, are both refused by the kernel rather than by a rule - so
// every move this executor performs is recorded and UNDONE when a later one
// fails (#411, R2). "A failed rewrite leaves the staging tree exactly as the
// extractor left it" is therefore a property of the whole function, not only
// of its typed refusals. Dropped members are moved into a scratch directory
// rather than unlinked, for the same reason: a drop that has already run
// when a later rename fails has to come back.
//
// Members are regular FILES only - relativeFileMembers, the one listing both
// callers use, returns no symlinks - which is what keeps the containment
// check sound even though it runs once, before the first rename: no move
// this loop performs can create a symlink for a later destination to route
// through.
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
	x := &layoutRewrite{root: root}
	defer x.discardScratch()

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
			if err := x.drop(src); err != nil {
				x.undo()
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
		if err := x.mkdirAll(filepath.Dir(dst)); err != nil {
			x.undo()
			return nil, fmt.Errorf("preparing %s: %w", dest, err)
		}
		if err := x.rename(src, dst); err != nil {
			x.undo()
			return nil, fmt.Errorf("moving %s to %s: %w", m, dest, err)
		}
		kept = append(kept, dest)
		vacated = append(vacated, m)
	}
	// The scratch directory holds only dropped members, and the table ran to
	// completion, so nothing in it is coming back.
	x.discardScratch()
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

// layoutRewrite is rewriteExtractedTree's undo log (#411, R2): the moves the
// executor has already performed and the directories it has already created,
// so a rename the whole-table validation could not predict - the kernel
// refusing a destination that is an existing directory, an I/O error - is
// rolled back instead of left half-applied.
//
// It is a log rather than a two-phase swap because the staging tree is
// arbitrarily large: undoing N renames costs N renames, while staging every
// member into a shadow tree and swapping would cost a full second copy
// whenever the two ends straddle a filesystem boundary.
type layoutRewrite struct {
	root string
	// scratch holds dropped members until the table has run to completion.
	// It is created lazily, only for a table that actually drops something,
	// and it lives INSIDE root - the same filesystem, so the move is a
	// rename - under lmm's own reserved ".lmm-" prefix, which an import
	// refuses by name if a killed process ever leaves one behind.
	scratch string
	moves   []layoutMove
	created []string
}

// layoutMove is one rename the executor performed, in the direction it
// performed it.
type layoutMove struct{ from, to string }

// rename performs one move and records it.
func (x *layoutRewrite) rename(src, dst string) error {
	if err := os.Rename(src, dst); err != nil {
		return err
	}
	x.moves = append(x.moves, layoutMove{from: src, to: dst})
	return nil
}

// drop retires a member: it is moved into the scratch directory rather than
// unlinked, so it can be restored if a later move fails. A member that is
// already gone is not an error, exactly as the unlinking version tolerated.
func (x *layoutRewrite) drop(src string) error {
	if _, err := os.Lstat(src); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if x.scratch == "" {
		dir, err := os.MkdirTemp(x.root, ".lmm-layout-drop-")
		if err != nil {
			return err
		}
		x.scratch = dir
	}
	return x.rename(src, filepath.Join(x.scratch, strconv.Itoa(len(x.moves))))
}

// mkdirAll creates dir, recording every component it had to create so undo
// can remove them and leave no empty directory the extractor did not.
func (x *layoutRewrite) mkdirAll(dir string) error {
	var missing []string
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Lstat(d); err == nil {
			break
		}
		missing = append(missing, d)
		if parent := filepath.Dir(d); parent == d {
			break
		}
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	// Shallowest first, so undo - which walks backwards - removes the
	// deepest first.
	for i := len(missing) - 1; i >= 0; i-- {
		x.created = append(x.created, missing[i])
	}
	return nil
}

// undo reverses everything this rewrite has done, in reverse order: moves
// first (which pulls dropped members back out of the scratch directory),
// then the scratch directory itself, then the directories the rewrite
// created, which are empty again by then.
//
// Every step is best-effort: the caller is already returning the error that
// caused the rollback, and an undo that cannot complete must not replace it
// with a less informative one.
func (x *layoutRewrite) undo() {
	for i := len(x.moves) - 1; i >= 0; i-- {
		_ = os.Rename(x.moves[i].to, x.moves[i].from)
	}
	x.moves = nil
	x.discardScratch()
	for i := len(x.created) - 1; i >= 0; i-- {
		_ = os.Remove(x.created[i])
	}
	x.created = nil
}

// discardScratch removes the dropped members for good. Idempotent, so it can
// be both deferred and called on the success path.
func (x *layoutRewrite) discardScratch() {
	if x.scratch == "" {
		return
	}
	_ = os.RemoveAll(x.scratch)
	x.scratch = ""
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
	if errors.Is(err, errUnresolvableAncestor) {
		// A dangling link is not provably an escape, but it is provably
		// not writable-through - os.MkdirAll fails on it too - so it is a
		// refusal, and a refusal is a typed error naming the member rather
		// than a leaked lstat (#411, R4).
		return refuse("destination routed through a symlink that cannot be resolved")
	}
	if err != nil {
		return fmt.Errorf("resolving destination %q: %w", rel, err)
	}
	if !ok {
		return refuse("destination escaping the cache entry through a symlink")
	}
	return nil
}

// errUnresolvableAncestor reports that a component of a destination's
// existing ancestry is a symlink whose target cannot be resolved - a
// dangling link the .7z/.rar extractor restored from the archive. It is a
// sentinel rather than a refusal of its own so that resolvesWithin stays a
// pure containment question and containedIn keeps sole ownership of the
// AdapterLayoutError vocabulary.
var errUnresolvableAncestor = errors.New("destination ancestry contains an unresolvable symlink")

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
			info, lerr := os.Lstat(next)
			if lerr != nil {
				// The rest does not exist; MkdirAll will create it under
				// current, which is already resolved.
				break
			}
			resolved, rerr := filepath.EvalSymlinks(next)
			if rerr != nil {
				if info.Mode()&os.ModeSymlink != 0 {
					return false, errUnresolvableAncestor
				}
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
	// source", "destination escaping the cache entry", "destination
	// escaping the cache entry through a symlink", "destination routed
	// through a symlink that cannot be resolved", or "unusable destination
	// path".
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
