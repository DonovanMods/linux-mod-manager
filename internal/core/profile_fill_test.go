package core_test

// #608 C3: `lmm profile fill` - a free account's collection profile, every
// pin pending, filled from the archives the user downloaded by hand,
// matched by the size and md5 the collection's recipe records.

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fillRecipeMod is one mod of the fill fixture's recipe.
type fillRecipeMod struct {
	Name   string `json:"name"`
	ModID  string `json:"modId"`
	FileID string `json:"fileId"`
	MD5    string `json:"md5"`
	Size   int64  `json:"size"`
	Policy string `json:"policy"`
	Type   string `json:"type"`
}

// fillSource is a NexusMods-like source to a free account: it resolves
// collections, reads recipes, describes mods and lists files, and serves no
// downloads.
type fillSource struct {
	*mockSource
	collection source.Collection
	archiveURL string
	resolves   int
	modErr     error
	filesErr   error
	urlCalls   int
}

func (s *fillSource) ResolveGameCollection(_ context.Context, _, ref string) (source.Collection, error) {
	s.resolves++
	if ref != fmt.Sprintf("%s@%d", s.collection.ID, s.collection.Revision) {
		return source.Collection{}, fmt.Errorf("unexpected ref %q", ref)
	}
	return s.collection, nil
}

func (s *fillSource) ManifestArchiveURL(context.Context, source.Collection) (string, error) {
	return s.archiveURL, nil
}

func (s *fillSource) ParseManifest(root fs.FS) (source.CollectionManifest, error) {
	data, err := fs.ReadFile(root, "collection.json")
	if err != nil {
		return source.CollectionManifest{}, err
	}
	var mods []fillRecipeMod
	if err := json.Unmarshal(data, &mods); err != nil {
		return source.CollectionManifest{}, err
	}
	var m source.CollectionManifest
	for _, mod := range mods {
		m.Mods = append(m.Mods, source.ManifestMod{Name: mod.Name, SourceType: "nexus", ModID: mod.ModID, FileID: mod.FileID,
			MD5: mod.MD5, SizeBytes: mod.Size, UpdatePolicy: mod.Policy, ModType: mod.Type, Version: "1"})
	}
	return m, nil
}

func (s *fillSource) GetMod(ctx context.Context, gameID, modID string) (*domain.Mod, error) {
	if s.modErr != nil {
		return nil, s.modErr
	}
	return s.mockSource.GetMod(ctx, gameID, modID)
}

func (s *fillSource) GetModFiles(_ context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	if s.filesErr != nil {
		return nil, s.filesErr
	}
	id := mod.ID + "0"
	return []domain.DownloadableFile{{ID: id, Name: "File " + id, FileName: mod.ID + "-" + id + ".zip", Version: "1"}}, nil
}

func (s *fillSource) GetDownloadURL(context.Context, *domain.Mod, string) (string, error) {
	s.urlCalls++
	return "", &source.AccountRestrictedError{Reason: "free accounts download by hand"}
}

type fillFixture struct {
	svc       *core.Service
	game      *domain.Game
	src       *fillSource
	archives  map[string][]byte // mod id -> its archive's bytes
	recipe    []fillRecipeMod
	downloads string
	// recipeArchive is what the fixture's recipe server serves.
	recipeArchive []byte
}

func md5Hex(b []byte) string {
	sum := md5.Sum(b)
	return hex.EncodeToString(sum[:])
}

// fillSetup varies newFillFixture: the game (default: a plain one), the
// profile's pins (default: 10, 11 and 12).
type fillSetup struct {
	game func(t *testing.T, svc *core.Service) *domain.Game
	refs []domain.ModReference
}

