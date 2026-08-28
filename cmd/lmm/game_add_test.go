package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/source/curseforge"
	"github.com/DonovanMods/linux-mod-manager/internal/source/nexusmods"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockGameAddSource is a minimal source.ModSource double for game-add menu
// and manual-flow tests: no source.GameCatalog, exercising the
// manual-identifier path a catalog-less source takes (today's NexusMods
// slug path, generalized).
type mockGameAddSource struct {
	id, name string
}

func (m *mockGameAddSource) ID() string      { return m.id }
func (m *mockGameAddSource) Name() string    { return m.name }
func (m *mockGameAddSource) AuthURL() string { return "" }
func (m *mockGameAddSource) ExchangeToken(context.Context, string) (*source.Token, error) {
	return nil, nil
}
func (m *mockGameAddSource) Search(context.Context, source.SearchQuery) (source.SearchResult, error) {
	return source.SearchResult{}, nil
}
func (m *mockGameAddSource) GetMod(context.Context, string, string) (*domain.Mod, error) {
	return nil, nil
}
func (m *mockGameAddSource) GetDependencies(context.Context, *domain.Mod) ([]domain.ModReference, error) {
	return nil, nil
}
func (m *mockGameAddSource) GetModFiles(context.Context, *domain.Mod) ([]domain.DownloadableFile, error) {
	return nil, nil
}
func (m *mockGameAddSource) GetDownloadURL(context.Context, *domain.Mod, string) (string, error) {
	return "", nil
}
func (m *mockGameAddSource) CheckUpdates(context.Context, []domain.InstalledMod) ([]domain.Update, error) {
	return nil, nil
}

// mockGameAddCatalogSource additionally implements source.GameCatalog, for
// exercising the catalog-search flow (today's CurseForge path, generalized)
// against a source that is neither NexusMods nor CurseForge - proving the
// dispatch is driven by the interface, not source identity.
type mockGameAddCatalogSource struct {
	mockGameAddSource
	entries []source.GameEntry
	listErr error
}

func (m *mockGameAddCatalogSource) ListGames(ctx context.Context) ([]source.GameEntry, error) {
	return m.entries, m.listErr
}

var (
	_ source.ModSource   = (*mockGameAddSource)(nil)
	_ source.GameCatalog = (*mockGameAddCatalogSource)(nil)
)

// setupGameAddTest builds a *core.Service backed by the same configDir the
// package-level saveGameConfig path reads via getServiceConfig (so games
// saved during the test are visible to config.LoadGames afterward), without
// running the real app.Open source-registration pipeline (no network).
func setupGameAddTest(t *testing.T) *core.Service {
	t.Helper()
	configDir = t.TempDir()
	dataDir = t.TempDir()

	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: configDir, DataDir: dataDir, CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	return svc
}

func newGameAddCmd() (*cobra.Command, *bytes.Buffer) {
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)
	return cmd, &buf
}

// TestDoGameAdd_MenuListsRegisteredSourcesSortedByID pins the registry-driven
// menu (design §4.3): every registered source appears as "Name (id)",
// ordered by ID - not the old literal two-item CurseForge/NexusMods list.
// svc.ListSources() carries no ordering guarantee (registry.List() ranges a
// map), so an unsorted menu would flake; a mock custom source proves the
// menu isn't special-cased to the two built-ins.
func TestDoGameAdd_MenuListsRegisteredSourcesSortedByID(t *testing.T) {
	svc := setupGameAddTest(t)
	svc.RegisterSource(nexusmods.New(nil, ""))
	svc.RegisterSource(curseforge.New(nil, ""))
	svc.RegisterSource(&mockGameAddSource{id: "acme-mods", name: "Acme Mods"})

	cmd, buf := newGameAddCmd()
	// Deliberately invalid choice: this test only cares about the menu
	// rendered before the choice is read, not about driving a full flow.
	reader := bufio.NewReader(strings.NewReader("9\n"))

	err := doGameAdd(context.Background(), cmd, reader, svc)
	require.Error(t, err)

	out := buf.String()
	// Sorted by ID: acme-mods, curseforge, nexusmods.
	assert.Contains(t, out, "[1] Acme Mods (acme-mods)")
	assert.Contains(t, out, "[2] CurseForge (curseforge)")
	assert.Contains(t, out, "[3] Nexus Mods (nexusmods)")
}

