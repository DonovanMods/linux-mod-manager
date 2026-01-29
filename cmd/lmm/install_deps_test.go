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

func TestResolveDependencies_CyclicDeps(t *testing.T) {
	// Mod A depends on B, B depends on A
	src := &mockDepSource{
		mods: map[string]*domain.Mod{
			"100": {ID: "100", SourceID: "nexusmods", Name: "Mod A", GameID: "skyrim"},
			"200": {ID: "200", SourceID: "nexusmods", Name: "Mod B", GameID: "skyrim"},
		},
		deps: map[string][]domain.ModReference{
			"100": {{SourceID: "nexusmods", ModID: "200"}},
			"200": {{SourceID: "nexusmods", ModID: "100"}},
		},
	}

	target := src.mods["100"]
	installed := make(map[string]bool)

	// Should not infinite loop - visited map prevents it
	plan, err := resolveDependencies(context.Background(), src, target, installed)
	require.NoError(t, err)
	// Both mods should be in plan (cycle is handled by visited check)
	assert.Len(t, plan.mods, 2)
}

func TestResolveDependencies_DeepTransitive(t *testing.T) {
	// A -> B -> C -> D (4 levels deep)
	src := &mockDepSource{
		mods: map[string]*domain.Mod{
			"A": {ID: "A", SourceID: "nexusmods", Name: "Mod A", GameID: "skyrim"},
			"B": {ID: "B", SourceID: "nexusmods", Name: "Mod B", GameID: "skyrim"},
			"C": {ID: "C", SourceID: "nexusmods", Name: "Mod C", GameID: "skyrim"},
			"D": {ID: "D", SourceID: "nexusmods", Name: "Mod D", GameID: "skyrim"},
		},
		deps: map[string][]domain.ModReference{
			"A": {{SourceID: "nexusmods", ModID: "B"}},
			"B": {{SourceID: "nexusmods", ModID: "C"}},
			"C": {{SourceID: "nexusmods", ModID: "D"}},
		},
	}

	target := src.mods["A"]
	installed := make(map[string]bool)

	plan, err := resolveDependencies(context.Background(), src, target, installed)
	require.NoError(t, err)
	assert.Len(t, plan.mods, 4)
	// Order should be D, C, B, A (deepest first)
	assert.Equal(t, "D", plan.mods[0].ID)
	assert.Equal(t, "C", plan.mods[1].ID)
	assert.Equal(t, "B", plan.mods[2].ID)
	assert.Equal(t, "A", plan.mods[3].ID)
}
