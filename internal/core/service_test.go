package core_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"

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
func (m *mockSource) Search(ctx context.Context, query source.SearchQuery) (source.SearchResult, error) {
	var results []domain.Mod
	for _, mod := range m.mods {
		if mod.GameID == query.GameID {
			results = append(results, *mod)
		}
	}
	return source.SearchResult{Mods: results}, nil
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
	t.Cleanup(func() {
		require.NoError(t, svc.Close())
	})
}

func TestService_RegisterSource(t *testing.T) {
	cfg := core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
	}

	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, svc.Close())
	})

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
	t.Cleanup(func() {
		require.NoError(t, svc.Close())
	})

	mock := newMockSource("test")
	mock.AddMod("skyrim", &domain.Mod{
		ID:       "123",
		SourceID: "test",
		Name:     "Test Mod",
		Version:  "1.0.0",
		GameID:   "skyrim",
	})
	svc.RegisterSource(mock)

	searchResult, err := svc.SearchMods(context.Background(), "test", "skyrim", "test", "", nil, 0, 0)
	require.NoError(t, err)
	assert.Len(t, searchResult.Mods, 1)
	assert.Equal(t, "Test Mod", searchResult.Mods[0].Name)
}

func TestService_GetMod(t *testing.T) {
	cfg := core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
	}

	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, svc.Close())
	})

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
	t.Cleanup(func() {
		require.NoError(t, svc.Close())
	})

	// Save a token
	err = svc.SaveSourceToken(context.Background(), "nexusmods", "test-api-key")
	require.NoError(t, err)

	// Verify it's saved
	assert.True(t, svc.IsSourceAuthenticated(context.Background(), "nexusmods"))
}

func TestService_DeleteSourceToken(t *testing.T) {
	cfg := core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
	}

	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, svc.Close())
	})

	// Save a token
	err = svc.SaveSourceToken(context.Background(), "nexusmods", "test-api-key")
	require.NoError(t, err)
	assert.True(t, svc.IsSourceAuthenticated(context.Background(), "nexusmods"))

	// Delete it
	err = svc.DeleteSourceToken(context.Background(), "nexusmods")
	require.NoError(t, err)
	assert.False(t, svc.IsSourceAuthenticated(context.Background(), "nexusmods"))
}

func TestService_IsSourceAuthenticated(t *testing.T) {
	cfg := core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
	}

	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, svc.Close())
	})

	// Not authenticated initially
	assert.False(t, svc.IsSourceAuthenticated(context.Background(), "nexusmods"))

	// Save a token
	err = svc.SaveSourceToken(context.Background(), "nexusmods", "test-api-key")
	require.NoError(t, err)

	// Now authenticated
	assert.True(t, svc.IsSourceAuthenticated(context.Background(), "nexusmods"))
}

