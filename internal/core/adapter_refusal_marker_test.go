package core_test

// Where #413's adapter refusal meets #431's `disabled:` marker. Enabling a
// mod clears the marker - on the already-enabled path too, which is the
// documented way back from a document that says off over a row that says
// on - and enabling deploys, so a refused game refuses it. The refusal has
// to come first on both paths: an enable that cleared the marker and then
// refused would leave the next converge run to deploy a mod the user never
// managed to switch on.

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

// defaultProfileBytes reads game's default profile document.
func defaultProfileBytes(t *testing.T, svc *core.Service, game *domain.Game) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(svc.ConfigDir(), "games", game.ID, "profiles", "default.yaml"))
	require.NoError(t, err)
	return string(data)
}

// TestEnableMod_ARefusedEnableLeavesTheMarker: on a refused game, `lmm mod
// enable` of a mod the document marks off changes nothing - not the row,
// not the marker, not the game directory - whether the row says disabled
// or already says enabled.
func TestEnableMod_ARefusedEnableLeavesTheMarker(t *testing.T) {
	ctx := context.Background()
	for name, edit := range refusals {
		for _, rowEnabled := range []bool{false, true} {
			label := name + "/row disabled"
			if rowEnabled {
				label = name + "/row enabled"
			}
			t.Run(label, func(t *testing.T) {
				svc, game, dotfile := newGameRootWithUserConfigs(t)
				mod := onlyMod(t, svc, game)
				if rowEnabled {
					require.NoError(t, svc.NewProfileManager().SetModDisabled(ctx, game.ID, "default", mod.SourceID, mod.ID, true))
				} else {
					_, err := svc.DisableMod(ctx, game, "default", mod.SourceID, mod.ID)
					require.NoError(t, err)
				}
				profile := defaultProfileBytes(t, svc, game)
				require.Contains(t, profile, "disabled: true", "fixture: the document marks the mod off")
				rowBefore := onlyMod(t, svc, game)
				require.Equal(t, rowEnabled, rowBefore.Enabled, "fixture: the row")

				game = refuse(t, svc, game, edit)
				want := refusalOf(t, svc, game)
				tree := byteTreeSnapshot(t, game.InstallPath)

				_, err := svc.EnableMod(ctx, game, "default", mod.SourceID, mod.ID)
				require.Error(t, err)
				assert.Equal(t, want.Error(), err.Error())

				assert.Equal(t, profile, defaultProfileBytes(t, svc, game), "the document, marker included, is byte-identical")
				rowAfter := onlyMod(t, svc, game)
				assert.Equal(t, rowBefore.Enabled, rowAfter.Enabled, "the row's enabled flag")
				assert.Equal(t, rowBefore.Deployed, rowAfter.Deployed, "the row's deployed flag")
				assert.Equal(t, tree, byteTreeSnapshot(t, game.InstallPath), "the tree is byte-identical")
				requireUserConfigsKept(t, game, dotfile)
			})
		}
	}
}

// TestDisableMod_ARefusedGameStillRecordsTheMarker: disable is the removal
// a refused game keeps, and the intent it records belongs in the document
// there as much as anywhere - the way out of the refused state ends with a
// converge run that reads it.
func TestDisableMod_ARefusedGameStillRecordsTheMarker(t *testing.T) {
	ctx := context.Background()
	for name, edit := range refusals {
		t.Run(name, func(t *testing.T) {
			svc, game, dotfile := newGameRootWithUserConfigs(t)
			game = refuse(t, svc, game, edit)
			mod := onlyMod(t, svc, game)
			require.NotContains(t, defaultProfileBytes(t, svc, game), "disabled:", "fixture: no marker yet")

			result, err := svc.DisableMod(ctx, game, "default", mod.SourceID, mod.ID)
			require.NoError(t, err)
			assert.True(t, result.Changed)
			for _, n := range result.Notes {
				assert.NotContains(t, n, "could not record", "the marker write itself succeeded")
			}

			assert.Contains(t, defaultProfileBytes(t, svc, game), "disabled: true")
			row := onlyMod(t, svc, game)
			assert.False(t, row.Enabled)
			assert.False(t, row.Deployed)
			assert.NoFileExists(t, filepath.Join(game.InstallPath, "BepInEx", "plugins", "Rooted.dll"))
			requireUserConfigsKept(t, game, dotfile)
		})
	}
}
