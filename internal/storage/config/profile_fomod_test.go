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

// #634: a mod whose files each carry an installer records its choices per
// file, as a nested `fomod_files:` block keyed by file id. The block is read
// and written exactly as `fomod:` is: in place, without touching the rest.

const withFomodFiles = `name: default
game_id: tg
mods:
  - source_id: nexusmods
    mod_id: "300"
    file_ids: ["10", "11"]
    fomod_files:
      "10":   # the main file
        config_sha256: ab12
        steps:
          - name: Main Files
            groups:
              - name: Texture Resolution
                choices:
                  - name: 2K
                    idx: 1
      "11":
        config_sha256: cd34
        steps: []
  - source_id: nexusmods
    mod_id: "200"
`

func samplePerFile() domain.FomodFiles {
	return domain.FomodFiles{"10": sampleChoices(), "11": {ConfigSHA256: "cd34", Steps: []fomod.StepChoice{}}}
}

func TestLoadProfile_ReadsFomodFilesBlock(t *testing.T) {
	dir := t.TempDir()
	writeProfileFile(t, dir, "tg", "default", withFomodFiles)
	profile, err := config.LoadProfile(dir, "tg", "default")
	require.NoError(t, err)
	require.Len(t, profile.Mods, 2)
	assert.True(t, samplePerFile().Equal(profile.Mods[0].FomodFiles), "%+v", profile.Mods[0].FomodFiles)
	assert.True(t, profile.Mods[0].Fomod.IsZero())
	assert.Nil(t, profile.Mods[1].FomodFiles)
}

// TestSaveProfile_KeepsAFomodFilesBlockThroughOtherEdits: editing the
// reference, or its neighbour, leaves the block - comments included - as
// the author wrote it.
func TestSaveProfile_KeepsAFomodFilesBlockThroughOtherEdits(t *testing.T) {
	dir := t.TempDir()
	path := writeProfileFile(t, dir, "tg", "default", withFomodFiles)
	profile, err := config.LoadProfile(dir, "tg", "default")
	require.NoError(t, err)
	profile.Mods[0].Locked = true
	profile.Mods[1].Disabled = true
	require.NoError(t, config.SaveProfile(dir, profile))
	text := readFile(t, path)
	assert.Contains(t, text, "    file_ids: [\"10\", \"11\"]\n    locked: true\n    fomod_files:\n      \"10\":   # the main file\n")
	assert.True(t, strings.HasSuffix(text, "    mod_id: \"200\"\n    disabled: true\n"), text)

	reloaded, err := config.LoadProfile(dir, "tg", "default")
	require.NoError(t, err)
	assert.True(t, samplePerFile().Equal(reloaded.Mods[0].FomodFiles))
}

// TestSaveProfile_FomodFilesIsEditedInPlace: recording, changing and
// clearing the per-file block touches only it, and switching a mod between
// the two forms swaps one block for the other.
func TestSaveProfile_FomodFilesIsEditedInPlace(t *testing.T) {
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
			assert.True(t, profile.Mods[i].FomodRecord().Equal(reloaded.Mods[i].FomodRecord()), "ref %d", i)
		}
		return reloaded
	}

	save(func(p *domain.Profile) { byID(p, "beta").FomodFiles = samplePerFile() })
	text := readFile(t, path)
	assert.Contains(t, text, "    mod_id: beta   # the one I care about\n    version: \"1.0\"\n    fomod_files:\n      \"10\":\n          config_sha256: ab12\n")
	assert.Contains(t, text, "# My carefully curated profile\n")

	// To the single form and back: one block replaces the other.
	save(func(p *domain.Profile) { byID(p, "beta").SetFomodRecord(domain.FomodRecord{Choices: sampleChoices()}) })
	text = readFile(t, path)
	assert.NotContains(t, text, "fomod_files")
	assert.Contains(t, text, "    fomod:\n      config_sha256: ab12\n")
	save(func(p *domain.Profile) { byID(p, "beta").SetFomodRecord(domain.FomodRecord{Files: samplePerFile()}) })
	text = readFile(t, path)
	assert.NotContains(t, text, "    fomod:\n")
	assert.Contains(t, text, "fomod_files:")
	assert.Contains(t, text, "# My carefully curated profile\n", "the swap is an in-place edit, not a whole write")

	save(func(p *domain.Profile) { byID(p, "beta").FomodFiles = nil })
	assert.Equal(t, handEdited, readFile(t, path), "clearing the choices removes exactly the block")

	// A flow reference cannot hold a block: the save still lands, whole.
	reloaded := save(func(p *domain.Profile) { byID(p, "alpha").FomodFiles = samplePerFile() })
	assert.True(t, samplePerFile().Equal(byID(reloaded, "alpha").FomodFiles))
}

