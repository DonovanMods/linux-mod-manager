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

// TestFomodProfileApply_ConvergesToTheRecordedChoices: a profile is
// desired state, so recorded choices whose tree is not the installed one
// (an edit, an import of another document) make apply reinstall the mod
// with them - and a second apply, with the canonical choices now recorded,
// has nothing to do.
func TestFomodProfileApply_ConvergesToTheRecordedChoices(t *testing.T) {
	f := newFomodFixture(t)
	f.serve(t, "mod1", "1.0", "4K", "v1")
	ctx := context.Background()
	_, err := f.install(t, "mod1", map[string]domain.FomodChoices{"src:mod1": answer("4K")})
	require.NoError(t, err)
	require.Equal(t, "4K-v1", f.deployed(t, "textures/t.dds"))

	p, err := f.svc.NewProfileManager().Get(ctx, "g1", "default")
	require.NoError(t, err)
	p.FindRef("src", "mod1").Fomod = answer("2K") // a hand edit: no config hash
	require.NoError(t, config.SaveProfile(f.svc.ConfigDir(), p))

	plan, err := f.svc.PlanProfileApply(ctx, f.game, "default")
	require.NoError(t, err)
	require.Len(t, plan.ToInstall, 1, "the choices drifted from the installed tree")
	_, err = f.svc.ApplyProfileApply(ctx, f.game, plan, core.ProfileApplyOptions{}, nil)
	require.NoError(t, err)
	assert.Equal(t, "2K-v1", f.deployed(t, "textures/t.dds"))
	ref := f.ref(t, "default", "mod1")
	assert.Len(t, ref.Fomod.ConfigSHA256, 64, "the canonical choices are recorded")
	row, err := f.svc.GetInstalledMod(ctx, "src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, core.FomodVariantForTest(ref.Fomod), row.Variant)

	plan, err = f.svc.PlanProfileApply(ctx, f.game, "default")
	require.NoError(t, err)
	assert.Empty(t, plan.ToInstall, "converged")
}

// TestFomodProfileSwitch_DeploysTheTargetsTree: a switch to a profile that
// records other choices for the same version installs that profile's tree
// - from the cache when it has it.
func TestFomodProfileSwitch_DeploysTheTargetsTree(t *testing.T) {
	f := newFomodFixture(t)
	f.serve(t, "mod1", "1.0", "4K", "v1")
	ctx := context.Background()
	pm := f.svc.NewProfileManager()
	_, err := f.install(t, "mod1", map[string]domain.FomodChoices{"src:mod1": answer("4K")})
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(ctx, "g1", "default"))

	// "alt" lists the same mod and version with the 2K choice.
	_, err = pm.Create(ctx, "g1", "alt")
	require.NoError(t, err)
	alt, err := pm.Get(ctx, "g1", "alt")
	require.NoError(t, err)
	alt.Mods = []domain.ModReference{{SourceID: "src", ModID: "mod1", Version: "1.0", Fomod: answer("2K")}}
	require.NoError(t, config.SaveProfile(f.svc.ConfigDir(), alt))

	plan, err := f.svc.PlanProfileSwitch(ctx, f.game, "alt")
	require.NoError(t, err)
	_, err = f.svc.ApplyProfileSwitch(ctx, f.game, plan, nil)
	require.NoError(t, err)
	assert.Equal(t, "2K-v1", f.deployed(t, "textures/t.dds"))
	altRow, err := f.svc.GetInstalledMod(ctx, "src", "mod1", "g1", "alt")
	require.NoError(t, err)
	defRow, err := f.svc.GetInstalledMod(ctx, "src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.NotEqual(t, defRow.Variant, altRow.Variant, "two choices, two trees of one version")

	// Back again: default's tree is cached, so nothing is downloaded.
	downloads := f.mock.DownloadCount()
	plan, err = f.svc.PlanProfileSwitch(ctx, f.game, "default")
	require.NoError(t, err)
	_, err = f.svc.ApplyProfileSwitch(ctx, f.game, plan, nil)
	require.NoError(t, err)
	assert.Equal(t, "4K-v1", f.deployed(t, "textures/t.dds"))
	assert.Equal(t, downloads, f.mock.DownloadCount())
}

// writeFomodArchive writes an installer archive to a local file, as a user
// would have downloaded it by hand.
func writeFomodArchive(t *testing.T, name, option4K, tag string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(p, fomodZip(t, option4K, tag), 0o644))
	return p
}

