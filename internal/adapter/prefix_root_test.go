package adapter_test

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
)

// prefixGame routes .pak files into the prefix (#636) and places each in a
// fixed folder there.
type prefixGame struct{ adapter.Generic }

func (prefixGame) ID() string { return "prefix-game" }

func (prefixGame) RouteRoots(_ *domain.Game, files []string) []adapter.DeployRoot {
	out := make([]adapter.DeployRoot, len(files))
	for i, f := range files {
		if f == "a.pak" || f == "wrap/b.pak" {
			out[i] = adapter.RootPrefix
		}
	}
	return out
}

func (prefixGame) RootForModType(_ *domain.Game, modType string) (adapter.DeployRoot, bool) {
	return adapter.RootPrefix, modType == "paks"
}

func (prefixGame) PrefixPath(_ *domain.Game, member string) string {
	switch member {
	case "bad-abs":
		return "/etc/passwd"
	case "bad-up":
		return "AppData/../../x"
	case "bad-empty":
		return ""
	}
	return "AppData/Local/Game/Mods/" + member
}

// rootOnly answers RootPrefix but cannot say where: an adapter bug.
type rootOnly struct{ prefixGame }

func (rootOnly) PrefixPath() {}

func TestPrefixRoot_DispatchesToTheCapability(t *testing.T) {
	g := &domain.Game{}
	assert.Equal(t, []adapter.DeployRoot{adapter.RootModPath, adapter.RootPrefix, adapter.RootPrefix},
		adapter.RouteRoots(prefixGame{}, g, []string{"Data/x.lsx", "a.pak", "wrap/b.pak"}))
	got, ok := adapter.PrefixPath(prefixGame{}, g, "a.pak")
	assert.True(t, ok)
	assert.Equal(t, "AppData/Local/Game/Mods/a.pak", got)
	assert.True(t, adapter.PlacesInPrefix(prefixGame{}))
	assert.False(t, adapter.PlacesInPrefix(adapter.Generic{}))
}

// An answer that is not a clean path beneath the user directory is no
// answer: core refuses the file rather than writing it anywhere else.
func TestPrefixPath_RefusesAnUnusableAnswer(t *testing.T) {
	for _, m := range []string{"bad-abs", "bad-up", "bad-empty"} {
		_, ok := adapter.PrefixPath(prefixGame{}, &domain.Game{}, m)
		assert.False(t, ok, m)
	}
	got, ok := adapter.PrefixPath(prefixGame{}, &domain.Game{}, "./x//y.pak")
	assert.True(t, ok)
	assert.Equal(t, "AppData/Local/Game/Mods/x/y.pak", got, "canonicalised")
	_, ok = adapter.PrefixPath(adapter.Generic{}, &domain.Game{}, "a.pak")
	assert.False(t, ok, "no capability, no answer")
}

// A RootRouter that routes into the prefix without saying where is an
// adapter bug, treated as no answer: every file deploys under the mod_path,
// as for a wrong-length answer, and a stated type naming the prefix is
// unknown.
func TestPrefixRoot_WithoutAPlacerIsNoAnswer(t *testing.T) {
	assert.Equal(t, []adapter.DeployRoot{adapter.RootModPath, adapter.RootModPath},
		adapter.RouteRoots(rootOnly{}, &domain.Game{}, []string{"x", "a.pak"}))
	_, ok := adapter.RootForModType(rootOnly{}, &domain.Game{}, "paks")
	assert.False(t, ok)
	root, ok := adapter.RootForModType(prefixGame{}, &domain.Game{}, "paks")
	assert.True(t, ok)
	assert.Equal(t, adapter.RootPrefix, root)
}
