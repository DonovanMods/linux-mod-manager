package core_test

// #455: AdapterFor's refusal is typed, so a read that cannot answer without
// a layout - conflict detection - hands it to the frontend as the known,
// handled state it is (a 409 on the web, the --json envelope's details on
// the CLI) rather than as an anonymous failure a server reports as a 500.

import (
	"context"
	"encoding/json/v2"
	"errors"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAdapterFor_EveryRefusalIsTyped(t *testing.T) {
	cases := map[string]struct {
		setup   func(svc *core.Service, g *domain.Game)
		adapter string
		says    string
	}{
		"an adapter this build does not ship": {
			setup:   func(_ *core.Service, g *domain.Game) { g.Adapter = "bepinx" },
			adapter: "bepinx", says: `game "lethal-company": unknown adapter "bepinx"`,
		},
		"deploy_mode: compile with an adapter that cannot compile": {
			setup:   func(svc *core.Service, g *domain.Game) { compile(svc, g); g.Adapter = "generic-files" },
			adapter: "generic-files", says: "cannot compile",
		},
		"bepinex off the game root": {
			setup:   func(_ *core.Service, g *domain.Game) { g.Adapter = "bepinex"; pluginsModPath(g) },
			adapter: "bepinex", says: "is not its install path",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			svc, game := newBepInExGameRootService(t)
			tc.setup(svc, game)

			_, err := svc.AdapterFor(game)
			var refused *core.AdapterRefusedError
			require.ErrorAs(t, err, &refused)
			assert.Equal(t, game.ID, refused.GameID)
			assert.Equal(t, tc.adapter, refused.Adapter)
			assert.Contains(t, err.Error(), tc.says, "the sentence is the one every flow already printed")
			assert.Equal(t, map[string]any{"game_id": game.ID, "adapter": tc.adapter}, detailsMap(t, refused.Details()))
		})
	}
}

func TestGetProfileConflicts_ARefusedGameAnswersWithTheRefusal(t *testing.T) {
	svc, game := newBepInExGameRootService(t)
	_, err := svc.NewProfileManager().CreateOrResetDefaultAfterGameSave(context.Background(), game.ID)
	require.NoError(t, err)
	seedInstalledMod(t, svc, game, "src", "m1", "1.0", true, map[string][]byte{"a.dll": []byte("x")})
	game.Adapter = "bepinx"
	require.NoError(t, svc.SaveGame(context.Background(), game))

	_, err = svc.GetProfileConflicts(context.Background(), game, "default")
	var refused *core.AdapterRefusedError
	require.ErrorAs(t, err, &refused)
	assert.False(t, errors.Is(err, context.Canceled))
}

// detailsMap is v encoded the way the --json envelope encodes Details().
func detailsMap(t *testing.T, v any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}
