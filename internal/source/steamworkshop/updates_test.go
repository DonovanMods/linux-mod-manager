package steamworkshop_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/steamworkshop"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func installedItem(id, version string, updatedAt int64) domain.InstalledMod {
	im := domain.InstalledMod{
		Mod: domain.Mod{
			ID: id, SourceID: "steamworkshop", Name: "Item " + id, Version: version, GameID: "1133870",
		},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		Deployed:     true,
		External:     true,
		ExternalPath: "/steam/workshop/content/1133870/" + id,
	}
	if updatedAt > 0 {
		im.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	}
	return im
}

func TestCheckUpdates_ContentIDMismatchIsThePrimarySignal(t *testing.T) {
	fx := serveFixture(t, "getpublishedfiledetails_ok.json")
	src := newTestSource(t, fx.srv.URL, t.TempDir(), nil)

	updates, err := src.CheckUpdates(context.Background(), []domain.InstalledMod{
		// Installed at an older content id: an update.
		installedItem("3617086610", "1111111111111111111", 1764767935),
		// Installed at exactly the API's content id: no update, even though
		// its recorded timestamp is older than the API's.
		installedItem("3512001122", "1122334455667788991", 1700000000),
	})
	require.NoError(t, err)
	require.Len(t, updates, 1)
	assert.Equal(t, "3617086610", updates[0].InstalledMod.ID)
	assert.Equal(t, "7987119735124793734", updates[0].NewVersion)
}

func TestCheckUpdates_TimestampIsTheSecondarySignalWhenNoContentID(t *testing.T) {
	fx := serveFixture(t, "getpublishedfiledetails_nomanifest.json")
	src := newTestSource(t, fx.srv.URL, t.TempDir(), nil)

	t.Run("a newer time_updated is an update", func(t *testing.T) {
		updates, err := src.CheckUpdates(context.Background(), []domain.InstalledMod{
			installedItem("1000000001", "", 1712345678),
		})
		require.NoError(t, err)
		require.Len(t, updates, 1)
	})

	t.Run("the same or an older time_updated is not", func(t *testing.T) {
		updates, err := src.CheckUpdates(context.Background(), []domain.InstalledMod{
			installedItem("1000000001", "", 1799999999),
		})
		require.NoError(t, err)
		assert.Empty(t, updates)
	})

	t.Run("no recorded timestamp at all cannot be compared, so nothing is claimed", func(t *testing.T) {
		updates, err := src.CheckUpdates(context.Background(), []domain.InstalledMod{
			installedItem("1000000001", "", 0),
		})
		require.NoError(t, err)
		assert.Empty(t, updates)
	})
}

func TestCheckUpdates_UnavailableItemNeverReportsAnUpdate(t *testing.T) {
	fx := serveFixture(t, "getpublishedfiledetails_mixed_result9.json")
	src := newTestSource(t, fx.srv.URL, t.TempDir(), nil)

	updates, err := src.CheckUpdates(context.Background(), []domain.InstalledMod{
		installedItem("2900001111", "9988776655443322110", 1700000000),
	})
	require.NoError(t, err, "one dead item is data, not a failed check")
	assert.Empty(t, updates, `"delisted" and "out of date" are different facts`)
}

func TestCheckUpdates_ReportsProgressPerItem(t *testing.T) {
	fx := serveFixture(t, "getpublishedfiledetails_ok.json")
	src := newTestSource(t, fx.srv.URL, t.TempDir(), nil)

	var ticks []string
	_, err := src.CheckUpdatesWithProgress(context.Background(),
		[]domain.InstalledMod{installedItem("3617086610", "x", 0), installedItem("3512001122", "y", 0)},
		func(n, total int, name string) {
			assert.Equal(t, 2, total)
			ticks = append(ticks, name)
		})
	require.NoError(t, err)
	assert.Equal(t, []string{"Item 3617086610", "Item 3512001122"}, ticks)
}

func TestCheckUpdates_APIFailureIsAnErrorNotSilence(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()
	src := newTestSource(t, srv.URL, t.TempDir(), nil)

	_, err := src.CheckUpdates(context.Background(), []domain.InstalledMod{installedItem("3617086610", "x", 0)})
	require.Error(t, err, `an item lmm could not ask about must not read as "up to date"`)
	assert.ErrorIs(t, err, steamworkshop.ErrMetadataUnavailable)
}

func TestCheckUpdatesRefreshing_BypassesTheMetadataCache(t *testing.T) {
	fx := serveFixture(t, "getpublishedfiledetails_ok.json")
	clock := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	src := newTestSource(t, fx.srv.URL, t.TempDir(), func() time.Time { return clock })
	installed := []domain.InstalledMod{installedItem("3617086610", "x", 0)}

	_, err := src.CheckUpdates(context.Background(), installed)
	require.NoError(t, err)
	require.Equal(t, 1, fx.calls)

	_, err = src.CheckUpdates(context.Background(), installed)
	require.NoError(t, err)
	assert.Equal(t, 1, fx.calls, "the cached answer serves the second check")

	_, err = src.CheckUpdatesRefreshing(context.Background(), installed, true, nil)
	require.NoError(t, err)
	assert.Equal(t, 2, fx.calls, "--refresh asks Steam again")
}

func TestDescribeItems_ReportsUnavailabilityPerItem(t *testing.T) {
	fx := serveFixture(t, "getpublishedfiledetails_mixed_result9.json")
	src := newTestSource(t, fx.srv.URL, t.TempDir(), nil)

	statuses, err := src.DescribeItems(context.Background(), "1133870",
		[]string{"3617086610", "2900001111", "4000000000"}, false)
	require.NoError(t, err)
	require.Len(t, statuses, 3)

	assert.False(t, statuses[0].Unavailable)
	assert.Equal(t, "Sample Workshop Item", statuses[0].Mod.Name)

	assert.True(t, statuses[1].Unavailable, "result: 9 is unavailable")
	assert.NotEmpty(t, statuses[1].Note)

	assert.True(t, statuses[2].Unavailable, "an id the API never mentions is unavailable too")
}
