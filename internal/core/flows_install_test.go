package core_test

// Tests for Service.PlanInstall - the pure, read-only half of the
// pre-extraction CLI's doInstall (cmd/lmm/install.go), extracted per Phase
// 5b Task 1. See internal/core/flows.go's InstallPlan/PlanInstall doc
// comments for the exact behavior being tested here, and
// docs/plans/.superpowers/sdd/task-1-report.md for the full mapping/decision
// log.
//
// These tests reuse newFlowsTestService/seedInstalledMod and the
// mockSource/mockSourceWithDownloads fakes defined in service_test.go and
// flows_test.go (same core_test package) - see those files for their own
// doc comments.

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// noDepsSource wraps mockSource but always fails GetDependencies with
// source.ErrNotSupported, simulating a source that lacks the Dependencies
// capability (e.g. internal/source/custom.API with no dependencies endpoint
// configured - see api.go's GetDependencies).
type noDepsSource struct{ *mockSource }

func (s *noDepsSource) GetDependencies(ctx context.Context, mod *domain.Mod) ([]domain.ModReference, error) {
	return nil, fmt.Errorf("source %q: dependencies: %w", s.id, source.ErrNotSupported)
}

// sizedFileSource wraps mockSource but returns a single downloadable file of
// a caller-chosen size, so TotalDownloadBytes' summing (and its "-1 when
// unknown" fallback) can be tested independently of mockSource's own fixed,
// zero-Size default file.
type sizedFileSource struct {
	*mockSource
	size int64
}

func (s *sizedFileSource) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	return []domain.DownloadableFile{
		{ID: "1", Name: "Main File", FileName: mod.ID + ".zip", IsPrimary: true, Size: s.size},
	}, nil
}

// categorizedFilesSource wraps mockSource but returns a caller-supplied file
// list verbatim - real Category values, raw (unsorted) order, no forced
// IsPrimary - so PlanInstall's filtering/sorting of GetModFiles' result can
// be tested independently of mockSource's own fixed, single, uncategorized,
// IsPrimary file (service_test.go:57-66, which can't exercise either the
// archived-filtering or the category-sort behavior this covers).
type categorizedFilesSource struct {
	*mockSource
	files []domain.DownloadableFile
}

func (s *categorizedFilesSource) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	return s.files, nil
}

// authFailingSource is a minimal source.ModSource whose GetMod always fails
// with domain.ErrAuthRequired, mirroring what a real source does when no API
// key/token is configured (see internal/source/httpclient's 401 mapping).
type authFailingSource struct{ id string }

func (s *authFailingSource) ID() string      { return s.id }
func (s *authFailingSource) Name() string    { return "Auth Failing Source" }
func (s *authFailingSource) AuthURL() string { return "" }
func (s *authFailingSource) ExchangeToken(ctx context.Context, code string) (*source.Token, error) {
	return nil, nil
}
func (s *authFailingSource) Search(ctx context.Context, query source.SearchQuery) (source.SearchResult, error) {
	return source.SearchResult{}, nil
}
func (s *authFailingSource) GetMod(ctx context.Context, gameID, modID string) (*domain.Mod, error) {
	return nil, fmt.Errorf("source %q: %w", s.id, domain.ErrAuthRequired)
}
func (s *authFailingSource) GetDependencies(ctx context.Context, mod *domain.Mod) ([]domain.ModReference, error) {
	return nil, nil
}
func (s *authFailingSource) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	return nil, nil
}
func (s *authFailingSource) GetDownloadURL(ctx context.Context, mod *domain.Mod, fileID string) (string, error) {
	return "", nil
}
func (s *authFailingSource) CheckUpdates(ctx context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	return nil, nil
}

// --- PlanInstall ---

// TestService_PlanInstall_FreshInstallPlan covers the base case: a mod never
// installed before, with no cached files (so conflict detection can't run -
// see Conflicts' doc comment) and no dependencies.
func TestService_PlanInstall_FreshInstallPlan(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	mock := newMockSource("src")
	svc.RegisterSource(mock)
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"})

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "mod1", false)
	require.NoError(t, err)
	require.NotNil(t, plan)

	assert.Equal(t, "src", plan.SourceID)
	assert.Equal(t, "g1", plan.GameID)
	assert.Equal(t, "default", plan.Profile)
	assert.Equal(t, "mod1", plan.Mod.ID)
	assert.Nil(t, plan.Replaces)
	assert.Empty(t, plan.Dependencies)
	assert.Empty(t, plan.Conflicts)
	require.Len(t, plan.Files, 1)
	assert.Equal(t, "1", plan.Files[0].ID)
	assert.True(t, plan.Files[0].IsPrimary)
}

// TestService_PlanInstall_AlreadyInstalledModPopulatesReplaces mirrors
// doInstall's existingMod: an installed row for (sourceID, modID, profile)
// populates Replaces regardless of whether the fetched mod's version
// matches the installed one (reinstall and upgrade both go through Replace).
func TestService_PlanInstall_AlreadyInstalledModPopulatesReplaces(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	seedInstalledMod(t, svc, game, "src", "mod1", "1.0", true, nil)

	mock := newMockSource("src")
	svc.RegisterSource(mock)
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "2.0", GameID: "g1"})

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "mod1", false)
	require.NoError(t, err)
	require.NotNil(t, plan.Replaces)
	assert.Equal(t, "1.0", plan.Replaces.Version)
	assert.Equal(t, "2.0", plan.Mod.Version)
}

// TestService_PlanInstall_ConflictingFilesListsPathAndOwningMod proves
// Conflicts is populated exactly as installer.GetConflicts reports it, in
// the one situation where PlanInstall can compute it without downloading:
// the target mod's exact cache entry already exists (e.g. a leftover from a
// previous, now-abandoned install attempt). See Conflicts' doc comment for
// why a never-before-cached mod always reports empty Conflicts instead.
func TestService_PlanInstall_ConflictingFilesListsPathAndOwningMod(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	// "other" is installed and deployed, owning shared.esp.
	seedInstalledMod(t, svc, game, "src", "other", "1.0", true, map[string][]byte{"shared.esp": []byte("o")})
	installer := svc.GetInstaller(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "other", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))

	// "newmod" is NOT installed, but its cache entry already exists (at the
	// same version PlanInstall will fetch) with an overlapping file.
	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, "src", "newmod", "1.0", "shared.esp", []byte("n")))

	mock := newMockSource("src")
	svc.RegisterSource(mock)
	mock.AddMod("g1", &domain.Mod{ID: "newmod", SourceID: "src", Name: "New Mod", Version: "1.0", GameID: "g1"})

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "newmod", false)
	require.NoError(t, err)
	require.Len(t, plan.Conflicts, 1)
	assert.Equal(t, "shared.esp", plan.Conflicts[0].RelativePath)
	assert.Equal(t, "src", plan.Conflicts[0].CurrentSourceID)
	assert.Equal(t, "other", plan.Conflicts[0].CurrentModID)
}

// TestService_PlanInstall_DependenciesResolvedInOrder mirrors
// resolveDependencies' topological ordering: deepest dependency first,
// target excluded (it's Mod, not part of Dependencies).
func TestService_PlanInstall_DependenciesResolvedInOrder(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	mock := newMockSource("src")
	svc.RegisterSource(mock)
	dep2 := &domain.Mod{ID: "dep2", SourceID: "src", Name: "Dep Two", Version: "1.0", GameID: "g1"}
	dep1 := &domain.Mod{ID: "dep1", SourceID: "src", Name: "Dep One", Version: "1.0", GameID: "g1",
		Dependencies: []domain.ModReference{{SourceID: "src", ModID: "dep2"}}}
	root := &domain.Mod{ID: "root", SourceID: "src", Name: "Root", Version: "1.0", GameID: "g1",
		Dependencies: []domain.ModReference{{SourceID: "src", ModID: "dep1"}}}
	mock.AddMod("g1", dep2)
	mock.AddMod("g1", dep1)
	mock.AddMod("g1", root)

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "root", false)
	require.NoError(t, err)
	require.Len(t, plan.Dependencies, 2)
	assert.Equal(t, *dep2, plan.Dependencies[0], "deepest dependency must resolve first")
	assert.Equal(t, *dep1, plan.Dependencies[1])
	assert.Empty(t, plan.MissingDependencies)
	assert.False(t, plan.CycleDetected)
}

