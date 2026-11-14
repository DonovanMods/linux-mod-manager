package core_test

// #609: a collection's recorded FOMOD choices are replayed only against the
// archive they were recorded against. The document's per-file md5 (the
// recipe's source.md5, which C2's converter carries into an item's files)
// is checked before the replay: a different archive is asked about, with
// the reason, rather than installed with choices made for another one.

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// md5Collection is an lmm collection document pinning mod1's file with the
// curator's 4K choice and, when sum is set, the file's md5.
func md5Collection(sum string) []byte {
	files := ""
	if sum != "" {
		files = fmt.Sprintf("    files: [{id: mod1, md5: %q}]\n", sum)
	}
	return []byte("lmm_collection: 1\nname: col\ngame_id: g1\nrequires: [fomod]\nmods:\n" +
		"  - source_id: src\n    mod_id: mod1\n    version: \"1.0\"\n    file_ids: [mod1]\n" +
		"    fomod: {steps: [{name: Main, groups: [{name: Texture, choices: [{name: 4K}]}]}]}\n" + files)
}

// newMD5Fixture serves mod1's installer and returns the fixture and the
// archive's md5.
func newMD5Fixture(t *testing.T) (*fomodFixture, string) {
	t.Helper()
	f := newFomodFixture(t)
	require.NoError(t, f.svc.SaveGame(context.Background(), f.game))
	archive := fomodZip(t, "4K", "a")
	f.mock.AddDownload("mod1", archive)
	f.mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod mod1", Version: "1.0", GameID: "g1"})
	sum := md5.Sum(archive)
	return f, hex.EncodeToString(sum[:])
}

func (f *fomodFixture) importCollection(t *testing.T, data []byte, answers map[string]domain.FomodChoices) error {
	t.Helper()
	ctx := context.Background()
	plan, err := f.svc.PlanCollectionImport(ctx, core.CollectionImportRequest{Data: data, GameID: "g1"})
	require.NoError(t, err)
	_, err = f.svc.ApplyCollectionImport(ctx, f.game, plan, core.ProfileImportOptions{Install: true, Fomod: answers}, nil)
	return err
}

// TestCollectionFomod_AMatchingArchiveReplaysTheChoices: the archive the
// curator recorded installs with the recorded choices, unasked - and so
// does a document that records no md5 at all, as before #609.
func TestCollectionFomod_AMatchingArchiveReplaysTheChoices(t *testing.T) {
	for name, sum := range map[string]func(actual string) string{
		"the same md5":          func(actual string) string { return actual },
		"the md5 in upper case": func(actual string) string { return strings.ToUpper(actual) },
		"no md5":                func(string) string { return "" },
	} {
		t.Run(name, func(t *testing.T) {
			f, actual := newMD5Fixture(t)
			require.NoError(t, f.importCollection(t, md5Collection(sum(actual)), nil))
			ref := f.ref(t, "col", "mod1")
			require.NotNil(t, ref)
			assert.Equal(t, "4K", ref.Fomod.Steps[0].Groups[0].Choices[0].Name)
			assert.Equal(t, "4K-a", f.deployed(t, "textures/t.dds"))
		})
	}
}

// TestCollectionFomod_ADifferentArchiveIsAskedAboutWithTheReason: an
// archive whose md5 is not the recorded one is never replayed. The import
// asks - the curator's choices preselected, the reason and both sums on the
// question - with nothing written; the answered re-run installs the answer
// from the kept download.
func TestCollectionFomod_ADifferentArchiveIsAskedAboutWithTheReason(t *testing.T) {
	f, actual := newMD5Fixture(t)
	ctx := context.Background()
	recorded := strings.Repeat("0", 32)

	err := f.importCollection(t, md5Collection(recorded), nil)
	var q *core.FomodChoicesRequiredError
	require.True(t, errors.As(err, &q), "got %v", err)
	require.Len(t, q.Mods, 1)
	m := q.Mods[0]
	assert.Equal(t, core.FomodReasonArchiveMismatch, m.Reason)
	require.NotNil(t, m.ArchiveMismatch)
	assert.Equal(t, recorded, m.ArchiveMismatch.ExpectedMD5)
	assert.Equal(t, actual, m.ArchiveMismatch.ActualMD5)
	assert.Equal(t, "4K", m.Choices[0].Groups[0].Choices[0].Name, "the curator's choices are the preselection")
	assert.Contains(t, err.Error(), "not the archive")
	_, err = f.svc.NewProfileManager().Get(ctx, "g1", "col")
	assert.Error(t, err, "nothing was written, the profile included")
	assert.Empty(t, f.deployed(t, "textures/t.dds"))

	require.NoError(t, f.importCollection(t, md5Collection(recorded), map[string]domain.FomodChoices{"src:mod1": answer("2K")}))
	assert.Equal(t, "2K-a", f.deployed(t, "textures/t.dds"))
	ref := f.ref(t, "col", "mod1")
	assert.Equal(t, "2K", ref.Fomod.Steps[0].Groups[0].Choices[0].Name, "the answer is what the profile records")
	assert.Equal(t, 1, f.mock.DownloadCount(), "the answered re-run fetched nothing again")
}

// TestCollectionFomod_AMalformedMD5IsIgnored: a stranger's md5 that is not
// 32 hex digits can never match anything, so it is no pin at all: the
// choices replay as they did before #609.
func TestCollectionFomod_AMalformedMD5IsIgnored(t *testing.T) {
	f, _ := newMD5Fixture(t)
	require.NoError(t, f.importCollection(t, md5Collection("not-an-md5"), nil))
	assert.Equal(t, "4K-a", f.deployed(t, "textures/t.dds"))
}

// TestCollectionFomod_TheMD5PinsOnlyAReplay: answers given up front are a
// fresh answer, not the curator's recording, so no md5 is asked about.
func TestCollectionFomod_TheMD5PinsOnlyAReplay(t *testing.T) {
	f, _ := newMD5Fixture(t)
	require.NoError(t, f.importCollection(t, md5Collection(strings.Repeat("0", 32)), map[string]domain.FomodChoices{"src:mod1": answer("4K")}))
	assert.Equal(t, "4K-a", f.deployed(t, "textures/t.dds"))
}

// TestCollectionFomod_TheMD5IsNotSavedOnTheProfile: the pin is the
// document's; the profile records the choices the install made, with the
// installer's own hash, exactly as before.
func TestCollectionFomod_TheMD5IsNotSavedOnTheProfile(t *testing.T) {
	f, actual := newMD5Fixture(t)
	require.NoError(t, f.importCollection(t, md5Collection(actual), nil))
	p, err := config.LoadProfile(f.svc.ConfigDir(), "g1", "col")
	require.NoError(t, err)
	export, err := config.ExportProfile(p)
	require.NoError(t, err)
	assert.NotContains(t, string(export), actual)
}
