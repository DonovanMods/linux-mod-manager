package core_test

// The loader precondition asked of the SOURCE (#409, design §3.4).
//
// #359 infers "this is a BepInEx mod" from an ARCHIVE's shape, which is the
// earliest an import can know it. A Thunderstore package says so outright,
// in its own metadata, before anything has been downloaded - so the same
// refusal can land at plan time, which is where a user wants it: before a
// 40 MB download, not after.
//
// One refusal, one set of setup steps. Whichever half sees it first builds
// a core.LoaderRequiredError through the same constructor, so the CLI, the
// web UI and --json cannot drift about what a user is told to do.

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// loaderDeclaringSource is a source that reports a loader requirement off
// its own metadata - source.LoaderRequirer's shape, standing in for
// Thunderstore's "this package depends on BepInEx-BepInExPack-5.4.2100".
type loaderDeclaringSource struct {
	*perModFileSource
	kind, version string
	required      bool
	err           error
	asked         int
}

func (s *loaderDeclaringSource) LoaderRequirement(ctx context.Context, mod *domain.Mod) (string, string, bool, error) {
	s.asked++
	return s.kind, s.version, s.required, s.err
}

// newLoaderRequiringService registers such a source with one downloadable
// mod, against a game that declares whatever loader the caller passes (nil
// for none).
func newLoaderRequiringService(t *testing.T, loader *domain.GameLoader) (*core.Service, *domain.Game, *loaderDeclaringSource) {
	t.Helper()
	svc := newFlowsTestService(t)
	root := t.TempDir()
	game := &domain.Game{
		ID: "lethal-company", Name: "Lethal Company",
		InstallPath: root, ModPath: root,
		LinkMethod: domain.LinkSymlink, Loader: loader,
	}
	require.NoError(t, svc.SaveGame(context.Background(), game))

	src := &loaderDeclaringSource{
		perModFileSource: &perModFileSource{mockSourceWithDownloads: newMockSourceWithDownloads("thunderstore")},
		kind:             domain.LoaderKindBepInEx, version: "5.4.2100", required: true,
	}
	t.Cleanup(src.Close)
	svc.RegisterSource(src)
	registerDownloadableMod(t, src.perModFileSource, &domain.Mod{
		ID: "RugbugRedfern-Skinwalkers", SourceID: "thunderstore", Name: "Skinwalkers",
		Version: "3.0.2", GameID: "lethal-company",
	}, "SkinwalkerMod.dll", "assembly")
	return svc, game, src
}

// TestPlanInstall_RefusesAPackageThatNeedsAnUndeclaredLoader is the
// headline: at PLAN time, before a byte is downloaded.
func TestPlanInstall_RefusesAPackageThatNeedsAnUndeclaredLoader(t *testing.T) {
	svc, game, src := newLoaderRequiringService(t, nil)

	_, err := svc.PlanInstall(context.Background(), game, "default", "thunderstore", "RugbugRedfern-Skinwalkers", false)
	require.Error(t, err)

	var loaderErr *core.LoaderRequiredError
	require.ErrorAs(t, err, &loaderErr)
	assert.Equal(t, domain.LoaderKindBepInEx, loaderErr.Kind)
	assert.Equal(t, "lethal-company", loaderErr.GameID)
	assert.Equal(t, "Skinwalkers", loaderErr.ModName)
	assert.Equal(t, "5.4.2100", loaderErr.Version,
		"the version the package pinned is the one piece of information the steps cannot derive")
	require.NotEmpty(t, loaderErr.Setup, "the same three sentences #359 prints")
	assert.Positive(t, src.asked)
	assert.Zero(t, src.DownloadCount(), "nothing may be downloaded by a refused plan")
}

// TestPlanInstall_ADeclaredGameInstallsNormally is the other half, and the
// design's "on a declared game the BepInExPack dependency is satisfied by
// the declaration and never installed as a mod".
func TestPlanInstall_ADeclaredGameInstallsNormally(t *testing.T) {
	svc, game, _ := newLoaderRequiringService(t, &domain.GameLoader{Kind: domain.LoaderKindBepInEx, Version: "5.4.2100"})

	plan, err := svc.PlanInstall(context.Background(), game, "default", "thunderstore", "RugbugRedfern-Skinwalkers", false)
	require.NoError(t, err)
	assert.Equal(t, "RugbugRedfern-Skinwalkers", plan.Mod.ID)
}

