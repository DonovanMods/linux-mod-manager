package curseforge

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCurseForge_ReportsUpdatedAtOnEverySurface pins #433 for this source:
// a mod's dateModified is its date on search and detail, a file's fileDate
// is its date in the install picker, and a document without one keeps the
// zero time.
func TestCurseForge_ReportsUpdatedAtOnEverySurface(t *testing.T) {
	dated := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/files"):
			_, _ = io.WriteString(w, `{"data":[
				{"id":2,"displayName":"m-1.1","fileName":"m-1.1.jar","releaseType":1,"fileDate":"2024-01-15T10:30:00Z"},
				{"id":1,"displayName":"m-1.0","fileName":"m-1.0.jar","releaseType":1}
			],"pagination":{"index":0,"pageSize":50,"resultCount":2,"totalCount":2}}`)
		case strings.HasSuffix(r.URL.Path, "/search"):
			_, _ = io.WriteString(w, `{"data":[
				{"id":1,"name":"Dated","latestFiles":[{"displayName":"m-1.1"}],"dateModified":"2024-01-15T10:30:00Z"},
				{"id":2,"name":"Undated","latestFiles":[{"displayName":"n-1.0"}]}
			],"pagination":{"index":0,"pageSize":20,"resultCount":2,"totalCount":2}}`)
		default:
			_, _ = io.WriteString(w, `{"data":{"id":1,"name":"Dated","latestFiles":[{"displayName":"m-1.1"}],"dateModified":"2024-01-15T10:30:00Z"}}`)
		}
	}))
	defer server.Close()

	cf := New(server.Client(), "test-api-key")
	cf.client.SetBaseURL(server.URL)
	ctx := context.Background()

	found, err := cf.Search(ctx, source.SearchQuery{GameID: "432", Query: "m", PageSize: 20})
	require.NoError(t, err)
	require.Len(t, found.Mods, 2)
	assert.True(t, found.Mods[0].UpdatedAt.Equal(dated), "dated hit: %v", found.Mods[0].UpdatedAt)
	assert.True(t, found.Mods[1].UpdatedAt.IsZero(), "undated hit: %v", found.Mods[1].UpdatedAt)

	mod, err := cf.GetMod(ctx, "432", "1")
	require.NoError(t, err)
	assert.True(t, mod.UpdatedAt.Equal(dated), "GetMod: %v", mod.UpdatedAt)

	files, err := cf.GetModFiles(ctx, &domain.Mod{ID: "1", GameID: "432"})
	require.NoError(t, err)
	require.Len(t, files, 2)
	assert.True(t, files[0].UploadedAt.Equal(dated), "dated file: %v", files[0].UploadedAt)
	assert.True(t, files[1].UploadedAt.IsZero(), "undated file: %v", files[1].UploadedAt)
}
