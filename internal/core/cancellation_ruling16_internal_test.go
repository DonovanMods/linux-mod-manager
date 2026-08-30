package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/db"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newRuling16Service builds a Service over three temp dirs - the same shape
// core_test's newFlowsTestService builds, restated here because these two
// shape tests exercise unexported gates (ensureProfileExists,
// lockedInstallRefusal) and so must live in package core.
func newRuling16Service(t *testing.T) *Service {
	t.Helper()
	svc, err := NewService(ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	return svc
}

// cancelledContext returns a context that is already cancelled.
func cancelledContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	t.Cleanup(cancel)
	return ctx
}

// TestEnsureProfileExists_CancelledRead_IsReportedNotTreatedAsExisting is the
// class-(B) shape test for v2 Phase 3 Ruling 16: a pre-write profile read
// that could not answer "does this profile exist?" must be returned, not
// mapped onto "the profile is fine". One test covers the shape; the sibling
// site (import_archive.go) now calls this same helper.
//
// Against 85a3ed0 this fails at the require.Error: ensureProfileExists
// returned nil for every error that was not ErrProfileNotFound, so its
// callers went straight on to a profile write with no answer in hand.
func TestEnsureProfileExists_CancelledRead_IsReportedNotTreatedAsExisting(t *testing.T) {
	configDir := t.TempDir()
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })

	pm := NewProfileManager(configDir, database)

	err = ensureProfileExists(cancelledContext(t), pm, "skyrim-se", "brand-new")
	require.Error(t, err, "a read that could not answer must not report success")
	assert.ErrorIs(t, err, context.Canceled)

	// Nothing was created: the profile still does not exist, so an
	// uncancelled Get still reports ErrProfileNotFound.
	_, err = pm.Get(context.Background(), "skyrim-se", "brand-new")
	assert.ErrorIs(t, err, domain.ErrProfileNotFound)
}

// TestLockedInstallRefusal_CancelledRead_DoesNotDegradeTheGateOpen is the
// class-(C) shape test for v2 Phase 3 Ruling 16: every lock gate reads the
// profile behind `err == nil` and treats a failed read as "no lock" - right
// for a missing or corrupt profile, wrong for a cancellation, which says
// nothing about the lock at all.
//
// The fixture is a genuinely locked ref (asserted, so the test cannot pass
// vacuously against a gate that would have answered "unlocked" anyway).
// Against 85a3ed0 the second half fails: the gate returned nil - the same
// answer it gives an unlocked mod - and ApplyInstall would have installed
// over the lock.
func TestLockedInstallRefusal_CancelledRead_DoesNotDegradeTheGateOpen(t *testing.T) {
	svc := newRuling16Service(t)
	ctx := context.Background()

	pm := svc.NewProfileManager()
	_, err := pm.Create(ctx, "g1", "default")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(ctx, "g1", "default", domain.ModReference{SourceID: "src", ModID: "m1", Version: "1.0"}))
	require.NoError(t, pm.SetModLock(ctx, "g1", "default", "src", "m1", "1.0"))

	plan := &InstallPlan{
		SourceID: "src",
		GameID:   "g1",
		Profile:  "default",
		Mod:      domain.Mod{ID: "m1", SourceID: "src", Version: "2.0", GameID: "g1"},
		Files:    []domain.DownloadableFile{{ID: "f1", Name: "Main", FileName: "m1.zip", IsPrimary: true, Version: "2.0"}},
	}

	profilePath := filepath.Join(svc.configDir, "games", "g1", "profiles", "default.yaml")
	before, err := os.ReadFile(profilePath)
	require.NoError(t, err)

	require.ErrorIs(t, svc.lockedInstallRefusal(ctx, plan, InstallOptions{}), ErrModLocked,
		"fixture check: an uncancelled gate must refuse this install")

	err = svc.lockedInstallRefusal(cancelledContext(t), plan, InstallOptions{})
	require.Error(t, err, "a cancelled read must not answer 'not locked'")
	assert.ErrorIs(t, err, context.Canceled)

	after, err := os.ReadFile(profilePath)
	require.NoError(t, err)
	assert.Equal(t, before, after, "a lock gate reads only; the mod's profile ref must be untouched")
}

