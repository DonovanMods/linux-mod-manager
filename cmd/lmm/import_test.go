package main

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestImportCommand_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	// This is a placeholder for manual testing
	// Full integration tests would require:
	// 1. Setting up a test config directory
	// 2. Creating a test game config
	// 3. Running the import command
	// 4. Verifying files are deployed

	t.Log("Import command integration test - run manually with: ./lmm import testmod.zip -g testgame")
}

func TestCreateTestArchive_Helper(t *testing.T) {
	// Verify the test helper works correctly
	tempDir := t.TempDir()
	archivePath := tempDir + "/test.zip"

	createTestArchive(t, archivePath, map[string]string{
		"file1.txt":     "content1",
		"dir/file2.txt": "content2",
	})

	// Verify archive was created
	info, err := os.Stat(archivePath)
	require.NoError(t, err)
	require.True(t, info.Size() > 0, "archive should not be empty")
}

// fakeMatchSource is a minimal source.ModSource test double for import's
// scan-matching and --id default resolution tests. Duplicated rather than
// shared with install_test.go's fakeInstallSource because this task's scope
// (deploy.go/import.go and their own tests only) forbids touching
// install_test.go, and because these tests need an explicit
// CapabilityReporter (to drive the "no searchable sources" case) that
// fakeInstallSource doesn't provide.
type fakeMatchSource struct {
	id         string
	caps       source.Capabilities
	searchMods []domain.Mod // returned verbatim by Search, regardless of query
	searchErr  error
	mods       map[string]*domain.Mod
}

func newFakeMatchSource(id string) *fakeMatchSource {
	return &fakeMatchSource{
		id:   id,
		caps: source.Capabilities{Search: true, Dependencies: true, Updates: true, Auth: true},
		mods: make(map[string]*domain.Mod),
	}
}

func (s *fakeMatchSource) ID() string                        { return s.id }
func (s *fakeMatchSource) Name() string                      { return s.id }
func (s *fakeMatchSource) AuthURL() string                   { return "" }
func (s *fakeMatchSource) Capabilities() source.Capabilities { return s.caps }
func (s *fakeMatchSource) ExchangeToken(ctx context.Context, code string) (*source.Token, error) {
	return nil, nil
}
func (s *fakeMatchSource) Search(ctx context.Context, query source.SearchQuery) (source.SearchResult, error) {
	if s.searchErr != nil {
		return source.SearchResult{}, s.searchErr
	}
	return source.SearchResult{Mods: s.searchMods, TotalCount: len(s.searchMods)}, nil
}
func (s *fakeMatchSource) GetMod(ctx context.Context, gameID, modID string) (*domain.Mod, error) {
	if mod, ok := s.mods[modID]; ok {
		return mod, nil
	}
	return nil, domain.ErrModNotFound
}
func (s *fakeMatchSource) GetDependencies(ctx context.Context, mod *domain.Mod) ([]domain.ModReference, error) {
	return nil, nil
}
func (s *fakeMatchSource) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	return nil, nil
}
func (s *fakeMatchSource) GetDownloadURL(ctx context.Context, mod *domain.Mod, fileID string) (string, error) {
	return "", nil
}
func (s *fakeMatchSource) CheckUpdates(ctx context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	return nil, nil
}

// --- tryMatchSources (Task 2 of #76's PR2 plan: import scan-matching generalizes) ---
//
// tryMatchCurseForge used to consult CurseForge only. tryMatchSources
// generalizes it to iterate SourcesForGame(game.ID) (already ID-sorted),
// filtered to search-capable sources, and returns the first source whose
// search turns up a result - the same "first non-empty result wins"
// acceptance rule as before, unchanged.

// setupTryMatchSourcesTest builds a *core.Service and registers game with it
// via AddGame - required because tryMatchSources calls
// service.SourcesForGame, which (unlike resolveSource) resolves gameID
// against the service's own internal game registry, not a bare struct.
func setupTryMatchSourcesTest(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()

	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir()}
	require.NoError(t, svc.AddGame(game))

	return svc, game
}

