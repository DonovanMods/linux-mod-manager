package modtypes

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
)

// Each adapter knows exactly the types its table lists (#635), and reports
// every other one as unknown - the default type "" included, which no table
// lists: core routes an untyped mod by RouteRoots, never by RootForModType - - which core refuses rather than deploying it
// under the mod_path.
func TestRootForModType(t *testing.T) {
	cases := []struct {
		a    adapter.GameAdapter
		want map[string]adapter.DeployRoot
		not  []string
	}{
		{Witcher3(), map[string]adapter.DeployRoot{
			"witcher3tl": adapter.RootInstallPath,
			"dinput":     adapter.RootInstallPath,
		}, []string{"", "witcher3dlc", "witcher3menumodroot", "witcher3menumoddocuments", "w3modlimitpatcher", "bg3-loose", "starfield-data-folder"}},
		{BaldursGate3(), map[string]adapter.DeployRoot{
			"bg3-loose": adapter.RootModPath,
		}, []string{"", "dinput", "bg3-replacer", "bg3-bg3se", "witcher3tl"}},
	}
	for _, c := range cases {
		for typ, want := range c.want {
			got, ok := adapter.RootForModType(c.a, &domain.Game{ID: "any"}, typ)
			assert.True(t, ok, "%s knows %q", c.a.ID(), typ)
			assert.Equal(t, want, got, "%s: %q", c.a.ID(), typ)
		}
		for _, typ := range c.not {
			_, ok := adapter.RootForModType(c.a, &domain.Game{ID: "any"}, typ)
			assert.False(t, ok, "%s does not know %q", c.a.ID(), typ)
		}
	}
}

// The adapters have no per-file rules: an untyped mod deploys as packaged,
// under the mod_path, exactly as the generic adapter deploys it.
func TestNoPerFileRules(t *testing.T) {
	for _, a := range All() {
		files := []string{"bin/x64/dinput8.dll", "mods/modA/content/blob0.bundle", "Public/x.lsx"}
		assert.Equal(t, []adapter.DeployRoot{adapter.RootModPath, adapter.RootModPath, adapter.RootModPath},
			adapter.RouteRoots(a, &domain.Game{}, files), a.ID())
		l, err := a.NormalizeArchive(adapter.NormalizeRequest{Members: files})
		assert.NoError(t, err)
		assert.False(t, l.Applies(), "%s leaves an archive as packaged", a.ID())
	}
}

func TestIdentity(t *testing.T) {
	assert.Equal(t, "witcher3", Witcher3().ID())
	assert.Equal(t, "baldurs-gate-3", BaldursGate3().ID())
	ids := map[string]bool{}
	for _, a := range All() {
		assert.NotEmpty(t, a.Label())
		assert.False(t, ids[a.ID()], "IDs are unique")
		ids[a.ID()] = true
	}
	assert.Len(t, ids, 2)
}
