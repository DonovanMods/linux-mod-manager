package curseforge

// #543: a mod whose author turned off third-party distribution
// (allowModDistribution: false) is classified manual-only from the metadata
// CurseForge already returns - on the mod itself and on the update check's
// rows - so no download has to be tried and refused to learn it.

import (
	"context"
	"sync"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModToDomain_AllowModDistribution(t *testing.T) {
	no, yes := false, true
	assert.True(t, modToDomain(Mod{ID: 1, AllowModDistribution: &no}, "432", nil).ManualOnly,
		"an author's opt-out is manual-only")
	assert.False(t, modToDomain(Mod{ID: 1, AllowModDistribution: &yes}, "432", nil).ManualOnly)
	assert.False(t, modToDomain(Mod{ID: 1}, "432", nil).ManualOnly,
		"an absent field says nothing, so the mod is not marked")
}

func TestCurseForge_CheckUpdates_StampsTheAuthorsOptOut(t *testing.T) {
	requests := 0
	server := batchModsServer(t, &requests, map[int]string{
		111: `{"id":111,"name":"Opted Out","allowModDistribution":false,"latestFiles":[{"id":2,"displayName":"mod-a-2.0.0"}],"dateModified":"2024-01-20T10:30:00Z"}`,
		222: `{"id":222,"name":"Served","allowModDistribution":true,"latestFiles":[{"id":4,"displayName":"mod-b-2.0.0"}],"dateModified":"2024-01-20T10:30:00Z"}`,
	})
	defer server.Close()

	cf := New(server.Client(), "test-api-key")
	cf.client.SetBaseURL(server.URL)

	updates, err := cf.CheckUpdates(context.Background(), []domain.InstalledMod{
		{Mod: domain.Mod{ID: "111", Name: "Opted Out", Version: "1.0.0", GameID: "432"}},
		{Mod: domain.Mod{ID: "222", Name: "Served", Version: "1.0.0", GameID: "432"}},
	})
	require.NoError(t, err)
	require.Len(t, updates, 2)
	byID := map[string]domain.Update{}
	for _, u := range updates {
		byID[u.InstalledMod.ID] = u
	}
	assert.True(t, byID["111"].InstalledMod.ManualOnly)
	assert.False(t, byID["222"].InstalledMod.ManualOnly)
}

// #627: the author's allowModDistribution reaches core as an explicit
// tri-state, so core can tell "the author allows it again" from "CurseForge
// said nothing".
func TestModToDomain_Distributable(t *testing.T) {
	no, yes := false, true
	got := modToDomain(Mod{ID: 1, AllowModDistribution: &yes}, "432", nil).Distributable
	require.NotNil(t, got)
	assert.True(t, *got, "an explicit allowModDistribution: true is reported as such")
	got = modToDomain(Mod{ID: 1, AllowModDistribution: &no}, "432", nil).Distributable
	require.NotNil(t, got)
	assert.False(t, *got)
	assert.Nil(t, modToDomain(Mod{ID: 1}, "432", nil).Distributable, "an absent field is reported as absent")
}

// Every mod the update check fetches is reported to core with the
// classification, including one with no update, so core can clear a
// manual-only record for a mod that has nothing to update.
func TestCurseForge_CheckUpdates_ReportsDistributableForEveryFetchedMod(t *testing.T) {
	requests := 0
	server := batchModsServer(t, &requests, map[int]string{
		111: `{"id":111,"name":"Allowed Again","allowModDistribution":true,"latestFiles":[{"id":1,"displayName":"mod-a-1.0.0"}],"dateModified":"2024-01-20T10:30:00Z"}`,
		222: `{"id":222,"name":"Says Nothing","latestFiles":[{"id":4,"displayName":"mod-b-2.0.0"}],"dateModified":"2024-01-20T10:30:00Z"}`,
	})
	defer server.Close()

	cf := New(server.Client(), "test-api-key")
	cf.client.SetBaseURL(server.URL)

	var mu sync.Mutex
	reported := map[string]*bool{}
	ctx := source.WithFetchedModObserver(context.Background(), func(m domain.Mod) {
		mu.Lock()
		defer mu.Unlock()
		reported[m.ID] = m.Distributable
	})
	_, err := cf.CheckUpdates(ctx, []domain.InstalledMod{
		{Mod: domain.Mod{ID: "111", Name: "Allowed Again", Version: "1.0.0", GameID: "432"}},
		{Mod: domain.Mod{ID: "222", Name: "Says Nothing", Version: "1.0.0", GameID: "432"}},
	})
	require.NoError(t, err)
	require.Contains(t, reported, "111")
	require.NotNil(t, reported["111"])
	assert.True(t, *reported["111"])
	require.Contains(t, reported, "222")
	assert.Nil(t, reported["222"])
}
