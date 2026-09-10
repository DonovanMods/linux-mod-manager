package core

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBepInExLayout_TheThreeObservedShapes covers the archive shapes the
// #267 spike listed from real Thunderstore packages (docs/plans/
// 2026-09-09-bepinex-spike.md §1.3), plus the two the spike's Tier-1 body
// adds: a loose root .dll and a framework pack.
//
// The table is the CONTRACT, member-for-member: every case names the exact
// deploy-relative path each archive member ends up at, because the whole
// point of the normaliser is that the cache entry's layout IS the game
// directory's layout - a wrong prefix here is a plugin the loader never
// sees.
func TestBepInExLayout_TheThreeObservedShapes(t *testing.T) {
	tests := []struct {
		name string
		// members is the archive's file listing, archive-relative.
		members []string
		// loaderDeclared is the game's `loader: kind: bepinex` block, the
		// Tier-2 signal that widens the normaliser past the shapes that
		// are unmistakable on their own.
		loaderDeclared bool
		wantShape      bepinexShape
		// wantPaths maps each member to where it deploys. A member absent
		// from the map is dropped (metadata).
		wantPaths map[string]string
		wantWarn  bool
	}{
		{
			name: "shape A: RugbugRedfern/Skinwalkers - game-root-relative, metadata dropped",
			members: []string{
				"BepInEx/plugins/SkinwalkerMod.dll",
				"icon.png", "manifest.json", "README.md",
			},
			wantShape: bepinexShapeRooted,
			wantPaths: map[string]string{"BepInEx/plugins/SkinwalkerMod.dll": "BepInEx/plugins/SkinwalkerMod.dll"},
		},
		{
			name: "shape A with an asset subdirectory: Sligili/More_Emotes",
			members: []string{
				"BepInEx/plugins/MoreEmotes1.3.3.dll",
				"BepInEx/plugins/MoreEmotes/anim/emote.bundle",
				"manifest.json", "icon.png", "README.md", "CHANGELOG.md",
			},
			wantShape: bepinexShapeRooted,
			wantPaths: map[string]string{
				"BepInEx/plugins/MoreEmotes1.3.3.dll":          "BepInEx/plugins/MoreEmotes1.3.3.dll",
				"BepInEx/plugins/MoreEmotes/anim/emote.bundle": "BepInEx/plugins/MoreEmotes/anim/emote.bundle",
			},
		},
		{
			name: "shape B: Evaisa/HookGenPatcher - BepInEx-relative, needs the declaration",
			members: []string{
				"patchers/BepInEx.MonoMod.HookGenPatcher/HookGenPatcher.dll",
				"config/HookGenPatcher.cfg",
				"manifest.json", "icon.png", "README.md",
			},
			loaderDeclared: true,
			wantShape:      bepinexShapeRelative,
			wantPaths: map[string]string{
				"patchers/BepInEx.MonoMod.HookGenPatcher/HookGenPatcher.dll": "BepInEx/patchers/BepInEx.MonoMod.HookGenPatcher/HookGenPatcher.dll",
				"config/HookGenPatcher.cfg":                                  "BepInEx/config/HookGenPatcher.cfg",
			},
		},
		{
			name: "shape B is NOT unmistakable: a bare plugins/ root without the declaration is left alone",
			members: []string{
				"plugins/Something.dll",
				"manifest.json",
			},
			wantShape: bepinexShapeNone,
		},
		{
			name: "shape C: denikson/BepInExPack_Valheim - a single wrapper dir containing BepInEx/",
			members: []string{
				"BepInExPack_Valheim/BepInEx/plugins/Thing.dll",
				"BepInExPack_Valheim/BepInEx/config/thing.cfg",
				"manifest.json", "icon.png",
			},
			wantShape: bepinexShapeWrapped,
			wantPaths: map[string]string{
				"BepInExPack_Valheim/BepInEx/plugins/Thing.dll": "BepInEx/plugins/Thing.dll",
				"BepInExPack_Valheim/BepInEx/config/thing.cfg":  "BepInEx/config/thing.cfg",
			},
		},
		{
			// How Thunderstore ACTUALLY builds a wrapped package: the
			// metadata is inside the wrapper, not beside it, so it only
			// becomes root metadata once the wrapper comes off. A drop
			// that runs before the strip never sees it, and the four
			// files ride into the game ROOT of every game the plugin is
			// installed into (review F1).
			name: "shape C as Thunderstore builds it: the metadata is INSIDE the wrapper",
			members: []string{
				"SomePack/BepInEx/plugins/Thing.dll",
				"SomePack/manifest.json", "SomePack/icon.png",
				"SomePack/README.md", "SomePack/CHANGELOG.md",
			},
			wantShape: bepinexShapeWrapped,
			wantPaths: map[string]string{
				"SomePack/BepInEx/plugins/Thing.dll": "BepInEx/plugins/Thing.dll",
			},
		},
		{
			// A Windows-authored listing separates with backslashes. The
			// rules matched the RAW member, so `BepInEx\plugins\Thing.dll`
			// held no "/" at all and was read as a loose assembly - wrapped
			// in a plugin directory under the nonsense name it arrived
			// with. Classification runs against the CLEANED path (review
			// F3); the archive's own spelling stays the rewrite key.
			name:      "a Windows-authored listing with backslashes is normalised, not read as a loose assembly",
			members:   []string{`BepInEx\plugins\Thing.dll`, "manifest.json"},
			wantShape: bepinexShapeRooted,
			wantPaths: map[string]string{`BepInEx\plugins\Thing.dll`: "BepInEx/plugins/Thing.dll"},
		},
		{
			// A "./"-prefixed listing read its root name as "." and
			// reported shape C - a wrapper named ".". The paths came out
			// right, but the shape string reaches the user through
			// LoaderRequiredError.Layout, and the "./"-prefixed metadata
			// was not dropped at all.
			name:      `a "./"-prefixed listing is shape A, not a wrapper directory named "."`,
			members:   []string{"./BepInEx/plugins/Foo.dll", "./manifest.json"},
			wantShape: bepinexShapeRooted,
			wantPaths: map[string]string{"./BepInEx/plugins/Foo.dll": "BepInEx/plugins/Foo.dll"},
		},
		{
			// The BepInEx directory tests were case-sensitive while the
			// metadata and shape-B tests folded case, so a lowercase
			// `bepinex/` root was neither normalised NOR refused: it fell
			// through to "unrecognised", was warned about, and deployed a
			// plugin to a path the loader does not read (review F4). Real
			// packs are correctly cased, but a SAFETY rule must not turn
			// on one character.
			name:      "a lowercase bepinex/ root is normalised to the canonical spelling",
			members:   []string{"bepinex/plugins/Thing.dll", "manifest.json"},
			wantShape: bepinexShapeRooted,
			wantPaths: map[string]string{"bepinex/plugins/Thing.dll": "BepInEx/plugins/Thing.dll"},
		},
		{
			name:      "a wrapper containing a case-variant BepInEx/ is still shape C",
			members:   []string{"SomePack/BEPINEX/plugins/Thing.dll", "SomePack/manifest.json"},
			wantShape: bepinexShapeWrapped,
			wantPaths: map[string]string{"SomePack/BEPINEX/plugins/Thing.dll": "BepInEx/plugins/Thing.dll"},
		},
		{
			// The four exact names were the .md/.json/.png spellings only,
			// so the equally common README.txt / LICENSE / CHANGELOG.txt
			// rode into the game root - which for a BepInEx game is the
			// Steam install directory (review F5). Matched on the STEM
			// instead, so the spelling of the extension stops mattering.
			name: "package metadata is recognised by its stem, whatever the extension",
			members: []string{
				"BepInEx/plugins/Thing.dll",
				"README.txt", "LICENSE", "CHANGELOG.txt", "icon.jpg", "Manifest.JSON",
			},
			wantShape: bepinexShapeRooted,
			wantPaths: map[string]string{"BepInEx/plugins/Thing.dll": "BepInEx/plugins/Thing.dll"},
		},
		{
			name:      "a loose root .dll becomes a plugin under its own directory",
			members:   []string{"CoolMod.dll", "manifest.json", "README.md"},
			wantShape: bepinexShapePlugin,
			wantPaths: map[string]string{"CoolMod.dll": "BepInEx/plugins/CoolMod/CoolMod.dll"},
			// A bare .dll is not unmistakably BepInEx (a Minecraft-adjacent
			// archive could ship one), so it needs the declaration.
			loaderDeclared: true,
		},
		{
			name:      "a loose root .dll without the declaration is left alone",
			members:   []string{"CoolMod.dll", "manifest.json"},
			wantShape: bepinexShapeNone,
		},
		{
			name: "an unrecognised root warns and rewrites nothing",
			members: []string{
				"Data/StreamingAssets/thing.bundle", "notes.txt",
			},
			loaderDeclared: true,
			wantShape:      bepinexShapeNone,
			wantWarn:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			layout, err := bepinexNormalise(tt.members, "CoolMod", tt.loaderDeclared)
			require.NoError(t, err)
			require.NotNil(t, layout)
			assert.Equal(t, tt.wantShape, layout.Shape)
			if tt.wantWarn {
				assert.NotEmpty(t, layout.Warnings, "an unrecognised layout must warn")
			} else {
				assert.Empty(t, layout.Warnings)
			}
			if tt.wantShape == bepinexShapeNone {
				assert.False(t, layout.Applies(), "an unapplied layout rewrites nothing")
				return
			}
			require.True(t, layout.Applies())
			for _, m := range tt.members {
				got, kept := layout.Rewrite(m)
				want, wantKept := tt.wantPaths[m]
				assert.Equal(t, wantKept, kept, "member %q kept?", m)
				if wantKept {
					assert.Equal(t, want, got, "member %q deploy path", m)
				}
			}
		})
	}
}

