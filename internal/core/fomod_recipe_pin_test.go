package core_test

// #609 review: the md5 pin outlives the import without being persisted. A
// collection profile's later flows - an install after a --no-install
// import - read the md5 from the recipe of the collection the profile came
// from, while the recorded choices are still the curator's (no
// config_sha256); once answered they are never pinned again; and a recipe
// that cannot be read is a warning, never a refusal. And the phases of a
// re-import: a mod the profile already has is present whatever its phase,
// and the run installs phase by phase whichever bucket an item is in.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// importNoInstall imports the phased collection with --no-install.
func (f *phasedFixture) importNoInstall(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	plan, err := f.svc.PlanSourceCollectionImport(ctx, f.game, core.SourceCollectionImportRequest{SourceID: "nexusmods", Ref: "phased"})
	require.NoError(t, err)
	_, err = f.svc.ApplyCollectionImport(ctx, f.game, plan, core.ProfileImportOptions{NoInstall: true}, nil)
	require.NoError(t, err)
}

func (f *phasedFixture) installInstaller(t *testing.T, answers map[string]domain.FomodChoices, sink core.EventSink) error {
	t.Helper()
	ctx := context.Background()
	plan, err := f.svc.PlanInstall(ctx, f.game, "Phased", "nexusmods", "11", false)
	require.NoError(t, err)
	_, err = f.svc.ApplyInstall(ctx, f.game, plan, core.InstallOptions{Fomod: answers}, sink)
	return err
}

func installerAnswer(option string) map[string]domain.FomodChoices {
	return map[string]domain.FomodChoices{"nexusmods:11": {Steps: []domain.FomodStepChoice{{Name: "Main", Groups: []domain.FomodGroupChoice{
		{Name: "Texture", Choices: []domain.FomodOptionChoice{{Name: option}}}}}}}}
}

func TestCollectionRecipePin_ALaterInstallAsksOnAMismatch(t *testing.T) {
	f := newPhasedFixture(t, phasedRecipe)
	f.src.md5["11"] = strings.Repeat("f", 32)
	f.importNoInstall(t)

	err := f.installInstaller(t, nil, nil)
	var q *core.FomodChoicesRequiredError
	require.True(t, errors.As(err, &q), "got %v", err)
	require.Len(t, q.Mods, 1)
	assert.Equal(t, core.FomodReasonArchiveMismatch, q.Mods[0].Reason)
	require.NotNil(t, q.Mods[0].ArchiveMismatch)
	assert.Equal(t, strings.Repeat("f", 32), q.Mods[0].ArchiveMismatch.ExpectedMD5)
}

func TestCollectionRecipePin_AMatchingArchiveInstallsUnasked(t *testing.T) {
	f := newPhasedFixture(t, phasedRecipe)
	f.importNoInstall(t)
	f.src.md5["11"] = md5Hex(f.installer)
	require.NoError(t, f.installInstaller(t, nil, nil))
}

// TestCollectionRecipePin_AnAnsweredRecordingIsNotAskedAgain: once the
// mismatch is answered, the profile records the installer's own hash, and
// a reinstall from a fresh download replays it unasked.
func TestCollectionRecipePin_AnAnsweredRecordingIsNotAskedAgain(t *testing.T) {
	f := newPhasedFixture(t, phasedRecipe)
	f.src.md5["11"] = strings.Repeat("f", 32)
	f.importNoInstall(t)
	require.NoError(t, f.installInstaller(t, installerAnswer("2K"), nil))
	ref := f.installerRef(t)
	require.Len(t, ref.Fomod.ConfigSHA256, 64)

	// Gone from the cache and the database, the profile still listing it
	// with its answers: a profile apply downloads and replays it.
	ctx := context.Background()
	row, err := f.svc.GetInstalledMod(ctx, "nexusmods", "11", "skyrim", "Phased")
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(f.svc.GetGameCache(f.game).ModPath(cacheKeyOf(row))))
	require.NoError(t, f.svc.DeleteInstalledMod(ctx, "nexusmods", "11", "skyrim", "Phased"))
	plan, err := f.svc.PlanProfileApply(ctx, f.game, "Phased")
	require.NoError(t, err)
	res, err := f.svc.ApplyProfileApply(ctx, f.game, plan, core.ProfileApplyOptions{}, nil)
	require.NoError(t, err)
	require.Empty(t, res.Failed, "the answered recording is not pinned to the curator's archive")
	assert.Equal(t, "2K", f.installerRef(t).Fomod.Steps[0].Groups[0].Choices[0].Name)
	_, err = f.svc.GetInstalledMod(ctx, "nexusmods", "11", "skyrim", "Phased")
	require.NoError(t, err, "installed again")
}

