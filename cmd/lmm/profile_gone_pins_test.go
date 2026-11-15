package main

// #657: `lmm profile import --nexus-collection` with a pinned file
// NexusMods no longer offers. The plan lists it as needing attention, with
// the mod's current main file; without a choice the import refuses, naming
// the flags; --use-current installs the current file instead, --skip
// leaves the mod out. --json carries the refusal's pins as its details.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// goneTenDouble is the NexusMods double whose list has no record of Ten's
// pinned file 100 (its author removed it), and whose listing offers only
// 101, Ten's main file.
type goneTenDouble struct {
	*nexusCollectionDouble
}

func (d *goneTenDouble) ResolveGameCollection(ctx context.Context, gameSourceID, ref string) (source.Collection, error) {
	c, err := d.nexusCollectionDouble.ResolveGameCollection(ctx, gameSourceID, ref)
	c.Items[0].ModID = ""
	return c, err
}

func (*goneTenDouble) GetModFiles(_ context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	if mod.ID != "10" {
		return nil, nil
	}
	return []domain.DownloadableFile{
		{ID: "101", FileName: "ten-2.zip", Name: "Ten Two", Version: "2.0", Category: "MAIN", IsPrimary: true},
	}, nil
}

func goneTenService(t *testing.T) (*core.Service, *domain.Game, string) {
	t.Helper()
	return nexusCollectionServiceAs(t, nexusDoubleRecipe, "skyrimspecialedition", func(d *nexusCollectionDouble) source.ModSource {
		return &goneTenDouble{nexusCollectionDouble: d}
	})
}

func setGonePinFlags(t *testing.T, f gonePinFlags) {
	t.Helper()
	old := profileImportGonePins
	profileImportGonePins = f
	t.Cleanup(func() { profileImportGonePins = old })
}

func TestDoNexusCollectionImport_AGonePinRefusesWithoutAChoice(t *testing.T) {
	svc, game, _ := goneTenService(t)
	setNexusFlags(t, "abcdef", false)
	stdout, _, err := captureStdoutStderrErr(t, func() error {
		return doNexusCollectionImport(context.Background(), svc, game)
	})
	require.True(t, core.IsCollectionGonePins(err), "got %v", err)
	assert.Contains(t, stdout, "Needs your attention - 1 mod(s) pin a file NexusMods no longer offers")
	assert.Contains(t, stdout, "! Ten (nexusmods:10): the collection pins file 100, version 1.0, which NexusMods no longer offers")
	assert.Contains(t, stdout, "current main file: Ten Two, version 2.0")
	assert.Contains(t, stdout, "choose: --use-current 10 (the current file), or --skip 10")
	assert.NotContains(t, stdout, "Imported profile")
	_, err = svc.NewProfileManager().Get(context.Background(), game.ID, "Three Mods")
	assert.ErrorIs(t, err, domain.ErrProfileNotFound, "nothing was written")
}

func TestDoNexusCollectionImport_UseCurrent(t *testing.T) {
	svc, game, configDir := goneTenService(t)
	setNexusFlags(t, "abcdef", false)
	setGonePinFlags(t, gonePinFlags{useCurrent: []string{"10"}})
	stdout, _, err := captureStdoutStderrErr(t, func() error {
		return doNexusCollectionImport(context.Background(), svc, game)
	})
	require.NoError(t, err, stdout)
	assert.Contains(t, stdout, "→ the current file will be installed instead")
	saved, err := config.LoadProfile(configDir, game.ID, "Three Mods")
	require.NoError(t, err)
	ten := saved.FindRef("nexusmods", "10")
	require.NotNil(t, ten)
	assert.Equal(t, []string{"101"}, ten.FileIDs)
	assert.Equal(t, "2.0", ten.Version)
	assert.Equal(t, &domain.CollectionPin{Version: "1.0", FileIDs: []string{"100"}}, ten.SubstitutedFor)
}

func TestDoNexusCollectionImport_Skip(t *testing.T) {
	svc, game, configDir := goneTenService(t)
	setNexusFlags(t, "abcdef", false)
	setGonePinFlags(t, gonePinFlags{skip: []string{"nexusmods:10"}})
	stdout, _, err := captureStdoutStderrErr(t, func() error {
		return doNexusCollectionImport(context.Background(), svc, game)
	})
	require.NoError(t, err, stdout)
	assert.Contains(t, stdout, "⊘ Ten 1.0")
	assert.Contains(t, stdout, "→ skipped")
	saved, err := config.LoadProfile(configDir, game.ID, "Three Mods")
	require.NoError(t, err)
	assert.Nil(t, saved.FindRef("nexusmods", "10"))
	require.NotNil(t, saved.CollectionBaseline)
	require.Len(t, saved.CollectionBaseline.Skipped, 1)
}

