package core_test

// #594: text a source supplies (a mod's name, author, summary, version,
// category, file names, update versions and changelogs) is cleaned of
// terminal control sequences where core receives it, so neither frontend
// nor the database ever sees a raw ESC/C1/bidi payload.

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// evilPayload is an ESC clear-screen, an OSC 0 title, an OSC 8 link, a C1
// CSI and a bidi override - every class of control #594 strips.
const evilPayload = "\x1b[2J\x1b]0;pwned\x07\x1b]8;;https://evil.example\x1b\\\u009b2J\u202E"

// assertNoControls fails when s carries any rune #594 strips.
func assertNoControls(t *testing.T, field, s string, multiline bool) {
	t.Helper()
	for _, r := range s {
		if (r == '\n' || r == '\t') && multiline {
			continue
		}
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) {
			t.Errorf("%s carries control rune %U: %q", field, r, s)
			return
		}
	}
}

func assertCleanMod(t *testing.T, where string, m *domain.Mod) {
	t.Helper()
	require.NotNil(t, m, where)
	for field, v := range map[string]string{
		"name": m.Name, "version": m.Version, "author": m.Author, "author_name": m.AuthorName,
		"summary": m.Summary, "category": m.Category, "category_name": m.CategoryName,
		"source_url": m.SourceURL, "picture_url": m.PictureURL, "display_version": m.DisplayVersion,
	} {
		assertNoControls(t, where+" "+field, v, false)
	}
	assertNoControls(t, where+" description", m.Description, true)
	for _, d := range m.Dependencies {
		assertNoControls(t, where+" dependency version", d.Version, false)
	}
	assert.Contains(t, m.Name, "Evil", where+": the visible text survives")
	assert.Contains(t, m.Name, "日本語", where+": non-ASCII survives")
}

func evilMod(id, gameID string) *domain.Mod {
	return &domain.Mod{
		ID: id, SourceID: "src", GameID: gameID,
		Name:         "Evil 日本語" + evilPayload,
		Version:      "1.0" + evilPayload,
		Author:       "Mallory" + evilPayload,
		Summary:      "line\none" + evilPayload,
		Description:  "para\r\nmore" + evilPayload,
		Category:     "7" + evilPayload,
		CategoryName: "Gameplay" + evilPayload,
		SourceURL:    "https://example.com/" + evilPayload,
		PictureURL:   "https://example.com/p.png" + evilPayload,
	}
}

func TestSourceText_SearchAndGetModAreClean(t *testing.T) {
	svc, game, src := newModDetailTestService(t)
	game.SourceIDs["src"] = game.ID
	src.AddMod(game.ID, evilMod("a", game.ID))
	ctx := context.Background()

	res, err := svc.SearchMods(ctx, "src", game.ID, "", "", nil, 0, 0)
	require.NoError(t, err)
	require.Len(t, res.Mods, 1)
	assertCleanMod(t, "SearchMods", &res.Mods[0])

	report, err := svc.Search(ctx, game, "default", "Evil", core.SearchOptions{SourceID: "src"})
	require.NoError(t, err)
	require.Len(t, report.Mods, 1)
	assertCleanMod(t, "Search", &report.Mods[0].Mod)

	mod, err := svc.GetMod(ctx, "src", game.ID, "a")
	require.NoError(t, err)
	assertCleanMod(t, "GetMod", mod)

	detail, err := svc.ModDetail(ctx, game, "default", "src", "a")
	require.NoError(t, err)
	assertCleanMod(t, "ModDetail", detail.Mod)

	files, err := svc.GetModFiles(ctx, game, "src", mod)
	require.NoError(t, err)
	require.NotEmpty(t, files)
}

// evilFilesSource serves an install of evilMod whose file metadata and
// update-check answer carry the payload too.
type evilFilesSource struct {
	*mockSourceWithDownloads
}

func (s *evilFilesSource) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	return []domain.DownloadableFile{{
		ID: "f1", Name: "Main" + evilPayload, FileName: "mod1-f1.zip" + evilPayload,
		Version: "1.0" + evilPayload, Category: "MAIN" + evilPayload,
		Description: "desc\n" + evilPayload, IsPrimary: true,
	}}, nil
}

func (s *evilFilesSource) CheckUpdates(ctx context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	var out []domain.Update
	for _, im := range installed {
		out = append(out, domain.Update{InstalledMod: im, NewVersion: "2.0" + evilPayload, Changelog: "fixed\r\n" + evilPayload})
	}
	return out, nil
}