// newFillFixture saves a collection profile "Coll" (active) whose pins are
// none installed, with the recipe cached where the collection import keeps
// it. By default 11 is "latest" and 10 is a locked "exact".
func newFillFixture(t *testing.T, setup fillSetup) *fillFixture {
	t.Helper()
	f := &fillFixture{archives: map[string][]byte{}, downloads: t.TempDir()}
	cdn := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(f.recipeArchive) }))
	t.Cleanup(cdn.Close)
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(), DownloadClient: cdn.Client(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	src := &fillSource{mockSource: newMockSource("nexusmods"), archiveURL: cdn.URL + "/recipe.7z",
		collection: source.Collection{ID: "abc", Revision: 2, Name: "Coll"}}
	svc.RegisterSource(src)
	game := &domain.Game{ID: "skyrim", Name: "Skyrim", ModPath: t.TempDir(), LinkMethod: domain.LinkCopy,
		SourceIDs: map[string]string{"nexusmods": "skyrimspecialedition"}}
	if setup.game != nil {
		game = setup.game(t, svc)
	} else {
		require.NoError(t, svc.SaveGame(context.Background(), game))
	}
	f.svc, f.game, f.src = svc, game, src
	refs := setup.refs
	if len(refs) == 0 {
		refs = []domain.ModReference{
			{SourceID: "nexusmods", ModID: "10", Version: "1", FileIDs: []string{"100"}, Locked: true},
			{SourceID: "nexusmods", ModID: "11", Version: "1", FileIDs: []string{"110"}},
			{SourceID: "nexusmods", ModID: "12", Version: "1", FileIDs: []string{"120"}},
		}
	}
	for i, ref := range refs {
		// Each archive a size of its own, as real downloads are.
		zip := createTestZipBytes(t, map[string]string{"mod" + ref.ModID + ".esp": "plugin " + ref.ModID + strings.Repeat(".", 100*i)})
		f.archives[ref.ModID] = zip
		src.AddMod("skyrimspecialedition", &domain.Mod{ID: ref.ModID, SourceID: "nexusmods", Name: "Mod " + ref.ModID, Version: "1", GameID: "skyrimspecialedition"})
		policy := "exact"
		if ref.ModID == "11" {
			policy = "latest"
		}
		f.recipe = append(f.recipe, fillRecipeMod{Name: "Mod " + ref.ModID, ModID: ref.ModID, FileID: ref.FileIDs[0],
			MD5: md5Hex(zip), Size: int64(len(zip)), Policy: policy, Type: ref.ModType})
	}
	f.writeRecipe(t)
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), &domain.Profile{
		Name: "Coll", GameID: game.ID, IsDefault: true, Mods: refs,
		Collection: &domain.CollectionInfo{ID: "nexusmods:abc", Origin: &domain.CollectionOrigin{SourceID: "nexusmods", Slug: "abc", Revision: 2}},
	}))
	return f
}

func (f *fillFixture) recipeDir() string {
	return filepath.Join(f.svc.GlobalCacheDir(), f.game.ID, "collections", "nexusmods-abc", "2")
}

func (f *fillFixture) writeRecipe(t *testing.T) {
	t.Helper()
	data, err := json.Marshal(f.recipe)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(f.recipeDir(), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(f.recipeDir(), "collection.json"), data, 0o644))
}

// download puts mod id's archive into the downloads directory as name.
func (f *fillFixture) download(t *testing.T, modID, name string) string {
	t.Helper()
	p := filepath.Join(f.downloads, name)
	require.NoError(t, os.WriteFile(p, f.archives[modID], 0o644))
	return p
}

func (f *fillFixture) plan(t *testing.T) *core.ProfileFillPlan {
	t.Helper()
	plan, err := f.svc.PlanProfileFill(context.Background(), f.game, core.ProfileFillRequest{Profile: "Coll", Paths: []string{f.downloads}})
	require.NoError(t, err)
	return plan
}

func matchedIDs(plan *core.ProfileFillPlan) []string {
	var out []string
	for _, m := range plan.Matched {
		out = append(out, m.ModID)
	}
	return out
}

func ignoredBy(plan *core.ProfileFillPlan) map[string]core.FillIgnoredReason {
	out := map[string]core.FillIgnoredReason{}
	for _, ig := range plan.Ignored {
		out[ig.Name] = ig.Reason
	}
	return out
}