// TestTryMatchSources_NonCurseForgeSourceMatches proves the generalization:
// a source with an arbitrary (non-CurseForge) ID supplies a match.
func TestTryMatchSources_NonCurseForgeSourceMatches(t *testing.T) {
	svc, game := setupTryMatchSourcesTest(t)
	src := newFakeMatchSource("acme-source")
	src.searchMods = []domain.Mod{{ID: "42", SourceID: "acme-source", Name: "Acme Mod"}}
	svc.RegisterSource(src)
	game.SourceIDs = map[string]string{"acme-source": "g1"}

	matched, err := tryMatchSources(context.Background(), svc, game, "Acme")

	require.NoError(t, err)
	require.NotNil(t, matched)
	assert.Equal(t, "acme-source", matched.SourceID)
	assert.Equal(t, "42", matched.ID)
}

// TestTryMatchSources_MultiSourceOrder_CurseforgeBeforeNexusmods pins the
// ID-sorted iteration order design §4.2 calls out explicitly: "curseforge"
// sorts before "nexusmods" alphabetically, so typical two-built-in setups
// keep today's outcome. Registration order is deliberately the opposite of
// alphabetical, to prove the winner is decided by sort order, not
// registration order.
func TestTryMatchSources_MultiSourceOrder_CurseforgeBeforeNexusmods(t *testing.T) {
	svc, game := setupTryMatchSourcesTest(t)
	cf := newFakeMatchSource("curseforge")
	cf.searchMods = []domain.Mod{{ID: "1", SourceID: "curseforge", Name: "CF Match"}}
	nx := newFakeMatchSource("nexusmods")
	nx.searchMods = []domain.Mod{{ID: "2", SourceID: "nexusmods", Name: "NX Match"}}
	svc.RegisterSource(nx)
	svc.RegisterSource(cf)
	game.SourceIDs = map[string]string{"curseforge": "g1", "nexusmods": "g1"}

	matched, err := tryMatchSources(context.Background(), svc, game, "Match")

	require.NoError(t, err)
	require.NotNil(t, matched)
	assert.Equal(t, "curseforge", matched.SourceID, "curseforge sorts before nexusmods alphabetically and must win")
}

// TestTryMatchSources_NoSearchableSources_CleanNoMatch guards the "no
// error" half of the contract: when the game's only configured source
// declares no search capability, tryMatchSources returns a clean no-match,
// not an error - the loop has nothing to try, which is not a failure.
func TestTryMatchSources_NoSearchableSources_CleanNoMatch(t *testing.T) {
	svc, game := setupTryMatchSourcesTest(t)
	src := newFakeMatchSource("no-search")
	src.caps.Search = false
	svc.RegisterSource(src)
	game.SourceIDs = map[string]string{"no-search": "g1"}

	matched, err := tryMatchSources(context.Background(), svc, game, "Anything")

	require.NoError(t, err)
	assert.Nil(t, matched)
}

// TestTryMatchSources_NoConfiguredSources_CleanNoMatch covers the simplest
// no-searchable-sources case: a game with no sources configured at all.
func TestTryMatchSources_NoConfiguredSources_CleanNoMatch(t *testing.T) {
	svc, game := setupTryMatchSourcesTest(t)

	matched, err := tryMatchSources(context.Background(), svc, game, "Anything")

	require.NoError(t, err)
	assert.Nil(t, matched)
}

// --- import --id default resolution (Task 3 of #76's PR2 plan) ---
//
// import --id's "prefer curseforge, else first available" block becomes a
// plain resolveSource(service, game, importSource, false) call - the same
// dynamic sole-source-auto/multi-source-prompt semantics as
// deploy/search/update/mod.

