package core_test

import (
	"context"
	"errors"
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

// catalogLessSource is a minimal source.ModSource double with NO
// source.GameCatalog - the shape SearchGameCatalog must refuse with
// core.ErrNoGameCatalog (today's NexusMods).
type catalogLessSource struct{ id, name string }

func (m *catalogLessSource) ID() string      { return m.id }
func (m *catalogLessSource) Name() string    { return m.name }
func (m *catalogLessSource) AuthURL() string { return "" }
func (m *catalogLessSource) ExchangeToken(context.Context, string) (*source.Token, error) {
	return nil, nil
}
func (m *catalogLessSource) Search(context.Context, source.SearchQuery) (source.SearchResult, error) {
	return source.SearchResult{}, nil
}
func (m *catalogLessSource) GetMod(context.Context, string, string) (*domain.Mod, error) {
	return nil, nil
}
func (m *catalogLessSource) GetDependencies(context.Context, *domain.Mod) ([]domain.ModReference, error) {
	return nil, nil
}
func (m *catalogLessSource) GetModFiles(context.Context, *domain.Mod) ([]domain.DownloadableFile, error) {
	return nil, nil
}
func (m *catalogLessSource) GetDownloadURL(context.Context, *domain.Mod, string) (string, error) {
	return "", nil
}
func (m *catalogLessSource) CheckUpdates(context.Context, []domain.InstalledMod) ([]domain.Update, error) {
	return nil, nil
}

// catalogSource adds source.GameCatalog, standing in for CurseForge without
// ever touching the network (the fake-sources-only rule).
type catalogSource struct {
	catalogLessSource
	entries []source.GameEntry
	listErr error
}

func (m *catalogSource) ListGames(context.Context) ([]source.GameEntry, error) {
	return m.entries, m.listErr
}

var (
	_ source.ModSource   = (*catalogLessSource)(nil)
	_ source.GameCatalog = (*catalogSource)(nil)
)

func newGameAddService(t *testing.T) *core.Service {
	t.Helper()
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	return svc
}

// TestSearchGameCatalog_FiltersByNameOrSlug pins the catalog query core now
// owns (#307): a case-insensitive substring match on either the entry's
// name or its slug, the exact filter the CLI's seven-prompt flow ran
// inline before this moved into core.
func TestSearchGameCatalog_FiltersByNameOrSlug(t *testing.T) {
	svc := newGameAddService(t)
	svc.RegisterSource(&catalogSource{
		catalogLessSource: catalogLessSource{id: "fakecat", name: "Fake Catalog"},
		entries: []source.GameEntry{
			{ID: "432", Name: "Minecraft", Slug: "minecraft"},
			{ID: "78022", Name: "Valheim", Slug: "valheim"},
			{ID: "1", Name: "Craft of Mine", Slug: "com"},
		},
	})

	report, err := svc.SearchGameCatalog(context.Background(), "fakecat", "CRAFT")
	require.NoError(t, err)
	assert.Equal(t, "fakecat", report.SourceID)
	assert.Equal(t, "CRAFT", report.Query)
	require.Len(t, report.Matches, 2)
	assert.Equal(t, core.GameCatalogMatch{Identifier: "432", Name: "Minecraft", Slug: "minecraft", GameID: "minecraft"}, report.Matches[0])
	assert.Equal(t, core.GameCatalogMatch{Identifier: "1", Name: "Craft of Mine", Slug: "com", GameID: "com"}, report.Matches[1])
}

// TestSearchGameCatalog_NoMatchesIsAnEmptyList pins that a query matching
// nothing is a successful, EMPTY report - not an error: "no games matched"
// is an answer, and the CLI printed exactly that before.
func TestSearchGameCatalog_NoMatchesIsAnEmptyList(t *testing.T) {
	svc := newGameAddService(t)
	svc.RegisterSource(&catalogSource{
		catalogLessSource: catalogLessSource{id: "fakecat", name: "Fake Catalog"},
		entries:           []source.GameEntry{{ID: "432", Name: "Minecraft", Slug: "minecraft"}},
	})

	report, err := svc.SearchGameCatalog(context.Background(), "fakecat", "skyrim")
	require.NoError(t, err)
	assert.Empty(t, report.Matches)
	assert.NotNil(t, report.Matches, "an empty match list must encode as [], never null")
}

// TestSearchGameCatalog_SlugFallsBackToIdentifier pins the derived local
// game id for a catalog whose entries carry no Slug: it falls back to the
// identifier, byte-for-byte the pre-#307 CLI derivation.
func TestSearchGameCatalog_SlugFallsBackToIdentifier(t *testing.T) {
	svc := newGameAddService(t)
	svc.RegisterSource(&catalogSource{
		catalogLessSource: catalogLessSource{id: "fakecat", name: "Fake Catalog"},
		entries:           []source.GameEntry{{ID: "Acme Quest", Name: "Acme Quest"}},
	})

	report, err := svc.SearchGameCatalog(context.Background(), "fakecat", "acme")
	require.NoError(t, err)
	require.Len(t, report.Matches, 1)
	assert.Equal(t, "acme-quest", report.Matches[0].GameID)
	assert.Equal(t, "Acme Quest", report.Matches[0].Identifier)
}

// TestSearchGameCatalog_CatalogLessSourceIsTyped pins the typed refusal
// both frontends branch on: the CLI falls back to its manual-identifier
// path, serve answers 400.
func TestSearchGameCatalog_CatalogLessSourceIsTyped(t *testing.T) {
	svc := newGameAddService(t)
	svc.RegisterSource(&catalogLessSource{id: "plain", name: "Plain"})

	_, err := svc.SearchGameCatalog(context.Background(), "plain", "anything")
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrNoGameCatalog)
}

