package nexusmods

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Compile-time conformance pins: NexusMods implements the optional metadata
// interfaces the source-registry design (#76) expects of a built-in.
var (
	_ source.EnvKeyProvider           = (*NexusMods)(nil)
	_ source.KeyValidator             = (*NexusMods)(nil)
	_ source.AuthInstructionsProvider = (*NexusMods)(nil)
	_ source.TypeLabeler              = (*NexusMods)(nil)
	_ source.CapabilityReporter       = (*NexusMods)(nil)
)

func writeJSON(t *testing.T, w http.ResponseWriter, v interface{}) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("encoding response: %v", err)
	}
}

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
		writeJSON(t, w, mockResponse)
	}))
	defer server.Close()

	nm := New(nil, "testapikey")
	nm.client.SetBaseURL(server.URL)

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

func TestNexusMods_GetModFiles_SanitizesPathLikeFileName(t *testing.T) {
	mockResponse := ModFileList{
		Files: []FileData{
			{
				FileID:       100,
				Name:         "Main File",
				FileName:     "c3/f2/ac/test-mod-1-0.zip",
				Version:      "1.0.0",
				CategoryID:   1,
				CategoryName: "MAIN",
				IsPrimary:    true,
			},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/games/starrupture/mods/12345/files.json", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		writeJSON(t, w, mockResponse)
	}))
	defer server.Close()

	nm := New(nil, "testapikey")
	nm.client.SetBaseURL(server.URL)

	mod := &domain.Mod{
		ID:     "12345",
		GameID: "starrupture",
	}

	files, err := nm.GetModFiles(context.Background(), mod)
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, "test-mod-1-0.zip", files[0].FileName)
}

func TestNexusMods_GetModFiles_SanitizesInvalidFileNameToFallback(t *testing.T) {
	mockResponse := ModFileList{
		Files: []FileData{
			{
				FileID:       100,
				Name:         "Main File",
				FileName:     "////",
				Version:      "1.0.0",
				CategoryID:   1,
				CategoryName: "MAIN",
				IsPrimary:    true,
			},
			{
				FileID:       101,
				Name:         "Optional File",
				FileName:     "..",
				Version:      "1.0.0",
				CategoryID:   4,
				CategoryName: "OPTIONAL",
				IsPrimary:    false,
			},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/games/starrupture/mods/12345/files.json", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		writeJSON(t, w, mockResponse)
	}))
	defer server.Close()

	nm := New(nil, "testapikey")
	nm.client.SetBaseURL(server.URL)

	mod := &domain.Mod{
		ID:     "12345",
		GameID: "starrupture",
	}

	files, err := nm.GetModFiles(context.Background(), mod)
	require.NoError(t, err)
	require.Len(t, files, 2)
	assert.Equal(t, "download", files[0].FileName)
	assert.Equal(t, "download", files[1].FileName)
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
		writeJSON(t, w, mockResponse)
	}))
	defer server.Close()

	nm := New(nil, "testapikey")
	nm.client.SetBaseURL(server.URL)

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
		writeJSON(t, w, mockResponse)
	}))
	defer server.Close()

	nm := New(nil, "testapikey")
	nm.client.SetBaseURL(server.URL)

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

func TestNexusMods_CheckUpdates_FindsUpdate(t *testing.T) {
	// Mock server: GetMod returns newer version; GetModFiles returns changelog for update
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/games/skyrimspecialedition/mods/12345.json":
			writeJSON(t, w, ModData{
				ModID:   12345,
				Name:    "Test Mod",
				Version: "2.0.0", // Newer than installed 1.0.0
				Author:  "TestAuthor",
			})
		case "/v1/games/skyrimspecialedition/mods/12345/files.json":
			writeJSON(t, w, ModFileList{
				Files: []FileData{
					{FileID: 100, IsPrimary: true, Changelog: "Fixed bugs in 2.0.0"},
				},
			})
		}
	}))
	defer server.Close()

	nm := New(nil, "testapikey")
	nm.client.SetBaseURL(server.URL)

	installed := []domain.InstalledMod{
		{
			Mod: domain.Mod{
				ID:       "12345",
				SourceID: "nexusmods",
				Name:     "Test Mod",
				Version:  "1.0.0",
				GameID:   "skyrimspecialedition",
			},
			ProfileName:  "default",
			UpdatePolicy: domain.UpdateNotify,
			Enabled:      true,
		},
	}

	updates, err := nm.CheckUpdates(context.Background(), installed)
	require.NoError(t, err)
	require.Len(t, updates, 1)
	assert.Equal(t, "2.0.0", updates[0].NewVersion)
	assert.Equal(t, "12345", updates[0].InstalledMod.ID)
	assert.Equal(t, "Fixed bugs in 2.0.0", updates[0].Changelog)
}

