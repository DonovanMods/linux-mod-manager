package core_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Mock source for testing
type mockSource struct {
	id   string
	mods map[string]*domain.Mod
}

func newMockSource(id string) *mockSource {
	return &mockSource{
		id:   id,
		mods: make(map[string]*domain.Mod),
	}
}

func (m *mockSource) ID() string      { return m.id }
func (m *mockSource) Name() string    { return "Mock Source" }
func (m *mockSource) AuthURL() string { return "" }
func (m *mockSource) ExchangeToken(ctx context.Context, code string) (*source.Token, error) {
	return nil, nil
}
func (m *mockSource) Search(ctx context.Context, query source.SearchQuery) ([]domain.Mod, error) {
	var results []domain.Mod
	for _, mod := range m.mods {
		if mod.GameID == query.GameID {
			results = append(results, *mod)
		}
	}
	return results, nil
}
func (m *mockSource) GetMod(ctx context.Context, gameID, modID string) (*domain.Mod, error) {
	key := gameID + "/" + modID
	if mod, ok := m.mods[key]; ok {
		return mod, nil
	}
	return nil, domain.ErrModNotFound
}
func (m *mockSource) GetDependencies(ctx context.Context, mod *domain.Mod) ([]domain.ModReference, error) {
	return mod.Dependencies, nil
}
func (m *mockSource) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	return []domain.DownloadableFile{
		{
			ID:        "1",
			Name:      "Main File",
			FileName:  mod.ID + ".zip",
			IsPrimary: true,
		},
	}, nil
}
func (m *mockSource) GetDownloadURL(ctx context.Context, mod *domain.Mod, fileID string) (string, error) {
	return "http://example.com/download/" + mod.ID, nil
}
func (m *mockSource) CheckUpdates(ctx context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	return nil, nil
}

func (m *mockSource) AddMod(gameID string, mod *domain.Mod) {
	key := gameID + "/" + mod.ID
	m.mods[key] = mod
}

func TestNewService(t *testing.T) {
	cfg := core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
	}

	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	assert.NotNil(t, svc)
	defer svc.Close()
}

func TestService_RegisterSource(t *testing.T) {
	cfg := core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
	}

	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	defer svc.Close()

	mock := newMockSource("test")
	svc.RegisterSource(mock)

	src, err := svc.GetSource("test")
	require.NoError(t, err)
	assert.Equal(t, "test", src.ID())
}

func TestService_SearchMods(t *testing.T) {
	cfg := core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
	}

	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	defer svc.Close()

	mock := newMockSource("test")
	mock.AddMod("skyrim", &domain.Mod{
		ID:       "123",
		SourceID: "test",
		Name:     "Test Mod",
		Version:  "1.0.0",
		GameID:   "skyrim",
	})
	svc.RegisterSource(mock)

	results, err := svc.SearchMods(context.Background(), "test", "skyrim", "test")
	require.NoError(t, err)
	assert.Len(t, results, 1)
	assert.Equal(t, "Test Mod", results[0].Name)
}

func TestService_GetMod(t *testing.T) {
	cfg := core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
	}

	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	defer svc.Close()

	mock := newMockSource("test")
	mock.AddMod("skyrim", &domain.Mod{
		ID:       "123",
		SourceID: "test",
		Name:     "Test Mod",
		Version:  "1.0.0",
		GameID:   "skyrim",
	})
	svc.RegisterSource(mock)

	mod, err := svc.GetMod(context.Background(), "test", "skyrim", "123")
	require.NoError(t, err)
	assert.Equal(t, "Test Mod", mod.Name)
}

func TestService_SaveSourceToken(t *testing.T) {
	cfg := core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
	}

	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	defer svc.Close()

	// Save a token
	err = svc.SaveSourceToken("nexusmods", "test-api-key")
	require.NoError(t, err)

	// Verify it's saved
	assert.True(t, svc.IsSourceAuthenticated("nexusmods"))
}

func TestService_DeleteSourceToken(t *testing.T) {
	cfg := core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
	}

	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	defer svc.Close()

	// Save a token
	err = svc.SaveSourceToken("nexusmods", "test-api-key")
	require.NoError(t, err)
	assert.True(t, svc.IsSourceAuthenticated("nexusmods"))

	// Delete it
	err = svc.DeleteSourceToken("nexusmods")
	require.NoError(t, err)
	assert.False(t, svc.IsSourceAuthenticated("nexusmods"))
}

func TestService_IsSourceAuthenticated(t *testing.T) {
	cfg := core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
	}

	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	defer svc.Close()

	// Not authenticated initially
	assert.False(t, svc.IsSourceAuthenticated("nexusmods"))

	// Save a token
	err = svc.SaveSourceToken("nexusmods", "test-api-key")
	require.NoError(t, err)

	// Now authenticated
	assert.True(t, svc.IsSourceAuthenticated("nexusmods"))
}

