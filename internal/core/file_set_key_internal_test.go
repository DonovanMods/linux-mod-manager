package core

import (
	"os"
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

func TestFileSetEntry_ReusesTheBaseEntryOnlyForExactlyItsSet(t *testing.T) {
	svc := newBareService(t)
	ctx := t.Context()
	c := cache.New(t.TempDir())
	k := cache.Key{GameID: "g", SourceID: "s", ModID: "m", Version: "1.0"}
	variantOf := func(ids ...string) cache.Key {
		v := k
		v.Variant = fileSetVariant("", ids)
		return v
	}
	got, warm := svc.fileSetEntry(ctx, c, k, []string{"20"}, false)
	assert.Equal(t, k, got, "no entry: the base key")
	assert.False(t, warm)

	require.NoError(t, c.Store(k, "a.esp", []byte("a")))
	require.NoError(t, cache.MarkFileComplete(c.ModPath(k), "20"))
	got, warm = svc.fileSetEntry(ctx, c, k, []string{"20"}, false)
	assert.Equal(t, k, got, "the set the entry holds keeps its key")
	assert.True(t, warm)
	got, _ = svc.fileSetEntry(ctx, c, k, []string{"20", "21"}, false)
	assert.Equal(t, variantOf("20", "21"), got, "a superset never grows another set's entry")
	got, _ = svc.fileSetEntry(ctx, c, k, []string{"21"}, false)
	assert.Equal(t, variantOf("21"), got, "another set moves")
	got, _ = svc.fileSetEntry(ctx, c, k, nil, false)
	assert.Equal(t, k, got, "no files named: the base key")
}

// An entry from before markers names no set: it is reused only for the set
// an installed row on it pins exactly, and a committing flow backfills its
// markers.
func TestFileSetEntry_UnmarkedEntryIsReusedOnlyForAPinningRow(t *testing.T) {
	svc := newBareService(t)
	ctx := t.Context()
	c := cache.New(t.TempDir())
	k := cache.Key{GameID: "g", SourceID: "s", ModID: "m", Version: "1.0"}
	require.NoError(t, os.MkdirAll(c.ModPath(k), 0o755))
	got, _ := svc.fileSetEntry(ctx, c, k, []string{"20"}, true)
	assert.Equal(t, k, got, "an empty, unmarked directory is no tree: the base key")
	require.NoError(t, c.Store(k, "a.esp", []byte("a")))

	got, warm := svc.fileSetEntry(ctx, c, k, []string{"20"}, true)
	assert.Equal(t, fileSetVariant("", []string{"20"}), got.Variant, "no row pins it: a variant")
	assert.False(t, warm)

	require.NoError(t, svc.db.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod: domain.Mod{ID: "m", SourceID: "s", Name: "M", Version: "1.0", GameID: "g"}, ProfileName: "p",
		UpdatePolicy: domain.UpdateNotify, FileIDs: []string{"20"},
	}))
	got, _ = svc.fileSetEntry(ctx, c, k, []string{"21"}, true)
	assert.Equal(t, fileSetVariant("", []string{"21"}), got.Variant, "the row pins another set")
	manifests, err := c.FileManifests(k)
	require.NoError(t, err)
	assert.Empty(t, manifests, "nothing backfilled for another set")

	got, warm = svc.fileSetEntry(ctx, c, k, []string{"20"}, false)
	assert.Equal(t, k, got, "the row's own set reuses the entry")
	assert.True(t, warm)
	manifests, err = c.FileManifests(k)
	require.NoError(t, err)
	assert.Empty(t, manifests, "a plan never writes")

	_, _ = svc.fileSetEntry(ctx, c, k, []string{"20"}, true)
	assert.True(t, c.HasFileIDs(k, []string{"20"}), "a committing flow backfills the markers")
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