func TestService_GetSourceToken(t *testing.T) {
	cfg := core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
	}

	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, svc.Close())
	})

	// No token initially
	token, err := svc.GetSourceToken(context.Background(), "nexusmods")
	require.NoError(t, err)
	assert.Nil(t, token)

	// Save a token
	err = svc.SaveSourceToken(context.Background(), "nexusmods", "test-api-key")
	require.NoError(t, err)

	// Get the token
	token, err = svc.GetSourceToken(context.Background(), "nexusmods")
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
	t.Cleanup(func() {
		require.NoError(t, svc.Close())
	})

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
	t.Cleanup(func() {
		require.NoError(t, svc.Close())
	})

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
	t.Cleanup(func() {
		require.NoError(t, svc.Close())
	})

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
	err = svc.SaveInstalledMod(context.Background(), installedMod)
	require.NoError(t, err)

	// Update the version
	err = svc.UpdateModVersion(context.Background(), "test", "123", "skyrim-se", "default", "2.0.0")
	require.NoError(t, err)

	// Verify the update
	updated, err := svc.GetInstalledMod(context.Background(), "test", "123", "skyrim-se", "default")
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
	t.Cleanup(func() {
		require.NoError(t, svc.Close())
	})

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
	err = svc.SaveInstalledMod(context.Background(), installedMod)
	require.NoError(t, err)

	// Rollback the version
	err = svc.RollbackModVersion(context.Background(), "test", "123", "skyrim-se", "default")
	require.NoError(t, err)

	// Verify the rollback
	rolledBack, err := svc.GetInstalledMod(context.Background(), "test", "123", "skyrim-se", "default")
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
	t.Cleanup(func() {
		require.NoError(t, svc.Close())
	})

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
	err = svc.SaveInstalledMod(context.Background(), installedMod)
	require.NoError(t, err)

	// Rollback should fail
	err = svc.RollbackModVersion(context.Background(), "test", "123", "skyrim-se", "default")
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
	t.Cleanup(func() {
		require.NoError(t, svc.Close())
	})

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
	err = svc.SaveInstalledMod(context.Background(), installedMod)
	require.NoError(t, err)

	// Change policy to auto
	err = svc.SetModUpdatePolicy(context.Background(), "test", "123", "skyrim-se", "default", domain.UpdateAuto)
	require.NoError(t, err)

	// Verify
	updated, err := svc.GetInstalledMod(context.Background(), "test", "123", "skyrim-se", "default")
	require.NoError(t, err)
	assert.Equal(t, domain.UpdateAuto, updated.UpdatePolicy)

	// Change policy to pinned
	err = svc.SetModUpdatePolicy(context.Background(), "test", "123", "skyrim-se", "default", domain.UpdatePinned)
	require.NoError(t, err)

	updated, err = svc.GetInstalledMod(context.Background(), "test", "123", "skyrim-se", "default")
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
	t.Cleanup(func() {
		require.NoError(t, svc.Close())
	})

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
	err = svc.SaveGame(context.Background(), game)
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
	result1, err := svc.DownloadMod(ctx, "test", game, mod, file1, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, result1.FilesExtracted, "First download should extract 1 file")
	assert.NotEmpty(t, result1.Checksum, "First download should have checksum")

	// Download second file - previously bugged: returned early because cache dir existed
	result2, err := svc.DownloadMod(ctx, "test", game, mod, file2, nil)
	require.NoError(t, err)
	// Returns total files in cache after extraction (1 from first + 1 from second = 2)
	assert.Equal(t, 2, result2.FilesExtracted, "After second download, cache should have 2 files total")
	assert.NotEmpty(t, result2.Checksum, "Second download should have checksum")

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

	// #96: each commit stamps its own completion marker, and prepareStaging's
	// reseed carries the earlier file's marker through the later file's
	// commit - so after both downloads the entry reads as complete for BOTH.
	assert.True(t, gameCache.HasFileIDs(game.ID, mod.SourceID, mod.ID, mod.Version, []string{file1.ID, file2.ID}),
		"both files' markers must survive a multi-file download")
}

