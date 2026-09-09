package core_test

// External tests for GamesUsingSource and SourceInUseError (core/sources.go) -
// the cross-reference both frontends refuse a source removal on.

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGamesUsingSource(t *testing.T) {
	svc, err := core.NewService(core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	ctx := t.Context()
	require.NoError(t, svc.SaveGame(ctx, &domain.Game{
		ID: "zeta", Name: "Zeta", InstallPath: t.TempDir(), ModPath: t.TempDir(),
		SourceIDs: map[string]string{"my-mods": "", "nexusmods": "z"},
	}))
	require.NoError(t, svc.SaveGame(ctx, &domain.Game{
		ID: "alpha", Name: "Alpha", InstallPath: t.TempDir(), ModPath: t.TempDir(),
		SourceIDs: map[string]string{"my-mods": ""},
	}))
	require.NoError(t, svc.SaveGame(ctx, &domain.Game{
		ID: "solo", Name: "Solo", InstallPath: t.TempDir(), ModPath: t.TempDir(),
		SourceIDs: map[string]string{"nexusmods": "s"},
	}))

	assert.Equal(t, []string{"alpha", "zeta"}, svc.GamesUsingSource("my-mods"), "sorted, both games")
	assert.Empty(t, svc.GamesUsingSource("curseforge"), "a source nothing maps")
}

func TestSourceInUseError(t *testing.T) {
	err := &core.SourceInUseError{SourceID: "my-mods", Games: []string{"alpha", "zeta"}}
	assert.Equal(t, `source "my-mods" is configured for 2 game(s): [alpha zeta]`, err.Error())
	assert.Same(t, err, err.Details(), "the envelope's details are the error's own wire fields")
}
