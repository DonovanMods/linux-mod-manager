package curseforge

// #543: a mod whose author turned off third-party distribution
// (allowModDistribution: false) is classified manual-only from the metadata
// CurseForge already returns - on the mod itself and on the update check's
// rows - so no download has to be tried and refused to learn it.

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModToDomain_AllowModDistribution(t *testing.T) {
	no, yes := false, true
	assert.True(t, modToDomain(Mod{ID: 1, AllowModDistribution: &no}, "432").ManualOnly,
		"an author's opt-out is manual-only")
	assert.False(t, modToDomain(Mod{ID: 1, AllowModDistribution: &yes}, "432").ManualOnly)
	assert.False(t, modToDomain(Mod{ID: 1}, "432").ManualOnly,
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