// TestService_DownloadMod_RecordsMemberManifests covers #144 item 4's capture
// side: commitStagedCacheWithMarker is the single choke point where staged
// content becomes cache content, and it must record WHICH members each file ID
// contributed (cache.FileManifests) so the same-version update path can later
// undeploy a superseded file's members with positive provenance.
//
// Three shapes, all through the real DownloadMod flow:
//   - DeployExtract (the default): the manifest holds the archive's EXTRACTED
//     member names, which bear no relation to DownloadableFile.FileName.
//   - Overwrite attribution: a member shipped by BOTH files (file2's archive
//     overwrites file1's shared.txt in the shared staging dir) must appear in
//     BOTH manifests - a staging-dir before/after diff would miss it, and the
//     "shared member survives" rule depends on it.
//   - Copy mode fallback (non-archive file): the manifest is the single
//     stored FileName.
func TestService_DownloadMod_RecordsMemberManifests(t *testing.T) {
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	mock := newMockSourceWithDownloads("test")
	defer mock.Close()
	svc.RegisterSource(mock)

	game := &domain.Game{ID: "testgame", Name: "Test Game", ModPath: t.TempDir()}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	mod := &domain.Mod{ID: "123", SourceID: "test", Name: "Mod", Version: "1.0.0", GameID: "testgame"}

	zip1, err := os.ReadFile(createTestZip(t, t.TempDir(), map[string]string{"shared.txt": "from file1", "one.txt": "1"}))
	require.NoError(t, err)
	zip2, err := os.ReadFile(createTestZip(t, t.TempDir(), map[string]string{"shared.txt": "from file2", "two.txt": "2"}))
	require.NoError(t, err)
	mock.AddDownload("file1", zip1)
	mock.AddDownload("file2", zip2)
	mock.AddDownload("file3", []byte("loose plugin bytes"))

	ctx := context.Background()
	_, err = svc.DownloadMod(ctx, "test", game, mod, &domain.DownloadableFile{ID: "file1", FileName: "file1.zip"}, nil)
	require.NoError(t, err)
	_, err = svc.DownloadMod(ctx, "test", game, mod, &domain.DownloadableFile{ID: "file2", FileName: "file2.zip"}, nil)
	require.NoError(t, err)
	_, err = svc.DownloadMod(ctx, "test", game, mod, &domain.DownloadableFile{ID: "file3", FileName: "loose.esp"}, nil)
	require.NoError(t, err)

	manifests, err := svc.GetGameCache(game).FileManifests("testgame", "test", "123", "1.0.0")
	require.NoError(t, err)
	require.Len(t, manifests, 3)

	m1 := manifests["file1"]
	require.True(t, m1.Recorded, "an extract-mode commit must record its manifest")
	assert.ElementsMatch(t, []string{"shared.txt", "one.txt"}, m1.Members,
		"the manifest holds extracted member names, not the archive's FileName")

	m2 := manifests["file2"]
	require.True(t, m2.Recorded)
	assert.ElementsMatch(t, []string{"shared.txt", "two.txt"}, m2.Members,
		"a member OVERWRITTEN in the shared staging dir must still be attributed to the overwriting file")

	m3 := manifests["file3"]
	require.True(t, m3.Recorded, "a copy-mode (non-archive) commit must record its manifest")
	assert.Equal(t, []string{"loose.esp"}, m3.Members)

	// The overwrite landed: file2 won the shared member's content.
	content, err := os.ReadFile(svc.GetGameCache(game).GetFilePath("testgame", "test", "123", "1.0.0", "shared.txt"))
	require.NoError(t, err)
	assert.Equal(t, "from file2", string(content))
}

// TestService_DownloadMod_PrunesUnclaimedStaleFiles is the #210 wiring test:
// commitStagedCacheWithMarker's single choke point calls cache.PruneUnclaimed
// right after stamping its own marker, so a cache entry carrying stale
// unclaimed debris (e.g. a pre-#197 compiled pak carried forward by
// prepareStaging's reseed, sitting beside a #197 retained-source marker) gets
// cleaned up the moment every marker in the entry becomes recorded - while
// the retained source itself, being reserved bookkeeping, survives.
func TestService_DownloadMod_PrunesUnclaimedStaleFiles(t *testing.T) {
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	mock := newMockSourceWithDownloads("test")
	defer mock.Close()
	svc.RegisterSource(mock)

	game := &domain.Game{ID: "testgame", Name: "Test Game", ModPath: t.TempDir()}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	mod := &domain.Mod{ID: "123", SourceID: "test", Name: "Mod", Version: "1.0.0", GameID: "testgame"}

	gameCache := svc.GetGameCache(game)
	dir := gameCache.ModPath(game.ID, mod.SourceID, mod.ID, mod.Version)

	// Pre-seed the entry with debris a manifest-unaware world would have left
	// behind: a stale pak no current marker claims, and a #197 retained
	// source whose own marker IS recorded (claiming nothing).
	require.NoError(t, gameCache.Store(game.ID, mod.SourceID, mod.ID, mod.Version, "stale.pak", []byte("debris")))
	require.NoError(t, os.WriteFile(filepath.Join(dir, cache.RetainedSourceName("legacy")), []byte("zip"), 0o644))
	require.NoError(t, cache.MarkFileCompleteWithMembers(dir, "legacy", nil))

	zip1, err := os.ReadFile(createTestZip(t, t.TempDir(), map[string]string{"new.txt": "fresh content"}))
	require.NoError(t, err)
	mock.AddDownload("file1", zip1)

	ctx := context.Background()
	_, err = svc.DownloadMod(ctx, "test", game, mod, &domain.DownloadableFile{ID: "file1", FileName: "file1.zip"}, nil)
	require.NoError(t, err)

	files, err := gameCache.ListFiles(game.ID, mod.SourceID, mod.ID, mod.Version)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"new.txt"}, files, "the unclaimed stale pak must be pruned once every marker is recorded")

	_, err = os.Stat(filepath.Join(dir, cache.RetainedSourceName("legacy")))
	require.NoError(t, err, "the retained source is reserved bookkeeping and must survive the prune")
}