// TestBepInExLayout_FrameworkPackIsRefused pins the refusal the spike calls
// for: BepInEx itself is a per-game prerequisite, not a mod, so an archive
// whose payload is BepInEx/core/ must not be installed as one. The message
// has to point at the loader setup, because "unsupported archive" would
// leave a user re-downloading the same pack.
func TestBepInExLayout_FrameworkPackIsRefused(t *testing.T) {
	for _, members := range [][]string{
		{"BepInEx/core/BepInEx.Preloader.dll", "BepInEx/core/0Harmony.dll", "doorstop_config.ini", "winhttp.dll"},
		{"BepInExPack/BepInEx/core/BepInEx.Preloader.dll", "BepInExPack/winhttp.dll", "manifest.json"},
		// Review F4: a one-character difference used to install the
		// preloader as a MOD - under lmm's deployed-files bookkeeping,
		// where the next profile switch tears the loader out from under
		// every plugin. That is exactly what this refusal exists to
		// prevent, so it cannot be case-dependent.
		{"bepinex/core/BepInEx.Preloader.dll", "winhttp.dll"},
		// Re-review R1: F4 folded the FIRST segment only, so `core` was
		// still compared exactly and a pack spelling it `Core/` was
		// classified as an ordinary plugin archive and installed - the
		// preloader, winhttp.dll and doorstop_config.ini all under
		// deployed_files. BepInEx owns the directory names below its own,
		// so lmm folds those too.
		{"BepInEx/Core/BepInEx.Preloader.dll", "winhttp.dll"},
		{"BepInEx/CORE/BepInEx.Preloader.dll", "winhttp.dll"},
		{"BEPINEX/Core/BepInEx.Preloader.dll", "winhttp.dll"},
		{"BepInExPack/BepInEx/Core/BepInEx.Preloader.dll", "BepInExPack/winhttp.dll", "manifest.json"},
		{"BepInExPack/BepInEx/CORE/BepInEx.Preloader.dll", "BepInExPack/winhttp.dll", "manifest.json"},
	} {
		layout, err := bepinexNormalise(members, "BepInExPack", false)
		assert.Nil(t, layout)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrBepInExFrameworkPack), "want ErrBepInExFrameworkPack, got %v", err)
		assert.Contains(t, err.Error(), "loader")
	}
}