func TestPlanProfileFill_MatchesBySizeThenChecksum(t *testing.T) {
	f := newFillFixture(t, fillSetup{})
	f.download(t, "10", "Mod 10-100-1-0.zip")
	// The size of 11's archive, other bytes: read, and refused.
	wrong := append([]byte(nil), f.archives["11"]...)
	wrong[len(wrong)-1] ^= 0xff
	require.NoError(t, os.WriteFile(filepath.Join(f.downloads, "impostor.zip"), wrong, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(f.downloads, "notes.txt"), []byte("unrelated"), 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(f.downloads, "folder"), 0o755))
	// A link to 12's real archive, kept outside: never followed.
	outside := filepath.Join(t.TempDir(), "12.zip")
	require.NoError(t, os.WriteFile(outside, f.archives["12"], 0o644))
	require.NoError(t, os.Symlink(outside, filepath.Join(f.downloads, "link.zip")))
	require.NoError(t, syscall.Mkfifo(filepath.Join(f.downloads, "pipe"), 0o644))

	plan := f.plan(t)
	assert.Equal(t, []string{"10"}, matchedIDs(plan))
	require.Len(t, plan.Matched[0].Files, 1)
	assert.Equal(t, "Mod 10-100-1-0.zip", plan.Matched[0].Files[0].Archive)
	assert.Equal(t, "Mod 10", plan.Matched[0].Name)
	assert.Equal(t, map[string]core.FillIgnoredReason{
		"impostor.zip": core.FillIgnoredChecksumMismatch,
		"notes.txt":    core.FillIgnoredNoMatch,
		"link.zip":     core.FillIgnoredSymlink,
		"pipe":         core.FillIgnoredNotRegular,
	}, ignoredBy(plan))
	assert.Equal(t, 2, plan.Hashed, "only the two files of a pending pin's size were read")
	require.Len(t, plan.Pending, 2)
	for _, p := range plan.Pending {
		assert.Equal(t, core.FillNoArchive, p.Reason)
	}
	assert.Equal(t, &domain.CollectionOrigin{SourceID: "nexusmods", Slug: "abc", Revision: 2}, plan.Collection)
}

func TestApplyProfileFill_InstallsTheMatchesAndLeavesTheFilesAlone(t *testing.T) {
	f := newFillFixture(t, fillSetup{})
	p10 := f.download(t, "10", "a.zip")
	p11 := f.download(t, "11", "b.zip")
	plan := f.plan(t)
	require.ElementsMatch(t, []string{"10", "11"}, matchedIDs(plan))
	assert.True(t, plan.Matched[1].AutoUpdate || plan.Matched[0].AutoUpdate, "11 is the curator's latest")

	result, err := f.svc.ApplyProfileFill(context.Background(), f.game, plan, core.ProfileFillOptions{}, nil)
	require.NoError(t, err)
	assert.Equal(t, 2, result.Installed)
	assert.Equal(t, 1, result.Remaining)
	assert.Equal(t, 0, f.src.urlCalls, "nothing was asked of the source to download")

	ctx := context.Background()
	row, err := f.svc.GetInstalledMod(ctx, "nexusmods", "10", f.game.ID, "Coll")
	require.NoError(t, err)
	assert.Equal(t, domain.UpdateNotify, row.UpdatePolicy)
	row, err = f.svc.GetInstalledMod(ctx, "nexusmods", "11", f.game.ID, "Coll")
	require.NoError(t, err)
	assert.Equal(t, domain.UpdateAuto, row.UpdatePolicy, "latest: installs the tested file, then follows the newest")
	assert.Equal(t, []string{"110"}, row.FileIDs)
	_, err = os.Stat(filepath.Join(f.game.ModPath, "mod11.esp"))
	assert.NoError(t, err, "deployed")

	saved, err := config.LoadProfile(f.svc.ConfigDir(), f.game.ID, "Coll")
	require.NoError(t, err)
	assert.True(t, saved.FindRef("nexusmods", "10").Locked, "the exact pin keeps its lock")
	assert.False(t, saved.FindRef("nexusmods", "11").Locked)

	for _, p := range []string{p10, p11} {
		_, err := os.Stat(p)
		assert.NoError(t, err, "the user's file is never moved or deleted")
	}
	again := f.plan(t)
	assert.Empty(t, again.Matched, "installed pins are no longer pending")
	assert.Len(t, again.Pending, 1)
	assert.Equal(t, core.FillIgnoredNoMatch, ignoredBy(again)["a.zip"])
}

