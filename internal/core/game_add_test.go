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

// newGameAddService builds a Service with the two source ids every test
// below spends as SourceID registered - AddGame refuses an unregistered
// one (#333 Important #1), and these tests are exercising everything else
// about the spec, not the registry check.
func newGameAddService(t *testing.T) *core.Service {
	t.Helper()
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	svc.RegisterSource(&catalogLessSource{id: "nexusmods", name: "NexusMods"})
	svc.RegisterSource(&catalogSource{catalogLessSource: catalogLessSource{id: "curseforge", name: "CurseForge"}})
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

// TestAddGame_UnregisteredSourceIsTyped pins #333 Important #1: an id no
// registered source claims is refused with a field-named GameSpecError
// BEFORE anything is written, not a 200 that parks an unusable game in
// games.yaml.
func TestAddGame_UnregisteredSourceIsTyped(t *testing.T) {
	svc := newGameAddService(t)
	install := t.TempDir()

	_, err := svc.AddGame(context.Background(), core.GameSpec{
		SourceID: "no-such-source", Identifier: "acme", Name: "Acme", InstallPath: install,
	})
	require.Error(t, err)
	var specErr *core.GameSpecError
	require.ErrorAs(t, err, &specErr)
	assert.Equal(t, "source_id", specErr.Field)

	games, err := config.LoadGames(svc.ConfigDir())
	require.NoError(t, err)
	assert.Empty(t, games, "no games.yaml row for a game whose source was refused")
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
		{"id has a path separator", core.GameSpec{SourceID: "nexusmods", Identifier: "acme", ID: "a/b", Name: "Acme", InstallPath: install}, "game_id"},
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
		{Slug: "skyrim-se", Name: "Skyrim Special Edition", InstallPath: "/games/skyrim", Known: true},
		{Slug: "valheim", Name: "Valheim", InstallPath: "/games/valheim", Known: true},
	}, []string{"a library was unreadable"}, core.GameDetectListingOptions{})
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
	listing, err := svc.GameDetectListing(context.Background(), nil, nil, core.GameDetectListingOptions{})
	require.NoError(t, err)
	assert.Empty(t, listing.Games)
	assert.NotNil(t, listing.Games)
}

