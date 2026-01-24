package nexusmods

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClient_GetMod(t *testing.T) {
	// Mock response from NexusMods REST API v1
	mockResponse := ModData{
		ModID:            12345,
		Name:             "Test Mod",
		Summary:          "A test mod",
		Description:      "This is a test mod description",
		Version:          "1.0.0",
		Author:           "TestAuthor",
		UploadedBy:       "TestUploader",
		CategoryID:       5,
		EndorsementCount: 100,
		DomainName:       "starrupture",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify the request path
		assert.Equal(t, "/v1/games/starrupture/mods/12345.json", r.URL.Path)
		assert.Equal(t, "testapikey", r.Header.Get("apikey"))

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(mockResponse)
	}))
	defer server.Close()

	client := NewClient(nil, "testapikey")
	client.baseURL = server.URL // Override for testing

	mod, err := client.GetMod(context.Background(), "starrupture", 12345)
	require.NoError(t, err)
	assert.Equal(t, 12345, mod.ModID)
	assert.Equal(t, "Test Mod", mod.Name)
	assert.Equal(t, "TestAuthor", mod.Author)
	assert.Equal(t, 100, mod.EndorsementCount)
}

func TestClient_GetLatestAdded(t *testing.T) {
	mockResponse := []ModData{
		{
			ModID:   1,
			Name:    "First Mod",
			Version: "1.0.0",
			Author:  "Author1",
		},
		{
			ModID:   2,
			Name:    "Second Mod",
			Version: "2.0.0",
			Author:  "Author2",
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/games/starrupture/mods/latest_added.json", r.URL.Path)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(mockResponse)
	}))
	defer server.Close()

	client := NewClient(nil, "testapikey")
	client.baseURL = server.URL

	mods, err := client.GetLatestAdded(context.Background(), "starrupture")
	require.NoError(t, err)
	assert.Len(t, mods, 2)
	assert.Equal(t, "First Mod", mods[0].Name)
}

func TestClient_SearchMods_FiltersByQuery(t *testing.T) {
	// GetLatestAdded returns all mods, SearchMods filters them
	mockResponse := []ModData{
		{ModID: 1, Name: "OreStack Mod", Version: "1.0.0"},
		{ModID: 2, Name: "Another Mod", Version: "1.0.0"},
		{ModID: 3, Name: "OreStack Enhancer", Version: "1.0.0"},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(mockResponse)
	}))
	defer server.Close()

	client := NewClient(nil, "testapikey")
	client.baseURL = server.URL

	// Search should filter results containing "orestack" (case-insensitive)
	mods, err := client.SearchMods(context.Background(), "starrupture", "orestack", 10, 0)
	require.NoError(t, err)
	assert.Len(t, mods, 2) // Should match "OreStack Mod" and "OreStack Enhancer"
}
