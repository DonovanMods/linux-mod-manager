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
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

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

// failingDepsSource wraps mockSource but always fails GetDependencies with a
// plain (non-source.ErrNotSupported) error, simulating a transient/real
// failure - a rate limit, a network blip, a malformed API response - as
// opposed to noDepsSource's "this source doesn't have the capability at
// all". Item 10 (#52) distinguishes these two: ErrNotSupported stays a
// silent degrade, everything else must be recorded in
// InstallPlan.DependencyWarnings rather than swallowed identically.
type failingDepsSource struct{ *mockSource }

func (s *failingDepsSource) GetDependencies(ctx context.Context, mod *domain.Mod) ([]domain.ModReference, error) {
	return nil, fmt.Errorf("source %q: dependencies: %w", s.id, errBoom)
}

// errBoom is a sentinel non-ErrNotSupported error for failingDepsSource.
var errBoom = errors.New("boom: dependency service unavailable")

// installedRefNames extracts each entry's Name, for tests below that only
// care which mods ended up in InstallResult.Installed/Skipped/Failed - not
// the structured (SourceID/ModID/Version/Reason) detail carried alongside it
// (v2 Phase 3 Task 2, #301).
func installedRefNames(refs []core.InstalledRef) []string {
	names := make([]string, len(refs))
	for i, r := range refs {
		names[i] = r.Name
	}
	return names
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
	installer := svc.GetInstallerForTest(game)
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
// covers resolveInstallDependencies' ErrNotSupported handling specifically
// (#52 item 10 split this from "ANY error swallowed" - see
// TestService_PlanInstall_DependencyResolutionErrorRecordedAsWarning for the
// other-error branch): a source that doesn't have the Dependencies
// capability at all degrades to "no dependencies" SILENTLY - no plan
// failure, and no DependencyWarnings entry either, since ErrNotSupported
// isn't something a user can act on.
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
	assert.Empty(t, plan.DependencyWarnings, "ErrNotSupported must stay silent, not surface as a warning")
}

// TestService_PlanInstall_DependencyResolutionErrorRecordedAsWarning covers
// the other half of item 10 (#52): a GetDependencies failure that is NOT
// source.ErrNotSupported (a real, non-capability-gap failure) must still
// degrade the plan to "no dependencies for this mod" - the plan still
// succeeds - but unlike ErrNotSupported it is recorded into
// InstallPlan.DependencyWarnings so a caller can tell the user dependency
// resolution didn't actually run cleanly.
func TestService_PlanInstall_DependencyResolutionErrorRecordedAsWarning(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	mock := &failingDepsSource{mockSource: newMockSource("src")}
	svc.RegisterSource(mock)
	mock.AddMod("g1", &domain.Mod{ID: "root", SourceID: "src", Name: "Root", Version: "1.0", GameID: "g1"})

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "root", false)
	require.NoError(t, err, "a dependency-resolution failure must not fail the whole plan")
	assert.Empty(t, plan.Dependencies)
	assert.Empty(t, plan.MissingDependencies)
	assert.False(t, plan.CycleDetected)
	require.Len(t, plan.DependencyWarnings, 1)
	assert.Equal(t, "src", plan.DependencyWarnings[0].SourceID)
	assert.Equal(t, "root", plan.DependencyWarnings[0].ModID)
	assert.Contains(t, plan.DependencyWarnings[0].Message, errBoom.Error())
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
// caller can render its own auth hint - PlanInstall does not (and must not)
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
	_, err := pm.Create(context.Background(), game.ID, "default")
	require.NoError(t, err)

	// Unrelated pre-existing state to prove untouched.
	seedInstalledMod(t, svc, game, "src", "existing", "1.0", true, map[string][]byte{"existing.esp": []byte("e")})
	installer := svc.GetInstallerForTest(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "existing", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))

	mock := newMockSourceWithDownloads("src") // no AddDownload: any download 404s
	defer mock.Close()
	svc.RegisterSource(mock)
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1",
		Dependencies: []domain.ModReference{{SourceID: "src", ModID: "dep1"}}})
	mock.AddMod("g1", &domain.Mod{ID: "dep1", SourceID: "src", Name: "Dep One", Version: "1.0", GameID: "g1"})

	beforeMods, err := svc.GetInstalledMods(context.Background(), "g1", "default")
	require.NoError(t, err)

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "mod1", false)
	require.NoError(t, err)
	require.NotNil(t, plan)

	afterMods, err := svc.GetInstalledMods(context.Background(), "g1", "default")
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

// oldFileSource serves two files: the primary/latest (v1.5) and an archived
// older file (v1.0) - mockSource's default GetModFiles (a single, Version-
// less file) is overridden so a test can exercise installing the non-primary
// file explicitly, mirroring the CLI --file path (cmd/lmm/install.go:
// 497-513 overwrites plan.Files with a caller-picked file after PlanInstall)
// - see TestApplyInstall_ExplicitOldFile_RecordsFileVersionAndCacheKey (#94).
type oldFileSource struct {
	*mockSourceWithDownloads
}

func (s *oldFileSource) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	return []domain.DownloadableFile{
		{ID: "1", Name: "Main File", FileName: mod.ID + ".zip", Version: "1.5", IsPrimary: true},
		{ID: "2", Name: "Old File", FileName: mod.ID + "-old.zip", Version: "1.0"},
	}, nil
}

// versionOverrideFileSource is oldFileSource's BATCH-path counterpart: like
// perModFileSource, its single served file's ID is the mod's own ID (so
// distinct mods - e.g. a dependency and its primary - each get an
// unambiguous download key and can be installed together via
// applyInstallBatchMod), but the file's Version is looked up per mod.ID in
// fileVersions rather than left blank, so a test can diverge it from the
// mod's own Version field - see
// TestApplyInstall_ExplicitOldFile_BatchPath_RecordsFileVersion (#94).
type versionOverrideFileSource struct {
	*mockSourceWithDownloads
	fileVersions map[string]string // mod.ID -> served file's Version
}

func (s *versionOverrideFileSource) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	return []domain.DownloadableFile{
		{ID: mod.ID, Name: mod.Name, FileName: mod.ID + ".zip", Version: s.fileVersions[mod.ID], IsPrimary: true},
	}, nil
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

	assert.Equal(t, []string{"Mod One"}, installedRefNames(result.Installed))
	assert.Empty(t, result.Warnings)
	assert.Empty(t, result.Notes)
	assert.Empty(t, result.Skipped)
	assert.Equal(t, 1, result.FilesDeployed)

	_, err = os.Lstat(filepath.Join(gameDir, "mod1.esp"))
	assert.NoError(t, err, "file should be deployed to the game directory")

	installed, err := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "g1", installed.GameID, "GameID must be normalized to the lmm game, not a source-mapped value")
	assert.True(t, installed.Enabled)
	assert.True(t, installed.Deployed)
	assert.Equal(t, domain.UpdateNotify, installed.UpdatePolicy)
	assert.Equal(t, domain.LinkSymlink, installed.LinkMethod)

	files, err := svc.GetFilesWithChecksums(context.Background(), "g1", "default")
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.NotEmpty(t, files[0].Checksum, "the downloaded file's checksum must be saved")

	pm := svc.NewProfileManager()
	profile, err := pm.Get(context.Background(), "g1", "default")
	require.NoError(t, err, "the profile must have been created since it didn't exist yet")
	require.Len(t, profile.Mods, 1)
	assert.Equal(t, "mod1", profile.Mods[0].ModID)
}

// TestService_ApplyInstall_KeepCacheReinstall_SavesChecksumFromCache pins the
// coordinator's Important 2 ruling on the task-8 review: fillPrimaryCache's
// cache-first guard (2026-08-29) is evaluated on EVERY STRICT install, not
// only the conflict accept re-run, so `lmm uninstall --keep-cache` followed
// by a plain `lmm install` also finds the cache warm and skips the download
// - and before this fix skipped computing a checksum right along with it,
// leaving the new DB row (installed_mod_files is recreated from scratch by
// SaveInstalledMod/replaceModFileIDsTx - the old row, checksum included, is
// gone) with nothing to report until the next `verify --fix` re-ingests.
// RED on d6e826e: files[0].Checksum is empty after the reinstall.
func TestService_ApplyInstall_KeepCacheReinstall_SavesChecksumFromCache(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	mock := &perModFileSource{mockSourceWithDownloads: newMockSourceWithDownloads("src")}
	defer mock.Close()
	svc.RegisterSource(mock)
	registerDownloadableMod(t, mock, &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"}, "mod1.esp", "payload")

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "mod1", false)
	require.NoError(t, err)
	_, err = svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, nil)
	require.NoError(t, err)
	require.Equal(t, 1, mock.DownloadCount(), "sanity: the first install actually downloaded")

	_, err = svc.UninstallMod(context.Background(), game, "default", "src", "mod1", core.UninstallOptions{KeepCache: true})
	require.NoError(t, err)
	require.True(t, svc.GetGameCache(game).HasFileIDs("g1", "src", "mod1", "1.0", []string{"mod1"}), "sanity: --keep-cache must leave the cache entry complete")

	plan2, err := svc.PlanInstall(context.Background(), game, "default", "src", "mod1", false)
	require.NoError(t, err)

	result, err := svc.ApplyInstall(context.Background(), game, plan2, core.InstallOptions{}, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"Mod One"}, installedRefNames(result.Installed))
	assert.Equal(t, 1, mock.DownloadCount(), "the warm cache must be reused, not re-downloaded")

	files, err := svc.GetFilesWithChecksums(context.Background(), "g1", "default")
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.NotEmpty(t, files[0].Checksum, "a cached install must still save a checksum, not leave `verify` reporting NO CHECKSUM")
}

// TestService_ApplyInstall_KeepCacheReinstall_VerifyReportsOk pins the
// invariant that keeps checksumFromCache's fallback VALUE inert (unit P
// review, Minor 5): verify never compares checksum values, only their
// presence. The warm-fill path records digestDirectoryMembers' path+member
// fold for a plain extracted mod, which is deliberately NOT the archive-level
// md5 a fresh download records - so if verify ever grew a value comparison,
// every cache-warm install would be flagged. This is the test that would go
// red the day it does; see verify's own doc comment for the contract.
func TestService_ApplyInstall_KeepCacheReinstall_VerifyReportsOk(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	require.NoError(t, svc.SaveGame(context.Background(), game))

	mock := &perModFileSource{mockSourceWithDownloads: newMockSourceWithDownloads("src")}
	defer mock.Close()
	svc.RegisterSource(mock)
	registerDownloadableMod(t, mock, &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"}, "mod1.zip", "payload")

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "mod1", false)
	require.NoError(t, err)
	_, err = svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, nil)
	require.NoError(t, err)

	fresh, err := svc.GetFilesWithChecksums(context.Background(), "g1", "default")
	require.NoError(t, err)
	require.Len(t, fresh, 1)
	require.NotEmpty(t, fresh[0].Checksum, "sanity: the cold install recorded the download's own checksum")

	_, err = svc.UninstallMod(context.Background(), game, "default", "src", "mod1", core.UninstallOptions{KeepCache: true})
	require.NoError(t, err)

	plan2, err := svc.PlanInstall(context.Background(), game, "default", "src", "mod1", false)
	require.NoError(t, err)
	_, err = svc.ApplyInstall(context.Background(), game, plan2, core.InstallOptions{}, nil)
	require.NoError(t, err)
	require.Equal(t, 1, mock.DownloadCount(), "sanity: the reinstall read the cache warm")

	warm, err := svc.GetFilesWithChecksums(context.Background(), "g1", "default")
	require.NoError(t, err)
	require.Len(t, warm, 1)
	require.NotEmpty(t, warm[0].Checksum)
	require.NotEqual(t, fresh[0].Checksum, warm[0].Checksum,
		"fixture must exercise the divergence: a plain extracted entry has no retained original to re-hash, so the warm value is digestDirectoryMembers' fold, not the archive-level md5")

	report, err := svc.VerifyReport(context.Background(), game, "default", core.VerifyOptions{Tier: core.VerifyLocal}, nil)
	require.NoError(t, err)
	require.Equal(t, []core.VerifyFinding{
		{ModID: "mod1", ModName: "Mod One", FileID: "mod1", Status: "ok"},
	}, report.Result.Findings, "a cache-warm install must verify clean: presence is all verify checks")
	assert.Zero(t, report.Result.Issues)
	assert.Zero(t, report.Result.Warnings)
}

