package core_test

// #634: a mod whose pinned files each carry their own FOMOD installer (a
// collection's Scrambled Bugs: a main file and a patch file, each a FOMOD)
// records and replays its choices PER FILE. Each flow asks every file's
// installer in one round, labelled with its file, takes the answers keyed
// per file (domain.FomodFileKey), installs both resolved trees into the
// mod's one cache entry under a variant over both, and records the choices
// under the reference's fomod_files - while a mod with one installer
// behaves exactly as before.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/fomod"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// patchModuleConfig is the patch file's installer: one exactly-one group
// choosing which compatibility patch to install. option renames USSEP, so a
// later version can make a recording stale.
func patchModuleConfig(optionUSSEP string) string {
	return `<config><moduleName>Patch Installer</moduleName>
<installSteps order="Explicit"><installStep name="Patches"><optionalFileGroups order="Explicit">
<group name="Which" type="SelectExactlyOne"><plugins order="Explicit">
<plugin name="` + optionUSSEP + `"><description>For USSEP.</description><files><file source="p\ussep.esp" destination="bugs_patch.esp"/></files><typeDescriptor><type name="Optional"/></typeDescriptor></plugin>
<plugin name="Vanilla"><description>Without USSEP.</description><files><file source="p\vanilla.esp" destination="bugs_patch.esp"/></files><typeDescriptor><type name="Optional"/></typeDescriptor></plugin>
</plugins></group></optionalFileGroups></installStep></installSteps></config>`
}

func patchZip(t *testing.T, optionUSSEP, tag string) []byte {
	t.Helper()
	zipPath := createTestZip(t, t.TempDir(), map[string]string{
		"fomod/ModuleConfig.xml": patchModuleConfig(optionUSSEP),
		"p/ussep.esp":            "USSEP-" + tag,
		"p/vanilla.esp":          "VANILLA-" + tag,
	})
	b, err := os.ReadFile(zipPath)
	require.NoError(t, err)
	return b
}

func patchAnswer(option string) domain.FomodChoices {
	return domain.FomodChoices{Steps: []fomod.StepChoice{{Name: "Patches", Groups: []fomod.GroupChoice{
		{Name: "Which", Choices: []fomod.OptionChoice{{Name: option}}},
	}}}}
}

// perFileFixture serves mod "bugs" as two files, each an installer.
type perFileFixture struct {
	svc     *core.Service
	game    *domain.Game
	gameDir string
	mock    *multiFileDownloadSource
}

func newPerFileFixture(t *testing.T) *perFileFixture {
	t.Helper()
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}
	mock := &multiFileDownloadSource{mockSourceWithDownloads: newMockSourceWithDownloads("src")}
	t.Cleanup(mock.Close)
	svc.RegisterSource(mock)
	return &perFileFixture{svc: svc, game: game, gameDir: gameDir, mock: mock}
}

// serve publishes version of "bugs": main-<n> (the texture installer) and
// patch-<n> (the patch installer), n being the version's own suffix.
func (f *perFileFixture) serve(t *testing.T, version, n, option4K, optionUSSEP string) {
	t.Helper()
	f.mock.AddDownload("main-"+n, fomodZip(t, option4K, "v"+n))
	f.mock.AddDownload("patch-"+n, patchZip(t, optionUSSEP, "v"+n))
	f.mock.AddMod("g1", &domain.Mod{ID: "bugs", SourceID: "src", Name: "Scrambled Bugs", Version: version, GameID: "g1"})
	f.mock.files = []domain.DownloadableFile{
		{ID: "main-" + n, Name: "Scrambled Bugs", FileName: "main-" + n + ".zip", Version: version, IsPrimary: true, Category: "MAIN"},
		{ID: "patch-" + n, Name: "Scrambled Bugs - Patches", FileName: "patch-" + n + ".zip", Version: version, Category: "OPTIONAL"},
	}
}

// install installs both files of "bugs", as `lmm install --file a,b` does.
func (f *perFileFixture) install(t *testing.T, answers map[string]domain.FomodChoices) (*core.InstallResult, error) {
	t.Helper()
	plan, err := f.svc.PlanInstall(context.Background(), f.game, "default", "src", "bugs", false)
	require.NoError(t, err)
	plan.Files = slices.Clone(f.mock.files)
	ids := []string{f.mock.files[0].ID, f.mock.files[1].ID}
	return f.svc.ApplyInstall(context.Background(), f.game, plan, core.InstallOptions{Fomod: answers, TargetFileIDs: ids}, nil)
}

func (f *perFileFixture) deployed(t *testing.T, rel string) string {
	t.Helper()
	return (&fomodFixture{gameDir: f.gameDir}).deployed(t, rel)
}

func (f *perFileFixture) ref(t *testing.T, profile string) *domain.ModReference {
	t.Helper()
	p, err := f.svc.NewProfileManager().Get(context.Background(), "g1", profile)
	require.NoError(t, err)
	return p.FindRef("src", "bugs")
}