func TestApplyProfileFill_AFileChangedSincePlanFailsThatModOnly(t *testing.T) {
	f := newFillFixture(t, fillSetup{})
	f.download(t, "10", "a.zip")
	p11 := f.download(t, "11", "b.zip")
	plan := f.plan(t)
	require.Len(t, plan.Matched, 2)
	changed := append([]byte(nil), f.archives["11"]...)
	changed[0] ^= 0xff
	require.NoError(t, os.WriteFile(p11, changed, 0o644))

	result, err := f.svc.ApplyProfileFill(context.Background(), f.game, plan, core.ProfileFillOptions{}, nil)
	var incomplete *core.ProfileFillIncompleteError
	require.True(t, errors.As(err, &incomplete), "got %v", err)
	assert.Equal(t, 1, result.Installed)
	assert.Equal(t, 1, result.Failed)
	require.Len(t, result.Failures, 1)
	assert.Contains(t, result.Failures[0].Reason, "changed since the fill was planned")
	assert.Equal(t, 2, result.Remaining)
	_, err = f.svc.GetInstalledMod(context.Background(), "nexusmods", "11", f.game.ID, "Coll")
	assert.Error(t, err)
}

func TestApplyProfileFill_StalePinIsRefused(t *testing.T) {
	f := newFillFixture(t, fillSetup{})
	f.download(t, "12", "c.zip")
	plan := f.plan(t)
	require.Len(t, plan.Matched, 1)
	profile, err := config.LoadProfile(f.svc.ConfigDir(), f.game.ID, "Coll")
	require.NoError(t, err)
	profile.FindRef("nexusmods", "12").FileIDs = []string{"121"}
	require.NoError(t, config.SaveProfile(f.svc.ConfigDir(), profile))

	_, err = f.svc.ApplyProfileFill(context.Background(), f.game, plan, core.ProfileFillOptions{}, nil)
	assert.ErrorIs(t, err, core.ErrStalePlan)
}

func TestPlanProfileFill_RefetchesAMissingRecipe(t *testing.T) {
	f := newFillFixture(t, fillSetup{})
	data, err := json.Marshal(f.recipe)
	require.NoError(t, err)
	f.recipeArchive = recipeArchiveOf(t, func(dir string) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "collection.json"), data, 0o644))
	})
	require.NoError(t, os.RemoveAll(f.recipeDir()))
	f.download(t, "12", "c.zip")

	plan := f.plan(t)
	assert.Equal(t, 1, f.src.resolves, "the collection is resolved at the profile's revision")
	assert.Equal(t, []string{"12"}, matchedIDs(plan))
	_, err = os.Stat(filepath.Join(f.recipeDir(), "collection.json"))
	assert.NoError(t, err, "the recipe is cached again")
}

func TestPlanProfileFill_AnUnreadableRecipeMatchesNothingAndSaysWhy(t *testing.T) {
	f := newFillFixture(t, fillSetup{})
	require.NoError(t, os.RemoveAll(f.recipeDir()))
	f.src.collection.Revision = 3 // the source no longer resolves revision 2
	f.download(t, "12", "c.zip")
	plan := f.plan(t)
	assert.Empty(t, plan.Matched)
	require.Len(t, plan.Pending, 3)
	assert.Equal(t, core.FillNoChecksum, plan.Pending[0].Reason)
	require.NotEmpty(t, plan.Notes)
	assert.Contains(t, plan.Notes[0], "recipe could not be read")
	assert.Equal(t, 0, plan.Hashed, "nothing to match, nothing read")
}

func TestPlanProfileFill_AProfileWithoutACollectionIsRefused(t *testing.T) {
	f := newFillFixture(t, fillSetup{})
	require.NoError(t, config.SaveProfile(f.svc.ConfigDir(), &domain.Profile{Name: "plain", GameID: "skyrim"}))
	_, err := f.svc.PlanProfileFill(context.Background(), f.game, core.ProfileFillRequest{Profile: "plain", Paths: []string{f.downloads}})
	assert.ErrorIs(t, err, core.ErrProfileNotFillable)
}