// TestService_PlanInstall_AlreadyInstalledDependencyIsSkipped mirrors
// resolveDependencies' installedIDs check: a dependency already installed
// under (game, profile) - regardless of Enabled - is not re-added.
func TestService_PlanInstall_AlreadyInstalledDependencyIsSkipped(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	seedInstalledMod(t, svc, game, "src", "dep1", "1.0", false, nil)

	mock := newMockSource("src")
	svc.RegisterSource(mock)
	mock.AddMod("g1", &domain.Mod{ID: "dep1", SourceID: "src", Name: "Dep One", Version: "1.0", GameID: "g1"})
	mock.AddMod("g1", &domain.Mod{ID: "root", SourceID: "src", Name: "Root", Version: "1.0", GameID: "g1",
		Dependencies: []domain.ModReference{{SourceID: "src", ModID: "dep1"}}})

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "root", false)
	require.NoError(t, err)
	assert.Empty(t, plan.Dependencies)
}

// TestService_PlanInstall_MissingAndCyclicDependenciesRecordedNotFatal
// covers two of resolveDependencies' non-fatal degradations at once: a
// dependency the source can't fetch, one listed for a different source than
// SourceID (both -> MissingDependencies, not an error), and a
// self-referential dependency (-> CycleDetected).
func TestService_PlanInstall_MissingAndCyclicDependenciesRecordedNotFatal(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	mock := newMockSource("src")
	svc.RegisterSource(mock)
	// "cross-source-dep" IS registered, but resolveInstallDependencies always
	// fetches using the top-level SourceID ("src"), and the fetched mod's own
	// SourceID ("src") won't match the reference's declared SourceID
	// ("other-source") - so it's still "missing", not resolved.
	mock.AddMod("g1", &domain.Mod{ID: "cross-source-dep", SourceID: "src", Name: "Cross", Version: "1.0", GameID: "g1"})
	mock.AddMod("g1", &domain.Mod{ID: "root2", SourceID: "src", Name: "Root Two", Version: "1.0", GameID: "g1",
		Dependencies: []domain.ModReference{
			{SourceID: "src", ModID: "missing-dep"},               // never registered - fetch fails
			{SourceID: "other-source", ModID: "cross-source-dep"}, // SourceID mismatch
			{SourceID: "src", ModID: "root2"},                     // self-reference - cycle
		}})

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "root2", false)
	require.NoError(t, err)
	assert.Empty(t, plan.Dependencies)
	assert.True(t, plan.CycleDetected)
	require.Len(t, plan.MissingDependencies, 2)
	assert.Contains(t, plan.MissingDependencies, domain.ModReference{SourceID: "src", ModID: "missing-dep"})
	assert.Contains(t, plan.MissingDependencies, domain.ModReference{SourceID: "other-source", ModID: "cross-source-dep"})
}

// TestService_PlanInstall_SourceWithoutDependenciesCapabilityDegradesToEmpty
// covers resolveDependencies' error-swallowing: ANY GetDependencies error
// (source.ErrNotSupported included) degrades to "no dependencies", not a
// failed plan.
func TestService_PlanInstall_SourceWithoutDependenciesCapabilityDegradesToEmpty(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	mock := &noDepsSource{mockSource: newMockSource("src")}
	svc.RegisterSource(mock)
	mock.AddMod("g1", &domain.Mod{ID: "root", SourceID: "src", Name: "Root", Version: "1.0", GameID: "g1"})

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "root", false)
	require.NoError(t, err)
	assert.Empty(t, plan.Dependencies)
	assert.Empty(t, plan.MissingDependencies)
	assert.False(t, plan.CycleDetected)
}

// TestService_PlanInstall_UnknownModReturnsErrModNotFound mirrors
// doInstall's own GetMod error handling ("failed to fetch mod: %w").
func TestService_PlanInstall_UnknownModReturnsErrModNotFound(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	mock := newMockSource("src")
	svc.RegisterSource(mock)

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "ghost", false)
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrModNotFound)
	assert.Nil(t, plan)
}

// TestService_PlanInstall_UnknownProfileIsNotAnError resolves the brief's
// "unknown profile" decision point by tracing doInstall: profiles are
// created lazily (pm.Get/pm.Create), only as a mutation right before saving
// - nothing in the read-only path this task extracts ever requires the
// profile to already exist. A never-before-seen profile name is therefore a
// perfectly valid Plan input, not an error.
func TestService_PlanInstall_UnknownProfileIsNotAnError(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	mock := newMockSource("src")
	svc.RegisterSource(mock)
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"})

	plan, err := svc.PlanInstall(context.Background(), game, "never-seen-before", "src", "mod1", false)
	require.NoError(t, err)
	require.NotNil(t, plan)
	assert.Nil(t, plan.Replaces)
}

// TestService_PlanInstall_TotalDownloadBytes covers both halves of the
// "sum when known, -1 when any unknown" rule against Files' single selected
// entry (PlanInstall's non-interactive default only ever selects one file -
// see the task report).
func TestService_PlanInstall_TotalDownloadBytes(t *testing.T) {
	t.Run("known size is reported directly", func(t *testing.T) {
		svc := newFlowsTestService(t)
		game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

		mock := &sizedFileSource{mockSource: newMockSource("src"), size: 12345}
		svc.RegisterSource(mock)
		mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"})

		plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "mod1", false)
		require.NoError(t, err)
		assert.Equal(t, int64(12345), plan.TotalDownloadBytes)
	})

	t.Run("unknown size reports -1", func(t *testing.T) {
		svc := newFlowsTestService(t)
		game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

		mock := newMockSource("src") // GetModFiles' fixed file has Size 0 (unknown)
		svc.RegisterSource(mock)
		mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"})

		plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "mod1", false)
		require.NoError(t, err)
		assert.Equal(t, int64(-1), plan.TotalDownloadBytes)
	})
}

// TestService_PlanInstall_AllArchivedFilesReturnsNoDownloadableFilesError
// covers the review finding (Phase 5b Task 1 fix wave 1): the CLI's
// doInstall filters out ARCHIVED/OLD_VERSION/DELETED files via
// filterAndSortFiles BEFORE its "no downloadable files" check
// (cmd/lmm/install.go:527-531). A mod whose only files are archived must
// therefore still produce this exact error from PlanInstall - not a "valid"
// plan pointing at a file the CLI would never let a user pick.
func TestService_PlanInstall_AllArchivedFilesReturnsNoDownloadableFilesError(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	mock := &categorizedFilesSource{
		mockSource: newMockSource("src"),
		files: []domain.DownloadableFile{
			{ID: "1", Name: "Old Main", FileName: "old.zip", Category: "ARCHIVED"},
			{ID: "2", Name: "Older Version", FileName: "older.zip", Category: "OLD_VERSION"},
		},
	}
	svc.RegisterSource(mock)
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"})

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "mod1", false)
	require.Error(t, err)
	assert.EqualError(t, err, "no downloadable files available for this mod")
	assert.Nil(t, plan)
}

