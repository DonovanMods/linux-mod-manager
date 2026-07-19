package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/source/custom"
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
	infos := p.SourceInfos()
	require.NotEmpty(t, infos)
	for _, si := range infos {
		assert.NotEmpty(t, si.ID)
		assert.NotEmpty(t, si.Type)
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

	for _, si := range NewPrototypeProvider().SourceInfos() {
		assert.Contains(t, view, si.ID)
		assert.Contains(t, view, si.Type)
	}
}

// builtinStubSource is a minimal source.ModSource with no CapabilityReporter
// or IsAuthenticated method, exercising coreProvider.SourceInfos' defaults:
// full capabilities (CapabilitiesOf's built-in fallback) and Auth "yes"
// (authState's fallback for a capable source with no IsAuthenticated probe).
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
	infos := provider.SourceInfos()
	require.Len(t, infos, 2)

	// Sorted by ID: "aaa-builtin" before "zzz-directory".
	require.Equal(t, "aaa-builtin", infos[0].ID)
	require.Equal(t, "built-in", infos[0].Type)
	require.Equal(t, "zzz-directory", infos[1].ID)
	require.Equal(t, "directory", infos[1].Type)
	require.Equal(t, "n/a", infos[1].Auth, "directory sources report no auth capability")
}

// longSourcesProvider returns sources with long IDs and capabilities to test
// truncation in narrow terminals.
type longSourcesProvider struct{}

func (longSourcesProvider) Overview(context.Context) (Summary, []ModItem, error) {
	return Summary{}, nil, nil
}
func (longSourcesProvider) Profiles(context.Context) ([]ProfileItem, error) { return nil, nil }
func (longSourcesProvider) Sources() []string                               { return nil }
func (longSourcesProvider) SourceInfos() []SourceInfo {
	return []SourceInfo{
		{
			ID:           "extremely-long-custom-source-identifier-that-exceeds-normal-widths",
			Name:         "Long Source",
			Type:         "custom",
			Auth:         "yes",
			Capabilities: "search,deps,updates,auth,conflict-detection,manifest-verification,auto-dependencies",
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
func (longSourcesProvider) Search(context.Context, string, string, int) (SearchPage, error) {
	return SearchPage{}, nil
}

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