// TestPlanInstall_APackageThatNeedsNoLoaderIsNeverRefused: the refusal is
// driven by the package's own metadata, so a mod that declares nothing is
// unaffected even on a game with no loader at all.
func TestPlanInstall_APackageThatNeedsNoLoaderIsNeverRefused(t *testing.T) {
	svc, game, src := newLoaderRequiringService(t, nil)
	src.required = false

	_, err := svc.PlanInstall(context.Background(), game, "default", "thunderstore", "RugbugRedfern-Skinwalkers", false)
	require.NoError(t, err)
}

// TestPlanInstall_ASourceThatCannotAnswerDoesNotBlockTheInstall: "could not
// tell" is not "needs a loader". The archive-shape rule downstream is the
// second line of defence, and refusing an install over a question lmm could
// not ask would make an unreachable source look like a misconfigured game.
func TestPlanInstall_ASourceThatCannotAnswerDoesNotBlockTheInstall(t *testing.T) {
	svc, game, src := newLoaderRequiringService(t, nil)
	src.err = assertAnError

	_, err := svc.PlanInstall(context.Background(), game, "default", "thunderstore", "RugbugRedfern-Skinwalkers", false)
	require.NoError(t, err)
}

// TestPlanInstall_ASourceWithNoLoaderOpinionIsUntouched keeps the seam
// optional: every source that does not implement source.LoaderRequirer must
// behave exactly as it did.
func TestPlanInstall_ASourceWithNoLoaderOpinionIsUntouched(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	mock := &perModFileSource{mockSourceWithDownloads: newMockSourceWithDownloads("src")}
	t.Cleanup(mock.Close)
	svc.RegisterSource(mock)
	registerDownloadableMod(t, mock, &domain.Mod{
		ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1",
	}, "mod1.esp", "content")

	_, err := svc.PlanInstall(context.Background(), game, "default", "src", "mod1", false)
	require.NoError(t, err)
}

// TestPlanInstallMany_RecordsTheLoaderRefusalPerEntry: the batch path
// records a refusal per entry rather than failing the whole plan, which is
// every other plan-time refusal's convention there.
func TestPlanInstallMany_RecordsTheLoaderRefusalPerEntry(t *testing.T) {
	svc, game, _ := newLoaderRequiringService(t, nil)
	mod, err := svc.GetMod(context.Background(), "thunderstore", game.ID, "RugbugRedfern-Skinwalkers")
	require.NoError(t, err)

	plan, err := svc.PlanInstallMany(context.Background(), game, "default", []*domain.Mod{mod}, false)
	require.NoError(t, err)
	require.Len(t, plan.Batch, 1)
	assert.Contains(t, plan.Batch[0].FetchError, "BepInEx")
	assert.Nil(t, plan.Batch[0].File, "a refused entry selects no file")
}

// TestDeclaresLoaderIsKindAgnostic: the game-side question generalises past
// BepInEx, because GameLoader.Kind is an open string (#359) and a source
// may name a loader lmm has never heard of.
func TestDeclaresLoaderIsKindAgnostic(t *testing.T) {
	g := &domain.Game{Loader: &domain.GameLoader{Kind: "MelonLoader"}}
	assert.True(t, g.DeclaresLoader("melonloader"), "the comparison folds case, as the config value is typed by hand")
	assert.False(t, g.DeclaresLoader(domain.LoaderKindBepInEx))
	assert.False(t, (&domain.Game{}).DeclaresLoader("melonloader"))
	assert.False(t, g.DeclaresLoader(""))

	bep := &domain.Game{Loader: &domain.GameLoader{Kind: "BepInEx"}}
	assert.True(t, bep.DeclaresLoader(domain.LoaderKindBepInEx))
	assert.True(t, bep.DeclaresBepInEx(), "and the BepInEx question is now one call into it")
}

// assertAnError is a stand-in failure for "the source could not answer".
var assertAnError = source.ErrIndexUnavailable
