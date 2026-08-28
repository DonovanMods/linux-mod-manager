package main

import (
	"archive/zip"
	"context"
	"errors"
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
	files      []domain.DownloadableFile // returned verbatim by GetModFiles
	filesErr   error
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
	if s.filesErr != nil {
		return nil, s.filesErr
	}
	return s.files, nil
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
// via SaveGame - required because tryMatchSources calls
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
	require.NoError(t, svc.SaveGame(context.Background(), game))

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

// --- tryMatchSources error semantics (PR #124 Copilot round 1, finding 2) ---
//
// A per-source search error used to survive the whole loop unconditionally:
// if source A errored and source B then responded cleanly with zero
// results, the stale error from A still came back to the caller, which
// contradicts tryMatchSources' own doc comment ("a clean no-match, not an
// error") - a real "no match" is not a failure just because an earlier
// source happened to fail first. The fix: an error is only reported when
// EVERY searchable source failed; any source that responds at all (even
// with zero results) proves the round was not a wash.

// TestTryMatchSources_FirstErrorsSecondEmpty_CleanNoMatchNotError is the
// exact scenario Copilot flagged: source A errors, source B searches
// successfully but finds nothing. The overall result must be a clean
// no-match (nil, nil), not A's stale error.
func TestTryMatchSources_FirstErrorsSecondEmpty_CleanNoMatchNotError(t *testing.T) {
	svc, game := setupTryMatchSourcesTest(t)
	failing := newFakeMatchSource("acme-fail")
	failing.searchErr = errors.New("boom")
	empty := newFakeMatchSource("beta-empty") // no searchMods set: succeeds with zero results
	svc.RegisterSource(failing)
	svc.RegisterSource(empty)
	game.SourceIDs = map[string]string{"acme-fail": "g1", "beta-empty": "g1"}

	matched, err := tryMatchSources(context.Background(), svc, game, "Anything")

	require.NoError(t, err, "a later source's clean empty result must clear an earlier source's error")
	assert.Nil(t, matched)
}

// TestTryMatchSources_AllSourcesError_ReturnsError guards the other half:
// when every searchable source fails, the round genuinely produced nothing
// usable and an error must still surface (not silently swallowed into a
// no-match).
func TestTryMatchSources_AllSourcesError_ReturnsError(t *testing.T) {
	svc, game := setupTryMatchSourcesTest(t)
	a := newFakeMatchSource("source-a")
	a.searchErr = errors.New("boom a")
	b := newFakeMatchSource("source-b")
	b.searchErr = errors.New("boom b")
	svc.RegisterSource(a)
	svc.RegisterSource(b)
	game.SourceIDs = map[string]string{"source-a": "g1", "source-b": "g1"}

	matched, err := tryMatchSources(context.Background(), svc, game, "Anything")

	require.Error(t, err)
	assert.Nil(t, matched)
}

// TestTryMatchSources_FirstEmptySecondMatches_ReturnsMatch guards that a
// clean empty result from an earlier source doesn't prevent a later
// source's real match from being found and returned.
func TestTryMatchSources_FirstEmptySecondMatches_ReturnsMatch(t *testing.T) {
	svc, game := setupTryMatchSourcesTest(t)
	empty := newFakeMatchSource("acme-empty")
	matchSrc := newFakeMatchSource("beta-match")
	matchSrc.searchMods = []domain.Mod{{ID: "5", SourceID: "beta-match", Name: "Found It"}}
	svc.RegisterSource(empty)
	svc.RegisterSource(matchSrc)
	game.SourceIDs = map[string]string{"acme-empty": "g1", "beta-match": "g1"}

	matched, err := tryMatchSources(context.Background(), svc, game, "Anything")

	require.NoError(t, err)
	require.NotNil(t, matched)
	assert.Equal(t, "beta-match", matched.SourceID)
}

// --- scan summary sourceTag (PR #124 Copilot round 1, finding 1) ---
//
// internal/core/importer.go's ScanModPath initializes every detected,
// unmatched ScanResult's MatchedSource to domain.SourceLocal ("local"), not
// "" (see importer.go:397/429) - a fact this cmd's generalization away from
// the old `== "curseforge"` check missed: `!= ""` is true for "local" too,
// so an unmatched mod's summary line got a spurious "local #<id>" tag where
// the pre-generalization code (which only ever matched the literal string
// "curseforge") rendered a plain "local" untagged.

// TestRunImportScan_SummaryTag_NoMatch_ShowsPlainLocalNotLocalHash pins the
// no-match summary rendering directly against ScanModPath's real default
// (domain.SourceLocal), with --skip-match set so the assertion is isolated
// from tryMatchSources entirely.
func TestRunImportScan_SummaryTag_NoMatch_ShowsPlainLocalNotLocalHash(t *testing.T) {
	svc, game := setupDoImportTest(t)
	game.DeployMode = domain.DeployCopy
	require.NoError(t, os.WriteFile(filepath.Join(game.ModPath, "MyMod-1-0.zip"), []byte("data"), 0644))

	importSkipMatch = true
	importDryRun = true

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	out, err := captureStdoutErr(t, func() error {
		return runImportScan(cmd, game, svc, "default")
	})

	require.NoError(t, err)
	assert.Contains(t, out, "(local, v", "an unmatched mod must show a plain \"local\" tag")
	assert.NotContains(t, out, "local #", "an unmatched mod must not be tagged with a fake source ID like \"local #<id>\"")
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

// --- #139 item 2: import stamps completion markers with resolved file IDs ---
//
// Import's direct cache writers used to bypass MarkFileComplete entirely, so
// an import-written entry always failed the file-granular cache-first guard
// (Cache.HasFileIDs) once and ate one redundant redownload. The issue's
// premise that "import knows the file ID at write time" doesn't hold - no
// import path ever sees a source file ID - so source-linked imports now
// RESOLVE it: GetModFiles on the already-fetched source mod, exact FileName
// match first (version-match fallback only on the explicit --id path), then
// stamp the manifest-bearing marker and record the FileIDs on the DB row and
// profile ref. Unresolvable/offline/local imports keep today's marker-less
// behavior.

// TestDoImport_ArchiveWithID_ResolvesFileIDAndStampsMarker is the archive-mode
// happy path: --id import resolves the archive to the source's file by exact
// FileName, adopts its version, stamps the completion marker (with the member
// manifest) and records the FileIDs on the DB row and profile ref.
func TestDoImport_ArchiveWithID_ResolvesFileIDAndStampsMarker(t *testing.T) {
	svc, game := setupDoImportTest(t)
	src := newFakeMatchSource("acme-source")
	src.mods["999"] = &domain.Mod{ID: "999", SourceID: "acme-source", Name: "Acme Mod", Version: "2.0", GameID: "g1"}
	src.files = []domain.DownloadableFile{
		{ID: "55", FileName: "mymod.zip", Version: "2.0", IsPrimary: true},
		{ID: "56", FileName: "other.zip", Version: "1.0"},
	}
	svc.RegisterSource(src)
	game.SourceIDs = map[string]string{"acme-source": "g1"}

	archivePath := filepath.Join(t.TempDir(), "mymod.zip")
	createTestArchive(t, archivePath, map[string]string{"mymod.esp": "data"})

	importModID = "999"

	_, err := captureStdoutErr(t, func() error {
		return doImport(context.Background(), &cobra.Command{}, svc, game, []string{archivePath})
	})
	require.NoError(t, err)

	installed, err := svc.GetInstalledMod(context.Background(), "acme-source", "999", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, []string{"55"}, installed.FileIDs,
		"the resolved source file ID must be recorded on the DB row")

	gameCache := svc.GetGameCache(game)
	assert.True(t, gameCache.HasFileIDs("g1", "acme-source", "999", "2.0", []string{"55"}),
		"the import-written cache entry must carry the resolved file's completion marker")

	manifests, err := gameCache.FileManifests("g1", "acme-source", "999", "2.0")
	require.NoError(t, err)
	require.Contains(t, manifests, "55")
	assert.True(t, manifests["55"].Recorded, "the marker must carry a recorded member manifest")
	assert.Equal(t, []string{"mymod.esp"}, manifests["55"].Members)

	prof, err := svc.NewProfileManager().Get("g1", "default")
	require.NoError(t, err)
	require.Len(t, prof.Mods, 1)
	assert.Equal(t, []string{"55"}, prof.Mods[0].FileIDs,
		"the resolved source file ID must be recorded on the profile ref")
}

// TestDoImport_ArchiveWithID_FileListingFails_DegradesToMarkerless pins
// guardrail 1: a failed GetModFiles (offline, source hiccup) must not fail
// the import - it degrades to today's marker-less, empty-FileIDs behavior.
func TestDoImport_ArchiveWithID_FileListingFails_DegradesToMarkerless(t *testing.T) {
	svc, game := setupDoImportTest(t)
	src := newFakeMatchSource("acme-source")
	src.mods["999"] = &domain.Mod{ID: "999", SourceID: "acme-source", Name: "Acme Mod", Version: "2.0", GameID: "g1"}
	src.filesErr = errors.New("source offline")
	svc.RegisterSource(src)
	game.SourceIDs = map[string]string{"acme-source": "g1"}

	archivePath := filepath.Join(t.TempDir(), "mymod.zip")
	createTestArchive(t, archivePath, map[string]string{"mymod.esp": "data"})

	importModID = "999"

	_, err := captureStdoutErr(t, func() error {
		return doImport(context.Background(), &cobra.Command{}, svc, game, []string{archivePath})
	})
	require.NoError(t, err, "a failed source file listing must not fail the import")

	installed, err := svc.GetInstalledMod(context.Background(), "acme-source", "999", "g1", "default")
	require.NoError(t, err)
	assert.Empty(t, installed.FileIDs, "no resolved file means no recorded FileIDs")
}

// TestRunImportScan_MatchedSource_ResolvesFileIDAndStampsMarker is the scan-mode
// twin: a scan import whose source match resolves the file by exact FileName
// stamps the marker on the cache entry it writes (copy mode) and records the
// FileIDs, while keeping the manual-download semantics scan imports always had.
func TestRunImportScan_MatchedSource_ResolvesFileIDAndStampsMarker(t *testing.T) {
	svc, game := setupDoImportTest(t)
	// tryMatchSources resolves sources via SourcesForGame, which consults the
	// service's own game registry - the game must actually be registered.
	require.NoError(t, svc.SaveGame(context.Background(), game))
	game.DeployMode = domain.DeployCopy
	require.NoError(t, os.WriteFile(filepath.Join(game.ModPath, "AcmeMod-1.0.zip"), []byte("payload"), 0644))

	src := newFakeMatchSource("acme-source")
	src.searchMods = []domain.Mod{{ID: "42", SourceID: "acme-source", Name: "AcmeMod", GameID: "g1"}}
	src.files = []domain.DownloadableFile{
		{ID: "77", FileName: "AcmeMod-1.0.zip", Version: "1.0"},
		{ID: "78", FileName: "AcmeMod-2.0.zip", Version: "2.0"},
	}
	svc.RegisterSource(src)
	game.SourceIDs = map[string]string{"acme-source": "g1"}

	importSkipMatch = false

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	_, err := captureStdoutErr(t, func() error {
		return runImportScan(cmd, game, svc, "default")
	})
	require.NoError(t, err)

	installed, err := svc.GetInstalledMod(context.Background(), "acme-source", "42", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, []string{"77"}, installed.FileIDs,
		"the resolved source file ID must be recorded on the DB row")
	assert.True(t, installed.ManualDownload,
		"scan imports keep their manual-download semantics regardless of file resolution")

	assert.True(t, svc.GetGameCache(game).HasFileIDs("g1", "acme-source", "42", installed.Version, []string{"77"}),
		"the scan-written cache entry must carry the resolved file's completion marker")
}
