package core_test

// #615: `lmm mod fomod` / "Reconfigure installer…" - re-asking an installed
// FOMOD mod's installer, preselected from its recorded choices, and
// installing the answer as a new variant that is redeployed.

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFomodReconfigure_AsksPreselectedThenRedeploysTheNewVariant(t *testing.T) {
	f := newFomodFixture(t)
	f.serve(t, "mod1", "1.0", "4K", "v1")
	ctx := context.Background()
	_, err := f.install(t, "mod1", map[string]domain.FomodChoices{"src:mod1": answer("4K")})
	require.NoError(t, err)
	before, err := f.svc.GetInstalledMod(ctx, "src", "mod1", "g1", "default")
	require.NoError(t, err)
	recorded := f.ref(t, "default", "mod1").Fomod
	detail, err := f.svc.ModDetail(ctx, f.game, "default", "src", "mod1")
	require.NoError(t, err)
	require.NotNil(t, detail.Installed.Fomod, "the mod page knows it can reconfigure")
	assert.Equal(t, recorded, *detail.Installed.Fomod)

	plan, err := f.svc.PlanFomodReconfigure(ctx, f.game, "default", "src", "mod1")
	require.NoError(t, err)
	assert.Equal(t, recorded, plan.Choices)
	assert.Equal(t, "1.0", plan.Mod.Version)

	// The first Apply asks, preselected from the recording, and changes
	// nothing.
	_, err = f.svc.ApplyFomodReconfigure(ctx, f.game, plan, core.FomodReconfigureOptions{}, nil)
	var q *core.FomodChoicesRequiredError
	require.True(t, errors.As(err, &q), "got %v", err)
	require.Len(t, q.Mods, 1)
	assert.Equal(t, core.FomodReasonReconfigure, q.Mods[0].Reason)
	assert.Equal(t, recorded.Steps, q.Mods[0].Choices)
	assert.True(t, q.Mods[0].Steps[0].Groups[0].Options[1].Selected, "4K is preselected")
	assert.Equal(t, "4K-v1", f.deployed(t, "textures/t.dds"), "nothing changed")
	still, err := f.svc.GetInstalledMod(ctx, "src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, before.Variant, still.Variant)

	// The answer installs a new variant and redeploys it.
	downloads := f.mock.DownloadCount()
	result, err := f.svc.ApplyFomodReconfigure(ctx, f.game, plan, core.FomodReconfigureOptions{
		Fomod: map[string]domain.FomodChoices{"src:mod1": answer("2K")},
	}, nil)
	require.NoError(t, err)
	require.Len(t, result.Installed, 1)
	assert.Equal(t, downloads, f.mock.DownloadCount(), "the kept download answers the re-run")
	assert.Equal(t, "2K-v1", f.deployed(t, "textures/t.dds"))
	assert.Equal(t, "PLUGIN-v1", f.deployed(t, "plugin.esp"))
	after, err := f.svc.GetInstalledMod(ctx, "src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0", after.Version)
	assert.NotEqual(t, before.Variant, after.Variant, "a new variant")
	assert.Equal(t, "2K", f.ref(t, "default", "mod1").Fomod.Steps[0].Groups[0].Choices[0].Name, "the new choices are recorded")
	assert.True(t, f.svc.GetGameCache(f.game).Exists(cacheKeyOf(after)))
	assert.False(t, f.svc.GetGameCache(f.game).Exists(cacheKeyOf(before)), "the old variant's entry is dropped")
}

func TestFomodReconfigure_RefusesAModWithNoInstaller(t *testing.T) {
	f := newFomodFixture(t)
	f.mock.AddDownload("plain", createTestZipBytes(t, map[string]string{"a.esp": "A"}))
	f.mock.AddMod("g1", &domain.Mod{ID: "plain", SourceID: "src", Name: "Plain", Version: "1.0", GameID: "g1"})
	_, err := f.install(t, "plain", nil)
	require.NoError(t, err)

	detail, err := f.svc.ModDetail(context.Background(), f.game, "default", "src", "plain")
	require.NoError(t, err)
	assert.Nil(t, detail.Installed.Fomod)
	_, err = f.svc.PlanFomodReconfigure(context.Background(), f.game, "default", "src", "plain")
	assert.ErrorIs(t, err, core.ErrNotFomodInstalled)
	_, err = f.svc.PlanFomodReconfigure(context.Background(), f.game, "default", "src", "absent")
	assert.Error(t, err)
}

func createTestZipBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	b, err := os.ReadFile(createTestZip(t, t.TempDir(), files))
	require.NoError(t, err)
	return b
}

