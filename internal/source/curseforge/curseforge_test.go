package curseforge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCurseForge_ImplementsModSource(t *testing.T) {
	// Compile-time check that CurseForge implements ModSource
	var _ source.ModSource = (*CurseForge)(nil)
}

func TestCurseForge_ID(t *testing.T) {
	cf := New(nil, "")
	assert.Equal(t, "curseforge", cf.ID())
}

func TestCurseForge_Name(t *testing.T) {
	cf := New(nil, "")
	assert.Equal(t, "CurseForge", cf.Name())
}

func TestCurseForge_Search(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"data": [
				{
					"id": 238222,
					"gameId": 432,
					"name": "Just Enough Items (JEI)",
					"slug": "jei",
					"summary": "View Items and Recipes",
					"downloadCount": 150000000,
					"thumbsUpCount": 5000,
					"primaryCategoryId": 420,
					"authors": [{"id": 1, "name": "mezz"}],
					"logo": {"thumbnailUrl": "https://example.com/jei.png"},
					"latestFiles": [
						{"id": 12345, "displayName": "jei-1.20.1-15.3.0.4", "fileName": "jei.jar"}
					],
					"dateModified": "2024-01-15T10:30:00Z"
				}
			],
			"pagination": {"index": 0, "pageSize": 20, "resultCount": 1, "totalCount": 1}
		}`))
	}))
	defer server.Close()

	cf := New(server.Client(), "test-api-key")
	cf.client.baseURL = server.URL

	result, err := cf.Search(context.Background(), source.SearchQuery{
		GameID:   "432",
		Query:    "jei",
		PageSize: 20,
	})
	require.NoError(t, err)
	mods := result.Mods
	require.Len(t, mods, 1)

	assert.Equal(t, "238222", mods[0].ID)
	assert.Equal(t, "curseforge", mods[0].SourceID)
	assert.Equal(t, "Just Enough Items (JEI)", mods[0].Name)
	assert.Equal(t, "15.3.0.4", mods[0].Version)
	assert.Equal(t, "mezz", mods[0].Author)
	assert.Equal(t, "View Items and Recipes", mods[0].Summary)
	assert.Equal(t, "432", mods[0].GameID)
	assert.Equal(t, int64(150000000), mods[0].Downloads)
	assert.Equal(t, int64(5000), mods[0].Endorsements)
	assert.Equal(t, "https://example.com/jei.png", mods[0].PictureURL)
}

func TestCurseForge_Search_InvalidGameID(t *testing.T) {
	cf := New(nil, "test-api-key")

	_, err := cf.Search(context.Background(), source.SearchQuery{
		GameID: "not-a-number",
		Query:  "test",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not-a-number")
}

func TestCurseForge_GetMod(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"data": {
				"id": 238222,
				"gameId": 432,
				"name": "Just Enough Items (JEI)",
				"summary": "View Items and Recipes",
				"downloadCount": 150000000,
				"authors": [{"name": "mezz"}],
				"latestFiles": [
					{"displayName": "jei-1.20.1-15.3.0.4"}
				],
				"dateModified": "2024-01-15T10:30:00Z"
			}
		}`))
	}))
	defer server.Close()

	cf := New(server.Client(), "test-api-key")
	cf.client.baseURL = server.URL

	mod, err := cf.GetMod(context.Background(), "432", "238222")
	require.NoError(t, err)

	assert.Equal(t, "238222", mod.ID)
	assert.Equal(t, "curseforge", mod.SourceID)
	assert.Equal(t, "Just Enough Items (JEI)", mod.Name)
	assert.Equal(t, "15.3.0.4", mod.Version)
}