// TestCollectionRecipePin_AnUnreadableRecipeWarnsAndReplays: no recipe to
// read (its cache entry gone, the source not serving it) replays the
// recorded choices as before, and says so.
func TestCollectionRecipePin_AnUnreadableRecipeWarnsAndReplays(t *testing.T) {
	f := newPhasedFixture(t, phasedRecipe)
	f.src.md5["11"] = strings.Repeat("f", 32)
	f.importNoInstall(t)
	require.NoError(t, os.RemoveAll(filepath.Join(f.cacheDir, "skyrim", "collections")))
	f.src.recipe.urlErr = errors.New("offline")

	var warnings []string
	sink := func(e core.Event) {
		if w, ok := e.(core.WarningEvent); ok {
			warnings = append(warnings, w.Message)
		}
	}
	require.NoError(t, f.installInstaller(t, nil, sink))
	assert.Equal(t, "4K", f.installerRef(t).Fomod.Steps[0].Groups[0].Choices[0].Name, "the recording replayed")
	found := false
	for _, w := range warnings {
		found = found || strings.Contains(w, "replayed unchecked")
	}
	assert.True(t, found, "the replay says it was not checked: %q", warnings)
}

// TestCollectionPhases_AModTheProfileHasIsPresentOnReimport: re-importing
// into the profile, the installer is installed again; the later phase's
// mod the profile already has (and the run leaves alone) is installed, so
// it is present.
func TestCollectionPhases_AModTheProfileHasIsPresentOnReimport(t *testing.T) {
	f := newPhasedFixture(t, phasedRecipe)
	ctx := context.Background()
	_, err := f.apply(t, nil)
	require.NoError(t, err)
	_, err = f.svc.UninstallMod(ctx, f.game, "Phased", "nexusmods", "11", core.UninstallOptions{})
	require.NoError(t, err)

	plan, err := f.svc.PlanSourceCollectionImport(ctx, f.game, core.SourceCollectionImportRequest{SourceID: "nexusmods", Ref: "phased", ProfileName: "Phased"})
	require.NoError(t, err)
	require.Len(t, plan.Missing, 1, "only the installer is installed again")
	_, err = f.svc.ApplyCollectionImport(ctx, f.game, plan, core.ProfileImportOptions{Install: true, Force: true}, nil)
	require.NoError(t, err)
	files := f.installerFiles(t)
	assert.Contains(t, files, "patch.txt")
	assert.Contains(t, files, "late.txt", "an installed mod of a later phase is present")
}

// TestCollectionPhases_ALaterPhaseRedownloadWaitsForAnEarlierPhase: the
// run's buckets put a redownload (the installer, phase 1) ahead of a
// missing mod (phase 0); the phase order installs phase 0 first, so the
// installer sees it.
func TestCollectionPhases_ALaterPhaseRedownloadWaitsForAnEarlierPhase(t *testing.T) {
	f := newPhasedFixture(t, phasedRecipe)
	ctx := context.Background()
	_, err := f.apply(t, nil)
	require.NoError(t, err)
	_, err = f.svc.UninstallMod(ctx, f.game, "Phased", "nexusmods", "10", core.UninstallOptions{})
	require.NoError(t, err)
	row, err := f.svc.GetInstalledMod(ctx, "nexusmods", "11", "skyrim", "Phased")
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(f.svc.GetGameCache(f.game).ModPath(cacheKeyOf(row))))

	plan, err := f.svc.PlanSourceCollectionImport(ctx, f.game, core.SourceCollectionImportRequest{SourceID: "nexusmods", Ref: "phased", ProfileName: "Phased"})
	require.NoError(t, err)
	require.Len(t, plan.NeedsRedownload, 1)
	require.Len(t, plan.Missing, 1)
	_, err = f.svc.ApplyCollectionImport(ctx, f.game, plan, core.ProfileImportOptions{Install: true, Force: true}, nil)
	require.NoError(t, err)
	assert.Contains(t, f.installerFiles(t), "patch.txt", "phase 0 was acquired before the phase 1 installer")
}
