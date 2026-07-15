package core_test

import (
	"context"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Mock source that supports update checking
type updateMockSource struct {
	id         string
	currentMod *domain.Mod
}

func (m *updateMockSource) ID() string      { return m.id }
func (m *updateMockSource) Name() string    { return "Update Mock" }
func (m *updateMockSource) AuthURL() string { return "" }
func (m *updateMockSource) ExchangeToken(ctx context.Context, code string) (*source.Token, error) {
	return nil, nil
}
func (m *updateMockSource) Search(ctx context.Context, query source.SearchQuery) (source.SearchResult, error) {
	return source.SearchResult{}, nil
}
func (m *updateMockSource) GetMod(ctx context.Context, gameID, modID string) (*domain.Mod, error) {
	if m.currentMod != nil && m.currentMod.ID == modID {
		return m.currentMod, nil
	}
	return nil, domain.ErrModNotFound
}
func (m *updateMockSource) GetDependencies(ctx context.Context, mod *domain.Mod) ([]domain.ModReference, error) {
	return nil, nil
}
func (m *updateMockSource) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	return nil, nil
}
func (m *updateMockSource) GetDownloadURL(ctx context.Context, mod *domain.Mod, fileID string) (string, error) {
	return "", nil
}
func (m *updateMockSource) CheckUpdates(ctx context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	var updates []domain.Update
	for _, inst := range installed {
		if m.currentMod != nil && inst.ID == m.currentMod.ID && inst.Version != m.currentMod.Version {
			updates = append(updates, domain.Update{
				InstalledMod: inst,
				NewVersion:   m.currentMod.Version,
				Changelog:    "Bug fixes and improvements",
			})
		}
	}
	return updates, nil
}

func TestUpdater_CheckUpdates(t *testing.T) {
	registry := source.NewRegistry()
	mockSrc := &updateMockSource{
		id: "test",
		currentMod: &domain.Mod{
			ID:       "123",
			SourceID: "test",
			Name:     "Test Mod",
			Version:  "2.0.0", // Newer version available
			GameID:   "skyrim",
		},
	}
	registry.Register(mockSrc)

	updater := core.NewUpdater(registry)

	installed := []domain.InstalledMod{
		{
			Mod: domain.Mod{
				ID:       "123",
				SourceID: "test",
				Name:     "Test Mod",
				Version:  "1.0.0", // Old version
				GameID:   "skyrim",
			},
			ProfileName:  "default",
			UpdatePolicy: domain.UpdateNotify,
			InstalledAt:  time.Now(),
			Enabled:      true,
		},
	}

	updates, err := updater.CheckUpdates(context.Background(), nil, installed)
	require.NoError(t, err)
	require.Len(t, updates, 1)
	assert.Equal(t, "2.0.0", updates[0].NewVersion)
	assert.Equal(t, "1.0.0", updates[0].InstalledMod.Version)
}

func TestUpdater_CheckUpdates_NoUpdates(t *testing.T) {
	registry := source.NewRegistry()
	mockSrc := &updateMockSource{
		id: "test",
		currentMod: &domain.Mod{
			ID:       "123",
			SourceID: "test",
			Name:     "Test Mod",
			Version:  "1.0.0", // Same version
			GameID:   "skyrim",
		},
	}
	registry.Register(mockSrc)

	updater := core.NewUpdater(registry)

	installed := []domain.InstalledMod{
		{
			Mod: domain.Mod{
				ID:       "123",
				SourceID: "test",
				Name:     "Test Mod",
				Version:  "1.0.0", // Same version - no update
				GameID:   "skyrim",
			},
			ProfileName: "default",
		},
	}

	updates, err := updater.CheckUpdates(context.Background(), nil, installed)
	require.NoError(t, err)
	assert.Empty(t, updates)
}

