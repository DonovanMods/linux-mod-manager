package core_test

import (
	"context"
	"testing"

	"lmm/internal/core"
	"lmm/internal/domain"
	"lmm/internal/source"

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
