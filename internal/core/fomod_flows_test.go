package core_test

// #614: the FOMOD resolve step through the flows a user runs - install,
// profile import, the update batch and a single update - with a mock source
// serving installer archives. Each asks for missing answers with nothing
// written, installs the answered tree under its own cache variant, and
// records the choices on the profile.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/fomod"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// flowModuleConfig: one step, an exactly-one Texture group (2K, 4K) and a
// required plugin. option renames 4K, so a later version can make a
// recording stale.
func flowModuleConfig(option4K string) string {
	return `<config><moduleName>Flow Installer</moduleName>
<requiredInstallFiles><file source="core\plugin.esp" destination="plugin.esp"/></requiredInstallFiles>
<installSteps order="Explicit"><installStep name="Main"><optionalFileGroups order="Explicit">
<group name="Texture" type="SelectExactlyOne"><plugins order="Explicit">
<plugin name="2K"><description>Light.</description><files><file source="tex\2k.dds" destination="textures\t.dds"/></files><typeDescriptor><type name="Optional"/></typeDescriptor></plugin>
<plugin name="` + option4K + `"><description>Heavy.</description><files><file source="tex\4k.dds" destination="textures\t.dds"/></files><typeDescriptor><type name="Optional"/></typeDescriptor></plugin>
</plugins></group></optionalFileGroups></installStep></installSteps></config>`
}

// fomodZip is an installer archive whose payload carries tag, so a test
// can tell one version's files from another's.
func fomodZip(t *testing.T, option4K, tag string) []byte {
	t.Helper()
	zipPath := createTestZip(t, t.TempDir(), map[string]string{
		"fomod/ModuleConfig.xml": flowModuleConfig(option4K),
		"core/plugin.esp":        "PLUGIN-" + tag,
		"tex/2k.dds":             "2K-" + tag,
		"tex/4k.dds":             "4K-" + tag,
	})
	b, err := os.ReadFile(zipPath)
	require.NoError(t, err)
	return b
}

func answer(option string) domain.FomodChoices {
	return domain.FomodChoices{Steps: []fomod.StepChoice{{Name: "Main", Groups: []fomod.GroupChoice{
		{Name: "Texture", Choices: []fomod.OptionChoice{{Name: option}}},
	}}}}
}

type fomodFixture struct {
	svc     *core.Service
	game    *domain.Game
	gameDir string
	mock    *perModFileSource
}

func newFomodFixture(t *testing.T) *fomodFixture {
	t.Helper()
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}
	mock := &perModFileSource{mockSourceWithDownloads: newMockSourceWithDownloads("src")}
	t.Cleanup(mock.Close)
	svc.RegisterSource(mock)
	return &fomodFixture{svc: svc, game: game, gameDir: gameDir, mock: mock}
}

func (f *fomodFixture) serve(t *testing.T, modID, version, option4K, tag string) {
	t.Helper()
	f.mock.AddDownload(modID, fomodZip(t, option4K, tag))
	f.mock.AddMod("g1", &domain.Mod{ID: modID, SourceID: "src", Name: "Mod " + modID, Version: version, GameID: "g1"})
}

func (f *fomodFixture) deployed(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.gameDir, filepath.FromSlash(rel)))
	if os.IsNotExist(err) {
		return ""
	}
	require.NoError(t, err)
	return string(b)
}

func (f *fomodFixture) install(t *testing.T, modID string, answers map[string]domain.FomodChoices) (*core.InstallResult, error) {
	t.Helper()
	plan, err := f.svc.PlanInstall(context.Background(), f.game, "default", "src", modID, false)
	require.NoError(t, err)
	return f.svc.ApplyInstall(context.Background(), f.game, plan, core.InstallOptions{Fomod: answers}, nil)
}

func (f *fomodFixture) ref(t *testing.T, profile, modID string) *domain.ModReference {
	t.Helper()
	p, err := f.svc.NewProfileManager().Get(context.Background(), "g1", profile)
	require.NoError(t, err)
	return p.FindRef("src", modID)
}

