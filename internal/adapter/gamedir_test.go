package adapter_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeTree materialises slash-separated files under a fresh root.
func writeTree(t *testing.T, files ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, f := range files {
		p := filepath.Join(root, filepath.FromSlash(f))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte("payload of "+f), 0o644))
	}
	return root
}

// TestGameOwnsDir pins the ONE copy of the "does the game own this
// directory" probe (#413 review F7). Core's misplaced-deploy check and the
// bepinex adapter's shape F both ask it; before this there were two
// hand-kept copies and nothing that failed when they drifted.
func TestGameOwnsDir(t *testing.T) {
	engine := []string{
		"valheim_Data/Managed/UnityEngine.dll",
		"valheim_Data/Managed/Assembly-CSharp.dll",
	}

	tests := []struct {
		name    string
		tree    []string // files in the game root
		dir     string   // the root-level directory asked about
		members []string // what the caller accounts for
		want    bool
	}{
		{
			name: "a directory holding files the members do not account for is the game's",
			tree: engine, dir: "valheim_Data",
			members: []string{"valheim_Data/Managed/MyPatch.dll"},
			want:    true,
		},
		{
			name: "a directory holding EXACTLY the members is lmm's own misdeployment",
			tree: []string{"Jotunn/Jotunn.dll", "Jotunn/Jotunn.xml"}, dir: "Jotunn",
			members: []string{"Jotunn/Jotunn.dll", "Jotunn/Jotunn.xml"},
			want:    false,
		},
		{
			name: "the name matches case-insensitively, on both the directory and the members",
			tree: []string{"Jotunn/Jotunn.dll"}, dir: "JOTUNN",
			members: []string{"jotunn/JOTUNN.DLL"},
			want:    false,
		},
		{
			name: "a directory the game root does not have is nobody's",
			tree: engine, dir: "Jotunn",
			members: []string{"Jotunn/Jotunn.dll"},
			want:    false,
		},
		{
			name: "a FILE of that name is not a directory the game owns",
			tree: []string{"Jotunn"}, dir: "Jotunn",
			want: false,
		},
		{name: "an empty name asks nothing", tree: engine, dir: "", want: false},
		{name: "a dot name asks nothing", tree: engine, dir: ".", want: false},
		{name: "a dot-dot name asks nothing", tree: engine, dir: "..", want: false},
		{name: "a nested name asks nothing", tree: engine, dir: "valheim_Data/Managed", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := writeTree(t, tt.tree...)
			assert.Equal(t, tt.want, adapter.GameOwnsDir(root, tt.dir, tt.members))
		})
	}

	t.Run("no game root to consult means the member list is all there is", func(t *testing.T) {
		assert.False(t, adapter.GameOwnsDir("", "valheim_Data", nil))
		assert.False(t, adapter.GameOwnsDir(filepath.Join(t.TempDir(), "missing"), "valheim_Data", nil))
	})

	t.Run("a symlinked directory is still the game's", func(t *testing.T) {
		elsewhere := writeTree(t, "Managed/UnityEngine.dll")
		root := t.TempDir()
		require.NoError(t, os.Symlink(elsewhere, filepath.Join(root, "valheim_Data")))
		assert.True(t, adapter.GameOwnsDir(root, "valheim_Data", nil))
	})

	t.Run("a dangling symlink cannot be read, so it counts as the game's", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, os.Symlink(filepath.Join(root, "gone"), filepath.Join(root, "valheim_Data")))
		assert.True(t, adapter.GameOwnsDir(root, "valheim_Data", nil))
	})

	t.Run("an unreadable directory counts as the game's", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads a 0o000 directory anyway")
		}
		root := writeTree(t, "Jotunn/Jotunn.dll")
		locked := filepath.Join(root, "Jotunn")
		require.NoError(t, os.Chmod(locked, 0o000))
		t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
		assert.True(t, adapter.GameOwnsDir(root, "Jotunn", []string{"Jotunn/Jotunn.dll"}),
			"lmm declines to move what it cannot read")
	})
}
