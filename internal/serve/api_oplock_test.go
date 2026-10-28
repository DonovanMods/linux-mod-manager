package serve_test

// #317's refusal on the SYNCHRONOUS write routes. The job-backed routes
// answer a contended mutation as a job failure, and the queue-depth
// refusal in kind_toggle.go already answers 409 - but the single-step
// writes (mod settings, profile CRUD, game add/edit, auth, the source
// editor) classified core.ErrOperationInProgress as "any other failure"
// and returned 500. It is neither a server fault nor permanent: another
// lmm holds the mutation lock, and the same request a second later
// succeeds, which is exactly what 409 tells a client.

import (
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/serve"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// holdOpLock takes the same advisory flock core's beginOp takes, standing
// in for the other lmm process: flock is per open file description, so a
// descriptor opened here contends with the server's exactly as a second
// process's would, with no subprocess to flake.
func holdOpLock(t *testing.T, path string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0600)
	require.NoError(t, err)
	require.NoError(t, syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB))
	t.Cleanup(func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		require.NoError(t, f.Close())
	})
}

func TestServer_APIModLock_ContendedMutationAnswers409(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), ".oplock")
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
		Logger: slog.New(slog.DiscardHandler), OpLockPath: lockPath,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	src := newFakeSource("fake")
	svc.RegisterSource(src)
	game := &domain.Game{
		ID: "g1", Name: "Fixture Game",
		InstallPath: t.TempDir(), ModPath: t.TempDir(),
		LinkMethod: domain.LinkSymlink, SourceIDs: map[string]string{src.ID(): ""},
	}
	require.NoError(t, svc.SaveGame(t.Context(), game))
	_, err = svc.NewProfileManager().Create(t.Context(), game.ID, "default")
	require.NoError(t, err)
	seedInstalledMod(t, svc, game, domain.Mod{
		ID: "1", SourceID: "fake", Name: "Better Boots", Version: "1.0", GameID: game.ID,
	}, true, nil)
	require.NoError(t, svc.SetDefaultGame(t.Context(), game.ID))

	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})

	// Everything above ran BEFORE the lock is held: the fixture's own
	// writes are mutations too.
	holdOpLock(t, lockPath)

	rec := postAPI(t, srv, "/api/v1/mods/fake/1/lock", `{}`)
	assert.Equal(t, http.StatusConflict, rec.Code,
		"a contended mutation is transient and retryable, not a server fault")
	assert.Contains(t, rec.Body.String(), "another lmm operation is in progress")
}