// TestService_ApplyInstall_MultiFileMod_FilesDeployedCountsTheEntryOnce
// pins InstallResult.FilesDeployed for a mod installed from more than one
// archive (unit P review, Minor 6; task-8 review Minor 3). The cold path
// used to ACCUMULATE downloadModToCache's own FilesExtracted per file, and
// that value is itself the whole cache ENTRY's listing on the extract
// branch - so a 2-file mod counted a + (a+b) = 3 for 2 deployed files, while
// the warm branch's single ListFiles reported the correct 2. files_deployed
// is a json-tagged contract field, so both paths now report the entry's own
// count.
//
// RED before the fix: 3 on the cold pass, 2 on the warm one.
func TestService_ApplyInstall_MultiFileMod_FilesDeployedCountsTheEntryOnce(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	// Two ARCHIVES, one member each: the extract branch is the one whose
	// FilesExtracted counts the whole entry rather than its own members, so
	// a copy-branch (.esp) fixture would not reproduce the over-count.
	src := &multiFileDownloadSource{
		mockSourceWithDownloads: newMockSourceWithDownloads("src"),
		files: []domain.DownloadableFile{
			{ID: "f1", Name: "File 1", FileName: "mod1-f1.zip", IsPrimary: true},
			{ID: "f2", Name: "File 2", FileName: "mod1-f2.zip"},
		},
	}
	defer src.Close()
	svc.RegisterSource(src)
	src.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"})
	for _, f := range []struct{ id, member string }{{"f1", "one.esp"}, {"f2", "two.esp"}} {
		zipBytes, err := os.ReadFile(createTestZip(t, t.TempDir(), map[string]string{f.member: f.id + "-payload"}))
		require.NoError(t, err)
		src.AddDownload(f.id, zipBytes)
	}

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "mod1", false)
	require.NoError(t, err)
	plan.Files = src.files // both files, as the CLI's --file/picker path sets it

	result, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, nil)
	require.NoError(t, err)

	cached, err := svc.GetGameCache(game).ListFiles("g1", "src", "mod1", "1.0")
	require.NoError(t, err)
	require.Len(t, cached, 2, "sanity: the entry holds one member per archive")
	assert.Equal(t, 2, result.FilesDeployed, "the cold fill must report the entry's file count, not the running sum of per-file listings")

	// The warm fill's own count, for the same entry.
	_, err = svc.UninstallMod(context.Background(), game, "default", "src", "mod1", core.UninstallOptions{KeepCache: true})
	require.NoError(t, err)
	plan2, err := svc.PlanInstall(context.Background(), game, "default", "src", "mod1", false)
	require.NoError(t, err)
	plan2.Files = src.files

	warm, err := svc.ApplyInstall(context.Background(), game, plan2, core.InstallOptions{}, nil)
	require.NoError(t, err)
	assert.Equal(t, 2, warm.FilesDeployed, "the warm fill already reported the entry's count; the two must agree")
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

	seedHooks(t, svc, game, "default", domain.GameHooks{Install: domain.HookConfig{
		BeforeAll: beforeAllScript, BeforeEach: beforeEachScript,
		AfterEach: afterEachScript, AfterAll: afterAllScript,
	}})

	result, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, nil)
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

	sink, seen := core.RecordEvents()
	result, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, sink)
	require.NoError(t, err, "a checksum-save failure must not fail the whole install")
	require.NotNil(t, result)
	assert.Equal(t, []string{"Mod One"}, installedRefNames(result.Installed))

	require.Len(t, result.Warnings, 1)
	assert.True(t, strings.HasPrefix(result.Warnings[0], "failed to save checksum for file mod1: "), "got: %s", result.Warnings[0])
	assert.Contains(t, result.Warnings[0], "blocked for test")
	assert.NotContains(t, result.Warnings[0], "Warning:", "the Warnings entry itself must not carry a baked-in prefix - the caller's printer adds it")

	var warningEvt *core.WarningEvent
	for _, e := range *seen {
		if w, ok := e.(core.WarningEvent); ok && w.Phase == core.InstallWarning {
			warningEvt = &w
		}
	}
	require.NotNil(t, warningEvt, "an InstallWarning event must fire for the checksum-save failure")
	assert.Equal(t, result.Warnings[0], warningEvt.Message)
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

	assert.Equal(t, []string{"Dep Two", "Dep One", "Root"}, installedRefNames(result.Installed), "dependencies must install before the primary, deepest first")
	assert.Empty(t, result.Skipped)

	for _, id := range []string{"dep2", "dep1", "root"} {
		_, err := svc.GetInstalledMod(context.Background(), "src", id, "g1", "default")
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
		installer := svc.GetInstallerForTest(game)
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
		assert.Equal(t, []string{"Mod One"}, installedRefNames(result.Installed))

		content, err := os.ReadFile(filepath.Join(gameDir, "mod1.esp"))
		require.NoError(t, err)
		assert.Equal(t, "new-content", string(content), "the reinstalled content must replace the old deployed file")
	})

	t.Run("version upgrade replaces old cache and deployed files", func(t *testing.T) {
		svc := newFlowsTestService(t)
		gameDir := t.TempDir()
		game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

		seedInstalledMod(t, svc, game, "src", "mod1", "1.0", true, map[string][]byte{"mod1-old.esp": []byte("old-content")})
		installer := svc.GetInstallerForTest(game)
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
		assert.Equal(t, []string{"Mod One"}, installedRefNames(result.Installed))

		_, err = os.Lstat(filepath.Join(gameDir, "mod1-old.esp"))
		assert.True(t, os.IsNotExist(err), "old version's file must be undeployed")
		_, err = os.Lstat(filepath.Join(gameDir, "mod1-new.esp"))
		assert.NoError(t, err, "new version's file must be deployed")

		assert.False(t, svc.GetGameCache(game).Exists("g1", "src", "mod1", "1.0"), "old version's cache entry should be cleared after a version upgrade")
		assert.True(t, svc.GetGameCache(game).Exists("g1", "src", "mod1", "2.0"))

		installed, err := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
		require.NoError(t, err)
		assert.Equal(t, "2.0", installed.Version)
	})

	t.Run("same-version reinstall whose download fails leaves the original deployed content untouched", func(t *testing.T) {
		svc := newFlowsTestService(t)
		gameDir := t.TempDir()
		game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

		seedInstalledMod(t, svc, game, "src", "mod1", "1.0", true, map[string][]byte{"mod1.esp": []byte("original-content")})
		installer := svc.GetInstallerForTest(game)
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

		installed, err := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
		require.NoError(t, err)
		assert.Equal(t, "1.0", installed.Version, "DB row must be unchanged")
	})

	// This subtest pins that prepareReinstallCacheTransaction's ephemeral
	// snapshot/staged caches (flows.go) are wired to the service's own
	// logger via SetLogger, not left on cache.New's silent-discard default
	// (#284). Neither cache.Cache method the reinstall path actually calls
	// logs anything on a clean run, so the only observable signal is
	// Cache.Exists' "stat failed" Debug line on a genuine stat error -
	// forced here by making the snapshot's version directory unreadable
	// (parent chmod 000) between prepare and the deploy step's
	// oldCache.Exists check, via an InstallDeploying sink hook (mirroring
	// TestService_ApplyInstall_SameVersionReinstall_CancelledMidDeploy_RestoresLiveCache's
	// TMPDIR + sink-hook technique). A wired logger sees the debug line; the
	// pre-fix code (cache.New's default discard logger) would see nothing.
	t.Run("same-version reinstall wires the transaction's caches to the service logger", func(t *testing.T) {
		if os.Getuid() == 0 {
			t.Skip("permission checks are bypassed when running as root")
		}
		tmpRoot := t.TempDir()
		t.Setenv("TMPDIR", tmpRoot) // where the transaction's snapshot temp dir lands

		var logBuf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
		svc, err := core.NewService(core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(), Logger: logger})
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, svc.Close()) })
		gameDir := t.TempDir()
		game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

		seedInstalledMod(t, svc, game, "src", "mod1", "1.0", true, map[string][]byte{"mod1.esp": []byte("old-content")})
		installer := svc.GetInstallerForTest(game)
		require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "mod1", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))

		mock := &perModFileSource{mockSourceWithDownloads: newMockSourceWithDownloads("src")}
		defer mock.Close()
		svc.RegisterSource(mock)
		registerDownloadableMod(t, mock, &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"}, "mod1.esp", "new-content")

		plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "mod1", false)
		require.NoError(t, err)
		require.NotNil(t, plan.Replaces)
		require.Equal(t, "1.0", plan.Replaces.Version, "a same-version reinstall - the reinstall-cache-transaction path")

		var lockedDir string
		_, err = svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, func(e core.Event) {
			fe, ok := e.(core.FlowEvent)
			if !ok || fe.FlowPhase() != core.InstallDeploying {
				return
			}
			entries, rerr := os.ReadDir(tmpRoot)
			require.NoError(t, rerr)
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), "lmm-reinstall-cache-") {
					// The parent of the version dir, not the version dir
					// itself: a stat needs execute (search) permission on
					// every ANCESTOR to resolve the target, not on the
					// target itself, so chmod 000 has to land one level up.
					lockedDir = filepath.Join(tmpRoot, entry.Name(), "snapshot", "g1", "src-mod1")
					require.NoError(t, os.Chmod(lockedDir, 0o000))
				}
			}
		})
		t.Cleanup(func() {
			if lockedDir != "" {
				_ = os.Chmod(lockedDir, 0o755)
			}
		})
		require.Error(t, err, "the forced stat failure makes the snapshot read as missing, so ReplaceWithOldCache refuses to proceed")
		require.NotEmpty(t, lockedDir, "the sink must have found and locked the transaction's snapshot dir")
		assert.Contains(t, logBuf.String(), "stat failed while checking cache entry",
			"the transaction's snapshot cache must log through the service logger, not cache.New's default discard")
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
		_, dbErr := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
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
		assert.Equal(t, []string{"Root"}, installedRefNames(result.Installed))
		require.Len(t, result.Skipped, 1)
		assert.Equal(t, "Dep One", result.Skipped[0].Name)
		assert.Contains(t, result.Skipped[0].Reason, "download failed")

		_, err = svc.GetInstalledMod(context.Background(), "src", "dep1", "g1", "default")
		assert.Error(t, err, "the failed dependency must not be saved")
		_, err = svc.GetInstalledMod(context.Background(), "src", "root", "g1", "default")
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

	sink, seen := core.RecordEvents()
	result, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, sink)
	require.NoError(t, err)
	require.NotNil(t, result)

	var sawStarted, sawDownloading, sawDone, sawInstalled bool
	for _, e := range *seen {
		switch ev := e.(type) {
		case core.StepEvent:
			switch ev.Phase {
			case core.InstallDownloadStarted:
				sawStarted = true
				assert.Equal(t, "Mod One", ev.ModName)
				require.NotNil(t, ev.File)
			case core.InstallDownloadDone:
				sawDone = true
			}
		case core.DownloadEvent:
			if ev.Phase == core.InstallDownloading {
				sawDownloading = true
				assert.GreaterOrEqual(t, ev.Percent, 0.0)
				assert.Greater(t, ev.TotalBytes, int64(0))
			}
		case core.ModEvent:
			if ev.Phase == core.InstallDone {
				sawInstalled = true
				assert.Equal(t, "Mod One", ev.ModName)
			}
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

// setupThreeFileInstall is TestService_ApplyInstall_ProgressEvents' fixture
// with a 3-file mod (multiFileDownloadSource, as
// TestService_ApplyUpdate_ProgressEvents uses for its own multi-file source)
// so a per-file cancellation test has more than one file to observe.
func setupThreeFileInstall(t *testing.T) (*core.Service, *domain.Game, *multiFileDownloadSource) {
	t.Helper()
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	src := &multiFileDownloadSource{
		mockSourceWithDownloads: newMockSourceWithDownloads("src"),
		files: []domain.DownloadableFile{
			{ID: "f1", Name: "File 1", FileName: "mod-1-f1.esp", IsPrimary: true},
			{ID: "f2", Name: "File 2", FileName: "mod-1-f2.esp"},
			{ID: "f3", Name: "File 3", FileName: "mod-1-f3.esp"},
		},
	}
	svc.RegisterSource(src)
	src.AddMod("g1", &domain.Mod{ID: "mod-1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"})
	src.AddDownload("f1", []byte("f1-content"))
	src.AddDownload("f2", []byte("f2-content"))
	src.AddDownload("f3", []byte("f3-content"))

	return svc, game, src
}

// TestService_ApplyInstall_ContextCancelledBetweenPrimaryFiles pins that the
// per-file download loop in fillPrimaryCache checks ctx at each iteration:
// with three selected files and a ctx cancelled once file 1 is fully
// downloaded, file 2's iteration never starts.
//
// The loop GUARD has to be the thing that stops the flow, or the test pins
// nothing (final-review Important 2: the first cut of this test still passed
// with the guard deleted, because a cancelled ctx makes the HTTP transport
// refuse the next request all by itself and the assertions could not tell
// the two apart). Two changes make it load-bearing:
//
//   - Cancellation fires from the SINK, on file 1's InstallDownloadDone -
//     emitted after DownloadModToCache has returned successfully and before
//     the loop head is reached again. File 1 therefore always completes for
//     real, so a failure can only come from the next iteration. (The old
//     server-side hook cancelled while file 1's own response was still in
//     flight, which could abort file 1 instead.)
//   - The assertions observe what a RUN iteration does before it touches the
//     network at all: the InstallDownloadStarted event fillPrimaryCache
//     emits at the top of the body, and the source's GetDownloadURL call
//     DownloadModToCache makes first. Delete the guard and both fire for
//     file 2 no matter what the transport then does.
func TestService_ApplyInstall_ContextCancelledBetweenPrimaryFiles(t *testing.T) {
	svc, game, src := setupThreeFileInstall(t)
	ctx, cancel := context.WithCancel(context.Background())

	var started []int
	var failed int
	sink := func(e core.Event) {
		fe, ok := e.(core.FlowEvent)
		if !ok {
			return
		}
		switch fe.FlowPhase() {
		case core.InstallDownloadStarted:
			started = append(started, fe.EventScope().Index)
		case core.InstallDownloadFailed:
			failed++
		case core.InstallDownloadDone:
			if fe.EventScope().Index == 1 {
				cancel() // file 1's bytes are cached; the loop head is next
			}
		}
	}

	plan, err := svc.PlanInstall(ctx, game, "default", src.ID(), "mod-1", false)
	require.NoError(t, err)
	_, err = svc.ApplyInstall(ctx, game, plan, core.InstallOptions{TargetFileIDs: []string{"f1", "f2", "f3"}}, sink)
	require.ErrorIs(t, err, context.Canceled)

	assert.Zero(t, failed, "file 1 must have downloaded successfully - the cancellation is meant to land BETWEEN files")
	assert.Equal(t, []int{1}, started, "file 2's iteration must never start: the loop head, not the transport, has to stop it")
	assert.Equal(t, int64(1), src.urlRequests.Load(), "a skipped iteration never even asks the source for file 2's URL")
	assert.Equal(t, int64(1), src.downloads.Load(), "second file must not be requested after cancellation")
}

// TestService_ApplyInstall_BeforeAllHookFailure mirrors
// TestService_DeployProfile's before_all Force-gate pattern: fatal without
// Force, a recorded (forced) Warning with Force, matching doInstall exactly
// (before_all only ever runs once, regardless of Dependencies).
func TestService_ApplyInstall_BeforeAllHookFailure(t *testing.T) {
	scriptsDir := t.TempDir()
	failScript := createTestScript(t, scriptsDir, "before_all.sh", "#!/bin/bash\necho boom >&2\nexit 1\n")

	newPlan := func(t *testing.T) (*core.Service, *domain.Game, *core.InstallPlan) {
		svc := newFlowsTestService(t)
		gameDir := t.TempDir()
		game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}
		mock := &perModFileSource{mockSourceWithDownloads: newMockSourceWithDownloads("src")}
		t.Cleanup(mock.Close)
		svc.RegisterSource(mock)
		registerDownloadableMod(t, mock, &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"}, "mod1.esp", "payload")
		seedHooks(t, svc, game, "default", domain.GameHooks{Install: domain.HookConfig{BeforeAll: failScript}})
		plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "mod1", false)
		require.NoError(t, err)
		return svc, game, plan
	}

	t.Run("fatal without Force", func(t *testing.T) {
		svc, game, plan := newPlan(t)
		result, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "install.before_all hook failed")
		require.NotNil(t, result)
		assert.Empty(t, result.Installed)
		_, dbErr := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
		assert.Error(t, dbErr)
	})

	t.Run("forced continues with a warning", func(t *testing.T) {
		svc, game, plan := newPlan(t)
		result, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{Force: true}, nil)
		require.NoError(t, err)
		require.NotNil(t, result)
		require.Len(t, result.Warnings, 1)
		assert.Contains(t, result.Warnings[0], "install.before_all hook failed")
		assert.Contains(t, result.Warnings[0], "forced")
		assert.Equal(t, []string{"Mod One"}, installedRefNames(result.Installed))
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

	newPlan := func(t *testing.T) (*core.Service, *domain.Game, *core.InstallPlan) {
		svc := newFlowsTestService(t)
		gameDir := t.TempDir()
		game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}
		mock := &perModFileSource{mockSourceWithDownloads: newMockSourceWithDownloads("src")}
		t.Cleanup(mock.Close)
		svc.RegisterSource(mock)
		registerDownloadableMod(t, mock, &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"}, "mod1.esp", "payload")
		seedHooks(t, svc, game, "default", domain.GameHooks{Install: domain.HookConfig{BeforeEach: failScript}})
		plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "mod1", false)
		require.NoError(t, err)
		return svc, game, plan
	}

	t.Run("fatal without Force", func(t *testing.T) {
		svc, game, plan := newPlan(t)
		result, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "install.before_each hook failed")
		require.NotNil(t, result)
		assert.Empty(t, result.Installed)
	})

	t.Run("forced continues with a warning", func(t *testing.T) {
		svc, game, plan := newPlan(t)
		result, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{Force: true}, nil)
		require.NoError(t, err)
		require.NotNil(t, result)
		require.Len(t, result.Warnings, 1)
		assert.Contains(t, result.Warnings[0], "install.before_each hook failed")
		assert.Contains(t, result.Warnings[0], "forced")
		assert.Equal(t, []string{"Mod One"}, installedRefNames(result.Installed))
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
	seedHooks(t, svc, game, "default", domain.GameHooks{Install: domain.HookConfig{BeforeEach: failScript}})

	result, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, nil)
	require.NoError(t, err, "a dependency's before_each failure must never fail the whole install, even without Force")
	require.NotNil(t, result)
	assert.Equal(t, []string{"Root"}, installedRefNames(result.Installed))
	require.Len(t, result.Skipped, 1)
	assert.Equal(t, "Dep One", result.Skipped[0].Name)
	assert.Contains(t, result.Skipped[0].Reason, "install.before_each hook failed")
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
	assert.Equal(t, []string{"Mod One"}, installedRefNames(result.Installed))

	_, err = os.Lstat(filepath.Join(gameDir, "main.esp"))
	assert.NoError(t, err, "main file must be installed")
	_, err = os.Lstat(filepath.Join(gameDir, "optional.esp"))
	assert.NoError(t, err, "the caller-added optional file must ALSO be installed - ApplyInstall must install exactly plan.Files, not re-select")

	installed, err := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"main", "optional"}, installed.FileIDs)
}

