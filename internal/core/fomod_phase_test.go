package core_test

// #609: a source collection's install phases reach the FOMOD resolve step.
// An item's fileDependency conditions see the files of EARLIER phases'
// mods as present - acquired, not yet committed, since a collection Apply
// writes nothing until every item is acquired - and never a later phase's,
// as Vortex installs phase by phase. And the recipe's source.md5 pins the
// recorded choices to the curator's archive on this path too.

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// phasedSource is collectionSource whose recipe states each item's phase,
// md5 and installer choices.
type phasedSource struct {
	*collectionSource
	phase   map[string]int
	md5     map[string]string
	choices map[string][]domain.FomodStepChoice
}

func (s *phasedSource) ParseManifest(root fs.FS) (source.CollectionManifest, error) {
	m, err := s.recipe.ParseManifest(root)
	for i := range m.Mods {
		id := m.Mods[i].ModID
		m.Mods[i].Phase = s.phase[id]
		m.Mods[i].MD5 = s.md5[id]
		if c, ok := s.choices[id]; ok {
			m.Mods[i].InstallerType, m.Mods[i].Choices = "fomod", c
		}
	}
	return m, err
}

// phasedInstaller is an installer whose conditional installs add
// patch.txt when early.esp is active and late.txt when late.esp is.
func phasedInstaller(t *testing.T) []byte {
	t.Helper()
	cfg := strings.Replace(flowModuleConfig("4K"), "</installSteps>", `</installSteps>
<conditionalFileInstalls><patterns>
<pattern><dependencies><fileDependency file="early.esp" state="Active"/></dependencies><files><file source="core\plugin.esp" destination="patch.txt"/></files></pattern>
<pattern><dependencies><fileDependency file="late.esp" state="Active"/></dependencies><files><file source="core\plugin.esp" destination="late.txt"/></files></pattern>
</patterns></conditionalFileInstalls>`, 1)
	return createTestZipBytes(t, map[string]string{
		"fomod/ModuleConfig.xml": cfg, "core/plugin.esp": "PLUGIN", "tex/2k.dds": "2K", "tex/4k.dds": "4K",
	})
}

type phasedFixture struct {
	svc       *core.Service
	game      *domain.Game
	src       *phasedSource
	installer []byte
}

// newPhasedFixture serves a three-item collection: 10 (early.esp) in
// phase 0, 11 (the installer, its 4K choice recorded) in phase 1, and 12
// (late.esp) in phase 2. The recipe lists them in the order given.
func newPhasedFixture(t *testing.T, recipe string) *phasedFixture {
	t.Helper()
	archive := recipeArchive(t, recipe)
	cdn := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(archive) }))
	t.Cleanup(cdn.Close)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(), DownloadClient: cdn.Client(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	downloads := newMockSourceWithDownloads("nexusmods")
	t.Cleanup(downloads.Close)
	items := []source.CollectionItem{
		{Kind: source.CollectionItemModFile, ModID: "10", FileID: "100", Name: "Early"},
		{Kind: source.CollectionItemModFile, ModID: "11", FileID: "110", Name: "Installer"},
		{Kind: source.CollectionItemModFile, ModID: "12", FileID: "120", Name: "Late"},
	}
	installer := phasedInstaller(t)
	src := &phasedSource{
		collectionSource: &collectionSource{
			mockSourceWithDownloads: downloads,
			recipe: &recipeSource{mockSource: downloads.mockSource, archiveURL: cdn.URL + "/recipe.7z",
				collection: source.Collection{ID: "phased", Name: "Phased", Revision: 1, Author: "curator",
					URL: "https://www.nexusmods.com/games/skyrimspecialedition/collections/phased", Handle: "/h", Items: items}},
			tier:       source.TierPremium,
			filesByMod: map[string][]string{"10": {"100"}, "11": {"110"}, "12": {"120"}},
		},
		phase: map[string]int{"10": 0, "11": 1, "12": 2},
		md5:   map[string]string{},
		choices: map[string][]domain.FomodStepChoice{"11": {{Name: "Main", Groups: []domain.FomodGroupChoice{
			{Name: "Texture", Choices: []domain.FomodOptionChoice{{Name: "4K"}}}}}}},
	}
	for id, payload := range map[string][]byte{
		"100": createTestZipBytes(t, map[string]string{"early.esp": "EARLY"}),
		"110": installer,
		"120": createTestZipBytes(t, map[string]string{"late.esp": "LATE"}),
	} {
		downloads.AddDownload(id, payload)
	}
	for _, id := range []string{"10", "11", "12"} {
		downloads.AddMod("skyrimspecialedition", &domain.Mod{ID: id, SourceID: "nexusmods", Name: "Mod " + id, Version: "1", GameID: "skyrimspecialedition"})
	}
	svc.RegisterSource(src)
	game := &domain.Game{ID: "skyrim", Name: "Skyrim", ModPath: t.TempDir(), LinkMethod: domain.LinkCopy,
		SourceIDs: map[string]string{"nexusmods": "skyrimspecialedition"}}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	return &phasedFixture{svc: svc, game: game, src: src, installer: installer}
}

const phasedRecipe = `{"name": "Phased", "mods": [
	{"name": "Early", "modId": "10", "fileId": "100"},
	{"name": "Installer", "modId": "11", "fileId": "110"},
	{"name": "Late", "modId": "12", "fileId": "120"}]}`

