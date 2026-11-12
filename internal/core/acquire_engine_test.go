package core_test

// #602: install-shaped Applies acquire every item before they commit any.
// These tests drive the engine through its real entry points - ApplyImport
// and ApplyInstall - against a real HTTP download, so "nothing was written"
// is asserted on the cache, the DB and the profile file, and "no second
// download" on the server's own request count.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/custom"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// acquireMod is one mod an acquireFixture serves: its id and name, and the
// members of its single archive.
type acquireMod struct {
	id, name string
	members  map[string]string
}

// plainMod is an ordinary mod: an archive no adapter refuses.
func plainMod(id, name string) acquireMod {
	return acquireMod{id: id, name: name, members: map[string]string{id + ".txt": "payload " + id}}
}

// loaderMod is a BepInEx plugin: a game with no loader declared refuses
// it, but only once its archive is extracted.
func loaderMod(id, name string) acquireMod {
	return acquireMod{id: id, name: name, members: map[string]string{
		"BepInEx/plugins/" + id + ".dll": "assembly",
		"manifest.json":                  "{}",
	}}
}

// acquireFixture is a manifest source serving mods over real HTTP into a
// game with no loader declared, counting the archive requests per mod.
type acquireFixture struct {
	svc       *core.Service
	game      *domain.Game
	downloads map[string]*atomic.Int64
}

func newAcquireFixture(t *testing.T, mods ...acquireMod) *acquireFixture {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	fx := &acquireFixture{downloads: map[string]*atomic.Int64{}}
	manifest := "version: 1\nmods:\n"
	for _, m := range mods {
		archive := bepinexZipBytes(t, m.members)
		counter := &atomic.Int64{}
		fx.downloads[m.id] = counter
		manifest += fmt.Sprintf(`  - id: %s
    name: %s
    version: 1.0.0
    files:
      - id: %s-main
        filename: %s-1.0.0.zip
        version: 1.0.0
        url: %s/files/%s.zip
        primary: true
`, m.id, m.name, m.id, m.id, srv.URL, m.id)
		mux.HandleFunc("/files/"+m.id+".zip", func(w http.ResponseWriter, _ *http.Request) {
			counter.Add(1)
			_, _ = w.Write(archive)
		})
	}
	mux.HandleFunc("/mods.yaml", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(manifest)) })

	src, err := custom.New(custom.SourceDefinition{
		ID: "repo", Name: "Repo", Type: custom.TypeManifest, AllowHTTP: true,
		Manifest: &custom.ManifestConfig{URL: srv.URL + "/mods.yaml"},
	})
	require.NoError(t, err)

	fx.svc = newFlowsTestService(t)
	fx.svc.RegisterSource(src)
	root := t.TempDir()
	fx.game = &domain.Game{
		ID: "valheim", Name: "Valheim", InstallPath: root, ModPath: root,
		LinkMethod: domain.LinkSymlink, SourceIDs: map[string]string{"repo": "valheim"},
	}
	require.NoError(t, fx.svc.SaveGame(context.Background(), fx.game))
	return fx
}

// fetches is how many times mod id's archive was requested.
func (fx *acquireFixture) fetches(id string) int64 { return fx.downloads[id].Load() }

// importPlan plans importing a profile named "target" holding ids.
func (fx *acquireFixture) importPlan(t *testing.T, ids ...string) *core.ImportPlan {
	t.Helper()
	refs := make([]domain.ModReference, 0, len(ids))
	for _, id := range ids {
		refs = append(refs, domain.ModReference{SourceID: "repo", ModID: id, Version: "1.0.0"})
	}
	data, err := config.ExportProfile(&domain.Profile{Name: "target", GameID: fx.game.ID, Mods: refs})
	require.NoError(t, err)
	plan, err := fx.svc.PlanImport(context.Background(), fx.game, data)
	require.NoError(t, err)
	return plan
}

// declareLoader is the remedy a loader refusal names.
func (fx *acquireFixture) declareLoader(t *testing.T) {
	t.Helper()
	_, err := fx.svc.UpdateGameLoader(context.Background(), fx.game.ID, &core.LoaderSpec{Kind: domain.LoaderKindBepInEx})
	require.NoError(t, err)
	game, err := fx.svc.GetGame(fx.game.ID)
	require.NoError(t, err)
	fx.game = game
}