// TestApplyInstall_ExplicitOldFile_RecordsFileVersionAndCacheKey is the #94
// regression test: installing an explicitly-picked non-primary (older) file
// must record THAT FILE's version - not the mod's own (newer) Version - in
// the DB row and the cache directory key, so a subsequent CheckUpdates still
// offers the real update instead of the old file silently masquerading as
// current. Mirrors the CLI --file path (cmd/lmm/install.go:497-513), which
// overwrites plan.Files with the caller-picked file after PlanInstall.
func TestApplyInstall_ExplicitOldFile_RecordsFileVersionAndCacheKey(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	mock := &oldFileSource{mockSourceWithDownloads: newMockSourceWithDownloads("src")}
	defer mock.Close()
	svc.RegisterSource(mock)

	mod := &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.5", GameID: "g1"}
	mock.AddMod(mod.GameID, mod)

	oldZip := createTestZip(t, t.TempDir(), map[string]string{"mod1.esp": "old-payload"})
	oldContent, err := os.ReadFile(oldZip)
	require.NoError(t, err)
	mock.AddDownload("2", oldContent)

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "mod1", false)
	require.NoError(t, err)
	require.Len(t, plan.Files, 1, "PlanInstall's own default picks just the primary file")
	assert.Equal(t, "1", plan.Files[0].ID)

	// Caller (CLI's --file override) picks the archived old file instead.
	plan.Files = []domain.DownloadableFile{
		{ID: "2", Name: "Old File", FileName: "mod1-old.zip", Version: "1.0"},
	}

	result, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, []string{"Mod One"}, installedRefNames(result.Installed))

	got, err := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0", got.Version, "DB row must record the selected file's version, not the mod's latest")

	gameCache := svc.GetGameCache(game)
	assert.True(t, gameCache.Exists("g1", "src", "mod1", "1.0"), "cache must be keyed by the installed file's version")
	assert.False(t, gameCache.Exists("g1", "src", "mod1", "1.5"), "cache must NOT be keyed by the mod's latest version when an older file was installed")

	// The user-visible symptom: installing an older file must not suppress
	// its own update notification. domain.IsNewerVersion backs CheckUpdates'
	// comparison for sources that use it directly.
	assert.True(t, domain.IsNewerVersion(got.Version, "1.5"))

	// Confirm at the CheckUpdates level too, via a source mock whose
	// CheckUpdates is actually wired up (mockSource's always returns nil).
	registry := source.NewRegistry()
	updateSrc := &updateMockSource{
		id: "src",
		currentMod: &domain.Mod{
			ID:       "mod1",
			SourceID: "src",
			Name:     "Mod One",
			Version:  "1.5",
			GameID:   "g1",
		},
	}
	registry.Register(updateSrc)
	updater := core.NewUpdater(registry)

	updates, err := updater.CheckUpdates(context.Background(), game, []domain.InstalledMod{*got}, nil)
	require.NoError(t, err)
	require.Len(t, updates, 1)
	assert.Equal(t, "1.5", updates[0].NewVersion)
}

// TestApplyInstall_ExplicitOldFile_BeforeEachHookSeesEffectiveVersion pins
// the other observable consequence of the #94 stamp: fillPrimaryCache
// sets hookCtx.ModVersion (flows.go, right after the stamp) from the SAME
// now-effective mod.Version, so install.before_each - and therefore the
// LMM_MOD_VERSION env var a hook script sees (hooks.go's Run) - reports the
// file actually being installed ("1.0"), not the mod's own latest version
// ("1.5"). Mirrors TestService_ApplyUpdate_HookOrder's callLog pattern
// (flows_update_test.go) for capturing hook context via a real script.
func TestApplyInstall_ExplicitOldFile_BeforeEachHookSeesEffectiveVersion(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	scriptsDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	mock := &oldFileSource{mockSourceWithDownloads: newMockSourceWithDownloads("src")}
	defer mock.Close()
	svc.RegisterSource(mock)

	mod := &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.5", GameID: "g1"}
	mock.AddMod(mod.GameID, mod)

	oldZip := createTestZip(t, t.TempDir(), map[string]string{"mod1.esp": "old-payload"})
	oldContent, err := os.ReadFile(oldZip)
	require.NoError(t, err)
	mock.AddDownload("2", oldContent)

	callLog := filepath.Join(scriptsDir, "calls.log")
	beforeEach := createTestScript(t, scriptsDir, "before_each.sh", `#!/bin/bash
echo "install.before_each:$LMM_MOD_ID:$LMM_MOD_VERSION" >> `+callLog+`
exit 0`)
	seedHooks(t, svc, game, "default", domain.GameHooks{Install: domain.HookConfig{BeforeEach: beforeEach}})

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "mod1", false)
	require.NoError(t, err)

	// Caller (CLI's --file override) picks the archived old file instead.
	plan.Files = []domain.DownloadableFile{
		{ID: "2", Name: "Old File", FileName: "mod1-old.zip", Version: "1.0"},
	}

	result, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, []string{"Mod One"}, installedRefNames(result.Installed))

	logContent, err := os.ReadFile(callLog)
	require.NoError(t, err)
	assert.Equal(t, "install.before_each:mod1:1.0\n", string(logContent),
		"install.before_each must see the effective (selected-file) version, not the mod-level 1.5")
}

// TestApplyInstall_ExplicitOldFile_BatchPath_RecordsFileVersion is the #94
// regression test for the BATCH path (applyInstallBatchMod), which installs
// dependencies AND the primary identically and - unlike fillPrimaryCache
// - always re-resolves its own file from the source rather than consulting
// plan.Files. A dependency mod's source reports a mod-level Version ("2.0")
// that differs from its actually-served primary file's Version ("2.0.1");
// the DB row and cache dir must carry the file's version, matching
// TestApplyInstall_ExplicitOldFile_RecordsFileVersionAndCacheKey's PRIMARY
// path assertion but exercised via a dependency install instead.
func TestApplyInstall_ExplicitOldFile_BatchPath_RecordsFileVersion(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	mock := &versionOverrideFileSource{
		mockSourceWithDownloads: newMockSourceWithDownloads("src"),
		fileVersions:            map[string]string{"dep1": "2.0.1", "root": "1.0"},
	}
	defer mock.Close()
	svc.RegisterSource(mock)

	dep1 := &domain.Mod{ID: "dep1", SourceID: "src", Name: "Dep One", Version: "2.0", GameID: "g1"}
	root := &domain.Mod{ID: "root", SourceID: "src", Name: "Root", Version: "1.0", GameID: "g1",
		Dependencies: []domain.ModReference{{SourceID: "src", ModID: "dep1"}}}

	depZip := createTestZip(t, t.TempDir(), map[string]string{"dep1.esp": "dep-payload"})
	depContent, err := os.ReadFile(depZip)
	require.NoError(t, err)
	mock.AddDownload("dep1", depContent)
	mock.AddMod(dep1.GameID, dep1)

	rootZip := createTestZip(t, t.TempDir(), map[string]string{"root.esp": "root-payload"})
	rootContent, err := os.ReadFile(rootZip)
	require.NoError(t, err)
	mock.AddDownload("root", rootContent)
	mock.AddMod(root.GameID, root)

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "root", false)
	require.NoError(t, err)
	require.Len(t, plan.Dependencies, 1)

	result, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, []string{"Dep One", "Root"}, installedRefNames(result.Installed))

	got, err := svc.GetInstalledMod(context.Background(), "src", "dep1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "2.0.1", got.Version, "the dependency's DB row must record the selected file's version, not the mod's mod-level 2.0")

	gameCache := svc.GetGameCache(game)
	assert.True(t, gameCache.Exists("g1", "src", "dep1", "2.0.1"), "cache must be keyed by the installed file's version")
	assert.False(t, gameCache.Exists("g1", "src", "dep1", "2.0"), "cache must NOT be keyed by the mod's mod-level version when the file's own version differs")
}

// --- #143: locked-ref install guard ---

// lockProfileRef seeds profileName with a ref for sourceID/modID at version
// (with fileIDs) and locks it - the profile-side state every #143 install
// guard test starts from.
func lockProfileRef(t *testing.T, svc *core.Service, gameID, profileName, sourceID, modID, version string, fileIDs []string) {
	t.Helper()
	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), gameID, profileName)
	require.NoError(t, err)
	require.NoError(t, pm.UpsertMod(context.Background(), gameID, profileName, domain.ModReference{
		SourceID: sourceID, ModID: modID, Version: version, FileIDs: fileIDs,
	}))
	require.NoError(t, pm.SetModLock(context.Background(), gameID, profileName, sourceID, modID, ""))
}