// TestSearchGameCatalog_UnknownSourceErrors pins that an unregistered
// source id is a lookup failure, not an empty result.
func TestSearchGameCatalog_UnknownSourceErrors(t *testing.T) {
	svc := newGameAddService(t)
	_, err := svc.SearchGameCatalog(context.Background(), "nope", "x")
	require.Error(t, err)
	assert.NotErrorIs(t, err, core.ErrNoGameCatalog)
}

// TestSearchGameCatalog_EmptyQueryIsRejected pins the one input rule: an
// empty query would list a source's ENTIRE catalog (tens of thousands of
// rows for CurseForge), which is not what either frontend asks for.
func TestSearchGameCatalog_EmptyQueryIsRejected(t *testing.T) {
	svc := newGameAddService(t)
	svc.RegisterSource(&catalogSource{catalogLessSource: catalogLessSource{id: "fakecat", name: "Fake Catalog"}})

	_, err := svc.SearchGameCatalog(context.Background(), "fakecat", "  ")
	require.Error(t, err)
	var specErr *core.GameSpecError
	require.ErrorAs(t, err, &specErr)
	assert.Equal(t, "query", specErr.Field)
}

// TestSearchGameCatalog_ListErrorPropagates pins that a source's own
// failure (an auth error, a network error) reaches the caller intact, so
// the CLI can still rewrite domain.ErrAuthRequired into its "run lmm auth
// login" prompt.
func TestSearchGameCatalog_ListErrorPropagates(t *testing.T) {
	svc := newGameAddService(t)
	svc.RegisterSource(&catalogSource{
		catalogLessSource: catalogLessSource{id: "fakecat", name: "Fake Catalog"},
		listErr:           domain.ErrAuthRequired,
	})

	_, err := svc.SearchGameCatalog(context.Background(), "fakecat", "x")
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrAuthRequired)
}

// TestAddGame_WritesGameAndDefaultProfile pins the whole single-step write:
// games.yaml gains the entry, the default profile is created, and the
// returned document is the same core.GameListEntry row `lmm game list
// --json` emits.
func TestAddGame_WritesGameAndDefaultProfile(t *testing.T) {
	svc := newGameAddService(t)
	install := t.TempDir()

	entry, err := svc.AddGame(context.Background(), core.GameSpec{
		SourceID:    "nexusmods",
		Identifier:  "skyrimspecialedition",
		Name:        "Skyrim Special Edition",
		InstallPath: install,
	})
	require.NoError(t, err)
	assert.Equal(t, "skyrimspecialedition", entry.ID)
	assert.Equal(t, "Skyrim Special Edition", entry.Name)
	assert.Equal(t, install, entry.InstallPath)
	assert.Equal(t, filepath.Join(install, "mods"), entry.ModPath, "the default mod path is install/mods")
	assert.Equal(t, map[string]string{"nexusmods": "skyrimspecialedition"}, entry.SourceIDs)
	assert.Equal(t, domain.LinkSymlink, entry.LinkMethod)
	assert.False(t, entry.Default)

	games, err := config.LoadGames(svc.ConfigDir())
	require.NoError(t, err)
	require.Contains(t, games, "skyrimspecialedition")

	profile, err := svc.NewProfileManager().Get(context.Background(), "skyrimspecialedition", "default")
	require.NoError(t, err)
	assert.Equal(t, "default", profile.Name)
}