func TestUpdater_CheckUpdates_PinnedModsSkipped(t *testing.T) {
	registry := source.NewRegistry()
	mockSrc := &updateMockSource{
		id: "test",
		currentMod: &domain.Mod{
			ID:       "123",
			SourceID: "test",
			Version:  "2.0.0",
		},
	}
	registry.Register(mockSrc)

	updater := core.NewUpdater(registry)

	installed := []domain.InstalledMod{
		{
			Mod: domain.Mod{
				ID:       "123",
				SourceID: "test",
				Version:  "1.0.0",
			},
			UpdatePolicy: domain.UpdatePinned, // Pinned - should skip
		},
	}

	updates, err := updater.CheckUpdates(context.Background(), nil, installed)
	require.NoError(t, err)
	assert.Empty(t, updates, "pinned mods should not show updates")
}

func TestUpdater_CheckUpdates_LocalModsSkipped(t *testing.T) {
	registry := source.NewRegistry()
	// No source registered for "local" - local mods have no remote source
	updater := core.NewUpdater(registry)

	installed := []domain.InstalledMod{
		{
			Mod: domain.Mod{
				ID:       "abc123",
				SourceID: domain.SourceLocal, // Local mod - should skip
				Name:     "My Local Mod",
				Version:  "1.0.0",
			},
			UpdatePolicy: domain.UpdateNotify,
		},
	}

	updates, err := updater.CheckUpdates(context.Background(), nil, installed)
	require.NoError(t, err)
	assert.Empty(t, updates, "local mods should not be checked for updates")
}

// gameIDCapturingSource records the GameIDs it receives in CheckUpdates.
type gameIDCapturingSource struct {
	source.ModSource // embed an existing test mock for the other methods
	id               string
	received         []string
}

func (g *gameIDCapturingSource) ID() string { return g.id }
func (g *gameIDCapturingSource) CheckUpdates(ctx context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	for _, inst := range installed {
		g.received = append(g.received, inst.GameID)
	}
	return nil, nil
}

func TestCheckUpdatesTranslatesGameIDPerSourceMapping(t *testing.T) {
	reg := source.NewRegistry()
	mapped := &gameIDCapturingSource{id: "nexusmods"}
	unmapped := &gameIDCapturingSource{id: "my-repo"}
	reg.Register(mapped)
	reg.Register(unmapped)
	u := core.NewUpdater(reg)

	game := &domain.Game{
		ID: "skyrim-se",
		SourceIDs: map[string]string{
			"nexusmods": "skyrimspecialedition",
			"my-repo":   "", // empty mapping: keep the lmm game id
		},
	}
	installed := []domain.InstalledMod{
		{Mod: domain.Mod{ID: "a", SourceID: "nexusmods", GameID: "skyrim-se", Version: "1.0"}},
		{Mod: domain.Mod{ID: "b", SourceID: "my-repo", GameID: "skyrim-se", Version: "1.0"}},
	}

	_, err := u.CheckUpdates(context.Background(), game, installed)
	require.NoError(t, err)
	assert.Equal(t, []string{"skyrimspecialedition"}, mapped.received)
	assert.Equal(t, []string{"skyrim-se"}, unmapped.received)
	// Caller's slice must be untouched.
	assert.Equal(t, "skyrim-se", installed[0].GameID)
}

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		v1       string
		v2       string
		expected int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.0.0", "2.0.0", -1},
		{"2.0.0", "1.0.0", 1},
		{"1.0.0", "1.0.1", -1},
		{"1.0.1", "1.0.0", 1},
		{"1.0", "1.0.0", 0},
		{"1.0.0", "1.0", 0},
		{"1.10.0", "1.9.0", 1},
		{"1.9.0", "1.10.0", -1},
	}

	for _, tt := range tests {
		t.Run(tt.v1+"_vs_"+tt.v2, func(t *testing.T) {
			result := core.CompareVersions(tt.v1, tt.v2)
			assert.Equal(t, tt.expected, result)
		})
	}
}
