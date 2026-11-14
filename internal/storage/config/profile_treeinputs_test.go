package config_test

// #610: a mod reference's exact file lists and binary patches live in the
// profile document as `file_list:` and `patches:`, keyed by file id. Absent
// means none, so every file written before the keys existed is unchanged,
// and an in-place save adds or removes only those blocks.

import (
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sampleFileList() domain.FileLists {
	return domain.FileLists{"10": {{Path: `r6\scripts\a.reds`, MD5: "25aa7b0b0c4d9b7f4f3f6e5a1c2b3d4e"}, {Path: "r6/tweaks/b.yaml", MD5: "0123456789abcdef0123456789abcdef"}}}
}

func samplePatches() domain.FilePatches {
	return domain.FilePatches{"10": {{Path: `[CP] Mod\content.json`, CRC32: "CABDDFC4", SHA256: strings.Repeat("ab", 32)}}}
}

const withTreeInputs = `name: default
game_id: tg
mods:
  - source_id: nexusmods
    mod_id: "300"
    file_ids: ["10"]
    file_list:
      "10":   # replicate mode
        - path: r6\scripts\a.reds
          md5: 25aa7b0b0c4d9b7f4f3f6e5a1c2b3d4e
        - {path: r6/tweaks/b.yaml, md5: 0123456789abcdef0123456789abcdef}
    patches:
      "10":
        - path: '[CP] Mod\content.json'
          crc32: CABDDFC4
          sha256: abababababababababababababababababababababababababababababababab
  - source_id: nexusmods
    mod_id: "200"
`

func TestLoadProfile_ReadsFileListAndPatches(t *testing.T) {
	dir := t.TempDir()
	writeProfileFile(t, dir, "tg", "default", withTreeInputs)
	profile, err := config.LoadProfile(dir, "tg", "default")
	require.NoError(t, err)
	require.Len(t, profile.Mods, 2)
	assert.True(t, sampleFileList().Equal(profile.Mods[0].FileList), "%+v", profile.Mods[0].FileList)
	assert.True(t, samplePatches().Equal(profile.Mods[0].Patches), "%+v", profile.Mods[0].Patches)
	assert.Nil(t, profile.Mods[1].FileList)
	assert.Nil(t, profile.Mods[1].Patches)
}

// TestSaveProfile_KeepsTreeInputBlocksThroughOtherEdits: editing the
// reference, or its neighbour, leaves the blocks - comments included - as
// written.
func TestSaveProfile_KeepsTreeInputBlocksThroughOtherEdits(t *testing.T) {
	dir := t.TempDir()
	path := writeProfileFile(t, dir, "tg", "default", withTreeInputs)
	profile, err := config.LoadProfile(dir, "tg", "default")
	require.NoError(t, err)
	profile.Mods[0].Locked = true
	profile.Mods[1].Disabled = true
	require.NoError(t, config.SaveProfile(dir, profile))
	text := readFile(t, path)
	assert.Contains(t, text, "    file_ids: [\"10\"]\n    locked: true\n    file_list:\n      \"10\":   # replicate mode\n")
	assert.True(t, strings.HasSuffix(text, "    mod_id: \"200\"\n    disabled: true\n"), text)

	reloaded, err := config.LoadProfile(dir, "tg", "default")
	require.NoError(t, err)
	assert.True(t, sampleFileList().Equal(reloaded.Mods[0].FileList))
	assert.True(t, samplePatches().Equal(reloaded.Mods[0].Patches))
}

// TestSaveProfile_TreeInputsAreEditedInPlace: recording and clearing either
// block touches only it.
func TestSaveProfile_TreeInputsAreEditedInPlace(t *testing.T) {
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
			assert.True(t, profile.Mods[i].FileList.Equal(reloaded.Mods[i].FileList), "ref %d", i)
			assert.True(t, profile.Mods[i].Patches.Equal(reloaded.Mods[i].Patches), "ref %d", i)
		}
		return reloaded
	}

	save(func(p *domain.Profile) { byID(p, "beta").FileList = sampleFileList() })
	text := readFile(t, path)
	assert.Contains(t, text, "    mod_id: beta   # the one I care about\n    version: \"1.0\"\n    file_list:\n      \"10\":\n")
	assert.Contains(t, text, "# My carefully curated profile\n")
	save(func(p *domain.Profile) { byID(p, "beta").Patches = samplePatches() })
	text = readFile(t, path)
	assert.Contains(t, text, "    patches:\n      \"10\":\n")
	assert.Less(t, strings.Index(text, "file_list:"), strings.Index(text, "patches:"), "the keys keep their canonical order")

	save(func(p *domain.Profile) { byID(p, "beta").FileList = nil })
	save(func(p *domain.Profile) { byID(p, "beta").Patches = nil })
	assert.Equal(t, handEdited, readFile(t, path), "clearing both removes exactly the blocks")

	// A flow reference cannot hold a block: the save still lands, whole.
	reloaded := save(func(p *domain.Profile) { byID(p, "alpha").Patches = samplePatches() })
	assert.True(t, samplePatches().Equal(byID(reloaded, "alpha").Patches))
}