// TestService_PlanInstall_MixedCategoriesNoPrimaryPicksMainFile covers the
// review finding's second consequence: with no IsPrimary flag set anywhere,
// the CLI's doInstall sorts files MAIN > OPTIONAL > UPDATE > MISCELLANEOUS >
// other (filterAndSortFiles) BEFORE selectInstallFiles's --yes default falls
// back to files[0] - so the CLI always picks the MAIN file here, never
// whichever file the source happened to return first.
func TestService_PlanInstall_MixedCategoriesNoPrimaryPicksMainFile(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	mock := &categorizedFilesSource{
		mockSource: newMockSource("src"),
		files: []domain.DownloadableFile{
			{ID: "optional-1", Name: "Optional Extra", FileName: "optional.zip", Category: "OPTIONAL"},
			{ID: "main-1", Name: "Main File", FileName: "main.zip", Category: "MAIN"},
		},
	}
	svc.RegisterSource(mock)
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"})

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "mod1", false)
	require.NoError(t, err)
	require.Len(t, plan.Files, 1)
	assert.Equal(t, "main-1", plan.Files[0].ID, "post-sort MAIN file must win the no-IsPrimary fallback, matching the CLI's filterAndSortFiles+selectInstallFiles order")
}

// TestService_PlanInstall_AuthRequiredSourceWrapsErrAuthRequired proves the
// returned error still satisfies errors.Is(err, domain.ErrAuthRequired) so a
// TUI caller can render its auth hint - PlanInstall does not (and must not)
// call the CLI's own authPromptError formatting.
func TestService_PlanInstall_AuthRequiredSourceWrapsErrAuthRequired(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	svc.RegisterSource(&authFailingSource{id: "src"})

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "mod1", false)
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrAuthRequired)
	assert.Nil(t, plan)
}

// TestService_PlanInstall_PerformsZeroMutations is PlanInstall's purity
// regression test, matching TestService_PlanProfileSwitch_PerformsZeroMutations's
// approach: an unrelated pre-existing mod's DB row and deployed file must be
// byte-for-byte/exactly unchanged, the planned mod's cache entry (and its
// dependency's) must never be created, and - since the mock source here
// never has AddDownload called for any file ID - any accidental download
// attempt would 404 and surface as an error, failing this test outright.
func TestService_PlanInstall_PerformsZeroMutations(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)

	// Unrelated pre-existing state to prove untouched.
	seedInstalledMod(t, svc, game, "src", "existing", "1.0", true, map[string][]byte{"existing.esp": []byte("e")})
	installer := svc.GetInstaller(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "existing", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))

	mock := newMockSourceWithDownloads("src") // no AddDownload: any download 404s
	defer mock.Close()
	svc.RegisterSource(mock)
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1",
		Dependencies: []domain.ModReference{{SourceID: "src", ModID: "dep1"}}})
	mock.AddMod("g1", &domain.Mod{ID: "dep1", SourceID: "src", Name: "Dep One", Version: "1.0", GameID: "g1"})

	beforeMods, err := svc.GetInstalledMods("g1", "default")
	require.NoError(t, err)

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "mod1", false)
	require.NoError(t, err)
	require.NotNil(t, plan)

	afterMods, err := svc.GetInstalledMods("g1", "default")
	require.NoError(t, err)
	assert.Equal(t, beforeMods, afterMods, "DB rows must be untouched after planning")

	gameCache := svc.GetGameCache(game)
	assert.False(t, gameCache.Exists("g1", "src", "mod1", "1.0"), "planning must not download/cache the target mod")
	assert.False(t, gameCache.Exists("g1", "src", "dep1", "1.0"), "planning must not download/cache dependencies")

	entries, err := os.ReadDir(gameDir)
	require.NoError(t, err)
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name()
	}
	assert.Equal(t, []string{"existing.esp"}, names, "planning must not deploy any files")
}

// --- ApplyInstall (Phase 5b Task 2) ---
//
// These tests reuse mockSourceWithDownloads (service_test.go) - a real
// httptest-backed source - since, unlike PlanInstall, ApplyInstall actually
// downloads. Two adapters wrap it for ApplyInstall's own needs:
//
//   - perModFileSource keys the single downloadable file by the MOD'S OWN
//     ID (mockSource.GetModFiles always hardcodes file ID "1"), so a plan
//     with a dependency AND a primary can register distinct download
//     content per mod via AddDownload(mod.ID, ...) without colliding.
//   - multiFileDownloadSource returns a caller-supplied file list verbatim,
//     for tests exercising a caller-edited (multi-file) plan.Files.

type perModFileSource struct {
	*mockSourceWithDownloads
}

func (s *perModFileSource) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	return []domain.DownloadableFile{
		{ID: mod.ID, Name: mod.Name, FileName: mod.ID + ".zip", IsPrimary: true},
	}, nil
}

type multiFileDownloadSource struct {
	*mockSourceWithDownloads
	files []domain.DownloadableFile
}

func (s *multiFileDownloadSource) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	return s.files, nil
}

// registerDownloadableMod registers mod with mock and stages a one-file zip
// archive (containing relativePath -> content) as that mod's download,
// keyed by mod.ID (matching perModFileSource's GetModFiles).
func registerDownloadableMod(t *testing.T, mock *perModFileSource, mod *domain.Mod, relativePath, content string) {
	t.Helper()
	zipPath := createTestZip(t, t.TempDir(), map[string]string{relativePath: content})
	zipContent, err := os.ReadFile(zipPath)
	require.NoError(t, err)
	mock.AddDownload(mod.ID, zipContent)
	mock.AddMod(mod.GameID, mod)
}

// TestService_ApplyInstall_FreshInstallEndToEnd covers ApplyInstall's base
// case end to end: a fresh (no existing, no dependencies) plan's file gets
// downloaded to cache, deployed to the game directory, saved to the DB with
// the normalized GameID/Enabled/Deployed/UpdatePolicy defaults, its checksum
// persisted, and the mod upserted into a profile that didn't exist yet.
func TestService_ApplyInstall_FreshInstallEndToEnd(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	mock := &perModFileSource{mockSourceWithDownloads: newMockSourceWithDownloads("src")}
	defer mock.Close()
	svc.RegisterSource(mock)
	registerDownloadableMod(t, mock, &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"}, "mod1.esp", "payload")

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "mod1", false)
	require.NoError(t, err)

	result, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, nil)
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, []string{"Mod One"}, result.Installed)
	assert.Empty(t, result.Warnings)
	assert.Empty(t, result.Notes)
	assert.Empty(t, result.Skipped)
	assert.Equal(t, 1, result.FilesDeployed)

	_, err = os.Lstat(filepath.Join(gameDir, "mod1.esp"))
	assert.NoError(t, err, "file should be deployed to the game directory")

	installed, err := svc.GetInstalledMod("src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "g1", installed.GameID, "GameID must be normalized to the lmm game, not a source-mapped value")
	assert.True(t, installed.Enabled)
	assert.True(t, installed.Deployed)
	assert.Equal(t, domain.UpdateNotify, installed.UpdatePolicy)
	assert.Equal(t, domain.LinkSymlink, installed.LinkMethod)

	files, err := svc.GetFilesWithChecksums("g1", "default")
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.NotEmpty(t, files[0].Checksum, "the downloaded file's checksum must be saved")

	pm := svc.NewProfileManager()
	profile, err := pm.Get("g1", "default")
	require.NoError(t, err, "the profile must have been created since it didn't exist yet")
	require.Len(t, profile.Mods, 1)
	assert.Equal(t, "mod1", profile.Mods[0].ModID)
}

