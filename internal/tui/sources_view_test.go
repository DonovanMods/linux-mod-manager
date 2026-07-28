package tui

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/source/curseforge"
	"github.com/DonovanMods/linux-mod-manager/internal/source/custom"
	"github.com/DonovanMods/linux-mod-manager/internal/source/nexusmods"
)

// TestSourcesScreenRegistered proves ScreenSources is wired into the
// standard navigation surface: it appears in screens, has a real display
// name, screenAt round-trips it, and "5" navigates to it from the
// dashboard (mirroring TestNumberKeysNavigateScreens in app_test.go).
func TestSourcesScreenRegistered(t *testing.T) {
	t.Parallel()

	require.Contains(t, screens, ScreenSources)
	require.NotContains(t, ScreenSources.String(), "Screen(")
	require.Equal(t, ScreenSources, screenAt(screensIndexOf(t, ScreenSources)))

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)

	updated := updateWithRunes(t, model, "5")
	require.Equal(t, ScreenSources, updated.CurrentScreen())
}

// screensIndexOf finds screen's index in the screens slice, failing the test
// if it isn't registered.
func screensIndexOf(t *testing.T, screen Screen) int {
	t.Helper()
	for i, s := range screens {
		if s == screen {
			return i
		}
	}
	t.Fatalf("%s not found in screens", screen)
	return -1
}

func TestSourceInfosPrototype(t *testing.T) {
	t.Parallel()

	p := NewPrototypeProvider()
	infos := p.SourceInfos(true)
	require.NotEmpty(t, infos)
	for _, si := range infos {
		assert.NotEmpty(t, si.ID)
		assert.NotEmpty(t, si.Type)
	}
}

// TestSourceInfosPrototype_ScopedIsSubsetOfAll pins Task 4's default-view
// contract for the canned demo data: SourceInfos(false) (the Sources
// screen's default) must be a strict, non-empty subset of SourceInfos(true)
// (the 'a'-toggled full registry) - never a disjoint or larger set - and its
// rows must agree exactly (all fields) with their SourceInfos(true)
// counterpart, matching Model.sourcesShowAll's "the two never disagree"
// contract.
func TestSourceInfosPrototype_ScopedIsSubsetOfAll(t *testing.T) {
	t.Parallel()

	p := NewPrototypeProvider()
	scoped := p.SourceInfos(false)
	all := p.SourceInfos(true)

	require.NotEmpty(t, scoped)
	require.Less(t, len(scoped), len(all), "the canned data must have at least one source NOT configured for the active game, or this test can't tell scoped from all")

	byID := make(map[string]SourceInfo, len(all))
	for _, si := range all {
		byID[si.ID] = si
	}
	for _, si := range scoped {
		full, ok := byID[si.ID]
		require.True(t, ok, "every scoped row must also appear in the full registry: %+v", si)
		assert.True(t, full.InUse, "a scoped row's SourceInfos(true) counterpart must be marked InUse: %+v", full)
		assert.Equal(t, full.Name, si.Name)
		assert.Equal(t, full.Type, si.Type)
	}
}

func TestSourcesViewRenders(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)

	model = updateWithRunes(t, model, "5")
	require.Equal(t, ScreenSources, model.CurrentScreen())

	loaded, _ := model.Update(model.Init()())
	model = loaded.(Model)

	view := model.screenView()
	require.Contains(t, view, "ID")
	require.Contains(t, view, "TYPE")
	require.Contains(t, view, "AUTH")
	require.Contains(t, view, "CAPABILITIES")

	// The DEFAULT view is game-scoped (Task 4, #75): only SourceInfos(false)'s
	// rows are guaranteed present - SourceInfos(true)'s extra (not-in-use)
	// entries are asserted absent below, in TestSourcesScreenTogglesScope.
	for _, si := range NewPrototypeProvider().SourceInfos(false) {
		assert.Contains(t, view, si.ID)
		assert.Contains(t, view, si.Type)
	}
}

