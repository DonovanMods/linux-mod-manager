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
	"fmt"
	"os"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "mod1")
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

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "mod1")
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

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "newmod")
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

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "root")
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

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "root")
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

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "root2")
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

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "root")
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

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "ghost")
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

	plan, err := svc.PlanInstall(context.Background(), game, "never-seen-before", "src", "mod1")
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

		plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "mod1")
		require.NoError(t, err)
		assert.Equal(t, int64(12345), plan.TotalDownloadBytes)
	})

	t.Run("unknown size reports -1", func(t *testing.T) {
		svc := newFlowsTestService(t)
		game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

		mock := newMockSource("src") // GetModFiles' fixed file has Size 0 (unknown)
		svc.RegisterSource(mock)
		mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"})

		plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "mod1")
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

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "mod1")
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

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "mod1")
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

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "mod1")
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

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "mod1")
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