// setupDoImportTest builds a *core.Service plus a game for exercising
// doImport's archive-mode path directly, mirroring setupDoDeployTest/
// setupDoInstallTest's pattern. importForce defaults true so the
// conflict-confirmation prompt (unrelated to source resolution) never
// blocks tests that don't drive stdin themselves.
func setupDoImportTest(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()

	configDir = t.TempDir()
	dataDir = t.TempDir()
	gameDir := t.TempDir()

	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: configDir, DataDir: dataDir, CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	oldProfile, oldSource, oldModID, oldForce, oldDryRun, oldSkipMatch :=
		importProfile, importSource, importModID, importForce, importDryRun, importSkipMatch
	oldVerbose, oldNoColor, oldNoHooks := verbose, noColor, noHooks
	importProfile = ""
	importSource = ""
	importModID = ""
	importForce = true
	importDryRun = false
	importSkipMatch = true
	verbose = false
	noColor = true
	noHooks = false
	t.Cleanup(func() {
		importProfile, importSource, importModID, importForce, importDryRun, importSkipMatch =
			oldProfile, oldSource, oldModID, oldForce, oldDryRun, oldSkipMatch
		verbose, noColor, noHooks = oldVerbose, oldNoColor, oldNoHooks
	})

	return svc, game
}

// TestDoImport_IDDefault_SoleConfiguredSource_AutoResolves guards
// resolveSource's auto-select path reached through --id with no --source:
// exactly one configured source resolves without prompting.
func TestDoImport_IDDefault_SoleConfiguredSource_AutoResolves(t *testing.T) {
	svc, game := setupDoImportTest(t)
	src := newFakeMatchSource("acme-source")
	src.mods["999"] = &domain.Mod{ID: "999", SourceID: "acme-source", Name: "Acme Mod", Version: "2.0"}
	svc.RegisterSource(src)
	game.SourceIDs = map[string]string{"acme-source": "g1"}

	archivePath := filepath.Join(t.TempDir(), "mymod.zip")
	createTestArchive(t, archivePath, map[string]string{"mymod.esp": "data"})

	importModID = "999"
	importSource = ""

	out, err := captureStdoutErr(t, func() error {
		return doImport(context.Background(), &cobra.Command{}, svc, game, []string{archivePath})
	})

	require.NoError(t, err)
	assert.Contains(t, out, "Fetching metadata from acme-source...")
	assert.Equal(t, "acme-source", importSource)
}

// TestDoImport_IDDefault_MultipleConfiguredSources_PromptsForSelection
// guards the interactive-prompt path: several configured sources and no
// --source prompts for a selection. getConfiguredSources sorts
// alphabetically ("acme-source" < "beta-source"), so choice "1" picks
// "acme-source".
func TestDoImport_IDDefault_MultipleConfiguredSources_PromptsForSelection(t *testing.T) {
	svc, game := setupDoImportTest(t)
	srcA := newFakeMatchSource("beta-source")
	srcA.mods["7"] = &domain.Mod{ID: "7", SourceID: "beta-source", Name: "Beta Mod", Version: "1.0"}
	srcB := newFakeMatchSource("acme-source")
	srcB.mods["7"] = &domain.Mod{ID: "7", SourceID: "acme-source", Name: "Acme Mod", Version: "1.0"}
	svc.RegisterSource(srcA)
	svc.RegisterSource(srcB)
	game.SourceIDs = map[string]string{"beta-source": "g1", "acme-source": "g1"}

	archivePath := filepath.Join(t.TempDir(), "mymod.zip")
	createTestArchive(t, archivePath, map[string]string{"mymod.esp": "data"})

	importModID = "7"
	importSource = ""

	var out string
	var err error
	withStdin(t, "1\n", func() {
		out, err = captureStdoutErr(t, func() error {
			return doImport(context.Background(), &cobra.Command{}, svc, game, []string{archivePath})
		})
	})

	require.NoError(t, err)
	assert.Contains(t, out, "multiple mod sources configured")
	assert.Contains(t, out, "Fetching metadata from acme-source...")
}

func createTestArchive(t *testing.T, path string, files map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()

	w := zip.NewWriter(f)
	for name, content := range files {
		fw, err := w.Create(name)
		require.NoError(t, err)
		_, err = fw.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
}