func TestService_GetSourceToken(t *testing.T) {
	cfg := core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
	}

	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	defer svc.Close()

	// No token initially
	token, err := svc.GetSourceToken("nexusmods")
	require.NoError(t, err)
	assert.Nil(t, token)

	// Save a token
	err = svc.SaveSourceToken("nexusmods", "test-api-key")
	require.NoError(t, err)

	// Get the token
	token, err = svc.GetSourceToken("nexusmods")
	require.NoError(t, err)
	require.NotNil(t, token)
	assert.Equal(t, "test-api-key", token.APIKey)
}

func TestService_GetModFiles(t *testing.T) {
	cfg := core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
	}

	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	defer svc.Close()

	mock := newMockSource("test")
	mock.AddMod("skyrim", &domain.Mod{
		ID:       "123",
		SourceID: "test",
		Name:     "Test Mod",
		Version:  "1.0.0",
		GameID:   "skyrim",
	})
	svc.RegisterSource(mock)

	mod := &domain.Mod{
		ID:       "123",
		SourceID: "test",
		GameID:   "skyrim",
	}

	files, err := svc.GetModFiles(context.Background(), "test", mod)
	require.NoError(t, err)
	assert.Len(t, files, 1)
	assert.Equal(t, "Main File", files[0].Name)
	assert.True(t, files[0].IsPrimary)
}

func TestService_GetModFiles_SourceNotFound(t *testing.T) {
	cfg := core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
	}

	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	defer svc.Close()

	mod := &domain.Mod{
		ID:       "123",
		SourceID: "nonexistent",
		GameID:   "skyrim",
	}

	_, err = svc.GetModFiles(context.Background(), "nonexistent", mod)
	require.Error(t, err)
}

func TestService_UpdateModVersion(t *testing.T) {
	cfg := core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
	}

	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	defer svc.Close()

	// Create an installed mod
	installedMod := &domain.InstalledMod{
		Mod: domain.Mod{
			ID:       "123",
			SourceID: "test",
			Name:     "Test Mod",
			Version:  "1.0.0",
			GameID:   "skyrim-se",
		},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
	}
	err = svc.DB().SaveInstalledMod(installedMod)
	require.NoError(t, err)

	// Update the version
	err = svc.UpdateModVersion("test", "123", "skyrim-se", "default", "2.0.0")
	require.NoError(t, err)

	// Verify the update
	updated, err := svc.DB().GetInstalledMod("test", "123", "skyrim-se", "default")
	require.NoError(t, err)
	assert.Equal(t, "2.0.0", updated.Version)
	assert.Equal(t, "1.0.0", updated.PreviousVersion)
}

func TestService_RollbackModVersion(t *testing.T) {
	cfg := core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
	}

	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	defer svc.Close()

	// Create an installed mod with previous version
	installedMod := &domain.InstalledMod{
		Mod: domain.Mod{
			ID:       "123",
			SourceID: "test",
			Name:     "Test Mod",
			Version:  "2.0.0",
			GameID:   "skyrim-se",
		},
		ProfileName:     "default",
		UpdatePolicy:    domain.UpdateNotify,
		Enabled:         true,
		PreviousVersion: "1.0.0",
	}
	err = svc.DB().SaveInstalledMod(installedMod)
	require.NoError(t, err)

	// Rollback the version
	err = svc.RollbackModVersion("test", "123", "skyrim-se", "default")
	require.NoError(t, err)

	// Verify the rollback
	rolledBack, err := svc.DB().GetInstalledMod("test", "123", "skyrim-se", "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0.0", rolledBack.Version)
	assert.Equal(t, "2.0.0", rolledBack.PreviousVersion)
}

func TestService_RollbackModVersion_NoPreviousVersion(t *testing.T) {
	cfg := core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
	}

	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	defer svc.Close()

	// Create an installed mod without previous version
	installedMod := &domain.InstalledMod{
		Mod: domain.Mod{
			ID:       "123",
			SourceID: "test",
			Name:     "Test Mod",
			Version:  "1.0.0",
			GameID:   "skyrim-se",
		},
		ProfileName: "default",
	}
	err = svc.DB().SaveInstalledMod(installedMod)
	require.NoError(t, err)

	// Rollback should fail
	err = svc.RollbackModVersion("test", "123", "skyrim-se", "default")
	require.Error(t, err)
}

