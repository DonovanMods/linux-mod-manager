package core

// Whole-table validation for the adapter tree rewriter (#411, I2/I3).
//
// rewriteExtractedTree used to rename member by member with a bare
// os.Rename and no view of the table as a whole, so a colliding or chained
// rewrite destroyed files silently - and slices.Compact on the returned
// member list hid the loss. Core owns this executor for every future
// adapter, so its refusals are pinned here.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// layoutTestTree writes files into a fresh root and returns the root plus a
// snapshot of its content, so a refused rewrite can be asserted to have
// changed nothing.
func layoutTestTree(t *testing.T, files map[string]string) (string, map[string]string) {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}
	return root, layoutTreeSnapshot(t, root)
}

func layoutTreeSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	snap := map[string]string{}
	require.NoError(t, filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		if d.IsDir() {
			snap[rel+"/"] = ""
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		snap[rel] = string(b)
		return nil
	}))
	return snap
}

// TestRewriteExtractedTreeRefusesAnUnexecutableTable is I2's regression
// test. Every one of these tables used to return err=nil having silently
// destroyed a file:
//
//   - two members onto one destination left only the second one's content;
//   - A->B with B->C destroyed B's bytes AND left C holding A's, which is
//     worse than loss: correct-looking paths, wrong content, then cached,
//     checksummed and deployed.
func TestRewriteExtractedTreeRefusesAnUnexecutableTable(t *testing.T) {
	tests := []struct {
		name   string
		files  map[string]string
		table  map[string]string
		wants  []string // members the error must name
		reason string
	}{
		{
			name:   "two members onto one destination",
			files:  map[string]string{"a/x.dll": "A", "b/x.dll": "B"},
			table:  map[string]string{"a/x.dll": "out/x.dll", "b/x.dll": "out/x.dll"},
			wants:  []string{"a/x.dll", "b/x.dll"},
			reason: "collide",
		},
		{
			name:   "a chained rewrite",
			files:  map[string]string{"A.txt": "contentA", "B.txt": "contentB"},
			table:  map[string]string{"A.txt": "B.txt", "B.txt": "C.txt"},
			wants:  []string{"A.txt", "B.txt"},
			reason: "chain",
		},
		{
			name:   "a destination equal to a dropped member's source",
			files:  map[string]string{"A.txt": "contentA", "B.txt": "contentB"},
			table:  map[string]string{"A.txt": "B.txt", "B.txt": ""},
			wants:  []string{"A.txt", "B.txt"},
			reason: "chain",
		},
		{
			name:   "an escaping destination after a legal one",
			files:  map[string]string{"a.dll": "A", "b.dll": "B"},
			table:  map[string]string{"a.dll": "out/a.dll", "b.dll": "../escaped.dll"},
			wants:  []string{"b.dll"},
			reason: "escap",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			members := make([]string, 0, len(tc.files))
			for rel := range tc.files {
				members = append(members, rel)
			}
			root, before := layoutTestTree(t, tc.files)

			_, err := rewriteExtractedTree(root, adapter.NewLayout("hostile", tc.table), members)
			require.Error(t, err, "an unexecutable table must be refused, not silently applied")
			assert.Contains(t, err.Error(), tc.reason)
			for _, m := range tc.wants {
				assert.Contains(t, err.Error(), m, "the error must name the offending member")
			}

			var typed *AdapterLayoutError
			require.ErrorAs(t, err, &typed, "the refusal must be a typed error a frontend can branch on")
			assert.NotEmpty(t, typed.Members)

			assert.Equal(t, before, layoutTreeSnapshot(t, root),
				"a refused layout must leave the staging tree byte-identical - no partial rewrite")
		})
	}
}

// TestValidateLayoutTableCaseOnlyCollision pins the case-insensitive-FS
// half of I2. On ext4 two destinations differing only in case are two
// distinct files and the layout is executable; on a case-insensitive
// filesystem the second rename clobbers the first, so the table is refused
// before anything moves.
func TestValidateLayoutTableCaseOnlyCollision(t *testing.T) {
	layout := adapter.NewLayout("case", map[string]string{
		"a.dll": "out/Mod.dll",
		"b.dll": "out/mod.dll",
	})
	members := []string{"a.dll", "b.dll"}

	root := t.TempDir()
	require.NoError(t, validateLayoutTable(root, layout, members, false),
		"on a case-sensitive filesystem these are two distinct destinations")

	err := validateLayoutTable(root, layout, members, true)
	require.Error(t, err)
	var typed *AdapterLayoutError
	require.ErrorAs(t, err, &typed)
	assert.Contains(t, err.Error(), "a.dll")
	assert.Contains(t, err.Error(), "b.dll")
}