// requireNothingWritten asserts the three stores an Apply commits to hold
// nothing of profile "target" or of ids.
func (fx *acquireFixture) requireNothingWritten(t *testing.T, ids ...string) {
	t.Helper()
	_, err := fx.svc.NewProfileManager().Get(context.Background(), fx.game.ID, "target")
	assert.ErrorIs(t, err, domain.ErrProfileNotFound, "the profile was not saved")
	gc := fx.svc.GetGameCache(fx.game)
	for _, id := range ids {
		assert.False(t, gc.Exists(cache.Key{GameID: fx.game.ID, SourceID: "repo", ModID: id, Version: "1.0.0"}), "no cache entry for %s", id)
		for _, profile := range []string{"target", "default"} {
			_, err := fx.svc.GetInstalledMod(context.Background(), "repo", id, fx.game.ID, profile)
			assert.Error(t, err, "no installed row for %s in %s", id, profile)
		}
	}
}

// retainedEntries counts the archives kept for a re-run.
func (fx *acquireFixture) retainedEntries(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(fx.svc.DataDirForTest(), "downloads", "retained"))
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	require.NoError(t, err)
	return len(entries)
}

// TestApplyImport_ARefusalOnTheLastItemWritesNothing is #602's acceptance
// test and its round trip. The loader refusal is only discoverable once
// the LAST mod's archive is extracted; before the engine, the first mod
// and the profile were already saved by then, and the re-run failed with
// ErrStalePlan. Now nothing is written, the plan stays fresh, and once the
// user declares the loader the re-plan and re-run download nothing again.
func TestApplyImport_ARefusalOnTheLastItemWritesNothing(t *testing.T) {
	fx := newAcquireFixture(t, plainMod("plain", "Plain"), loaderMod("thing", "Thing"))
	plan := fx.importPlan(t, "plain", "thing")

	sink, seen := core.RecordEvents()
	result, err := fx.svc.ApplyImport(context.Background(), fx.game, plan, core.ProfileImportOptions{Install: true}, sink)

	// The document an incomplete import always had, marked refused.
	var incomplete *core.ProfileImportIncompleteError
	require.ErrorAs(t, err, &incomplete)
	var loaderErr *core.LoaderRequiredError
	require.ErrorAs(t, err, &loaderErr, "the refusal stays reachable, so the CLI prints its setup steps")
	assert.NotErrorIs(t, err, core.ErrStalePlan)
	require.NotNil(t, result)
	assert.True(t, result.Refused)
	require.NotNil(t, result.LoaderRequired)
	assert.Equal(t, "Thing", result.LoaderRequired.ModName)
	assert.NotEmpty(t, result.LoaderRequired.Setup)
	assert.Zero(t, result.Installed)
	assert.Equal(t, 2, result.Failed)
	require.Len(t, result.Failures, 2)
	assert.Equal(t, core.RefusalHeldBack, result.Failures[0].Refusal, "the clean mod was held back")
	assert.Equal(t, "plain", result.Failures[0].ModID)
	assert.Equal(t, core.RefusalLoaderRequired, result.Failures[1].Refusal, "and told apart from the one that refused")
	assert.Equal(t, "thing", result.Failures[1].ModID)

	fx.requireNothingWritten(t, "plain", "thing")
	phases, _ := phasesOf(*seen)
	assert.NotContains(t, phases, core.ImportSaved)
	assert.NotContains(t, phases, core.ImportModInstalled)
	assert.Equal(t, int64(1), fx.fetches("plain"))
	assert.Equal(t, int64(1), fx.fetches("thing"))
	assert.Equal(t, 2, fx.retainedEntries(t), "both archives are kept: the refused one and the held-back one")

	// The plan is still fresh: the same plan re-runs to the same refusal,
	// not ErrStalePlan, and downloads nothing.
	_, err = fx.svc.ApplyImport(context.Background(), fx.game, plan, core.ProfileImportOptions{Install: true}, nil)
	require.ErrorAs(t, err, &loaderErr)
	assert.NotErrorIs(t, err, core.ErrStalePlan)
	assert.Equal(t, int64(1), fx.fetches("plain"))
	assert.Equal(t, int64(1), fx.fetches("thing"))

	// The round trip: the user does what the error said, re-plans, re-runs.
	fx.declareLoader(t)
	plan = fx.importPlan(t, "plain", "thing")
	result, err = fx.svc.ApplyImport(context.Background(), fx.game, plan, core.ProfileImportOptions{Install: true}, nil)
	require.NoError(t, err)
	assert.Equal(t, 2, result.Installed)
	assert.False(t, result.Refused)
	assert.Equal(t, int64(1), fx.fetches("plain"), "no second download")
	assert.Equal(t, int64(1), fx.fetches("thing"), "no second download")
	assert.Zero(t, fx.retainedEntries(t), "the kept archives are dropped once committed")

	profile, err := fx.svc.NewProfileManager().Get(context.Background(), fx.game.ID, "target")
	require.NoError(t, err)
	assert.Len(t, profile.Mods, 2)
}