func TestPlanProfileFill_HashingIsBoundedPerSize(t *testing.T) {
	f := newFillFixture(t, fillSetup{})
	same := len(f.archives["12"])
	for i := range 10 {
		b := make([]byte, same)
		b[0] = byte(i + 1)
		require.NoError(t, os.WriteFile(filepath.Join(f.downloads, fmt.Sprintf("junk%02d", i)), b, 0o644))
	}
	plan := f.plan(t)
	reasons := map[core.FillIgnoredReason]int{}
	for _, ig := range plan.Ignored {
		reasons[ig.Reason]++
	}
	assert.LessOrEqual(t, plan.Hashed, 4+0, "at most four wrong guesses per size")
	assert.Positive(t, reasons[core.FillIgnoredHashLimit])
}

func TestPlanProfileFill_DuplicateDownloadsAndUploadNames(t *testing.T) {
	f := newFillFixture(t, fillSetup{})
	staged := t.TempDir()
	a := filepath.Join(staged, "upload-1")
	b := filepath.Join(staged, "upload-2")
	require.NoError(t, os.WriteFile(a, f.archives["10"], 0o644))
	require.NoError(t, os.WriteFile(b, f.archives["10"], 0o644))
	plan, err := f.svc.PlanProfileFill(context.Background(), f.game, core.ProfileFillRequest{Profile: "Coll",
		Archives: []core.FillArchive{{Path: a, Name: "Mod Ten.zip"}, {Path: b, Name: "Mod Ten (1).zip"}}})
	require.NoError(t, err)
	assert.Equal(t, []string{"10"}, matchedIDs(plan))
	assert.Equal(t, "Mod Ten.zip", plan.Matched[0].Files[0].Archive)
	assert.Equal(t, core.FillIgnoredNoMatch, ignoredBy(plan)["Mod Ten (1).zip"], "its pin is matched; the copy is not read")
}

func TestApplyProfileFill_SourceUnreachableInstallsFromTheCollectionsFacts(t *testing.T) {
	f := newFillFixture(t, fillSetup{})
	f.download(t, "12", "c.zip")
	plan := f.plan(t)
	f.src.modErr = errors.New("offline")
	f.src.filesErr = errors.New("offline")
	result, err := f.svc.ApplyProfileFill(context.Background(), f.game, plan, core.ProfileFillOptions{}, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Installed)
	assert.NotEmpty(t, result.Warnings)
	row, err := f.svc.GetInstalledMod(context.Background(), "nexusmods", "12", f.game.ID, "Coll")
	require.NoError(t, err)
	assert.Equal(t, "Mod 12", row.Name, "the collection's name for it")
	assert.Equal(t, "1", row.Version)
}