// TestService_DownloadMod_OrganicPrune_PreConvergencePakClaimedThenExmodzRetain
// is the organic (two-real-generation) companion to
// TestService_DownloadMod_PrunesUnclaimedStaleFiles above: instead of
// hand-seeding unclaimed debris, generation 1 is a genuine
// Service.DownloadMod commit that CLAIMS a compiled pak - the honest
// pre-#197 shape a real user's cache would have carried. Its file (a
// DeployCompile mod's file ID whose FileName is NOT .exmodz) misses the
// exmodz branch entirely and falls through to the plain "copy to cache"
// path (service.go's DownloadModToCache, ~line 605), which commits with
// members=[]string{fileName} - a real, recorded claim on the pak.
//
// Generation 2 re-downloads the SAME file ID, now served as a .exmodz -
// #197's validate+retain shape. commitStagedCacheWithMarker re-marks that
// SAME file ID's manifest (members=nil this time), overwriting its earlier
// claim, and prepareStaging has seeded the new commit from the existing
// entry, so the pak from generation 1 is still on disk but is no longer
// claimed by any manifest. PruneUnclaimed - run at this same commit, now
// that every marker is Recorded and a retained source exists - removes it.
//
// This is the real-world scenario #210/#212 exist for: a mod installed
// before #197 gets updated/re-downloaded after upgrading lmm, and the stale
// per-mod pak its old commit produced must not linger forever.
func TestService_DownloadMod_OrganicPrune_PreConvergencePakClaimedThenExmodzRetain(t *testing.T) {
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	mock := &compilerMockSource{mockSourceWithDownloads: newMockSourceWithDownloads("test-compiler")}
	defer mock.Close()
	svc.RegisterSource(mock)

	game := &domain.Game{ID: "testgame", Name: "Test Game", ModPath: t.TempDir(), DeployMode: domain.DeployCompile}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	mod := &domain.Mod{ID: "123", SourceID: "test-compiler", Name: "Mod", Version: "1.0.0", GameID: "testgame"}

	gameCache := svc.GetGameCache(game)
	dir := gameCache.ModPath(game.ID, mod.SourceID, mod.ID, mod.Version)

	// Generation 1: the honest pre-#197 shape - a real DownloadModToCache
	// commit that claims a compiled pak for this file ID.
	mock.AddDownload("file1", []byte("compiled pak bytes"))
	_, err = svc.DownloadMod(context.Background(), "test-compiler", game, mod, &domain.DownloadableFile{ID: "file1", FileName: "Mod_P.pak"}, nil)
	require.NoError(t, err)

	files, err := gameCache.ListFiles(game.ID, mod.SourceID, mod.ID, mod.Version)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"Mod_P.pak"}, files, "generation 1 must land the compiled pak as a claimed member")

	manifests, err := gameCache.FileManifests(game.ID, mod.SourceID, mod.ID, mod.Version)
	require.NoError(t, err)
	require.True(t, manifests["file1"].Recorded)
	require.Equal(t, []string{"Mod_P.pak"}, manifests["file1"].Members, "generation 1's marker must genuinely claim the pak")

	// Generation 2: the SAME source and file ID now serve the
	// validated/retained .exmodz - #197's shape.
	mock.AddDownload("file1", []byte("fake-exmodz-bytes"))
	_, err = svc.DownloadMod(context.Background(), "test-compiler", game, mod, &domain.DownloadableFile{ID: "file1", FileName: "Mod.exmodz"}, nil)
	require.NoError(t, err)

	files, err = gameCache.ListFiles(game.ID, mod.SourceID, mod.ID, mod.Version)
	require.NoError(t, err)
	assert.Empty(t, files, "the stale pak must be pruned once file1's marker is re-recorded claiming nothing")

	_, err = os.Stat(filepath.Join(dir, cache.RetainedSourceName("file1")))
	require.NoError(t, err, "the retained .exmodz must survive - reserved bookkeeping, not prune-eligible")

	_, err = os.Stat(filepath.Join(dir, "Mod_P.pak"))
	require.True(t, os.IsNotExist(err), "the unclaimed compiled pak must actually be gone from disk")
}

