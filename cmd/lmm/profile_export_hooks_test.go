package main

// #296 at the CLI seam: `lmm profile export` writes the profile document to
// stdout verbatim, and an exported profile that HAS hook overrides carries
// a hooks: block. Since #621 that document is the version-1 collection
// (owner ruling 2026-11-12: stdout stays YAML, now version 1), which writes
// only the hooks that are set.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

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
collection:
    id: fixed-id
hooks:
    install:
        after_all: ""
    uninstall:
        after_all: "/opt/lmm/cleanup.sh"
`), 0o644))

	out := captureStdout(t, func() error {
		return doProfileExport(context.Background(), svc, game, "modded")
	})

	// An unset hook is absent (it keeps inheriting the game's), and an
	// explicitly disabled one is present and empty: the profile file's own
	// encoding, which an older lmm's reader decodes the same way.
	assert.Equal(t, `lmm_collection: 1
id: fixed-id
version: 1
name: modded
game_id: g1
game:
    name: Game
mods: []
hooks:
    install:
        after_all: ""
    uninstall:
        after_all: /opt/lmm/cleanup.sh
`, out)
}

// TestDoProfileExport_NoHooks_PrintsNoHooksBlock: a profile without hook
// overrides writes no hooks: key. Its first export mints the collection id
// and records it in the profile (#621), and stdout still carries exactly
// the one document.
func TestDoProfileExport_NoHooks_PrintsNoHooksBlock(t *testing.T) {
	svc, game, _ := newProfileExportTestService(t)
	_, err := getProfileManager(svc).Create(context.Background(), game.ID, "plain")
	require.NoError(t, err)

	out := captureStdout(t, func() error {
		return doProfileExport(context.Background(), svc, game, "plain")
	})

	saved, err := getProfileManager(svc).Get(context.Background(), game.ID, "plain")
	require.NoError(t, err)
	require.NotEmpty(t, saved.Collection.ID, "the first export records the minted id")
	assert.Equal(t, "lmm_collection: 1\nid: "+saved.Collection.ID+"\nversion: 1\nname: plain\ngame_id: g1\ngame:\n    name: Game\nmods: []\n", out)
}