func picked(c domain.FomodChoices) string {
	if len(c.Steps) == 0 || len(c.Steps[0].Groups) == 0 || len(c.Steps[0].Groups[0].Choices) == 0 {
		return ""
	}
	return c.Steps[0].Groups[0].Choices[0].Name
}

func perFileAnswers(main, patch string, n string) map[string]domain.FomodChoices {
	out := map[string]domain.FomodChoices{}
	if main != "" {
		out[domain.FomodFileKey("src", "bugs", "main-"+n)] = answer(main)
	}
	if patch != "" {
		out[domain.FomodFileKey("src", "bugs", "patch-"+n)] = patchAnswer(patch)
	}
	return out
}

func TestFomodPerFile_InstallAsksEveryFileThenRecordsEach(t *testing.T) {
	f := newPerFileFixture(t)
	f.serve(t, "1.0", "1", "4K", "USSEP")
	ctx := context.Background()

	_, err := f.install(t, nil)
	var q *core.FomodChoicesRequiredError
	require.True(t, errors.As(err, &q), "got %v", err)
	require.Len(t, q.Mods, 2, "both installers ask in one round")
	assert.Equal(t, "main-1", q.Mods[0].FileID)
	assert.Equal(t, "Scrambled Bugs", q.Mods[0].FileName)
	assert.Equal(t, "Flow Installer", q.Mods[0].ModuleName)
	assert.Equal(t, "patch-1", q.Mods[1].FileID)
	assert.Equal(t, "Scrambled Bugs - Patches", q.Mods[1].FileName)
	assert.Equal(t, "Patch Installer", q.Mods[1].ModuleName)
	assert.Equal(t, "src:bugs#patch-1", q.Mods[1].Key())
	assert.Contains(t, err.Error(), "Scrambled Bugs (Scrambled Bugs - Patches)", "each question names its file")
	_, rowErr := f.svc.GetInstalledMod(ctx, "src", "bugs", "g1", "default")
	assert.ErrorIs(t, rowErr, domain.ErrModNotFound, "nothing was written")
	assert.Equal(t, 2, f.mock.DownloadCount())

	_, err = f.install(t, perFileAnswers("2K", "Vanilla", "1"))
	require.NoError(t, err)
	assert.Equal(t, 2, f.mock.DownloadCount(), "the re-run used the kept downloads")
	assert.Equal(t, "PLUGIN-v1", f.deployed(t, "plugin.esp"))
	assert.Equal(t, "2K-v1", f.deployed(t, "textures/t.dds"))
	assert.Equal(t, "VANILLA-v1", f.deployed(t, "bugs_patch.esp"), "the patch file's own installer ran too")
	assert.Empty(t, f.deployed(t, "fomod/ModuleConfig.xml"))

	row, err := f.svc.GetInstalledMod(ctx, "src", "bugs", "g1", "default")
	require.NoError(t, err)
	assert.Len(t, row.Variant, 12)
	assert.ElementsMatch(t, []string{"main-1", "patch-1"}, row.FileIDs)
	ref := f.ref(t, "default")
	require.NotNil(t, ref)
	assert.True(t, ref.Fomod.IsZero(), "not the single form")
	require.Len(t, ref.FomodFiles, 2)
	assert.Equal(t, "2K", picked(ref.FomodFiles["main-1"]))
	assert.Equal(t, "Vanilla", picked(ref.FomodFiles["patch-1"]))
	assert.Len(t, ref.FomodFiles["patch-1"].ConfigSHA256, 64, "each answer is recorded with the installer it answers")
	assert.NotEqual(t, ref.FomodFiles["main-1"].ConfigSHA256, ref.FomodFiles["patch-1"].ConfigSHA256)
	assert.True(t, f.svc.GetGameCache(f.game).Exists(cacheKeyOf(row)), "the variant entry is cached")

	// A reinstall replays each file's recorded choices: no question.
	_, err = f.install(t, nil)
	require.NoError(t, err)
	assert.Equal(t, "VANILLA-v1", f.deployed(t, "bugs_patch.esp"))
	again, err := f.svc.GetInstalledMod(ctx, "src", "bugs", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, row.Variant, again.Variant, "the same choices are the same tree")
}

// TestFomodPerFile_AnswerOneFileAsksTheOther: answers for one file leave
// the other's installer still asking, still labelled.
func TestFomodPerFile_AnswerOneFileAsksTheOther(t *testing.T) {
	f := newPerFileFixture(t)
	f.serve(t, "1.0", "1", "4K", "USSEP")
	_, err := f.install(t, perFileAnswers("4K", "", "1"))
	var q *core.FomodChoicesRequiredError
	require.True(t, errors.As(err, &q), "got %v", err)
	require.Len(t, q.Mods, 1)
	assert.Equal(t, "patch-1", q.Mods[0].FileID, "labelled: the mod has two installers")
}

