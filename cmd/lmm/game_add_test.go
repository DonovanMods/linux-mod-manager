package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/curseforge"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/nexusmods"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
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

// mockIdentifierIgnoringSource declares that its per-game mapped value
// addresses nothing (P1b review F5) - what a directory source does.
type mockIdentifierIgnoringSource struct{ mockGameAddSource }

func (m *mockIdentifierIgnoringSource) IgnoresGameIdentifier() bool { return true }

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
	resetGameAddFlags(t)
	return svc
}

// resetGameAddFlags zeroes #307's cobra flag variables for the duration of
// one test. They are package globals bound by init(), so a test that sets
// one would otherwise leak it into every test that runs after it.
func resetGameAddFlags(t *testing.T) {
	t.Helper()
	src, id, query, pick := gameAddSource, gameAddID, gameAddQuery, gameAddPick
	name, gameID, path, modPath := gameAddName, gameAddGameID, gameAddPath, gameAddModPath
	fromDetected := gameAddFromDetected
	t.Cleanup(func() {
		gameAddSource, gameAddID, gameAddQuery, gameAddPick = src, id, query, pick
		gameAddName, gameAddGameID, gameAddPath, gameAddModPath = name, gameID, path, modPath
		gameAddFromDetected = fromDetected
	})
	gameAddSource, gameAddID, gameAddQuery, gameAddPick = "", "", "", 0
	gameAddName, gameAddGameID, gameAddPath, gameAddModPath = "", "", "", ""
	gameAddFromDetected = ""
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

// TestDoGameAdd_JSON_MissingSourceRefusesWithoutReadingStdin pins the
// post-#307 shape of the non-interactive rule (Ruling 2). `game add` is no
// longer interactive-only - a fully-flagged run works under --json - so the
// refusal narrowed from "this command" to "this value": with no --source
// there is nothing to resolve the source prompt, and the error names the
// flag while stdin is provably never touched.
func TestDoGameAdd_JSON_MissingSourceRefusesWithoutReadingStdin(t *testing.T) {
	svc := setupGameAddTest(t)
	svc.RegisterSource(&mockGameAddSource{id: "acme-manual", name: "Acme Manual"})
	withJSONOutput(t)

	cmd, _ := newGameAddCmd()
	err := doGameAdd(context.Background(), cmd, bufio.NewReader(poisonReader{t: t}), svc)

	require.ErrorIs(t, err, core.ErrInteractiveOnly)
	assert.Contains(t, err.Error(), "--source")
}

// TestDoGameAdd_JSON_MissingValueNamesItsFlag covers the other three
// per-value refusals a --json run can hit, each naming the flag that
// answers it. Table-driven because they differ only in which flag is
// withheld.
func TestDoGameAdd_JSON_MissingValueNamesItsFlag(t *testing.T) {
	tests := []struct {
		name      string
		id, path  string
		wantsFlag string
	}{
		{"no identifier", "", "", "--id"},
		{"no install path", "acme-quest", "", "--path"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := setupGameAddTest(t)
			svc.RegisterSource(&mockGameAddSource{id: "acme-manual", name: "Acme Manual"})
			withJSONOutput(t)
			gameAddSource, gameAddID, gameAddName, gameAddPath = "acme-manual", tt.id, "Acme Quest", tt.path

			cmd, _ := newGameAddCmd()
			err := doGameAdd(context.Background(), cmd, bufio.NewReader(poisonReader{t: t}), svc)

			require.ErrorIs(t, err, core.ErrInteractiveOnly)
			assert.Contains(t, err.Error(), tt.wantsFlag)
		})
	}
}

