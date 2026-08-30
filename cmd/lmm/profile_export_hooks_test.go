package main

// #296 at the CLI seam: `lmm profile export` writes the profile document to
// stdout verbatim, so the recorded plain-text delta is exactly "an exported
// profile that HAS hook overrides gains a hooks: block". A profile without
// overrides prints byte-identically to every export before #296, which the
// second test pins.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDoProfileExport_JSON pins the domain.ExportedProfile document's
// framing (one document decoding into the declared type with no unknown
// members, empty stderr) and its recorded golden (#309); the plain path's
// unchanged YAML bytes stay pinned by the two tests above.
func TestDoProfileExport_JSON(t *testing.T) {
	svc, game, _ := newProfileExportTestService(t)
	pm := getProfileManager(svc)
	_, err := pm.Create(context.Background(), game.ID, "modded")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(context.Background(), game.ID, "modded", domain.ModReference{SourceID: "src", ModID: "a", Version: "1.0"}))

	out := runJSONCommand(t, func() error {
		return doProfileExport(context.Background(), svc, game, "modded")
	})
	var got domain.ExportedProfile
	decodeStrict(t, out, &got)
	assertJSONCLIGolden(t, "profile_export", out)
}

func newProfileExportTestService(t *testing.T) (*core.Service, *domain.Game, string) {
	t.Helper()
	configDir := t.TempDir()
	cfg := core.ServiceConfig{ConfigDir: configDir, DataDir: t.TempDir(), CacheDir: t.TempDir()}
	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	return svc, game, configDir
}

// TestDoProfileExport_PrintsHookOverrides is the #296 delta: the exported
// document now carries the profile's own hooks: block, in the profile file's
// own *string-pointer encoding (an explicitly-disabled hook is present and
// empty; an unset one is absent, so it keeps inheriting the game's).
func TestDoProfileExport_PrintsHookOverrides(t *testing.T) {
	svc, game, configDir := newProfileExportTestService(t)

	dir := filepath.Join(configDir, "games", game.ID, "profiles")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "modded.yaml"), []byte(`name: modded
game_id: g1
mods: []
hooks:
    install:
        after_all: ""
    uninstall:
        after_all: "/opt/lmm/cleanup.sh"
`), 0o644))

	out := captureStdout(t, func() error {
		return doProfileExport(context.Background(), svc, game, "modded")
	})

	// The explicit `null`s are the profile FILE's own shape: the export
	// reuses config's ProfileHooksYAML verbatim (an unset hook is a nil
	// *string, which parseProfileHooks reads back as "not explicit"), so an
	// exported profile is a profile file and nothing had to be invented for
	// the wire.
	assert.Equal(t, `name: modded
game_id: g1
mods: []
hooks:
    install:
        before_all: null
        before_each: null
        after_each: null
        after_all: ""
    uninstall:
        before_all: null
        before_each: null
        after_each: null
        after_all: /opt/lmm/cleanup.sh
`, out)
}

// TestDoProfileExport_NoHooks_PrintsNoHooksBlock is the compatibility half:
// nothing about an ordinary export moved.
func TestDoProfileExport_NoHooks_PrintsNoHooksBlock(t *testing.T) {
	svc, game, _ := newProfileExportTestService(t)
	_, err := getProfileManager(svc).Create(context.Background(), game.ID, "plain")
	require.NoError(t, err)

	out := captureStdout(t, func() error {
		return doProfileExport(context.Background(), svc, game, "plain")
	})

	assert.Equal(t, "name: plain\ngame_id: g1\nmods: []\n", out)
}
