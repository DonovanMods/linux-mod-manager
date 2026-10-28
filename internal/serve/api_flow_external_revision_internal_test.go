package serve

// Issue 538 through the endpoint the SPA hydrates from: GET /api/v1/updates
// records the revision Steam has installed for a tracked Workshop item
// (so the library and the mod page read the real one), and names an item
// Steam no longer lists in the report's external_missing rather than as
// an update.

import (
	"encoding/json/v2"
	"net/http"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedStaleExternalRow tracks the fixture's Workshop item at a revision
// OLDER than the one seedWorkshopFixture's Steam manifest names - the state
// a Steam-side update leaves behind.
func seedStaleExternalRow(t *testing.T, svc *core.Service, game *domain.Game, steamDir string) {
	t.Helper()
	require.NoError(t, svc.SaveInstalledMod(t.Context(), &domain.InstalledMod{
		Mod: domain.Mod{
			ID: "3617086610", SourceID: workshopFixtureSourceID, Name: "Sample Workshop Item",
			Version: "1111111111111111111", GameID: game.ID,
			UpdatedAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		ProfileName: "default", UpdatePolicy: domain.UpdateNotify,
		Enabled: true, Deployed: true, External: true, ExternalPath: steamDir,
	}))
	require.NoError(t, svc.NewProfileManager().UpsertMod(t.Context(), game.ID, "default",
		domain.ModReference{SourceID: workshopFixtureSourceID, ModID: "3617086610", Version: "1111111111111111111"}))
}

func TestAPIFlow_Updates_RecordsTheRevisionSteamInstalled(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)
	steamDir := seedWorkshopFixture(t, svc, game)
	seedStaleExternalRow(t, svc, game, steamDir)

	rec := doAPI(s, http.MethodGet, scoped("/api/v1/updates", game), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var report core.UpdateCheckReport
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &report))
	assert.Empty(t, report.Updates)
	assert.Empty(t, report.ExternalMissing)

	row, err := svc.GetInstalledMod(t.Context(), workshopFixtureSourceID, "3617086610", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "7987119735124793734", row.Version, "the row records Steam's manifest")
	assert.Equal(t, int64(1764767935), row.UpdatedAt.Unix(), "dated by Steam's timeupdated")

	profile, err := svc.NewProfileManager().Get(t.Context(), game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "7987119735124793734", profile.FindRef(workshopFixtureSourceID, "3617086610").Version)
}

func TestAPIFlow_Updates_NamesAnItemSteamNoLongerLists(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)
	steamDir := seedWorkshopFixture(t, svc, game)
	seedStaleExternalRow(t, svc, game, steamDir)
	src, err := svc.GetSource(workshopFixtureSourceID)
	require.NoError(t, err)
	src.(*workshopFixtureSource).scan.Items = nil

	rec := doAPI(s, http.MethodGet, scoped("/api/v1/updates", game), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var report core.UpdateCheckReport
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &report))
	assert.Empty(t, report.Updates)
	assert.Equal(t, []core.ExternalModRef{{SourceID: workshopFixtureSourceID, ModID: "3617086610", Name: "Sample Workshop Item"}}, report.ExternalMissing)

	row, err := svc.GetInstalledMod(t.Context(), workshopFixtureSourceID, "3617086610", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "1111111111111111111", row.Version, "a missing item's record is left alone")
}