func TestApplyProfileFill_ReplaysRecordedChoicesAndAsksOtherwise(t *testing.T) {
	refs := []domain.ModReference{
		{SourceID: "nexusmods", ModID: "20", Version: "1", FileIDs: []string{"200"}, Fomod: answer("4K")},
		{SourceID: "nexusmods", ModID: "21", Version: "1", FileIDs: []string{"210"}},
	}
	f := newFillFixture(t, fillSetup{refs: refs})
	f.archives["20"] = fomodZip(t, "4K", "a")
	f.archives["21"] = fomodZip(t, "4K", "b")
	for i := range f.recipe {
		zip := f.archives[f.recipe[i].ModID]
		f.recipe[i].MD5, f.recipe[i].Size = md5Hex(zip), int64(len(zip))
	}
	f.writeRecipe(t)
	f.download(t, "20", "twenty.zip")
	f.download(t, "21", "twentyone.zip")
	ctx := context.Background()

	plan := f.plan(t)
	require.Len(t, plan.Matched, 2)
	_, err := f.svc.ApplyProfileFill(ctx, f.game, plan, core.ProfileFillOptions{}, nil)
	var q *core.FomodChoicesRequiredError
	require.True(t, errors.As(err, &q), "got %v", err)
	require.Len(t, q.Mods, 1, "only the mod with no recorded choices asks")
	var incomplete *core.ProfileFillIncompleteError
	require.True(t, errors.As(err, &incomplete))
	assert.True(t, incomplete.Result.Refused)
	_, err = f.svc.GetInstalledMod(ctx, "nexusmods", "20", f.game.ID, "Coll")
	assert.Error(t, err, "nothing was written")

	plan = f.plan(t)
	result, err := f.svc.ApplyProfileFill(ctx, f.game, plan,
		core.ProfileFillOptions{Fomod: map[string]domain.FomodChoices{"nexusmods:21": answer("2K")}}, nil)
	require.NoError(t, err)
	assert.Equal(t, 2, result.Installed)
	data, err := os.ReadFile(filepath.Join(f.game.ModPath, "textures", "t.dds"))
	require.NoError(t, err)
	assert.Contains(t, []string{"4K-a", "2K-b"}, string(data))
	saved, err := config.LoadProfile(f.svc.ConfigDir(), f.game.ID, "Coll")
	require.NoError(t, err)
	assert.Equal(t, "4K", saved.FindRef("nexusmods", "20").Fomod.Steps[0].Groups[0].Choices[0].Name, "the recorded choices were replayed")
	assert.Equal(t, "2K", saved.FindRef("nexusmods", "21").Fomod.Steps[0].Groups[0].Choices[0].Name)
}

func TestApplyProfileFill_CarriesTheStatedModType(t *testing.T) {
	f := newFillFixture(t, fillSetup{
		game: func(t *testing.T, svc *core.Service) *domain.Game { return typedGame(t, svc, "bethesda") },
		refs: []domain.ModReference{{SourceID: "nexusmods", ModID: "30", Version: "1", FileIDs: []string{"300"}, ModType: "dinput"}},
	})
	f.download(t, "30", "skse.zip")

	plan := f.plan(t)
	require.Len(t, plan.Matched, 1)
	assert.Equal(t, "dinput", plan.Matched[0].ModType)
	_, err := f.svc.ApplyProfileFill(context.Background(), f.game, plan, core.ProfileFillOptions{}, nil)
	require.NoError(t, err)
	row, err := f.svc.GetInstalledMod(context.Background(), "nexusmods", "30", f.game.ID, "Coll")
	require.NoError(t, err)
	assert.Equal(t, "dinput", row.ModType, "never the empty type: the next apply would see drift and reinstall")
	_, err = os.Stat(filepath.Join(f.game.InstallPath, "mod30.esp"))
	assert.NoError(t, err, "deployed to the game root, by its type")
}

// #608 review: two pins whose collection files are the same bytes are both
// filled by one download.
func TestApplyProfileFill_TwoPinsShareOneFile(t *testing.T) {
	f := newFillFixture(t, fillSetup{})
	f.archives["11"] = f.archives["10"]
	f.recipe[1].MD5, f.recipe[1].Size = f.recipe[0].MD5, f.recipe[0].Size
	f.writeRecipe(t)
	f.download(t, "10", "shared.zip")

	plan := f.plan(t)
	assert.ElementsMatch(t, []string{"10", "11"}, matchedIDs(plan))
	result, err := f.svc.ApplyProfileFill(context.Background(), f.game, plan, core.ProfileFillOptions{}, nil)
	require.NoError(t, err)
	assert.Equal(t, 2, result.Installed)
}