// TestDoGameAdd_NoRegisteredSources guards the degenerate case a bare
// registry (e.g. a test double set) can reach: an empty menu must error
// instead of prompting over zero options.
func TestDoGameAdd_NoRegisteredSources(t *testing.T) {
	svc := setupGameAddTest(t)

	cmd, _ := newGameAddCmd()
	reader := bufio.NewReader(strings.NewReader(""))

	err := doGameAdd(context.Background(), cmd, reader, svc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no mod sources are registered")
}

// TestDoGameAdd_CatalogPath_DrivesMockGameCatalog pins the catalog-search
// flow (today's CurseForge path, generalized) against a mock
// source.GameCatalog that is neither built-in: ListGames -> filter by query
// -> select -> save. Proves the flow is driven by the GameCatalog interface,
// not curseforge-specific code.
func TestDoGameAdd_CatalogPath_DrivesMockGameCatalog(t *testing.T) {
	svc := setupGameAddTest(t)
	svc.RegisterSource(&mockGameAddCatalogSource{
		mockGameAddSource: mockGameAddSource{id: "acme-cat", name: "Acme Catalog"},
		entries: []source.GameEntry{
			{ID: "42", Name: "Acme Quest", Slug: "acme-quest"},
			{ID: "7", Name: "Other Game", Slug: "other-game"},
		},
	})

	input := strings.Join([]string{
		"1",                    // select acme-cat (only registered source)
		"quest",                // search query
		"1",                    // select the (sole) match
		"/opt/games/acmequest", // install path
		"",                     // accept default mod path
	}, "\n") + "\n"

	cmd, buf := newGameAddCmd()
	reader := bufio.NewReader(strings.NewReader(input))

	err := doGameAdd(context.Background(), cmd, reader, svc)
	require.NoError(t, err, "output so far:\n%s", buf.String())

	games, err := config.LoadGames(configDir)
	require.NoError(t, err)
	game, ok := games["acme-quest"]
	require.True(t, ok, "expected a game keyed by slug %q; got %v", "acme-quest", games)
	assert.Equal(t, map[string]string{"acme-cat": "42"}, game.SourceIDs)
	assert.Equal(t, "/opt/games/acmequest", game.InstallPath)
	assert.Equal(t, "/opt/games/acmequest/mods", game.ModPath)
}

// TestDoGameAdd_CatalogPath_NoMatches proves an empty filter result reports
// and returns cleanly (no error) - matching today's CurseForge "no games
// found" behavior rather than treating an empty result as a failure.
func TestDoGameAdd_CatalogPath_NoMatches(t *testing.T) {
	svc := setupGameAddTest(t)
	svc.RegisterSource(&mockGameAddCatalogSource{
		mockGameAddSource: mockGameAddSource{id: "acme-cat", name: "Acme Catalog"},
		entries: []source.GameEntry{
			{ID: "42", Name: "Acme Quest", Slug: "acme-quest"},
		},
	})

	input := "1\nnonexistent\n"
	cmd, buf := newGameAddCmd()
	reader := bufio.NewReader(strings.NewReader(input))

	err := doGameAdd(context.Background(), cmd, reader, svc)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), `No games found matching "nonexistent"`)
}

// TestDoGameAdd_CatalogPath_AuthRequiredSurfacesFriendlyPrompt pins the
// restored auth UX: the codebase convention (helpers.go's authPromptError,
// applied at 5 sibling sites - search.go, install.go x3, update.go x2) is
// for a source to return a domain.ErrAuthRequired-wrapped error, and the
// caller to errors.Is + rewrap into "run lmm auth login <id>" instead of
// surfacing the raw API error. runGameAddCatalog must do the same for
// ListGames, rather than the generic "fetching games from %s: %w" wrap
// swallowing the sentinel.
func TestDoGameAdd_CatalogPath_AuthRequiredSurfacesFriendlyPrompt(t *testing.T) {
	svc := setupGameAddTest(t)
	svc.RegisterSource(&mockGameAddCatalogSource{
		mockGameAddSource: mockGameAddSource{id: "acme-cat", name: "Acme Catalog"},
		listErr:           fmt.Errorf("listing games: %w", domain.ErrAuthRequired),
	})

	input := "1\nquest\n"
	cmd, buf := newGameAddCmd()
	reader := bufio.NewReader(strings.NewReader(input))

	err := doGameAdd(context.Background(), cmd, reader, svc)
	require.Error(t, err, "output so far:\n%s", buf.String())
	// authPromptError's own message doesn't wrap domain.ErrAuthRequired
	// further (it's the terminal, user-facing rewrap - matching every
	// sibling call site, none of which assert errors.Is on its result
	// either); the pin here is the message match itself.
	assert.Equal(t, authPromptError("acme-cat").Error(), err.Error())
}