func (f *phasedFixture) apply(t *testing.T, answers map[string]domain.FomodChoices) (*core.ImportPlan, error) {
	t.Helper()
	plan, err := f.svc.PlanSourceCollectionImport(context.Background(), f.game, core.SourceCollectionImportRequest{SourceID: "nexusmods", Ref: "phased"})
	require.NoError(t, err)
	require.Empty(t, plan.Collection.Unsupported)
	_, err = f.svc.ApplyCollectionImport(context.Background(), f.game, plan, core.ProfileImportOptions{Install: true, Fomod: answers}, nil)
	return plan, err
}

func (f *phasedFixture) installerRef(t *testing.T) *domain.ModReference {
	t.Helper()
	p, err := f.svc.NewProfileManager().Get(context.Background(), "skyrim", "Phased")
	require.NoError(t, err)
	ref := p.FindRef("nexusmods", "11")
	require.NotNil(t, ref)
	return ref
}

// installerFiles is the installer's tree: its cache entry's files (an
// import into a profile that is not the active one deploys nothing).
func (f *phasedFixture) installerFiles(t *testing.T) []string {
	t.Helper()
	row, err := f.svc.GetInstalledMod(context.Background(), "nexusmods", "11", "skyrim", "Phased")
	require.NoError(t, err)
	files, err := f.svc.GetGameCache(f.game).ListFiles(cacheKeyOf(row))
	require.NoError(t, err)
	return files
}

// TestCollectionPhases_AnInstallerSeesEarlierPhasesOnly: phase 1's
// installer sees phase 0's early.esp - not installed yet, only acquired -
// and not phase 2's late.esp.
func TestCollectionPhases_AnInstallerSeesEarlierPhasesOnly(t *testing.T) {
	f := newPhasedFixture(t, phasedRecipe)
	_, err := f.apply(t, nil)
	require.NoError(t, err)
	files := f.installerFiles(t)
	assert.Contains(t, files, "patch.txt", "an earlier phase's plugin is present")
	assert.NotContains(t, files, "late.txt", "a later phase's is not")
	assert.Equal(t, map[string]string{"early.esp": "Active", "late.esp": "Missing"}, f.installerRef(t).Fomod.FileStates)
}

// TestCollectionPhases_RecipeOrderDoesNotDecide: the recipe lists the
// later phase first; the phases still decide what the installer sees.
func TestCollectionPhases_RecipeOrderDoesNotDecide(t *testing.T) {
	f := newPhasedFixture(t, `{"name": "Phased", "mods": [
	{"name": "Late", "modId": "12", "fileId": "120"},
	{"name": "Installer", "modId": "11", "fileId": "110"},
	{"name": "Early", "modId": "10", "fileId": "100"}]}`)
	_, err := f.apply(t, nil)
	require.NoError(t, err)
	files := f.installerFiles(t)
	assert.Contains(t, files, "patch.txt")
	assert.NotContains(t, files, "late.txt")
}

// TestCollectionPhases_ASamePhaseModIsNotYetPresent: Vortex deploys a
// phase only once all of it is installed, so a mod of the SAME phase is not
// present to the installer either.
func TestCollectionPhases_ASamePhaseModIsNotYetPresent(t *testing.T) {
	f := newPhasedFixture(t, phasedRecipe)
	f.src.phase["10"] = 1
	_, err := f.apply(t, nil)
	require.NoError(t, err)
	assert.NotContains(t, f.installerFiles(t), "patch.txt")
	assert.Equal(t, "Missing", f.installerRef(t).Fomod.FileStates["early.esp"])
}

// TestCollectionPhases_AlreadyCachedEarlierPhaseIsPresent: an earlier
// phase's mod another profile already has installs from its cache entry,
// without an acquire - and is present to the installer all the same.
func TestCollectionPhases_AlreadyCachedEarlierPhaseIsPresent(t *testing.T) {
	f := newPhasedFixture(t, phasedRecipe)
	seedNamedInstalledMod(t, f.svc, f.game, "nexusmods", "10", "Mod 10", "1", true, map[string][]byte{"early.esp": []byte("EARLY")})
	seedProfileWithMod(t, f.svc, "skyrim", "default", "nexusmods", "10", "1")
	plan, err := f.apply(t, nil)
	require.NoError(t, err)
	require.Len(t, plan.AlreadyCached, 1)
	assert.Contains(t, f.installerFiles(t), "patch.txt")
}

// TestCollectionSourceMD5_ADifferentArchiveAsks: the recipe's source.md5
// for the installer's file pins its recorded choices to the curator's
// archive: a different one asks, with the reason.
func TestCollectionSourceMD5_ADifferentArchiveAsks(t *testing.T) {
	f := newPhasedFixture(t, phasedRecipe)
	f.src.md5["11"] = strings.Repeat("f", 32)
	_, err := f.apply(t, nil)
	var q *core.FomodChoicesRequiredError
	require.True(t, errors.As(err, &q), "got %v", err)
	require.Len(t, q.Mods, 1)
	assert.Equal(t, core.FomodReasonArchiveMismatch, q.Mods[0].Reason)
	sum := md5.Sum(f.installer)
	require.NotNil(t, q.Mods[0].ArchiveMismatch)
	assert.Equal(t, hex.EncodeToString(sum[:]), q.Mods[0].ArchiveMismatch.ActualMD5)

	// The curator's own archive replays unasked.
	f.src.md5["11"] = hex.EncodeToString(sum[:])
	_, err = f.apply(t, nil)
	require.NoError(t, err)
	assert.Contains(t, f.installerFiles(t), "patch.txt")
}