// TestService_ApplyInstall_LockedRefDifferentVersion_RefusedBeforeAnySideEffect
// is #143's STRICT-path flow guard: installing a mod whose profile ref is
// LOCKED at a different version than the install would record must be
// refused up front - before any hook, download, deploy, or DB/profile write
// - with LockedRefRefusalError's remedy wording (errors.Is core.ErrModLocked).
// Previously the install deployed the new version and either silently moved
// the lock target (UpsertMod) or - with the core guard alone - left a
// deployed-but-unrecorded drift behind a mere Note.
func TestService_ApplyInstall_LockedRefDifferentVersion_RefusedBeforeAnySideEffect(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	mock := &oldFileSource{mockSourceWithDownloads: newMockSourceWithDownloads("src")}
	defer mock.Close()
	svc.RegisterSource(mock)

	mod := &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.5", GameID: "g1"}
	mock.AddMod(mod.GameID, mod)

	newZip := createTestZip(t, t.TempDir(), map[string]string{"mod1.esp": "new-payload"})
	newContent, err := os.ReadFile(newZip)
	require.NoError(t, err)
	mock.AddDownload("1", newContent)

	// The profile holds mod1 LOCKED at v1.0; the plan's primary file is v1.5.
	lockProfileRef(t, svc, "g1", "default", "src", "mod1", "1.0", []string{"2"})

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "mod1", false)
	require.NoError(t, err)
	require.Len(t, plan.Files, 1)
	assert.Equal(t, "1.5", plan.Files[0].Version)

	result, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, nil)
	require.Error(t, err, "installing a different version over a locked ref must be refused")
	assert.True(t, errors.Is(err, core.ErrModLocked), "the refusal must wrap core.ErrModLocked, got: %v", err)
	assert.Contains(t, err.Error(), "locked at v1.0")
	assert.Contains(t, err.Error(), "lmm mod lock -s src -p default mod1", "the refusal must carry the move-the-lock remedy")
	assert.Contains(t, err.Error(), "lmm mod unlock -s src -p default mod1", "the refusal must carry the unlock remedy")
	require.NotNil(t, result)
	assert.Empty(t, result.Installed)

	// Zero side effects: nothing downloaded, deployed, or recorded.
	assert.Equal(t, 0, mock.DownloadCount(), "the refusal must fire BEFORE any download")
	entries, err := os.ReadDir(gameDir)
	require.NoError(t, err)
	assert.Empty(t, entries, "nothing may be deployed to the game directory")
	_, err = svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
	assert.True(t, errors.Is(err, domain.ErrModNotFound), "no DB row may be written")

	profile, err := svc.NewProfileManager().Get(context.Background(), "g1", "default")
	require.NoError(t, err)
	ref := profile.FindRef("src", "mod1")
	require.NotNil(t, ref)
	assert.Equal(t, "1.0", ref.Version, "the locked version must be untouched")
	assert.Equal(t, []string{"2"}, ref.FileIDs, "the ref's FileIDs must be untouched")
	assert.True(t, ref.Locked)
}

// TestService_ApplyInstall_LockedRef_EmptyPlanFiles_NotRefusedAsLocked pins
// resolveInstallTargetVersion's documented ok=false contract on the STRICT
// path: with plan.Files emptied by a caller, no target version can be
// derived, so the up-front gate must NOT refuse with ErrModLocked on the
// mod-level fallback version it never actually derived - the flow's own
// handling (and the UpsertMod backstop, which still protects the ref) is
// authoritative for the degenerate shape.
func TestService_ApplyInstall_LockedRef_EmptyPlanFiles_NotRefusedAsLocked(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	mock := &oldFileSource{mockSourceWithDownloads: newMockSourceWithDownloads("src")}
	defer mock.Close()
	svc.RegisterSource(mock)

	mod := &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.5", GameID: "g1"}
	mock.AddMod(mod.GameID, mod)

	lockProfileRef(t, svc, "g1", "default", "src", "mod1", "1.0", []string{"2"})

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "mod1", false)
	require.NoError(t, err)
	plan.Files = nil

	_, err = svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, nil)
	assert.False(t, errors.Is(err, core.ErrModLocked),
		"an underivable target version (empty plan.Files) must not be refused as a lock conflict, got: %v", err)

	// The backstop still holds: the locked ref is untouched either way.
	profile, err := svc.NewProfileManager().Get(context.Background(), "g1", "default")
	require.NoError(t, err)
	ref := profile.FindRef("src", "mod1")
	require.NotNil(t, ref)
	assert.Equal(t, "1.0", ref.Version, "the locked version must be untouched")
	assert.True(t, ref.Locked)
}

// TestService_ApplyInstall_LockedRefExactVersion_Succeeds pins the converge/
// repair half of #143: installing a locked mod at EXACTLY its locked version
// stays allowed - the install completes, FileIDs refresh, and the lock
// marker survives.
func TestService_ApplyInstall_LockedRefExactVersion_Succeeds(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	mock := &oldFileSource{mockSourceWithDownloads: newMockSourceWithDownloads("src")}
	defer mock.Close()
	svc.RegisterSource(mock)

	mod := &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.5", GameID: "g1"}
	mock.AddMod(mod.GameID, mod)

	newZip := createTestZip(t, t.TempDir(), map[string]string{"mod1.esp": "payload"})
	newContent, err := os.ReadFile(newZip)
	require.NoError(t, err)
	mock.AddDownload("1", newContent)

	// The profile holds mod1 LOCKED at v1.5 - exactly the plan's primary file.
	lockProfileRef(t, svc, "g1", "default", "src", "mod1", "1.5", nil)

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "mod1", false)
	require.NoError(t, err)

	result, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, nil)
	require.NoError(t, err, "installing a locked mod at exactly its locked version is a legitimate converge/repair")
	require.NotNil(t, result)
	assert.Equal(t, []string{"Mod One"}, installedRefNames(result.Installed))
	assert.Empty(t, result.Notes, "the profile upsert must succeed, not demote to a Note")

	profile, err := svc.NewProfileManager().Get(context.Background(), "g1", "default")
	require.NoError(t, err)
	ref := profile.FindRef("src", "mod1")
	require.NotNil(t, ref)
	assert.Equal(t, "1.5", ref.Version)
	assert.Equal(t, []string{"1"}, ref.FileIDs, "FileIDs must refresh to the installed file")
	assert.True(t, ref.Locked, "the lock marker must survive the reinstall")
}

// TestService_ApplyInstall_LockedRef_BatchPath_RefusedBeforeDependencies is
// the BATCH-path (dependencies present) variant of the #143 guard: the
// primary's locked-ref refusal must abort the WHOLE install up front - zero
// dependencies installed, zero downloads - mirroring the #96 TargetVersion
// precedent ("abort the whole install rather than a per-mod Failed line
// after dependencies already installed").
func TestService_ApplyInstall_LockedRef_BatchPath_RefusedBeforeDependencies(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	mock := &versionOverrideFileSource{
		mockSourceWithDownloads: newMockSourceWithDownloads("src"),
		fileVersions:            map[string]string{"dep1": "1.0", "root": "2.0"},
	}
	defer mock.Close()
	svc.RegisterSource(mock)

	dep1 := &domain.Mod{ID: "dep1", SourceID: "src", Name: "Dep One", Version: "1.0", GameID: "g1"}
	root := &domain.Mod{ID: "root", SourceID: "src", Name: "Root", Version: "2.0", GameID: "g1",
		Dependencies: []domain.ModReference{{SourceID: "src", ModID: "dep1"}}}
	mock.AddMod(dep1.GameID, dep1)
	mock.AddMod(root.GameID, root)

	// The profile holds root LOCKED at v1.0; the batch would install v2.0.
	lockProfileRef(t, svc, "g1", "default", "src", "root", "1.0", nil)

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "root", false)
	require.NoError(t, err)
	require.Len(t, plan.Dependencies, 1)

	result, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, core.ErrModLocked), "the refusal must wrap core.ErrModLocked, got: %v", err)
	require.NotNil(t, result)
	assert.Empty(t, result.Installed, "no dependency may install before the primary's lock refusal")
	assert.Equal(t, 0, mock.DownloadCount(), "the refusal must fire BEFORE any download")
	_, err = svc.GetInstalledMod(context.Background(), "src", "dep1", "g1", "default")
	assert.True(t, errors.Is(err, domain.ErrModNotFound), "the dependency must not be installed")
}

// TestService_ApplyInstall_LockedDependency_BatchPath_SkippedNotMoved covers
// the batch loop's own #143 backstop for a DEPENDENCY whose profile ref is
// locked (a profile-YAML/DB drift state: only a ref absent from the DB still
// resolves as a dependency): the dependency is skipped - batch skip-and-
// continue semantics, like every other per-mod failure - BEFORE its download
// or deploy, its locked ref stays untouched, and the primary still installs.
func TestService_ApplyInstall_LockedDependency_BatchPath_SkippedNotMoved(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	mock := &versionOverrideFileSource{
		mockSourceWithDownloads: newMockSourceWithDownloads("src"),
		fileVersions:            map[string]string{"dep1": "2.0", "root": "3.0"},
	}
	defer mock.Close()
	svc.RegisterSource(mock)

	dep1 := &domain.Mod{ID: "dep1", SourceID: "src", Name: "Dep One", Version: "2.0", GameID: "g1"}
	root := &domain.Mod{ID: "root", SourceID: "src", Name: "Root", Version: "3.0", GameID: "g1",
		Dependencies: []domain.ModReference{{SourceID: "src", ModID: "dep1"}}}
	mock.AddMod(dep1.GameID, dep1)
	mock.AddMod(root.GameID, root)

	rootZip := createTestZip(t, t.TempDir(), map[string]string{"root.esp": "root-payload"})
	rootContent, err := os.ReadFile(rootZip)
	require.NoError(t, err)
	mock.AddDownload("root", rootContent)

	// dep1's ref is LOCKED at v1.0 in the profile but has NO DB row, so
	// PlanInstall still resolves it as a not-yet-installed dependency.
	lockProfileRef(t, svc, "g1", "default", "src", "dep1", "1.0", []string{"old"})

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "root", false)
	require.NoError(t, err)
	require.Len(t, plan.Dependencies, 1)

	result, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, nil)
	require.NoError(t, err, "a locked dependency skips - batch per-mod semantics, never fatal")
	require.NotNil(t, result)
	assert.Equal(t, []string{"Root"}, installedRefNames(result.Installed), "the primary must still install")
	assert.Equal(t, []string{"Dep One"}, installedRefNames(result.Failed))
	require.Len(t, result.Skipped, 1)
	assert.Equal(t, "Dep One", result.Skipped[0].Name)
	assert.Contains(t, result.Skipped[0].Reason, "locked at v1.0", "the skip reason must name the lock")

	// The locked dependency must be untouched: no DB row, no deploy, ref intact.
	_, err = svc.GetInstalledMod(context.Background(), "src", "dep1", "g1", "default")
	assert.True(t, errors.Is(err, domain.ErrModNotFound))
	_, err = os.Lstat(filepath.Join(gameDir, "dep1.esp"))
	assert.True(t, os.IsNotExist(err), "the locked dependency must not be deployed")

	profile, err := svc.NewProfileManager().Get(context.Background(), "g1", "default")
	require.NoError(t, err)
	ref := profile.FindRef("src", "dep1")
	require.NotNil(t, ref)
	assert.Equal(t, "1.0", ref.Version)
	assert.Equal(t, []string{"old"}, ref.FileIDs)
	assert.True(t, ref.Locked)
}

// flakyVersionedFileSource is versionOverrideFileSource with a failNext
// fuse: the next failNext GetModFiles calls fail with a transient error,
// then service resumes - so a test can fail EXACTLY the up-front
// lockedInstallRefusal derivation while the batch loop's own later calls
// succeed (#143 review finding F2).
type flakyVersionedFileSource struct {
	*mockSourceWithDownloads
	fileVersions map[string]string // mod.ID -> served file's Version
	failNext     int
}

func (s *flakyVersionedFileSource) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	if s.failNext > 0 {
		s.failNext--
		return nil, fmt.Errorf("transient: files endpoint unavailable")
	}
	return []domain.DownloadableFile{
		{ID: mod.ID, Name: mod.Name, FileName: mod.ID + ".zip", Version: s.fileVersions[mod.ID], IsPrimary: true},
	}, nil
}