// TestService_DownloadMod_SiblingReingestKeepsConvertedPakCopy reproduces
// #250: a converted pak's manifest is deliberately members=nil (the merged
// pak claims its content - reconcilePakManifests' participating branch), so
// its deployable cache copy is unclaimed by every recorded manifest. A later
// ingest of a SIBLING file ID into the same version directory opens
// PruneUnclaimed's gate (staging is seeded from the existing entry, every
// marker reads Recorded, retained sources are present) - and pre-fix, the
// converted pak's copy was deleted as if it were stale pre-#197 content.
// It is not stale: it is the designated raw-fallback artifact, and pruning
// it left a later opt-out or failed merge deploying nothing, silently.
func TestService_DownloadMod_SiblingReingestKeepsConvertedPakCopy(t *testing.T) {
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	mock := &compilerMockSource{mockSourceWithDownloads: newMockSourceWithDownloads("test-compiler")}
	defer mock.Close()
	svc.RegisterSource(mock)

	game := &domain.Game{ID: "testgame", Name: "Test Game", ModPath: t.TempDir(), DeployMode: domain.DeployCompile, ConvertPaks: true}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	mod := &domain.Mod{ID: "123", SourceID: "test-compiler", Name: "Mod", Version: "1.0.0", GameID: "testgame"}

	gameCache := svc.GetGameCache(game)
	dir := gameCache.ModPath(game.ID, mod.SourceID, mod.ID, mod.Version)

	// Generation 1: the pak variant ingests via the validate+retain branch -
	// a retained source plus a claimed deployable copy (#221's raw-deploy
	// default state).
	mock.AddDownload("pak", []byte("prebuilt-pak-bytes"))
	_, err = svc.DownloadMod(context.Background(), "test-compiler", game, mod, &domain.DownloadableFile{ID: "pak", FileName: "Mod_P.pak"}, nil)
	require.NoError(t, err)

	manifests, err := gameCache.FileManifests(game.ID, mod.SourceID, mod.ID, mod.Version)
	require.NoError(t, err)
	require.Equal(t, []string{"Mod_P.pak"}, manifests["pak"].Members, "precondition: ingest claims the deployable pak copy")

	// The first successful merge flips the pak's manifest to members=nil -
	// the merged pak claims its content (reconcilePakManifests'
	// participating branch). The copy is now unclaimed but NOT stale.
	require.NoError(t, cache.MarkFileCompleteWithMembers(dir, "pak", nil))

	// Generation 2: a sibling file ID of the same mod+version is ingested
	// afterward (the issue's repro: an optional patch file, a verify --fix
	// re-download, ...). This commit runs PruneUnclaimed with its gate open.
	mock.AddDownload("exmodz", []byte("fake-exmodz-bytes"))
	_, err = svc.DownloadMod(context.Background(), "test-compiler", game, mod, &domain.DownloadableFile{ID: "exmodz", FileName: "Mod.exmodz"}, nil)
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(dir, "Mod_P.pak"))
	require.NoError(t, err, "the converted pak's deployable copy is the designated raw-fallback artifact (#250) and must survive a sibling ingest's prune")
	assert.Equal(t, "prebuilt-pak-bytes", string(data))

	_, err = os.Stat(filepath.Join(dir, cache.RetainedSourceName("pak")))
	require.NoError(t, err, "the pak's retained source must survive as before")
	_, err = os.Stat(filepath.Join(dir, cache.RetainedSourceName("exmodz")))
	require.NoError(t, err, "the sibling's retained source must survive as before")
}

