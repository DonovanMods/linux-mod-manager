package adapter_test

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// importCorpus is every archive SHAPE internal/core's import and deploy
// tests exercise, taken from their own member lists: a sole top-level
// directory, a flat root, nested directories, a Data/-rooted Bethesda
// layout, a loader-rooted layout, single-file compile artifacts and the
// degenerate cases (empty archive, a member with no directory at all).
//
// The generic adapter must answer ALL of them with the identity, which is
// what makes "the entire golden set passes with no re-recording" a proof
// rather than a hope (design §6.2).
var importCorpus = map[string][]string{
	"sole top-level directory": {"MyMod/a.esp", "MyMod/b.esp", "MyMod/sub/b.txt"},
	"flat root":                {"a.esp", "readme.txt"},
	"bethesda data root":       {"Data/mod.esp", "Data/meshes/test.nif", "Data/game.ini"},
	"loader rooted":            {"BepInEx/core/plugin.dll", "BepInEx/config/plugin.cfg"},
	"bare plugins root":        {"plugins/mod.dll"},
	"nested only":              {"aaa/b.txt", "aaa/d.txt"},
	"single compile artifact":  {"MyMod_P.pak"},
	"native merge source":      {"MyMod.exmodz"},
	"empty archive":            {},
	"no directory at all":      {"single.txt"},
}

func TestGenericNormalizeArchiveIsTheIdentity(t *testing.T) {
	a := adapter.Generic{}
	game := &domain.Game{ID: "skyrim-se", ModPath: "/games/skyrim-se/Data"}

	for name, members := range importCorpus {
		t.Run(name, func(t *testing.T) {
			layout, err := a.NormalizeArchive(adapter.NormalizeRequest{
				Game: game, ModName: "MyMod", Members: members,
			})
			require.NoError(t, err)
			assert.False(t, layout.Applies(), "the generic adapter must never claim a rewrite")
			assert.Empty(t, layout.Warnings, "the generic adapter never warns")
			assert.Empty(t, layout.Kind)

			for _, m := range members {
				got, keep := layout.Rewrite(m)
				assert.True(t, keep, "%q must be kept", m)
				assert.Equal(t, m, got, "%q must not move", m)
			}
		})
	}
}

func TestGenericImplementsNoOptionalCapability(t *testing.T) {
	// Stated as an assertion rather than left to the reader: the day this
	// package grows a capability is the day "byte-for-byte identical for
	// every existing game" stops being provable.
	var a adapter.GameAdapter = adapter.Generic{}

	_, isRouter := a.(adapter.FileRouter)
	assert.False(t, isRouter, "generic-files must implement no FileRouter")
	_, isPre := a.(adapter.Preconditioner)
	assert.False(t, isPre, "generic-files must implement no Preconditioner")
	_, isVerifier := a.(adapter.Verifier)
	assert.False(t, isVerifier, "generic-files must implement no Verifier")
	_, isGuide := a.(adapter.Guide)
	assert.False(t, isGuide, "generic-files must implement no Guide")
	_, isCompiler := adapter.Compiler(a)
	assert.False(t, isCompiler, "generic-files must implement no MergeCompiler")

	// And every file it is asked about routes to the linker.
	game := &domain.Game{ID: "skyrim-se"}
	for _, members := range importCorpus {
		for _, m := range members {
			assert.Equal(t, adapter.RouteLink, adapter.Route(a, game, m))
		}
	}
}

func TestGenericIdentity(t *testing.T) {
	a := adapter.Generic{}
	assert.Equal(t, adapter.GenericID, a.ID())
	assert.NotEmpty(t, a.Label())
}