// TestFomodImportArchive_AsksThenImportsTheAnswer: `lmm import <archive>`
// runs the resolve step on the archive it is given, refusing before
// anything is cached when the installer needs answers.
func TestFomodImportArchive_AsksThenImportsTheAnswer(t *testing.T) {
	f := newFomodFixture(t)
	ctx := context.Background()
	archive := writeFomodArchive(t, "Flow Mod-123-1-0.zip", "4K", "local")
	_, err := f.svc.NewProfileManager().Create(ctx, "g1", "default")
	require.NoError(t, err)

	plan, err := f.svc.PlanImportArchive(ctx, f.game, "default", archive, core.ImportArchiveOptions{})
	require.NoError(t, err)
	_, err = f.svc.ApplyImportArchive(ctx, f.game, "default", plan, core.ImportArchiveOptions{}, nil)
	var q *core.FomodChoicesRequiredError
	require.True(t, errors.As(err, &q), "got %v", err)
	key := domain.ModKey(plan.Mod.SourceID, plan.Mod.ID)
	assert.Equal(t, key, domain.ModKey(q.Mods[0].SourceID, q.Mods[0].ModID), "the question names the mod the plan resolved")
	assert.Empty(t, f.deployed(t, "plugin.esp"))

	plan, err = f.svc.PlanImportArchive(ctx, f.game, "default", archive, core.ImportArchiveOptions{})
	require.NoError(t, err)
	_, err = f.svc.ApplyImportArchive(ctx, f.game, "default", plan, core.ImportArchiveOptions{Fomod: map[string]domain.FomodChoices{key: answer("2K")}}, nil)
	require.NoError(t, err)
	assert.Equal(t, "2K-local", f.deployed(t, "textures/t.dds"))
	p, err := f.svc.NewProfileManager().Get(ctx, "g1", "default")
	require.NoError(t, err)
	ref := p.FindRef(plan.Mod.SourceID, plan.Mod.ID)
	require.NotNil(t, ref)
	assert.Len(t, ref.Fomod.ConfigSHA256, 64)
	row, err := f.svc.GetInstalledMod(ctx, plan.Mod.SourceID, plan.Mod.ID, "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, core.FomodVariantForTest(ref.Fomod), row.Variant)
}

// TestFomodUpdateFromArchive_ReplaysTheRecordedChoices: an update from a
// hand-downloaded file installs the new version with the recorded choices.
func TestFomodUpdateFromArchive_ReplaysTheRecordedChoices(t *testing.T) {
	f := newFomodFixture(t)
	f.serve(t, "mod1", "1.0", "4K", "v1")
	ctx := context.Background()
	_, err := f.install(t, "mod1", map[string]domain.FomodChoices{"src:mod1": answer("4K")})
	require.NoError(t, err)

	archive := writeFomodArchive(t, "mod1-2.0.zip", "4K", "v2")
	plan, err := f.svc.PlanUpdateFromArchive(ctx, f.game, "default", "src", "mod1", archive, core.UpdateFromArchiveOptions{Version: "2.0"})
	require.NoError(t, err)
	_, err = f.svc.ApplyUpdateFromArchive(ctx, f.game, plan, core.UpdateFromArchiveOptions{Version: "2.0", AcceptMismatch: true}, nil)
	require.NoError(t, err)
	assert.Equal(t, "4K-v2", f.deployed(t, "textures/t.dds"))
	row, err := f.svc.GetInstalledMod(ctx, "src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "2.0", row.Version)
	assert.Equal(t, core.FomodVariantForTest(f.ref(t, "default", "mod1").Fomod), row.Variant)
	assert.NotEmpty(t, row.PreviousVariant)

	// A stale recording refuses the from-file update, nothing cached.
	stale := writeFomodArchive(t, "mod1-3.0.zip", "Ultra", "v3")
	plan, err = f.svc.PlanUpdateFromArchive(ctx, f.game, "default", "src", "mod1", stale, core.UpdateFromArchiveOptions{Version: "3.0"})
	require.NoError(t, err)
	_, err = f.svc.ApplyUpdateFromArchive(ctx, f.game, plan, core.UpdateFromArchiveOptions{Version: "3.0", AcceptMismatch: true}, nil)
	var q *core.FomodChoicesRequiredError
	require.True(t, errors.As(err, &q), "got %v", err)
	assert.Equal(t, "4K-v2", f.deployed(t, "textures/t.dds"))
}

// TestFomodInstallBatch_OneQuestionForTheModAndItsDependency: an install
// with dependencies acquires every mod first, so two installers' questions
// come back as one document, with nothing written; the answered re-run
// installs both.
func TestFomodInstallBatch_OneQuestionForTheModAndItsDependency(t *testing.T) {
	f := newFomodFixture(t)
	ctx := context.Background()
	f.mock.AddDownload("dep", fomodZip(t, "4K", "dep"))
	f.mock.AddMod("g1", &domain.Mod{ID: "dep", SourceID: "src", Name: "Dep", Version: "1.0", GameID: "g1"})
	f.mock.AddDownload("root", fomodZip(t, "4K", "root"))
	f.mock.AddMod("g1", &domain.Mod{ID: "root", SourceID: "src", Name: "Root", Version: "1.0", GameID: "g1",
		Dependencies: []domain.ModReference{{SourceID: "src", ModID: "dep"}}})

	plan, err := f.svc.PlanInstall(ctx, f.game, "default", "src", "root", false)
	require.NoError(t, err)
	require.NotEmpty(t, plan.Dependencies)
	result, err := f.svc.ApplyInstall(ctx, f.game, plan, core.InstallOptions{}, nil)
	// A multi-mod install reports a refusal on its result (#602), as it
	// does the loader refusal.
	require.NoError(t, err)
	require.NotNil(t, result.FomodChoicesRequired, "the refusal rides on the result: %+v", result)
	assert.True(t, result.Refused)
	assert.Len(t, result.FomodChoicesRequired.Mods, 2)
	for _, failed := range result.Failed {
		assert.Equal(t, core.RefusalFomodChoicesRequired, failed.Refusal)
	}
	assert.Empty(t, f.deployed(t, "plugin.esp"), "nothing was written")

	plan, err = f.svc.PlanInstall(ctx, f.game, "default", "src", "root", false)
	require.NoError(t, err)
	result, err = f.svc.ApplyInstall(ctx, f.game, plan, core.InstallOptions{Fomod: map[string]domain.FomodChoices{
		"src:root": answer("4K"), "src:dep": answer("2K"),
	}}, nil)
	require.NoError(t, err)
	assert.Empty(t, result.Failed)
	assert.Equal(t, "4K", f.ref(t, "default", "root").Fomod.Steps[0].Groups[0].Choices[0].Name)
	assert.Equal(t, "2K", f.ref(t, "default", "dep").Fomod.Steps[0].Groups[0].Choices[0].Name)
	assert.Equal(t, 2, f.mock.DownloadCount(), "the kept downloads served the re-run")
}

// TestFomodInstall_CancellationAtTheProfileWriteKeepsTheChoicesPaired is
// Ruling 16 for a FOMOD install: the DB row (whose variant is the answered
// tree) and the profile ref (which records the answers) are one pair, so a
// cancellation landing after the row is written still finishes the ref -
// with the choices, or the profile would name a tree its row does not have.
func TestFomodInstall_CancellationAtTheProfileWriteKeepsTheChoicesPaired(t *testing.T) {
	for name, deps := range map[string]bool{"strict path": false, "batch path": true} {
		t.Run(name, func(t *testing.T) {
			f := newFomodFixture(t)
			f.serve(t, "mod1", "1.0", "4K", "v1")
			if deps {
				f.mock.AddDownload("dep", fomodZip(t, "4K", "dep"))
				f.mock.AddMod("g1", &domain.Mod{ID: "dep", SourceID: "src", Name: "Dep", Version: "1.0", GameID: "g1"})
				f.mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod mod1", Version: "1.0", GameID: "g1",
					Dependencies: []domain.ModReference{{SourceID: "src", ModID: "dep"}}})
			}
			plan, err := f.svc.PlanInstall(context.Background(), f.game, "default", "src", "mod1", false)
			require.NoError(t, err)

			inner, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			ctx := &cancelAtCompletingProfileWrite{Context: inner, cancel: cancel}
			_, err = f.svc.ApplyInstall(ctx, f.game, plan, core.InstallOptions{Fomod: map[string]domain.FomodChoices{
				"src:mod1": answer("4K"), "src:dep": answer("2K"),
			}}, nil)
			require.True(t, ctx.fired.Load(), "the cancellation must land at the completing profile write")
			assert.ErrorIs(t, err, context.Canceled)

			first := "mod1"
			if deps {
				first = "dep" // a dependency is committed first
			}
			row, err := f.svc.GetInstalledMod(context.Background(), "src", first, "g1", "default")
			require.NoError(t, err)
			ref := f.ref(t, "default", first)
			require.NotNil(t, ref, "the ref completing the row was written")
			assert.False(t, ref.Fomod.IsZero(), "with the choices")
			assert.Equal(t, core.FomodVariantForTest(ref.Fomod), row.Variant, "naming the row's own tree")
		})
	}
}

