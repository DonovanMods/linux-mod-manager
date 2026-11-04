package curseforge

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #592: the mod response's categories[] already carry names, so the
// primary category's name is read from there with no extra call.
func TestModToDomain_CategoryName(t *testing.T) {
	cats := []Category{{ID: 5, Name: "Armor"}, {ID: 9, Name: "User Interface"}}

	got := modToDomain(Mod{ID: 1, PrimaryCategoryID: 9, Categories: cats}, "432", nil)
	assert.Equal(t, "9", got.Category, "Category keeps the id")
	assert.Equal(t, "User Interface", got.CategoryName, "the primary category's name")

	got = modToDomain(Mod{ID: 1, PrimaryCategoryID: 42, Categories: cats}, "432", nil)
	assert.Equal(t, "Armor", got.CategoryName, "no primary match falls back to the first")

	got = modToDomain(Mod{ID: 1, Categories: []Category{{ID: 3, Name: ""}, {ID: 4, Name: "Maps"}}}, "432", nil)
	assert.Equal(t, "Maps", got.CategoryName, "the first with a name")

	got = modToDomain(Mod{ID: 1, PrimaryCategoryID: 9}, "432", nil)
	assert.Empty(t, got.CategoryName, "no categories, no name")
}

// The update check reports every mod it fetched - an update offered or not -
// so core can backfill the stored category name with no request of its own.
func TestCurseForge_CheckUpdates_ReportsFetchedMods(t *testing.T) {
	requests := 0
	server := batchModsServer(t, &requests, map[int]string{
		111: `{"id":111,"name":"Current","primaryCategoryId":9,"categories":[{"id":9,"name":"User Interface"}],"latestFiles":[{"id":2,"displayName":"mod-a-1.0.0"}],"dateModified":"2024-01-20T10:30:00Z"}`,
	})
	defer server.Close()
	cf := New(server.Client(), "test-api-key")
	cf.client.SetBaseURL(server.URL)

	var seen []domain.Mod
	ctx := source.WithFetchedModObserver(context.Background(), func(m domain.Mod) { seen = append(seen, m) })
	_, err := cf.CheckUpdates(ctx, []domain.InstalledMod{
		{Mod: domain.Mod{ID: "111", Name: "Current", Version: "1.0.0", GameID: "432"}, FileIDs: []string{"2"}},
	})
	require.NoError(t, err)
	require.Len(t, seen, 1)
	assert.Equal(t, "111", seen[0].ID)
	assert.Equal(t, "User Interface", seen[0].CategoryName)
}
