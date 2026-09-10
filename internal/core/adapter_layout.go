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
	return a.NormalizeArchive(adapter.NormalizeRequest{Game: game, ModName: modName, Members: members})
}

// rewriteExtractedTree applies layout to the ALREADY-EXTRACTED tree at root,
// renaming and dropping members on disk, and returns the resulting
// root-relative member list.
//
// The identity Layout returns members unchanged and touches nothing, so the
// only game that pays for this is one whose adapter actually has an opinion.
//
// A rewritten path is refused if it escapes root, exactly as the extractor
// refuses an escaping archive member: an adapter is in-tree code, but the
// containment rule is core's to enforce because the write is core's.
func rewriteExtractedTree(root string, layout adapter.Layout, members []string) ([]string, error) {
	if !layout.Applies() {
		return members, nil
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
		if err := containedIn(root, dest); err != nil {
			return nil, err
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

// containedIn refuses a rewritten member that would land outside root.
func containedIn(root, rel string) error {
	if rel == "" || filepath.IsAbs(filepath.FromSlash(rel)) {
		return fmt.Errorf("adapter layout produced an unusable path %q", rel)
	}
	cleaned := filepath.Clean(filepath.FromSlash(rel))
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return fmt.Errorf("adapter layout produced a path escaping the cache entry: %q", rel)
	}
	return nil
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
		if rt.RouteFile(game, f) == adapter.RouteLink {
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
		if rt.RouteFile(game, f) == adapter.RouteCopyOnce {
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