// TestFomodUpdateBatch_TheSkipCarriesItsQuestionAndTakesAnswers: the batch
// never asks, but its skip carries the question, and the batch run again
// with the answers updates the mod (#615: the web UI answers an update
// that way).
func TestFomodUpdateBatch_TheSkipCarriesItsQuestionAndTakesAnswers(t *testing.T) {
	f := newFomodFixture(t)
	f.serve(t, "mod1", "1.0", "4K", "v1")
	ctx := context.Background()
	_, err := f.install(t, "mod1", map[string]domain.FomodChoices{"src:mod1": answer("4K")})
	require.NoError(t, err)

	f.serve(t, "mod1", "1.1", "Ultra", "v2")
	batch := func(opts core.UpdateBatchOptions) *core.UpdateBatchResult {
		t.Helper()
		row, err := f.svc.GetInstalledMod(ctx, "src", "mod1", "g1", "default")
		require.NoError(t, err)
		plan, err := f.svc.PlanUpdateBatchFrom(ctx, f.game, "default", []domain.Update{{InstalledMod: *row, NewVersion: "1.1"}}, nil)
		require.NoError(t, err)
		result, err := f.svc.ApplyUpdateBatch(ctx, f.game, plan, opts, nil)
		require.NoError(t, err)
		return result
	}

	result := batch(core.UpdateBatchOptions{})
	require.Len(t, result.Skipped, 1)
	skip := result.Skipped[0]
	require.True(t, skip.FomodReanswer)
	require.NotNil(t, skip.FomodChoicesRequired, "the skip carries its question")
	assert.Equal(t, "mod1", skip.FomodChoicesRequired.Mods[0].ModID)
	assert.Equal(t, core.FomodReasonStale, skip.FomodChoicesRequired.Mods[0].Reason)

	result = batch(core.UpdateBatchOptions{Fomod: map[string]domain.FomodChoices{"src:mod1": answer("Ultra")}})
	require.Len(t, result.Applied, 1, "skipped %+v failed %+v", result.Skipped, result.Failed)
	assert.Equal(t, "4K-v2", f.deployed(t, "textures/t.dds"))
}

// TestFomodReconfigure_RunsTheInstallerOfAModInstalledBeforeFomodSupport:
// a mod installed before lmm ran FOMOD installers has no variant and no
// recorded choices - its cache entry is the raw installer archive. The mod
// page offers to reconfigure it, and reconfiguring runs its installer.
func TestFomodReconfigure_RunsTheInstallerOfAModInstalledBeforeFomodSupport(t *testing.T) {
	f := newFomodFixture(t)
	f.serve(t, "mod1", "1.0", "4K", "v1")
	ctx := context.Background()
	mod := domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod mod1", Version: "1.0", GameID: "g1"}
	key := cacheKeyOf(&domain.InstalledMod{Mod: mod})
	gc := f.svc.GetGameCache(f.game)
	for rel, body := range map[string]string{
		"fomod/ModuleConfig.xml": flowModuleConfig("4K"),
		"core/plugin.esp":        "PLUGIN-v1",
		"tex/2k.dds":             "2K-v1",
		"tex/4k.dds":             "4K-v1",
	} {
		require.NoError(t, gc.Store(key, rel, []byte(body)))
	}
	require.NoError(t, f.svc.SaveInstalledMod(ctx, &domain.InstalledMod{Mod: mod, ProfileName: "default",
		UpdatePolicy: domain.UpdateNotify, Enabled: true, Deployed: true, LinkMethod: domain.LinkSymlink}))
	_, err := f.svc.NewProfileManager().Create(ctx, "g1", "default")
	require.NoError(t, err)
	require.NoError(t, f.svc.NewProfileManager().AddMod(ctx, "g1", "default", domain.ModReference{SourceID: "src", ModID: "mod1", Version: "1.0"}))

	detail, err := f.svc.ModDetail(ctx, f.game, "default", "src", "mod1")
	require.NoError(t, err)
	require.NotNil(t, detail.Installed, "installed")
	require.NotNil(t, detail.Installed.Fomod, "a legacy FOMOD install can be reconfigured")

	plan, err := f.svc.PlanFomodReconfigure(ctx, f.game, "default", "src", "mod1")
	require.NoError(t, err)
	_, err = f.svc.ApplyFomodReconfigure(ctx, f.game, plan, core.FomodReconfigureOptions{}, nil)
	var q *core.FomodChoicesRequiredError
	require.True(t, errors.As(err, &q), "got %v", err)
	assert.Equal(t, core.FomodReasonUnanswered, q.Mods[0].Reason, "nothing was recorded: a first answer")

	_, err = f.svc.ApplyFomodReconfigure(ctx, f.game, plan, core.FomodReconfigureOptions{
		Fomod: map[string]domain.FomodChoices{"src:mod1": answer("2K")},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "2K-v1", f.deployed(t, "textures/t.dds"))
	assert.Empty(t, f.deployed(t, "fomod/ModuleConfig.xml"), "the installer itself is gone from the game")
	row, err := f.svc.GetInstalledMod(ctx, "src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.NotEmpty(t, row.Variant)
	assert.False(t, gc.Exists(key), "the raw legacy entry is dropped")
	assert.Equal(t, "2K", f.ref(t, "default", "mod1").Fomod.Steps[0].Groups[0].Choices[0].Name)
}
