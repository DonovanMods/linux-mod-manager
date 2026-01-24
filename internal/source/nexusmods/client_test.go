package nexusmods

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"lmm/internal/domain"

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
	// GraphQL returns filtered results from the server
	graphqlResponse := `{
		"data": {
			"mods": {
				"nodes": [
					{"modId": 1, "name": "OreStack Mod", "summary": "", "version": "1.0.0", "uploader": {"name": "Author1"}},
					{"modId": 3, "name": "OreStack Enhancer", "summary": "", "version": "1.0.0", "uploader": {"name": "Author2"}}
				]
			}
		}
	}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(graphqlResponse))
	}))
	defer server.Close()

	client := NewClient(nil, "testapikey")
	client.graphqlURL = server.URL

	// Search returns results from GraphQL API
	mods, err := client.SearchMods(context.Background(), "starrupture", "orestack", 10, 0)
	require.NoError(t, err)
	assert.Len(t, mods, 2)
}

func TestClient_SetAPIKey(t *testing.T) {
	client := NewClient(nil, "")

	assert.False(t, client.IsAuthenticated())

	client.SetAPIKey("new-api-key")
	assert.True(t, client.IsAuthenticated())
	assert.Equal(t, "new-api-key", client.apiKey)
}

func TestClient_IsAuthenticated(t *testing.T) {
	tests := []struct {
		name     string
		apiKey   string
		expected bool
	}{
		{"empty key", "", false},
		{"with key", "test-key", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient(nil, tt.apiKey)
			assert.Equal(t, tt.expected, client.IsAuthenticated())
		})
	}
}

func TestClient_ValidateAPIKey_Success(t *testing.T) {
	// Mock response for /v1/users/validate.json
	mockResponse := map[string]interface{}{
		"user_id":       12345,
		"key":           "test-key",
		"name":          "TestUser",
		"is_premium":    false,
		"is_supporter":  false,
		"email":         "test@example.com",
		"profile_url":   "https://www.nexusmods.com/users/12345",
		"is_premium?":   false,
		"is_supporter?": false,
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/users/validate.json", r.URL.Path)
		assert.Equal(t, "test-key", r.Header.Get("apikey"))

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(mockResponse)
	}))
	defer server.Close()

	client := NewClient(nil, "")
	client.baseURL = server.URL

	err := client.ValidateAPIKey(context.Background(), "test-key")
	require.NoError(t, err)
}

func TestClient_ValidateAPIKey_Invalid(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"message":"Invalid API Key"}`))
	}))
	defer server.Close()

	client := NewClient(nil, "")
	client.baseURL = server.URL

	err := client.ValidateAPIKey(context.Background(), "invalid-key")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid API key")
}

func TestClient_ReturnsAuthRequired_On401(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"message":"Not authorized"}`))
	}))
	defer server.Close()

	client := NewClient(nil, "bad-key")
	client.baseURL = server.URL

	_, err := client.GetMod(context.Background(), "starrupture", 12345)
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrAuthRequired)
}

func TestClient_SearchMods_UsesGraphQL(t *testing.T) {
	// Mock GraphQL response
	graphqlResponse := `{
		"data": {
			"mods": {
				"nodes": [
					{"modId": 1, "name": "Ore Mod", "summary": "Test", "version": "1.0.0", "uploader": {"name": "Author1"}},
					{"modId": 2, "name": "OreStack", "summary": "Stack", "version": "2.0.0", "uploader": {"name": "Author2"}},
					{"modId": 3, "name": "Better Ores", "summary": "Better", "version": "1.5.0", "uploader": {"name": "Author3"}}
				]
			}
		}
	}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify it's a GraphQL request
		assert.Equal(t, "/v2/graphql", r.URL.Path)
		assert.Equal(t, "POST", r.Method)

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(graphqlResponse))
	}))
	defer server.Close()

	client := NewClient(nil, "testapikey")
	client.graphqlURL = server.URL + "/v2/graphql"

	mods, err := client.SearchMods(context.Background(), "starrupture", "ore", 10, 0)
	require.NoError(t, err)
	assert.Len(t, mods, 3)

	// Verify mod data is parsed correctly
	assert.Equal(t, 1, mods[0].ModID)
	assert.Equal(t, "Ore Mod", mods[0].Name)
	assert.Equal(t, "Author1", mods[0].Author)
}