func TestNexusMods_CheckUpdates_NoUpdateWhenSameVersion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/files.json") {
			writeJSON(t, w, ModFileList{Files: []FileData{{FileID: 100, IsPrimary: true}}})
			return
		}
		writeJSON(t, w, ModData{
			ModID:   12345,
			Name:    "Test Mod",
			Version: "1.0.0", // Same as installed
		})
	}))
	defer server.Close()

	nm := New(nil, "testapikey")
	nm.client.SetBaseURL(server.URL)

	installed := []domain.InstalledMod{
		{
			Mod: domain.Mod{
				ID:       "12345",
				SourceID: "nexusmods",
				Name:     "Test Mod",
				Version:  "1.0.0",
				GameID:   "skyrimspecialedition",
			},
		},
	}

	updates, err := nm.CheckUpdates(context.Background(), installed)
	require.NoError(t, err)
	assert.Empty(t, updates)
}

func TestNexusMods_CheckUpdates_FindsFileUpdate(t *testing.T) {
	// Mod version unchanged but an installed file was superseded (NexusMods FileUpdates)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/files.json") {
			writeJSON(t, w, ModFileList{
				Files: []FileData{
					{FileID: 100, IsPrimary: true, Version: "1.0.0"},
					{FileID: 101, IsPrimary: false, Version: "1.0.1", Changelog: "Hotfix for optional file"},
				},
				FileUpdates: []FileUpdate{
					{OldFileID: 100, NewFileID: 101},
				},
			})
			return
		}
		writeJSON(t, w, ModData{ModID: 12345, Name: "Test Mod", Version: "1.0.0"})
	}))
	defer server.Close()

	nm := New(nil, "testapikey")
	nm.client.SetBaseURL(server.URL)

	installed := []domain.InstalledMod{
		{
			Mod: domain.Mod{
				ID:      "12345",
				Name:    "Test Mod",
				Version: "1.0.0",
				GameID:  "skyrimspecialedition",
			},
			FileIDs: []string{"100"},
		},
	}

	updates, err := nm.CheckUpdates(context.Background(), installed)
	require.NoError(t, err)
	require.Len(t, updates, 1)
	assert.Equal(t, "1.0.1", updates[0].NewVersion, "should use new file version")
	assert.Equal(t, map[string]string{"100": "101"}, updates[0].FileIDReplacements)
}

func TestNexusMods_CheckUpdates_MultipleMods(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case "/v1/games/skyrimspecialedition/mods/111.json":
			writeJSON(t, w, ModData{ModID: 111, Version: "2.0.0"}) // Update available
		case "/v1/games/skyrimspecialedition/mods/111/files.json":
			writeJSON(t, w, ModFileList{Files: []FileData{{FileID: 1, IsPrimary: true}}})
		case "/v1/games/skyrimspecialedition/mods/222.json":
			writeJSON(t, w, ModData{ModID: 222, Version: "1.0.0"}) // No update
		case "/v1/games/skyrimspecialedition/mods/222/files.json":
			writeJSON(t, w, ModFileList{Files: []FileData{{FileID: 10, IsPrimary: true}}})
		case "/v1/games/skyrimspecialedition/mods/333.json":
			writeJSON(t, w, ModData{ModID: 333, Version: "3.5.0"}) // Update available
		case "/v1/games/skyrimspecialedition/mods/333/files.json":
			writeJSON(t, w, ModFileList{Files: []FileData{{FileID: 2, IsPrimary: true}}})
		}
	}))
	defer server.Close()

	nm := New(nil, "testapikey")
	nm.client.SetBaseURL(server.URL)

	installed := []domain.InstalledMod{
		{Mod: domain.Mod{ID: "111", Version: "1.0.0", GameID: "skyrimspecialedition"}},
		{Mod: domain.Mod{ID: "222", Version: "1.0.0", GameID: "skyrimspecialedition"}},
		{Mod: domain.Mod{ID: "333", Version: "3.0.0", GameID: "skyrimspecialedition"}},
	}

	updates, err := nm.CheckUpdates(context.Background(), installed)
	require.NoError(t, err)
	assert.Len(t, updates, 2, "should find 2 mods with updates")
	// 3 GetMod + 3 GetModFiles (one per mod)
	assert.Equal(t, 6, requestCount, "should make GetMod and GetModFiles per mod")
}

func TestNexusMods_GetDependencies(t *testing.T) {
	// Mock GraphQL response for modRequirements query
	mockResponse := map[string]interface{}{
		"data": map[string]interface{}{
			"modRequirements": map[string]interface{}{
				"nexusRequirements": map[string]interface{}{
					"nodes": []map[string]interface{}{
						{
							"modId":   999,
							"modName": "Required Mod A",
						},
						{
							"modId":   888,
							"modName": "Required Mod B",
						},
					},
				},
			},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		writeJSON(t, w, mockResponse)
	}))
	defer server.Close()

	nm := New(nil, "testapikey")
	nm.client.graphqlURL = server.URL

	mod := &domain.Mod{
		ID:     "12345",
		GameID: "skyrimspecialedition",
	}

	deps, err := nm.GetDependencies(context.Background(), mod)
	require.NoError(t, err)
	require.Len(t, deps, 2)
	assert.Equal(t, "999", deps[0].ModID)
	assert.Equal(t, "888", deps[1].ModID)
	assert.Equal(t, "nexusmods", deps[0].SourceID)
}