// #608 review: a mod the curator wants on its newest file, which the user
// has locked, keeps the lock and its policy, and the plan says why.
func TestPlanProfileFill_ALockedLatestPinIsNotSetToAuto(t *testing.T) {
	f := newFillFixture(t, fillSetup{refs: []domain.ModReference{
		{SourceID: "nexusmods", ModID: "11", Version: "1", FileIDs: []string{"110"}, Locked: true},
	}})
	f.download(t, "11", "b.zip")
	plan := f.plan(t)
	require.Len(t, plan.Matched, 1)
	assert.False(t, plan.Matched[0].AutoUpdate)
	require.NotEmpty(t, plan.Notes)
	assert.Contains(t, plan.Notes[len(plan.Notes)-1], "they are locked")
	_, err := f.svc.ApplyProfileFill(context.Background(), f.game, plan, core.ProfileFillOptions{}, nil)
	require.NoError(t, err)
	row, err := f.svc.GetInstalledMod(context.Background(), "nexusmods", "11", f.game.ID, "Coll")
	require.NoError(t, err)
	assert.Equal(t, domain.UpdateNotify, row.UpdatePolicy)
}

// #608 review: a recipe md5 that is not 32 hex digits is no checksum: the
// pin cannot be matched, nothing is read for it, and the junk is never
// echoed into the plan.
func TestPlanProfileFill_AMalformedRecipeChecksumIsNone(t *testing.T) {
	f := newFillFixture(t, fillSetup{})
	f.recipe[2].MD5 = strings.Repeat("z", 5000) + "\x00"
	f.writeRecipe(t)
	f.download(t, "12", "c.zip")
	plan := f.plan(t)
	assert.Empty(t, plan.Matched)
	var twelve *core.FillPending
	for i := range plan.Pending {
		if plan.Pending[i].ModID == "12" {
			twelve = &plan.Pending[i]
		}
	}
	require.NotNil(t, twelve)
	assert.Equal(t, core.FillNoChecksum, twelve.Reason)
	assert.Empty(t, twelve.Files[0].MD5, "the recipe's junk is not echoed")
	assert.Zero(t, plan.Hashed, "nothing to match it against, so nothing is read")
}

// #608 review: a matched file swapped for a link between the plan and the
// apply is never followed - that mod fails, nothing of it is written.
func TestApplyProfileFill_AFileSwappedForALinkIsNotFollowed(t *testing.T) {
	f := newFillFixture(t, fillSetup{})
	p := f.download(t, "12", "c.zip")
	plan := f.plan(t)
	require.Len(t, plan.Matched, 1)
	outside := filepath.Join(t.TempDir(), "c.zip")
	require.NoError(t, os.WriteFile(outside, f.archives["12"], 0o644))
	require.NoError(t, os.Remove(p))
	require.NoError(t, os.Symlink(outside, p))

	result, err := f.svc.ApplyProfileFill(context.Background(), f.game, plan, core.ProfileFillOptions{}, nil)
	var incomplete *core.ProfileFillIncompleteError
	require.True(t, errors.As(err, &incomplete), "got %v", err)
	assert.Equal(t, 0, result.Installed)
	_, err = f.svc.GetInstalledMod(context.Background(), "nexusmods", "12", f.game.ID, "Coll")
	assert.Error(t, err)
}

// #608 review: copies of one file count against the per-size budget, so a
// folder of duplicates is not read end to end.
func TestPlanProfileFill_DuplicatesCountAgainstTheBudget(t *testing.T) {
	f := newFillFixture(t, fillSetup{refs: []domain.ModReference{
		{SourceID: "nexusmods", ModID: "10", Version: "1", FileIDs: []string{"100"}},
		{SourceID: "nexusmods", ModID: "11", Version: "1", FileIDs: []string{"110"}},
	}})
	// 11's pin is the size of 10's, with other bytes: it stays unmatched,
	// so every copy of 10 is of a size still wanted.
	f.recipe[1].Size = f.recipe[0].Size
	f.recipe[1].MD5 = strings.Repeat("ab", 16)
	f.writeRecipe(t)
	for i := range 10 {
		f.download(t, "10", fmt.Sprintf("copy%02d.zip", i))
	}
	plan := f.plan(t)
	assert.Equal(t, []string{"10"}, matchedIDs(plan))
	assert.LessOrEqual(t, plan.Hashed, 1+4, "one match, then at most four reads that add nothing")
	assert.Equal(t, core.FillIgnoredHashLimit, ignoredBy(plan)["copy09.zip"])
}