func TestDoNexusCollectionImport_GonePinJSON(t *testing.T) {
	svc, game, _ := goneTenService(t)
	setNexusFlags(t, "abcdef", false)
	withJSONOutput(t)
	err := doNexusCollectionImport(context.Background(), svc, game)
	require.True(t, core.IsCollectionGonePins(err), "got %v", err)
	var gone *core.CollectionGonePinsError
	require.ErrorAs(t, err, &gone)
	assert.Equal(t, "nexusmods:10", gone.Pins[0].Key())
	assert.Equal(t, "101", gone.Pins[0].Current.FileID)
}

func TestReportError_JSON_CollectionGonePinsError(t *testing.T) {
	withJSONOutput(t)
	err := &core.CollectionGonePinsError{Pins: []core.CollectionGonePin{{SourceID: "nexusmods", ModID: "10", Name: "Ten", Version: "1.0",
		FileIDs: []string{"100"}, Current: &core.CollectionCurrentFile{FileID: "101", Name: "Ten Two", Version: "2.0"}}}}
	out := captureStdout(t, func() error { reportError(err); return nil })
	assert.Equal(t, "{\n"+
		"  \"error\": \"the collection pins 1 file(s) the source no longer offers - Ten: the collection pins file 100 (version 1.0); choose for each: --use-current <mod> (or --use-current-all) installs the mod's current main file instead, --skip <mod> leaves it out\",\n"+
		"  \"details\": {\n"+
		"    \"pins\": [\n"+
		"      {\n"+
		"        \"source_id\": \"nexusmods\",\n"+
		"        \"mod_id\": \"10\",\n"+
		"        \"name\": \"Ten\",\n"+
		"        \"version\": \"1.0\",\n"+
		"        \"file_ids\": [\n"+
		"          \"100\"\n"+
		"        ],\n"+
		"        \"current\": {\n"+
		"          \"file_id\": \"101\",\n"+
		"          \"name\": \"Ten Two\",\n"+
		"          \"version\": \"2.0\"\n"+
		"        }\n"+
		"      }\n"+
		"    ]\n"+
		"  }\n"+
		"}\n", out)
}