// TestFomodPerFile_OneInstallerAmongFilesIsUnchanged: a mod whose second
// file is plain asks and records exactly as before #634 - keyed by the mod,
// recorded under fomod.
func TestFomodPerFile_OneInstallerAmongFilesIsUnchanged(t *testing.T) {
	f := newPerFileFixture(t)
	f.serve(t, "1.0", "1", "4K", "USSEP")
	plainZip := createTestZip(t, t.TempDir(), map[string]string{"readme.txt": "plain"})
	b, err := os.ReadFile(plainZip)
	require.NoError(t, err)
	f.mock.AddDownload("patch-1", b)

	_, err = f.install(t, nil)
	var q *core.FomodChoicesRequiredError
	require.True(t, errors.As(err, &q), "got %v", err)
	require.Len(t, q.Mods, 1)
	assert.Empty(t, q.Mods[0].FileID, "one installer: the question is the mod's")
	assert.Empty(t, q.Mods[0].FileName)
	assert.Equal(t, "src:bugs", q.Mods[0].Key())

	_, err = f.install(t, map[string]domain.FomodChoices{"src:bugs": answer("4K")})
	require.NoError(t, err)
	ref := f.ref(t, "default")
	assert.Equal(t, "4K", picked(ref.Fomod))
	assert.Nil(t, ref.FomodFiles)
	assert.Equal(t, "plain", f.deployed(t, "readme.txt"))
}

