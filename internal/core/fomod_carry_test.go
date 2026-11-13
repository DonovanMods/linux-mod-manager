package core_test

// #614: recorded FOMOD choices are profile intent, so the flows that write a
// profile from a document keep them - an import, then the install that
// upserts the reference with the downloaded files.

import (
	"context"
	"os"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestApplyImport_ClearsChoicesRecordedForAPlainArchive (#614 review B1):
// a document can record choices for a mod whose archive is not a FOMOD
// installer. They would name a tree the row does not have, so the install
// clears them - and the profile then converges: apply plans nothing.
func TestApplyImport_ClearsChoicesRecordedForAPlainArchive(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	mock := newMockSourceWithDownloads("src")
	defer mock.Close()
	svc.RegisterSource(mock)
	zipPath := createTestZip(t, t.TempDir(), map[string]string{"mod1.esp": "payload"})
	zipContent, err := os.ReadFile(zipPath)
	require.NoError(t, err)
	mock.AddDownload("1", zipContent)
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"})

	profile := &domain.Profile{Name: "target", GameID: "g1", Mods: []domain.ModReference{
		{SourceID: "src", ModID: "mod1", Version: "1.0", Fomod: recordedChoices()},
	}}
	data, err := config.ExportProfile(profile)
	require.NoError(t, err)
	assert.Contains(t, string(data), "config_sha256: ab12", "the export carries the choices")

	plan, err := svc.PlanImport(context.Background(), game, data)
	require.NoError(t, err)
	_, err = svc.ApplyImport(context.Background(), game, plan, core.ProfileImportOptions{Install: true}, nil)
	require.NoError(t, err)

	saved, err := svc.NewProfileManager().Get(context.Background(), "g1", "target")
	require.NoError(t, err)
	require.Len(t, saved.Mods, 1)
	assert.Equal(t, []string{"1"}, saved.Mods[0].FileIDs, "the install upserted the reference")
	assert.True(t, saved.Mods[0].Fomod.IsZero(), "and cleared choices no tree of this archive uses")

	for i := 0; i < 3; i++ {
		applyPlan, err := svc.PlanProfileApply(context.Background(), game, "target")
		require.NoError(t, err)
		assert.Empty(t, applyPlan.ToInstall, "apply %d: converged", i)
	}
}

// TestUpsertMod_PreservesFomod: install, update and converge write a fresh
// reference with no choices; the recorded ones survive it, as Disabled and
// Category do.
func TestUpsertMod_PreservesFomod(t *testing.T) {
	f := newCopyFixture(t)
	ctx := context.Background()
	f.setRefs(t, "default", domain.ModReference{SourceID: "src", ModID: "a", Version: "1.0", Fomod: recordedChoices()})
	pm := f.svc.NewProfileManager()
	require.NoError(t, pm.UpsertMod(ctx, f.game.ID, "default", domain.ModReference{SourceID: "src", ModID: "a", Version: "2.0", FileIDs: []string{"f2"}}))
	p, err := pm.Get(ctx, f.game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "2.0", p.FindRef("src", "a").Version)
	assert.True(t, recordedChoices().Equal(p.FindRef("src", "a").Fomod))
}