// TestService_ApplyInstall_HookOrder proves install.before_all ->
// install.before_each -> (deploy) -> install.after_each -> install.after_all
// ordering for a single-mod (no dependencies) plan, mirroring
// TestService_DeployProfile_HookOrder/TestService_UninstallMod_HookOrder.
func TestService_ApplyInstall_HookOrder(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	scriptsDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	mock := &perModFileSource{mockSourceWithDownloads: newMockSourceWithDownloads("src")}
	defer mock.Close()
	svc.RegisterSource(mock)
	registerDownloadableMod(t, mock, &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"}, "mod1.esp", "payload")

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "mod1", false)
	require.NoError(t, err)

	callLog := filepath.Join(scriptsDir, "calls.log")
	beforeAllScript := createTestScript(t, scriptsDir, "before_all.sh", `#!/bin/bash
echo "before_all" >> `+callLog+`
exit 0`)
	beforeEachScript := createTestScript(t, scriptsDir, "before_each.sh", `#!/bin/bash
echo "before_each:$LMM_MOD_ID" >> `+callLog+`
exit 0`)
	afterEachScript := createTestScript(t, scriptsDir, "after_each.sh", `#!/bin/bash
echo "after_each:$LMM_MOD_ID" >> `+callLog+`
exit 0`)
	afterAllScript := createTestScript(t, scriptsDir, "after_all.sh", `#!/bin/bash
echo "after_all" >> `+callLog+`
exit 0`)

	hooks := &core.ResolvedHooks{Install: domain.HookConfig{
		BeforeAll: beforeAllScript, BeforeEach: beforeEachScript,
		AfterEach: afterEachScript, AfterAll: afterAllScript,
	}}
	runner := core.NewHookRunner(5 * time.Second)

	result, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{
		Hooks: hooks, HookRunner: runner,
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, result)

	logContent, err := os.ReadFile(callLog)
	require.NoError(t, err)
	assert.Equal(t, "before_all\nbefore_each:mod1\nafter_each:mod1\nafter_all\n", string(logContent))
}

// TestService_ApplyInstall_ChecksumSaveFailure_WarningNotDoublePrefixed
// guards a review finding: InstallResult.Warnings entries must NOT carry a
// baked-in "Warning: " prefix (matching DeployResult.Warnings' own
// convention - see its doc comment) since the CLI's InstallWarning handler
// already adds one uniformly (fmt.Fprintf(os.Stderr, "Warning: %s\n",
// p.Detail)); baking it into the message too would print
// "Warning: Warning: failed to save checksum...". Forces SaveFileChecksum to
// fail deterministically via a blocking UPDATE trigger on
// installed_mod_files.checksum (mirrors deploy_test.go's
// installBlockingTrigger).
func TestService_ApplyInstall_ChecksumSaveFailure_WarningNotDoublePrefixed(t *testing.T) {
	configDir, dataDir, gameDir := t.TempDir(), t.TempDir(), t.TempDir()
	svc, err := core.NewService(core.ServiceConfig{ConfigDir: configDir, DataDir: dataDir, CacheDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	mock := &perModFileSource{mockSourceWithDownloads: newMockSourceWithDownloads("src")}
	defer mock.Close()
	svc.RegisterSource(mock)
	registerDownloadableMod(t, mock, &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"}, "mod1.esp", "payload")

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "mod1", false)
	require.NoError(t, err)

	// Block UPDATEs to installed_mod_files.checksum with a second connection
	// - narrow enough that Install/SaveInstalledMod still succeed.
	conn, err := sql.Open("sqlite", filepath.Join(dataDir, "lmm.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })
	_, err = conn.Exec(`
		CREATE TRIGGER block_checksum_updates
		BEFORE UPDATE OF checksum ON installed_mod_files
		BEGIN
			SELECT RAISE(ABORT, 'blocked for test');
		END;
	`)
	require.NoError(t, err)

	var events []core.DeployProgress
	result, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, func(p core.DeployProgress) {
		events = append(events, p)
	})
	require.NoError(t, err, "a checksum-save failure must not fail the whole install")
	require.NotNil(t, result)
	assert.Equal(t, []string{"Mod One"}, result.Installed)

	require.Len(t, result.Warnings, 1)
	assert.True(t, strings.HasPrefix(result.Warnings[0], "failed to save checksum for file mod1: "), "got: %s", result.Warnings[0])
	assert.Contains(t, result.Warnings[0], "blocked for test")
	assert.NotContains(t, result.Warnings[0], "Warning:", "the Warnings entry itself must not carry a baked-in prefix - the caller's printer adds it")

	var warningEvt *core.DeployProgress
	for i := range events {
		if events[i].Phase == core.InstallWarning {
			warningEvt = &events[i]
		}
	}
	require.NotNil(t, warningEvt, "an InstallWarning event must fire for the checksum-save failure")
	assert.Equal(t, result.Warnings[0], warningEvt.Detail)
}

// TestService_ApplyInstall_DependencyInstallOrder proves dependencies
// install in plan order (deepest first) BEFORE the primary, all the way
// through to the DB/cache/deploy - not just that InstallPlan.Dependencies is
// ordered correctly (already covered by PlanInstall's own tests).
func TestService_ApplyInstall_DependencyInstallOrder(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	mock := &perModFileSource{mockSourceWithDownloads: newMockSourceWithDownloads("src")}
	defer mock.Close()
	svc.RegisterSource(mock)

	dep2 := &domain.Mod{ID: "dep2", SourceID: "src", Name: "Dep Two", Version: "1.0", GameID: "g1"}
	dep1 := &domain.Mod{ID: "dep1", SourceID: "src", Name: "Dep One", Version: "1.0", GameID: "g1",
		Dependencies: []domain.ModReference{{SourceID: "src", ModID: "dep2"}}}
	root := &domain.Mod{ID: "root", SourceID: "src", Name: "Root", Version: "1.0", GameID: "g1",
		Dependencies: []domain.ModReference{{SourceID: "src", ModID: "dep1"}}}
	registerDownloadableMod(t, mock, dep2, "dep2.esp", "payload-dep2")
	registerDownloadableMod(t, mock, dep1, "dep1.esp", "payload-dep1")
	registerDownloadableMod(t, mock, root, "root.esp", "payload-root")

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "root", false)
	require.NoError(t, err)
	require.Len(t, plan.Dependencies, 2)

	result, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, nil)
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, []string{"Dep Two", "Dep One", "Root"}, result.Installed, "dependencies must install before the primary, deepest first")
	assert.Empty(t, result.Skipped)

	for _, id := range []string{"dep2", "dep1", "root"} {
		_, err := svc.GetInstalledMod("src", id, "g1", "default")
		assert.NoError(t, err, "%s should be installed", id)
		_, err = os.Lstat(filepath.Join(gameDir, id+".esp"))
		assert.NoError(t, err, "%s should be deployed", id)
	}
}