// TestService_ApplyInstall_LockedPrimary_BatchPath_GuardFallthroughSkipsBeforeUninstall
// closes #143 review finding F2: when lockedInstallRefusal's up-front
// derivation fails transiently (ok=false), a locked, already-installed
// PRIMARY falls through to the batch loop - reachable whenever an installed
// primary has a missing dependency (BATCH path). The loop must then run its
// own lock check BEFORE the uninstall-existing block: previously the block
// uninstalled the deployed lock target and deleted its cache, and only THEN
// the post-selection lock check skipped - leaving the lock target
// undeployed and uncached while the profile still said locked v1.0.
func TestService_ApplyInstall_LockedPrimary_BatchPath_GuardFallthroughSkipsBeforeUninstall(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	mock := &flakyVersionedFileSource{
		mockSourceWithDownloads: newMockSourceWithDownloads("src"),
		fileVersions:            map[string]string{"dep1": "1.0", "root": "2.0"},
	}
	defer mock.Close()
	svc.RegisterSource(mock)

	dep1 := &domain.Mod{ID: "dep1", SourceID: "src", Name: "Dep One", Version: "1.0", GameID: "g1"}
	root := &domain.Mod{ID: "root", SourceID: "src", Name: "Root", Version: "2.0", GameID: "g1",
		Dependencies: []domain.ModReference{{SourceID: "src", ModID: "dep1"}}}
	mock.AddMod(dep1.GameID, dep1)
	mock.AddMod(root.GameID, root)

	depZip := createTestZip(t, t.TempDir(), map[string]string{"dep1.esp": "dep-payload"})
	depContent, err := os.ReadFile(depZip)
	require.NoError(t, err)
	mock.AddDownload("dep1", depContent)

	// root is installed at v1.0 (DB row + cache entry) and LOCKED at v1.0;
	// dep1 is NOT installed, so the plan takes the BATCH path.
	seedNamedInstalledMod(t, svc, game, "src", "root", "Root", "1.0", true, map[string][]byte{
		"root.esp": []byte("v1 payload"),
	})
	lockProfileRef(t, svc, "g1", "default", "src", "root", "1.0", nil)

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "root", false)
	require.NoError(t, err)
	require.Len(t, plan.Dependencies, 1)

	// The NEXT GetModFiles call - lockedInstallRefusal's own up-front
	// derivation - fails transiently; every later call succeeds.
	mock.failNext = 1

	sink, seen := core.RecordEvents()
	result, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, sink)
	require.NoError(t, err, "batch semantics: the locked primary skips, the run itself succeeds")
	require.NotNil(t, result)
	assert.Equal(t, []string{"Dep One"}, installedRefNames(result.Installed), "the dependency must still install")
	assert.Equal(t, []string{"Root"}, installedRefNames(result.Failed))
	require.Len(t, result.Skipped, 1)
	assert.Equal(t, "Root", result.Skipped[0].Name)
	assert.Contains(t, result.Skipped[0].Reason, "locked at v1.0", "the skip reason must name the lock")

	// The deployed lock target's cache must survive: the loop's lock check
	// must fire BEFORE the uninstall-existing block, not after it.
	assert.True(t, svc.GetGameCache(game).Exists("g1", "src", "root", "1.0"),
		"the lock target's cache entry must not be deleted on a refused reinstall")
	phases, _ := phasesOf(*seen)
	for _, ph := range phases {
		assert.NotEqual(t, core.InstallDepReinstalling, ph,
			"the uninstall-existing block must never run for the refused locked primary")
	}

	got, dbErr := svc.GetInstalledMod(context.Background(), "src", "root", "g1", "default")
	require.NoError(t, dbErr)
	assert.Equal(t, "1.0", got.Version, "the DB row must stay at the locked version")

	profile, pErr := svc.NewProfileManager().Get(context.Background(), "g1", "default")
	require.NoError(t, pErr)
	ref := profile.FindRef("src", "root")
	require.NotNil(t, ref)
	assert.Equal(t, "1.0", ref.Version)
	assert.True(t, ref.Locked)
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

	_, dbErr := svc.GetInstalledMod(context.Background(), "src", "dep1", "g1", "default")
	assert.Error(t, dbErr, "nothing should be installed once the context is already cancelled")
}

// TestService_ApplyInstall_ContextCancelledBetweenBatchMods_ReturnsPartialResultWithCtxErr
// is Task 6 item d's mid-run counterpart to
// TestService_ApplyInstall_ContextCancellation above (which only proved a
// pre-cancelled ctx short-circuits before any work): the BATCH path's
// combined [Dependencies..., primary] loop already checked ctx.Err() at
// the top of every iteration (ApplyInstall's own early ctx.Err() guard,
// present since Task 2/Fix wave 1); this formalizes coverage for the
// between-mods case the doc comment above promised. The progress callback
// cancels the instant the first mod (dep1) finishes installing; root (the
// primary, second in the combined list) must never be touched at all.
func TestService_ApplyInstall_ContextCancelledBetweenBatchMods_ReturnsPartialResultWithCtxErr(t *testing.T) {
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
	result, err := svc.ApplyInstall(ctx, game, plan, core.InstallOptions{}, func(e core.Event) {
		if m, ok := e.(core.ModEvent); ok && m.Phase == core.InstallDepInstalled && m.ModName == "Dep One" {
			cancel()
		}
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	require.NotNil(t, result, "diagnostics accumulated before cancellation must not be discarded")
	assert.Equal(t, []string{"Dep One"}, installedRefNames(result.Installed))

	_, dbErr := svc.GetInstalledMod(context.Background(), "src", "root", "g1", "default")
	assert.ErrorIs(t, dbErr, domain.ErrModNotFound, "root must never have been installed - cancellation lands BETWEEN batch mods")
	_, statErr := os.Lstat(filepath.Join(gameDir, "root.esp"))
	assert.True(t, os.IsNotExist(statErr), "root.esp must never have been deployed")
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
	seedHooks(t, svc, game, "default", domain.GameHooks{Install: domain.HookConfig{BeforeEach: failScript}})

	sink, seen := core.RecordEvents()
	result, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, sink)
	require.NoError(t, err, "the primary's before_each failure must never fail the whole install in the BATCH path, even without Force")
	require.NotNil(t, result)
	assert.Equal(t, []string{"Dep One"}, installedRefNames(result.Installed), "only the dependency installs - the primary was skipped")
	require.Len(t, result.Skipped, 1)
	assert.Equal(t, "Root", result.Skipped[0].Name)
	assert.Contains(t, result.Skipped[0].Reason, "install.before_each hook failed")
	require.Len(t, result.Failed, 1)
	assert.Equal(t, "Root", result.Failed[0].Name)
	assert.Empty(t, result.Warnings, "a BATCH-path hook skip is never Force-gated, so it must never produce a Warning")

	_, dbErr := svc.GetInstalledMod(context.Background(), "src", "root", "g1", "default")
	assert.Error(t, dbErr, "the skipped primary must not be saved")

	var installingEvents []core.ModEvent
	for _, e := range *seen {
		if m, ok := e.(core.ModEvent); ok && m.Phase == core.InstallDepInstalling {
			installingEvents = append(installingEvents, m)
		}
	}
	require.Len(t, installingEvents, 2, "InstallDepInstalling must fire for the primary too, not just the dependency")
	assert.Equal(t, 1, installingEvents[0].Index)
	assert.Equal(t, 2, installingEvents[0].Total, "Index/Total must span the WHOLE combined list (dep + primary)")
	assert.Equal(t, "Dep One", installingEvents[0].ModName)
	assert.Equal(t, "1.0", installingEvents[0].Version)
	assert.Equal(t, 2, installingEvents[1].Index)
	assert.Equal(t, 2, installingEvents[1].Total)
	assert.Equal(t, "Root", installingEvents[1].ModName)
	assert.Equal(t, "1.0", installingEvents[1].Version)
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

	sink, seen := core.RecordEvents()
	result, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, sink)
	require.NoError(t, err)
	assert.Equal(t, []string{"Dep One", "Root"}, installedRefNames(result.Installed))
	assert.Equal(t, 0, result.FilesDeployed, "FilesDeployed is a STRICT-path-only accumulator - the BATCH path never touches it")

	var installedEvents []core.ModEvent
	for _, e := range *seen {
		if m, ok := e.(core.ModEvent); ok && m.Phase == core.InstallDepInstalled {
			installedEvents = append(installedEvents, m)
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
	installer := svc.GetInstallerForTest(game)
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

	sink, seen := core.RecordEvents()
	result, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, sink)
	require.NoError(t, err)
	assert.Equal(t, []string{"Dep One", "Root"}, installedRefNames(result.Installed))

	var sawReinstalling bool
	for _, e := range *seen {
		if m, ok := e.(core.ModEvent); ok && m.Phase == core.InstallDepReinstalling && m.ModName == "Root" {
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

	sink, seen := core.RecordEvents()
	_, err = svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, sink)
	require.NoError(t, err)

	var sawFileSelected, sawDownloadDone, sawChecksum int
	for _, e := range *seen {
		step, ok := e.(core.StepEvent)
		if !ok {
			continue
		}
		switch step.Phase {
		case core.InstallDepFileSelected:
			sawFileSelected++
			require.NotNil(t, step.File)
		case core.InstallDepDownloadDone:
			sawDownloadDone++
		case core.InstallChecksumComputed:
			sawChecksum++
			assert.NotEmpty(t, step.Detail)
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
	installer := svc.GetInstallerForTest(game)
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

	installed, err := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0", installed.Version, "DB row must be unchanged")
}

// --- ApplyInstall: conflicts (Ruling 1 - *ConflictError / AcceptConflicts) ---
//
// These tests guard the post-cache-fill/pre-hook conflict gate in
// fillPrimaryCache - which, since the 2026-08-29 Apply-order ruling, sits
// AHEAD of install.before_all/before_each rather than at the pre-extraction
// CLI's own prompt position (see InstallOptions.AcceptConflicts' doc
// comment). v2 Phase 3 Task 8 replaced the confirmation callback with a
// typed error:
// core computes the conflicts and returns *core.ConflictError before it
// deploys or writes anything, and the frontend answers by re-running
// ApplyInstall with AcceptConflicts set. Two twin arrangements exercise the
// two ways fillPrimaryCache can reach the check:
//
//   - "fresh install": the primary has NEVER been cached before -
//     installer.GetConflicts can only see the conflict once the download
//     loop populates the LIVE cache, which is exactly what PlanInstall.
//     Conflicts (computed pre-download) can never do for a mod like this -
//     the live-proven silent-overwrite half of C1.
//   - "same-version reinstall": plan.Replaces.Version == mod.Version, so
//     fillPrimaryCache routes the fresh download into a
//     reinstallCacheTransaction's STAGED cache, not the live one -
//     installer.GetConflicts (bound to the LIVE cache) therefore still
//     inspects the mod's PRE-existing cached file list, not the
//     newly-downloaded one, exactly like the pre-extraction CLI's own
//     confirmInstallConflicts did at this same point (Activate() hasn't run
//     yet) - so the conflict must already be present in the mod's ORIGINAL
//     cache entry for this leg.

// applyInstallConflictFixture seeds "other", installed and deployed, owning
// shared.esp, then registers a NEW ("newmod") not-yet-cached mod whose own
// download also contains shared.esp - the only way to reach the "fresh
// install" leg of the conflict check (a pre-cached "newmod" would instead
// exercise PlanInstall's own Conflicts detection - already covered by
// TestService_PlanInstall_ConflictingFilesListsPathAndOwningMod).
func applyInstallConflictFixture(t *testing.T) (svc *core.Service, game *domain.Game, gameDir string, mock *perModFileSource) {
	t.Helper()
	svc = newFlowsTestService(t)
	gameDir = t.TempDir()
	game = &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedInstalledMod(t, svc, game, "src", "other", "1.0", true, map[string][]byte{"shared.esp": []byte("original-other-content")})
	installer := svc.GetInstallerForTest(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "other", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))

	mock = &perModFileSource{mockSourceWithDownloads: newMockSourceWithDownloads("src")}
	t.Cleanup(mock.Close)
	svc.RegisterSource(mock)
	registerDownloadableMod(t, mock, &domain.Mod{ID: "newmod", SourceID: "src", Name: "New Mod", Version: "1.0", GameID: "g1"}, "shared.esp", "new-content")

	return svc, game, gameDir, mock
}

func TestService_ApplyInstall_Conflicts_FreshInstall(t *testing.T) {
	t.Run("unaccepted conflicts return *ConflictError with the post-download list; plan-time Conflicts stayed empty", func(t *testing.T) {
		svc, game, gameDir, _ := applyInstallConflictFixture(t)

		plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "newmod", false)
		require.NoError(t, err)
		require.Empty(t, plan.Conflicts, "an uncached mod's plan must never report conflicts - see InstallPlan.Conflicts' doc comment")

		result, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, nil)
		require.Error(t, err)

		var conflictErr *core.ConflictError
		require.ErrorAs(t, err, &conflictErr, "the conflict must surface as a typed error, never a callback")
		assert.ErrorIs(t, err, domain.ErrFileConflict, "*ConflictError must unwrap to the domain sentinel")
		require.Len(t, conflictErr.Conflicts, 1, "only detectable AFTER download, never by PlanInstall")
		assert.Equal(t, "shared.esp", conflictErr.Conflicts[0].RelativePath)
		assert.Equal(t, "other", conflictErr.Conflicts[0].CurrentModID)

		// End state: the cache fill happened (it is not a mutation of
		// managed state - Ruling 1), and NOTHING else did.
		require.NotNil(t, result, "a partial result must be returned alongside the error")
		assert.Empty(t, result.Installed)
		assert.True(t, svc.GetGameCache(game).Exists("g1", "src", "newmod", "1.0"), "the download must remain cached - the re-run with AcceptConflicts reuses this entry")

		_, dbErr := svc.GetInstalledMod(context.Background(), "src", "newmod", "g1", "default")
		assert.Error(t, dbErr, "an unaccepted conflict must leave zero DB mutations")

		profile, perr := svc.NewProfileManager().Get(context.Background(), "g1", "default")
		if perr == nil {
			for _, ref := range profile.Mods {
				assert.NotEqual(t, "newmod", ref.ModID, "an unaccepted conflict must leave zero profile mutations")
			}
		}

		content, err := os.ReadFile(filepath.Join(gameDir, "shared.esp"))
		require.NoError(t, err)
		assert.Equal(t, "original-other-content", string(content), "the conflicting mod's deployed file must survive untouched")
	})

	t.Run("AcceptConflicts deploys - the frontend's re-run after its own prompt", func(t *testing.T) {
		svc, game, gameDir, _ := applyInstallConflictFixture(t)

		plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "newmod", false)
		require.NoError(t, err)

		_, err = svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, nil)
		require.ErrorAs(t, err, new(*core.ConflictError), "sanity: the first run must refuse")

		result, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{AcceptConflicts: true}, nil)
		require.NoError(t, err)
		assert.Equal(t, []string{"New Mod"}, installedRefNames(result.Installed))

		content, err := os.ReadFile(filepath.Join(gameDir, "shared.esp"))
		require.NoError(t, err)
		assert.Equal(t, "new-content", string(content), "accepting must overwrite the conflicting file")
	})

	t.Run("Force implies AcceptConflicts - the check never runs", func(t *testing.T) {
		svc, game, _, _ := applyInstallConflictFixture(t)

		plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "newmod", false)
		require.NoError(t, err)

		result, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{Force: true}, nil)
		require.NoError(t, err, "--force must skip the conflict check entirely, matching the pre-extraction CLI's own \"if !installForce\" gate")
		assert.Equal(t, []string{"New Mod"}, installedRefNames(result.Installed))
	})
}

