package core_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Fix round 1: cancellation safety (task-3 review I1) ---

// TestService_DeployProfile_CancelledDuringLastModRedownload_RecordsSkipAndErrors
// is review finding I1's regression guard: redeployFromSource's ctx checks
// returned a bare `true`, which DeployProfile turns into a plain `continue`.
// When the cancelled mod is the LAST one in the profile there is no next
// iteration to trip the loop-head check, so the deploy reported success with
// the mod silently missing. Cancelling on the last mod's DeployRedownloading
// event (the callback immediately before its per-file download loop, so the
// loop-head check is what fires) must both record the skip and surface a
// context.Canceled error from DeployProfile.
func TestService_DeployProfile_CancelledDuringLastModRedownload_RecordsSkipAndErrors(t *testing.T) {
	svc := newFlowsTestService(t)
	gameDir := t.TempDir()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink}

	mock := &perModMultiFileSource{
		mockSourceWithDownloads: newMockSourceWithDownloads("src"),
		files: map[string][]domain.DownloadableFile{
			"first": {{ID: "first-1", Name: "First File", FileName: "first.zip", Version: "1.0", IsPrimary: true}},
			"last":  {{ID: "last-1", Name: "Last File", FileName: "last.zip", Version: "1.0", IsPrimary: true}},
		},
	}
	t.Cleanup(mock.Close)
	svc.RegisterSource(mock)
	stageInterplayDownload(t, mock, "first-1", "first.esp")
	stageInterplayDownload(t, mock, "last-1", "last.esp")
	mock.AddMod("g1", &domain.Mod{ID: "first", SourceID: "src", Name: "First Mod", Version: "1.0", GameID: "g1"})
	mock.AddMod("g1", &domain.Mod{ID: "last", SourceID: "src", Name: "Last Mod", Version: "1.0", GameID: "g1"})

	// Both rows exist with nothing cached, so each mod takes
	// redeployFromSource; the profile's order makes "last" the final one.
	for _, m := range []struct{ id, name, fileID string }{{"first", "First Mod", "first-1"}, {"last", "Last Mod", "last-1"}} {
		require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
			Mod:          domain.Mod{ID: m.id, SourceID: "src", Name: m.name, Version: "1.0", GameID: "g1"},
			ProfileName:  "default",
			UpdatePolicy: domain.UpdateNotify,
			Enabled:      true,
			FileIDs:      []string{m.fileID},
		}))
		seedProfileWithMod(t, svc, "g1", "default", "src", m.id, "1.0")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result, err := svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, func(e core.Event) {
		if fe, ok := e.(core.FlowEvent); ok && fe.FlowPhase() == core.DeployRedownloading && fe.EventScope().ModName == "Last Mod" {
			cancel()
		}
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Deployed, "the first mod deployed before the cancellation")
	require.Len(t, result.Skipped, 1, "the cancelled mod must be recorded as skipped, not silently dropped")
	assert.Equal(t, "Last Mod", result.Skipped[0].Name)
	assert.Contains(t, result.Skipped[0].Reason, "cancelled")
}

// TestService_QueriesRunDuringMutation pins the contract end to end under
// -race (v2 Phase 1 Task 6, #279; rewritten in fix round 1 per review
// finding I2): a DeployProfile holds the mutation slot open on a gate, 16
// query goroutines (GetInstalledMods + ListGames) must all complete while
// the gate is still closed, and a second mutation (SetModDeployed) started
// during that same window must NOT return until the gate opens. A
// regression that lets a query - or a second mutation - through
// unserialized deadlocks this test into one of its timeouts instead of
// passing green with nothing checked.
func TestService_QueriesRunDuringMutation(t *testing.T) {
	svc, game := newDeployableService(t)
	ctx := context.Background()

	started := make(chan struct{})
	gate := make(chan struct{})
	errs := make(chan error, 32)

	deployDone := make(chan struct{})
	go func() {
		defer close(deployDone)
		first := true
		_, err := svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, func(core.Event) {
			if first {
				first = false
				close(started)
				<-gate
			}
		})
		errs <- err
	}()
	<-started

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := svc.GetInstalledMods(ctx, game.ID, "default"); err != nil {
				errs <- err
			}
			_ = svc.ListGames()
		}()
	}
	queriesDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(queriesDone)
	}()
	select {
	case <-queriesDone:
	case <-time.After(5 * time.Second):
		t.Fatal("queries blocked behind a mutation")
	}

	secondDone := make(chan struct{})
	go func() {
		defer close(secondDone)
		if err := svc.SetModDeployed(ctx, "src", "1", game.ID, "default", false); err != nil {
			errs <- err
		}
	}()

	select {
	case <-secondDone:
		t.Fatal("second mutation returned while the first mutation still holds the slot")
	case <-time.After(100 * time.Millisecond):
	}

	close(gate)

	select {
	case <-deployDone:
	case <-time.After(5 * time.Second):
		t.Fatal("DeployProfile never finished after the gate was released")
	}
	select {
	case <-secondDone:
	case <-time.After(5 * time.Second):
		t.Fatal("second mutation never finished after the gate was released")
	}

	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
}