// TestDoGameAdd_FullyFlagged_ManualPath pins #307's headline: every prompt
// answered by a flag, no stdin, and the core.GameListEntry document `lmm
// game list --json` emits for the same game.
func TestDoGameAdd_FullyFlagged_ManualPath(t *testing.T) {
	svc := setupGameAddTest(t)
	svc.RegisterSource(&mockGameAddSource{id: "acme-manual", name: "Acme Manual"})
	withJSONOutput(t)
	installDir, modDir := t.TempDir(), t.TempDir()
	gameAddSource, gameAddID, gameAddName = "acme-manual", "acme-quest-slug", "Acme Quest"
	gameAddPath, gameAddModPath = installDir, modDir

	cmd, _ := newGameAddCmd()
	out := captureStdout(t, func() error {
		return doGameAdd(context.Background(), cmd, bufio.NewReader(poisonReader{t: t}), svc)
	})

	var entry core.GameListEntry
	require.NoError(t, json.Unmarshal([]byte(out), &entry, json.RejectUnknownMembers(true)))
	assert.Equal(t, "acme-quest-slug", entry.ID)
	assert.Equal(t, "Acme Quest", entry.Name)
	assert.Equal(t, installDir, entry.InstallPath)
	assert.Equal(t, modDir, entry.ModPath)
	assert.Equal(t, map[string]string{"acme-manual": "acme-quest-slug"}, entry.SourceIDs)
}

// TestDoGameAdd_GameIDFlag_ManualPath pins #333 Minor #4: --game-id sets
// the LOCAL games.yaml key on the manual path, distinct from --id (the
// SOURCE identifier) - the one-way parity hole against POST
// /api/v1/games' game_id member, which could already do this.
func TestDoGameAdd_GameIDFlag_ManualPath(t *testing.T) {
	svc := setupGameAddTest(t)
	svc.RegisterSource(&mockGameAddSource{id: "acme-manual", name: "Acme Manual"})
	withJSONOutput(t)
	installDir := t.TempDir()
	gameAddSource, gameAddID, gameAddName = "acme-manual", "acme-quest-slug", "Acme Quest"
	gameAddGameID, gameAddPath = "my-local-key", installDir

	cmd, _ := newGameAddCmd()
	out := captureStdout(t, func() error {
		return doGameAdd(context.Background(), cmd, bufio.NewReader(poisonReader{t: t}), svc)
	})

	var entry core.GameListEntry
	require.NoError(t, json.Unmarshal([]byte(out), &entry, json.RejectUnknownMembers(true)))
	assert.Equal(t, "my-local-key", entry.ID, "the LOCAL key is --game-id, not derived from --id")
	assert.Equal(t, map[string]string{"acme-manual": "acme-quest-slug"}, entry.SourceIDs,
		"the SOURCE identifier is still --id, stored verbatim")
}

// TestDoGameAdd_QueryWithoutPick_EmitsTheCatalogDocument pins the two-step
// catalog flow a non-interactive caller uses: --query alone answers with
// the matches (core.GameCatalogReport) and adds nothing, so the caller can
// re-run with --pick. It is not an error - the caller asked what matched.
func TestDoGameAdd_QueryWithoutPick_EmitsTheCatalogDocument(t *testing.T) {
	svc := setupGameAddTest(t)
	svc.RegisterSource(&mockGameAddCatalogSource{
		mockGameAddSource: mockGameAddSource{id: "acme-cat", name: "Acme Catalog"},
		entries: []source.GameEntry{
			{ID: "42", Name: "Acme Quest", Slug: "acme-quest"},
			{ID: "7", Name: "Other Game", Slug: "other-game"},
		},
	})
	withJSONOutput(t)
	gameAddSource, gameAddQuery = "acme-cat", "quest"

	cmd, _ := newGameAddCmd()
	out := captureStdout(t, func() error {
		return doGameAdd(context.Background(), cmd, bufio.NewReader(poisonReader{t: t}), svc)
	})

	var report core.GameCatalogReport
	require.NoError(t, json.Unmarshal([]byte(out), &report, json.RejectUnknownMembers(true)))
	assert.Equal(t, "acme-cat", report.SourceID)
	require.Len(t, report.Matches, 1)
	assert.Equal(t, "acme-quest", report.Matches[0].GameID)

	games, err := config.LoadGames(configDir)
	require.NoError(t, err)
	assert.Empty(t, games, "a search must add nothing")
}