// TestFomodPerFile_UpdateAsksOnlyTheStaleFileAndRollbackRestoresBoth: an
// update replays each file's choices; a file whose installer changed under
// its recording asks alone, labelled. The update records the replaced
// per-file choices, and a rollback puts them - and the old tree - back.
func TestFomodPerFile_UpdateAsksOnlyTheStaleFileAndRollbackRestoresBoth(t *testing.T) {
	f := newPerFileFixture(t)
	f.serve(t, "1.0", "1", "4K", "USSEP")
	ctx := context.Background()
	_, err := f.install(t, perFileAnswers("2K", "USSEP", "1"))
	require.NoError(t, err)
	before, err := f.svc.GetInstalledMod(ctx, "src", "bugs", "g1", "default")
	require.NoError(t, err)

	// 1.1: the patch installer renamed USSEP, so its recording is stale;
	// the main installer did not change.
	f.serve(t, "1.1", "2", "4K", "USSEP 2")
	upd := domain.Update{InstalledMod: *before, NewVersion: "1.1", FileIDReplacements: map[string]string{"main-1": "main-2", "patch-1": "patch-2"}}

	// The batch never asks: it skips the mod, carrying the labelled question.
	plan, err := f.svc.PlanUpdateBatchFrom(ctx, f.game, "default", []domain.Update{upd}, nil)
	require.NoError(t, err)
	batch, err := f.svc.ApplyUpdateBatch(ctx, f.game, plan, core.UpdateBatchOptions{}, nil)
	require.NoError(t, err)
	require.Len(t, batch.Skipped, 1)
	skip := batch.Skipped[0]
	assert.True(t, skip.FomodReanswer)
	require.NotNil(t, skip.FomodChoicesRequired)
	require.Len(t, skip.FomodChoicesRequired.Mods, 1, "only the stale file asks")
	assert.Equal(t, "patch-2", skip.FomodChoicesRequired.Mods[0].FileID)
	assert.Equal(t, core.FomodReasonStale, skip.FomodChoicesRequired.Mods[0].Reason)

	up, err := f.svc.PlanUpdateFrom(ctx, f.game, "default", upd)
	require.NoError(t, err)
	_, err = f.svc.ApplyUpdate(ctx, f.game, up, core.UpdateOptions{Fomod: perFileAnswers("", "USSEP 2", "2")}, nil)
	require.NoError(t, err)
	assert.Equal(t, "2K-v2", f.deployed(t, "textures/t.dds"), "the main file replayed its recording")
	assert.Equal(t, "USSEP-v2", f.deployed(t, "bugs_patch.esp"))
	ref := f.ref(t, "default")
	assert.Equal(t, "2K", picked(ref.FomodFiles["main-2"]))
	assert.Equal(t, "USSEP 2", picked(ref.FomodFiles["patch-2"]))
	assert.NotContains(t, ref.FomodFiles, "main-1", "the old files' choices are not the new version's")
	after, err := f.svc.GetInstalledMod(ctx, "src", "bugs", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "1.1", after.Version)
	assert.Equal(t, before.Variant, after.PreviousVariant)
	assert.Equal(t, "USSEP", picked(after.PreviousFomodFiles["patch-1"]), "the replaced per-file choices are kept for a rollback")

	rb, err := f.svc.PlanRollback(ctx, f.game, "default", "src", "bugs")
	require.NoError(t, err)
	_, err = f.svc.ApplyRollback(ctx, f.game, rb, core.RollbackOptions{}, nil)
	require.NoError(t, err)
	restored, err := f.svc.GetInstalledMod(ctx, "src", "bugs", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0", restored.Version)
	assert.Equal(t, before.Variant, restored.Variant)
	ref = f.ref(t, "default")
	assert.Equal(t, "USSEP", picked(ref.FomodFiles["patch-1"]), "the rollback restored the per-file choices")
	assert.Equal(t, "2K", picked(ref.FomodFiles["main-1"]))
	assert.NotContains(t, ref.FomodFiles, "patch-2")
	assert.Equal(t, "USSEP-v1", f.deployed(t, "bugs_patch.esp"))
	assert.Equal(t, "USSEP 2", picked(restored.PreviousFomodFiles["patch-2"]))
}

// TestFomodPerFile_ReconfigureOneFile: `lmm mod fomod --file` asks only
// that file's installer, preselected from its recording; the other file
// keeps its choices.
func TestFomodPerFile_ReconfigureOneFile(t *testing.T) {
	f := newPerFileFixture(t)
	f.serve(t, "1.0", "1", "4K", "USSEP")
	ctx := context.Background()
	_, err := f.install(t, perFileAnswers("2K", "USSEP", "1"))
	require.NoError(t, err)

	plan, err := f.svc.PlanFomodReconfigure(ctx, f.game, "default", "src", "bugs")
	require.NoError(t, err)
	require.Len(t, plan.Installers, 2, "the plan lists which installers there are to pick from")
	assert.Equal(t, "main-1", plan.Installers[0].FileID)
	assert.Equal(t, "Scrambled Bugs", plan.Installers[0].Name)
	assert.Equal(t, "2K", picked(plan.Installers[0].Choices))
	assert.Equal(t, "patch-1", plan.Installers[1].FileID)

	_, err = f.svc.ApplyFomodReconfigure(ctx, f.game, plan, core.FomodReconfigureOptions{FileID: "nope"}, nil)
	require.Error(t, err, "a file the mod does not install is refused")

	_, err = f.svc.ApplyFomodReconfigure(ctx, f.game, plan, core.FomodReconfigureOptions{FileID: "patch-1"}, nil)
	var q *core.FomodChoicesRequiredError
	require.True(t, errors.As(err, &q), "got %v", err)
	require.Len(t, q.Mods, 1, "only the picked file's installer asks")
	assert.Equal(t, "patch-1", q.Mods[0].FileID)
	assert.Equal(t, core.FomodReasonReconfigure, q.Mods[0].Reason)
	assert.Equal(t, "USSEP", picked(domain.FomodChoices{Steps: q.Mods[0].Choices}), "preselected from the recording")

	_, err = f.svc.ApplyFomodReconfigure(ctx, f.game, plan, core.FomodReconfigureOptions{FileID: "patch-1", Fomod: perFileAnswers("", "Vanilla", "1")}, nil)
	require.NoError(t, err)
	assert.Equal(t, "VANILLA-v1", f.deployed(t, "bugs_patch.esp"))
	assert.Equal(t, "2K-v1", f.deployed(t, "textures/t.dds"))
	ref := f.ref(t, "default")
	assert.Equal(t, "Vanilla", picked(ref.FomodFiles["patch-1"]))
	assert.Equal(t, "2K", picked(ref.FomodFiles["main-1"]), "the other file kept its choices")

	// Without a file, every installer asks.
	plan, err = f.svc.PlanFomodReconfigure(ctx, f.game, "default", "src", "bugs")
	require.NoError(t, err)
	_, err = f.svc.ApplyFomodReconfigure(ctx, f.game, plan, core.FomodReconfigureOptions{}, nil)
	require.True(t, errors.As(err, &q), "got %v", err)
	assert.Len(t, q.Mods, 2)
}

// TestFomodPerFile_ImportReplaysRecordedPerFileChoices: a document that
// records the choices per file (an export, a converted collection) installs
// each file with its own, asking nothing.
func TestFomodPerFile_ImportReplaysRecordedPerFileChoices(t *testing.T) {
	f := newPerFileFixture(t)
	f.serve(t, "1.0", "1", "4K", "USSEP")
	ctx := context.Background()
	data, err := config.ExportProfile(&domain.Profile{Name: "target", GameID: "g1", Mods: []domain.ModReference{{
		SourceID: "src", ModID: "bugs", Version: "1.0", FileIDs: []string{"main-1", "patch-1"},
		FomodFiles: domain.FomodFiles{"main-1": answer("4K"), "patch-1": patchAnswer("Vanilla")},
	}}})
	require.NoError(t, err)
	plan, err := f.svc.PlanImport(ctx, f.game, data)
	require.NoError(t, err)
	_, err = f.svc.ApplyImport(ctx, f.game, plan, core.ProfileImportOptions{Install: true}, nil)
	require.NoError(t, err)
	assert.Equal(t, "4K-v1", f.deployed(t, "textures/t.dds"))
	assert.Equal(t, "VANILLA-v1", f.deployed(t, "bugs_patch.esp"))
	ref := f.ref(t, "target")
	assert.Len(t, ref.FomodFiles["patch-1"].ConfigSHA256, 64, "recorded canonically once installed")
}

// TestFomodPerFile_ReconfigureOneInstallerBesideAPlainFile: a reconfigure
// asks the mod's installer and installs its plain file as it is - the
// plain file is not "an archive that is not a FOMOD installer".
func TestFomodPerFile_ReconfigureOneInstallerBesideAPlainFile(t *testing.T) {
	f := newPerFileFixture(t)
	f.serve(t, "1.0", "1", "4K", "USSEP")
	plainZip := createTestZip(t, t.TempDir(), map[string]string{"readme.txt": "plain"})
	b, err := os.ReadFile(plainZip)
	require.NoError(t, err)
	f.mock.AddDownload("patch-1", b)
	ctx := context.Background()
	_, err = f.install(t, map[string]domain.FomodChoices{"src:bugs": answer("2K")})
	require.NoError(t, err)

	plan, err := f.svc.PlanFomodReconfigure(ctx, f.game, "default", "src", "bugs")
	require.NoError(t, err)
	assert.Empty(t, plan.Installers, "one installer: nothing to pick")
	_, err = f.svc.ApplyFomodReconfigure(ctx, f.game, plan, core.FomodReconfigureOptions{FileID: "main-1"}, nil)
	require.Error(t, err, "--file is for a mod with several installers")

	_, err = f.svc.ApplyFomodReconfigure(ctx, f.game, plan, core.FomodReconfigureOptions{}, nil)
	var q *core.FomodChoicesRequiredError
	require.True(t, errors.As(err, &q), "got %v", err)
	require.Len(t, q.Mods, 1)
	assert.Empty(t, q.Mods[0].FileID)
	_, err = f.svc.ApplyFomodReconfigure(ctx, f.game, plan, core.FomodReconfigureOptions{Fomod: map[string]domain.FomodChoices{"src:bugs": answer("4K")}}, nil)
	require.NoError(t, err)
	assert.Equal(t, "4K-v1", f.deployed(t, "textures/t.dds"))
	assert.Equal(t, "plain", f.deployed(t, "readme.txt"))
}

// overlapPatchZip is a patch installer that writes the main installer's
// own texture path too.
func overlapPatchZip(t *testing.T) []byte {
	t.Helper()
	cfg := strings.ReplaceAll(patchModuleConfig("USSEP"), `destination="bugs_patch.esp"`, `destination="textures\t.dds"`)
	zipPath := createTestZip(t, t.TempDir(), map[string]string{
		"fomod/ModuleConfig.xml": cfg,
		"p/ussep.esp":            "PATCH-TEX",
		"p/vanilla.esp":          "PATCH-TEX-VANILLA",
	})
	b, err := os.ReadFile(zipPath)
	require.NoError(t, err)
	return b
}

// TestFomodPerFile_OneCanonicalLayerOrder (#634): two installers of one
// mod that write the same path layer in ONE order whichever flow installs
// them and in whatever order the files are named - by file id, unless the
// reference's fomod_order (a collection's rules between the mod's files)
// says otherwise - and that order is part of the variant, so the same
// choices in two orders are the same tree under the same key, and a
// different order is a different key.
func TestFomodPerFile_OneCanonicalLayerOrder(t *testing.T) {
	answers := domain.FomodFiles{"main-1": answer("4K"), "patch-1": patchAnswer("USSEP")}
	keyed := map[string]domain.FomodChoices{
		domain.FomodFileKey("src", "bugs", "main-1"):  answer("4K"),
		domain.FomodFileKey("src", "bugs", "patch-1"): patchAnswer("USSEP"),
	}
	install := func(t *testing.T, f *perFileFixture, profile string, order []string) {
		t.Helper()
		plan, err := f.svc.PlanInstall(context.Background(), f.game, profile, "src", "bugs", false)
		require.NoError(t, err)
		plan.Files = nil
		for _, id := range order {
			for _, file := range f.mock.files {
				if file.ID == id {
					plan.Files = append(plan.Files, file)
				}
			}
		}
		_, err = f.svc.ApplyInstall(context.Background(), f.game, plan, core.InstallOptions{TargetFileIDs: order, Fomod: keyed}, nil)
		require.NoError(t, err)
	}
	newOverlap := func(t *testing.T) *perFileFixture {
		f := newPerFileFixture(t)
		f.serve(t, "1.0", "1", "4K", "USSEP")
		f.mock.AddDownload("patch-1", overlapPatchZip(t))
		return f
	}
	ctx := context.Background()

	// install --file main,patch and --file patch,main: one tree, one key.
	f := newOverlap(t)
	install(t, f, "default", []string{"main-1", "patch-1"})
	first, err := f.svc.GetInstalledMod(ctx, "src", "bugs", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "PATCH-TEX", f.deployed(t, "textures/t.dds"), "by file id: patch-1 layers after main-1")
	install(t, f, "default", []string{"patch-1", "main-1"})
	second, err := f.svc.GetInstalledMod(ctx, "src", "bugs", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "PATCH-TEX", f.deployed(t, "textures/t.dds"), "the order the files are named in does not matter")
	assert.Equal(t, first.Variant, second.Variant)

	// An import (source order) reaches the same tree and key.
	f2 := newOverlap(t)
	data, err := config.ExportProfile(&domain.Profile{Name: "target", GameID: "g1", Mods: []domain.ModReference{{
		SourceID: "src", ModID: "bugs", Version: "1.0", FileIDs: []string{"patch-1", "main-1"}, FomodFiles: answers,
	}}})
	require.NoError(t, err)
	plan, err := f2.svc.PlanImport(ctx, f2.game, data)
	require.NoError(t, err)
	_, err = f2.svc.ApplyImport(ctx, f2.game, plan, core.ProfileImportOptions{Install: true}, nil)
	require.NoError(t, err)
	assert.Equal(t, "PATCH-TEX", f2.deployed(t, "textures/t.dds"))
	imported, err := f2.svc.GetInstalledMod(ctx, "src", "bugs", "g1", "target")
	require.NoError(t, err)
	assert.Equal(t, first.Variant, imported.Variant, "the same choices in the same order are the same key")

	// A collection's rule puts the patch first: the main file wins, under
	// another key, and the hint is kept on the reference.
	f3 := newOverlap(t)
	data, err = config.ExportProfile(&domain.Profile{Name: "target", GameID: "g1", Mods: []domain.ModReference{{
		SourceID: "src", ModID: "bugs", Version: "1.0", FileIDs: []string{"main-1", "patch-1"}, FomodFiles: answers,
		FomodOrder: []string{"patch-1", "main-1"},
	}}})
	require.NoError(t, err)
	plan, err = f3.svc.PlanImport(ctx, f3.game, data)
	require.NoError(t, err)
	_, err = f3.svc.ApplyImport(ctx, f3.game, plan, core.ProfileImportOptions{Install: true}, nil)
	require.NoError(t, err)
	assert.Equal(t, "4K-v1", f3.deployed(t, "textures/t.dds"))
	hinted, err := f3.svc.GetInstalledMod(ctx, "src", "bugs", "g1", "target")
	require.NoError(t, err)
	assert.NotEqual(t, first.Variant, hinted.Variant, "another layer order is another tree")
	ref := f3.ref(t, "target")
	assert.Equal(t, []string{"patch-1", "main-1"}, ref.FomodOrder)
	// A reinstall, files named the other way, keeps the hinted order.
	install(t, f3, "target", []string{"main-1", "patch-1"})
	assert.Equal(t, "4K-v1", f3.deployed(t, "textures/t.dds"))
}

// TestFomodPerFile_VerifyFixRebuildKeepsTheTree (#634): verify --fix's
// one-file repair rebuilds the whole entry in its layer order, so the
// overlapped path keeps its winner and the row keeps its variant.
func TestFomodPerFile_VerifyFixRebuildKeepsTheTree(t *testing.T) {
	f := newPerFileFixture(t)
	f.serve(t, "1.0", "1", "4K", "USSEP")
	f.mock.AddDownload("patch-1", overlapPatchZip(t))
	ctx := context.Background()
	plan, err := f.svc.PlanInstall(ctx, f.game, "default", "src", "bugs", false)
	require.NoError(t, err)
	// Named patch first, so the row lists it first: a repair file by file,
	// in the row's order, would leave the main file's texture on top.
	plan.Files = []domain.DownloadableFile{f.mock.files[1], f.mock.files[0]}
	_, err = f.svc.ApplyInstall(ctx, f.game, plan, core.InstallOptions{SkipVerify: true, TargetFileIDs: []string{"patch-1", "main-1"},
		Fomod: perFileAnswers("4K", "USSEP", "1")}, nil)
	require.NoError(t, err)
	before, err := f.svc.GetInstalledMod(ctx, "src", "bugs", "g1", "default")
	require.NoError(t, err)
	require.Equal(t, "PATCH-TEX", f.deployed(t, "textures/t.dds"))

	// No checksums were recorded, so --fix re-downloads each file.
	_, err = f.svc.VerifyForTest(ctx, f.game, "default", core.VerifyOptions{Fix: true}, nil)
	require.NoError(t, err)
	after, err := f.svc.GetInstalledMod(ctx, "src", "bugs", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, before.Variant, after.Variant)
	files, err := f.svc.GetGameCache(f.game).ListFiles(cacheKeyOf(after))
	require.NoError(t, err)
	assert.Contains(t, files, filepath.Join("textures", "t.dds"))
	b, err := os.ReadFile(filepath.Join(f.svc.GetGameCache(f.game).ModPath(cacheKeyOf(after)), "textures", "t.dds"))
	require.NoError(t, err)
	assert.Equal(t, "PATCH-TEX", string(b), "the overlapped path keeps the winner its layer order gives")
	for _, id := range []string{"main-1", "patch-1"} {
		assert.NotEmpty(t, storedChecksum(t, f.svc, "g1", "src", "bugs", id), "file %s's checksum was filled", id)
	}
}

// TestFomodPerFile_UpdatePairsFilesByNameNotPosition (#634): with no
// replacement from the source for one of the files, and the new files
// listed in another order than the row's, that file's choices go to the
// new file of the same name - never to the other file's installer by
// position.
func TestFomodPerFile_UpdatePairsFilesByNameNotPosition(t *testing.T) {
	f := newPerFileFixture(t)
	f.serve(t, "1.0", "1", "4K", "USSEP")
	ctx := context.Background()
	_, err := f.install(t, perFileAnswers("2K", "Vanilla", "1"))
	require.NoError(t, err)
	row, err := f.svc.GetInstalledMod(ctx, "src", "bugs", "g1", "default")
	require.NoError(t, err)
	require.Equal(t, []string{"main-1", "patch-1"}, row.FileIDs)

	f.serve(t, "1.1", "2", "4K", "USSEP")
	// The source still lists the old files, and the new ones patch first.
	old := []domain.DownloadableFile{
		{ID: "main-1", Name: "Scrambled Bugs", FileName: "main-1.zip", Version: "1.0", Category: "MAIN"},
		{ID: "patch-1", Name: "Scrambled Bugs - Patches", FileName: "patch-1.zip", Version: "1.0", Category: "OPTIONAL"},
	}
	f.mock.files = append([]domain.DownloadableFile{f.mock.files[1], f.mock.files[0]}, old...)
	up, err := f.svc.PlanUpdateFrom(ctx, f.game, "default", domain.Update{InstalledMod: *row, NewVersion: "1.1",
		FileIDReplacements: map[string]string{"main-1": "main-2"}})
	require.NoError(t, err)
	_, err = f.svc.ApplyUpdate(ctx, f.game, up, core.UpdateOptions{}, nil)
	require.NoError(t, err, "each file replays its own choices, so nothing asks")
	assert.Equal(t, "2K-v2", f.deployed(t, "textures/t.dds"))
	assert.Equal(t, "VANILLA-v2", f.deployed(t, "bugs_patch.esp"))
	ref := f.ref(t, "default")
	assert.Equal(t, "Vanilla", picked(ref.FomodFiles["patch-2"]))
	assert.Equal(t, "2K", picked(ref.FomodFiles["main-2"]))
}

// TestFomodPerFile_ModAnswerForSeveralInstallersIsRefused (#634): one
// answer under the mod's key cannot answer an installer in each of its
// files; the refusal says to key each file's.
func TestFomodPerFile_ModAnswerForSeveralInstallersIsRefused(t *testing.T) {
	f := newPerFileFixture(t)
	f.serve(t, "1.0", "1", "4K", "USSEP")
	_, err := f.install(t, map[string]domain.FomodChoices{"src:bugs": answer("4K")})
	var refusal *core.FomodInstallError
	require.True(t, errors.As(err, &refusal), "got %v", err)
	assert.Contains(t, refusal.Reason, "src:bugs#main-1")
	assert.Empty(t, f.deployed(t, "textures/t.dds"), "nothing was installed")
}

// TestFomodPerFile_RollbackAfterAReconfigureKeepsTheLayerHint (#634
// re-review): install with a collection's layer-order hint, update,
// reconfigure one file, roll back. The reconfigure carries the update's
// previous choices forward - their hint included - so the rollback puts
// the hint back with the old tree, and the reference names the row's own
// variant again: nothing for a converge flow to undo.
func TestFomodPerFile_RollbackAfterAReconfigureKeepsTheLayerHint(t *testing.T) {
	f := newPerFileFixture(t)
	f.serve(t, "1.0", "1", "4K", "USSEP")
	f.mock.AddDownload("patch-1", overlapPatchZip(t))
	ctx := context.Background()
	data, err := config.ExportProfile(&domain.Profile{Name: "default", GameID: "g1", Mods: []domain.ModReference{{
		SourceID: "src", ModID: "bugs", Version: "1.0", FileIDs: []string{"main-1", "patch-1"},
		FomodFiles: domain.FomodFiles{"main-1": answer("4K"), "patch-1": patchAnswer("USSEP")},
		FomodOrder: []string{"patch-1", "main-1"},
	}}})
	require.NoError(t, err)
	plan, err := f.svc.PlanImport(ctx, f.game, data)
	require.NoError(t, err)
	_, err = f.svc.ApplyImport(ctx, f.game, plan, core.ProfileImportOptions{Install: true, Force: true}, nil)
	require.NoError(t, err)
	require.Equal(t, "4K-v1", f.deployed(t, "textures/t.dds"), "the hint puts the main file last")
	installed, err := f.svc.GetInstalledMod(ctx, "src", "bugs", "g1", "default")
	require.NoError(t, err)

	f.serve(t, "1.1", "2", "4K", "USSEP")
	f.mock.AddDownload("patch-2", overlapPatchZip(t))
	up, err := f.svc.PlanUpdateFrom(ctx, f.game, "default", domain.Update{InstalledMod: *installed, NewVersion: "1.1",
		FileIDReplacements: map[string]string{"main-1": "main-2", "patch-1": "patch-2"}})
	require.NoError(t, err)
	_, err = f.svc.ApplyUpdate(ctx, f.game, up, core.UpdateOptions{}, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"patch-2", "main-2"}, f.ref(t, "default").FomodOrder, "the update carried the hint")

	rc, err := f.svc.PlanFomodReconfigure(ctx, f.game, "default", "src", "bugs")
	require.NoError(t, err)
	_, err = f.svc.ApplyFomodReconfigure(ctx, f.game, rc, core.FomodReconfigureOptions{FileID: "patch-2",
		Fomod: map[string]domain.FomodChoices{domain.FomodFileKey("src", "bugs", "patch-2"): patchAnswer("Vanilla")}}, nil)
	require.NoError(t, err)
	row, err := f.svc.GetInstalledMod(ctx, "src", "bugs", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, []string{"patch-1", "main-1"}, row.PreviousFomodOrder, "the reconfigure kept the update's rollback record whole")

	rb, err := f.svc.PlanRollback(ctx, f.game, "default", "src", "bugs")
	require.NoError(t, err)
	_, err = f.svc.ApplyRollback(ctx, f.game, rb, core.RollbackOptions{}, nil)
	require.NoError(t, err)
	restored, err := f.svc.GetInstalledMod(ctx, "src", "bugs", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, installed.Variant, restored.Variant)
	ref := f.ref(t, "default")
	assert.Equal(t, []string{"patch-1", "main-1"}, ref.FomodOrder, "the hint is back with the old tree")
	assert.Equal(t, "4K-v1", f.deployed(t, "textures/t.dds"))

	// A converge run finds the reference and the row in agreement. (The
	// source still lists the old files.)
	f.mock.files = append(f.mock.files,
		domain.DownloadableFile{ID: "main-1", Name: "Scrambled Bugs", FileName: "main-1.zip", Version: "1.0", Category: "MAIN"},
		domain.DownloadableFile{ID: "patch-1", Name: "Scrambled Bugs - Patches", FileName: "patch-1.zip", Version: "1.0", Category: "OPTIONAL"})
	apply, err := f.svc.PlanProfileApply(ctx, f.game, "default")
	require.NoError(t, err)
	for _, e := range apply.ToInstall {
		assert.NotEqual(t, "bugs", e.Ref.ModID, "nothing to reinstall: %+v", e)
	}
}