// TestService_DownloadMod_ForgedCacheMarkerInArchiveIsRejected is the
// integration-level guard for the #96 round 2 review finding, reproducing its
// probe exactly: an archive downloaded for file1 that smuggles in a member
// named ".lmm-file-file2". Before the extractor guard, that member landed in
// the cache version directory and made HasFileIDs(["file1","file2"]) report
// true - so the cache-first convergence guard skipped file2's download
// entirely and the mod deployed with a genuinely missing file, silently and
// with no error.
func TestService_DownloadMod_ForgedCacheMarkerInArchiveIsRejected(t *testing.T) {
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	mock := newMockSourceWithDownloads("test")
	defer mock.Close()
	svc.RegisterSource(mock)

	game := &domain.Game{ID: "testgame", Name: "Test Game", ModPath: t.TempDir()}
	require.NoError(t, svc.SaveGame(context.Background(), game))

	mod := &domain.Mod{ID: "123", SourceID: "test", Name: "Hostile Mod", Version: "1.0.0", GameID: "testgame"}
	file1 := &domain.DownloadableFile{ID: "file1", Name: "File One", FileName: "file1.zip"}

	zipPath := createTestZip(t, t.TempDir(), map[string]string{
		"file1_content.txt": "content from file 1",
		".lmm-file-file2":   "", // forges file2's completion
	})
	zipContent, err := os.ReadFile(zipPath)
	require.NoError(t, err)
	mock.AddDownload(file1.ID, zipContent)

	_, err = svc.DownloadMod(context.Background(), "test", game, mod, file1, nil)
	require.Error(t, err, "a forged cache marker must fail the download, not land in the cache")
	assert.Contains(t, err.Error(), ".lmm-")

	gameCache := svc.GetGameCache(game)
	assert.False(t, gameCache.HasFileIDs(game.ID, mod.SourceID, mod.ID, mod.Version, []string{file1.ID, "file2"}),
		"a forged marker must never make an un-downloaded file read as cached")
	assert.False(t, gameCache.HasFileIDs(game.ID, mod.SourceID, mod.ID, mod.Version, []string{"file2"}),
		"file2 was never downloaded and must not read as complete")
}

func TestService_DownloadMod_PathLikeFilename_ArchiveWithoutExtension(t *testing.T) {
	cfg := core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
	}

	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, svc.Close())
	})

	mock := newMockSourceWithDownloads("test")
	defer mock.Close()
	svc.RegisterSource(mock)

	game := &domain.Game{
		ID:      "testgame",
		Name:    "Test Game",
		ModPath: filepath.Join(t.TempDir(), "mods"),
	}
	err = svc.SaveGame(context.Background(), game)
	require.NoError(t, err)

	mod := &domain.Mod{
		ID:       "123",
		SourceID: "test",
		Name:     "Nested File Mod",
		Version:  "1.0.0",
		GameID:   "testgame",
	}

	file := &domain.DownloadableFile{
		ID:       "file1",
		Name:     "Nested File",
		FileName: "c3/f2/ac/c3f2ac27-ca21-42f3-bb09-cc41e09db10d",
	}

	tmpDir := t.TempDir()
	zipPath := createTestZip(t, tmpDir, map[string]string{"plugin.esp": "payload"})
	zipContent, err := os.ReadFile(zipPath)
	require.NoError(t, err)

	mock.AddDownload(file.ID, zipContent)

	result, err := svc.DownloadMod(context.Background(), "test", game, mod, file, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, result.FilesExtracted)
	assert.NotEmpty(t, result.Checksum)

	gameCache := svc.GetGameCache(game)
	files, err := gameCache.ListFiles(game.ID, mod.SourceID, mod.ID, mod.Version)
	require.NoError(t, err)
	assert.Equal(t, []string{"plugin.esp"}, files)

	content, err := os.ReadFile(gameCache.GetFilePath(game.ID, mod.SourceID, mod.ID, mod.Version, "plugin.esp"))
	require.NoError(t, err)
	assert.Equal(t, []byte("payload"), content)
}

