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

	require.NoError(t, validateLayoutTable(layout, members, false),
		"on a case-sensitive filesystem these are two distinct destinations")

	err := validateLayoutTable(layout, members, true)
	require.Error(t, err)
	var typed *AdapterLayoutError
	require.ErrorAs(t, err, &typed)
	assert.Contains(t, err.Error(), "a.dll")
	assert.Contains(t, err.Error(), "b.dll")
}
