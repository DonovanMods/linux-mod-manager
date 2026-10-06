package curseforge

// #539: a mod the CurseForge API omits from a successful batch answer is
// gone from the catalog, and is reported per mod as a
// *source.ModNotFoundError so core reports it as gone. A mod missing only
// because its chunk's REQUEST failed is not: that is an outage, not a fact
// about the mod.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCurseForge_CheckUpdates_OmittedModIsModNotFoundError(t *testing.T) {
	requests := 0
	server := batchModsServer(t, &requests, map[int]string{
		111: `{"id":111,"name":"Mod A","latestFiles":[{"displayName":"mod-a-2.0.0"}],"dateModified":"2024-01-20T10:30:00Z"}`,
	})
	defer server.Close()

	cf := New(server.Client(), "test-api-key")
	cf.client.SetBaseURL(server.URL)

	installed := []domain.InstalledMod{
		{Mod: domain.Mod{ID: "111", Name: "Mod A", Version: "1.0.0", GameID: "432"}},
		{Mod: domain.Mod{ID: "999", Name: "Gone Mod", Version: "1.0.0", GameID: "432"}},
		{Mod: domain.Mod{ID: "not-a-number", Name: "Hand Made", Version: "1.0.0", GameID: "432"}},
	}

	_, err := cf.CheckUpdates(context.Background(), installed)
	require.Error(t, err)
	missing, rest := source.SplitModNotFound(err)
	require.Len(t, missing, 1)
	assert.Equal(t, "999", missing[0].ModID)
	require.Error(t, rest, "the unparseable id is still a failure")
	assert.Contains(t, rest.Error(), "Hand Made")
	assert.NotContains(t, rest.Error(), "Gone Mod")
}

func TestCurseForge_CheckUpdates_FailedChunkIsNotModNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream down", http.StatusBadGateway)
	}))
	defer server.Close()

	cf := New(server.Client(), "test-api-key")
	cf.client.SetBaseURL(server.URL)

	installed := []domain.InstalledMod{
		{Mod: domain.Mod{ID: "111", Name: "Mod A", Version: "1.0.0", GameID: "432"}},
	}

	_, err := cf.CheckUpdates(context.Background(), installed)
	require.Error(t, err)
	var nf *source.ModNotFoundError
	assert.False(t, errors.As(err, &nf), "a failed request says nothing about whether the mod exists: %v", err)
}
