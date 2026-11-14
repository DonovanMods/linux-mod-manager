package config_test

// #620: a profile's `load_orders:` block - per file load order id, the file
// names in order. A top-level, omitempty key: a profile without one is
// unchanged, and an in-place save adds or removes only that key.

import (
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/require"
)

func loadOrdersFixture() domain.FileLoadOrders {
	return domain.FileLoadOrders{"cyberpunk2077-archive": {"! fixes.archive", "basegame_b.archive", "basegame_a.archive"}}
}

func TestSaveProfile_LoadOrdersRoundTrip(t *testing.T) {
	dir := t.TempDir()
	profile := &domain.Profile{Name: "p", GameID: "tg", LoadOrders: loadOrdersFixture()}
	require.NoError(t, config.SaveProfile(dir, profile))
	got := readFile(t, dir+"/games/tg/profiles/p.yaml")
	require.Contains(t, got, "load_orders:\n    cyberpunk2077-archive:\n        - '! fixes.archive'\n")

	reloaded, err := config.LoadProfile(dir, "tg", "p")
	require.NoError(t, err)
	require.Equal(t, profile.LoadOrders, reloaded.LoadOrders)
}

func TestSaveProfile_LoadOrdersAreEditedInPlace(t *testing.T) {
	dir := t.TempDir()
	path := writeProfileFile(t, dir, "tg", "default", handEdited)

	profile, err := config.LoadProfile(dir, "tg", "default")
	require.NoError(t, err)
	require.True(t, profile.LoadOrders.IsZero(), "a file without the key has no load order")

	profile.LoadOrders = loadOrdersFixture()
	require.NoError(t, config.SaveProfile(dir, profile))
	got := readFile(t, path)
	require.True(t, strings.HasPrefix(got, handEdited), "the author's text is kept, the key appended:\n%s", got)

	reloaded, err := config.LoadProfile(dir, "tg", "default")
	require.NoError(t, err)
	require.Equal(t, profile.LoadOrders, reloaded.LoadOrders)

	reloaded.LoadOrders = nil
	require.NoError(t, config.SaveProfile(dir, reloaded))
	require.Equal(t, handEdited, readFile(t, path), "clearing the order restores the file")
}

func TestExportProfileValue_CarriesLoadOrders(t *testing.T) {
	profile := &domain.Profile{Name: "p", GameID: "tg", LoadOrders: loadOrdersFixture()}
	exported := config.ExportProfileValue(profile)
	require.Equal(t, profile.LoadOrders, exported.LoadOrders)
	exported.LoadOrders["cyberpunk2077-archive"][0] = "changed"
	require.Equal(t, "! fixes.archive", profile.LoadOrders["cyberpunk2077-archive"][0], "the export holds its own copy")
	back := config.ProfileFromExported(exported)
	require.Equal(t, exported.LoadOrders, back.LoadOrders)
}

func TestCollectionDocument_LoadOrdersRoundTrip(t *testing.T) {
	doc := &domain.CollectionDocument{FormatVersion: 1, Name: "c", LoadOrders: loadOrdersFixture()}
	data, err := config.MarshalCollection(doc)
	require.NoError(t, err)
	require.Contains(t, string(data), "load_orders:")
	back, err := config.ParseCollection(data)
	require.NoError(t, err)
	require.Equal(t, doc.LoadOrders, back.LoadOrders)
}