// TestNexusMods_Getters folds the trivial identity/auth-state getters into
// one small test, per the task brief.
func TestNexusMods_Getters(t *testing.T) {
	nm := New(nil, "")
	assert.Equal(t, "nexusmods", nm.ID())
	assert.Equal(t, "Nexus Mods", nm.Name())
	assert.Equal(t, "https://www.nexusmods.com/oauth/authorize", nm.AuthURL())
	assert.False(t, nm.IsAuthenticated())

	nm.SetAPIKey("new-key")
	assert.True(t, nm.IsAuthenticated())
}

// TestNexusMods_ExchangeToken asserts the documented OAuth-refusal error;
// NexusMods uses API-key auth instead, so this is a permanent stub.
func TestNexusMods_ExchangeToken(t *testing.T) {
	nm := New(nil, "")

	_, err := nm.ExchangeToken(context.Background(), "code")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "API key authentication")
}

func TestNexusMods_ValidateAPIKey_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/users/validate.json", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		writeJSON(t, w, map[string]interface{}{"user_id": 12345, "name": "TestUser"})
	}))
	defer server.Close()

	nm := New(nil, "")
	nm.client.SetBaseURL(server.URL)

	err := nm.ValidateAPIKey(context.Background(), "test-key")
	require.NoError(t, err)
}

func TestNexusMods_ValidateAPIKey_Failure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Invalid API Key"}`))
	}))
	defer server.Close()

	nm := New(nil, "")
	nm.client.SetBaseURL(server.URL)

	err := nm.ValidateAPIKey(context.Background(), "bad-key")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid API key")
}

func TestNexusMods_GetDependencies_NoDeps(t *testing.T) {
	// Mock GraphQL response with no dependencies
	mockResponse := map[string]interface{}{
		"data": map[string]interface{}{
			"modRequirements": map[string]interface{}{
				"nexusRequirements": map[string]interface{}{
					"nodes": []map[string]interface{}{},
				},
			},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		writeJSON(t, w, mockResponse)
	}))
	defer server.Close()

	nm := New(nil, "testapikey")
	nm.client.graphqlURL = server.URL

	mod := &domain.Mod{
		ID:     "12345",
		GameID: "skyrimspecialedition",
	}

	deps, err := nm.GetDependencies(context.Background(), mod)
	require.NoError(t, err)
	assert.Empty(t, deps)
}

func TestNexusMods_EnvKey(t *testing.T) {
	nm := New(nil, "")
	assert.Equal(t, "NEXUSMODS_API_KEY", nm.EnvKey())
}

func TestNexusMods_TypeLabel(t *testing.T) {
	nm := New(nil, "")
	assert.Equal(t, "built-in", nm.TypeLabel())
}

func TestNexusMods_Capabilities(t *testing.T) {
	nm := New(nil, "")
	assert.Equal(t, source.Capabilities{Search: true, Dependencies: true, Updates: true, Auth: true}, nm.Capabilities())
}

func TestNexusMods_AuthInstructions(t *testing.T) {
	nm := New(nil, "")
	want := "To authenticate with NexusMods:\n" +
		"1. Visit https://www.nexusmods.com/users/myaccount?tab=api\n" +
		"2. Click \"Request an API Key\" if you don't have one\n" +
		"3. Copy your Personal API Key\n"
	assert.Equal(t, want, nm.AuthInstructions())
}

func TestNexusMods_ValidateKey_OK(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/users/validate.json", r.URL.Path)
		assert.Equal(t, "good-key", r.Header.Get("apikey"))
		w.Header().Set("Content-Type", "application/json")
		writeJSON(t, w, map[string]any{"user_id": 1})
	}))
	defer server.Close()

	nm := New(server.Client(), "")
	nm.client.SetBaseURL(server.URL)

	err := nm.ValidateKey(context.Background(), "good-key")
	assert.NoError(t, err)
}

func TestNexusMods_ValidateKey_Unauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	nm := New(server.Client(), "")
	nm.client.SetBaseURL(server.URL)

	err := nm.ValidateKey(context.Background(), "bad-key")
	assert.Error(t, err)
}

// TestNexusMods_ValidateKey_WrongKeyAgainstAuthenticatedReceiver pins that
// ValidateKey checks the candidate key, never falling back to a key already
// stored on the receiver. A receiver constructed with a valid stored key
// ("good-key") must still fail when asked to validate a different candidate.
func TestNexusMods_ValidateKey_WrongKeyAgainstAuthenticatedReceiver(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("apikey") != "good-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		writeJSON(t, w, map[string]any{"user_id": 1})
	}))
	defer server.Close()

	nm := New(server.Client(), "good-key")
	nm.client.SetBaseURL(server.URL)

	err := nm.ValidateKey(context.Background(), "bad-key")
	assert.Error(t, err, "ValidateKey must use the candidate key, not the receiver's stored key")

	// Bonus: prove the server logic actually discriminates on the header.
	err = nm.ValidateKey(context.Background(), "good-key")
	assert.NoError(t, err)
}
