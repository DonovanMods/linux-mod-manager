package app

// #412: registering the Icarus adapter is what makes U1's
// `deploy_mode: compile` => `icarus` migration LIVE. U1 pinned both states
// deliberately - core derives the name only for an adapter that is actually
// registered - so this package is where the switch is thrown, and this is
// the test that proves it was.

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegisterAdapters_RegistersIcarusBesideTheIdentity(t *testing.T) {
	svc := newTestService(t)
	registerAdapters(svc)

	assert.ElementsMatch(t, []string{adapter.GenericID, "icarus"}, svc.ListAdapters(),
		"internal/app registers every concrete adapter; the identity is the registry's own built-in")
}

// TestRegisterAdapters_CompileGameMigratesToIcarus is the migration itself:
// an existing games.yaml carrying `deploy_mode: compile` and NO `adapter:`
// key resolves to the Icarus adapter, with nothing for the user to edit and
// nothing written back.
func TestRegisterAdapters_CompileGameMigratesToIcarus(t *testing.T) {
	svc := newTestService(t)
	registerAdapters(svc)

	game := &domain.Game{ID: "icarus", DeployMode: domain.DeployCompile}
	assert.Equal(t, "icarus", svc.AdapterName(game))

	a, err := svc.AdapterFor(game)
	require.NoError(t, err)
	_, canCompile := adapter.Compiler(a)
	assert.True(t, canCompile, "the migrated adapter must be the one that actually compiles")
	assert.Empty(t, game.Adapter, "the derivation is in-memory: games.yaml is never rewritten")
}

// A game that says nothing still gets the identity, which is every game lmm
// managed before the seam existed.
func TestRegisterAdapters_PlainGameStaysGeneric(t *testing.T) {
	svc := newTestService(t)
	registerAdapters(svc)

	a, err := svc.AdapterFor(&domain.Game{ID: "skyrim-se"})
	require.NoError(t, err)
	assert.Equal(t, adapter.GenericID, a.ID())
	_, canCompile := adapter.Compiler(a)
	assert.False(t, canCompile)
}