// TestService_ApplyInstall_ReplacePath covers plan.Replaces' two cache
// handling variants, both mirroring doInstall's existingMod branch exactly:
// a same-version reinstall (the reinstall-cache-transaction path) and a
// version upgrade (a plain Replace, with the old version's cache cleared
// afterward).
func TestService_ApplyInstall_ReplacePath(t *testing.T) {
	t.Run("same-version reinstall replaces deployed content", func(t *testing.T) {
		svc := newFlowsTestService(t)
		gameDir := t.TempDir()
		game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

		seedInstalledMod(t, svc, game, "src", "mod1", "1.0", true, map[string][]byte{"mod1.esp": []byte("old-content")})
		installer := svc.GetInstaller(game)
		require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "mod1", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))

		mock := &perModFileSource{mockSourceWithDownloads: newMockSourceWithDownloads("src")}
		defer mock.Close()
		svc.RegisterSource(mock)
		registerDownloadableMod(t, mock, &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"}, "mod1.esp", "new-content")

		plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "mod1", false)
		require.NoError(t, err)
		require.NotNil(t, plan.Replaces)
		assert.Equal(t, "1.0", plan.Replaces.Version)

		result, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, nil)
		require.NoError(t, err)
		assert.Equal(t, []string{"Mod One"}, result.Installed)

		content, err := os.ReadFile(filepath.Join(gameDir, "mod1.esp"))
		require.NoError(t, err)
		assert.Equal(t, "new-content", string(content), "the reinstalled content must replace the old deployed file")
	})

	t.Run("version upgrade replaces old cache and deployed files", func(t *testing.T) {
		svc := newFlowsTestService(t)
		gameDir := t.TempDir()
		game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

		seedInstalledMod(t, svc, game, "src", "mod1", "1.0", true, map[string][]byte{"mod1-old.esp": []byte("old-content")})
		installer := svc.GetInstaller(game)
		require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "mod1", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))

		mock := &perModFileSource{mockSourceWithDownloads: newMockSourceWithDownloads("src")}
		defer mock.Close()
		svc.RegisterSource(mock)
		registerDownloadableMod(t, mock, &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "2.0", GameID: "g1"}, "mod1-new.esp", "new-content")

		plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "mod1", false)
		require.NoError(t, err)
		require.NotNil(t, plan.Replaces)
		assert.Equal(t, "1.0", plan.Replaces.Version)
		assert.Equal(t, "2.0", plan.Mod.Version)

		result, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, nil)
		require.NoError(t, err)
		assert.Equal(t, []string{"Mod One"}, result.Installed)

		_, err = os.Lstat(filepath.Join(gameDir, "mod1-old.esp"))
		assert.True(t, os.IsNotExist(err), "old version's file must be undeployed")
		_, err = os.Lstat(filepath.Join(gameDir, "mod1-new.esp"))
		assert.NoError(t, err, "new version's file must be deployed")

		assert.False(t, svc.GetGameCache(game).Exists("g1", "src", "mod1", "1.0"), "old version's cache entry should be cleared after a version upgrade")
		assert.True(t, svc.GetGameCache(game).Exists("g1", "src", "mod1", "2.0"))

		installed, err := svc.GetInstalledMod("src", "mod1", "g1", "default")
		require.NoError(t, err)
		assert.Equal(t, "2.0", installed.Version)
	})

	t.Run("same-version reinstall whose download fails leaves the original deployed content untouched", func(t *testing.T) {
		svc := newFlowsTestService(t)
		gameDir := t.TempDir()
		game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

		seedInstalledMod(t, svc, game, "src", "mod1", "1.0", true, map[string][]byte{"mod1.esp": []byte("original-content")})
		installer := svc.GetInstaller(game)
		require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "mod1", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))

		mock := &perModFileSource{mockSourceWithDownloads: newMockSourceWithDownloads("src")}
		defer mock.Close()
		svc.RegisterSource(mock)
		mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"})
		// Deliberately no AddDownload - the reinstall's download 404s, so
		// the reinstall-cache-transaction's deferred Rollback (Activate
		// never ran) must leave the live cache/deployed file untouched.

		plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "mod1", false)
		require.NoError(t, err)
		require.NotNil(t, plan.Replaces)

		result, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, nil)
		require.Error(t, err)
		require.NotNil(t, result, "a partial result must be returned alongside the error")
		assert.Empty(t, result.Installed)

		content, err := os.ReadFile(filepath.Join(gameDir, "mod1.esp"))
		require.NoError(t, err, "the originally-deployed file must survive untouched")
		assert.Equal(t, "original-content", string(content))

		installed, err := svc.GetInstalledMod("src", "mod1", "g1", "default")
		require.NoError(t, err)
		assert.Equal(t, "1.0", installed.Version, "DB row must be unchanged")
	})
}

// TestService_ApplyInstall_DownloadFailure covers the primary's download
// failure (fatal, partial result returned per convention, nothing half-saved)
// and a dependency's download failure (skip-and-continue, matching
// batchInstallMods - the primary still installs).
func TestService_ApplyInstall_DownloadFailure(t *testing.T) {
	t.Run("primary download failure is fatal with a partial result", func(t *testing.T) {
		svc := newFlowsTestService(t)
		gameDir := t.TempDir()
		game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

		mock := &perModFileSource{mockSourceWithDownloads: newMockSourceWithDownloads("src")}
		defer mock.Close()
		svc.RegisterSource(mock)
		// Deliberately no AddDownload - the download 404s deterministically.
		mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"})

		plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "mod1", false)
		require.NoError(t, err)

		result, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "download failed")
		require.NotNil(t, result, "a partial result must be returned alongside the error")
		assert.Empty(t, result.Installed)

		assert.False(t, svc.GetGameCache(game).Exists("g1", "src", "mod1", "1.0"))
		_, dbErr := svc.GetInstalledMod("src", "mod1", "g1", "default")
		assert.Error(t, dbErr, "the mod must not be saved to the DB when its download fails")
	})

	t.Run("dependency download failure skips the dependency but still installs the primary", func(t *testing.T) {
		svc := newFlowsTestService(t)
		gameDir := t.TempDir()
		game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

		mock := &perModFileSource{mockSourceWithDownloads: newMockSourceWithDownloads("src")}
		defer mock.Close()
		svc.RegisterSource(mock)

		dep1 := &domain.Mod{ID: "dep1", SourceID: "src", Name: "Dep One", Version: "1.0", GameID: "g1"}
		root := &domain.Mod{ID: "root", SourceID: "src", Name: "Root", Version: "1.0", GameID: "g1",
			Dependencies: []domain.ModReference{{SourceID: "src", ModID: "dep1"}}}
		mock.AddMod("g1", dep1) // no AddDownload for dep1 - its download 404s
		registerDownloadableMod(t, mock, root, "root.esp", "payload")

		plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "root", false)
		require.NoError(t, err)
		require.Len(t, plan.Dependencies, 1)

		result, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, nil)
		require.NoError(t, err, "a dependency's download failure must not fail the whole install")
		require.NotNil(t, result)
		assert.Equal(t, []string{"Root"}, result.Installed)
		require.Len(t, result.Skipped, 1)
		assert.Contains(t, result.Skipped[0], "Dep One: download failed")

		_, err = svc.GetInstalledMod("src", "dep1", "g1", "default")
		assert.Error(t, err, "the failed dependency must not be saved")
		_, err = svc.GetInstalledMod("src", "root", "g1", "default")
		assert.NoError(t, err, "the primary must still install despite the dependency's failure")
	})
}

// TestService_ApplyInstall_ProgressEvents covers the download percent
// sequence, per-mod attribution, and a nil progress callback being safe.
func TestService_ApplyInstall_ProgressEvents(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	mock := &perModFileSource{mockSourceWithDownloads: newMockSourceWithDownloads("src")}
	defer mock.Close()
	svc.RegisterSource(mock)
	mod := &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"}
	registerDownloadableMod(t, mock, mod, "mod1.esp", strings.Repeat("x", 8192))

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "mod1", false)
	require.NoError(t, err)

	var events []core.DeployProgress
	result, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, func(p core.DeployProgress) {
		events = append(events, p)
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	var sawStarted, sawDownloading, sawDone, sawInstalled bool
	for _, e := range events {
		switch e.Phase {
		case core.InstallDownloadStarted:
			sawStarted = true
			assert.Equal(t, "Mod One", e.ModName)
			require.NotNil(t, e.File)
		case core.InstallDownloading:
			sawDownloading = true
			assert.GreaterOrEqual(t, e.Percent, 0.0)
			assert.Greater(t, e.TotalBytes, int64(0))
		case core.InstallDownloadDone:
			sawDone = true
		case core.InstallDone:
			sawInstalled = true
			assert.Equal(t, "Mod One", e.ModName)
		}
	}
	assert.True(t, sawStarted, "InstallDownloadStarted must fire")
	assert.True(t, sawDownloading, "at least one InstallDownloading tick expected for a known-size download")
	assert.True(t, sawDone, "InstallDownloadDone must fire")
	assert.True(t, sawInstalled, "InstallDone must fire")

	// A nil progress callback must be safe (no panic).
	plan2, err := svc.PlanInstall(context.Background(), game, "default", "src", "mod1", false)
	require.NoError(t, err)
	_, err = svc.ApplyInstall(context.Background(), game, plan2, core.InstallOptions{}, nil)
	require.NoError(t, err)
}