// Two mods that both need the loader are refused as ONE requirement naming
// both: the remedy is a single game edit.
func TestApplyImport_EveryLoaderRefusalIsNamed(t *testing.T) {
	fx := newAcquireFixture(t, loaderMod("one", "Mod One"), plainMod("plain", "Plain"), loaderMod("two", "Mod Two"))
	plan := fx.importPlan(t, "one", "plain", "two")

	result, err := fx.svc.ApplyImport(context.Background(), fx.game, plan, core.ProfileImportOptions{Install: true}, nil)
	var loaderErr *core.LoaderRequiredError
	require.ErrorAs(t, err, &loaderErr)
	assert.Equal(t, []string{"Mod One", "Mod Two"}, loaderErr.Mods)
	assert.Equal(t, "Mod One and Mod Two", loaderErr.ModName)
	assert.Contains(t, loaderErr.Error(), "Mod One and Mod Two need the BepInEx mod loader")
	require.NotNil(t, result.LoaderRequired)
	assert.Equal(t, []string{"Mod One", "Mod Two"}, result.LoaderRequired.Mods)

	codes := make([]string, 0, len(result.Failures))
	for _, f := range result.Failures {
		codes = append(codes, f.Refusal)
	}
	assert.Equal(t, []string{core.RefusalLoaderRequired, core.RefusalHeldBack, core.RefusalLoaderRequired}, codes)
	fx.requireNothingWritten(t, "one", "plain", "two")
}

// An UNANSWERABLE failure keeps today's per-item counting: a mod the
// source does not have fails on its own, and the rest of the import -
// profile included - is written.
func TestApplyImport_AnUnanswerableFailureIsCountedPerItem(t *testing.T) {
	fx := newAcquireFixture(t, plainMod("plain", "Plain"))
	plan := fx.importPlan(t, "ghost", "plain")

	result, err := fx.svc.ApplyImport(context.Background(), fx.game, plan, core.ProfileImportOptions{Install: true}, nil)
	var incomplete *core.ProfileImportIncompleteError
	require.ErrorAs(t, err, &incomplete)
	assert.False(t, result.Refused)
	assert.Nil(t, result.LoaderRequired)
	assert.Equal(t, 1, result.Installed)
	assert.Equal(t, 1, result.Failed)
	require.Len(t, result.Failures, 1)
	assert.Equal(t, "ghost", result.Failures[0].ModID)
	assert.Empty(t, result.Failures[0].Refusal, "an ordinary failure carries no refusal code")

	_, err = fx.svc.NewProfileManager().Get(context.Background(), fx.game.ID, "target")
	require.NoError(t, err, "the profile is saved")
	assert.True(t, fx.svc.GetGameCache(fx.game).Exists(cache.Key{GameID: fx.game.ID, SourceID: "repo", ModID: "plain", Version: "1.0.0"}))
}