func TestService_SetModUpdatePolicy(t *testing.T) {
	cfg := core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
	}

	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	defer svc.Close()

	// Create an installed mod
	installedMod := &domain.InstalledMod{
		Mod: domain.Mod{
			ID:       "123",
			SourceID: "test",
			Name:     "Test Mod",
			Version:  "1.0.0",
			GameID:   "skyrim-se",
		},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
	}
	err = svc.DB().SaveInstalledMod(installedMod)
	require.NoError(t, err)

	// Change policy to auto
	err = svc.SetModUpdatePolicy("test", "123", "skyrim-se", "default", domain.UpdateAuto)
	require.NoError(t, err)

	// Verify
	updated, err := svc.DB().GetInstalledMod("test", "123", "skyrim-se", "default")
	require.NoError(t, err)
	assert.Equal(t, domain.UpdateAuto, updated.UpdatePolicy)

	// Change policy to pinned
	err = svc.SetModUpdatePolicy("test", "123", "skyrim-se", "default", domain.UpdatePinned)
	require.NoError(t, err)

	updated, err = svc.DB().GetInstalledMod("test", "123", "skyrim-se", "default")
	require.NoError(t, err)
	assert.Equal(t, domain.UpdatePinned, updated.UpdatePolicy)
}

func TestService_DownloadMod_MultipleFiles(t *testing.T) {
	// This test verifies that downloading multiple files for the same mod
	// correctly adds all files to the cache (not just the first one).

	cfg := core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
	}

	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	defer svc.Close()

	// Create a mock source that can provide download URLs
	mock := newMockSourceWithDownloads("test")
	defer mock.Close()
	svc.RegisterSource(mock)

	// Create a game config
	gameDir := t.TempDir()
	game := &domain.Game{
		ID:      "testgame",
		Name:    "Test Game",
		ModPath: gameDir,
	}
	err = svc.AddGame(game)
	require.NoError(t, err)

	// Create a mod
	mod := &domain.Mod{
		ID:       "123",
		SourceID: "test",
		Name:     "Multi-File Mod",
		Version:  "1.0.0",
		GameID:   "testgame",
	}

	// Define two files for the same mod
	file1 := &domain.DownloadableFile{
		ID:       "file1",
		Name:     "File One",
		FileName: "file1.zip",
	}
	file2 := &domain.DownloadableFile{
		ID:       "file2",
		Name:     "File Two",
		FileName: "file2.zip",
	}

	// Register the files with our mock - use temp dirs to create zip files
	tmpDir := t.TempDir()
	zip1Path := createTestZip(t, tmpDir, map[string]string{"file1_content.txt": "content from file 1"})
	zip1Content, err := os.ReadFile(zip1Path)
	require.NoError(t, err)

	tmpDir2 := t.TempDir()
	zip2Path := createTestZip(t, tmpDir2, map[string]string{"file2_content.txt": "content from file 2"})
	zip2Content, err := os.ReadFile(zip2Path)
	require.NoError(t, err)

	mock.AddDownload(file1.ID, zip1Content)
	mock.AddDownload(file2.ID, zip2Content)

	ctx := context.Background()

	// Download first file
	count1, err := svc.DownloadMod(ctx, "test", game, mod, file1, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, count1, "First download should extract 1 file")

	// Download second file - previously bugged: returned early because cache dir existed
	count2, err := svc.DownloadMod(ctx, "test", game, mod, file2, nil)
	require.NoError(t, err)
	// Returns total files in cache after extraction (1 from first + 1 from second = 2)
	assert.Equal(t, 2, count2, "After second download, cache should have 2 files total")

	// Verify both files are in the cache
	gameCache := svc.GetGameCache(game)
	files, err := gameCache.ListFiles(game.ID, mod.SourceID, mod.ID, mod.Version)
	require.NoError(t, err)

	// Should have 2 files total
	assert.Len(t, files, 2, "Cache should contain files from both downloads")

	// Verify both expected files are present
	fileNames := make(map[string]bool)
	for _, f := range files {
		fileNames[f] = true
	}
	assert.True(t, fileNames["file1_content.txt"], "Cache should contain file1_content.txt")
	assert.True(t, fileNames["file2_content.txt"], "Cache should contain file2_content.txt")
}

// mockSourceWithDownloads extends mockSource with download URL support
type mockSourceWithDownloads struct {
	*mockSource
	downloads map[string][]byte // fileID -> zip content
	server    *httptest.Server
}

func newMockSourceWithDownloads(id string) *mockSourceWithDownloads {
	m := &mockSourceWithDownloads{
		mockSource: newMockSource(id),
		downloads:  make(map[string][]byte),
	}
	// Create test server that serves our downloads
	m.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fileID := filepath.Base(r.URL.Path)
		if content, ok := m.downloads[fileID]; ok {
			w.Header().Set("Content-Type", "application/zip")
			w.Write(content)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	return m
}

func (m *mockSourceWithDownloads) AddDownload(fileID string, content []byte) {
	m.downloads[fileID] = content
}

func (m *mockSourceWithDownloads) GetDownloadURL(ctx context.Context, mod *domain.Mod, fileID string) (string, error) {
	return m.server.URL + "/" + fileID, nil
}

func (m *mockSourceWithDownloads) Close() {
	m.server.Close()
}