// TestService_ApplyInstall_BeforeAllHookFailure mirrors
// TestService_DeployProfile's before_all Force-gate pattern: fatal without
// Force, a recorded (forced) Warning with Force, matching doInstall exactly
// (before_all only ever runs once, regardless of Dependencies).
func TestService_ApplyInstall_BeforeAllHookFailure(t *testing.T) {
	scriptsDir := t.TempDir()
	failScript := createTestScript(t, scriptsDir, "before_all.sh", "#!/bin/bash\necho boom >&2\nexit 1\n")
	hooks := &core.ResolvedHooks{Install: domain.HookConfig{BeforeAll: failScript}}
	runner := core.NewHookRunner(5 * time.Second)

	newPlan := func(t *testing.T) (*core.Service, *domain.Game, *core.InstallPlan) {
		svc := newFlowsTestService(t)
		gameDir := t.TempDir()
		game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}
		mock := &perModFileSource{mockSourceWithDownloads: newMockSourceWithDownloads("src")}
		t.Cleanup(mock.Close)
		svc.RegisterSource(mock)
		registerDownloadableMod(t, mock, &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"}, "mod1.esp", "payload")
		plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "mod1", false)
		require.NoError(t, err)
		return svc, game, plan
	}

	t.Run("fatal without Force", func(t *testing.T) {
		svc, game, plan := newPlan(t)
		result, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{Hooks: hooks, HookRunner: runner}, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "install.before_all hook failed")
		require.NotNil(t, result)
		assert.Empty(t, result.Installed)
		_, dbErr := svc.GetInstalledMod("src", "mod1", "g1", "default")
		assert.Error(t, dbErr)
	})

	t.Run("forced continues with a warning", func(t *testing.T) {
		svc, game, plan := newPlan(t)
		result, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{Hooks: hooks, HookRunner: runner, Force: true}, nil)
		require.NoError(t, err)
		require.NotNil(t, result)
		require.Len(t, result.Warnings, 1)
		assert.Contains(t, result.Warnings[0], "install.before_all hook failed")
		assert.Contains(t, result.Warnings[0], "forced")
		assert.Equal(t, []string{"Mod One"}, result.Installed)
	})
}

// TestService_ApplyInstall_PrimaryBeforeEachHookFailure mirrors
// doInstall's OWN before_each Force-gate for the primary mod (fatal unless
// Force) - deliberately distinct from a dependency's before_each semantics
// (always skip-and-continue, never Force-gated - see
// TestService_ApplyInstall_DependencyBeforeEachHookFailure_SkipsAndContinues).
func TestService_ApplyInstall_PrimaryBeforeEachHookFailure(t *testing.T) {
	scriptsDir := t.TempDir()
	failScript := createTestScript(t, scriptsDir, "before_each.sh", "#!/bin/bash\necho boom >&2\nexit 1\n")
	hooks := &core.ResolvedHooks{Install: domain.HookConfig{BeforeEach: failScript}}
	runner := core.NewHookRunner(5 * time.Second)

	newPlan := func(t *testing.T) (*core.Service, *domain.Game, *core.InstallPlan) {
		svc := newFlowsTestService(t)
		gameDir := t.TempDir()
		game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}
		mock := &perModFileSource{mockSourceWithDownloads: newMockSourceWithDownloads("src")}
		t.Cleanup(mock.Close)
		svc.RegisterSource(mock)
		registerDownloadableMod(t, mock, &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"}, "mod1.esp", "payload")
		plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "mod1", false)
		require.NoError(t, err)
		return svc, game, plan
	}

	t.Run("fatal without Force", func(t *testing.T) {
		svc, game, plan := newPlan(t)
		result, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{Hooks: hooks, HookRunner: runner}, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "install.before_each hook failed")
		require.NotNil(t, result)
		assert.Empty(t, result.Installed)
	})

	t.Run("forced continues with a warning", func(t *testing.T) {
		svc, game, plan := newPlan(t)
		result, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{Hooks: hooks, HookRunner: runner, Force: true}, nil)
		require.NoError(t, err)
		require.NotNil(t, result)
		require.Len(t, result.Warnings, 1)
		assert.Contains(t, result.Warnings[0], "install.before_each hook failed")
		assert.Contains(t, result.Warnings[0], "forced")
		assert.Equal(t, []string{"Mod One"}, result.Installed)
	})
}

// TestService_ApplyInstall_DependencyBeforeEachHookFailure_SkipsAndContinues
// proves a dependency's before_each hook failure is NEVER Force-gated
// (unconditional skip-and-continue, matching batchInstallMods, which is what
// pre-extraction doInstall actually delegated dependency installation to) -
// the primary still installs even though Force is not set.
func TestService_ApplyInstall_DependencyBeforeEachHookFailure_SkipsAndContinues(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	mock := &perModFileSource{mockSourceWithDownloads: newMockSourceWithDownloads("src")}
	defer mock.Close()
	svc.RegisterSource(mock)
	dep1 := &domain.Mod{ID: "dep1", SourceID: "src", Name: "Dep One", Version: "1.0", GameID: "g1"}
	root := &domain.Mod{ID: "root", SourceID: "src", Name: "Root", Version: "1.0", GameID: "g1",
		Dependencies: []domain.ModReference{{SourceID: "src", ModID: "dep1"}}}
	registerDownloadableMod(t, mock, dep1, "dep1.esp", "payload-dep1")
	registerDownloadableMod(t, mock, root, "root.esp", "payload-root")

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "root", false)
	require.NoError(t, err)
	require.Len(t, plan.Dependencies, 1)

	scriptsDir := t.TempDir()
	// Fails ONLY for dep1 - the primary's own before_each must still succeed,
	// isolating this test to the dependency's skip-and-continue semantics.
	failScript := createTestScript(t, scriptsDir, "before_each.sh", `#!/bin/bash
if [ "$LMM_MOD_ID" = "dep1" ]; then
  echo boom >&2
  exit 1
fi
exit 0`)
	hooks := &core.ResolvedHooks{Install: domain.HookConfig{BeforeEach: failScript}}
	runner := core.NewHookRunner(5 * time.Second)

	result, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{Hooks: hooks, HookRunner: runner}, nil)
	require.NoError(t, err, "a dependency's before_each failure must never fail the whole install, even without Force")
	require.NotNil(t, result)
	assert.Equal(t, []string{"Root"}, result.Installed)
	require.Len(t, result.Skipped, 1)
	assert.Contains(t, result.Skipped[0], "Dep One: install.before_each hook failed")
	assert.Empty(t, result.Warnings, "a dependency hook skip is never Force-gated, so it must never produce a Warning")
}

// TestService_ApplyInstall_EditedPlanFilesHonored proves ApplyInstall
// installs exactly plan.Files - no re-selection - so a caller (the CLI's
// interactive/--file override) can freely edit plan.Files between Plan and
// Apply.
func TestService_ApplyInstall_EditedPlanFilesHonored(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	mock := &multiFileDownloadSource{
		mockSourceWithDownloads: newMockSourceWithDownloads("src"),
		files: []domain.DownloadableFile{
			{ID: "main", Name: "Main File", FileName: "main.zip", IsPrimary: true, Category: "MAIN"},
			{ID: "optional", Name: "Optional File", FileName: "optional.zip", Category: "OPTIONAL"},
		},
	}
	defer mock.Close()
	svc.RegisterSource(mock)

	mainZip := createTestZip(t, t.TempDir(), map[string]string{"main.esp": "main-payload"})
	mainContent, err := os.ReadFile(mainZip)
	require.NoError(t, err)
	mock.AddDownload("main", mainContent)

	optZip := createTestZip(t, t.TempDir(), map[string]string{"optional.esp": "optional-payload"})
	optContent, err := os.ReadFile(optZip)
	require.NoError(t, err)
	mock.AddDownload("optional", optContent)

	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"})

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "mod1", false)
	require.NoError(t, err)
	require.Len(t, plan.Files, 1, "PlanInstall's own default picks just the primary/main file")
	assert.Equal(t, "main", plan.Files[0].ID)

	// Caller (CLI's interactive/--file override) selects BOTH files instead.
	plan.Files = mock.files

	result, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, []string{"Mod One"}, result.Installed)

	_, err = os.Lstat(filepath.Join(gameDir, "main.esp"))
	assert.NoError(t, err, "main file must be installed")
	_, err = os.Lstat(filepath.Join(gameDir, "optional.esp"))
	assert.NoError(t, err, "the caller-added optional file must ALSO be installed - ApplyInstall must install exactly plan.Files, not re-select")

	installed, err := svc.GetInstalledMod("src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"main", "optional"}, installed.FileIDs)
}