// TestSourcesScreenTogglesScope is Task 4's RED-first pin for the 'a'
// keybinding (#75): default view is scoped (a not-in-use source's ID is
// absent, no IN USE column at all); pressing 'a' switches to the
// full-registry view (every source's ID present, IN USE column appears,
// selection resets to the top row); pressing 'a' again returns to the
// scoped view.
func TestSourcesScreenTogglesScope(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)
	model = updateWithRunes(t, model, "5")
	require.Equal(t, ScreenSources, model.CurrentScreen())
	loaded, _ := model.Update(model.Init()())
	model = loaded.(Model)

	all := NewPrototypeProvider().SourceInfos(true)
	scoped := NewPrototypeProvider().SourceInfos(false)
	scopedIDs := make(map[string]bool, len(scoped))
	for _, si := range scoped {
		scopedIDs[si.ID] = true
	}
	var notInUseID string
	for _, si := range all {
		if !scopedIDs[si.ID] {
			notInUseID = si.ID
			break
		}
	}
	require.NotEmpty(t, notInUseID, "canned data must have at least one source not configured for the active game")

	require.False(t, model.sourcesShowAll)
	defaultView := model.screenView()
	assert.NotContains(t, defaultView, notInUseID, "the scoped default view must not show a source that isn't configured for the active game")
	assert.NotContains(t, defaultView, "IN USE", "the scoped default view carries no IN USE column")

	// Move selection off row 0 before toggling, so the reset-to-0 half of
	// the contract is actually exercised.
	if len(scoped) > 1 {
		model = updateWithRunes(t, model, "j")
		require.Equal(t, 1, model.SelectedIndex(ScreenSources))
	}

	toggled := updateWithRunes(t, model, "a")
	require.True(t, toggled.sourcesShowAll)
	require.Equal(t, 0, toggled.SelectedIndex(ScreenSources), "toggling must reset the selection")
	allView := toggled.screenView()
	assert.Contains(t, allView, notInUseID, "the all-sources view must show every registered source")
	assert.Contains(t, allView, "IN USE")

	backToScoped := updateWithRunes(t, toggled, "a")
	require.False(t, backToScoped.sourcesShowAll)
	backView := backToScoped.screenView()
	assert.NotContains(t, backView, notInUseID, "toggling again must return to the scoped view")
	assert.NotContains(t, backView, "IN USE")
}

// TestSourcesScreenTitleIndicatesScope pins design §5's "screen title or
// header indicates scope" requirement.
func TestSourcesScreenTitleIndicatesScope(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)
	model = updateWithRunes(t, model, "5")
	loaded, _ := model.Update(model.Init()())
	model = loaded.(Model)

	scopedView := model.screenView()
	assert.Contains(t, scopedView, model.summary.GameName, "the scoped default title must name the active game")

	toggled := updateWithRunes(t, model, "a")
	allView := toggled.screenView()
	assert.Contains(t, allView, "ALL", "the full-registry title must indicate the all-sources scope")
}

// TestToggleAllSources_NoOpOffSourcesScreen proves the 'a' binding is
// scoped to ScreenSources, mirroring ToggleEnable/Files/Policy's own
// screen-guarded pattern (mutations.go) - it must not fire (or be
// misinterpreted as a typed character/other action) on another screen.
func TestToggleAllSources_NoOpOffSourcesScreen(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)
	require.Equal(t, ScreenDashboard, model.CurrentScreen())
	loaded, _ := model.Update(model.Init()())
	model = loaded.(Model)

	before := model.sources
	updated := updateWithRunes(t, model, "a")
	require.Equal(t, ScreenDashboard, updated.CurrentScreen())
	assert.False(t, updated.sourcesShowAll)
	assert.Equal(t, before, updated.sources)
}

