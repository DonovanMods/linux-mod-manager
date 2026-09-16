package core

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter/bepinex"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bepinexGameForTree is the game every case below is laid out for: a
// loader-DECLARING game whose mod path is its install path, which is the
// shape a BepInEx install needs. Declaring matters here - an undeclared
// game whose adapter resolved from an install lmm merely FOUND gets an
// extra notice on the layout (#424), which these cases are not about.
func bepinexGameForTree() *domain.Game {
	return &domain.Game{ID: "valheim", Loader: &domain.GameLoader{Kind: domain.LoaderKindBepInEx}}
}

// layoutTree is the pair this file exists to test: an ADAPTER's rule table
// (NormalizeArchive, pure) applied to a real extracted tree by CORE's
// executor (rewriteExtractedTree). U3 (#413) split the two - the rules moved
// to internal/adapter/bepinex, the rewriting stayed here - and the property
// every case below asserts is that the pair still produces the tree #358
// shipped.
func layoutTree(t *testing.T, a adapter.GameAdapter, root, modName string) (adapter.Layout, error) {
	t.Helper()
	members, err := relativeFileMembers(root)
	require.NoError(t, err)
	members = slashMembers(members)
	layout, err := a.NormalizeArchive(adapter.NormalizeRequest{
		Game: bepinexGameForTree(), ModName: modName, Members: members,
	})
	if err != nil {
		return adapter.Layout{}, err
	}
	if !layout.Applies() {
		return layout, nil
	}
	if _, rerr := rewriteExtractedTree(root, layout, members); rerr != nil {
		return adapter.Layout{}, rerr
	}
	return layout, nil
}

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

// TestBepInExLayoutTree_RewritesEachShapeInPlace is the ingest half of
// #358: the normaliser's paper answer applied to a real extracted tree, for
// each of the three observed shapes. It is the tree the cache entry becomes,
// so it is also the layout the linker deploys into the game root.
func TestBepInExLayoutTree_RewritesEachShapeInPlace(t *testing.T) {
	tests := []struct {
		name    string
		members []string
		// generic runs the case through the IDENTITY adapter, which is
		// what a game with no BepInEx resolves to - the replacement for
		// #358's `loaderDeclared: false`, and the reason that parameter
		// could go away (design §2, decision 12).
		generic  bool
		want     []string
		wantWarn bool
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
			name:    "shape B gains the BepInEx/ prefix for a BepInEx game",
			members: []string{"patchers/HookGen/HookGenPatcher.dll", "config/HookGenPatcher.cfg", "manifest.json"},
			want:    []string{"BepInEx/config/HookGenPatcher.cfg", "BepInEx/patchers/HookGen/HookGenPatcher.dll"},
		},
		{
			name:    "shape B is untouched for a game with no BepInEx",
			members: []string{"patchers/HookGen/HookGenPatcher.dll", "config/HookGenPatcher.cfg", "manifest.json"},
			generic: true,
			// Not even the metadata drop: an unrecognised archive is left
			// exactly as it arrived.
			want: []string{"config/HookGenPatcher.cfg", "manifest.json", "patchers/HookGen/HookGenPatcher.dll"},
		},
		{
			name:    "a loose .dll lands under its own plugin directory",
			members: []string{"CoolMod.dll", "CoolMod.xml", "manifest.json"},
			want:    []string{"BepInEx/plugins/CoolMod/CoolMod.dll", "BepInEx/plugins/CoolMod/CoolMod.xml"},
		},
		{
			name:     "an unrecognised tree is left alone, with a warning",
			members:  []string{"Data/StreamingAssets/thing.bundle"},
			want:     []string{"Data/StreamingAssets/thing.bundle"},
			wantWarn: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := writeArchiveTree(t, tt.members...)
			var a adapter.GameAdapter = bepinex.New()
			if tt.generic {
				a = adapter.Generic{}
			}
			layout, err := layoutTree(t, a, root, "CoolMod")
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

// TestBepInExLayoutTree_LeavesNoEmptyWrapperBehind: a stripped wrapper
// or a moved-away root directory must not survive as an empty directory in
// the cache entry, or `lmm mod files` and every plan readout show a phantom
// the game never sees.
func TestBepInExLayoutTree_LeavesNoEmptyWrapperBehind(t *testing.T) {
	root := writeArchiveTree(t, "BepInExPack/BepInEx/plugins/A.dll", "manifest.json")
	_, err := layoutTree(t, bepinex.New(), root, "Pack")
	require.NoError(t, err)

	_, err = os.Stat(filepath.Join(root, "BepInExPack"))
	assert.True(t, os.IsNotExist(err), "the stripped wrapper directory must be gone, got %v", err)
}

// TestBepInExLayoutTree_RefusesAFrameworkPack: the refusal reaches the
// ingest, so a user who downloads BepInExPack as a mod is told to configure
// the loader instead of having the preloader tracked as profile content.
func TestBepInExLayoutTree_RefusesAFrameworkPack(t *testing.T) {
	root := writeArchiveTree(t, "BepInExPack/BepInEx/core/BepInEx.Preloader.dll", "BepInExPack/winhttp.dll", "manifest.json")
	_, err := layoutTree(t, bepinex.New(), root, "BepInExPack")
	require.ErrorIs(t, err, ErrNotAMod)

	// And it refused BEFORE touching anything: the tree is intact, so the
	// caller's own cleanup has a coherent directory to remove.
	assert.Equal(t, []string{
		"BepInExPack/BepInEx/core/BepInEx.Preloader.dll",
		"BepInExPack/winhttp.dll",
		"manifest.json",
	}, treeFiles(t, root))
}

// TestBepInExLayoutTree_PluginFolderMovesWhole is #424 on a real
// extracted tree: the directory the author shipped becomes a directory
// under BepInEx/plugins/, with everything inside it carried along and the
// vacated root cleaned up.
func TestBepInExLayoutTree_PluginFolderMovesWhole(t *testing.T) {
	root := writeArchiveTree(t,
		"Jotunn/Jotunn.dll", "Jotunn/Jotunn.pdb", "Jotunn/Jotunn.xml",
		"Jotunn/README.md", "Jotunn/CHANGELOG.md")

	layout, err := layoutTree(t, bepinex.New(), root, "Jotunn")
	require.NoError(t, err)
	require.True(t, layout.Applies())
	assert.Equal(t, "a plugin folder", layout.Kind)
	assert.Equal(t, []string{
		"BepInEx/plugins/Jotunn/CHANGELOG.md",
		"BepInEx/plugins/Jotunn/Jotunn.dll",
		"BepInEx/plugins/Jotunn/Jotunn.pdb",
		"BepInEx/plugins/Jotunn/Jotunn.xml",
		"BepInEx/plugins/Jotunn/README.md",
	}, treeFiles(t, root))

	_, err = os.Stat(filepath.Join(root, "Jotunn"))
	assert.True(t, os.IsNotExist(err), "the vacated root directory is swept")
}
