package config_test

// #614: a mod reference's recorded FOMOD choices live in the profile
// document as a nested `fomod:` block. Absent means none recorded, so every
// file written before the key existed is unchanged; an in-place save edits
// only that block, and every other edit leaves it exactly as written.

import (
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/fomod"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const withFomod = `name: default
game_id: tg
mods:
  - source_id: nexusmods
    mod_id: "12604"
    version: "1.0"
    fomod:
      config_sha256: ab12   # the installer they were made against
      steps:
        - name: Main Files
          groups:
            - name: Texture Resolution
              choices:
                - name: 2K
                  idx: 1
  - source_id: nexusmods
    mod_id: "200"
`

func sampleChoices() domain.FomodChoices {
	return domain.FomodChoices{ConfigSHA256: "ab12", Steps: []fomod.StepChoice{{
		Name:   "Main Files",
		Groups: []fomod.GroupChoice{{Name: "Texture Resolution", Choices: []fomod.OptionChoice{{Name: "2K", Idx: 1}}}},
	}}}
}

func TestLoadProfile_ReadsFomodBlock(t *testing.T) {
	dir := t.TempDir()
	writeProfileFile(t, dir, "tg", "default", withFomod)
	profile, err := config.LoadProfile(dir, "tg", "default")
	require.NoError(t, err)
	require.Len(t, profile.Mods, 2)
	assert.True(t, sampleChoices().Equal(profile.Mods[0].Fomod), "%+v", profile.Mods[0].Fomod)
	assert.True(t, profile.Mods[1].Fomod.IsZero())
}

// TestSaveProfile_KeepsAFomodBlockThroughOtherEdits: editing a reference
// that carries choices, or its neighbour, moves only the edited text.
func TestSaveProfile_KeepsAFomodBlockThroughOtherEdits(t *testing.T) {
	dir := t.TempDir()
	path := writeProfileFile(t, dir, "tg", "default", withFomod)

	profile, err := config.LoadProfile(dir, "tg", "default")
	require.NoError(t, err)
	profile.Mods[0].Version = "1.1"
	profile.Mods[0].Locked = true
	profile.Mods[1].Disabled = true
	require.NoError(t, config.SaveProfile(dir, profile))

	want := `name: default
game_id: tg
mods:
  - source_id: nexusmods
    mod_id: "12604"
    version: "1.1"
    locked: true
    fomod:
      config_sha256: ab12   # the installer they were made against
      steps:
        - name: Main Files
          groups:
            - name: Texture Resolution
              choices:
                - name: 2K
                  idx: 1
  - source_id: nexusmods
    mod_id: "200"
    disabled: true
`
	assert.Equal(t, want, readFile(t, path))

	reloaded, err := config.LoadProfile(dir, "tg", "default")
	require.NoError(t, err)
	assert.True(t, sampleChoices().Equal(reloaded.Mods[0].Fomod))
}

// TestSaveProfile_FomodIsEditedInPlace: recording, changing and clearing
// choices touches only the block.
func TestSaveProfile_FomodIsEditedInPlace(t *testing.T) {
	dir := t.TempDir()
	path := writeProfileFile(t, dir, "tg", "default", handEdited)
	byID := func(p *domain.Profile, id string) *domain.ModReference { return p.FindRef("local", id) }

	save := func(change func(p *domain.Profile)) *domain.Profile {
		t.Helper()
		profile, err := config.LoadProfile(dir, "tg", "default")
		require.NoError(t, err)
		change(profile)
		require.NoError(t, config.SaveProfile(dir, profile))
		reloaded, err := config.LoadProfile(dir, "tg", "default")
		require.NoError(t, err)
		for i := range profile.Mods {
			assert.True(t, profile.Mods[i].Fomod.Equal(reloaded.Mods[i].Fomod), "ref %d", i)
		}
		return reloaded
	}

	save(func(p *domain.Profile) { byID(p, "beta").Fomod = sampleChoices() })
	text := readFile(t, path)
	// Formatted as a whole write formats it (yaml.Marshal's indentation),
	// under the reference's own keys.
	assert.Contains(t, text, "    mod_id: beta   # the one I care about\n    version: \"1.0\"\n    fomod:\n      config_sha256: ab12\n      steps:\n          - name: Main Files\n")
	assert.Contains(t, text, "# My carefully curated profile\n", "the rest of the file is the author's")
	assert.Contains(t, text, "  - {source_id: local, mod_id: alpha, version: \"1.0\"}\n")

	changed := sampleChoices()
	changed.Steps[0].Groups[0].Choices[0] = fomod.OptionChoice{Name: "4K", Idx: 2}
	save(func(p *domain.Profile) { byID(p, "beta").Fomod = changed })
	assert.Contains(t, readFile(t, path), "- name: 4K\n")
	assert.NotContains(t, readFile(t, path), "2K")

	save(func(p *domain.Profile) { byID(p, "beta").Fomod = domain.FomodChoices{} })
	assert.Equal(t, handEdited, readFile(t, path), "clearing the choices removes exactly the block")

	// A flow reference cannot hold a block: the save still lands, whole.
	reloaded := save(func(p *domain.Profile) { byID(p, "alpha").Fomod = sampleChoices() })
	assert.True(t, sampleChoices().Equal(byID(reloaded, "alpha").Fomod))
}

// TestMarkModsDisabled_AfterAFomodBlock: the marker lands after the nested
// block, at the reference's own indentation.
func TestMarkModsDisabled_AfterAFomodBlock(t *testing.T) {
	dir := t.TempDir()
	path := writeProfileFile(t, dir, "tg", "default", strings.TrimSuffix(withFomod, "  - source_id: nexusmods\n    mod_id: \"200\"\n"))
	marked, err := config.MarkModsDisabled(path, []domain.ModReference{{SourceID: "nexusmods", ModID: "12604"}})
	require.NoError(t, err)
	require.Len(t, marked, 1)
	assert.True(t, strings.HasSuffix(readFile(t, path), "                  idx: 1\n    disabled: true\n"), readFile(t, path))
	profile, err := config.LoadProfile(dir, "tg", "default")
	require.NoError(t, err)
	assert.True(t, profile.Mods[0].Disabled)
	assert.True(t, sampleChoices().Equal(profile.Mods[0].Fomod))
}

// TestRenameProfileModRef_CarriesFomod: a re-link keeps the reference's
// recorded choices with everything else it carries (#593).
func TestRenameProfileModRef_CarriesFomod(t *testing.T) {
	dir := t.TempDir()
	writeProfileFile(t, dir, "tg", "default", withFomod)
	_, err := config.RenameProfileModRef(dir, "tg", "default", config.ModRefRename{
		FromSourceID: "nexusmods", FromModID: "12604", ToSourceID: "nexusmods", ToModID: "99999", Version: "1.0",
	})
	require.NoError(t, err)
	profile, err := config.LoadProfile(dir, "tg", "default")
	require.NoError(t, err)
	ref := profile.FindRef("nexusmods", "99999")
	require.NotNil(t, ref)
	assert.True(t, sampleChoices().Equal(ref.Fomod))
}