// TestSelectDetectedGames pins the selector vocabulary both frontends
// share: a 1-based index into the listing, or a slug.
func TestSelectDetectedGames(t *testing.T) {
	games := []domain.DetectedGame{
		{Slug: "skyrim-se", Name: "Skyrim", Known: true},
		{Slug: "valheim", Name: "Valheim", Known: true},
		{Slug: "icarus", Name: "Icarus", Known: true},
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
	games := []domain.DetectedGame{{Slug: "skyrim-se", Name: "Skyrim", Known: true}}

	for _, sel := range [][]string{nil, {"0"}, {"2"}, {"nope"}, {"1", "skyrim-se"}} {
		_, err := core.SelectDetectedGames(games, sel)
		assert.Error(t, err, "selection %v must be refused", sel)
	}

	// A scan that found nothing says so, rather than offering the
	// nonsensical range "use 1-0".
	_, err := core.SelectDetectedGames(nil, []string{"1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no games were detected")
}

// TestSelectDetectedGames_NumericSelectionWithNoKnownRows pins Minor 7 of
// the #206 review: a scan with installed games but ZERO known rows (all
// unknown) hit the generic "use 1-0" range message, which is not a range
// and buries the real answer - nothing here is selectable by number at
// all.
func TestSelectDetectedGames_NumericSelectionWithNoKnownRows(t *testing.T) {
	games := []domain.DetectedGame{{Slug: "satisfactory", Name: "Satisfactory"}}

	_, err := core.SelectDetectedGames(games, []string{"1"})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "use 1-0")
	assert.ErrorIs(t, err, core.ErrUnknownDetectedGame)
	// #206 review Minor 7: this error is only ever reached via `lmm serve`
	// today (the CLI's own interactive flow never calls SelectDetectedGames),
	// so a CLI command baked into the core message would be wrong for the
	// browser user actually reading it.
	assert.NotContains(t, err.Error(), "lmm game add",
		"the core message must not name a specific CLI command")
}

// --- #206: prefilling an add from an installed game ---

// detectedSkyrim is the curated candidate app.DetectGames produces for a
// known game: slug, mod path, nexus id and Known all supplied by the
// known-games entry.
func detectedSkyrim(install string) domain.DetectedGame {
	return domain.DetectedGame{
		SteamAppID: "489830", Slug: "skyrim-se", Name: "Skyrim Special Edition",
		InstallPath: install, ModPath: filepath.Join(install, "Data"),
		NexusID: "skyrimspecialedition", Known: true,
	}
}

// TestGameSpecFromDetected_KnownGame pins the whole prefill for a curated
// candidate: nothing is left for the caller to type.
func TestGameSpecFromDetected_KnownGame(t *testing.T) {
	install := t.TempDir()
	spec := core.GameSpecFromDetected(detectedSkyrim(install), core.GameSpec{})

	assert.Equal(t, "Skyrim Special Edition", spec.Name)
	assert.Equal(t, "skyrim-se", spec.ID)
	assert.Equal(t, install, spec.InstallPath)
	assert.Equal(t, filepath.Join(install, "Data"), spec.ModPath)
	assert.Equal(t, map[string]string{"nexusmods": "skyrimspecialedition"}, spec.Sources)
}

// TestGameSpecFromDetected_KnownGameWithSourceMap pins #177's multi-source
// shape surviving the prefill: the known entry's own map wins over the
// derived {nexusmods: nexus_id}, so `game add --from-detected` reproduces
// exactly the games.yaml block `game detect` writes.
func TestGameSpecFromDetected_KnownGameWithSourceMap(t *testing.T) {
	install := t.TempDir()
	spec := core.GameSpecFromDetected(domain.DetectedGame{
		SteamAppID: "1149460", Slug: "icarus", Name: "Icarus",
		InstallPath: install, ModPath: filepath.Join(install, "Icarus", "Content", "Paks", "mods"),
		DeployMode: "compile", Sources: map[string]string{"icarus": "icarus"}, Known: true,
	}, core.GameSpec{})

	assert.Equal(t, map[string]string{"icarus": "icarus"}, spec.Sources)
	assert.Equal(t, "compile", spec.DeployMode)
}

// TestGameSpecFromDetected_UnknownGame pins the defaults an uncurated
// candidate needs: the derived slug becomes the game id, and the mod path
// - which detection deliberately left empty - defaults to <install>/mods,
// the same default core has always applied to a bare `game add`.
func TestGameSpecFromDetected_UnknownGame(t *testing.T) {
	install := t.TempDir()
	spec := core.GameSpecFromDetected(domain.DetectedGame{
		SteamAppID: "526870", Slug: "satisfactory", Name: "Satisfactory", InstallPath: install,
	}, core.GameSpec{SourceID: "nexusmods", Identifier: "satisfactory"})

	assert.Equal(t, "Satisfactory", spec.Name)
	assert.Equal(t, "satisfactory", spec.ID)
	assert.Equal(t, install, spec.InstallPath)
	assert.Equal(t, filepath.Join(install, "mods"), spec.ModPath)
	assert.Equal(t, "nexusmods", spec.SourceID)
	assert.Equal(t, "satisfactory", spec.Identifier)
	assert.Empty(t, spec.Sources)
}

// TestGameSpecFromDetected_OverridesWin pins the rule the CLI's flags and
// the web form both rely on: anything the caller supplied beats the
// candidate, field by field.
func TestGameSpecFromDetected_OverridesWin(t *testing.T) {
	install, otherInstall, mods := t.TempDir(), t.TempDir(), t.TempDir()
	spec := core.GameSpecFromDetected(detectedSkyrim(install), core.GameSpec{
		Name: "My Skyrim", ID: "skyrim-modded", InstallPath: otherInstall, ModPath: mods,
		SourceID: "curseforge", Identifier: "432", LinkMethod: domain.LinkHardlink,
	})

	assert.Equal(t, "My Skyrim", spec.Name)
	assert.Equal(t, "skyrim-modded", spec.ID)
	assert.Equal(t, otherInstall, spec.InstallPath)
	assert.Equal(t, mods, spec.ModPath)
	assert.Equal(t, domain.LinkHardlink, spec.LinkMethod)
	// The curated map is still the base - an explicit source is ADDED to
	// it, never a silent replacement that drops what detection knew.
	assert.Equal(t, map[string]string{"nexusmods": "skyrimspecialedition"}, spec.Sources)
	assert.Equal(t, "curseforge", spec.SourceID)
}

// TestAddGame_FromDetectedKnownGame is the end-to-end claim: a prefilled
// spec writes the same games.yaml entry `lmm game detect` would have.
func TestAddGame_FromDetectedKnownGame(t *testing.T) {
	svc := newGameAddService(t)
	install := t.TempDir()

	entry, err := svc.AddGame(context.Background(), core.GameSpecFromDetected(detectedSkyrim(install), core.GameSpec{}))
	require.NoError(t, err)
	assert.Equal(t, "skyrim-se", entry.ID)
	assert.Equal(t, map[string]string{"nexusmods": "skyrimspecialedition"}, entry.SourceIDs)
	assert.Equal(t, filepath.Join(install, "Data"), entry.ModPath)
}

// TestAddGame_FromDetectedIcarusPersistsDeployModeCompile is the end-to-end
// claim Minor 4 of the #206 review found unguarded: GameSpec.DeployMode
// (judgement call (b), outside the brief's field list but required for
// correctness) must actually reach the games.yaml `deploy_mode` a
// from-detected Icarus is configured with, not just the intermediate
// GameSpec TestGameSpecFromDetected_KnownGameWithSourceMap already pins.
func TestAddGame_FromDetectedIcarusPersistsDeployModeCompile(t *testing.T) {
	svc := newGameAddService(t)
	svc.RegisterSource(&catalogLessSource{id: "icarus", name: "Icarus"})
	install := t.TempDir()

	detected := domain.DetectedGame{
		SteamAppID: "1149460", Slug: "icarus", Name: "Icarus",
		InstallPath: install, ModPath: filepath.Join(install, "Icarus", "Content", "Paks", "mods"),
		DeployMode: "compile", Sources: map[string]string{"icarus": "icarus"}, Known: true,
	}
	_, err := svc.AddGame(context.Background(), core.GameSpecFromDetected(detected, core.GameSpec{}))
	require.NoError(t, err)

	games, err := config.LoadGames(svc.ConfigDir())
	require.NoError(t, err)
	require.Contains(t, games, "icarus")
	assert.Equal(t, domain.DeployCompile, games["icarus"].DeployMode)
}

// TestAddGame_SourcesMapAndExplicitSourceMerge pins spec.Sources as the
// base an explicit SourceID/Identifier is layered onto, which is how a
// curated multi-source game gains a second source in one add.
func TestAddGame_SourcesMapAndExplicitSourceMerge(t *testing.T) {
	svc := newGameAddService(t)
	install := t.TempDir()

	entry, err := svc.AddGame(context.Background(), core.GameSpec{
		Sources:  map[string]string{"nexusmods": "skyrimspecialedition"},
		SourceID: "curseforge", Identifier: "432",
		Name: "Skyrim", ID: "skyrim-se", InstallPath: install,
	})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"nexusmods": "skyrimspecialedition", "curseforge": "432"}, entry.SourceIDs)
}