// TestFomodPerFile_TwoQuestionsAreOneDownloadFailure (#634): a mod whose
// two installers both ask reports one download failure - the mod's
// question - not one per asking file; a lone installer's question still
// reports its one.
func TestFomodPerFile_TwoQuestionsAreOneDownloadFailure(t *testing.T) {
	failures := func(t *testing.T, f *perFileFixture) int {
		t.Helper()
		plan, err := f.svc.PlanInstall(context.Background(), f.game, "default", "src", "bugs", false)
		require.NoError(t, err)
		plan.Files = slices.Clone(f.mock.files)
		n := 0
		_, err = f.svc.ApplyInstall(context.Background(), f.game, plan, core.InstallOptions{TargetFileIDs: []string{"main-1", "patch-1"}}, func(e core.Event) {
			if m, ok := e.(core.ModEvent); ok && m.Phase == core.InstallDownloadFailed {
				n++
			}
		})
		var q *core.FomodChoicesRequiredError
		require.True(t, errors.As(err, &q), "got %v", err)
		return n
	}
	f := newPerFileFixture(t)
	f.serve(t, "1.0", "1", "4K", "USSEP")
	assert.Equal(t, 1, failures(t, f), "two installers ask: one failure")

	plain := newPerFileFixture(t)
	plain.serve(t, "1.0", "1", "4K", "USSEP")
	zipPath := createTestZip(t, t.TempDir(), map[string]string{"readme.txt": "plain"})
	b, err := os.ReadFile(zipPath)
	require.NoError(t, err)
	plain.mock.AddDownload("patch-1", b)
	assert.Equal(t, 1, failures(t, plain), "one installer asks: one failure, as before")
}
