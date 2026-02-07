package curseforge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClient_SearchMods(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/mods/search", r.URL.Path)
		assert.Equal(t, "432", r.URL.Query().Get("gameId"))
		assert.Equal(t, "jei", r.URL.Query().Get("searchFilter"))
		assert.Equal(t, "test-api-key", r.Header.Get("x-api-key"))

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
					"authors": [{"id": 1, "name": "mezz", "url": "https://curseforge.com/members/mezz"}],
					"logo": {"thumbnailUrl": "https://example.com/jei.png"},
					"latestFiles": [],
					"dateModified": "2024-01-15T10:30:00Z"
				}
			],
			"pagination": {
				"index": 0,
				"pageSize": 20,
				"resultCount": 1,
				"totalCount": 1
			}
		}`))
	}))
	defer server.Close()

	client := NewClient(server.Client(), "test-api-key")
	client.baseURL = server.URL

	mods, pagination, err := client.SearchMods(context.Background(), 432, "jei", 0, 20, 0)
	require.NoError(t, err)
	require.Len(t, mods, 1)

	assert.Equal(t, 238222, mods[0].ID)
	assert.Equal(t, "Just Enough Items (JEI)", mods[0].Name)
	assert.Equal(t, "View Items and Recipes", mods[0].Summary)
	assert.Equal(t, int64(150000000), mods[0].DownloadCount)
	assert.Equal(t, "mezz", mods[0].Authors[0].Name)

	assert.Equal(t, 1, pagination.ResultCount)
	assert.Equal(t, 1, pagination.TotalCount)
}

func TestClient_GetMod(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/mods/238222", r.URL.Path)
		assert.Equal(t, "test-api-key", r.Header.Get("x-api-key"))

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"data": {
				"id": 238222,
				"gameId": 432,
				"name": "Just Enough Items (JEI)",
				"slug": "jei",
				"summary": "View Items and Recipes",
				"downloadCount": 150000000,
				"thumbsUpCount": 5000,
				"primaryCategoryId": 420,
				"authors": [{"id": 1, "name": "mezz", "url": "https://curseforge.com/members/mezz"}],
				"logo": {"thumbnailUrl": "https://example.com/jei.png"},
				"latestFiles": [
					{
						"id": 12345,
						"displayName": "jei-1.20.1-15.3.0.4",
						"fileName": "jei-1.20.1-15.3.0.4.jar",
						"fileLength": 1234567,
						"releaseType": 1
					}
				],
				"dateModified": "2024-01-15T10:30:00Z"
			}
		}`))
	}))
	defer server.Close()

	client := NewClient(server.Client(), "test-api-key")
	client.baseURL = server.URL

	mod, err := client.GetMod(context.Background(), 238222)
	require.NoError(t, err)

	assert.Equal(t, 238222, mod.ID)
	assert.Equal(t, "Just Enough Items (JEI)", mod.Name)
	assert.Equal(t, 5000, mod.ThumbsUpCount)
	assert.Equal(t, 420, mod.PrimaryCategoryID)
	require.Len(t, mod.LatestFiles, 1)
	assert.Equal(t, "jei-1.20.1-15.3.0.4", mod.LatestFiles[0].DisplayName)
}

func TestClient_GetModFiles(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/mods/238222/files", r.URL.Path)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"data": [
				{
					"id": 12345,
					"modId": 238222,
					"displayName": "jei-1.20.1-15.3.0.4",
					"fileName": "jei-1.20.1-15.3.0.4.jar",
					"fileLength": 1234567,
					"releaseType": 1,
					"dependencies": [
						{"modId": 306612, "relationType": 3}
					],
					"gameVersions": ["1.20.1", "Forge"]
				},
				{
					"id": 12344,
					"modId": 238222,
					"displayName": "jei-1.20.1-15.3.0.3",
					"fileName": "jei-1.20.1-15.3.0.3.jar",
					"fileLength": 1234000,
					"releaseType": 1,
					"dependencies": [],
					"gameVersions": ["1.20.1", "Forge"]
				}
			],
			"pagination": {
				"index": 0,
				"pageSize": 50,
				"resultCount": 2,
				"totalCount": 2
			}
		}`))
	}))
	defer server.Close()

	client := NewClient(server.Client(), "test-api-key")
	client.baseURL = server.URL

	files, err := client.GetModFiles(context.Background(), 238222)
	require.NoError(t, err)
	require.Len(t, files, 2)

	assert.Equal(t, 12345, files[0].ID)
	assert.Equal(t, "jei-1.20.1-15.3.0.4.jar", files[0].FileName)
	assert.Equal(t, int64(1234567), files[0].FileLength)
	assert.Equal(t, ReleaseTypeRelease, files[0].ReleaseType)
	require.Len(t, files[0].Dependencies, 1)
	assert.Equal(t, 306612, files[0].Dependencies[0].ModID)
	assert.Equal(t, RelationRequiredDependency, files[0].Dependencies[0].RelationType)
}

func TestClient_GetDownloadURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/mods/238222/files/12345/download-url", r.URL.Path)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"data": "https://edge.forgecdn.net/files/1234/567/jei-1.20.1-15.3.0.4.jar"
		}`))
	}))
	defer server.Close()

	client := NewClient(server.Client(), "test-api-key")
	client.baseURL = server.URL

	url, err := client.GetDownloadURL(context.Background(), 238222, 12345)
	require.NoError(t, err)
	assert.Equal(t, "https://edge.forgecdn.net/files/1234/567/jei-1.20.1-15.3.0.4.jar", url)
}

func TestClient_AuthRequired(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error": "API key required"}`))
	}))
	defer server.Close()

	client := NewClient(server.Client(), "")
	client.baseURL = server.URL

	_, _, err := client.SearchMods(context.Background(), 432, "test", 0, 20, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "API key required")
}

func TestClient_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error": "Not found"}`))
	}))
	defer server.Close()

	client := NewClient(server.Client(), "test-api-key")
	client.baseURL = server.URL

	_, err := client.GetMod(context.Background(), 99999)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}