func TestSourceText_InstallListAndUpdateCheckAreClean(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	src := &evilFilesSource{mockSourceWithDownloads: newMockSourceWithDownloads("src")}
	t.Cleanup(src.Close)
	svc.RegisterSource(src)
	src.AddMod("g1", evilMod("mod1", "g1"))
	zipBytes, err := os.ReadFile(createTestZip(t, t.TempDir(), map[string]string{"a.esp": "a"}))
	require.NoError(t, err)
	src.AddDownload("f1", zipBytes)
	ctx := context.Background()

	plan, err := svc.PlanInstall(ctx, game, "default", "src", "mod1", false)
	require.NoError(t, err)
	assertCleanMod(t, "InstallPlan", &plan.Mod)
	require.NotEmpty(t, plan.Files)
	for _, f := range plan.Files {
		for field, v := range map[string]string{"name": f.Name, "file_name": f.FileName, "version": f.Version, "category": f.Category} {
			assertNoControls(t, "plan file "+field, v, false)
		}
		assertNoControls(t, "plan file description", f.Description, true)
	}
	_, err = svc.ApplyInstall(ctx, game, plan, core.InstallOptions{}, nil)
	require.NoError(t, err)

	list, err := svc.ListMods(ctx, game, "default")
	require.NoError(t, err)
	require.Len(t, list.Mods, 1)
	assertCleanMod(t, "ListMods", &list.Mods[0].Mod)

	// The profile's version record came from the fetched mod, not the DB
	// read, so it shows the ingestion point itself cleaned the text.
	prof, err := svc.NewProfileManager().Get(ctx, "g1", "default")
	require.NoError(t, err)
	require.Len(t, prof.Mods, 1)
	assertNoControls(t, "profile ref version", prof.Mods[0].Version, false)
	assert.True(t, strings.HasPrefix(prof.Mods[0].Version, "1.0"))

	installed, err := svc.GetInstalledMods(ctx, "g1", "default")
	require.NoError(t, err)
	updates, err := svc.CheckGameUpdates(ctx, game, "default", installed, nil, core.UpdateCheckOptions{})
	require.NoError(t, err)
	require.Len(t, updates, 1)
	assertNoControls(t, "update new_version", updates[0].NewVersion, false)
	assertNoControls(t, "update changelog", updates[0].Changelog, true)
	assert.Equal(t, "fixed\n[2J]0;pwned]8;;https://evil.example\\2J", updates[0].Changelog)
}

// The Steam Workshop adopt plan folds in metadata from the source's batch
// describer, a path outside GetMod: its titles and notes are cleaned too.
func TestSourceText_WorkshopAdoptPlanIsClean(t *testing.T) {
	svc, game, src, root := newWorkshopService(t)
	a := steamItem(t, root, "1133870", "3617086610", "7987119735124793734", 1764767935)
	b := steamItem(t, root, "1133870", "3512001122", "1122334455667788990", 1758000000)
	src.scan = source.WorkshopScan{Roots: []string{root}, Items: []domain.WorkshopItem{a, b}}
	m := evilMod("3617086610", game.ID)
	m.SourceID = "steamworkshop"
	src.describe = []source.ModDescription{
		{ModID: "3617086610", Mod: *m},
		{ModID: "3512001122", Unavailable: true, Note: "gone" + evilPayload},
	}

	plan, err := svc.PlanWorkshopAdopt(context.Background(), game, "default", core.WorkshopAdoptOptions{})
	require.NoError(t, err)
	require.Len(t, plan.Entries, 2)
	for _, e := range plan.Entries {
		if e.Mod != nil {
			assertCleanMod(t, "workshop adopt", e.Mod)
		} else {
			assertNoControls(t, "workshop adopt note", e.Note, false)
		}
	}
	assert.Equal(t, "Evil 日本語"+evilPayload, src.describe[0].Mod.Name, "the source's own value is not written into")
}

// A source's game version types (the CurseForge picker) are display text.
func TestSourceText_GameVersionTypesAreClean(t *testing.T) {
	lister := &versionTypeSource{types: []source.GameVersionType{{ID: 1, Name: "WoW" + evilPayload, Slug: "wow" + evilPayload}}}
	svc := newVersionTypeService(t, lister)
	report, err := svc.GameVersionTypes(context.Background(), "wow-classic", "versioned")
	require.NoError(t, err)
	require.Len(t, report.Types, 1)
	assertNoControls(t, "version type name", report.Types[0].Name, false)
	assertNoControls(t, "version type slug", report.Types[0].Slug, false)
}

// evilCatalogSource lists one game whose name carries the payload.
type evilCatalogSource struct{ catalogLessSource }

func (s *evilCatalogSource) ListGames(context.Context) ([]source.GameEntry, error) {
	return []source.GameEntry{{ID: "432", Name: "Evil Game" + evilPayload, Slug: "evil-game" + evilPayload}}, nil
}

func TestSourceText_GameCatalogIsClean(t *testing.T) {
	svc := newGameAddService(t)
	svc.RegisterSource(&evilCatalogSource{catalogLessSource{id: "cat", name: "Cat"}})
	report, err := svc.SearchGameCatalog(context.Background(), "cat", "evil")
	require.NoError(t, err)
	require.Len(t, report.Matches, 1)
	assertNoControls(t, "catalog name", report.Matches[0].Name, false)
	assertNoControls(t, "catalog slug", report.Matches[0].Slug, false)
	assert.Equal(t, "432", report.Matches[0].Identifier)
}
