package nexusmods

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

// TestNexusMods_ReportsUpdatedAtOnEverySurface pins #433 for this source.
// The REST mod document always carried updated_time, but the GraphQL search
// asked for no date at all, so every search hit had the zero time. The
// search now selects the Mod type's `updatedAt: DateTime!` (introspected
// against the live v2 schema on 2026-09-17), and a node that omits it keeps
// the zero value. A file's uploaded_time reaches the install picker.
func TestNexusMods_ReportsUpdatedAtOnEverySurface(t *testing.T) {
	dated := time.Date(2025, 11, 2, 8, 30, 0, 0, time.UTC)

	gql := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		assert.Contains(t, string(body), "updatedAt", "the search must ask for the date")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":{"mods":{"nodes":[
			{"modId":1,"name":"Dated","version":"1.0","updatedAt":"2025-11-02T08:30:00Z","uploader":{"name":"a"}},
			{"modId":2,"name":"Undated","version":"1.0","uploader":{"name":"b"}}
		]}}}`)
	}))
	defer gql.Close()

	rest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/files.json"):
			_, _ = io.WriteString(w, `{"files":[
				{"file_id":10,"name":"Main","file_name":"m.zip","version":"1.0","is_primary":true,"uploaded_time":"2025-11-02T08:30:00Z"},
				{"file_id":11,"name":"Old","file_name":"o.zip","version":"0.9"}
			]}`)
		default:
			_, _ = io.WriteString(w, `{"mod_id":1,"name":"Dated","version":"1.0","updated_time":"2025-11-02T08:30:00Z"}`)
		}
	}))
	defer rest.Close()

	nm := New(nil, "testapikey")
	nm.client.SetBaseURL(rest.URL)
	nm.client.graphqlURL = gql.URL
	ctx := context.Background()

	found, err := nm.Search(ctx, source.SearchQuery{GameID: "skyrim", Query: "x"})
	require.NoError(t, err)
	require.Len(t, found.Mods, 2)
	assert.True(t, found.Mods[0].UpdatedAt.Equal(dated), "dated hit: %v", found.Mods[0].UpdatedAt)
	assert.True(t, found.Mods[1].UpdatedAt.IsZero(), "undated hit: %v", found.Mods[1].UpdatedAt)

	mod, err := nm.GetMod(ctx, "skyrim", "1")
	require.NoError(t, err)
	assert.True(t, mod.UpdatedAt.Equal(dated), "GetMod: %v", mod.UpdatedAt)

	files, err := nm.GetModFiles(ctx, &domain.Mod{ID: "1", GameID: "skyrim"})
	require.NoError(t, err)
	require.Len(t, files, 2)
	assert.True(t, files[0].UploadedAt.Equal(dated), "dated file: %v", files[0].UploadedAt)
	assert.True(t, files[1].UploadedAt.IsZero(), "undated file: %v", files[1].UploadedAt)
}