// TestAddGame_ExplicitIDWins pins the catalog path: the caller passes the
// game id SearchGameCatalog suggested (derived from the entry's SLUG), so a
// CurseForge add is keyed "minecraft", never the numeric identifier.
func TestAddGame_ExplicitIDWins(t *testing.T) {
	svc := newGameAddService(t)
	install := t.TempDir()

	entry, err := svc.AddGame(context.Background(), core.GameSpec{
		SourceID: "curseforge", Identifier: "432", ID: "minecraft",
		Name: "Minecraft", InstallPath: install,
	})
	require.NoError(t, err)
	assert.Equal(t, "minecraft", entry.ID)
	assert.Equal(t, map[string]string{"curseforge": "432"}, entry.SourceIDs)
}

// TestAddGame_DerivesIDFromIdentifier pins the manual path's derivation -
// lower-cased, spaces to dashes - moved verbatim out of the CLI.
func TestAddGame_DerivesIDFromIdentifier(t *testing.T) {
	svc := newGameAddService(t)
	entry, err := svc.AddGame(context.Background(), core.GameSpec{
		SourceID: "nexusmods", Identifier: "Acme Quest", Name: "Acme Quest",
		InstallPath: t.TempDir(),
	})
	require.NoError(t, err)
	assert.Equal(t, "acme-quest", entry.ID)
	assert.Equal(t, map[string]string{"nexusmods": "Acme Quest"}, entry.SourceIDs,
		"the SOURCE identifier is stored verbatim; only the local id is slugified")
}

// TestAddGame_ExplicitModPathAndLinkMethod pins that both optional fields
// are honoured rather than defaulted over.
func TestAddGame_ExplicitModPathAndLinkMethod(t *testing.T) {
	svc := newGameAddService(t)
	install, mods := t.TempDir(), t.TempDir()

	entry, err := svc.AddGame(context.Background(), core.GameSpec{
		SourceID: "nexusmods", Identifier: "acme", Name: "Acme",
		InstallPath: install, ModPath: mods, LinkMethod: domain.LinkHardlink,
	})
	require.NoError(t, err)
	assert.Equal(t, mods, entry.ModPath)
	assert.Equal(t, domain.LinkHardlink, entry.LinkMethod)
}

// TestAddGame_DuplicateIsTyped pins the typed collision serve answers 409
// with - detected INSIDE the gate, so the status never depends on an
// untyped error's wording (the #332 M6 rule, applied to games).
func TestAddGame_DuplicateIsTyped(t *testing.T) {
	svc := newGameAddService(t)
	install := t.TempDir()
	spec := core.GameSpec{SourceID: "nexusmods", Identifier: "acme", Name: "Acme", InstallPath: install}

	_, err := svc.AddGame(context.Background(), spec)
	require.NoError(t, err)

	_, err = svc.AddGame(context.Background(), spec)
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrGameExists)
}

// TestAddGame_FieldErrors pins one typed, FIELD-NAMED rejection per bad
// input - the SPA renders the message against the offending form field, so
// "which field" must never be a substring guess.
func TestAddGame_FieldErrors(t *testing.T) {
	install := t.TempDir()
	notADir := filepath.Join(install, "file.txt")
	require.NoError(t, os.WriteFile(notADir, []byte("x"), 0o600))

	tests := []struct {
		name  string
		spec  core.GameSpec
		field string
	}{
		{"no source", core.GameSpec{Identifier: "acme", Name: "Acme", InstallPath: install}, "source_id"},
		{"no identifier", core.GameSpec{SourceID: "nexusmods", Name: "Acme", InstallPath: install}, "identifier"},
		{"no name", core.GameSpec{SourceID: "nexusmods", Identifier: "acme", InstallPath: install}, "name"},
		{"no install path", core.GameSpec{SourceID: "nexusmods", Identifier: "acme", Name: "Acme"}, "install_path"},
		{"install path missing", core.GameSpec{SourceID: "nexusmods", Identifier: "acme", Name: "Acme", InstallPath: filepath.Join(install, "nope")}, "install_path"},
		{"install path is a file", core.GameSpec{SourceID: "nexusmods", Identifier: "acme", Name: "Acme", InstallPath: notADir}, "install_path"},
		{"mod path is a file", core.GameSpec{SourceID: "nexusmods", Identifier: "acme", Name: "Acme", InstallPath: install, ModPath: notADir}, "mod_path"},
		{"id has a path separator", core.GameSpec{SourceID: "nexusmods", Identifier: "acme", ID: "a/b", Name: "Acme", InstallPath: install}, "id"},
		{"identifier slugifies to nothing", core.GameSpec{SourceID: "nexusmods", Identifier: "   ", Name: "Acme", InstallPath: install}, "identifier"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newGameAddService(t)
			_, err := svc.AddGame(context.Background(), tt.spec)
			require.Error(t, err)
			var specErr *core.GameSpecError
			require.ErrorAs(t, err, &specErr)
			assert.Equal(t, tt.field, specErr.Field)
		})
	}
}

