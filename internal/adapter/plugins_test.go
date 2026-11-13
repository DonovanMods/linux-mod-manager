package adapter_test

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
)

// pluginGame is a fake adapter with the plugin and root capabilities, so
// the nil-safe helpers below can be seen to dispatch to it.
type pluginGame struct{ adapter.Generic }

func (pluginGame) ID() string { return "plugin-game" }

func (pluginGame) PluginRules(*domain.Game) adapter.PluginSpec {
	return adapter.PluginSpec{ListKind: adapter.ListAsterisk, ListFile: "Plugins.txt"}
}

func (pluginGame) RouteRoots(_ *domain.Game, files []string) []adapter.DeployRoot {
	out := make([]adapter.DeployRoot, len(files))
	for i, f := range files {
		if f == "loader.exe" {
			out[i] = adapter.RootInstallPath
		}
	}
	return out
}

func (pluginGame) RootForModType(_ *domain.Game, modType string) (adapter.DeployRoot, bool) {
	if modType == "dinput" {
		return adapter.RootInstallPath, true
	}
	return adapter.RootModPath, false
}

// An adapter without PluginOrderer has no rules: the zero spec, reported
// as absent, which is how core will know to leave a game's plugin list
// alone.
func TestPluginRules_AbsentCapabilityIsTheZeroSpec(t *testing.T) {
	spec, ok := adapter.PluginRules(adapter.Generic{}, &domain.Game{ID: "g"})
	assert.False(t, ok)
	assert.Equal(t, adapter.PluginSpec{}, spec)
	assert.Equal(t, adapter.ListNone, spec.ListKind, "the zero ListKind is none")
}

func TestPluginRules_DispatchesToTheCapability(t *testing.T) {
	spec, ok := adapter.PluginRules(pluginGame{}, &domain.Game{ID: "g"})
	assert.True(t, ok)
	assert.Equal(t, adapter.ListAsterisk, spec.ListKind)
	assert.Equal(t, "Plugins.txt", spec.ListFile)
}

// A PluginOrderer that does not recognise the game answers the zero spec,
// and the helper reports that as absent too: "this adapter manages plugins"
// and "these are this game's rules" are different claims.
func TestPluginRules_ZeroSpecFromTheCapabilityIsAbsent(t *testing.T) {
	_, ok := adapter.PluginRules(zeroOrderer{}, &domain.Game{ID: "g"})
	assert.False(t, ok)
}

type zeroOrderer struct{ adapter.Generic }

func (zeroOrderer) PluginRules(*domain.Game) adapter.PluginSpec { return adapter.PluginSpec{} }

func TestEnumStrings(t *testing.T) {
	assert.Equal(t, "none", adapter.ListNone.String())
	assert.Equal(t, "asterisk", adapter.ListAsterisk.String())
	assert.Equal(t, "textfile", adapter.ListTextfile.String())
	assert.Equal(t, "unknown", adapter.ListKind(99).String())
	assert.Equal(t, "none", adapter.HeaderNone.String())
	assert.Equal(t, "tes4", adapter.HeaderTES4.String())
	assert.Equal(t, "unknown", adapter.HeaderKind(99).String())
	assert.Equal(t, "game", adapter.SpecRootGame.String())
	assert.Equal(t, "my_games", adapter.SpecRootMyGames.String())
	assert.Equal(t, "unknown", adapter.SpecRoot(99).String())
	assert.Equal(t, "mod_path", adapter.RootModPath.String())
	assert.Equal(t, "install_path", adapter.RootInstallPath.String())
	assert.Equal(t, "unknown", adapter.DeployRoot(99).String())
}

// Without a RootRouter every file deploys under mod_path, which is every
// adapter's behaviour before #606.
func TestRouteRoots_AbsentCapabilityIsModPath(t *testing.T) {
	got := adapter.RouteRoots(adapter.Generic{}, &domain.Game{}, []string{"a.esp", "loader.exe"})
	assert.Equal(t, []adapter.DeployRoot{adapter.RootModPath, adapter.RootModPath}, got)

	root, ok := adapter.RootForModType(adapter.Generic{}, &domain.Game{}, "dinput")
	assert.False(t, ok)
	assert.Equal(t, adapter.RootModPath, root)
}

func TestRouteRoots_DispatchesToTheCapability(t *testing.T) {
	got := adapter.RouteRoots(pluginGame{}, &domain.Game{}, []string{"a.esp", "loader.exe"})
	assert.Equal(t, []adapter.DeployRoot{adapter.RootModPath, adapter.RootInstallPath}, got)

	root, ok := adapter.RootForModType(pluginGame{}, &domain.Game{}, "dinput")
	assert.True(t, ok)
	assert.Equal(t, adapter.RootInstallPath, root)
}

// A RootRouter that answers the wrong number of routes is a bug in the
// adapter; the helper must not hand core a slice it would index out of
// range, so it falls back to mod_path for every file.
func TestRouteRoots_WrongLengthAnswerIsModPath(t *testing.T) {
	got := adapter.RouteRoots(shortRouter{}, &domain.Game{}, []string{"a", "b"})
	assert.Equal(t, []adapter.DeployRoot{adapter.RootModPath, adapter.RootModPath}, got)
}

type shortRouter struct{ pluginGame }

func (shortRouter) RouteRoots(*domain.Game, []string) []adapter.DeployRoot {
	return []adapter.DeployRoot{adapter.RootInstallPath}
}