// TestDoGameAdd_QueryWithPick_AddsTheMatch pins the second step: --pick
// resolves a match, and the saved game is keyed by the match's SLUG-derived
// id with the source identifier stored verbatim - the CurseForge shape
// ("minecraft" keyed, "432" stored).
func TestDoGameAdd_QueryWithPick_AddsTheMatch(t *testing.T) {
	svc := setupGameAddTest(t)
	svc.RegisterSource(&mockGameAddCatalogSource{
		mockGameAddSource: mockGameAddSource{id: "curseforge", name: "CurseForge"},
		entries:           []source.GameEntry{{ID: "432", Name: "Minecraft", Slug: "minecraft"}},
	})
	withJSONOutput(t)
	installDir := t.TempDir()
	gameAddSource, gameAddQuery, gameAddPick, gameAddPath = "curseforge", "mine", 1, installDir

	cmd, _ := newGameAddCmd()
	out := captureStdout(t, func() error {
		return doGameAdd(context.Background(), cmd, bufio.NewReader(poisonReader{t: t}), svc)
	})

	var entry core.GameListEntry
	require.NoError(t, json.Unmarshal([]byte(out), &entry, json.RejectUnknownMembers(true)))
	assert.Equal(t, "minecraft", entry.ID)
	assert.Equal(t, "Minecraft", entry.Name, "the match's name is the default display name")
	assert.Equal(t, map[string]string{"curseforge": "432"}, entry.SourceIDs)
}

// TestDoGameAdd_GameIDFlag_OverridesCatalogMatch pins that an explicit
// --game-id wins over the catalog match's own slug-derived suggestion -
// the flag is the caller's decision, not just a manual-path-only escape
// hatch.
func TestDoGameAdd_GameIDFlag_OverridesCatalogMatch(t *testing.T) {
	svc := setupGameAddTest(t)
	svc.RegisterSource(&mockGameAddCatalogSource{
		mockGameAddSource: mockGameAddSource{id: "curseforge", name: "CurseForge"},
		entries:           []source.GameEntry{{ID: "432", Name: "Minecraft", Slug: "minecraft"}},
	})
	withJSONOutput(t)
	installDir := t.TempDir()
	gameAddSource, gameAddQuery, gameAddPick, gameAddPath = "curseforge", "mine", 1, installDir
	gameAddGameID = "my-minecraft"

	cmd, _ := newGameAddCmd()
	out := captureStdout(t, func() error {
		return doGameAdd(context.Background(), cmd, bufio.NewReader(poisonReader{t: t}), svc)
	})

	var entry core.GameListEntry
	require.NoError(t, json.Unmarshal([]byte(out), &entry, json.RejectUnknownMembers(true)))
	assert.Equal(t, "my-minecraft", entry.ID)
	assert.Equal(t, map[string]string{"curseforge": "432"}, entry.SourceIDs)
}

