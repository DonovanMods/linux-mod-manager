package core_test

// #605: detection's compatdata prefix reaches games.yaml's `prefix:` through
// both doors into it - GameFromDetected (`lmm game detect`'s apply) and
// GameSpecFromDetected (`lmm game add --from-detected`, POST /api/v1/games'
// prefill) - and a repair fills it only where the game sets none.

import (
	"context"
	"path/filepath"
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

func TestGameFromDetected_CarriesThePrefix(t *testing.T) {
	game, err := core.GameFromDetected(detectedSkyrimWithPrefix(t.TempDir()))
	require.NoError(t, err)
	assert.Equal(t, detectedPrefix, game.Prefix)

	game, err = core.GameFromDetected(detectedSkyrim(t.TempDir()))
	require.NoError(t, err)
	assert.Empty(t, game.Prefix, "no compatdata, no prefix: key")
}

func TestGameSpecFromDetected_CarriesThePrefix(t *testing.T) {
	spec := core.GameSpecFromDetected(detectedSkyrimWithPrefix(t.TempDir()), core.GameSpec{})
	assert.Equal(t, detectedPrefix, spec.Prefix)

	spec = core.GameSpecFromDetected(detectedSkyrimWithPrefix(t.TempDir()), core.GameSpec{Prefix: "/mine"})
	assert.Equal(t, "/mine", spec.Prefix, "an explicit prefix wins, like every prefilled field")
}

// AddGame writes the spec's prefix; a relative one is refused on the wire
// key, like `lmm game edit --prefix`.
func TestAddGame_WritesThePrefix(t *testing.T) {
	svc := newGameAddService(t)
	spec := core.GameSpecFromDetected(detectedSkyrimWithPrefix(t.TempDir()), core.GameSpec{})
	_, err := svc.AddGame(context.Background(), spec)
	require.NoError(t, err)
	onDisk, err := config.LoadGames(svc.ConfigDir())
	require.NoError(t, err)
	assert.Equal(t, detectedPrefix, onDisk["skyrim-se"].Prefix)

	bad := core.GameSpecFromDetected(detectedSkyrim(t.TempDir()), core.GameSpec{ID: "other", Prefix: "relative/pfx"})
	_, err = svc.AddGame(context.Background(), bad)
	var spec2 *core.GameSpecError
	require.ErrorAs(t, err, &spec2)
	assert.Equal(t, "prefix", spec2.Field)
}

// A repair (re-detecting a configured game) fills the prefix only where the
// game sets none: one the user set is theirs.
func TestApplyGameDetect_RepairFillsOnlyAMissingPrefix(t *testing.T) {
	svc := newGameDetectTestService(t)
	ctx := context.Background()
	install := t.TempDir()

	_, err := svc.ApplyGameDetect(ctx, []domain.DetectedGame{detectedSkyrim(install)})
	require.NoError(t, err)
	_, err = svc.ApplyGameDetect(ctx, []domain.DetectedGame{detectedSkyrimWithPrefix(install)})
	require.NoError(t, err)
	game, err := svc.GetGame("skyrim-se")
	require.NoError(t, err)
	assert.Equal(t, detectedPrefix, game.Prefix, "a gap is filled")

	mine := filepath.Join(t.TempDir(), "mine")
	game.Prefix = mine
	require.NoError(t, svc.SaveGame(ctx, game))
	_, err = svc.ApplyGameDetect(ctx, []domain.DetectedGame{detectedSkyrimWithPrefix(install)})
	require.NoError(t, err)
	game, err = svc.GetGame("skyrim-se")
	require.NoError(t, err)
	assert.Equal(t, mine, game.Prefix, "the user's own prefix is kept")
}