// TestService_ApplyInstall_Conflicts_DeclineThenAccept_RunsHooksExactlyOnce
// pins the Apply ordering ruled on 2026-08-29: the cache fill and the
// conflict computation both precede install.before_all/before_each, so a
// REFUSED Apply costs ZERO hook runs and the frontend's accept re-run is the
// only one that runs them. Decline->accept is ONE user-level install, so a
// non-idempotent user hook fires exactly once across the pair.
func TestService_ApplyInstall_Conflicts_DeclineThenAccept_RunsHooksExactlyOnce(t *testing.T) {
	svc, game, _, _ := applyInstallConflictFixture(t)

	scriptsDir := t.TempDir()
	callLog := filepath.Join(scriptsDir, "calls.log")
	beforeAll := createTestScript(t, scriptsDir, "before_all.sh", `#!/bin/bash
echo "install.before_all:$LMM_MOD_ID" >> `+callLog+`
exit 0`)
	beforeEach := createTestScript(t, scriptsDir, "before_each.sh", `#!/bin/bash
echo "install.before_each:$LMM_MOD_ID" >> `+callLog+`
exit 0`)
	seedHooks(t, svc, game, "default", domain.GameHooks{Install: domain.HookConfig{BeforeAll: beforeAll, BeforeEach: beforeEach}})

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "newmod", false)
	require.NoError(t, err)

	_, err = svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, nil)
	require.ErrorAs(t, err, new(*core.ConflictError))

	_, statErr := os.Stat(callLog)
	assert.True(t, os.IsNotExist(statErr),
		"a refused conflict must run NO hook at all - the conflict gate precedes install.before_all")

	_, err = svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{AcceptConflicts: true}, nil)
	require.NoError(t, err)

	logContent, err := os.ReadFile(callLog)
	require.NoError(t, err)
	assert.Equal(t, "install.before_all:\ninstall.before_each:newmod\n", string(logContent),
		"the accept re-run runs each hook exactly once (before_all with no mod identity, before_each with the primary's)")
}

// TestService_ApplyInstall_Conflicts_DeclineThenAccept_DownloadsExactlyOnce is
// the network half of the same ruling: the refused run's cache fill is what
// makes the conflict computable at all, so the accept re-run finds the cache
// WARM and skips the download entirely - the same HasFileIDs cache-first
// guard ApplyProfileSwitch/ApplyProfileImport already use (#96/#138), not an
// AcceptConflicts special case. The deployed-file count the frontend prints
// must survive the skip.
func TestService_ApplyInstall_Conflicts_DeclineThenAccept_DownloadsExactlyOnce(t *testing.T) {
	svc, game, gameDir, mock := applyInstallConflictFixture(t)

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "newmod", false)
	require.NoError(t, err)

	_, err = svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, nil)
	require.ErrorAs(t, err, new(*core.ConflictError))
	require.Equal(t, 1, mock.DownloadCount(), "sanity: the refused run DID fill the cache - that is what makes the conflict computable")

	result, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{AcceptConflicts: true}, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, mock.DownloadCount(), "the accept re-run must reuse the warm cache entry, not re-download it")
	assert.Equal(t, 1, result.FilesDeployed, "the file count the frontend prints must survive the cache-first skip")

	content, err := os.ReadFile(filepath.Join(gameDir, "shared.esp"))
	require.NoError(t, err)
	assert.Equal(t, "new-content", string(content), "the warm cache entry is what gets deployed")

	// Important 2 (task-8 review): the warm-fill skip that makes the accept
	// re-run download-free must not ALSO make it checksum-free.
	files, err := svc.GetFilesWithChecksums(context.Background(), "g1", "default")
	require.NoError(t, err)
	var newmodChecksum string
	for _, f := range files {
		if f.ModID == "newmod" {
			newmodChecksum = f.Checksum
		}
	}
	assert.NotEmpty(t, newmodChecksum, "the accept re-run's cache-warm install must still save a checksum")
}

// TestService_ApplyInstall_Conflicts_SameVersionReinstall_LeavesOriginalDeployedContentUntouched
// is the reinstall-cache-transaction twin of the fresh-install leg above:
// mod1 is already installed+deployed (and its ORIGINAL cache entry already
// overlaps "other" - see the fixture's own note on why the freshly
// re-downloaded content isn't what this leg's GetConflicts call inspects),
// then reinstalled at the SAME version. Returning *ConflictError must roll
// back the staged reinstall-cache-transaction via its existing deferred
// Rollback, restoring the live cache/deployed files exactly as they were -
// mirroring TestService_ApplyInstall_ReplacePath's "download fails" subtest.
func TestService_ApplyInstall_Conflicts_SameVersionReinstall_LeavesOriginalDeployedContentUntouched(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	// mod1 already installed+deployed, cached at 1.0 with its own exclusive
	// file PLUS shared.esp (as if a prior archive version bundled it) -
	// shared.esp is stored directly to cache, not deployed, so "other"'s own
	// later Install of the same path below never collides with a live
	// symlink mod1 already owns.
	seedInstalledMod(t, svc, game, "src", "mod1", "1.0", true, map[string][]byte{"mod1.esp": []byte("original-content")})
	installer := svc.GetInstallerForTest(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "mod1", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))
	require.NoError(t, svc.GetGameCache(game).Store(game.ID, "src", "mod1", "1.0", "shared.esp", []byte("mod1-shared-content")))

	// "other" is installed+deployed AFTER mod1, independently owning
	// shared.esp too - the DB conflict GetConflicts will find against
	// mod1's own (pre-existing) cached copy.
	seedInstalledMod(t, svc, game, "src", "other", "1.0", true, map[string][]byte{"shared.esp": []byte("other-content")})
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "other", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))

	mock := &perModFileSource{mockSourceWithDownloads: newMockSourceWithDownloads("src")}
	defer mock.Close()
	svc.RegisterSource(mock)
	registerDownloadableMod(t, mock, &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"}, "mod1.esp", "new-content")

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "mod1", false)
	require.NoError(t, err)
	require.NotNil(t, plan.Replaces)
	assert.Equal(t, "1.0", plan.Replaces.Version, "a same-version reinstall - the reinstall-cache-transaction path")
	// PlanInstall itself ALSO detects this (pre-download, since mod1's own
	// cache already exists) - the "cached reinstall" leg of C1: conflicts
	// WERE detectable pre-download here, just prompted at the wrong
	// position before this fix (see the CLI-level twin,
	// TestDoInstall_ConflictPrompt_ForceSkipsPrompt, in cmd/lmm/install_test.go).
	require.Len(t, plan.Conflicts, 1)

	result, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, nil)
	require.Error(t, err)
	require.ErrorAs(t, err, new(*core.ConflictError))
	require.NotNil(t, result)
	assert.Empty(t, result.Installed)

	content, err := os.ReadFile(filepath.Join(gameDir, "mod1.esp"))
	require.NoError(t, err)
	assert.Equal(t, "original-content", string(content), "the reinstall-cache-transaction's rollback must restore the ORIGINAL deployed content")

	assert.True(t, svc.GetGameCache(game).Exists("g1", "src", "mod1", "1.0"), "the live cache entry must exist (restored, not left empty/half-migrated)")

	installed, err := svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0", installed.Version, "DB row must be unchanged")
}

// TestService_ApplyInstall_Conflicts_SameVersionReinstall_AcceptRerun_Redownloads
// pins the first recorded warm-cache carve-out (task-8 review, Important 1):
// unlike the fresh/upgrade-install leg (DeclineThenAccept_DownloadsExactlyOnce,
// which stays at 1), a same-version reinstall's accept re-run downloads AGAIN
// - the reinstall-cache transaction always stages into a fresh EMPTY cache
// (prepareReinstallCacheTransaction clones only the DB snapshot, never the
// live cache directory), so cacheWarm's own HasFileIDs check can never see it
// as complete. This is by design (a reinstall is a repair, not a skip), and
// this test is what turns that into a characterized property instead of a
// latent one.
func TestService_ApplyInstall_Conflicts_SameVersionReinstall_AcceptRerun_Redownloads(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedInstalledMod(t, svc, game, "src", "mod1", "1.0", true, map[string][]byte{"mod1.esp": []byte("original-content")})
	installer := svc.GetInstallerForTest(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "mod1", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))
	require.NoError(t, svc.GetGameCache(game).Store(game.ID, "src", "mod1", "1.0", "shared.esp", []byte("mod1-shared-content")))

	seedInstalledMod(t, svc, game, "src", "other", "1.0", true, map[string][]byte{"shared.esp": []byte("other-content")})
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "other", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))

	mock := &perModFileSource{mockSourceWithDownloads: newMockSourceWithDownloads("src")}
	defer mock.Close()
	svc.RegisterSource(mock)
	registerDownloadableMod(t, mock, &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"}, "mod1.esp", "new-content")

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "mod1", false)
	require.NoError(t, err)
	require.NotNil(t, plan.Replaces, "sanity: the reinstall-cache-transaction path")

	_, err = svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, nil)
	require.ErrorAs(t, err, new(*core.ConflictError))
	require.Equal(t, 1, mock.DownloadCount(), "sanity: the refused run downloaded into the staged transaction cache")

	result, err := svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{AcceptConflicts: true}, nil)
	require.NoError(t, err)
	assert.Equal(t, 2, mock.DownloadCount(), "a same-version reinstall's accept re-run re-downloads by design - it never routes through the cache-first guard")
	assert.Equal(t, []string{"Mod One"}, installedRefNames(result.Installed))

	content, err := os.ReadFile(filepath.Join(gameDir, "mod1.esp"))
	require.NoError(t, err)
	assert.Equal(t, "new-content", string(content))
}

// --- #140: --version/--file interplay (TargetFileIDs; strict-path TargetVersion) ---

// perModMultiFileSource serves a caller-supplied file list PER MOD ID - the
// #140 interplay tests need the primary to offer several files across
// several versions (so --version pools, --file pins, and the archived
// filter all diverge) while a dependency keeps a single unambiguous file.
// fileFetches counts GetModFiles calls so a test can pin the "caller's
// selection already satisfies the targets - no refetch" fast path.
type perModMultiFileSource struct {
	*mockSourceWithDownloads
	files       map[string][]domain.DownloadableFile // mod.ID -> served files, verbatim
	fileFetches atomic.Int64
}

func (s *perModMultiFileSource) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	s.fileFetches.Add(1)
	return s.files[mod.ID], nil
}

// interplayRootFiles is root's served file list for every #140 test: v2.0's
// primary MAIN file, v1.0's superseded MAIN (OLD_VERSION category, so the
// default archived filter drops it), and a v1.0 OPTIONAL patch. The v1.0
// version pool therefore holds two files whose auto-pick (no IsPrimary ->
// category sort puts OPTIONAL first) differs from an explicit --file pin on
// the OLD_VERSION file.
func interplayRootFiles() []domain.DownloadableFile {
	return []domain.DownloadableFile{
		{ID: "root-main-2", Name: "Root 2.0", FileName: "root-2.0.zip", Version: "2.0", IsPrimary: true, Category: "MAIN"},
		{ID: "root-main-1", Name: "Root 1.0", FileName: "root-1.0.zip", Version: "1.0", Category: "OLD_VERSION"},
		{ID: "root-opt-1", Name: "Root Patch 1.0", FileName: "root-patch-1.0.zip", Version: "1.0", Category: "OPTIONAL"},
	}
}

// stageInterplayDownload registers a one-file zip download for fileID.
func stageInterplayDownload(t *testing.T, mock *perModMultiFileSource, fileID, relativePath string) {
	t.Helper()
	zipPath := createTestZip(t, t.TempDir(), map[string]string{relativePath: fileID + "-payload"})
	content, err := os.ReadFile(zipPath)
	require.NoError(t, err)
	mock.AddDownload(fileID, content)
}