// TestMarkModsDisabled_AfterAFomodFilesBlock: the marker lands after the
// nested block, at the reference's own indentation.
func TestMarkModsDisabled_AfterAFomodFilesBlock(t *testing.T) {
	dir := t.TempDir()
	path := writeProfileFile(t, dir, "tg", "default", strings.TrimSuffix(withFomodFiles, "  - source_id: nexusmods\n    mod_id: \"200\"\n"))
	marked, err := config.MarkModsDisabled(path, []domain.ModReference{{SourceID: "nexusmods", ModID: "300"}})
	require.NoError(t, err)
	require.Len(t, marked, 1)
	assert.True(t, strings.HasSuffix(readFile(t, path), "        steps: []\n    disabled: true\n"), readFile(t, path))
	profile, err := config.LoadProfile(dir, "tg", "default")
	require.NoError(t, err)
	assert.True(t, profile.Mods[0].Disabled)
	assert.True(t, samplePerFile().Equal(profile.Mods[0].FomodFiles))
}

// TestRenameProfileModRef_CarriesFomodFiles: a re-link keeps the per-file
// choices.
func TestRenameProfileModRef_CarriesFomodFiles(t *testing.T) {
	dir := t.TempDir()
	writeProfileFile(t, dir, "tg", "default", withFomodFiles)
	_, err := config.RenameProfileModRef(dir, "tg", "default", config.ModRefRename{
		FromSourceID: "nexusmods", FromModID: "300", ToSourceID: "nexusmods", ToModID: "301",
	})
	require.NoError(t, err)
	profile, err := config.LoadProfile(dir, "tg", "default")
	require.NoError(t, err)
	ref := profile.FindRef("nexusmods", "301")
	require.NotNil(t, ref)
	assert.True(t, samplePerFile().Equal(ref.FomodFiles))
}

// TestExportProfile_CarriesFomodFiles: a version-0 export and its import
// keep the per-file choices; a profile without them exports as before.
func TestExportProfile_CarriesFomodFiles(t *testing.T) {
	profile := &domain.Profile{Name: "p", GameID: "tg", Mods: []domain.ModReference{{SourceID: "nexusmods", ModID: "300", FomodFiles: samplePerFile()}}}
	data, err := config.ExportProfile(profile)
	require.NoError(t, err)
	assert.Contains(t, string(data), "fomod_files:")
	back, err := config.ImportProfile(data)
	require.NoError(t, err)
	assert.True(t, samplePerFile().Equal(back.Mods[0].FomodFiles))
}

// TestSaveProfile_FomodOrderIsEditedInPlace (#634): the per-file record's
// layer-order hint is a list beside the block, written and cleared in
// place, as file_ids is.
func TestSaveProfile_FomodOrderIsEditedInPlace(t *testing.T) {
	dir := t.TempDir()
	path := writeProfileFile(t, dir, "tg", "default", withFomodFiles)
	set := func(order []string) *domain.Profile {
		t.Helper()
		profile, err := config.LoadProfile(dir, "tg", "default")
		require.NoError(t, err)
		profile.Mods[0].FomodOrder = order
		require.NoError(t, config.SaveProfile(dir, profile))
		reloaded, err := config.LoadProfile(dir, "tg", "default")
		require.NoError(t, err)
		return reloaded
	}
	reloaded := set([]string{"11", "10"})
	assert.Equal(t, []string{"11", "10"}, reloaded.Mods[0].FomodOrder)
	text := readFile(t, path)
	assert.Contains(t, text, "      \"10\":   # the main file\n", "the rest is as written")
	assert.Contains(t, text, "    fomod_order:\n      - \"11\"\n      - \"10\"\n")
	reloaded = set(nil)
	assert.Nil(t, reloaded.Mods[0].FomodOrder)
	assert.Equal(t, withFomodFiles, readFile(t, path), "clearing it removes exactly the list")
}
