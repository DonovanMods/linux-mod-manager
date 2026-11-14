package core

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/fomod"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #639: the file-set variant and the rules that read it.

func TestFileSetVariant_IsASetFunctionOfItsBase(t *testing.T) {
	v := fileSetVariant("", []string{"20", "21"})
	assert.Len(t, v, 12)
	assert.Equal(t, v, fileSetVariant("", []string{"21", "20", "21"}), "order and repeats never matter")
	assert.NotEqual(t, v, fileSetVariant("", []string{"20"}))
	assert.NotEqual(t, v, fileSetVariant("abcdef123456", []string{"20", "21"}), "a FOMOD tree's file-set variant is its own")
}

func TestFileSetDrift_NeedsBothSidesToRecordFiles(t *testing.T) {
	row := &domain.InstalledMod{FileIDs: []string{"20"}}
	assert.True(t, fileSetDrift(domain.ModReference{FileIDs: []string{"21"}}, row))
	assert.False(t, fileSetDrift(domain.ModReference{FileIDs: []string{"20"}}, row))
	assert.False(t, fileSetDrift(domain.ModReference{}, row), "an unpinned ref")
	assert.False(t, fileSetDrift(domain.ModReference{FileIDs: []string{"21"}}, &domain.InstalledMod{}), "a row from before file ids")
	assert.False(t, fileSetDrift(domain.ModReference{FileIDs: []string{"21"}}, nil))
}

func TestFileSetKey_MovesOnlyAnotherSet(t *testing.T) {
	c := cache.New(t.TempDir())
	k := cache.Key{GameID: "g", SourceID: "s", ModID: "m", Version: "1.0"}
	assert.Equal(t, k, fileSetKey(c, k, []string{"20"}), "no entry: the plain key")
	require.NoError(t, c.Store(k, "a.esp", []byte("a")))
	assert.Equal(t, k, fileSetKey(c, k, []string{"20"}), "a markerless (legacy) entry holds no other set")
	require.NoError(t, cache.MarkFileComplete(c.ModPath(k), "20"))
	assert.Equal(t, k, fileSetKey(c, k, []string{"20"}), "the set the entry holds keeps its key")
	assert.Equal(t, k, fileSetKey(c, k, []string{"20", "21"}), "a superset grows the entry")
	moved := fileSetKey(c, k, []string{"21"})
	assert.Equal(t, fileSetVariant("", []string{"21"}), moved.Variant, "another set moves")
}

func TestRowFitsRef_AcceptsTheFileSetVariant(t *testing.T) {
	choices := domain.FomodChoices{ConfigSHA256: "cfg", Steps: []fomod.StepChoice{}}
	ref := domain.ModReference{FileIDs: []string{"21"}, Fomod: choices}
	base := refVariant(&ref)
	require.NotEmpty(t, base)
	assert.True(t, rowFitsRef(&domain.InstalledMod{Mod: domain.Mod{Variant: base}}, &ref))
	assert.True(t, rowFitsRef(&domain.InstalledMod{Mod: domain.Mod{Variant: fileSetVariant(base, []string{"21"})}}, &ref))
	assert.False(t, rowFitsRef(&domain.InstalledMod{Mod: domain.Mod{Variant: fileSetVariant("", []string{"21"})}}, &ref), "a plain tree is not the choices'")

	// A ref that pins no files fits the file-set variant of the row's own,
	// so a converge does not reinstall it forever.
	unpinned := domain.ModReference{Fomod: choices}
	row := &domain.InstalledMod{Mod: domain.Mod{Variant: fileSetVariant(base, []string{"21"})}, FileIDs: []string{"21"}}
	assert.True(t, rowFitsRef(row, &unpinned))
	assert.False(t, fomodDrift(unpinned, row))
}

func TestFomodTreeVariant_IgnoresAPlainFileSetVariant(t *testing.T) {
	plain := &domain.InstalledMod{Mod: domain.Mod{Variant: fileSetVariant("", []string{"21"})}, FileIDs: []string{"21"}}
	assert.Empty(t, fomodTreeVariant(plain))
	assert.True(t, inFileSetEntry(plain, nil))
	fomodRow := &domain.InstalledMod{Mod: domain.Mod{Variant: "abcdef123456"}, FileIDs: []string{"21"}}
	assert.Equal(t, "abcdef123456", fomodTreeVariant(fomodRow))
	assert.False(t, inFileSetEntry(fomodRow, nil))
}