// builtinStubSource is a minimal source.ModSource with no CapabilityReporter,
// IsAuthenticated, or TypeLabeler method, exercising coreProvider.SourceInfos'
// defaults: full capabilities (CapabilitiesOf's built-in fallback), Auth
// "yes" (authState's fallback for a capable source with no IsAuthenticated
// probe), and Type "unknown" (source.TypeLabelOf's fallback for a source
// implementing no TypeLabeler). The name predates Task 4: this double used to
// fall through the old concrete-type switch (customSourceType) to its
// "built-in" default - exactly the mislabeling Task 4 replaced with an
// explicit "unknown" for exactly this case. Kept as-is (not renamed) since
// another file's comment references it by this name.
type builtinStubSource struct{ id string }

func (s *builtinStubSource) ID() string      { return s.id }
func (s *builtinStubSource) Name() string    { return "Built-in Stub" }
func (s *builtinStubSource) AuthURL() string { return "" }
func (s *builtinStubSource) ExchangeToken(context.Context, string) (*source.Token, error) {
	return nil, errors.New("not implemented")
}
func (s *builtinStubSource) Search(context.Context, source.SearchQuery) (source.SearchResult, error) {
	return source.SearchResult{}, nil
}
func (s *builtinStubSource) GetMod(context.Context, string, string) (*domain.Mod, error) {
	return nil, errors.New("not implemented")
}
func (s *builtinStubSource) GetDependencies(context.Context, *domain.Mod) ([]domain.ModReference, error) {
	return nil, errors.New("not implemented")
}
func (s *builtinStubSource) GetModFiles(context.Context, *domain.Mod) ([]domain.DownloadableFile, error) {
	return nil, errors.New("not implemented")
}
func (s *builtinStubSource) GetDownloadURL(context.Context, *domain.Mod, string) (string, error) {
	return "", errors.New("not implemented")
}
func (s *builtinStubSource) CheckUpdates(context.Context, []domain.InstalledMod) ([]domain.Update, error) {
	return nil, errors.New("not implemented")
}

func TestCoreProviderSourceInfos(t *testing.T) {
	t.Parallel()

	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	dir, err := custom.NewDirectory(custom.SourceDefinition{
		ID:        "zzz-directory",
		Name:      "Local Mods",
		Type:      custom.TypeDirectory,
		Directory: &custom.DirectoryConfig{Path: t.TempDir()},
	})
	require.NoError(t, err)
	svc.RegisterSource(dir)
	svc.RegisterSource(&builtinStubSource{id: "aaa-builtin"})

	game := &domain.Game{ID: "test-game", Name: "Test Game", InstallPath: t.TempDir(), ModPath: t.TempDir()}
	require.NoError(t, svc.AddGame(game))
	pm := svc.NewProfileManager()
	_, err = pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "default"))

	provider := NewCoreProvider(svc, game, "default")
	// true (the full-registry view): game has no SourceIDs configured, so
	// the scoped default (false) would report zero rows - this test is
	// about the TypeLabeler/auth-fallback rendering, not scoping itself.
	infos := provider.SourceInfos(true)
	require.Len(t, infos, 2)

	// Sorted by ID: "aaa-builtin" before "zzz-directory".
	require.Equal(t, "aaa-builtin", infos[0].ID)
	require.Equal(t, "unknown", infos[0].Type, "builtinStubSource implements no TypeLabeler; source.TypeLabelOf's fallback is \"unknown\", not the old switch's \"built-in\" default")
	require.Equal(t, "yes", infos[0].Auth, "builtinStubSource has no IsAuthenticated method; authState's fallback for a capable source is \"yes\"")
	require.Equal(t, "zzz-directory", infos[1].ID)
	require.Equal(t, "directory", infos[1].Type)
	require.Equal(t, "n/a", infos[1].Auth, "directory sources report no auth capability")
}

