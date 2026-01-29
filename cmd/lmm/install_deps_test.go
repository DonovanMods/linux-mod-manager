package main

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockDepSource implements a minimal source for testing dependency resolution
type mockDepSource struct {
	mods map[string]*domain.Mod           // modID -> Mod
	deps map[string][]domain.ModReference // modID -> dependencies
}

func (m *mockDepSource) GetMod(ctx context.Context, gameID, modID string) (*domain.Mod, error) {
	if mod, ok := m.mods[modID]; ok {
		return mod, nil
	}
	return nil, domain.ErrModNotFound
}

func (m *mockDepSource) GetDependencies(ctx context.Context, mod *domain.Mod) ([]domain.ModReference, error) {
	if deps, ok := m.deps[mod.ID]; ok {
		return deps, nil
	}
	return nil, nil
}

func TestResolveDependencies_NoDeps(t *testing.T) {
	src := &mockDepSource{
		mods: map[string]*domain.Mod{
			"100": {ID: "100", SourceID: "nexusmods", Name: "Target Mod"},
		},
		deps: map[string][]domain.ModReference{},
	}

	target := src.mods["100"]
	installed := make(map[string]bool)

	plan, err := resolveDependencies(context.Background(), src, target, installed)
	require.NoError(t, err)
	assert.Len(t, plan.mods, 1)
	assert.Equal(t, "100", plan.mods[0].ID)
	assert.Empty(t, plan.missing)
}

func TestResolveDependencies_WithDeps(t *testing.T) {
	src := &mockDepSource{
		mods: map[string]*domain.Mod{
			"100": {ID: "100", SourceID: "nexusmods", Name: "Target Mod"},
			"200": {ID: "200", SourceID: "nexusmods", Name: "Dependency A"},
			"300": {ID: "300", SourceID: "nexusmods", Name: "Dependency B"},
		},
		deps: map[string][]domain.ModReference{
			"100": {
				{SourceID: "nexusmods", ModID: "200"},
				{SourceID: "nexusmods", ModID: "300"},
			},
		},
	}

	target := src.mods["100"]
	installed := make(map[string]bool)

	plan, err := resolveDependencies(context.Background(), src, target, installed)
	require.NoError(t, err)
	assert.Len(t, plan.mods, 3)
	// Target should be last
	assert.Equal(t, "100", plan.mods[len(plan.mods)-1].ID)
	assert.Empty(t, plan.missing)
}

func TestResolveDependencies_SkipsInstalled(t *testing.T) {
	src := &mockDepSource{
		mods: map[string]*domain.Mod{
			"100": {ID: "100", SourceID: "nexusmods", Name: "Target Mod"},
			"200": {ID: "200", SourceID: "nexusmods", Name: "Already Installed"},
		},
		deps: map[string][]domain.ModReference{
			"100": {{SourceID: "nexusmods", ModID: "200"}},
		},
	}

	target := src.mods["100"]
	installed := map[string]bool{"nexusmods:200": true}

	plan, err := resolveDependencies(context.Background(), src, target, installed)
	require.NoError(t, err)
	assert.Len(t, plan.mods, 1) // Only target, dep is skipped
	assert.Equal(t, "100", plan.mods[0].ID)
}

func TestResolveDependencies_MissingDep(t *testing.T) {
	src := &mockDepSource{
		mods: map[string]*domain.Mod{
			"100": {ID: "100", SourceID: "nexusmods", Name: "Target Mod"},
			// "200" is missing (external dependency like SKSE)
		},
		deps: map[string][]domain.ModReference{
			"100": {{SourceID: "nexusmods", ModID: "200"}},
		},
	}

	target := src.mods["100"]
	installed := make(map[string]bool)

	plan, err := resolveDependencies(context.Background(), src, target, installed)
	require.NoError(t, err)
	assert.Len(t, plan.mods, 1) // Only target
	assert.Contains(t, plan.missing, "nexusmods:200")
}

func TestResolveDependencies_TransitiveDeps(t *testing.T) {
	src := &mockDepSource{
		mods: map[string]*domain.Mod{
			"100": {ID: "100", SourceID: "nexusmods", Name: "Target"},
			"200": {ID: "200", SourceID: "nexusmods", Name: "Direct Dep"},
			"300": {ID: "300", SourceID: "nexusmods", Name: "Transitive Dep"},
		},
		deps: map[string][]domain.ModReference{
			"100": {{SourceID: "nexusmods", ModID: "200"}},
			"200": {{SourceID: "nexusmods", ModID: "300"}},
		},
	}

	target := src.mods["100"]
	installed := make(map[string]bool)

	plan, err := resolveDependencies(context.Background(), src, target, installed)
	require.NoError(t, err)
	assert.Len(t, plan.mods, 3)
	// Order should be: 300 (transitive), 200 (direct), 100 (target)
	assert.Equal(t, "300", plan.mods[0].ID)
	assert.Equal(t, "200", plan.mods[1].ID)
	assert.Equal(t, "100", plan.mods[2].ID)
}