// TestRenameProfileModRef_CarriesTreeInputs: a re-link keeps both.
func TestRenameProfileModRef_CarriesTreeInputs(t *testing.T) {
	dir := t.TempDir()
	writeProfileFile(t, dir, "tg", "default", withTreeInputs)
	_, err := config.RenameProfileModRef(dir, "tg", "default", config.ModRefRename{
		FromSourceID: "nexusmods", FromModID: "300", ToSourceID: "nexusmods", ToModID: "301",
	})
	require.NoError(t, err)
	profile, err := config.LoadProfile(dir, "tg", "default")
	require.NoError(t, err)
	ref := profile.FindRef("nexusmods", "301")
	require.NotNil(t, ref)
	assert.True(t, sampleFileList().Equal(ref.FileList))
	assert.True(t, samplePatches().Equal(ref.Patches))
}

// TestExportProfile_AndCollectionCarryTreeInputs: a version-0 export, its
// import, and a version-1 collection document keep both.
func TestExportProfile_AndCollectionCarryTreeInputs(t *testing.T) {
	ref := domain.ModReference{SourceID: "nexusmods", ModID: "300", FileIDs: []string{"10"}, FileList: sampleFileList(), Patches: samplePatches()}
	data, err := config.ExportProfile(&domain.Profile{Name: "p", GameID: "tg", Mods: []domain.ModReference{ref}})
	require.NoError(t, err)
	back, err := config.ImportProfile(data)
	require.NoError(t, err)
	assert.True(t, sampleFileList().Equal(back.Mods[0].FileList))
	assert.True(t, samplePatches().Equal(back.Mods[0].Patches))

	doc := &domain.CollectionDocument{FormatVersion: 1, Name: "c", Items: []domain.CollectionItem{{Ref: ref, Name: "M",
		Files: []domain.CollectionFileRef{{ID: "10", MD5: "ab"}}}}}
	data, err = config.MarshalCollection(doc)
	require.NoError(t, err)
	parsed, err := config.ParseCollection(data)
	require.NoError(t, err)
	assert.True(t, sampleFileList().Equal(parsed.Items[0].Ref.FileList))
	assert.True(t, samplePatches().Equal(parsed.Items[0].Ref.Patches))
	assert.Equal(t, []domain.CollectionFileRef{{ID: "10", MD5: "ab"}}, parsed.Items[0].Files, "the document's files: is its own key")
}

// TestMarkModsDisabled_AfterTreeInputBlocks: the marker lands after the
// nested blocks, at the reference's own indentation.
func TestMarkModsDisabled_AfterTreeInputBlocks(t *testing.T) {
	dir := t.TempDir()
	path := writeProfileFile(t, dir, "tg", "default", strings.TrimSuffix(withTreeInputs, "  - source_id: nexusmods\n    mod_id: \"200\"\n"))
	marked, err := config.MarkModsDisabled(path, []domain.ModReference{{SourceID: "nexusmods", ModID: "300"}})
	require.NoError(t, err)
	require.Len(t, marked, 1)
	assert.True(t, strings.HasSuffix(readFile(t, path), "abababababababababab\n    disabled: true\n"), readFile(t, path))
	profile, err := config.LoadProfile(dir, "tg", "default")
	require.NoError(t, err)
	assert.True(t, profile.Mods[0].Disabled)
	assert.True(t, samplePatches().Equal(profile.Mods[0].Patches))
}