func TestFomodInstall_AsksThenInstallsTheAnswer(t *testing.T) {
	f := newFomodFixture(t)
	f.serve(t, "mod1", "1.0", "4K", "v1")
	ctx := context.Background()

	_, err := f.install(t, "mod1", nil)
	var q *core.FomodChoicesRequiredError
	require.True(t, errors.As(err, &q), "got %v", err)
	require.Len(t, q.Mods, 1)
	assert.Equal(t, "mod1", q.Mods[0].ModID)
	assert.Equal(t, "Flow Installer", q.Mods[0].ModuleName)
	_, rowErr := f.svc.GetInstalledMod(ctx, "src", "mod1", "g1", "default")
	assert.ErrorIs(t, rowErr, domain.ErrModNotFound, "nothing was written")
	assert.Empty(t, f.deployed(t, "plugin.esp"))
	assert.Equal(t, 1, f.mock.DownloadCount())

	result, err := f.install(t, "mod1", map[string]domain.FomodChoices{"src:mod1": answer("4K")})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, f.mock.DownloadCount(), "the re-run used the kept download")
	assert.Equal(t, "PLUGIN-v1", f.deployed(t, "plugin.esp"))
	assert.Equal(t, "4K-v1", f.deployed(t, "textures/t.dds"))
	assert.Empty(t, f.deployed(t, "fomod/ModuleConfig.xml"), "the installer itself is never deployed")

	row, err := f.svc.GetInstalledMod(ctx, "src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0", row.Version, "the variant is never part of the version")
	assert.Len(t, row.Variant, 12)
	ref := f.ref(t, "default", "mod1")
	require.NotNil(t, ref)
	assert.Len(t, ref.Fomod.ConfigSHA256, 64, "the answer is recorded with the installer it answers")
	require.Len(t, ref.Fomod.Steps, 1)
	assert.Equal(t, "4K", ref.Fomod.Steps[0].Groups[0].Choices[0].Name)
	assert.True(t, f.svc.GetGameCache(f.game).Exists(cacheKeyOf(row)), "the variant entry is cached")

	// A reinstall replays the recorded choices: no question. (A
	// same-version reinstall always re-downloads into its transaction's
	// empty staging cache.)
	_, err = f.install(t, "mod1", nil)
	require.NoError(t, err)
	assert.Equal(t, "4K-v1", f.deployed(t, "textures/t.dds"))
	again, err := f.svc.GetInstalledMod(ctx, "src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, row.Variant, again.Variant, "the same choices are the same tree")
}

func TestFomodImport_OneQuestionForEveryModThenTheAnswers(t *testing.T) {
	f := newFomodFixture(t)
	f.serve(t, "mod1", "1.0", "4K", "a")
	f.serve(t, "mod2", "1.0", "4K", "b")
	ctx := context.Background()

	data, err := config.ExportProfile(&domain.Profile{Name: "target", GameID: "g1", Mods: []domain.ModReference{
		{SourceID: "src", ModID: "mod1", Version: "1.0"},
		{SourceID: "src", ModID: "mod2", Version: "1.0"},
	}})
	require.NoError(t, err)
	plan, err := f.svc.PlanImport(ctx, f.game, data)
	require.NoError(t, err)
	_, err = f.svc.ApplyImport(ctx, f.game, plan, core.ProfileImportOptions{Install: true}, nil)
	var q *core.FomodChoicesRequiredError
	require.True(t, errors.As(err, &q), "got %v", err)
	require.Len(t, q.Mods, 2, "one question document covers every mod")
	var incomplete *core.ProfileImportIncompleteError
	require.True(t, errors.As(err, &incomplete))
	require.NotNil(t, incomplete.Result.FomodChoicesRequired)
	assert.True(t, incomplete.Result.Refused)
	for _, failure := range incomplete.Result.Failures {
		assert.Equal(t, core.RefusalFomodChoicesRequired, failure.Refusal)
	}
	_, err = f.svc.NewProfileManager().Get(ctx, "g1", "target")
	assert.Error(t, err, "nothing was written, the profile included")

	plan, err = f.svc.PlanImport(ctx, f.game, data)
	require.NoError(t, err)
	_, err = f.svc.ApplyImport(ctx, f.game, plan, core.ProfileImportOptions{Install: true,
		Fomod: map[string]domain.FomodChoices{"src:mod1": answer("2K"), "src:mod2": answer("4K")}}, nil)
	require.NoError(t, err)
	assert.Equal(t, "2K", f.ref(t, "target", "mod1").Fomod.Steps[0].Groups[0].Choices[0].Name)
	assert.Equal(t, "4K", f.ref(t, "target", "mod2").Fomod.Steps[0].Groups[0].Choices[0].Name)
	assert.Equal(t, 2, f.mock.DownloadCount(), "the answered re-run fetched nothing again")
}

// TestFomodImport_RecordedChoicesInstallWithoutAsking: a document that
// carries choices (an export, a collection) installs with them.
func TestFomodImport_RecordedChoicesInstallWithoutAsking(t *testing.T) {
	f := newFomodFixture(t)
	f.serve(t, "mod1", "1.0", "4K", "a")
	ctx := context.Background()

	data, err := config.ExportProfile(&domain.Profile{Name: "target", GameID: "g1", Mods: []domain.ModReference{
		{SourceID: "src", ModID: "mod1", Version: "1.0", Fomod: answer("4K")},
	}})
	require.NoError(t, err)
	plan, err := f.svc.PlanImport(ctx, f.game, data)
	require.NoError(t, err)
	_, err = f.svc.ApplyImport(ctx, f.game, plan, core.ProfileImportOptions{Install: true}, nil)
	require.NoError(t, err)
	ref := f.ref(t, "target", "mod1")
	assert.Len(t, ref.Fomod.ConfigSHA256, 64, "the recording is completed with the installer's hash")
	row, err := f.svc.GetInstalledMod(ctx, "src", "mod1", "g1", "target")
	require.NoError(t, err)
	assert.NotEmpty(t, row.Variant)
}