// #608 review: the web UI's folder mode scans a chosen folder exactly as
// the CLI scans a directory, and refuses anything but an absolute path to a
// real folder.
func TestPlanProfileFill_Folder(t *testing.T) {
	f := newFillFixture(t, fillSetup{})
	f.download(t, "10", "a.zip")
	ctx := context.Background()
	plan, err := f.svc.PlanProfileFill(ctx, f.game, core.ProfileFillRequest{Profile: "Coll", Folder: f.downloads})
	require.NoError(t, err)
	assert.Equal(t, []string{"10"}, matchedIDs(plan))

	link := filepath.Join(t.TempDir(), "dl")
	require.NoError(t, os.Symlink(f.downloads, link))
	for name, folder := range map[string]string{
		"relative": "Downloads",
		"a file":   filepath.Join(f.downloads, "a.zip"),
		"a link":   link,
		"missing":  filepath.Join(f.downloads, "nope"),
	} {
		_, err := f.svc.PlanProfileFill(ctx, f.game, core.ProfileFillRequest{Profile: "Coll", Folder: folder})
		assert.ErrorIs(t, err, core.ErrFillFolderInvalid, name)
	}
}

func TestDownloadsDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DOWNLOAD_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "cfg"))
	assert.Equal(t, filepath.Join(home, "Downloads"), core.DownloadsDir())

	require.NoError(t, os.MkdirAll(filepath.Join(home, "cfg"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(home, "cfg", "user-dirs.dirs"),
		[]byte("# written by xdg-user-dirs-update\nXDG_DOWNLOAD_DIR=\"$HOME/Téléchargements\"\n"), 0o644))
	assert.Equal(t, filepath.Join(home, "Téléchargements"), core.DownloadsDir())

	t.Setenv("XDG_DOWNLOAD_DIR", "/srv/dl")
	assert.Equal(t, "/srv/dl", core.DownloadsDir())
}

// #634 x #608: a mod whose files each carry a FOMOD installer, recorded per
// file, fills from its two downloads with each file's own choices, asking
// nothing.
func TestApplyProfileFill_ReplaysPerFileChoices(t *testing.T) {
	f := newFillFixture(t, fillSetup{refs: []domain.ModReference{{
		SourceID: "nexusmods", ModID: "40", Version: "1", FileIDs: []string{"400", "401"},
		FomodFiles: domain.FomodFiles{"400": answer("4K"), "401": patchAnswer("Vanilla")},
	}}})
	main, patch := fomodZip(t, "4K", "v1"), patchZip(t, "USSEP", "v1")
	f.recipe = []fillRecipeMod{
		{Name: "Bugs", ModID: "40", FileID: "400", MD5: md5Hex(main), Size: int64(len(main)), Policy: "exact"},
		{Name: "Bugs", ModID: "40", FileID: "401", MD5: md5Hex(patch), Size: int64(len(patch)), Policy: "exact"},
	}
	f.writeRecipe(t)
	require.NoError(t, os.WriteFile(filepath.Join(f.downloads, "bugs-main.zip"), main, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(f.downloads, "bugs-patch.zip"), patch, 0o644))

	plan := f.plan(t)
	require.Len(t, plan.Matched, 1)
	assert.True(t, plan.Matched[0].Installer, "per-file choices are recorded installer choices")
	assert.Len(t, plan.Matched[0].Files, 2)
	_, err := f.svc.ApplyProfileFill(context.Background(), f.game, plan, core.ProfileFillOptions{}, nil)
	require.NoError(t, err)
	data, err := os.ReadFile(filepath.Join(f.game.ModPath, "textures", "t.dds"))
	require.NoError(t, err)
	assert.Equal(t, "4K-v1", string(data))
	data, err = os.ReadFile(filepath.Join(f.game.ModPath, "bugs_patch.esp"))
	require.NoError(t, err)
	assert.Equal(t, "VANILLA-v1", string(data))
	saved, err := config.LoadProfile(f.svc.ConfigDir(), f.game.ID, "Coll")
	require.NoError(t, err)
	ref := saved.FindRef("nexusmods", "40")
	assert.Equal(t, "Vanilla", picked(ref.FomodFiles["401"]))
}
