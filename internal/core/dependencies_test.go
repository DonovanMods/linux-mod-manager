package core_test

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolver_ResolveDependencies_NoDeps(t *testing.T) {
	resolver := core.NewDependencyResolver()

	mod := domain.Mod{
		ID:           "123",
		SourceID:     "nexusmods",
		Name:         "Test Mod",
		Dependencies: nil,
	}

	order, err := resolver.Resolve([]domain.Mod{mod})
	require.NoError(t, err)
	require.Len(t, order, 1)
	assert.Equal(t, "123", order[0].ID)
}

func TestResolver_ResolveDependencies_SimpleDep(t *testing.T) {
	resolver := core.NewDependencyResolver()

	modA := domain.Mod{
		ID:       "A",
		SourceID: "nexusmods",
		Name:     "Mod A",
		Dependencies: []domain.ModReference{
			{SourceID: "nexusmods", ModID: "B"},
		},
	}
	modB := domain.Mod{
		ID:       "B",
		SourceID: "nexusmods",
		Name:     "Mod B",
	}

	order, err := resolver.Resolve([]domain.Mod{modA, modB})
	require.NoError(t, err)
	require.Len(t, order, 2)

	// B should come before A (dependency before dependent)
	assert.Equal(t, "B", order[0].ID)
	assert.Equal(t, "A", order[1].ID)
}

func TestResolver_ResolveDependencies_Chain(t *testing.T) {
	resolver := core.NewDependencyResolver()

	// A -> B -> C
	modA := domain.Mod{
		ID:       "A",
		SourceID: "nexusmods",
		Dependencies: []domain.ModReference{
			{SourceID: "nexusmods", ModID: "B"},
		},
	}
	modB := domain.Mod{
		ID:       "B",
		SourceID: "nexusmods",
		Dependencies: []domain.ModReference{
			{SourceID: "nexusmods", ModID: "C"},
		},
	}
	modC := domain.Mod{
		ID:       "C",
		SourceID: "nexusmods",
	}

	order, err := resolver.Resolve([]domain.Mod{modA, modB, modC})
	require.NoError(t, err)
	require.Len(t, order, 3)

	// C should come first, then B, then A
	assert.Equal(t, "C", order[0].ID)
	assert.Equal(t, "B", order[1].ID)
	assert.Equal(t, "A", order[2].ID)
}

func TestResolver_ResolveDependencies_CycleDetection(t *testing.T) {
	resolver := core.NewDependencyResolver()

	// A -> B -> A (cycle)
	modA := domain.Mod{
		ID:       "A",
		SourceID: "nexusmods",
		Dependencies: []domain.ModReference{
			{SourceID: "nexusmods", ModID: "B"},
		},
	}
	modB := domain.Mod{
		ID:       "B",
		SourceID: "nexusmods",
		Dependencies: []domain.ModReference{
			{SourceID: "nexusmods", ModID: "A"},
		},
	}

	_, err := resolver.Resolve([]domain.Mod{modA, modB})
	assert.ErrorIs(t, err, domain.ErrDependencyLoop)
}

func TestResolver_ResolveDependencies_Diamond(t *testing.T) {
	resolver := core.NewDependencyResolver()

	// Diamond: A -> B, A -> C, B -> D, C -> D
	modA := domain.Mod{
		ID:       "A",
		SourceID: "nexusmods",
		Dependencies: []domain.ModReference{
			{SourceID: "nexusmods", ModID: "B"},
			{SourceID: "nexusmods", ModID: "C"},
		},
	}
	modB := domain.Mod{
		ID:       "B",
		SourceID: "nexusmods",
		Dependencies: []domain.ModReference{
			{SourceID: "nexusmods", ModID: "D"},
		},
	}
	modC := domain.Mod{
		ID:       "C",
		SourceID: "nexusmods",
		Dependencies: []domain.ModReference{
			{SourceID: "nexusmods", ModID: "D"},
		},
	}
	modD := domain.Mod{
		ID:       "D",
		SourceID: "nexusmods",
	}

	order, err := resolver.Resolve([]domain.Mod{modA, modB, modC, modD})
	require.NoError(t, err)
	require.Len(t, order, 4)

	// D must come before B and C, which must come before A
	posD := findModPosition(order, "D")
	posB := findModPosition(order, "B")
	posC := findModPosition(order, "C")
	posA := findModPosition(order, "A")

	assert.Less(t, posD, posB, "D should come before B")
	assert.Less(t, posD, posC, "D should come before C")
	assert.Less(t, posB, posA, "B should come before A")
	assert.Less(t, posC, posA, "C should come before A")
}

func TestResolver_MissingDependency(t *testing.T) {
	resolver := core.NewDependencyResolver()

	modA := domain.Mod{
		ID:       "A",
		SourceID: "nexusmods",
		Dependencies: []domain.ModReference{
			{SourceID: "nexusmods", ModID: "missing"},
		},
	}

	_, err := resolver.Resolve([]domain.Mod{modA})
	assert.Error(t, err)
}

func findModPosition(mods []domain.Mod, id string) int {
	for i, m := range mods {
		if m.ID == id {
			return i
		}
	}
	return -1
}