// TestCoreProviderSourceInfos_ScopedToGame is Task 4's RED-first pin for
// coreProvider.SourceInfos' scoping contract (#75): SourceInfos(false)
// (the Sources screen's default) must return exactly the active game's
// configured+registered sources - "zzz-directory" only, NOT "aaa-builtin"
// (registered, but never added to game.SourceIDs) - while SourceInfos(true)
// still returns the full registry, with InUse marking the same subset.
func TestCoreProviderSourceInfos_ScopedToGame(t *testing.T) {
	t.Parallel()

	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	dir, err := custom.NewDirectory(custom.SourceDefinition{
		ID:        "zzz-directory",
		Name:      "Local Mods",
		Type:      custom.TypeDirectory,
		Directory: &custom.DirectoryConfig{Path: t.TempDir()},
	})
	require.NoError(t, err)
	svc.RegisterSource(dir)
	svc.RegisterSource(&builtinStubSource{id: "aaa-builtin"})

	game := &domain.Game{
		ID: "test-game", Name: "Test Game", InstallPath: t.TempDir(), ModPath: t.TempDir(),
		SourceIDs: map[string]string{"zzz-directory": ""},
	}
	require.NoError(t, svc.AddGame(game))
	pm := svc.NewProfileManager()
	_, err = pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "default"))

	provider := NewCoreProvider(svc, game, "default")

	scoped := provider.SourceInfos(false)
	require.Len(t, scoped, 1, "scoped view must include only the game's configured sources: %+v", scoped)
	assert.Equal(t, "zzz-directory", scoped[0].ID)
	assert.False(t, scoped[0].InUse, "scoped rows leave InUse at its zero value - see SourceInfo's own doc comment")

	all := provider.SourceInfos(true)
	require.Len(t, all, 2, "the full-registry view must include every registered source regardless of scoping: %+v", all)
	byID := make(map[string]SourceInfo, len(all))
	for _, si := range all {
		byID[si.ID] = si
	}
	assert.True(t, byID["zzz-directory"].InUse)
	assert.False(t, byID["aaa-builtin"].InUse, "aaa-builtin is registered but not in game.SourceIDs")
}

// TestCoreProviderSourceInfos_TypeLabelerTable is Task 4's RED-first pin for
// coreProvider.SourceInfos' Type field: one instance of every real
// source.ModSource type (both custom.* concrete types and both built-ins)
// must report its own TypeLabel() rather than being classified by a
// hand-synced concrete-type switch (the deleted customSourceType), plus a
// bare double with no TypeLabeler (builtinStubSource) falls back to
// "unknown" — mirrors cmd/lmm/source_test.go's
// TestSourceTypeLabel_AllRealTypesPlusBareMock for the TUI's own consumer.
func TestCoreProviderSourceInfos_TypeLabelerTable(t *testing.T) {
	t.Parallel()

	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	dir, err := custom.NewDirectory(custom.SourceDefinition{
		ID:        "z-directory",
		Name:      "Dir",
		Type:      custom.TypeDirectory,
		Directory: &custom.DirectoryConfig{Path: t.TempDir()},
	})
	require.NoError(t, err)
	man, err := custom.NewManifest(custom.SourceDefinition{
		ID:       "y-manifest",
		Name:     "Man",
		Type:     custom.TypeManifest,
		Manifest: &custom.ManifestConfig{URL: filepath.Join(t.TempDir(), "manifest.yaml")},
	})
	require.NoError(t, err)
	api, err := custom.NewAPI(custom.SourceDefinition{
		ID:   "x-api",
		Name: "API",
		Type: custom.TypeAPI,
		API:  &custom.APIConfig{BaseURL: "https://api.x.test"},
	})
	require.NoError(t, err)

	svc.RegisterSource(dir)
	svc.RegisterSource(man)
	svc.RegisterSource(api)
	svc.RegisterSource(nexusmods.New(nil, ""))
	svc.RegisterSource(curseforge.New(nil, ""))
	svc.RegisterSource(&builtinStubSource{id: "w-bare"})

	game := &domain.Game{ID: "test-game", Name: "Test Game", InstallPath: t.TempDir(), ModPath: t.TempDir()}
	require.NoError(t, svc.AddGame(game))
	pm := svc.NewProfileManager()
	_, err = pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "default"))

	provider := NewCoreProvider(svc, game, "default")
	// true: same "game has no SourceIDs configured" reasoning as
	// TestCoreProviderSourceInfos above - this test is about the
	// TypeLabeler table, not scoping.
	infos := provider.SourceInfos(true)

	byID := make(map[string]string, len(infos))
	for _, i := range infos {
		byID[i.ID] = i.Type
	}
	assert.Equal(t, "directory", byID["z-directory"])
	assert.Equal(t, "manifest", byID["y-manifest"])
	assert.Equal(t, "api", byID["x-api"])
	assert.Equal(t, "built-in", byID["nexusmods"])
	assert.Equal(t, "built-in", byID["curseforge"])
	assert.Equal(t, "unknown", byID["w-bare"])
}