// TestAddGame_SourcesMapAlone pins that a spec carrying only a source MAP
// (the curated prefill's shape) is complete - no SourceID/Identifier pair
// is required on top of it.
func TestAddGame_SourcesMapAlone(t *testing.T) {
	svc := newGameAddService(t)
	install := t.TempDir()

	entry, err := svc.AddGame(context.Background(), core.GameSpec{
		Sources: map[string]string{"nexusmods": "acme"}, Name: "Acme", ID: "acme", InstallPath: install,
	})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"nexusmods": "acme"}, entry.SourceIDs)
}

// TestAddGame_SourcesMapUnregisteredSource refuses a map naming a source
// that is not registered, the same way an unregistered --source is
// refused - and names the "sources" field so a form marks the row.
func TestAddGame_SourcesMapUnregisteredSource(t *testing.T) {
	svc := newGameAddService(t)

	_, err := svc.AddGame(context.Background(), core.GameSpec{
		Sources: map[string]string{"nope": "acme"}, Name: "Acme", ID: "acme", InstallPath: t.TempDir(),
	})
	require.Error(t, err)
	var specErr *core.GameSpecError
	require.ErrorAs(t, err, &specErr)
	assert.Equal(t, "sources", specErr.Field)
}

// TestAddGame_InvalidDeployMode fails loud rather than silently defaulting
// to extract, the rule #172 set for the same value in steam-games.yaml.
func TestAddGame_InvalidDeployMode(t *testing.T) {
	svc := newGameAddService(t)

	_, err := svc.AddGame(context.Background(), core.GameSpec{
		SourceID: "nexusmods", Identifier: "acme", Name: "Acme", InstallPath: t.TempDir(),
		DeployMode: "teleport",
	})
	require.Error(t, err)
	var specErr *core.GameSpecError
	require.ErrorAs(t, err, &specErr)
	assert.Equal(t, "deploy_mode", specErr.Field)
	assert.True(t, errors.Is(err, domain.ErrInvalidDeployMode))
}