// TestBepInExLayout_RefusesACollidingRewrite guards the one way a rewrite
// could silently lose a member: two members whose normalised destinations
// are the same path. "Never guesses" applies here too - the archive is
// refused rather than half-deployed.
func TestBepInExLayout_RefusesACollidingRewrite(t *testing.T) {
	_, err := bepinexNormalise([]string{
		"plugins/A.dll",
		"BepInEx/plugins/A.dll",
		"patchers/x.dll",
	}, "Mod", true)
	// BepInEx/ is present, so this is shape A and nothing is prefixed -
	// no collision. The collision case is a wrapper strip that lands on a
	// sibling of the wrapper.
	require.NoError(t, err)

	_, err = bepinexNormalise([]string{
		"Wrapper/BepInEx/plugins/A.dll",
		"config/A.dll",
	}, "Mod", true)
	require.NoError(t, err) // two roots, so no wrapper strip: unrecognised
}

// TestBepInExConfigSeedMember names the paths the deploy path treats as
// seeded configuration rather than linked mod content (#358 (b)).
func TestBepInExConfigSeedMember(t *testing.T) {
	assert.True(t, isBepInExConfigMember("BepInEx/config/thing.cfg"))
	assert.True(t, isBepInExConfigMember("BepInEx/config/nested/thing.cfg"))
	assert.False(t, isBepInExConfigMember("BepInEx/plugins/thing.dll"))
	assert.False(t, isBepInExConfigMember("BepInEx/config"))
	assert.False(t, isBepInExConfigMember("config/thing.cfg"))
}