// longSourcesProvider returns sources with long IDs and capabilities to test
// truncation in narrow terminals.
type longSourcesProvider struct{}

func (longSourcesProvider) Overview(context.Context) (Summary, []ModItem, error) {
	return Summary{}, nil, nil
}
func (longSourcesProvider) Profiles(context.Context) ([]ProfileItem, error) { return nil, nil }
func (longSourcesProvider) Sources() []string                               { return nil }
func (longSourcesProvider) SourceInfos(bool) []SourceInfo {
	return []SourceInfo{
		{
			ID:           "extremely-long-custom-source-identifier-that-exceeds-normal-widths",
			Name:         "Long Source",
			Type:         "custom",
			Auth:         "yes",
			Capabilities: "search,deps,updates,auth,conflict-detection,manifest-verification,auto-dependencies",
			InUse:        true,
		},
		{
			ID:           "another-overly-verbose-identifier-for-testing-purposes-in-narrow-terminals",
			Name:         "Another Long",
			Type:         "built-in",
			Auth:         "no",
			Capabilities: "search,updates,manifest-fetching,advanced-filtering,batch-operations",
		},
	}
}
func (longSourcesProvider) Search(context.Context, string, string, int, int) (SearchPage, error) {
	return SearchPage{}, nil
}
func (longSourcesProvider) DeployedFiles(string, string) ([]string, error)    { return nil, nil }
func (longSourcesProvider) ListGames() ([]GameInfo, error)                    { return nil, nil }
func (longSourcesProvider) Conflicts(context.Context) ([]ConflictItem, error) { return nil, nil }

// TestSourcesViewFitsPanelWidthNarrowTerminal guards that sourcesView rows
// truncate to the panel's content width (not the full terminal width) to
// prevent overlong source IDs or capability lists from re-wrapping inside
// the panel and growing the view past its fixed height budget. This mirrors
// the fix applied to searchView's zero-results warning in commit 2c075e3.
func TestSourcesViewFitsPanelWidthNarrowTerminal(t *testing.T) {
	t.Parallel()

	model, err := NewModel(Options{Theme: "wizardry", Provider: longSourcesProvider{}})
	require.NoError(t, err)

	loaded, _ := model.Update(model.Init()())
	updated, _ := loaded.Update(tea.WindowSizeMsg{Width: 40, Height: 12})
	model = updated.(Model)

	model = updateWithRunes(t, model, "5") // jump to sources screen
	require.Equal(t, ScreenSources, model.CurrentScreen())

	view := model.screenView()
	require.Equal(t, model.availableContentHeight(), lipgloss.Height(view),
		"an overlong source ID or capability list must not wrap and grow the sources panel past its height budget")
	for _, line := range strings.Split(view, "\n") {
		require.LessOrEqual(t, lipgloss.Width(line), model.availableWidth(), "no rendered line exceeds terminal width")
	}
}