// TestFindDetectedGame resolves the Steam app id both frontends take
// (--from-detected, from_steam_app_id) against a scan, and names the field
// on the miss so a web form can mark the offending input.
func TestFindDetectedGame(t *testing.T) {
	games := []domain.DetectedGame{{SteamAppID: "489830", Slug: "skyrim-se"}, {SteamAppID: "526870", Slug: "satisfactory"}}

	got, err := core.FindDetectedGame(games, "526870")
	require.NoError(t, err)
	assert.Equal(t, "satisfactory", got.Slug)

	_, err = core.FindDetectedGame(games, "1")
	require.Error(t, err)
	var specErr *core.GameSpecError
	require.ErrorAs(t, err, &specErr)
	assert.Equal(t, "from_steam_app_id", specErr.Field)
	assert.Equal(t, "1", specErr.Value)
	// #206 review Minor 7: the reason must stay frontend-neutral - both
	// callers already scan with IncludeUnknown:true before reaching here,
	// so telling either of them to rescan wider would not even help (the
	// app id genuinely is not installed, or was uninstalled since the
	// scan). The SPA's own stale-scan banner sits directly beside its own
	// Rescan button, which said the same CLI-flavored thing redundantly.
	assert.NotContains(t, specErr.Reason, "detect scan",
		"the core message must not tell either frontend to run a specific command")
}

// TestExactGameCatalogMatch is the auto-pick rule: a catalog search by the
// detected game's NAME resolves itself only when exactly one match carries
// that same name. Anything else - several matches, a near miss, two
// entries sharing the name - is the caller's pick to make.
func TestExactGameCatalogMatch(t *testing.T) {
	report := &core.GameCatalogReport{Matches: []core.GameCatalogMatch{
		{Identifier: "432", Name: "Minecraft"},
		{Identifier: "78022", Name: "Minecraft Dungeons"},
	}}
	m := core.ExactGameCatalogMatch(report, "  minecraft ")
	require.NotNil(t, m)
	assert.Equal(t, "432", m.Identifier)

	assert.Nil(t, core.ExactGameCatalogMatch(report, "Minecraft: Java Edition"))
	assert.Nil(t, core.ExactGameCatalogMatch(report, ""))
	assert.Nil(t, core.ExactGameCatalogMatch(nil, "Minecraft"))
	assert.Nil(t, core.ExactGameCatalogMatch(&core.GameCatalogReport{Matches: []core.GameCatalogMatch{
		{Identifier: "1", Name: "Twin"}, {Identifier: "2", Name: "twin"},
	}}, "Twin"), "two entries sharing the name is ambiguous, not an auto-pick")
}

// TestGameDetectListing_UnknownRows pins the listing's #206 shape: unknown
// rows appear only when asked for, carry known:false, and - crucially -
// carry NO index, because a detect selection cannot name them. The known
// rows' numbering is identical either way, so an index means the same row
// whether or not the caller asked for the wider list.
func TestGameDetectListing_UnknownRows(t *testing.T) {
	svc := newGameAddService(t)
	scan := []domain.DetectedGame{
		{Slug: "satisfactory", Name: "Satisfactory", InstallPath: "/games/sf"},
		{Slug: "skyrim-se", Name: "Skyrim Special Edition", InstallPath: "/games/skyrim", Known: true},
		{Slug: "hades", Name: "Hades", InstallPath: "/games/hades"},
		{Slug: "icarus", Name: "Icarus", InstallPath: "/games/icarus", Known: true},
	}

	knownOnly, err := svc.GameDetectListing(context.Background(), scan, nil, core.GameDetectListingOptions{})
	require.NoError(t, err)
	require.Len(t, knownOnly.Games, 2)
	assert.Equal(t, []int{1, 2}, []int{knownOnly.Games[0].Index, knownOnly.Games[1].Index})

	all, err := svc.GameDetectListing(context.Background(), scan, nil, core.GameDetectListingOptions{IncludeUnknown: true})
	require.NoError(t, err)
	require.Len(t, all.Games, 4)
	assert.Equal(t, 0, all.Games[0].Index, "an unknown row is not selectable, so it has no index")
	assert.False(t, all.Games[0].Known)
	assert.Equal(t, 1, all.Games[1].Index)
	assert.True(t, all.Games[1].Known)
	assert.Equal(t, 0, all.Games[2].Index)
	assert.Equal(t, 2, all.Games[3].Index, "known numbering must not shift when unknown rows join")
}

// TestSelectDetectedGames_RefusesUnknown: a detect selection configures a
// game from its curated entry, and an unknown candidate has none - no mod
// path, no sources. Naming one is refused with the sentinel both frontends
// branch on to point at the from-detected add flow instead.
func TestSelectDetectedGames_RefusesUnknown(t *testing.T) {
	games := []domain.DetectedGame{
		{SteamAppID: "489830", Slug: "skyrim-se", Known: true},
		{SteamAppID: "526870", Slug: "satisfactory"},
	}

	_, err := core.SelectDetectedGames(games, []string{"satisfactory"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, core.ErrUnknownDetectedGame))
	assert.Contains(t, err.Error(), "526870")

	// Indices still count known rows only, so "1" is Skyrim either way.
	got, err := core.SelectDetectedGames(games, []string{"1"})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "skyrim-se", got[0].Slug)
}