func TestCurseForge_GetDependencies(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"data": [
				{
					"id": 12345,
					"dependencies": [
						{"modId": 306612, "relationType": 3},
						{"modId": 123456, "relationType": 2}
					]
				}
			],
			"pagination": {"index": 0, "pageSize": 50, "resultCount": 1, "totalCount": 1}
		}`))
	}))
	defer server.Close()

	cf := New(server.Client(), "test-api-key")
	cf.client.baseURL = server.URL

	mod := &domain.Mod{ID: "238222", GameID: "432"}
	deps, err := cf.GetDependencies(context.Background(), mod)
	require.NoError(t, err)

	// Should only return required dependencies (relationType 3)
	require.Len(t, deps, 1)
	assert.Equal(t, "curseforge", deps[0].SourceID)
	assert.Equal(t, "306612", deps[0].ModID)
}

func TestCurseForge_GetModFiles(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"data": [
				{
					"id": 12345,
					"displayName": "jei-1.20.1-15.3.0.4",
					"fileName": "jei-1.20.1-15.3.0.4.jar",
					"fileLength": 1234567,
					"releaseType": 1
				},
				{
					"id": 12344,
					"displayName": "jei-1.20.1-15.3.0.3-beta",
					"fileName": "jei-1.20.1-15.3.0.3.jar",
					"fileLength": 1234000,
					"releaseType": 2
				}
			],
			"pagination": {"index": 0, "pageSize": 50, "resultCount": 2, "totalCount": 2}
		}`))
	}))
	defer server.Close()

	cf := New(server.Client(), "test-api-key")
	cf.client.baseURL = server.URL

	mod := &domain.Mod{ID: "238222", GameID: "432"}
	files, err := cf.GetModFiles(context.Background(), mod)
	require.NoError(t, err)
	require.Len(t, files, 2)

	assert.Equal(t, "12345", files[0].ID)
	assert.Equal(t, "jei-1.20.1-15.3.0.4", files[0].Name)
	assert.Equal(t, "jei-1.20.1-15.3.0.4.jar", files[0].FileName)
	assert.Equal(t, int64(1234567), files[0].Size)
	assert.True(t, files[0].IsPrimary)
	assert.Equal(t, "Release", files[0].Category)

	assert.Equal(t, "12344", files[1].ID)
	assert.False(t, files[1].IsPrimary)
	assert.Equal(t, "Beta", files[1].Category)
}

func TestCurseForge_GetDownloadURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"data": "https://edge.forgecdn.net/files/1234/567/jei.jar"
		}`))
	}))
	defer server.Close()

	cf := New(server.Client(), "test-api-key")
	cf.client.baseURL = server.URL

	mod := &domain.Mod{ID: "238222", GameID: "432"}
	url, err := cf.GetDownloadURL(context.Background(), mod, "12345")
	require.NoError(t, err)
	assert.Equal(t, "https://edge.forgecdn.net/files/1234/567/jei.jar", url)
}

func TestCurseForge_CheckUpdates(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		// Return different versions based on call
		if callCount == 1 {
			// First mod has an update
			_, _ = w.Write([]byte(`{
				"data": {
					"id": 238222,
					"name": "JEI",
					"latestFiles": [{"displayName": "jei-1.20.1-15.4.0.0"}],
					"dateModified": "2024-01-20T10:30:00Z"
				}
			}`))
		} else {
			// Second mod is up to date
			_, _ = w.Write([]byte(`{
				"data": {
					"id": 306612,
					"name": "Fabric API",
					"latestFiles": [{"displayName": "fabric-api-0.92.0"}],
					"dateModified": "2024-01-15T10:30:00Z"
				}
			}`))
		}
	}))
	defer server.Close()

	cf := New(server.Client(), "test-api-key")
	cf.client.baseURL = server.URL

	installed := []domain.InstalledMod{
		{
			Mod: domain.Mod{
				ID:       "238222",
				SourceID: "curseforge",
				Name:     "JEI",
				Version:  "15.3.0.4",
				GameID:   "432",
			},
		},
		{
			Mod: domain.Mod{
				ID:       "306612",
				SourceID: "curseforge",
				Name:     "Fabric API",
				Version:  "0.92.0",
				GameID:   "432",
			},
		},
	}

	updates, err := cf.CheckUpdates(context.Background(), installed)
	require.NoError(t, err)

	// Only JEI should have an update
	require.Len(t, updates, 1)
	assert.Equal(t, "238222", updates[0].InstalledMod.ID)
	assert.Equal(t, "15.4.0.0", updates[0].NewVersion)
}

func TestCurseForge_ExchangeToken(t *testing.T) {
	cf := New(nil, "")

	_, err := cf.ExchangeToken(context.Background(), "code")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "API key authentication")
}