func TestRunProfileImport_GonePinFlagsNeedNexusCollection(t *testing.T) {
	setGonePinFlags(t, gonePinFlags{useCurrentAll: true})
	err := runProfileImport(profileImportCmd, []string{"some.yaml"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--use-current-all")
	assert.Contains(t, err.Error(), "--nexus-collection")
}

func TestRunProfileSync_GonePinFlagsNeedCollection(t *testing.T) {
	old := profileSyncGonePins
	profileSyncGonePins = gonePinFlags{skip: []string{"10"}}
	t.Cleanup(func() { profileSyncGonePins = old })
	assert.Contains(t, collectionSyncFlagsUsed(), "--skip")
}

func TestGonePinFlags_Choices(t *testing.T) {
	f := gonePinFlags{useCurrent: []string{"10", "nexusmods:11", " "}, skip: []string{"12"}, useCurrentAll: true}
	assert.Equal(t, core.GonePinChoices{UseCurrent: []string{"nexusmods:10", "nexusmods:11"}, Skip: []string{"nexusmods:12"}, UseCurrentAll: true},
		f.choices("nexusmods"))
	assert.Equal(t, []string{"--use-current", "--use-current-all", "--skip"}, f.used())
}

// TestPrintCollectionUpdatePlan_GonePinsAndSkipped: `profile sync
// --collection` lists the mods the profile skipped and the new version's
// gone pins, with the flags that decide them.
func TestPrintCollectionUpdatePlan_GonePinsAndSkipped(t *testing.T) {
	plan := &core.CollectionUpdatePlan{Name: "Three Mods", Origin: core.CollectionFromSource, SourceID: "nexusmods", Slug: "abcdef",
		Profile: "coll", Version: 3, NewVersion: 4,
		Skipped: []core.CollectionUpdateItem{{SourceID: "nexusmods", ModID: "10", Name: "Ten", NewVersion: "1", Note: "skipped when its pinned file was gone"}},
		Source: &core.ConvertedCollection{GonePins: []core.CollectionGonePin{{SourceID: "nexusmods", ModID: "13", Name: "Thirteen",
			FileIDs: []string{"130"}, Current: &core.CollectionCurrentFile{FileID: "131", Version: "2"}}}},
	}
	game := &domain.Game{ID: "skyrim", Name: "Skyrim"}
	stdout := captureStdout(t, func() error { printCollectionUpdatePlan(plan, game, core.CollectionUpdateOptions{}); return nil })
	assert.Contains(t, stdout, "⊘ Ten (nexusmods:10) 1: kept out")
	assert.Contains(t, stdout, "! Thirteen (nexusmods:13): the collection pins file 130, which NexusMods no longer offers")
	assert.Contains(t, stdout, "current main file: file 131, version 2")
	assert.Contains(t, stdout, "re-run `lmm profile sync --collection` with --use-current <mod>")
}

// reofferedTenDouble serves the collection at revision 4, which pins Ten
// anew at file 102 - available - beside Twelve.
type reofferedTenDouble struct{ *goneTenDouble }

func (d reofferedTenDouble) ResolveGameCollection(ctx context.Context, gameSourceID, ref string) (source.Collection, error) {
	c, err := d.nexusCollectionDouble.ResolveGameCollection(ctx, gameSourceID, ref)
	c.Revision = 4
	c.Items[0].FileID = "102"
	return c, err
}

const reofferedTenRecipe = `{"Name": "Three Mods", "Mods": [
	{"Name": "Ten", "Version": "3.0", "SourceType": "nexus", "ModID": "10", "FileID": "102", "UpdatePolicy": "exact"},
	{"Name": "Twelve", "Version": "2.0", "SourceType": "nexus", "ModID": "12", "FileID": "120", "UpdatePolicy": "latest", "Optional": true}]}`

// TestDoCollectionUpdate_ASkippedModComesBack (#657): a mod the import
// skipped, which revision 4 pins at an available file, is listed for
// attention and added; --keep-skipped keeps it out.
func TestDoCollectionUpdate_ASkippedModComesBack(t *testing.T) {
	for _, keep := range []bool{false, true} {
		svc, game, configDir := goneTenService(t)
		setNexusFlags(t, "abcdef", false)
		setGonePinFlags(t, gonePinFlags{skip: []string{"10"}})
		_, _, err := captureStdoutStderrErr(t, func() error { return doNexusCollectionImport(context.Background(), svc, game) })
		require.NoError(t, err)

		src, err := svc.GetSource("nexusmods")
		require.NoError(t, err)
		svc.RegisterSource(reofferedTenDouble{src.(*goneTenDouble)})
		entry := filepath.Join(svc.GlobalCacheDir(), game.ID, "collections", "nexusmods-abcdef", "4")
		require.NoError(t, os.MkdirAll(entry, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(entry, "collection.json"), []byte(reofferedTenRecipe), 0o644))

		oldKeep := profileSyncKeepSkipped
		t.Cleanup(func() { profileSyncKeepSkipped = oldKeep })
		setCollectionSyncFlags(t, func() {
			profileSyncYes = true
			profileSyncKeepSkipped = nil
			if keep {
				profileSyncKeepSkipped = []string{"10"}
			}
		})
		stdout, _, err := captureStdoutStderrErr(t, func() error {
			return doCollectionUpdate(context.Background(), svc, game, []string{"Three Mods"})
		})
		require.NoError(t, err, stdout)
		assert.Contains(t, stdout, "Needs your attention - 1 skipped mod(s) the collection now pins at an available file")
		assert.Contains(t, stdout, "previously skipped because its pinned file was gone; the collection now pins file 102 (version 3.0), which is available")
		saved, err := config.LoadProfile(configDir, game.ID, "Three Mods")
		require.NoError(t, err)
		if keep {
			assert.Contains(t, stdout, "kept skipped (--keep-skipped)")
			assert.Nil(t, saved.FindRef("nexusmods", "10"))
			assert.Len(t, saved.CollectionBaseline.Skipped, 1)
			continue
		}
		assert.Contains(t, stdout, "will be added")
		assert.Contains(t, stdout, "keep it skipped with --keep-skipped 10")
		ten := saved.FindRef("nexusmods", "10")
		require.NotNil(t, ten, "it comes back by default")
		assert.Equal(t, []string{"102"}, ten.FileIDs)
		assert.Empty(t, saved.CollectionBaseline.Skipped)
	}
}

func TestRunProfileSync_KeepSkippedNeedsCollection(t *testing.T) {
	old := profileSyncKeepSkipped
	profileSyncKeepSkipped = []string{"10"}
	t.Cleanup(func() { profileSyncKeepSkipped = old })
	assert.Contains(t, collectionSyncFlagsUsed(), "--keep-skipped")
}