// TestService_DownloadMod_RejectsFileURLFromNonDirectorySource is a regression
// test for final-review finding 3: a source.ModSource that is NOT a directory
// source must never have its file:// download URL ingested by local copy. A
// compromised or buggy remote source returning file:///etc/hostname (or any
// other local path) must not let lmm read arbitrary files off disk into the
// mod cache.
func TestService_DownloadMod_RejectsFileURLFromNonDirectorySource(t *testing.T) {
	cfg := core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
	}

	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, svc.Close())
	})

	secret := filepath.Join(t.TempDir(), "secret.txt")
	require.NoError(t, os.WriteFile(secret, []byte("do not leak"), 0644))

	mock := &mockSourceWithFileURL{mockSource: newMockSource("test"), fileURL: "file://" + secret}
	svc.RegisterSource(mock)

	game := &domain.Game{ID: "testgame", Name: "Test Game", ModPath: t.TempDir()}
	require.NoError(t, svc.SaveGame(context.Background(), game))

	mod := &domain.Mod{ID: "123", SourceID: "test", Name: "Sneaky Mod", Version: "1.0.0", GameID: "testgame"}
	file := &domain.DownloadableFile{ID: "file1", Name: "File", FileName: "file1.zip"}

	_, err = svc.DownloadMod(context.Background(), "test", game, mod, file, nil)
	require.Error(t, err)

	gameCache := svc.GetGameCache(game)
	assert.False(t, gameCache.Exists(game.ID, mod.SourceID, mod.ID, mod.Version),
		"a file:// URL from a non-directory source must not be ingested into the cache")
}

// mockSourceWithFileURL returns a file:// download URL regardless of source
// type, simulating a compromised or misbehaving remote source.
type mockSourceWithFileURL struct {
	*mockSource
	fileURL string
}

func (m *mockSourceWithFileURL) GetDownloadURL(ctx context.Context, mod *domain.Mod, fileID string) (string, error) {
	return m.fileURL, nil
}

// mockSourceWithDownloads extends mockSource with download URL support
type mockSourceWithDownloads struct {
	*mockSource
	payloads map[string][]byte // fileID -> zip content
	server   *httptest.Server

	// served counts download requests the test server actually handled, so a
	// test can assert a cache-first guard SKIPPED the download rather than
	// inferring it from side effects (#96 round 2). Atomic because the
	// handler runs on the server's own goroutine.
	served atomic.Int64

	// downloads counts requests the test server has fully served; onDownload
	// (if set) fires right after, once per request - a per-file-cancellation
	// test hangs a cancel() off it to assert a loop never starts the next
	// file once ctx is done (e.g. TestService_ApplyInstall_
	// ContextCancelledBetweenPrimaryFiles). Atomic for the same reason as
	// served: the handler runs on the server's own goroutine, and nothing
	// establishes a happens-before edge between the client's read of the
	// response body and the handler's post-Write statements.
	downloads  atomic.Int64
	onDownload func()

	// urlRequests counts GetDownloadURL calls - the FIRST thing
	// DownloadModToCache does for a file, before the HTTP client (and its
	// ctx-aware transport) is anywhere in the picture. A per-file
	// cancellation test asserts on this rather than on served/downloads:
	// those two only prove the transport refused to fetch file N, which a
	// cancelled ctx produces whether or not the caller's loop guard stopped
	// the iteration (final-review Important 2 - both cancellation tests
	// passed with the guards they pin deleted). A loop that runs an
	// iteration it should have skipped always bumps this counter.
	urlRequests atomic.Int64
}

func newMockSourceWithDownloads(id string) *mockSourceWithDownloads {
	m := &mockSourceWithDownloads{
		mockSource: newMockSource(id),
		payloads:   make(map[string][]byte),
	}
	// Create test server that serves our downloads
	m.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.served.Add(1)
		fileID := filepath.Base(r.URL.Path)
		if content, ok := m.payloads[fileID]; ok {
			w.Header().Set("Content-Type", "application/zip")
			if _, err := w.Write(content); err != nil {
				return
			}
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
		m.downloads.Add(1)
		if m.onDownload != nil {
			m.onDownload()
		}
	}))
	return m
}