// versionPassCancelSource is a minimal source.ModSource whose GetModFiles
// cancels ctx as a side effect (mirroring a real request that gets
// cancelled mid-flight) before returning a normal result. Used to drive
// versionPass directly rather than through the full Verify pipeline: a
// later phase (perFileWalk) independently re-checks ctx.Err() over the
// checksummed-files list and - since any mod with FileIDs (required to
// reach versionPass's mismatch check at all) always has a row in that list
// - would otherwise always observe the same cancellation first and mask a
// missing post-loop check in versionPass itself.
type versionPassCancelSource struct {
	id     string
	cancel context.CancelFunc
}

func (s *versionPassCancelSource) ID() string      { return s.id }
func (s *versionPassCancelSource) Name() string    { return s.id }
func (s *versionPassCancelSource) AuthURL() string { return "" }

func (s *versionPassCancelSource) ExchangeToken(ctx context.Context, code string) (*source.Token, error) {
	return nil, errors.New("not implemented")
}

func (s *versionPassCancelSource) Search(ctx context.Context, query source.SearchQuery) (source.SearchResult, error) {
	return source.SearchResult{}, errors.New("not implemented")
}

func (s *versionPassCancelSource) GetMod(ctx context.Context, gameID, modID string) (*domain.Mod, error) {
	return nil, errors.New("not implemented")
}

func (s *versionPassCancelSource) GetDependencies(ctx context.Context, mod *domain.Mod) ([]domain.ModReference, error) {
	return nil, nil
}

func (s *versionPassCancelSource) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	s.cancel()
	return []domain.DownloadableFile{{ID: "f1", Version: "1.0", IsPrimary: true}}, nil
}

func (s *versionPassCancelSource) GetDownloadURL(ctx context.Context, mod *domain.Mod, fileID string) (string, error) {
	return "", errors.New("not implemented")
}

func (s *versionPassCancelSource) CheckUpdates(ctx context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	return nil, nil
}

// TestVersionPass_CancellationDuringLastModsOwnIteration_ReturnsCtxErr is
// the shape test for the task 18 re-review round 2 NEW-3 finding:
// versionPass checks ctx.Err() at the TOP of each iteration, so a
// cancellation landing inside the LAST (here, only) mod's own iteration
// never reaches a next iteration to catch it. Driven directly against
// versionPass (see versionPassCancelSource's doc for why the full Verify
// pipeline can't isolate this).
//
// Against a versionPass with the post-loop check removed, this fails: the
// call returns nil even though the cancellation landed inside the only
// mod's own iteration.
func TestVersionPass_CancellationDuringLastModsOwnIteration_ReturnsCtxErr(t *testing.T) {
	svc := newRuling16Service(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir()}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), "g1", "default")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	svc.RegisterSource(&versionPassCancelSource{id: "csrc", cancel: cancel})

	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: "mod-a", SourceID: "csrc", Name: "Mod A", Version: "1.0", GameID: "g1"},
		ProfileName:  "default",
		Enabled:      true,
		FileIDs:      []string{"f1"},
		UpdatePolicy: domain.UpdateNotify,
	}))

	mods, err := svc.GetInstalledMods(context.Background(), "g1", "default")
	require.NoError(t, err)
	require.Len(t, mods, 1, "test setup: exactly one mod, so this is both the first and the LAST iteration")

	prof, err := pm.Get(context.Background(), "g1", "default")
	require.NoError(t, err)

	r := &verifyRun{ctx: ctx, svc: svc, game: game, profile: "default", opts: VerifyOptions{Tier: VerifyFull}, result: &VerifyResult{}}
	err = r.versionPass(mods, prof)
	require.Error(t, err, "the cancellation inside the only mod's own iteration must not be swallowed as a nil return")
	assert.ErrorIs(t, err, context.Canceled)
}