// TestBepInExLayout_CanonicalisesBepInExsOwnSubdirectories is re-review
// R1/R2: BepInEx reads FIXED paths (BepInEx/plugins, BepInEx/config), so a
// Windows-authored archive spelling one of them `Plugins/` or `Config/`
// deploys somewhere the loader never looks - and, for config, somewhere
// isBepInExConfigMember does not recognise, which turned #358 (b)'s seeded
// real file back into a symlink into the cache.
func TestBepInExLayout_CanonicalisesBepInExsOwnSubdirectories(t *testing.T) {
	for _, tc := range []struct {
		name    string
		members []string
		want    map[string]string
	}{
		{
			name:    "shape A: a case-variant subdirectory takes BepInEx's spelling",
			members: []string{"BepInEx/Plugins/A.dll", "BepInEx/Config/a.cfg", "BepInEx/PATCHERS/p.dll"},
			want: map[string]string{
				"BepInEx/Plugins/A.dll":  "BepInEx/plugins/A.dll",
				"BepInEx/Config/a.cfg":   "BepInEx/config/a.cfg",
				"BepInEx/PATCHERS/p.dll": "BepInEx/patchers/p.dll",
			},
		},
		{
			name:    "shape B: the prefix path is canonicalised too, not just the stripped half",
			members: []string{"Plugins/Cfg.dll", "Config/cfg.cfg"},
			want: map[string]string{
				"Plugins/Cfg.dll": "BepInEx/plugins/Cfg.dll",
				"Config/cfg.cfg":  "BepInEx/config/cfg.cfg",
			},
		},
		{
			name:    "a directory BepInEx does not own keeps the archive's spelling",
			members: []string{"BepInEx/Custom/thing.dat"},
			want:    map[string]string{"BepInEx/Custom/thing.dat": "BepInEx/Custom/thing.dat"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			layout, err := bepinexNormalise(tc.members, "Mod", true)
			require.NoError(t, err)
			require.True(t, layout.Applies())
			for member, want := range tc.want {
				dest, kept := layout.Rewrite(member)
				assert.True(t, kept, "member %q must be kept", member)
				assert.Equal(t, want, dest)
			}
		})
	}
}