// TestRewriteExtractedTreeRefusesASymlinkedEscape is I3's regression test.
// containedIn was lexical only - it checked filepath.Clean for ".." and for
// absoluteness - while os.MkdirAll and os.Rename both FOLLOW symlinks, so a
// destination routed through a symlink already in the staging tree wrote
// outside the cache entry with no error at all. The symlink need not come
// from the adapter: an archive can carry one.
func TestRewriteExtractedTreeRefusesASymlinkedEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.dll"), []byte("A"), 0o644))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "link")))

	layout := adapter.NewLayout("hostile", map[string]string{"a.dll": "link/escaped.dll"})
	_, err := rewriteExtractedTree(root, layout, []string{"a.dll"})

	require.Error(t, err, "a destination that resolves outside the staging root must be refused")
	var typed *AdapterLayoutError
	require.ErrorAs(t, err, &typed)
	assert.Contains(t, err.Error(), "escaping")
	assert.NoFileExists(t, filepath.Join(outside, "escaped.dll"), "nothing may land outside the staging root")
	assert.FileExists(t, filepath.Join(root, "a.dll"), "the refused member stays where the extractor put it")
}

// TestRewriteExtractedTreeAllowsAnInTreeSymlinkedDirectory is I3's other
// half: resolving symlinks must refuse an ESCAPE, not every symlink. A link
// that stays inside the staging root is a legal destination prefix.
func TestRewriteExtractedTreeAllowsAnInTreeSymlinkedDirectory(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "real"), 0o755))
	require.NoError(t, os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "link")))
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.dll"), []byte("A"), 0o644))

	layout := adapter.NewLayout("in-tree", map[string]string{"a.dll": "link/a.dll"})
	got, err := rewriteExtractedTree(root, layout, []string{"a.dll"})
	require.NoError(t, err)
	assert.Equal(t, []string{"link/a.dll"}, got)
	assert.FileExists(t, filepath.Join(root, "real", "a.dll"))
}

// TestCopyOnceNeverLeavesAPartialFile is M1's regression test: copyOnce
// used to O_TRUNC the destination and stream into it, so a kill, a full
// disk or an I/O error mid-copy left a truncated file that copy-once's own
// contract - never overwrite - then refused to repair on every subsequent
// deploy. The destination must only ever appear complete.
func TestCopyOnceNeverLeavesAPartialFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.cfg")
	dst := filepath.Join(dir, "nested", "dst.cfg")
	require.NoError(t, os.WriteFile(src, []byte("shipped"), 0o644))

	// A source that cannot be read part-way through is the observable
	// stand-in for a mid-copy failure: whatever copyOnce does, dst must not
	// be left behind holding a prefix.
	require.NoError(t, os.Remove(src))
	require.NoError(t, os.Mkdir(src, 0o755)) // a directory: open succeeds, read fails

	err := copyOnce(src, dst)
	require.Error(t, err)
	assert.NoFileExists(t, dst, "a failed copy must leave no file at the destination")

	entries, err := os.ReadDir(filepath.Dir(dst))
	require.NoError(t, err)
	assert.Empty(t, entries, "and no temporary file either")
}

// TestApplyAdapterCopyOnceRefusesAnEscapingMember is M2's regression test:
// the copy-once write joined a cache-relative member onto game.ModPath
// unchecked, while applyProfileOverrides fifteen lines above explicitly
// refuses a traversing override path. Cache members are sanitised at
// extraction, but I3 showed the rewriter can put a path into a cache entry
// the extractor never saw - so the guard belongs beside the write.
func TestApplyAdapterCopyOnceRefusesAnEscapingMember(t *testing.T) {
	outside := t.TempDir()
	_, err := copyOnceDest(filepath.Join(outside, "game"), "../escaped.cfg")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "escap")

	within, err := copyOnceDest(filepath.Join(outside, "game"), "BepInEx/config/mod.cfg")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(outside, "game", "BepInEx", "config", "mod.cfg"), within)
}