// TestDoGameAdd_PickOutOfRange refuses a --pick beyond the match list
// rather than indexing past it.
func TestDoGameAdd_PickOutOfRange(t *testing.T) {
	svc := setupGameAddTest(t)
	svc.RegisterSource(&mockGameAddCatalogSource{
		mockGameAddSource: mockGameAddSource{id: "acme-cat", name: "Acme Catalog"},
		entries:           []source.GameEntry{{ID: "42", Name: "Acme Quest", Slug: "acme-quest"}},
	})
	gameAddSource, gameAddQuery, gameAddPick, gameAddPath = "acme-cat", "quest", 9, t.TempDir()

	cmd, _ := newGameAddCmd()
	err := doGameAdd(context.Background(), cmd, bufio.NewReader(strings.NewReader("")), svc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid selection")
}

// TestDoGameAdd_UnknownSourceFlag names the registered sources rather than
// leaving the caller to guess what "--source" accepts.
func TestDoGameAdd_UnknownSourceFlag(t *testing.T) {
	svc := setupGameAddTest(t)
	svc.RegisterSource(&mockGameAddSource{id: "acme-manual", name: "Acme Manual"})
	gameAddSource = "nope"

	cmd, _ := newGameAddCmd()
	err := doGameAdd(context.Background(), cmd, bufio.NewReader(strings.NewReader("")), svc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "acme-manual")
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

	installDir := t.TempDir()
	input := strings.Join([]string{
		"1",        // select acme-cat (only registered source)
		"quest",    // search query
		"1",        // select the (sole) match
		installDir, // install path (must exist)
		"",         // accept default mod path
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
	assert.Equal(t, installDir, game.InstallPath)
	assert.Equal(t, filepath.Join(installDir, "mods"), game.ModPath)
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

	installDir := t.TempDir()
	input := strings.Join([]string{
		"1",               // select acme-manual (only registered source)
		"Acme Quest",      // game name (display)
		"acme-quest-slug", // source identifier
		installDir,        // install path (must exist)
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

	installDir := t.TempDir()
	input := "1\nminecraft\n1\n" + installDir + "\n\n"
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

	installDir := t.TempDir()
	input := strings.Join([]string{
		"1", // select nexusmods (only registered source)
		"Skyrim Special Edition",
		"skyrimspecialedition",
		installDir,
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

// The identifier-vs-slug decision this file used to unit-test
// (catalogIdentifier: prefer GameEntry.ID, fall back to Slug) moved into
// core.Service.SearchGameCatalog with #307, where it is covered by
// TestSearchGameCatalog_SlugFallsBackToIdentifier - the CLI no longer owns
// that decision, so the local helper and its test are gone rather than
// duplicated.

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

	installDir := t.TempDir()
	input := strings.Join([]string{
		"1",        // select acme-cat
		"quest",    // search query
		"1",        // select the sole match
		installDir, // install path (must exist)
		"",         // accept default mod path
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
// catalog entry populating neither ID nor Slug: a typed, FIELD-NAMED
// refusal from core.AddGame, never a games.yaml entry keyed "". Before
// #307 the CLI raised this itself ("no usable identifier"); the check now
// lives with the write, so the message is core.GameSpecError's.
func TestDoGameAdd_CatalogPath_NoUsableIdentifierErrors(t *testing.T) {
	svc := setupGameAddTest(t)
	svc.RegisterSource(&mockGameAddCatalogSource{
		mockGameAddSource: mockGameAddSource{id: "acme-cat", name: "Acme Catalog"},
		entries: []source.GameEntry{
			{ID: "", Name: "Broken Entry", Slug: ""},
		},
	})

	input := "1\nbroken\n1\n" + t.TempDir() + "\n\n"
	cmd, buf := newGameAddCmd()
	reader := bufio.NewReader(strings.NewReader(input))

	err := doGameAdd(context.Background(), cmd, reader, svc)
	require.Error(t, err, "output so far:\n%s", buf.String())
	var specErr *core.GameSpecError
	require.ErrorAs(t, err, &specErr)
	assert.Equal(t, "identifier", specErr.Field)

	games, err := config.LoadGames(configDir)
	require.NoError(t, err)
	_, empty := games[""]
	assert.False(t, empty, "an empty-string game key must never be written")
}

// TestReportError_JSON_GameSpecError pins core.GameSpecError's --json
// envelope (detailsCoverage): the "<field>: <reason>" message on "error"
// and the field/value/reason document on "details", which is what lets an
// SPA form mark the offending input rather than substring-matching prose.
func TestReportError_JSON_GameSpecError(t *testing.T) {
	withJSONOutput(t)

	err := &core.GameSpecError{Field: "install_path", Value: "/games/nope", Reason: "path does not exist"}
	out := captureStdout(t, func() error { reportError(err); return nil })

	assert.Equal(t, "{\n"+
		"  \"error\": \"install_path: path does not exist\",\n"+
		"  \"details\": {\n"+
		"    \"field\": \"install_path\",\n"+
		"    \"value\": \"/games/nope\",\n"+
		"    \"reason\": \"path does not exist\"\n"+
		"  }\n"+
		"}\n", out)
}

// --- #206: `lmm game add --from-detected <steam-app-id>` ---

// fakeSteamGame fabricates a Steam library under a sandboxed HOME holding
// one installed app, and returns its install path. Detection tests must
// never read the host's real library, so HOME and STEAM_ROOT are both
// overridden (steam.FindSteamRoots reads exactly those two).
// The uncurated-game stand-in for every test below that needs an installed
// app with NO known-games entry. Fictional on purpose: these tests used to
// borrow a real uncurated game (Satisfactory), so curating it (#406) turned
// "an app lmm has never heard of" into "an app it has" - and five of them
// failed with "no source is registered with that id", because the curated
// entry's nexusmods prefill joined a service that only registers a fake.
const (
	uncuratedAppID = "9999990"
	uncuratedName  = "Uncurated Example Game"
	uncuratedDir   = "UncuratedExampleGame"
	uncuratedSlug  = "uncurated-example-game"
)

func fakeSteamGame(t *testing.T, appID, name, installDir string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("STEAM_ROOT", "")
	steamapps := filepath.Join(home, ".steam", "steam", "steamapps")
	install := filepath.Join(steamapps, "common", installDir)
	require.NoError(t, os.MkdirAll(install, 0755))
	acf := "\n\"AppState\"\n{\n\t\"appid\"\t\t\"" + appID + "\"\n\t\"name\"\t\t\"" + name +
		"\"\n\t\"installdir\"\t\t\"" + installDir + "\"\n}\n"
	require.NoError(t, os.WriteFile(filepath.Join(steamapps, "appmanifest_"+appID+".acf"), []byte(acf), 0644))
	return install
}

// TestDoGameAdd_FromDetected_KnownGameNeedsNothingElse is #206's headline:
// a curated game that is already installed is added by app id alone - the
// name, install path, game id, mod path and source mapping all come from
// the detection, and the result is the games.yaml entry `game detect`
// would have written.
func TestDoGameAdd_FromDetected_KnownGameNeedsNothingElse(t *testing.T) {
	svc := setupGameAddTest(t)
	svc.RegisterSource(nexusmods.New(nil, ""))
	install := fakeSteamGame(t, "489830", "Skyrim Special Edition", "Skyrim Special Edition")
	gameAddFromDetected = "489830"

	cmd, buf := newGameAddCmd()
	require.NoError(t, doGameAdd(context.Background(), cmd, bufio.NewReader(poisonReader{t: t}), svc))

	saved, err := config.LoadGames(configDir)
	require.NoError(t, err)
	require.Contains(t, saved, "skyrim-se")
	g := saved["skyrim-se"]
	assert.Equal(t, "Skyrim Special Edition", g.Name)
	assert.Equal(t, install, g.InstallPath)
	assert.Equal(t, filepath.Join(install, "Data"), g.ModPath)
	assert.Equal(t, map[string]string{"nexusmods": "skyrimspecialedition"}, g.SourceIDs)
	assert.Contains(t, buf.String(), "Added Skyrim Special Edition (id: skyrim-se)")
}

// TestDoGameAdd_FromDetected_UnknownGameWithExplicitSource covers the
// uncurated half: detection supplies everything except the source mapping,
// and --source/--id supply that. The mod path defaults to <install>/mods.
func TestDoGameAdd_FromDetected_UnknownGameWithExplicitSource(t *testing.T) {
	svc := setupGameAddTest(t)
	svc.RegisterSource(&mockGameAddSource{id: "acme-manual", name: "Acme Manual"})
	install := fakeSteamGame(t, uncuratedAppID, uncuratedName, uncuratedDir)
	gameAddFromDetected, gameAddSource, gameAddID = uncuratedAppID, "acme-manual", uncuratedSlug

	cmd, _ := newGameAddCmd()
	require.NoError(t, doGameAdd(context.Background(), cmd, bufio.NewReader(poisonReader{t: t}), svc))

	saved, err := config.LoadGames(configDir)
	require.NoError(t, err)
	require.Contains(t, saved, uncuratedSlug)
	g := saved[uncuratedSlug]
	assert.Equal(t, uncuratedName, g.Name)
	assert.Equal(t, install, g.InstallPath)
	assert.Equal(t, filepath.Join(install, "mods"), g.ModPath)
	assert.Equal(t, map[string]string{"acme-manual": uncuratedSlug}, g.SourceIDs)
}

// TestDoGameAdd_FromDetected_CatalogSearchesByGameName is the suggestion
// path: --source alone on an uncurated game searches that source's catalog
// by the game's OWN name. In a terminal the matches are printed and the
// user picks one, exactly as the --query flow does.
func TestDoGameAdd_FromDetected_CatalogSearchesByGameName(t *testing.T) {
	svc := setupGameAddTest(t)
	svc.RegisterSource(&mockGameAddCatalogSource{
		mockGameAddSource: mockGameAddSource{id: "acme", name: "Acme"},
		entries: []source.GameEntry{
			{ID: "1", Name: "Uncurated Example Game Deluxe", Slug: "uncurated-example-game-deluxe"},
			{ID: "2", Name: "Uncurated Example Game Redux", Slug: "uncurated-example-game-redux"},
		},
	})
	fakeSteamGame(t, uncuratedAppID, uncuratedName, uncuratedDir)
	gameAddFromDetected, gameAddSource = uncuratedAppID, "acme"

	cmd, buf := newGameAddCmd()
	require.NoError(t, doGameAdd(context.Background(), cmd, bufio.NewReader(strings.NewReader("1\n")), svc))

	out := buf.String()
	assert.Contains(t, out, "Found 2 game(s):")
	assert.Contains(t, out, "Uncurated Example Game Deluxe")

	saved, err := config.LoadGames(configDir)
	require.NoError(t, err)
	require.Contains(t, saved, uncuratedSlug)
	assert.Equal(t, map[string]string{"acme": "1"}, saved[uncuratedSlug].SourceIDs)
}

// TestDoGameAdd_FromDetected_CatalogSearchIsTheDocumentUnderJSON: with no
// --pick there is nothing to choose with under --json (Ruling 2 forbids
// the prompt), so the catalog report IS the answer - search first, add
// second - and nothing is written.
func TestDoGameAdd_FromDetected_CatalogSearchIsTheDocumentUnderJSON(t *testing.T) {
	svc := setupGameAddTest(t)
	svc.RegisterSource(&mockGameAddCatalogSource{
		mockGameAddSource: mockGameAddSource{id: "acme", name: "Acme"},
		entries: []source.GameEntry{
			{ID: "1", Name: "Uncurated Example Game Deluxe", Slug: "uncurated-example-game-deluxe"},
			{ID: "2", Name: "Uncurated Example Game Redux", Slug: "uncurated-example-game-redux"},
		},
	})
	fakeSteamGame(t, uncuratedAppID, uncuratedName, uncuratedDir)
	gameAddFromDetected, gameAddSource = uncuratedAppID, "acme"
	withJSONOutput(t)

	cmd, _ := newGameAddCmd()
	out := runJSONCommand(t, func() error {
		return doGameAdd(context.Background(), cmd, bufio.NewReader(poisonReader{t: t}), svc)
	})

	var report core.GameCatalogReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	assert.Equal(t, uncuratedName, report.Query, "the search term is the detected game's own name")
	assert.Len(t, report.Matches, 2)

	saved, err := config.LoadGames(configDir)
	require.NoError(t, err)
	assert.Empty(t, saved, "a search adds nothing; --pick chooses")
}

// TestDoGameAdd_FromDetected_CatalogExactNameIsTakenAutomatically pins the
// one case the suggestion resolves itself - an entry whose name IS the
// game's name - and, in the same fixture, that a merely-similar sibling
// does not make it ambiguous.
func TestDoGameAdd_FromDetected_CatalogExactNameIsTakenAutomatically(t *testing.T) {
	svc := setupGameAddTest(t)
	svc.RegisterSource(&mockGameAddCatalogSource{
		mockGameAddSource: mockGameAddSource{id: "acme", name: "Acme"},
		entries: []source.GameEntry{
			{ID: "77", Name: uncuratedName, Slug: uncuratedSlug},
			{ID: "78", Name: "Uncurated Example Game Redux", Slug: "uncurated-example-game-redux"},
		},
	})
	fakeSteamGame(t, uncuratedAppID, uncuratedName, uncuratedDir)
	gameAddFromDetected, gameAddSource = uncuratedAppID, "acme"

	cmd, _ := newGameAddCmd()
	require.NoError(t, doGameAdd(context.Background(), cmd, bufio.NewReader(poisonReader{t: t}), svc))

	saved, err := config.LoadGames(configDir)
	require.NoError(t, err)
	require.Contains(t, saved, uncuratedSlug, "the detected game's own slug outranks the catalog entry's")
	assert.Equal(t, map[string]string{"acme": "77"}, saved[uncuratedSlug].SourceIDs)
}

// TestDoGameAdd_FromDetected_PickChoosesAmongMatches: --pick resolves the
// search without a prompt, so the whole flow stays non-interactive.
func TestDoGameAdd_FromDetected_PickChoosesAmongMatches(t *testing.T) {
	svc := setupGameAddTest(t)
	svc.RegisterSource(&mockGameAddCatalogSource{
		mockGameAddSource: mockGameAddSource{id: "acme", name: "Acme"},
		entries: []source.GameEntry{
			{ID: "1", Name: "Uncurated Example Game Deluxe", Slug: "uncurated-example-game-deluxe"},
			{ID: "2", Name: "Uncurated Example Game Redux", Slug: "uncurated-example-game-redux"},
		},
	})
	fakeSteamGame(t, uncuratedAppID, uncuratedName, uncuratedDir)
	gameAddFromDetected, gameAddSource, gameAddPick = uncuratedAppID, "acme", 2

	cmd, _ := newGameAddCmd()
	require.NoError(t, doGameAdd(context.Background(), cmd, bufio.NewReader(poisonReader{t: t}), svc))

	saved, err := config.LoadGames(configDir)
	require.NoError(t, err)
	require.Contains(t, saved, uncuratedSlug)
	assert.Equal(t, map[string]string{"acme": "2"}, saved[uncuratedSlug].SourceIDs)
}

// TestDoGameAdd_FromDetected_OverridesWin: every existing flag still beats
// the prefill, field by field.
func TestDoGameAdd_FromDetected_OverridesWin(t *testing.T) {
	svc := setupGameAddTest(t)
	svc.RegisterSource(&mockGameAddSource{id: "acme-manual", name: "Acme Manual"})
	install := fakeSteamGame(t, uncuratedAppID, uncuratedName, uncuratedDir)
	otherInstall, mods := t.TempDir(), t.TempDir()
	gameAddFromDetected, gameAddSource, gameAddID = uncuratedAppID, "acme-manual", "sf"
	gameAddName, gameAddGameID = "My Renamed Game", "renamed-game"
	gameAddPath, gameAddModPath = otherInstall, mods

	cmd, _ := newGameAddCmd()
	require.NoError(t, doGameAdd(context.Background(), cmd, bufio.NewReader(poisonReader{t: t}), svc))

	saved, err := config.LoadGames(configDir)
	require.NoError(t, err)
	require.Contains(t, saved, "renamed-game")
	g := saved["renamed-game"]
	assert.Equal(t, "My Renamed Game", g.Name)
	assert.Equal(t, otherInstall, g.InstallPath)
	assert.NotEqual(t, install, g.InstallPath)
	assert.Equal(t, mods, g.ModPath)
}

// TestDoGameAdd_FromDetected_UnknownAppID names the offending input, so a
// web form marks the field and a shell user sees the app id they typed.
func TestDoGameAdd_FromDetected_UnknownAppID(t *testing.T) {
	svc := setupGameAddTest(t)
	svc.RegisterSource(&mockGameAddSource{id: "acme-manual", name: "Acme Manual"})
	fakeSteamGame(t, uncuratedAppID, uncuratedName, uncuratedDir)
	gameAddFromDetected = "999999"

	cmd, _ := newGameAddCmd()
	err := doGameAdd(context.Background(), cmd, bufio.NewReader(poisonReader{t: t}), svc)

	require.Error(t, err)
	var specErr *core.GameSpecError
	require.ErrorAs(t, err, &specErr)
	assert.Equal(t, "from_steam_app_id", specErr.Field)
	assert.Equal(t, "999999", specErr.Value)
}

// TestDoGameAdd_FromDetected_IDWithoutSourceOnCuratedGameIsRefused pins
// Important 3 from the #206 review: a curated candidate's source map is
// taken as-is with no source named, so --id/--query/--pick were silently
// dropped with exit 0 - the worst shape for a value the user explicitly
// typed. poisonReader proves the refusal never falls back to a prompt.
func TestDoGameAdd_FromDetected_IDWithoutSourceOnCuratedGameIsRefused(t *testing.T) {
	svc := setupGameAddTest(t)
	svc.RegisterSource(nexusmods.New(nil, ""))
	fakeSteamGame(t, "489830", "Skyrim Special Edition", "Skyrim Special Edition")
	gameAddFromDetected, gameAddID = "489830", "TOTALLY-BOGUS"

	cmd, _ := newGameAddCmd()
	err := doGameAdd(context.Background(), cmd, bufio.NewReader(poisonReader{t: t}), svc)

	require.Error(t, err)
	var specErr *core.GameSpecError
	require.ErrorAs(t, err, &specErr)
	assert.Equal(t, "source_id", specErr.Field)

	saved, loadErr := config.LoadGames(configDir)
	require.NoError(t, loadErr)
	assert.Empty(t, saved, "nothing is written when the flag is refused")
}

// TestDoGameAdd_FromDetected_JSONEmitsTheGameDocument keeps the flag inside
// Ruling 15: the add's one document on stdout, nothing else.
func TestDoGameAdd_FromDetected_JSONEmitsTheGameDocument(t *testing.T) {
	svc := setupGameAddTest(t)
	svc.RegisterSource(nexusmods.New(nil, ""))
	fakeSteamGame(t, "489830", "Skyrim Special Edition", "Skyrim Special Edition")
	gameAddFromDetected = "489830"
	withJSONOutput(t)

	cmd, buf := newGameAddCmd()
	out := runJSONCommand(t, func() error {
		return doGameAdd(context.Background(), cmd, bufio.NewReader(poisonReader{t: t}), svc)
	})

	assert.Empty(t, buf.String(), "no console text may sit beside the document")
	var entry core.GameListEntry
	require.NoError(t, json.Unmarshal([]byte(out), &entry))
	assert.Equal(t, "skyrim-se", entry.ID)
}

// TestDoGameAdd_ManualPath_EmptyIdentifierIsAcceptedForACatalogLessSource is
// #387's own reproduction: a directory source has no identifier to give -
// the README says "directory sources ignore this value", and
// `lmm game edit --source localmods=` writes it empty - but `game add`
// answered a bare Enter with "Error: id is required" and abandoned the
// whole add. With a --game-id to key the entry, an empty identifier is a
// legitimate mapping.
//
// The source declares that itself (P1b review F5); a catalogue-less source
// whose mapped value is a real game slug does not, and is covered below.
func TestDoGameAdd_ManualPath_EmptyIdentifierIsAcceptedForACatalogLessSource(t *testing.T) {
	svc := setupGameAddTest(t)
	svc.RegisterSource(&mockIdentifierIgnoringSource{mockGameAddSource{id: "localmods", name: "Local Mods"}})
	gameAddGameID = "testgame"

	installDir := t.TempDir()
	input := strings.Join([]string{
		"1",         // select localmods (only registered source)
		"Test Game", // game name (display)
		"",          // no identifier: this source has none
		installDir,  // install path (must exist)
		"",          // accept default mod path
	}, "\n") + "\n"

	cmd, buf := newGameAddCmd()
	err := doGameAdd(context.Background(), cmd, bufio.NewReader(strings.NewReader(input)), svc)
	require.NoError(t, err, "output so far:\n%s", buf.String())

	games, err := config.LoadGames(configDir)
	require.NoError(t, err)
	game, ok := games["testgame"]
	require.True(t, ok, "expected a game keyed testgame; got %v", games)
	assert.Equal(t, map[string]string{"localmods": ""}, game.SourceIDs,
		"the mapping is written empty, exactly as `game edit --source localmods=` writes it")
}

// TestDoGameAdd_ManualPath_ASourceThatNeedsAnIdentifierIsNotOfferedAnEmptyOne
// is P1b review F5. #387 made the identifier optional for every
// catalogue-less source, and the prompt said so - "NexusMods identifier
// (Enter if it has none): " - but NexusMods and Steam Workshop are
// catalogue-less with a real, REQUIRED game slug/appid. Pressing Enter wrote
// `nexusmods: ""`, a mapping that fails at first use and that `game add`
// refused before. Whether an empty value is legitimate is the source's own
// answer, not a consequence of having no catalogue.
func TestDoGameAdd_ManualPath_ASourceThatNeedsAnIdentifierIsNotOfferedAnEmptyOne(t *testing.T) {
	svc := setupGameAddTest(t)
	svc.RegisterSource(&mockGameAddSource{id: "nexusmods", name: "NexusMods"})
	gameAddGameID = "testgame"

	input := strings.Join([]string{
		"1",         // select nexusmods (only registered source)
		"Test Game", // game name (display)
		"",          // a bare Enter: the identifier this source needs
	}, "\n") + "\n"

	cmd, buf := newGameAddCmd()
	err := doGameAdd(context.Background(), cmd, bufio.NewReader(strings.NewReader(input)), svc)

	require.Error(t, err, "output so far:\n%s", buf.String())
	assert.Contains(t, err.Error(), "id is required")
	assert.NotContains(t, buf.String(), "Enter if it has none",
		"the prompt must not invite an empty value this source cannot use")

	games, loadErr := config.LoadGames(configDir)
	require.NoError(t, loadErr)
	assert.Empty(t, games, "nothing is written when the identifier is refused")
}
