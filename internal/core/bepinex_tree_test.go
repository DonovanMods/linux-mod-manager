package core

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeArchiveTree builds one of the spike's archive shapes as a real
// extracted directory - the fixture every ingest-side test shares, standing
// in for the pristine tree the extractor produces.
func writeArchiveTree(t *testing.T, members ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, m := range members {
		p := filepath.Join(root, filepath.FromSlash(m))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte("payload of "+m), 0o644))
	}
	return root
}

// treeFiles lists root's regular files as slash-separated relative paths,
// sorted, so a test can assert on the whole tree at once.
func treeFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	require.NoError(t, filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		require.NoError(t, err)
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		require.NoError(t, err)
		out = append(out, filepath.ToSlash(rel))
		return nil
	}))
	sort.Strings(out)
	return out
}

// TestNormalizeBepInExTree_RewritesEachShapeInPlace is the ingest half of
// #358: the normaliser's paper answer applied to a real extracted tree, for
// each of the three observed shapes. It is the tree the cache entry becomes,
// so it is also the layout the linker deploys into the game root.
func TestNormalizeBepInExTree_RewritesEachShapeInPlace(t *testing.T) {
	tests := []struct {
		name           string
		members        []string
		loaderDeclared bool
		want           []string
		wantWarn       bool
	}{
		{
			name:    "shape A drops the metadata and moves nothing",
			members: []string{"BepInEx/plugins/Skinwalkers.dll", "manifest.json", "icon.png", "README.md"},
			want:    []string{"BepInEx/plugins/Skinwalkers.dll"},
		},
		{
			name:    "shape C strips the wrapper directory",
			members: []string{"BepInExPack_Valheim/BepInEx/plugins/Thing.dll", "BepInExPack_Valheim/BepInEx/config/thing.cfg", "manifest.json"},
			want:    []string{"BepInEx/config/thing.cfg", "BepInEx/plugins/Thing.dll"},
		},
		{
			// The real wrapped shape: the metadata is INSIDE the wrapper,
			// so the drop has to run again after the strip or all four
			// files land in the game root (review F1).
			name: "shape C drops the metadata the wrapper contained",
			members: []string{
				"SomePack/BepInEx/plugins/Thing.dll",
				"SomePack/manifest.json", "SomePack/icon.png",
				"SomePack/README.md", "SomePack/CHANGELOG.md",
			},
			want: []string{"BepInEx/plugins/Thing.dll"},
		},
		{
			// The tree half of review F3: the extracted file really is
			// named with backslashes on Linux, and it must be moved to the
			// path BepInEx looks at rather than wrapped in a plugin
			// directory under its raw name.
			name:    "a backslash-separated listing is moved to the path it means",
			members: []string{`BepInEx\plugins\Thing.dll`, "manifest.json"},
			want:    []string{"BepInEx/plugins/Thing.dll"},
		},
		{
			// Review F4 on disk: the case-variant directory is RENAMED to
			// BepInEx's own spelling, because that is the path the loader
			// reads.
			name:    "a case-variant bepinex/ directory is renamed to the canonical spelling",
			members: []string{"bepinex/plugins/Thing.dll", "manifest.json"},
			want:    []string{"BepInEx/plugins/Thing.dll"},
		},
		{
			name:           "shape B gains the BepInEx/ prefix for a declared game",
			members:        []string{"patchers/HookGen/HookGenPatcher.dll", "config/HookGenPatcher.cfg", "manifest.json"},
			loaderDeclared: true,
			want:           []string{"BepInEx/config/HookGenPatcher.cfg", "BepInEx/patchers/HookGen/HookGenPatcher.dll"},
		},
		{
			name:    "shape B is untouched for a game with no declaration",
			members: []string{"patchers/HookGen/HookGenPatcher.dll", "config/HookGenPatcher.cfg", "manifest.json"},
			// Not even the metadata drop: an unrecognised archive is left
			// exactly as it arrived.
			want: []string{"config/HookGenPatcher.cfg", "manifest.json", "patchers/HookGen/HookGenPatcher.dll"},
		},
		{
			name:           "a loose .dll lands under its own plugin directory",
			members:        []string{"CoolMod.dll", "CoolMod.xml", "manifest.json"},
			loaderDeclared: true,
			want:           []string{"BepInEx/plugins/CoolMod/CoolMod.dll", "BepInEx/plugins/CoolMod/CoolMod.xml"},
		},
		{
			name:           "an unrecognised tree is left alone, with a warning",
			members:        []string{"Data/StreamingAssets/thing.bundle"},
			loaderDeclared: true,
			want:           []string{"Data/StreamingAssets/thing.bundle"},
			wantWarn:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := writeArchiveTree(t, tt.members...)
			layout, err := normalizeBepInExTree(root, "CoolMod", tt.loaderDeclared)
			require.NoError(t, err)
			assert.Equal(t, tt.want, treeFiles(t, root))
			if tt.wantWarn {
				assert.NotEmpty(t, layout.Warnings)
			} else {
				assert.Empty(t, layout.Warnings)
			}
			// The payload travelled with the path: a rewrite is a move, not
			// a truncate-and-create.
			for _, f := range tt.want {
				b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(f)))
				require.NoError(t, err)
				assert.Contains(t, string(b), "payload of ")
			}
		})
	}
}

// TestNormalizeBepInExTree_LeavesNoEmptyWrapperBehind: a stripped wrapper
// or a moved-away root directory must not survive as an empty directory in
// the cache entry, or `lmm mod files` and every plan readout show a phantom
// the game never sees.
func TestNormalizeBepInExTree_LeavesNoEmptyWrapperBehind(t *testing.T) {
	root := writeArchiveTree(t, "BepInExPack/BepInEx/plugins/A.dll", "manifest.json")
	_, err := normalizeBepInExTree(root, "Pack", false)
	require.NoError(t, err)

	_, err = os.Stat(filepath.Join(root, "BepInExPack"))
	assert.True(t, os.IsNotExist(err), "the stripped wrapper directory must be gone, got %v", err)
}

// TestNormalizeBepInExTree_RefusesAFrameworkPack: the refusal reaches the
// ingest, so a user who downloads BepInExPack as a mod is told to configure
// the loader instead of having the preloader tracked as profile content.
func TestNormalizeBepInExTree_RefusesAFrameworkPack(t *testing.T) {
	root := writeArchiveTree(t, "BepInExPack/BepInEx/core/BepInEx.Preloader.dll", "BepInExPack/winhttp.dll", "manifest.json")
	_, err := normalizeBepInExTree(root, "BepInExPack", false)
	require.ErrorIs(t, err, ErrBepInExFrameworkPack)

	// And it refused BEFORE touching anything: the tree is intact, so the
	// caller's own cleanup has a coherent directory to remove.
	assert.Equal(t, []string{
		"BepInExPack/BepInEx/core/BepInEx.Preloader.dll",
		"BepInExPack/winhttp.dll",
		"manifest.json",
	}, treeFiles(t, root))
}