// conditionalZip is an installer whose conditional install adds patch.txt
// when dep.esp is active - a tree that depends on the profile's files.
func conditionalZip(t *testing.T) []byte {
	t.Helper()
	cfg := strings.Replace(flowModuleConfig("4K"), "</installSteps>", `</installSteps>
<conditionalFileInstalls><patterns><pattern><dependencies><fileDependency file="dep.esp" state="Active"/></dependencies>
<files><file source="core\plugin.esp" destination="patch.txt"/></files></pattern></patterns></conditionalFileInstalls>`, 1)
	zipPath := createTestZip(t, t.TempDir(), map[string]string{
		"fomod/ModuleConfig.xml": cfg,
		"core/plugin.esp":        "PLUGIN",
		"tex/2k.dds":             "2K",
		"tex/4k.dds":             "4K",
	})
	b, err := os.ReadFile(zipPath)
	require.NoError(t, err)
	return b
}

// TestFomodFileStates_AreTheTargetProfilesAndPartOfTheVariant (#614 review
// B2): the same choices in two profiles - one with the mod that provides
// dep.esp, one without - are two trees and two cache variants, and a switch
// evaluates the installer against the profile it switches TO, not the
// deployed one.
func TestFomodFileStates_AreTheTargetProfilesAndPartOfTheVariant(t *testing.T) {
	f := newFomodFixture(t)
	ctx := context.Background()
	pm := f.svc.NewProfileManager()
	depZip := createTestZip(t, t.TempDir(), map[string]string{"dep.esp": "DEP"})
	dep, err := os.ReadFile(depZip)
	require.NoError(t, err)
	f.mock.AddDownload("dep", dep)
	f.mock.AddMod("g1", &domain.Mod{ID: "dep", SourceID: "src", Name: "Dep", Version: "1.0", GameID: "g1"})
	f.mock.AddDownload("mod1", conditionalZip(t))
	f.mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod mod1", Version: "1.0", GameID: "g1"})

	// default: dep first, then the FOMOD mod, which sees dep.esp.
	_, err = f.install(t, "dep", nil)
	require.NoError(t, err)
	_, err = f.install(t, "mod1", map[string]domain.FomodChoices{"src:mod1": answer("4K")})
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(ctx, "g1", "default"))
	assert.Equal(t, "PLUGIN", f.deployed(t, "patch.txt"), "dep.esp is active in this profile")
	defRef := f.ref(t, "default", "mod1")
	assert.Equal(t, map[string]string{"dep.esp": "Active"}, defRef.Fomod.FileStates, "the outcome is recorded with the choices")

	// alt lists the FOMOD mod with the same choices, and not dep.
	_, err = pm.Create(ctx, "g1", "alt")
	require.NoError(t, err)
	alt, err := pm.Get(ctx, "g1", "alt")
	require.NoError(t, err)
	alt.Mods = []domain.ModReference{{SourceID: "src", ModID: "mod1", Version: "1.0", Fomod: answer("4K")}}
	require.NoError(t, config.SaveProfile(f.svc.ConfigDir(), alt))

	plan, err := f.svc.PlanProfileSwitch(ctx, f.game, "alt")
	require.NoError(t, err)
	_, err = f.svc.ApplyProfileSwitch(ctx, f.game, plan, nil)
	require.NoError(t, err)
	// alt has no dep.esp: the switch evaluated the installer against alt,
	// though default's dep.esp was on disk. (The old tree's patch.txt link
	// is left where it was: a cross-profile Replace judges ownership by the
	// target profile's ledger, as it does for any version drift.)
	altRowNow, err := f.svc.GetInstalledMod(ctx, "src", "mod1", "g1", "alt")
	require.NoError(t, err)
	altFiles, err := f.svc.GetGameCache(f.game).ListFiles(cacheKeyOf(altRowNow))
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"plugin.esp", filepath.Join("textures", "t.dds")}, altFiles, "alt's tree has no patch")
	altDeployed, err := f.svc.GetDeployedFilesForMod(ctx, "g1", "alt", "src", "mod1")
	require.NoError(t, err)
	assert.NotContains(t, altDeployed, "patch.txt")
	assert.Equal(t, "4K", f.deployed(t, "textures/t.dds"))
	altRef := f.ref(t, "alt", "mod1")
	assert.Equal(t, map[string]string{"dep.esp": "Missing"}, altRef.Fomod.FileStates)

	defRow, err := f.svc.GetInstalledMod(ctx, "src", "mod1", "g1", "default")
	require.NoError(t, err)
	altRow, err := f.svc.GetInstalledMod(ctx, "src", "mod1", "g1", "alt")
	require.NoError(t, err)
	assert.NotEqual(t, defRow.Variant, altRow.Variant, "two trees, two variants, though the choices are the same")
	assert.Equal(t, core.FomodVariantForTest(altRef.Fomod), altRow.Variant)

	// And back: default's tree, from its own warm entry, with the patch.
	plan, err = f.svc.PlanProfileSwitch(ctx, f.game, "default")
	require.NoError(t, err)
	_, err = f.svc.ApplyProfileSwitch(ctx, f.game, plan, nil)
	require.NoError(t, err)
	defDeployed, err := f.svc.GetDeployedFilesForMod(ctx, "g1", "default", "src", "mod1")
	require.NoError(t, err)
	assert.Contains(t, defDeployed, "patch.txt", "default deploys its own tree again")
}