func (m *mockSourceWithDownloads) AddDownload(fileID string, content []byte) {
	m.payloads[fileID] = content
}

func (m *mockSourceWithDownloads) GetDownloadURL(ctx context.Context, mod *domain.Mod, fileID string) (string, error) {
	m.urlRequests.Add(1)
	return m.server.URL + "/" + fileID, nil
}

// DownloadCount reports how many download requests the mock actually served.
func (m *mockSourceWithDownloads) DownloadCount() int { return int(m.served.Load()) }

func (m *mockSourceWithDownloads) Close() {
	m.server.Close()
}

// compilerMockSource extends mockSourceWithDownloads with source.MergeCompiler
// so a single source instance can serve both of
// TestService_DownloadMod_OrganicPrune_PreConvergencePakClaimedThenExmodzRetain's
// real generations for the SAME file ID: a plain (non-.exmodz) download that
// takes DownloadModToCache's ordinary copy path, and a later .exmodz
// re-download that takes its validate+retain (#197) path.
type compilerMockSource struct {
	fakeMergeFormat // #256: the format-vocabulary half of source.MergeCompiler
	*mockSourceWithDownloads
}

// ValidateSource implements source.MergeCompiler minimally - confirms the
// staged archive exists, mirroring service_icarus_compile_test.go's
// fakeCompilerSource. Real .exmodz parsing is covered elsewhere
// (internal/source/icarus).
func (s *compilerMockSource) ValidateSource(sourceFilePath string) error {
	_, err := os.Stat(sourceFilePath)
	return err
}

// MergeCompile is never exercised by this file's tests (they only drive
// ingest, not a merge), but is required to satisfy source.MergeCompiler.
func (s *compilerMockSource) MergeCompile(ctx context.Context, basePakPath string, sources []source.MergeSource, outputPath string) ([]string, []source.MergeFailure, error) {
	return nil, nil, os.WriteFile(outputPath, []byte("merged"), 0o644)
}

var _ source.MergeCompiler = (*compilerMockSource)(nil)

// TestService_ModLifecycleFacade pins the Phase 3 Service boundary: callers
// drive the full mod lifecycle via Service methods without ever reaching into
// *db.DB or *source.Registry directly.
func TestService_ModLifecycleFacade(t *testing.T) {
	cfg := core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
	}
	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	require.NoError(t, svc.SaveGame(context.Background(), &domain.Game{
		ID:      "g1",
		Name:    "Game 1",
		ModPath: t.TempDir(),
	}))

	mod := &domain.InstalledMod{
		Mod: domain.Mod{
			ID: "42", SourceID: "test", Name: "Test", Version: "1.0",
			GameID: "g1",
		},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		Deployed:     false,
	}

	require.NoError(t, svc.SaveInstalledMod(context.Background(), mod))

	require.NoError(t, svc.SetModDeployed(context.Background(), "test", "42", "g1", "default", true))
	got, err := svc.GetInstalledMod(context.Background(), "test", "42", "g1", "default")
	require.NoError(t, err)
	assert.True(t, got.Deployed)

	require.NoError(t, svc.SetModEnabled(context.Background(), "test", "42", "g1", "default", false))
	got, err = svc.GetInstalledMod(context.Background(), "test", "42", "g1", "default")
	require.NoError(t, err)
	assert.False(t, got.Enabled)

	require.NoError(t, svc.DeleteInstalledMod(context.Background(), "test", "42", "g1", "default"))
	_, err = svc.GetInstalledMod(context.Background(), "test", "42", "g1", "default")
	assert.Error(t, err, "mod should be gone after delete")
}

// TestService_GetFileOwner_NotFound proves the tuple-shaped GetFileOwner
// signature: (sourceID, modID, found, err). Returns found=false (not an error)
// when no record exists.
func TestService_GetFileOwner_NotFound(t *testing.T) {
	cfg := core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
	}
	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	sourceID, modID, found, err := svc.GetFileOwner(context.Background(), "g1", "default", "missing/path.txt")
	require.NoError(t, err)
	assert.False(t, found)
	assert.Empty(t, sourceID)
	assert.Empty(t, modID)
}
