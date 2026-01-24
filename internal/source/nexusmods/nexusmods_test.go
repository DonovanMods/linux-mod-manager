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

func TestNexusMods_GetModFiles(t *testing.T) {
	mockResponse := ModFileList{
		Files: []FileData{
			{
				FileID:       100,
				Name:         "Main File",
				FileName:     "test-mod-1-0.zip",
				Version:      "1.0.0",
				CategoryID:   1,
				CategoryName: "MAIN",
				IsPrimary:    true,
				Size:         1234,
				SizeKB:       1,
				Description:  "Main installation file",
			},
			{
				FileID:       101,
				Name:         "Optional Patch",
				FileName:     "test-mod-patch-1-0.zip",
				Version:      "1.0.0",
				CategoryID:   4,
				CategoryName: "OPTIONAL",
				IsPrimary:    false,
				Size:         456,
				SizeKB:       0,
				Description:  "Optional quality improvements",
			},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/games/starrupture/mods/12345/files.json", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(mockResponse)
	}))
	defer server.Close()

	nm := New(nil, "testapikey")
	nm.client.baseURL = server.URL

	mod := &domain.Mod{
		ID:     "12345",
		GameID: "starrupture",
	}

	files, err := nm.GetModFiles(context.Background(), mod)
	require.NoError(t, err)
	assert.Len(t, files, 2)

	// Verify primary file
	assert.Equal(t, "100", files[0].ID)
	assert.Equal(t, "Main File", files[0].Name)
	assert.Equal(t, "test-mod-1-0.zip", files[0].FileName)
	assert.True(t, files[0].IsPrimary)
	assert.Equal(t, "MAIN", files[0].Category)
	assert.Equal(t, "Main installation file", files[0].Description)

	// Verify optional file
	assert.Equal(t, "101", files[1].ID)
	assert.False(t, files[1].IsPrimary)
	assert.Equal(t, "OPTIONAL", files[1].Category)
}

func TestNexusMods_GetDownloadURL(t *testing.T) {
	mockResponse := []DownloadLink{
		{
			Name:      "Nexus CDN",
			ShortName: "Nexus",
			URI:       "https://cf-files.nexusmods.com/cdn/123/file.zip?key=abc&expires=123",
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/games/starrupture/mods/12345/files/100/download_link.json", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(mockResponse)
	}))
	defer server.Close()

	nm := New(nil, "testapikey")
	nm.client.baseURL = server.URL

	mod := &domain.Mod{
		ID:     "12345",
		GameID: "starrupture",
	}

	url, err := nm.GetDownloadURL(context.Background(), mod, "100")
	require.NoError(t, err)
	assert.Equal(t, "https://cf-files.nexusmods.com/cdn/123/file.zip?key=abc&expires=123", url)
}

func TestNexusMods_GetDownloadURL_NoLinks(t *testing.T) {
	mockResponse := []DownloadLink{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(mockResponse)
	}))
	defer server.Close()

	nm := New(nil, "testapikey")
	nm.client.baseURL = server.URL

	mod := &domain.Mod{
		ID:     "12345",
		GameID: "starrupture",
	}

	_, err := nm.GetDownloadURL(context.Background(), mod, "100")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no download links available")
}

func TestNexusMods_GetDownloadURL_InvalidModID(t *testing.T) {
	nm := New(nil, "testapikey")

	mod := &domain.Mod{
		ID:     "not-a-number",
		GameID: "starrupture",
	}

	_, err := nm.GetDownloadURL(context.Background(), mod, "100")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid mod ID")
}

func TestNexusMods_GetDownloadURL_InvalidFileID(t *testing.T) {
	nm := New(nil, "testapikey")

	mod := &domain.Mod{
		ID:     "12345",
		GameID: "starrupture",
	}

	_, err := nm.GetDownloadURL(context.Background(), mod, "not-a-number")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid file ID")
}