// TestDoGameAdd_ManualPath_DrivesCatalogLessSource pins the manual-identifier
// flow (today's NexusMods slug path, generalized): a registered source with
// no GameCatalog prompts for a display name and the source's identifier
// directly, with no search/catalog step.
func TestDoGameAdd_ManualPath_DrivesCatalogLessSource(t *testing.T) {
	svc := setupGameAddTest(t)
	svc.RegisterSource(&mockGameAddSource{id: "acme-manual", name: "Acme Manual"})

	input := strings.Join([]string{
		"1",               // select acme-manual (only registered source)
		"Acme Quest",      // game name (display)
		"acme-quest-slug", // source identifier
		"/opt/games/acme", // install path
		"",                // accept default mod path
	}, "\n") + "\n"

	cmd, buf := newGameAddCmd()
	reader := bufio.NewReader(strings.NewReader(input))

	err := doGameAdd(context.Background(), cmd, reader, svc)
	require.NoError(t, err, "output so far:\n%s", buf.String())

	games, err := config.LoadGames(configDir)
	require.NoError(t, err)
	game, ok := games["acme-quest-slug"]
	require.True(t, ok, "expected a game keyed by slug %q; got %v", "acme-quest-slug", games)
	assert.Equal(t, "Acme Quest", game.Name)
	assert.Equal(t, map[string]string{"acme-manual": "acme-quest-slug"}, game.SourceIDs)
}

// TestDoGameAdd_ZeroMigration_CurseForgeShape pins that the generalized
// catalog path saves CurseForge's identifier exactly as today's
// doGameAddCurseForge did: SourceIDs{"curseforge": "<numeric id string>"}.
// The mock's entries mirror the exact shape CurseForge.ListGames produces
// (internal/source/curseforge/curseforge.go: GameEntry{ID:
// strconv.Itoa(g.ID), Name: g.Name, Slug: g.Slug}) - registered under the
// real "curseforge" ID so this exercises the identical
// map[string]string{sourceID: catalogIdentifier(selected)} save path a real
// CurseForge source would take. A real network-backed CurseForge instance
// isn't used here because CurseForge exposes no exported seam to redirect
// its HTTP client at a test server from outside its own package (out of
// scope for this task's file list) - the mapping formula itself
// (GameEntry.ID = strconv.Itoa(g.ID)) was verified by reading
// curseforge.go directly, and matches exactly what this test drives.
func TestDoGameAdd_ZeroMigration_CurseForgeShape(t *testing.T) {
	svc := setupGameAddTest(t)
	svc.RegisterSource(&mockGameAddCatalogSource{
		mockGameAddSource: mockGameAddSource{id: "curseforge", name: "CurseForge"},
		entries: []source.GameEntry{
			{ID: "432", Name: "Minecraft", Slug: "minecraft"},
		},
	})

	input := "1\nminecraft\n1\n/opt/games/minecraft\n\n"
	cmd, buf := newGameAddCmd()
	reader := bufio.NewReader(strings.NewReader(input))

	err := doGameAdd(context.Background(), cmd, reader, svc)
	require.NoError(t, err, "output so far:\n%s", buf.String())

	games, err := config.LoadGames(configDir)
	require.NoError(t, err)
	game, ok := games["minecraft"]
	require.True(t, ok)
	assert.Equal(t, map[string]string{"curseforge": "432"}, game.SourceIDs)

	raw, err := readGamesYAML(t)
	require.NoError(t, err)
	assert.Contains(t, raw, `curseforge: "432"`)
}

// TestDoGameAdd_ZeroMigration_NexusModsShape pins that the generalized
// manual path saves NexusMods' identifier exactly as today's
// runGameAddNexusMods did: SourceIDs{"nexusmods": "<slug>"}, using the real
// nexusmods.NexusMods source (no network call is reachable on the manual
// path - it never calls the source beyond ID()/Name()).
func TestDoGameAdd_ZeroMigration_NexusModsShape(t *testing.T) {
	svc := setupGameAddTest(t)
	svc.RegisterSource(nexusmods.New(nil, ""))

	input := strings.Join([]string{
		"1", // select nexusmods (only registered source)
		"Skyrim Special Edition",
		"skyrimspecialedition",
		"/opt/games/skyrim",
		"",
	}, "\n") + "\n"

	cmd, buf := newGameAddCmd()
	reader := bufio.NewReader(strings.NewReader(input))

	err := doGameAdd(context.Background(), cmd, reader, svc)
	require.NoError(t, err, "output so far:\n%s", buf.String())

	games, err := config.LoadGames(configDir)
	require.NoError(t, err)
	game, ok := games["skyrimspecialedition"]
	require.True(t, ok)
	assert.Equal(t, map[string]string{"nexusmods": "skyrimspecialedition"}, game.SourceIDs)

	raw, err := readGamesYAML(t)
	require.NoError(t, err)
	assert.Contains(t, raw, `nexusmods: skyrimspecialedition`)
}