// TestAddGame_InvalidIDUnwrapsToDomainSentinel pins that a rejected game id
// still satisfies errors.Is(err, domain.ErrInvalidGameID), so a caller that
// branches on the domain sentinel keeps working through the field wrapper.
func TestAddGame_InvalidIDUnwrapsToDomainSentinel(t *testing.T) {
	svc := newGameAddService(t)
	_, err := svc.AddGame(context.Background(), core.GameSpec{
		SourceID: "nexusmods", Identifier: "acme", ID: "../escape", Name: "Acme",
		InstallPath: t.TempDir(),
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrInvalidGameID)
}

// TestAddGame_NothingIsWrittenWhenValidationFails pins that a refused spec
// leaves games.yaml untouched: validation runs before the gate, so a bad
// request never half-creates a game.
func TestAddGame_NothingIsWrittenWhenValidationFails(t *testing.T) {
	svc := newGameAddService(t)
	_, err := svc.AddGame(context.Background(), core.GameSpec{
		SourceID: "nexusmods", Identifier: "acme", Name: "Acme",
		InstallPath: filepath.Join(t.TempDir(), "missing"),
	})
	require.Error(t, err)
	games, err := config.LoadGames(svc.ConfigDir())
	require.NoError(t, err)
	assert.Empty(t, games)
}

// TestAddGame_CancelledContextDoesNotWrite pins the gate: a cancelled
// context is refused by beginOp before anything is persisted.
func TestAddGame_CancelledContextDoesNotWrite(t *testing.T) {
	svc := newGameAddService(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := svc.AddGame(ctx, core.GameSpec{
		SourceID: "nexusmods", Identifier: "acme", Name: "Acme", InstallPath: t.TempDir(),
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled))
}

// TestGameDetectListing_MarksAlreadyConfigured pins the pre-selection
// listing document serve needs (the CLI printed this to the terminal and
// emitted nothing under --json): every detected row, its 1-based index -
// the number `--select` takes - and whether games.yaml already holds it.
func TestGameDetectListing_MarksAlreadyConfigured(t *testing.T) {
	svc := newGameAddService(t)
	require.NoError(t, svc.SaveGame(context.Background(), &domain.Game{
		ID: "valheim", Name: "Valheim", InstallPath: t.TempDir(), ModPath: t.TempDir(),
	}))

	listing, err := svc.GameDetectListing(context.Background(), []domain.DetectedGame{
		{Slug: "skyrim-se", Name: "Skyrim Special Edition", InstallPath: "/games/skyrim"},
		{Slug: "valheim", Name: "Valheim", InstallPath: "/games/valheim"},
	}, []string{"a library was unreadable"})
	require.NoError(t, err)

	require.Len(t, listing.Games, 2)
	assert.Equal(t, 1, listing.Games[0].Index)
	assert.False(t, listing.Games[0].AlreadyConfigured)
	assert.Equal(t, 2, listing.Games[1].Index)
	assert.True(t, listing.Games[1].AlreadyConfigured)
	assert.Equal(t, []string{"a library was unreadable"}, listing.Warnings)
}

// TestGameDetectListing_EmptyScan pins that a scan finding nothing is still
// a well-formed document with an empty (never null) game list.
func TestGameDetectListing_EmptyScan(t *testing.T) {
	svc := newGameAddService(t)
	listing, err := svc.GameDetectListing(context.Background(), nil, nil)
	require.NoError(t, err)
	assert.Empty(t, listing.Games)
	assert.NotNil(t, listing.Games)
}

// TestSelectDetectedGames pins the selector vocabulary both frontends
// share: a 1-based index into the listing, or a slug.
func TestSelectDetectedGames(t *testing.T) {
	games := []domain.DetectedGame{
		{Slug: "skyrim-se", Name: "Skyrim"},
		{Slug: "valheim", Name: "Valheim"},
		{Slug: "icarus", Name: "Icarus"},
	}

	got, err := core.SelectDetectedGames(games, []string{"2", "icarus"})
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "valheim", got[0].Slug)
	assert.Equal(t, "icarus", got[1].Slug)
}

// TestSelectDetectedGames_Rejections pins every refusal: an empty
// selection, an out-of-range index, an unknown slug, and a duplicate (which
// would apply the same overwrite twice).
func TestSelectDetectedGames_Rejections(t *testing.T) {
	games := []domain.DetectedGame{{Slug: "skyrim-se", Name: "Skyrim"}}

	for _, sel := range [][]string{nil, {"0"}, {"2"}, {"nope"}, {"1", "skyrim-se"}} {
		_, err := core.SelectDetectedGames(games, sel)
		assert.Error(t, err, "selection %v must be refused", sel)
	}
}