// All-or-nothing (a collection apply's mode, C2) blocks on EVERY acquire
// failure, the unanswerable kind included, and is reachable through
// ApplyImport itself.
func TestApplyImport_AllOrNothingRefusesOnAnyAcquireFailure(t *testing.T) {
	fx := newAcquireFixture(t, plainMod("plain", "Plain"))
	plan := fx.importPlan(t, "plain", "ghost")

	_, err := fx.svc.ApplyImport(context.Background(), fx.game, plan,
		core.ProfileImportOptions{Install: true, AllOrNothing: true}, nil)
	var refused *core.AcquireRefusedError
	require.ErrorAs(t, err, &refused)
	require.Len(t, refused.Refusals, 1)
	assert.Equal(t, "ghost", refused.Refusals[0].ModID)
	assert.NotErrorIs(t, err, core.ErrStalePlan)
	fx.requireNothingWritten(t, "plain")
	assert.Equal(t, 1, fx.retainedEntries(t), "the clean mod's archive is kept for the re-run")

	// The same plan, without the mode, is the ordinary import: the clean
	// mod installs from the kept archive.
	result, err := fx.svc.ApplyImport(context.Background(), fx.game, plan, core.ProfileImportOptions{Install: true}, nil)
	var incomplete *core.ProfileImportIncompleteError
	require.ErrorAs(t, err, &incomplete)
	assert.Equal(t, 1, result.Installed)
	assert.Equal(t, int64(1), fx.fetches("plain"), "no second download")
}

// The mode-selection rule's behavioural half (acquire_engine.go):
// ApplyImport and ApplyInstall run PHASED - every item's Installing line
// before any item's Installed line - and the ratchet in
// acquire_engine_internal_test.go keeps INTERLEAVED to the update batch.
func TestAcquireModeSelection_InstallShapedAppliesArePhased(t *testing.T) {
	t.Run("ApplyImport", func(t *testing.T) {
		fx := newAcquireFixture(t, plainMod("a", "A"), plainMod("b", "B"))
		sink, seen := core.RecordEvents()
		_, err := fx.svc.ApplyImport(context.Background(), fx.game, fx.importPlan(t, "a", "b"), core.ProfileImportOptions{Install: true}, sink)
		require.NoError(t, err)
		phases, _ := phasesOf(*seen)
		lastInstalling := lastIndex(phases, core.ImportModInstalling)
		assert.Less(t, lastInstalling, slices.Index(phases, core.ImportSaved), "the profile is saved after every mod is acquired")
		assert.Less(t, lastInstalling, slices.Index(phases, core.ImportModInstalled))
	})
	t.Run("ApplyInstall", func(t *testing.T) {
		fx := newAcquireFixture(t, plainMod("a", "A"), plainMod("b", "B"))
		plan, err := fx.svc.PlanInstallMany(context.Background(), fx.game, "default",
			[]*domain.Mod{{ID: "a", SourceID: "repo", Name: "A", Version: "1.0.0", GameID: "valheim"},
				{ID: "b", SourceID: "repo", Name: "B", Version: "1.0.0", GameID: "valheim"}}, false)
		require.NoError(t, err)
		sink, seen := core.RecordEvents()
		_, err = fx.svc.ApplyInstall(context.Background(), fx.game, plan, core.InstallOptions{}, sink)
		require.NoError(t, err)
		phases, _ := phasesOf(*seen)
		assert.Less(t, lastIndex(phases, core.InstallDepInstalling), slices.Index(phases, core.InstallDepInstalled))
	})
}

func lastIndex(phases []core.DeployPhase, p core.DeployPhase) int {
	last := -1
	for i, q := range phases {
		if q == p {
			last = i
		}
	}
	return last
}

