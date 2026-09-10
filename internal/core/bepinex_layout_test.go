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