// TestService_ApplyInstall_ContextCancellation proves ApplyInstall checks
// ctx at least once before doing any work, so an already-cancelled context
// leaves nothing installed - the seam Phase 5b's cancel-then-drain task will
// build on for mid-run cancellation between mods.
func TestService_ApplyInstall_ContextCancellation(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	mock := &perModFileSource{mockSourceWithDownloads: newMockSourceWithDownloads("src")}
	defer mock.Close()
	svc.RegisterSource(mock)
	dep1 := &domain.Mod{ID: "dep1", SourceID: "src", Name: "Dep One", Version: "1.0", GameID: "g1"}
	root := &domain.Mod{ID: "root", SourceID: "src", Name: "Root", Version: "1.0", GameID: "g1",
		Dependencies: []domain.ModReference{{SourceID: "src", ModID: "dep1"}}}
	registerDownloadableMod(t, mock, dep1, "dep1.esp", "payload")
	registerDownloadableMod(t, mock, root, "root.esp", "payload")

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "root", false)
	require.NoError(t, err)
	require.Len(t, plan.Dependencies, 1)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before ApplyInstall even starts

	result, err := svc.ApplyInstall(ctx, game, plan, core.InstallOptions{}, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	require.NotNil(t, result)
	assert.Empty(t, result.Installed)

	_, dbErr := svc.GetInstalledMod("src", "dep1", "g1", "default")
	assert.Error(t, dbErr, "nothing should be installed once the context is already cancelled")
}

// --- Fix wave 1 (dep-path fidelity) ---
//
// The tests below pin the review's Critical finding: when plan.Dependencies
// is non-empty, ApplyInstall must apply batchInstallMods' lenient BATCH
// mechanics to the PRIMARY too (never Force-gated, no Replace, no
// interactive selection, non-blocking conflicts) - not the STRICT/no-deps
// path's mechanics, which Task 2's original design incorrectly ran for the
// primary unconditionally. See task-2-report.md's "Fix wave 1" entry for
// the full review trace and cmd/lmm/install.go's pre-extraction
// batchInstallMods (git show 5243286:cmd/lmm/install.go, lines ~1175-1347)
// for the ground truth this restores.

// TestService_ApplyInstall_DependenciesPresent_PrimaryUsesBatchSemantics
// proves the primary's own before_each hook failure is skip-and-continue
// (never fatal, never Force-gated) once Dependencies is non-empty -
// mirroring TestService_ApplyInstall_DependencyBeforeEachHookFailure_SkipsAndContinues,
// but for the PRIMARY instead of a dependency. Also proves InstallDepInstalling
// fires for BOTH mods with Index/Total spanning the whole combined list and
// ModVersion populated - the data the restored "[%d/%d] Installing: %s v%s"
// header needs.
func TestService_ApplyInstall_DependenciesPresent_PrimaryUsesBatchSemantics(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	mock := &perModFileSource{mockSourceWithDownloads: newMockSourceWithDownloads("src")}
	defer mock.Close()
	svc.RegisterSource(mock)
	dep1 := &domain.Mod{ID: "dep1", SourceID: "src", Name: "Dep One", Version: "1.0", GameID: "g1"}
	root := &domain.Mod{ID: "root", SourceID: "src", Name: "Root", Version: "1.0", GameID: "g1",
		Dependencies: []domain.ModReference{{SourceID: "src", ModID: "dep1"}}}
	registerDownloadableMod(t, mock, dep1, "dep1.esp", "payload-dep1")
	registerDownloadableMod(t, mock, root, "root.esp", "payload-root")

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "root", false)
	require.NoError(t, err)
	require.Len(t, plan.Dependencies, 1)

	scriptsDir := t.TempDir()
	// Fails ONLY for the primary (root) - dep1's before_each must still
	// succeed, isolating this test to the primary's own skip-and-continue
	// semantics in the BATCH path.
	failScript := createTestScript(t, scriptsDir, "before_each.sh", `#!/bin/bash
if [ "$LMM_MOD_ID" = "root" ]; then
  echo boom >&2
  exit 1
fi
exit 0`)
	hooks := &core.ResolvedHooks{Install: domain.HookConfig{BeforeEach: failScript}}
	runner := core.NewHookRunner(5 * time.Second)

	var events []core.DeployProgress
	result, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{Hooks: hooks, HookRunner: runner}, func(p core.DeployProgress) {
		events = append(events, p)
	})
	require.NoError(t, err, "the primary's before_each failure must never fail the whole install in the BATCH path, even without Force")
	require.NotNil(t, result)
	assert.Equal(t, []string{"Dep One"}, result.Installed, "only the dependency installs - the primary was skipped")
	require.Len(t, result.Skipped, 1)
	assert.Contains(t, result.Skipped[0], "Root: install.before_each hook failed")
	require.Len(t, result.Failed, 1)
	assert.Equal(t, "Root", result.Failed[0])
	assert.Empty(t, result.Warnings, "a BATCH-path hook skip is never Force-gated, so it must never produce a Warning")

	_, dbErr := svc.GetInstalledMod("src", "root", "g1", "default")
	assert.Error(t, dbErr, "the skipped primary must not be saved")

	var installingEvents []core.DeployProgress
	for _, e := range events {
		if e.Phase == core.InstallDepInstalling {
			installingEvents = append(installingEvents, e)
		}
	}
	require.Len(t, installingEvents, 2, "InstallDepInstalling must fire for the primary too, not just the dependency")
	assert.Equal(t, 1, installingEvents[0].Index)
	assert.Equal(t, 2, installingEvents[0].Total, "Index/Total must span the WHOLE combined list (dep + primary)")
	assert.Equal(t, "Dep One", installingEvents[0].ModName)
	assert.Equal(t, "1.0", installingEvents[0].ModVersion)
	assert.Equal(t, 2, installingEvents[1].Index)
	assert.Equal(t, 2, installingEvents[1].Total)
	assert.Equal(t, "Root", installingEvents[1].ModName)
	assert.Equal(t, "1.0", installingEvents[1].ModVersion)
}

// TestService_ApplyInstall_DependenciesPresent_InstalledEventCarriesFileCount
// pins InstallDepInstalled's restored FilesExtracted payload (mirroring
// batchInstallMods' "  ✓ Installed (%d files)\n" - Task 2's original design
// used the mod's name instead) for BOTH a dependency and the primary, and
// proves InstallResult.FilesDeployed (a STRICT-path-only accumulator) stays
// 0 in the BATCH path, matching batchInstallMods' own terminal summary,
// which never printed a file count.
func TestService_ApplyInstall_DependenciesPresent_InstalledEventCarriesFileCount(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	mock := &perModFileSource{mockSourceWithDownloads: newMockSourceWithDownloads("src")}
	defer mock.Close()
	svc.RegisterSource(mock)
	dep1 := &domain.Mod{ID: "dep1", SourceID: "src", Name: "Dep One", Version: "1.0", GameID: "g1"}
	root := &domain.Mod{ID: "root", SourceID: "src", Name: "Root", Version: "1.0", GameID: "g1",
		Dependencies: []domain.ModReference{{SourceID: "src", ModID: "dep1"}}}
	registerDownloadableMod(t, mock, dep1, "dep1.esp", "payload-dep1")
	registerDownloadableMod(t, mock, root, "root.esp", "payload-root")

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "root", false)
	require.NoError(t, err)

	var events []core.DeployProgress
	result, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, func(p core.DeployProgress) {
		events = append(events, p)
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"Dep One", "Root"}, result.Installed)
	assert.Equal(t, 0, result.FilesDeployed, "FilesDeployed is a STRICT-path-only accumulator - the BATCH path never touches it")

	var installedEvents []core.DeployProgress
	for _, e := range events {
		if e.Phase == core.InstallDepInstalled {
			installedEvents = append(installedEvents, e)
		}
	}
	require.Len(t, installedEvents, 2)
	assert.Equal(t, 1, installedEvents[0].FilesExtracted, "each mod's own extracted-file count must be reported")
	assert.Equal(t, 1, installedEvents[1].FilesExtracted)
}