// setupInterplayService builds a service whose "src" source serves root's
// interplayRootFiles (and, when withDep, a single-file dep1 that root
// depends on - forcing PlanInstall onto the BATCH path).
func setupInterplayService(t *testing.T, withDep bool) (*core.Service, *domain.Game, *perModMultiFileSource) {
	t.Helper()
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	mock := &perModMultiFileSource{
		mockSourceWithDownloads: newMockSourceWithDownloads("src"),
		files:                   map[string][]domain.DownloadableFile{"root": interplayRootFiles()},
	}
	t.Cleanup(mock.Close)
	svc.RegisterSource(mock)

	root := &domain.Mod{ID: "root", SourceID: "src", Name: "Root", Version: "2.0", GameID: "g1"}
	if withDep {
		root.Dependencies = []domain.ModReference{{SourceID: "src", ModID: "dep1"}}
		dep := &domain.Mod{ID: "dep1", SourceID: "src", Name: "Dep One", Version: "3.0", GameID: "g1"}
		mock.files["dep1"] = []domain.DownloadableFile{{ID: "dep1", Name: "Dep One", FileName: "dep1.zip", IsPrimary: true}}
		stageInterplayDownload(t, mock, "dep1", "dep1.esp")
		mock.AddMod(dep.GameID, dep)
	}
	for _, fid := range []string{"root-main-2", "root-main-1", "root-opt-1"} {
		stageInterplayDownload(t, mock, fid, fid+".esp")
	}
	mock.AddMod(root.GameID, root)
	return svc, game, mock
}

// TestService_ApplyInstall_BatchPath_TargetVersionWithFileIDs_HonorsFileForPrimary
// is #140 item 1's core repro: on the BATCH (dependencies-present) path,
// TargetVersion + TargetFileIDs must install exactly the pinned file for the
// primary - previously TargetFileIDs did not exist and the version pool's
// auto-pick silently won (--file silently ignored, #93's silent-flag class).
func TestService_ApplyInstall_BatchPath_TargetVersionWithFileIDs_HonorsFileForPrimary(t *testing.T) {
	svc, game, _ := setupInterplayService(t, true)

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "root", false)
	require.NoError(t, err)
	require.Len(t, plan.Dependencies, 1, "root must take the BATCH path")

	opts := core.InstallOptions{TargetVersion: "1.0", TargetFileIDs: []string{"root-main-1"}}
	result, err := svc.ApplyInstall(context.Background(), game, plan, opts, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"Dep One", "Root"}, installedRefNames(result.Installed))

	got, err := svc.GetInstalledMod(context.Background(), "src", "root", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, []string{"root-main-1"}, got.FileIDs, "the pinned --file must win over the version pool's auto-pick")
	assert.Equal(t, "1.0", got.Version)

	profile, err := svc.NewProfileManager().Get(context.Background(), "g1", "default")
	require.NoError(t, err)
	ref := profile.FindRef("src", "root")
	require.NotNil(t, ref)
	assert.Equal(t, []string{"root-main-1"}, ref.FileIDs)
	assert.Equal(t, "1.0", ref.Version)

	dep, err := svc.GetInstalledMod(context.Background(), "src", "dep1", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, []string{"dep1"}, dep.FileIDs, "dependencies still auto-select their own primary, untouched by the primary's pins")
}

// TestService_ApplyInstall_BatchPath_TargetFileIDs_MultipleFilesAllInstalled
// pins full --file fidelity on the BATCH path: several pinned file IDs all
// download and are all recorded, matching the STRICT path's existing
// multi-file behavior.
func TestService_ApplyInstall_BatchPath_TargetFileIDs_MultipleFilesAllInstalled(t *testing.T) {
	svc, game, mock := setupInterplayService(t, true)

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "root", false)
	require.NoError(t, err)
	require.Len(t, plan.Dependencies, 1)

	opts := core.InstallOptions{TargetVersion: "1.0", TargetFileIDs: []string{"root-main-1", "root-opt-1"}}
	result, err := svc.ApplyInstall(context.Background(), game, plan, opts, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"Dep One", "Root"}, installedRefNames(result.Installed))

	got, err := svc.GetInstalledMod(context.Background(), "src", "root", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, []string{"root-main-1", "root-opt-1"}, got.FileIDs, "every pinned file must be recorded, in pin order")
	assert.Equal(t, "1.0", got.Version)
	assert.Equal(t, 3, mock.DownloadCount(), "dep1 + both pinned root files must each be downloaded")
}

// TestService_ApplyInstall_BatchPath_TargetFileIDs_UnknownID_AbortsWholeInstall
// pins the loud-failure half of #140 item 1: a --file ID that doesn't
// resolve (here: against the version pool) is fatal to the WHOLE install, up
// front, with zero dependencies installed - the #96 TargetVersion loudness
// precedent, not a quiet per-mod Failed line.
func TestService_ApplyInstall_BatchPath_TargetFileIDs_UnknownID_AbortsWholeInstall(t *testing.T) {
	svc, game, mock := setupInterplayService(t, true)

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "root", false)
	require.NoError(t, err)
	require.Len(t, plan.Dependencies, 1)

	opts := core.InstallOptions{TargetVersion: "1.0", TargetFileIDs: []string{"nope"}}
	result, err := svc.ApplyInstall(context.Background(), game, plan, opts, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "file ID nope not found")
	require.NotNil(t, result)
	assert.Empty(t, result.Installed, "zero mods may be installed when the primary's file pin cannot resolve")

	assert.Equal(t, 0, mock.DownloadCount(), "the abort must fire BEFORE any download")
	_, err = svc.GetInstalledMod(context.Background(), "src", "dep1", "g1", "default")
	assert.True(t, errors.Is(err, domain.ErrModNotFound), "no dependency may be installed")
}

// TestService_ApplyInstall_BatchPath_TargetFileIDsWithoutVersion_ResolvedAgainstFilteredList
// pins --file-without---version on the BATCH path: the pin resolves against
// the plan.ShowArchived-filtered list (the STRICT path's exact pool
// semantics) - an unarchived file resolves, an archived one is refused.
func TestService_ApplyInstall_BatchPath_TargetFileIDsWithoutVersion_ResolvedAgainstFilteredList(t *testing.T) {
	svc, game, _ := setupInterplayService(t, true)

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "root", false)
	require.NoError(t, err)
	require.Len(t, plan.Dependencies, 1)

	opts := core.InstallOptions{TargetFileIDs: []string{"root-opt-1"}}
	result, err := svc.ApplyInstall(context.Background(), game, plan, opts, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"Dep One", "Root"}, installedRefNames(result.Installed))

	got, err := svc.GetInstalledMod(context.Background(), "src", "root", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, []string{"root-opt-1"}, got.FileIDs)
	assert.Equal(t, "1.0", got.Version, "the recorded version must be the pinned file's own (#94 effective-version stamp)")
}

// TestService_ApplyInstall_BatchPath_TargetFileIDs_ArchivedWithoutShowArchived_Refused
// is the filtered half of the previous test: without ShowArchived, an
// OLD_VERSION file ID is not in the pool and must be refused loudly (use
// --version or --show-archived to reach it), never silently swapped.
func TestService_ApplyInstall_BatchPath_TargetFileIDs_ArchivedWithoutShowArchived_Refused(t *testing.T) {
	svc, game, mock := setupInterplayService(t, true)

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "root", false)
	require.NoError(t, err)
	require.Len(t, plan.Dependencies, 1)

	opts := core.InstallOptions{TargetFileIDs: []string{"root-main-1"}}
	_, err = svc.ApplyInstall(context.Background(), game, plan, opts, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "file ID root-main-1 not found")
	assert.Equal(t, 0, mock.DownloadCount())
}

// TestService_ApplyInstall_StrictPath_TargetVersion_ResolvedInCore is #140
// item 2's core repro: on the STRICT (no-deps) path, a core caller that sets
// only opts.TargetVersion (plan.Files still PlanInstall's latest default)
// must get the target version installed - previously TargetVersion was
// documented-inert there (the CLI compensated by overriding plan.Files), so
// a future core caller would silently install latest.
func TestService_ApplyInstall_StrictPath_TargetVersion_ResolvedInCore(t *testing.T) {
	svc, game, _ := setupInterplayService(t, false)

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "root", false)
	require.NoError(t, err)
	require.Empty(t, plan.Dependencies, "root must take the STRICT path")
	require.Len(t, plan.Files, 1)
	assert.Equal(t, "root-main-2", plan.Files[0].ID, "precondition: the plan's default is the latest primary")

	opts := core.InstallOptions{TargetVersion: "1.0"}
	result, err := svc.ApplyInstall(context.Background(), game, plan, opts, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"Root"}, installedRefNames(result.Installed))

	got, err := svc.GetInstalledMod(context.Background(), "src", "root", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0", got.Version, "TargetVersion must be honored in core on the STRICT path")
	assert.Equal(t, []string{"root-opt-1"}, got.FileIDs, "the version pool's auto-pick (category sort, no IsPrimary -> first) must be installed")
	assert.True(t, svc.GetGameCache(game).Exists("g1", "src", "root", "1.0"), "cache must be keyed by the target version")
}

// TestService_ApplyInstall_StrictPath_TargetVersion_CallerSelectionWithinVersionKept
// pins the compatibility contract that keeps the CLI's interactive/--file
// sub-selection working: when plan.Files already sits entirely inside the
// target version, the caller's exact selection is installed verbatim - core
// must not re-derive (and must not even refetch the file list).
func TestService_ApplyInstall_StrictPath_TargetVersion_CallerSelectionWithinVersionKept(t *testing.T) {
	svc, game, mock := setupInterplayService(t, false)

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "root", false)
	require.NoError(t, err)
	require.Empty(t, plan.Dependencies)

	// The caller (the CLI's selectInstallFiles) chose the version pool's
	// NON-default file.
	plan.Files = []domain.DownloadableFile{interplayRootFiles()[1]} // root-main-1, v1.0
	fetchesBeforeApply := mock.fileFetches.Load()

	opts := core.InstallOptions{TargetVersion: "1.0"}
	_, err = svc.ApplyInstall(context.Background(), game, plan, opts, nil)
	require.NoError(t, err)

	got, err := svc.GetInstalledMod(context.Background(), "src", "root", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, []string{"root-main-1"}, got.FileIDs, "a caller selection already within TargetVersion must be installed verbatim")
	assert.Equal(t, "1.0", got.Version)
	assert.Equal(t, fetchesBeforeApply, mock.fileFetches.Load(), "a satisfied selection must not trigger a redundant GetModFiles refetch")
}

// TestService_ApplyInstall_StrictPath_TargetVersionUnresolvable_FailsBeforeSideEffects
// pins the STRICT path's loud-failure contract for TargetVersion: an unknown
// version is fatal up front (ErrVersionNotFound), with nothing downloaded,
// deployed, or recorded - never a silent latest-install.
func TestService_ApplyInstall_StrictPath_TargetVersionUnresolvable_FailsBeforeSideEffects(t *testing.T) {
	svc, game, mock := setupInterplayService(t, false)
	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "root", false)
	require.NoError(t, err)

	opts := core.InstallOptions{TargetVersion: "9.9"}
	result, err := svc.ApplyInstall(context.Background(), game, plan, opts, nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, core.ErrVersionNotFound), "must fail with ErrVersionNotFound, got: %v", err)
	require.NotNil(t, result)
	assert.Empty(t, result.Installed)

	assert.Equal(t, 0, mock.DownloadCount(), "no download may happen for an unresolvable version")
	entries, err := os.ReadDir(game.ModPath)
	require.NoError(t, err)
	assert.Empty(t, entries, "nothing may be deployed")
	_, err = svc.GetInstalledMod(context.Background(), "src", "root", "g1", "default")
	assert.True(t, errors.Is(err, domain.ErrModNotFound), "no DB row may be written")
}

// TestService_ApplyInstall_StrictPath_TargetFileIDs_ResolvedInCore pins
// TargetFileIDs on the STRICT path for a core caller that never touches
// plan.Files: the pin resolves against the filtered list and replaces the
// plan's default selection.
func TestService_ApplyInstall_StrictPath_TargetFileIDs_ResolvedInCore(t *testing.T) {
	svc, game, _ := setupInterplayService(t, false)
	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "root", false)
	require.NoError(t, err)
	require.Len(t, plan.Files, 1)
	require.Equal(t, "root-main-2", plan.Files[0].ID)

	opts := core.InstallOptions{TargetFileIDs: []string{"root-opt-1"}}
	_, err = svc.ApplyInstall(context.Background(), game, plan, opts, nil)
	require.NoError(t, err)

	got, err := svc.GetInstalledMod(context.Background(), "src", "root", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, []string{"root-opt-1"}, got.FileIDs)
	assert.Equal(t, "1.0", got.Version)
}

// TestService_ApplyInstall_StrictPath_TargetVersionConvergesToLock_Allowed
// pins the #143 lock-gate interplay with core-resolved TargetVersion:
// installing a locked mod AT exactly its locked version via TargetVersion
// (plan.Files still the latest default) must converge, not be refused - the
// up-front gate must judge the version the resolved install would actually
// record, not the stale plan default.
func TestService_ApplyInstall_StrictPath_TargetVersionConvergesToLock_Allowed(t *testing.T) {
	svc, game, _ := setupInterplayService(t, false)

	lockProfileRef(t, svc, "g1", "default", "src", "root", "1.0", []string{"root-opt-1"})

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "root", false)
	require.NoError(t, err)
	require.Len(t, plan.Files, 1)
	require.Equal(t, "2.0", plan.Files[0].Version, "precondition: the plan default is the non-locked latest")

	opts := core.InstallOptions{TargetVersion: "1.0"}
	_, err = svc.ApplyInstall(context.Background(), game, plan, opts, nil)
	require.NoError(t, err, "installing at exactly the locked version via TargetVersion must be allowed (converge/repair)")

	got, err := svc.GetInstalledMod(context.Background(), "src", "root", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0", got.Version)

	profile, err := svc.NewProfileManager().Get(context.Background(), "g1", "default")
	require.NoError(t, err)
	ref := profile.FindRef("src", "root")
	require.NotNil(t, ref)
	assert.True(t, ref.Locked, "the lock marker must survive the converge")
	assert.Equal(t, "1.0", ref.Version)
}

