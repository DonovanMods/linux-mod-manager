package core

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/fomod"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResolveFomod_APinWithNoArchiveSumAsks (#609 review): a recording
// pinned to an md5 whose archive lmm has no sum for is never replayed
// unchecked: it is asked about, the archive's md5 left empty.
func TestResolveFomod_APinWithNoArchiveSumAsks(t *testing.T) {
	tree := fomodTree(t, "", testModuleConfig, nil)
	_, err := (&Service{}).resolveFomod(fomodTestMod, "mod.zip", tree, fomodInput{choices: pick("2K"), expectMD5: "0123456789abcdef0123456789abcdef"})
	var q *FomodChoicesRequiredError
	require.ErrorAs(t, err, &q)
	assert.Equal(t, FomodReasonArchiveMismatch, q.Mods[0].Reason)
	assert.Equal(t, &FomodArchiveMismatch{ExpectedMD5: "0123456789abcdef0123456789abcdef"}, q.Mods[0].ArchiveMismatch)
}

// TestResolveFomod_TheRecipePinSkipsAnInstalledRecording: the recipe's
// md5 pins only a curator's recording lmm never installed - choices with a
// config hash are never checked against it, and the recipe is not read.
func TestResolveFomod_TheRecipePinSkipsAnInstalledRecording(t *testing.T) {
	read := 0
	lazy := func() (string, error) { read++; return "0123456789abcdef0123456789abcdef", nil }
	installed := pick("2K")
	installed.ConfigSHA256 = "ab"
	_, err := (&Service{}).resolveFomod(fomodTestMod, "mod.zip", fomodTree(t, "", testModuleConfig, nil), fomodInput{choices: installed, lazyMD5: lazy})
	require.NoError(t, err, "replayed, not pinned")
	assert.Zero(t, read, "the recipe is not read for an installed recording")
}

// TestPluginFileStates_UnlistedPluginsAreActive (#618 review): a plugin
// the game strips from its list and loads itself (Starfield's
// BlueprintShips-*) is active whenever it is present.
func TestPluginFileStates_UnlistedPluginsAreActive(t *testing.T) {
	p := &pluginFileStates{
		spec:  adapter.PluginSpec{Extensions: []string{".esm", ".esp"}, UnlistedPrefixes: []string{"BlueprintShips-"}},
		order: func() domain.ProfilePlugins { return domain.ProfilePlugins{} },
		game:  func() (map[string]bool, bool) { return map[string]bool{}, true },
	}
	assert.Equal(t, fomod.FileActive, p.state("blueprintships-starfield.esm", false))
	assert.Equal(t, fomod.FileInactive, p.state("other.esm", false), "a listed-kind plugin the list leaves off is inactive")
}