func TestFomodUpdate_MatchingChoicesUpdateAndStaleOnesSkip(t *testing.T) {
	f := newFomodFixture(t)
	f.serve(t, "mod1", "1.0", "4K", "v1")
	ctx := context.Background()
	_, err := f.install(t, "mod1", map[string]domain.FomodChoices{"src:mod1": answer("4K")})
	require.NoError(t, err)
	before := f.ref(t, "default", "mod1").Fomod

	batch := func(newVersion string) *core.UpdateBatchResult {
		t.Helper()
		row, err := f.svc.GetInstalledMod(ctx, "src", "mod1", "g1", "default")
		require.NoError(t, err)
		plan, err := f.svc.PlanUpdateBatchFrom(ctx, f.game, "default", []domain.Update{{InstalledMod: *row, NewVersion: newVersion}}, nil)
		require.NoError(t, err)
		result, err := f.svc.ApplyUpdateBatch(ctx, f.game, plan, core.UpdateBatchOptions{}, nil)
		require.NoError(t, err)
		return result
	}

	// 1.1 changes the installer text but keeps the choices' names: the
	// recording still fits, so the batch updates and records the new hash.
	f.serve(t, "mod1", "1.1", "4K", "v2")
	result := batch("1.1")
	require.Len(t, result.Applied, 1, "skipped %+v failed %+v", result.Skipped, result.Failed)
	assert.Equal(t, "4K-v2", f.deployed(t, "textures/t.dds"))
	after := f.ref(t, "default", "mod1")
	assert.Equal(t, "1.1", after.Version)
	assert.Equal(t, before.ConfigSHA256, after.Fomod.ConfigSHA256, "the same installer, the same hash")
	row, err := f.svc.GetInstalledMod(ctx, "src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.NotEmpty(t, row.Variant)
	assert.NotEmpty(t, row.PreviousVariant, "rollback knows the old tree")

	// 1.2 renames the chosen option: the batch never asks, it skips - and
	// writes nothing.
	f.serve(t, "mod1", "1.2", "Ultra", "v3")
	result = batch("1.2")
	require.Empty(t, result.Applied)
	require.Len(t, result.Skipped, 1, "failed %+v", result.Failed)
	assert.True(t, result.Skipped[0].FomodReanswer)
	assert.Equal(t, core.ReasonFomodReanswer, result.Skipped[0].Reason)
	assert.Equal(t, "4K-v2", f.deployed(t, "textures/t.dds"), "the installed version stays")
	assert.Equal(t, "1.1", f.ref(t, "default", "mod1").Version)

	// A single update asks instead, and takes the answer.
	row, err = f.svc.GetInstalledMod(ctx, "src", "mod1", "g1", "default")
	require.NoError(t, err)
	plan, err := f.svc.PlanUpdateFrom(ctx, f.game, "default", domain.Update{InstalledMod: *row, NewVersion: "1.2"})
	require.NoError(t, err)
	_, err = f.svc.ApplyUpdate(ctx, f.game, plan, core.UpdateOptions{}, nil)
	var q *core.FomodChoicesRequiredError
	require.True(t, errors.As(err, &q), "got %v", err)
	assert.Equal(t, core.FomodReasonStale, q.Mods[0].Reason)
	require.NotEmpty(t, q.Mods[0].Unmatched)

	plan, err = f.svc.PlanUpdateFrom(ctx, f.game, "default", domain.Update{InstalledMod: *row, NewVersion: "1.2"})
	require.NoError(t, err)
	_, err = f.svc.ApplyUpdate(ctx, f.game, plan, core.UpdateOptions{Fomod: map[string]domain.FomodChoices{"src:mod1": answer("Ultra")}}, nil)
	require.NoError(t, err)
	assert.Equal(t, "4K-v3", f.deployed(t, "textures/t.dds"))
	assert.Equal(t, "Ultra", f.ref(t, "default", "mod1").Fomod.Steps[0].Groups[0].Choices[0].Name)
	assert.True(t, strings.HasPrefix(f.ref(t, "default", "mod1").Version, "1.2"))
}

func cacheKeyOf(row *domain.InstalledMod) cache.Key {
	return cache.Key{GameID: row.GameID, SourceID: row.SourceID, ModID: row.ID, Version: row.Version, Variant: row.Variant}
}