// TestService_ApplyInstall_BatchPath_TargetFileIDs_DuplicatesDeduped pins
// resolveTargetFiles' duplicate handling (#140 review): a repeated pinned ID
// (--file 9,9) resolves to ONE selection entry - otherwise the duplicate
// survives to SaveInstalledMod's installed_mod_files INSERTs, whose primary
// key includes file_id, failing the install only AFTER download and deploy.
func TestService_ApplyInstall_BatchPath_TargetFileIDs_DuplicatesDeduped(t *testing.T) {
	svc, game, _ := setupInterplayService(t, true)

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "root", false)
	require.NoError(t, err)
	require.Len(t, plan.Dependencies, 1)

	opts := core.InstallOptions{TargetVersion: "1.0", TargetFileIDs: []string{"root-main-1", "root-main-1"}}
	result, err := svc.ApplyInstall(context.Background(), game, plan, opts, nil)
	require.NoError(t, err, "a duplicated pin must not fail the install")
	assert.Equal(t, []string{"Dep One", "Root"}, installedRefNames(result.Installed))

	got, err := svc.GetInstalledMod(context.Background(), "src", "root", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, []string{"root-main-1"}, got.FileIDs, "duplicate pins collapse to one recorded file")
}

// --- resolveInstallDependencies game-ID threading (#230) ---

// TestService_PlanInstall_DependencyFetchSurvivesGameIDNamespaceCollision is
// the #230 regression test: dependency fetches must use the LMM game id (and
// let Service.GetMod translate it) rather than feeding the SOURCE-DOMAIN id
// the source stamped onto the target mod back into GetMod. The old code only
// worked because s.games[<source-domain-id>] normally misses, letting the id
// fall through untranslated - but LMM game ids are user-chosen in games.yaml,
// so another game's LMM id can legally collide with this game's source-domain
// id. Here "skyrimspecialedition" is BOTH skyrim's source-domain id on "src"
// AND a second configured game's own LMM id: with the old code the dependency
// fetch hit that second game's mapping ("other-domain") and silently looked
// for the dependency in the wrong game.
func TestService_PlanInstall_DependencyFetchSurvivesGameIDNamespaceCollision(t *testing.T) {
	svc := newFlowsTestService(t)

	game := &domain.Game{ID: "skyrim", Name: "Skyrim", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink,
		SourceIDs: map[string]string{"src": "skyrimspecialedition"}}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	// The colliding game: its user-chosen LMM id equals skyrim's source-domain
	// id, and it maps "src" to a different domain of its own.
	require.NoError(t, svc.SaveGame(context.Background(), &domain.Game{ID: "skyrimspecialedition", Name: "Collider",
		ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink,
		SourceIDs: map[string]string{"src": "other-domain"}}))

	mock := newMockSource("src")
	svc.RegisterSource(mock)
	// Both mods live under skyrim's SOURCE-DOMAIN id, and the source stamps
	// that id onto what it returns (mirroring the real sources - e.g.
	// internal/source/nexusmods stamps the id it was queried with).
	mock.AddMod("skyrimspecialedition", &domain.Mod{ID: "dep1", SourceID: "src", Name: "Dep One",
		Version: "1.0", GameID: "skyrimspecialedition"})
	mock.AddMod("skyrimspecialedition", &domain.Mod{ID: "root", SourceID: "src", Name: "Root",
		Version: "1.0", GameID: "skyrimspecialedition",
		Dependencies: []domain.ModReference{{SourceID: "src", ModID: "dep1"}}})

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "root", false)
	require.NoError(t, err)
	assert.Empty(t, plan.MissingDependencies,
		"dependency must be fetched from skyrim's own source domain, not the colliding game's mapping")
	require.Len(t, plan.Dependencies, 1)
	assert.Equal(t, "dep1", plan.Dependencies[0].ID)
}

// TestService_PlanInstall_DependencyFetchTranslatesMappedGameID pins the
// ordinary, non-colliding path around the #230 fix: with a real SourceIDs
// mapping the source must keep receiving its own domain id for dependency
// fetches - previously via the stamped id falling through GetMod untranslated,
// now via GetMod translating the LMM id. The mods are registered ONLY under
// the source-domain id, so the test fails if the dependency fetch ever sends
// the raw LMM id ("skyrim") to the source.
func TestService_PlanInstall_DependencyFetchTranslatesMappedGameID(t *testing.T) {
	svc := newFlowsTestService(t)

	game := &domain.Game{ID: "skyrim", Name: "Skyrim", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink,
		SourceIDs: map[string]string{"src": "skyrimspecialedition"}}
	require.NoError(t, svc.SaveGame(context.Background(), game))

	mock := newMockSource("src")
	svc.RegisterSource(mock)
	mock.AddMod("skyrimspecialedition", &domain.Mod{ID: "dep1", SourceID: "src", Name: "Dep One",
		Version: "1.0", GameID: "skyrimspecialedition"})
	mock.AddMod("skyrimspecialedition", &domain.Mod{ID: "root", SourceID: "src", Name: "Root",
		Version: "1.0", GameID: "skyrimspecialedition",
		Dependencies: []domain.ModReference{{SourceID: "src", ModID: "dep1"}}})

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "root", false)
	require.NoError(t, err)
	assert.Empty(t, plan.MissingDependencies)
	require.Len(t, plan.Dependencies, 1)
	assert.Equal(t, "dep1", plan.Dependencies[0].ID)
}

// TestService_PlanInstall_DependencyFetchEmptyMappingKeepsLMMGameID pins the
// empty-mapping nuance of the #230 fix: a SourceIDs entry mapped to "" (e.g.
// directory sources: `donovan-mods: ""`) means "this source applies to any
// game" and must not blank the id GetMod sends - dependency fetches keep
// using the LMM game id itself, exactly as the target-mod fetch does.
func TestService_PlanInstall_DependencyFetchEmptyMappingKeepsLMMGameID(t *testing.T) {
	svc := newFlowsTestService(t)

	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink,
		SourceIDs: map[string]string{"src": ""}}
	require.NoError(t, svc.SaveGame(context.Background(), game))

	mock := newMockSource("src")
	svc.RegisterSource(mock)
	mock.AddMod("g1", &domain.Mod{ID: "dep1", SourceID: "src", Name: "Dep One", Version: "1.0", GameID: "g1"})
	mock.AddMod("g1", &domain.Mod{ID: "root", SourceID: "src", Name: "Root", Version: "1.0", GameID: "g1",
		Dependencies: []domain.ModReference{{SourceID: "src", ModID: "dep1"}}})

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "root", false)
	require.NoError(t, err)
	assert.Empty(t, plan.MissingDependencies)
	require.Len(t, plan.Dependencies, 1)
	assert.Equal(t, "dep1", plan.Dependencies[0].ID)
}

// --- Fix round 1: cancellation safety (task-3 review C1 / I2) ---

// TestService_ApplyInstall_SameVersionReinstall_CancelledMidDeploy_RestoresLiveCache
// is review finding C1's regression guard: the reinstall cache transaction's
// recovery half (RestoreLive/Rollback) must run to completion even when the
// request ctx is already dead, because it is a Delete-then-CloneMod sequence
// whose live cache entry is destroyed by the Delete. InstallDeploying is the
// last callback the flow emits before Activate, so cancelling there lands
// inside exactly the destructive window C1 describes ("Ctrl-C during the
// deploy step" of a same-version reinstall). Afterwards the live entry must
// still hold its ORIGINAL files and the snapshot temp dir must be gone.
func TestService_ApplyInstall_SameVersionReinstall_CancelledMidDeploy_RestoresLiveCache(t *testing.T) {
	tmpRoot := t.TempDir()
	t.Setenv("TMPDIR", tmpRoot) // where the transaction's snapshot temp dir lands

	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	seedInstalledMod(t, svc, game, "src", "mod1", "1.0", true, map[string][]byte{"mod1.esp": []byte("original-content")})
	installer := svc.GetInstallerForTest(game)
	require.NoError(t, installer.Install(context.Background(), game, &domain.Mod{ID: "mod1", SourceID: "src", Version: "1.0", GameID: "g1"}, "default"))

	mock := &perModFileSource{mockSourceWithDownloads: newMockSourceWithDownloads("src")}
	defer mock.Close()
	svc.RegisterSource(mock)
	registerDownloadableMod(t, mock, &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"}, "mod1.esp", "new-content")

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "mod1", false)
	require.NoError(t, err)
	require.NotNil(t, plan.Replaces)
	require.Equal(t, "1.0", plan.Replaces.Version, "a same-version reinstall - the reinstall-cache-transaction path")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result, err := svc.ApplyInstall(ctx, game, plan, core.InstallOptions{}, func(e core.Event) {
		if fe, ok := e.(core.FlowEvent); ok && fe.FlowPhase() == core.InstallDeploying {
			cancel()
		}
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	require.NotNil(t, result)

	gameCache := svc.GetGameCache(game)
	require.True(t, gameCache.Exists("g1", "src", "mod1", "1.0"),
		"the live cache entry must be restored, not left deleted by a cancelled clone")
	files, err := gameCache.ListFiles("g1", "src", "mod1", "1.0")
	require.NoError(t, err)
	assert.Equal(t, []string{"mod1.esp"}, files, "the restored live entry must carry its files")
	content, err := os.ReadFile(gameCache.GetFilePath("g1", "src", "mod1", "1.0", "mod1.esp"))
	require.NoError(t, err)
	assert.Equal(t, "original-content", string(content), "the ORIGINAL cached bytes must be what was restored")

	entries, err := os.ReadDir(tmpRoot)
	require.NoError(t, err)
	var leaked []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "lmm-reinstall-cache-") {
			leaked = append(leaked, e.Name())
		}
	}
	assert.Empty(t, leaked, "Rollback must remove the snapshot temp dir even when the restore ran under a cancelled ctx")
}

// --- Fix round 2: recovery paths never inherit the caller's cancellation ---

// TestService_ApplyInstall_FreshInstall_CancelledAtDBSave_UndeploysFiles is the
// round-2 regression guard: every best-effort recovery call that runs AFTER the
// primary operation failed must run under context.WithoutCancel, because the
// cancellation that provoked the failure would otherwise also disable its
// recovery. The plain-install shape is the cheapest and most common instance -
// Ctrl-C landing on SaveInstalledMod means installer.Uninstall gets the same
// dead ctx, returns ctx.Err() at its first file, and leaves an orphaned
// deployment in the game directory with no installed_mods row to find it by.
func TestService_ApplyInstall_FreshInstall_CancelledAtDBSave_UndeploysFiles(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	mock := &perModFileSource{mockSourceWithDownloads: newMockSourceWithDownloads("src")}
	defer mock.Close()
	svc.RegisterSource(mock)
	registerDownloadableMod(t, mock, &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1"}, "mod1.esp", "payload")

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "mod1", false)
	require.NoError(t, err)
	require.Nil(t, plan.Replaces, "a fresh install - the plain (non-transaction) path")

	// The deploy has already succeeded by the time this fires; the DB save is
	// the step the cancellation breaks.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc.SetBeforeSaveInstalledForTest(cancel)

	result, err := svc.ApplyInstall(ctx, game, plan, core.InstallOptions{}, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	require.NotNil(t, result)

	_, statErr := os.Lstat(filepath.Join(gameDir, "mod1.esp"))
	assert.True(t, os.IsNotExist(statErr),
		"the Uninstall recovery must run to completion under the cancelled ctx, leaving no orphaned deployment: %v", statErr)

	_, err = svc.GetInstalledMod(context.Background(), "src", "mod1", "g1", "default")
	assert.Error(t, err, "the failed save must leave no installed_mods row")
}

// TestService_ApplyInstall_BatchPath_CancelledBetweenPrimaryFiles_RecordsFailureAndErrors
// is review finding I2's regression guard: on the BATCH path the primary is
// the LAST entry in the loop by construction, so a per-file ctx check that
// returns a bare nil lets ApplyInstall exit 0 with the primary uninstalled
// and nothing recorded. Cancelling on the primary's FIRST InstallDepDownloadDone
// (its download already finished; the next iteration's head check is what
// fires) must surface as an error AND name the primary in result.Failed.
func TestService_ApplyInstall_BatchPath_CancelledBetweenPrimaryFiles_RecordsFailureAndErrors(t *testing.T) {
	svc, game, _ := setupInterplayService(t, true)

	plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "root", false)
	require.NoError(t, err)
	require.Len(t, plan.Dependencies, 1, "root must take the BATCH path")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var cancelled bool
	opts := core.InstallOptions{TargetVersion: "1.0", TargetFileIDs: []string{"root-main-1", "root-opt-1"}}
	result, err := svc.ApplyInstall(ctx, game, plan, opts, func(e core.Event) {
		if fe, ok := e.(core.FlowEvent); ok && !cancelled && fe.FlowPhase() == core.InstallDepDownloadDone && fe.EventScope().ModName == "Root" {
			cancelled = true
			cancel()
		}
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	require.NotNil(t, result)
	assert.Contains(t, installedRefNames(result.Failed), "Root", "the cancelled primary must be recorded as failed, not silently dropped")
	assert.NotContains(t, installedRefNames(result.Installed), "Root")
}