// TestCatalogIdentifier_PrefersIDFallsBackToSlug pins the decision for a
// GameCatalog implementer that only populates Slug (design brief: "prefer
// ID, fall back to Slug"): ID wins when present (CurseForge's case), Slug is
// used only when ID is empty.
func TestCatalogIdentifier_PrefersIDFallsBackToSlug(t *testing.T) {
	assert.Equal(t, "42", catalogIdentifier(source.GameEntry{ID: "42", Slug: "the-slug"}))
	assert.Equal(t, "the-slug", catalogIdentifier(source.GameEntry{Slug: "the-slug"}))
	assert.Equal(t, "", catalogIdentifier(source.GameEntry{}))
}

// readGamesYAML reads the raw games.yaml bytes from the test's configDir,
// for byte-shape assertions the map-based assertions above can't make (map
// key order/quoting).
func readGamesYAML(t *testing.T) (string, error) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(configDir, "games.yaml"))
	return string(data), err
}

// TestDoGameAdd_CatalogPath_EmptySlugFallsBackToIdentifier pins the local
// game-ID derivation for a catalog entry with no Slug (GameEntry doesn't
// guarantee it; CurseForge happens to set it): the ID falls back to
// catalogIdentifier's value instead of saving a games.yaml entry keyed "".
func TestDoGameAdd_CatalogPath_EmptySlugFallsBackToIdentifier(t *testing.T) {
	svc := setupGameAddTest(t)
	svc.RegisterSource(&mockGameAddCatalogSource{
		mockGameAddSource: mockGameAddSource{id: "acme-cat", name: "Acme Catalog"},
		entries: []source.GameEntry{
			{ID: "Game 99", Name: "Slugless Quest", Slug: ""},
		},
	})

	input := strings.Join([]string{
		"1",                   // select acme-cat
		"quest",               // search query
		"1",                   // select the sole match
		"/opt/games/slugless", // install path
		"",                    // accept default mod path
	}, "\n") + "\n"

	cmd, buf := newGameAddCmd()
	reader := bufio.NewReader(strings.NewReader(input))

	err := doGameAdd(context.Background(), cmd, reader, svc)
	require.NoError(t, err, "output so far:\n%s", buf.String())

	games, err := config.LoadGames(configDir)
	require.NoError(t, err)
	// "Game 99" -> lowercased, spaces dashed: "game-99".
	game, ok := games["game-99"]
	require.True(t, ok, "expected the ID-derived key %q; got %v", "game-99", games)
	assert.Equal(t, map[string]string{"acme-cat": "Game 99"}, game.SourceIDs)
	_, empty := games[""]
	assert.False(t, empty, "an empty-string game key must never be written")
}

// TestDoGameAdd_CatalogPath_NoUsableIdentifierErrors pins the guard for a
// catalog entry populating neither ID nor Slug: a clear error naming the
// source, never a games.yaml entry keyed "".
func TestDoGameAdd_CatalogPath_NoUsableIdentifierErrors(t *testing.T) {
	svc := setupGameAddTest(t)
	svc.RegisterSource(&mockGameAddCatalogSource{
		mockGameAddSource: mockGameAddSource{id: "acme-cat", name: "Acme Catalog"},
		entries: []source.GameEntry{
			{ID: "", Name: "Broken Entry", Slug: ""},
		},
	})

	input := "1\nbroken\n1\n"
	cmd, buf := newGameAddCmd()
	reader := bufio.NewReader(strings.NewReader(input))

	err := doGameAdd(context.Background(), cmd, reader, svc)
	require.Error(t, err, "output so far:\n%s", buf.String())
	assert.Contains(t, err.Error(), "no usable identifier")
	assert.Contains(t, err.Error(), "acme-cat")

	games, err := config.LoadGames(configDir)
	require.NoError(t, err)
	_, empty := games[""]
	assert.False(t, empty, "an empty-string game key must never be written")
}
