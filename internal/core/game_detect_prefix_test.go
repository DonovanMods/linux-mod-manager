package core_test

// #605: detection reports the compatdata prefix it found (DetectedGame.
// Prefix) as information only. It is never written to games.yaml - by
// GameFromDetected (`lmm game detect`'s apply), by the repair of a
// configured game, or by `lmm game add --from-detected` - because
// ResolvePrefix derives the same prefix at runtime whenever `prefix:` is
// absent, and a pinned copy would only go stale (a game moved to another
// library, a wiped compatdata) while counting as the user's explicit
// choice. `prefix:` is the user's override, and only theirs.

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const detectedPrefix = "/home/user/.steam/steam/steamapps/compatdata/489830/pfx"

func detectedSkyrimWithPrefix(install string) domain.DetectedGame {
	d := detectedSkyrim(install)
	d.Prefix = detectedPrefix
	return d
}

func TestGameFromDetected_DoesNotPinTheDerivedPrefix(t *testing.T) {
	game, err := core.GameFromDetected(detectedSkyrimWithPrefix(t.TempDir()))
	require.NoError(t, err)
	assert.Empty(t, game.Prefix, "the prefix is derived at runtime, never written")
}

// `lmm game add --from-detected` writes no prefix: either.
func TestAddGameFromDetected_DoesNotPinTheDerivedPrefix(t *testing.T) {
	svc := newGameAddService(t)
	spec := core.GameSpecFromDetected(detectedSkyrimWithPrefix(t.TempDir()), core.GameSpec{})
	_, err := svc.AddGame(context.Background(), spec)
	require.NoError(t, err)
	onDisk, err := config.LoadGames(svc.ConfigDir())
	require.NoError(t, err)
	assert.Empty(t, onDisk["skyrim-se"].Prefix)
}

// Neither the first apply nor a repair writes the derived prefix, and a
// repair keeps a prefix the user set.
func TestApplyGameDetect_NeverWritesTheDerivedPrefix(t *testing.T) {
	svc := newGameDetectTestService(t)
	ctx := context.Background()
	install := t.TempDir()

	_, err := svc.ApplyGameDetect(ctx, []domain.DetectedGame{detectedSkyrimWithPrefix(install)})
	require.NoError(t, err)
	_, err = svc.ApplyGameDetect(ctx, []domain.DetectedGame{detectedSkyrimWithPrefix(install)})
	require.NoError(t, err)
	onDisk, err := config.LoadGames(svc.ConfigDir())
	require.NoError(t, err)
	assert.Empty(t, onDisk["skyrim-se"].Prefix, "neither the add nor the repair pins it")

	game, err := svc.GetGame("skyrim-se")
	require.NoError(t, err)
	game.Prefix = "/home/user/Games/mine"
	require.NoError(t, svc.SaveGame(ctx, game))
	_, err = svc.ApplyGameDetect(ctx, []domain.DetectedGame{detectedSkyrimWithPrefix(install)})
	require.NoError(t, err)
	game, err = svc.GetGame("skyrim-se")
	require.NoError(t, err)
	assert.Equal(t, "/home/user/Games/mine", game.Prefix, "the user's own prefix is kept")
}