// TestFomodReinstall_RawAnswersUseTheTransaction (#614 review should-fix
// 3): answers without a config hash name no cache entry, so a reinstall
// with them always downloads - into the reinstall transaction's staging,
// never over the live entry - and lands on the right tree whether the
// answers are the same as the installed ones or not.
func TestFomodReinstall_RawAnswersUseTheTransaction(t *testing.T) {
	f := newFomodFixture(t)
	f.serve(t, "mod1", "1.0", "4K", "v1")
	ctx := context.Background()
	_, err := f.install(t, "mod1", map[string]domain.FomodChoices{"src:mod1": answer("4K")})
	require.NoError(t, err)
	first, err := f.svc.GetInstalledMod(ctx, "src", "mod1", "g1", "default")
	require.NoError(t, err)

	_, err = f.install(t, "mod1", map[string]domain.FomodChoices{"src:mod1": answer("4K")})
	require.NoError(t, err)
	same, err := f.svc.GetInstalledMod(ctx, "src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, first.Variant, same.Variant, "the same answers are the same tree")
	assert.Equal(t, "4K-v1", f.deployed(t, "textures/t.dds"))

	_, err = f.install(t, "mod1", map[string]domain.FomodChoices{"src:mod1": answer("2K")})
	require.NoError(t, err)
	other, err := f.svc.GetInstalledMod(ctx, "src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.NotEqual(t, first.Variant, other.Variant)
	assert.Equal(t, "2K-v1", f.deployed(t, "textures/t.dds"))
	c := f.svc.GetGameCache(f.game)
	assert.True(t, c.Exists(cacheKeyOf(other)))
	assert.False(t, c.Exists(cacheKeyOf(first)), "the replaced tree's entry goes, as any replaced entry does")
}

// twoFileSource serves a mod as two files: a plain archive FIRST, then the
// FOMOD installer.
type twoFileSource struct {
	*mockSourceWithDownloads
}

func (s *twoFileSource) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	return []domain.DownloadableFile{
		{ID: "extra", Name: "Extra", FileName: "extra.zip", IsPrimary: true},
		{ID: "installer", Name: "Installer", FileName: "installer.zip"},
	}, nil
}

// TestFomodInstall_InstallerNotTheFirstFileIsOneEntry (#614 review): a mod
// whose installer is its SECOND file lands in one cache entry, the
// resolved variant's, holding both files' trees - no stray plain entry.
func TestFomodInstall_InstallerNotTheFirstFileIsOneEntry(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}
	src := &twoFileSource{mockSourceWithDownloads: newMockSourceWithDownloads("src")}
	t.Cleanup(src.Close)
	svc.RegisterSource(src)
	extra := createTestZip(t, t.TempDir(), map[string]string{"extra.esp": "EXTRA"})
	b, err := os.ReadFile(extra)
	require.NoError(t, err)
	src.AddDownload("extra", b)
	src.AddDownload("installer", fomodZip(t, "4K", "v1"))
	src.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"})
	ctx := context.Background()

	plan, err := svc.PlanInstall(ctx, game, "default", "src", "mod1", false)
	require.NoError(t, err)
	_, err = svc.ApplyInstall(ctx, game, plan, core.InstallOptions{TargetFileIDs: []string{"extra", "installer"},
		Fomod: map[string]domain.FomodChoices{"src:mod1": answer("4K")}}, nil)
	require.NoError(t, err)

	row, err := svc.GetInstalledMod(ctx, "src", "mod1", "g1", "default")
	require.NoError(t, err)
	require.NotEmpty(t, row.Variant)
	c := svc.GetGameCache(game)
	files, err := c.ListFiles(cacheKeyOf(row))
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"extra.esp", "plugin.esp", filepath.Join("textures", "t.dds")}, files)
	plain := cacheKeyOf(row)
	plain.Variant = ""
	assert.False(t, c.Exists(plain), "no stray plain entry from the first file")
	for _, rel := range []string{"extra.esp", "plugin.esp", "textures/t.dds"} {
		_, err := os.Stat(filepath.Join(gameDir, filepath.FromSlash(rel)))
		assert.NoError(t, err, rel)
	}
}