// The install batch refuses the same way, in the document a batch with
// failed mods already had: exit-0-shaped (no error), every mod in Failed
// with its code, nothing written, and no hook run.
func TestApplyInstall_Batch_ALoaderRefusalWritesNothing(t *testing.T) {
	fx := newAcquireFixture(t, plainMod("plain", "Plain"), loaderMod("thing", "Thing"))
	scripts := t.TempDir()
	marker := filepath.Join(scripts, "ran")
	hook := createTestScript(t, scripts, "hook.sh", "#!/bin/bash\necho ran >> "+marker+"\n")
	seedHooks(t, fx.svc, fx.game, "default", domain.GameHooks{Install: domain.HookConfig{BeforeAll: hook, BeforeEach: hook}})

	mods := []*domain.Mod{
		{ID: "plain", SourceID: "repo", Name: "Plain", Version: "1.0.0", GameID: "valheim"},
		{ID: "thing", SourceID: "repo", Name: "Thing", Version: "1.0.0", GameID: "valheim"},
	}
	plan, err := fx.svc.PlanInstallMany(context.Background(), fx.game, "default", mods, false)
	require.NoError(t, err)
	result, err := fx.svc.ApplyInstall(context.Background(), fx.game, plan, core.InstallOptions{}, nil)
	require.NoError(t, err, "a refused batch keeps the batch's document, not an error")

	assert.True(t, result.Refused)
	require.NotNil(t, result.LoaderRequired)
	assert.Empty(t, result.Installed)
	require.Len(t, result.Failed, 2)
	assert.Equal(t, core.RefusalHeldBack, result.Failed[0].Refusal)
	assert.Equal(t, core.RefusalLoaderRequired, result.Failed[1].Refusal)
	assert.Equal(t, result.Failed, result.Skipped)
	_, statErr := os.Stat(marker)
	assert.True(t, os.IsNotExist(statErr), "no install hook runs for an install that writes nothing")
	gc := fx.svc.GetGameCache(fx.game)
	for _, id := range []string{"plain", "thing"} {
		assert.False(t, gc.Exists(cache.Key{GameID: fx.game.ID, SourceID: "repo", ModID: id, Version: "1.0.0"}))
		_, err := fx.svc.GetInstalledMod(context.Background(), "repo", id, fx.game.ID, "default")
		assert.Error(t, err)
	}

	// The round trip, as with import.
	fx.declareLoader(t)
	plan, err = fx.svc.PlanInstallMany(context.Background(), fx.game, "default", mods, false)
	require.NoError(t, err)
	result, err = fx.svc.ApplyInstall(context.Background(), fx.game, plan, core.InstallOptions{}, nil)
	require.NoError(t, err)
	assert.Len(t, result.Installed, 2)
	assert.Equal(t, int64(1), fx.fetches("plain"))
	assert.Equal(t, int64(1), fx.fetches("thing"))
}

// Cancellation in the COMMIT phase keeps Ruling 16's pairing: the item in
// flight finishes both its DB row and its profile ref, and nothing after
// it is written. Cancellation in the ACQUIRE phase writes nothing at all.
func TestApplyImport_CancellationKeepsTheRuling16Pairing(t *testing.T) {
	t.Run("during commit", func(t *testing.T) {
		fx := newAcquireFixture(t, plainMod("a", "A"), plainMod("b", "B"))
		plan := fx.importPlan(t, "a", "b")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		_, err := fx.svc.ApplyImport(ctx, fx.game, plan, core.ProfileImportOptions{Install: true}, func(e core.Event) {
			if fe, ok := e.(core.FlowEvent); ok && fe.FlowPhase() == core.ImportModInstalled {
				cancel()
			}
		})
		require.ErrorIs(t, err, context.Canceled)

		profile, perr := fx.svc.NewProfileManager().Get(context.Background(), fx.game.ID, "target")
		require.NoError(t, perr)
		_, rowErr := fx.svc.GetInstalledMod(context.Background(), "repo", "a", fx.game.ID, "target")
		require.NoError(t, rowErr, "a's row is written")
		// The saved document lists both mods; the commit's paired profile
		// write is what records a mod's installed file ids.
		require.NotNil(t, profile.FindRef("repo", "a"))
		assert.Equal(t, []string{"a-main"}, profile.FindRef("repo", "a").FileIDs, "and so is its paired profile write")
		_, rowErr = fx.svc.GetInstalledMod(context.Background(), "repo", "b", fx.game.ID, "target")
		assert.Error(t, rowErr, "b was never committed")
		require.NotNil(t, profile.FindRef("repo", "b"))
		assert.Empty(t, profile.FindRef("repo", "b").FileIDs)
		assert.False(t, fx.svc.GetGameCache(fx.game).Exists(cache.Key{GameID: fx.game.ID, SourceID: "repo", ModID: "b", Version: "1.0.0"}))
	})
	t.Run("during acquire", func(t *testing.T) {
		fx := newAcquireFixture(t, plainMod("a", "A"), plainMod("b", "B"))
		plan := fx.importPlan(t, "a", "b")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		_, err := fx.svc.ApplyImport(ctx, fx.game, plan, core.ProfileImportOptions{Install: true}, func(e core.Event) {
			if fe, ok := e.(core.FlowEvent); ok && fe.FlowPhase() == core.ImportDownloadDone {
				cancel()
			}
		})
		require.ErrorIs(t, err, context.Canceled)
		fx.requireNothingWritten(t, "a", "b")
		assert.Zero(t, fx.retainedEntries(t), "a cancellation is not a refusal: nothing is kept")
	})
}
