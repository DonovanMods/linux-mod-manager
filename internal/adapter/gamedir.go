package adapter

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// GameOwnsDir reports whether the top-level directory name in gameRoot is
// one the GAME itself owns, as opposed to one lmm deployed there (#424
// review finding 1).
//
// It is a read-only disk probe with no adapter vocabulary in it, and it
// lives in the seam because both sides of the seam ask it (#413 review
// F7): the bepinex adapter's plugin-folder shape must not swallow a
// directory the game ships, and core's misplaced-deploy check must not
// report one as a misplaced plugin. They used to hold two hand-kept copies
// with nothing to say when they drifted; this package is the one both may
// import.
//
// For a game whose mod_path IS its install path, an archive root and the
// game root are ONE namespace: <Game>_Data/ holds assemblies, and so, in
// their own way, do MonoBleedingEdge/, unstripped_corlib/ and
// doorstop_libs/. The test is what the directory actually CONTAINS rather
// than a list of names, which would have to grow with every engine,
// launcher and loader lmm meets. Two halves:
//
//	gameRoot has a directory of this name (matched case-insensitively,
//	because the content very likely came from a Windows-authored archive);
//	AND
//
//	that directory holds at least one file members does not account for.
//
// The second half is the difference between "the game owns this" and "lmm
// put this here": a directory holding EXACTLY the members being classified
// is lmm's own misdeployment, while one holding the whole engine besides is
// the game's. It also answers the question a repair needs - would this
// directory survive an undeploy - without consulting deployed_files, which
// an ingest cannot read for a profile it was never given.
//
// members are slash-separated and relative to gameRoot; only those under
// name count. An empty gameRoot, a name that is not a single path segment,
// and a gameRoot that cannot be listed all answer false - there is no game
// directory to consult, so the member list is all there is. Anything
// unreadable INSIDE a matching directory (a walk error, a permission
// refusal, a symlink that cannot be followed) answers true, which is the
// safe direction: lmm declines to move what it cannot read.
func GameOwnsDir(gameRoot, name string, members []string) bool {
	if gameRoot == "" || name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return false
	}
	entries, err := os.ReadDir(gameRoot)
	if err != nil {
		return false
	}
	actual := ""
	for _, e := range entries {
		if strings.EqualFold(e.Name(), name) {
			actual = e.Name()
			break
		}
	}
	if actual == "" {
		return false
	}
	dir := filepath.Join(gameRoot, actual)
	// Stat, not the DirEntry's own type: a game whose <Game>_Data is a
	// symlink (a split install, a case-folding overlay) still owns it.
	if info, serr := os.Stat(dir); serr != nil || !info.IsDir() {
		return serr != nil
	}

	accounted := make(map[string]bool, len(members))
	for _, m := range members {
		root, rest, nested := strings.Cut(m, "/")
		if !nested || !strings.EqualFold(root, name) {
			continue
		}
		accounted[strings.ToLower(rest)] = true
	}

	owned := false
	werr := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			owned = true
			return filepath.SkipAll
		}
		if d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil || !accounted[strings.ToLower(filepath.ToSlash(rel))] {
			owned = true
			return filepath.SkipAll
		}
		return nil
	})
	return owned || werr != nil
}