// TestService_ApplyInstall_DependenciesPresent_ExistingPrimaryUsesUninstallNotReplace
// proves that even though plan.Replaces is populated (the primary is
// already installed), a dependency-having install must uninstall+
// cache-delete the existing row first and perform a FRESH Install - never
// Replace/the reinstall-cache-transaction (STRICT-path-only mechanisms) -
// matching batchInstallMods' "Remove previous installation" branch, applied
// identically to a dependency or the primary. Also proves InstallDepReinstalling
// fires (mirroring batchInstallMods' unconditional "  Removing previous
// installation...").
func TestService_ApplyInstall_DependenciesPresent_ExistingPrimaryUsesUninstallNotReplace(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedInstalledMod(t, svc, game, "src", "root", "1.0", true, map[string][]byte{"root-old.esp": []byte("old-content")})
	installer := svc.GetInstaller(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "root", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))

	mock := &perModFileSource{mockSourceWithDownloads: newMockSourceWithDownloads("src")}
	defer mock.Close()
	svc.RegisterSource(mock)
	dep1 := &domain.Mod{ID: "dep1", SourceID: "src", Name: "Dep One", Version: "1.0", GameID: "g1"}
	root := &domain.Mod{ID: "root", SourceID: "src", Name: "Root", Version: "1.0", GameID: "g1",
		Dependencies: []domain.ModReference{{SourceID: "src", ModID: "dep1"}}}
	registerDownloadableMod(t, mock, dep1, "dep1.esp", "payload-dep1")
	registerDownloadableMod(t, mock, root, "root.esp", "payload-root")

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "root", false)
	require.NoError(t, err)
	require.NotNil(t, plan.Replaces, "the primary IS already installed - PlanInstall must still populate Replaces")

	var events []core.DeployProgress
	result, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, func(p core.DeployProgress) {
		events = append(events, p)
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"Dep One", "Root"}, result.Installed)

	var sawReinstalling bool
	for _, e := range events {
		if e.Phase == core.InstallDepReinstalling && e.ModName == "Root" {
			sawReinstalling = true
		}
	}
	assert.True(t, sawReinstalling, "InstallDepReinstalling must fire for the already-installed primary in the BATCH path")

	_, err = os.Lstat(filepath.Join(gameDir, "root-old.esp"))
	assert.True(t, os.IsNotExist(err), "old file must be undeployed (uninstalled, not Replaced)")
	_, err = os.Lstat(filepath.Join(gameDir, "root.esp"))
	assert.NoError(t, err, "new file must be deployed via a fresh Install")
}

// TestService_ApplyInstall_DependenciesPresent_ProgressVocabularyRestored
// pins the BATCH path's restored per-event vocabulary for a plain,
// successful dependency-having install: InstallDepFileSelected (the
// restored "  File: %s" line), InstallDepDownloading/InstallDepDownloadDone
// (the per-mod download progress and its unconditional trailing blank
// line), and InstallChecksumComputed (now ALSO reused for BATCH-path mods,
// not STRICT-path-only) must all fire once per mod - dependency and primary
// alike.
func TestService_ApplyInstall_DependenciesPresent_ProgressVocabularyRestored(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	mock := &perModFileSource{mockSourceWithDownloads: newMockSourceWithDownloads("src")}
	defer mock.Close()
	svc.RegisterSource(mock)
	dep1 := &domain.Mod{ID: "dep1", SourceID: "src", Name: "Dep One", Version: "1.0", GameID: "g1"}
	mod := &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1",
		Dependencies: []domain.ModReference{{SourceID: "src", ModID: "dep1"}}}
	registerDownloadableMod(t, mock, dep1, "dep1.esp", "payload")
	registerDownloadableMod(t, mock, mod, "mod1.esp", "payload")

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "mod1", false)
	require.NoError(t, err)

	var events []core.DeployProgress
	_, err = svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, func(p core.DeployProgress) {
		events = append(events, p)
	})
	require.NoError(t, err)

	var sawFileSelected, sawDownloadDone, sawChecksum int
	for _, e := range events {
		switch e.Phase {
		case core.InstallDepFileSelected:
			sawFileSelected++
			require.NotNil(t, e.File)
		case core.InstallDepDownloadDone:
			sawDownloadDone++
		case core.InstallChecksumComputed:
			sawChecksum++
			assert.NotEmpty(t, e.Detail)
		}
	}
	assert.Equal(t, 2, sawFileSelected, "one per mod - dependency and primary alike")
	assert.Equal(t, 2, sawDownloadDone, "one per mod, unconditional (success or failure)")
	assert.Equal(t, 2, sawChecksum, "one per mod - InstallChecksumComputed is no longer STRICT-path-only")
}

// TestService_ApplyInstall_ReplacePath_SaveInstalledModFailureRollsBackReinstallCache
// covers the review's "Important" ask: forcing SaveInstalledMod to fail
// deterministically MID-REINSTALL (after the reinstall-cache-transaction has
// already Activate()'d - i.e. downloaded/deployed the new content) must roll
// back to the ORIGINAL cached/deployed content, not leave a half-migrated
// cache behind. Uses installBlockingTrigger (flows_test.go), which blocks
// any UPDATE touching installed_mods.link_method/deployed - exactly the
// columns SaveInstalledMod's ON CONFLICT...DO UPDATE always sets, so a
// reinstall's SECOND SaveInstalledMod call (an UPDATE, since the row already
// exists) fails deterministically - the same technique
// TestService_ApplyInstall_ChecksumSaveFailure_WarningNotDoublePrefixed uses
// for a different column.
func TestService_ApplyInstall_ReplacePath_SaveInstalledModFailureRollsBackReinstallCache(t *testing.T) {
	configDir, dataDir, gameDir := t.TempDir(), t.TempDir(), t.TempDir()
	svc, err := core.NewService(core.ServiceConfig{ConfigDir: configDir, DataDir: dataDir, CacheDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedInstalledMod(t, svc, game, "src", "mod1", "1.0", true, map[string][]byte{"mod1.esp": []byte("original-content")})
	installer := svc.GetInstaller(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "mod1", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))

	mock := &perModFileSource{mockSourceWithDownloads: newMockSourceWithDownloads("src")}
	defer mock.Close()
	svc.RegisterSource(mock)
	registerDownloadableMod(t, mock, &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"}, "mod1.esp", "new-content")

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "mod1", false)
	require.NoError(t, err)
	require.NotNil(t, plan.Replaces)
	assert.Equal(t, "1.0", plan.Replaces.Version, "a same-version reinstall - the reinstall-cache-transaction path")

	installBlockingTrigger(t, filepath.Join(dataDir, "lmm.db"))

	result, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to save mod")
	require.NotNil(t, result, "a partial result must be returned alongside the error")
	assert.Empty(t, result.Installed)

	content, err := os.ReadFile(filepath.Join(gameDir, "mod1.esp"))
	require.NoError(t, err, "the original deployed file must survive the rollback")
	assert.Equal(t, "original-content", string(content))

	assert.True(t, svc.GetGameCache(game).Exists("g1", "src", "mod1", "1.0"), "the live cache entry must exist (restored, not left empty/half-migrated)")

	installed, err := svc.GetInstalledMod("src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0", installed.Version, "DB row must be unchanged")
}
