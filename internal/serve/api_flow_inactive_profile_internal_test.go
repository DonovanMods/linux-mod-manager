package serve

// #445 over /api/v1: the web UI offers Deploy and Purge for whichever
// profile the page is showing, including one that is not active. The game
// directory holds the active profile's mods, so both plans are refused -
// 409, naming `lmm profile switch` - and nothing on disk changes.

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFlowInactiveProfile_DeployAndPurgeAreRefused(t *testing.T) {
	for _, kind := range []string{"deploy", "purge"} {
		t.Run(kind, func(t *testing.T) {
			s, svc, game := newFlowFixtureServer(t)
			deployFixtureProfile(t, s, game)
			pm := svc.NewProfileManager()
			require.NoError(t, pm.SetDefault(t.Context(), game.ID, "default"))
			_, err := pm.Create(t.Context(), game.ID, "alt")
			require.NoError(t, err)
			require.NoError(t, svc.SaveInstalledMod(t.Context(), &domain.InstalledMod{
				Mod:          domain.Mod{ID: "m1", SourceID: fixtureSourceID, Name: "Mod One", Version: "1.0", GameID: game.ID},
				ProfileName:  "alt",
				UpdatePolicy: domain.UpdateNotify,
				Enabled:      true,
				Deployed:     true,
			}))

			target := "/api/v1/plans/" + kind + "?" + gameParam + "=" + url.QueryEscape(game.ID) + "&" + profileParam + "=alt"
			rec := doAPI(s, http.MethodPost, target, "")
			require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
			var envelope apiErrorEnvelope
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
			assert.Contains(t, envelope.Error, core.ErrProfileNotActive.Error())
			assert.Contains(t, envelope.Error, "lmm profile switch alt")

			assert.FileExists(t, deployedFixturePath(game), "the active profile's deployment is untouched")
			row, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, "m1", game.ID, "alt")
			require.NoError(t, err)
			assert.True(t, row.Enabled, "and alt's rows are untouched")
		})
	}
}
