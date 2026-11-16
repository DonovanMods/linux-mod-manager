package core_test

// #661: a profile import's and a collection finish's install loops write
// their profile refs a batch at a time. These tests hold the flows to it:
// the save count (a ratchet that counts, never times), Ruling 16 under a
// cancellation, a failure and a panic mid-batch, and the document the
// flow ends with, which must be the per-item writes' to the byte.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// refBatchItems mirrors core's refBatchItems: the ratchets' arithmetic.
const refBatchItems = 25

// maxBatchedSaves is the most profile saves an n-item loop may make in
// batches of refBatchItems, plus extra for the flow's own writes around
// the loop (the imported document, a finish's pin rewrite, its to-dos).
func maxBatchedSaves(n, extra int) int64 {
	return int64((n+refBatchItems-1)/refBatchItems + extra)
}

func modIDs(prefix string, n int) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("%s%03d", prefix, i)
	}
	return ids
}

// requireEveryRowHasItsRef asserts Ruling 16 over profile: each
// installed row's profile ref records the files the row installed.
func requireEveryRowHasItsRef(t *testing.T, svc *core.Service, gameID, profile string) int {
	t.Helper()
	rows, err := svc.GetInstalledMods(context.Background(), gameID, profile)
	require.NoError(t, err)
	saved, err := config.LoadProfile(svc.ConfigDir(), gameID, profile)
	require.NoError(t, err)
	for _, row := range rows {
		ref := saved.FindRef(row.SourceID, row.ID)
		require.NotNil(t, ref, "%s has a row and no ref", row.ID)
		assert.Equal(t, row.FileIDs, ref.FileIDs, "%s's ref records the files its row installed", row.ID)
		assert.Equal(t, row.Version, ref.Version, "%s's ref records its row's version", row.ID)
	}
	return len(rows)
}

// TestApplyImport_ProfileSavesAreBatched is #661's ratchet: a 300-item
// import saves its profile once for the document and once per batch of
// refs, not once per item.
func TestApplyImport_ProfileSavesAreBatched(t *testing.T) {
	ids := modIDs("m", 300)
	mods := make([]acquireMod, len(ids))
	for i, id := range ids {
		mods[i] = plainMod(id, "Mod "+id)
	}
	fx := newAcquireFixture(t, mods...)
	plan := fx.importPlan(t, ids...)

	before := fx.svc.ProfileSavesForTest()
	result, err := fx.svc.ApplyImport(context.Background(), fx.game, plan, core.ProfileImportOptions{Install: true}, nil)
	require.NoError(t, err)
	require.Equal(t, 300, result.Installed)
	saves := fx.svc.ProfileSavesForTest() - before
	assert.LessOrEqual(t, saves, maxBatchedSaves(300, 2), "one save for the document, one per batch of refs")
	assert.Equal(t, 300, requireEveryRowHasItsRef(t, fx.svc, fx.game.ID, "target"))
}

// TestApplyImport_CancelledMidBatch_EveryRowHasItsRef is Ruling 16 under
// the batch: a cancellation after the fourth of ten items (a batch holds
// 25) still leaves every installed row's ref on disk.
func TestApplyImport_CancelledMidBatch_EveryRowHasItsRef(t *testing.T) {
	ids := modIDs("c", 10)
	mods := make([]acquireMod, len(ids))
	for i, id := range ids {
		mods[i] = plainMod(id, "Mod "+id)
	}
	fx := newAcquireFixture(t, mods...)
	plan := fx.importPlan(t, ids...)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	installed := 0
	_, err := fx.svc.ApplyImport(ctx, fx.game, plan, core.ProfileImportOptions{Install: true}, func(e core.Event) {
		if fe, ok := e.(core.FlowEvent); ok && fe.FlowPhase() == core.ImportModInstalled {
			if installed++; installed == 4 {
				cancel()
			}
		}
	})
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 4, requireEveryRowHasItsRef(t, fx.svc, fx.game.ID, "target"), "four items committed, each with its ref")
}

// TestApplyImport_AFailedItemMidBatch_TheOthersRefsAreWritten pins an
// item that fails at commit (its deploy cannot place its file) in the middle
// of a batch: the items before and after it are installed, and their refs
// are on disk when the flow returns.
func TestApplyImport_AFailedItemMidBatch_TheOthersRefsAreWritten(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes into a read-only directory")
	}
	ids := modIDs("f", 10)
	mods := make([]acquireMod, len(ids))
	for i, id := range ids {
		mods[i] = plainMod(id, "Mod "+id)
	}
	// f004 deploys into a directory lmm cannot write.
	mods[4].members = map[string]string{"sealed/f004.txt": "payload"}
	fx := newAcquireFixture(t, mods...)
	sealed := filepath.Join(fx.game.ModPath, "sealed")
	require.NoError(t, os.MkdirAll(sealed, 0o555))
	t.Cleanup(func() { _ = os.Chmod(sealed, 0o755) })
	plan := fx.importPlan(t, ids...)
	result, err := fx.svc.ApplyImport(context.Background(), fx.game, plan, core.ProfileImportOptions{Install: true}, nil)
	var incomplete *core.ProfileImportIncompleteError
	require.ErrorAs(t, err, &incomplete)
	require.Equal(t, 1, result.Failed)
	assert.Equal(t, "f004", result.Failures[0].ModID)
	assert.Equal(t, 9, requireEveryRowHasItsRef(t, fx.svc, fx.game.ID, "target"))
	saved, err := config.LoadProfile(fx.svc.ConfigDir(), fx.game.ID, "target")
	require.NoError(t, err)
	assert.Empty(t, saved.FindRef("repo", "f004").FileIDs, "the failed item's ref is the document's, unwritten")
}

// TestApplyImport_APanicMidBatch_TheEarlierRefsAreWritten pins the flow's
// deferred flush: a commit that panics part-way through a batch still
// leaves the refs of the items committed before it on disk.
func TestApplyImport_APanicMidBatch_TheEarlierRefsAreWritten(t *testing.T) {
	ids := modIDs("p", 6)
	mods := make([]acquireMod, len(ids))
	for i, id := range ids {
		mods[i] = plainMod(id, "Mod "+id)
	}
	fx := newAcquireFixture(t, mods...)
	plan := fx.importPlan(t, ids...)
	fx.svc.SetAfterDeployLinkForTest(func(file string) {
		if filepath.Base(file) == "p003.txt" {
			panic("boom")
		}
	})
	func() {
		defer func() { require.Equal(t, "boom", recover()) }()
		_, _ = fx.svc.ApplyImport(context.Background(), fx.game, plan, core.ProfileImportOptions{Install: true}, nil)
	}()
	fx.svc.SetAfterDeployLinkForTest(nil)
	assert.Equal(t, 3, requireEveryRowHasItsRef(t, fx.svc, fx.game.ID, "target"), "p000-p002 committed before the panic")
}

// TestApplyImport_BatchedDocumentIsThePerItemOne holds a whole flow to
// the per-item writes: the same 60-item import, saved after every item
// (a batch of 1) and in batches, leaves the same profile file to the byte.
func TestApplyImport_BatchedDocumentIsThePerItemOne(t *testing.T) {
	run := func(items int) []byte {
		f := newManyCollectionFixture(t, 60, nil)
		f.svc.SetRefBatchItemsForTest(items)
		_, err := f.svc.ApplyCollectionImport(context.Background(), f.game, f.plan(t, core.SourceCollectionImportRequest{}), core.ProfileImportOptions{Install: true}, nil)
		require.NoError(t, err)
		path, err := config.ProfilePath(f.svc.ConfigDir(), f.game.ID, manyCollectionName)
		require.NoError(t, err)
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		return data
	}
	perItem, batched := run(1), run(0)
	assert.Equal(t, string(perItem), string(batched))
}

// TestCollectionImport_ProfileSavesAreBatched is the ratchet over a
// collection import: the imported document, a batch of refs at a time,
// and the to-dos.
func TestCollectionImport_ProfileSavesAreBatched(t *testing.T) {
	f := newManyCollectionFixture(t, 100, nil)
	plan := f.plan(t, core.SourceCollectionImportRequest{})
	before := f.svc.ProfileSavesForTest()
	result, err := f.svc.ApplyCollectionImport(context.Background(), f.game, plan, core.ProfileImportOptions{Install: true}, nil)
	require.NoError(t, err)
	require.Equal(t, 100, result.Installed)
	assert.LessOrEqual(t, f.svc.ProfileSavesForTest()-before, maxBatchedSaves(100, 3))
	assert.Equal(t, 100, requireEveryRowHasItsRef(t, f.svc, f.game.ID, manyCollectionName))
}

// TestCollectionFinish_ProfileSavesAreBatched is the ratchet over `lmm
// profile finish`: 60 failed downloads retried in one finish, one of which
// fails again - its new to-do, written after the loop, is not overwritten
// by the batch.
func TestCollectionFinish_ProfileSavesAreBatched(t *testing.T) {
	ids := modIDs("2", 60)
	f := newManyCollectionFixture(t, 60, errors.New("HTTP 503 from the CDN"))
	_, err := f.svc.ApplyCollectionImport(context.Background(), f.game, f.plan(t, core.SourceCollectionImportRequest{}), core.ProfileImportOptions{Install: true}, nil)
	var incomplete *core.ProfileImportIncompleteError
	require.ErrorAs(t, err, &incomplete)
	again := ids[30]
	f.src.urlErrs = map[string]error{again: errors.New("HTTP 502 this time")}

	retry := make([]string, len(ids))
	for i, id := range ids {
		retry[i] = "nexusmods:" + id
	}
	plan, err := f.svc.PlanCollectionFinish(context.Background(), f.game, core.CollectionFinishRequest{Profile: manyCollectionName, Retry: retry})
	require.NoError(t, err)
	before := f.svc.ProfileSavesForTest()
	result, err := f.svc.ApplyCollectionFinish(context.Background(), f.game, plan, core.CollectionFinishOptions{}, nil)
	var unfinished *core.CollectionFinishIncompleteError
	require.ErrorAs(t, err, &unfinished)
	require.Equal(t, 59, result.Installed)
	require.Len(t, result.Todos, 1)
	assert.Contains(t, result.Todos[0].Detail, "HTTP 502 this time")
	assert.LessOrEqual(t, f.svc.ProfileSavesForTest()-before, maxBatchedSaves(60, 2))
	assert.Equal(t, 59, requireEveryRowHasItsRef(t, f.svc, f.game.ID, manyCollectionName))
	saved, err := config.LoadProfile(f.svc.ConfigDir(), f.game.ID, manyCollectionName)
	require.NoError(t, err)
	require.NotNil(t, saved.FindRef("nexusmods", again).Todo)
	assert.Contains(t, saved.FindRef("nexusmods", again).Todo.Detail, "HTTP 502 this time", "the finish's to-do is on disk")
}

// TestCollectionFinish_CancelledMidBatch_EveryRowHasItsRef is Ruling 16
// under the finish's batch: every item installed before the cancellation
// has its ref, its to-do cleared; the rest keep theirs.
func TestCollectionFinish_CancelledMidBatch_EveryRowHasItsRef(t *testing.T) {
	ids := modIDs("2", 10)
	f := newManyCollectionFixture(t, 10, errors.New("HTTP 503 from the CDN"))
	_, err := f.svc.ApplyCollectionImport(context.Background(), f.game, f.plan(t, core.SourceCollectionImportRequest{}), core.ProfileImportOptions{Install: true}, nil)
	var incomplete *core.ProfileImportIncompleteError
	require.ErrorAs(t, err, &incomplete)
	f.src.urlErrs = nil

	retry := make([]string, len(ids))
	for i, id := range ids {
		retry[i] = "nexusmods:" + id
	}
	plan, err := f.svc.PlanCollectionFinish(context.Background(), f.game, core.CollectionFinishRequest{Profile: manyCollectionName, Retry: retry})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	installed := 0
	_, err = f.svc.ApplyCollectionFinish(ctx, f.game, plan, core.CollectionFinishOptions{}, func(e core.Event) {
		if fe, ok := e.(core.FlowEvent); ok && fe.FlowPhase() == core.ImportModInstalled {
			if installed++; installed == 3 {
				cancel()
			}
		}
	})
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 3, requireEveryRowHasItsRef(t, f.svc, f.game.ID, manyCollectionName))
	saved, err := config.LoadProfile(f.svc.ConfigDir(), f.game.ID, manyCollectionName)
	require.NoError(t, err)
	for _, id := range ids {
		if _, rowErr := f.svc.GetInstalledMod(context.Background(), "nexusmods", id, f.game.ID, manyCollectionName); rowErr == nil {
			assert.Nil(t, saved.FindRef("nexusmods", id).Todo, "%s is installed: its to-do is resolved", id)
		} else {
			assert.NotNil(t, saved.FindRef("nexusmods", id).Todo, "%s was never installed: its to-do stays", id)
		}
	}
}

// manyCollectionName is newManyCollectionFixture's collection, and so its
// profile's name.
const manyCollectionName = "Many Mods"

// newManyCollectionFixture is newSourceImportFixture's NexusMods
// collection with n items (mod ids 2000 up, one file each), every
// download failing with urlErr when it is set.
func newManyCollectionFixture(t *testing.T, n int, urlErr error) *sourceImportFixture {
	t.Helper()
	ids := modIDs("2", n)
	type recipeMod struct {
		Name   string `json:"name"`
		ModID  string `json:"modId"`
		FileID string `json:"fileId"`
	}
	recipe := struct {
		Name string      `json:"name"`
		Mods []recipeMod `json:"mods"`
	}{Name: manyCollectionName}
	items := make([]source.CollectionItem, 0, n)
	files := make(map[string][]string, n)
	for _, id := range ids {
		recipe.Mods = append(recipe.Mods, recipeMod{Name: "Mod " + id, ModID: id, FileID: id + "0"})
		items = append(items, source.CollectionItem{Kind: source.CollectionItemModFile, ModID: id, FileID: id + "0", Name: "Mod " + id, FileCategory: "MAIN"})
		files[id] = []string{id + "0"}
	}
	recipeJSON, err := json.Marshal(recipe)
	require.NoError(t, err)
	archive := recipeArchive(t, string(recipeJSON))
	cdn := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(archive) }))
	t.Cleanup(cdn.Close)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(), DownloadClient: cdn.Client(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	downloads := newMockSourceWithDownloads("nexusmods")
	t.Cleanup(downloads.Close)
	src := &collectionSource{
		mockSourceWithDownloads: downloads,
		recipe: &recipeSource{mockSource: downloads.mockSource, archiveURL: cdn.URL + "/recipe.7z",
			collection: source.Collection{ID: "abcdef", Name: manyCollectionName, Revision: 1, Author: "curator",
				URL: "https://www.nexusmods.com/games/skyrimspecialedition/collections/abcdef", Handle: "/h", Items: items}},
		tier:       source.TierPremium,
		filesByMod: files,
	}
	if urlErr != nil {
		src.urlErrs = make(map[string]error, n)
	}
	for _, id := range ids {
		downloads.AddMod("skyrimspecialedition", &domain.Mod{ID: id, SourceID: "nexusmods", Name: "Mod " + id, Version: "1", GameID: "skyrimspecialedition"})
		downloads.AddDownload(id+"0", createTestZipBytes(t, map[string]string{"mod" + id + ".txt": "payload " + id}))
		if urlErr != nil {
			src.urlErrs[id] = urlErr
		}
	}
	svc.RegisterSource(src)
	game := &domain.Game{ID: "skyrim", Name: "Skyrim", ModPath: t.TempDir(), LinkMethod: domain.LinkCopy,
		SourceIDs: map[string]string{"nexusmods": "skyrimspecialedition"}}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	return &sourceImportFixture{svc: svc, game: game, src: src}
}

// TestImportBookkeepingTiming is #661's measurement, not a test: with
// LMM_PERF_661 set it times a profile import of 40 to 600 plain mods from
// a local manifest source (no real downloads) and logs each run's
// duration and profile saves. Run it without -race:
//
//	LMM_PERF_661=1 go test ./internal/core -run TestImportBookkeepingTiming -v
func TestImportBookkeepingTiming(t *testing.T) {
	if os.Getenv("LMM_PERF_661") == "" {
		t.Skip("set LMM_PERF_661=1 to measure")
	}
	for _, n := range []int{40, 150, 300, 600} {
		ids := modIDs("t", n)
		mods := make([]acquireMod, len(ids))
		for i, id := range ids {
			mods[i] = plainMod(id, "Mod "+id)
		}
		fx := newAcquireFixture(t, mods...)
		plan := fx.importPlan(t, ids...)
		before := fx.svc.ProfileSavesForTest()
		start := time.Now()
		result, err := fx.svc.ApplyImport(context.Background(), fx.game, plan, core.ProfileImportOptions{Install: true}, nil)
		elapsed := time.Since(start)
		require.NoError(t, err)
		require.Equal(t, n, result.Installed)
		t.Logf("items=%d import=%.2fs saves=%d", n, elapsed.Seconds(), fx.svc.ProfileSavesForTest()-before)
	}
}

// newManyFillFixture is newFillFixture with n pending pins (mod ids 3000
// up), every one's archive in the downloads directory.
func newManyFillFixture(t *testing.T, n int) (*fillFixture, []string) {
	t.Helper()
	ids := modIDs("3", n)
	refs := make([]domain.ModReference, n)
	for i, id := range ids {
		refs[i] = domain.ModReference{SourceID: "nexusmods", ModID: id, Version: "1", FileIDs: []string{id + "0"}}
	}
	f := newFillFixture(t, fillSetup{refs: refs})
	for _, id := range ids {
		f.download(t, id, id+".zip")
	}
	return f, ids
}

// TestApplyProfileFill_ProfileSavesAreBatched is the ratchet over `lmm
// profile fill`, which installs through the import's commit.
func TestApplyProfileFill_ProfileSavesAreBatched(t *testing.T) {
	f, _ := newManyFillFixture(t, 40)
	plan := f.plan(t)
	require.Len(t, plan.Matched, 40)
	before := f.svc.ProfileSavesForTest()
	result, err := f.svc.ApplyProfileFill(context.Background(), f.game, plan, core.ProfileFillOptions{}, nil)
	require.NoError(t, err)
	require.Equal(t, 40, result.Installed)
	assert.LessOrEqual(t, f.svc.ProfileSavesForTest()-before, maxBatchedSaves(40, 0))
	assert.Equal(t, 40, requireEveryRowHasItsRef(t, f.svc, f.game.ID, "Coll"))
}

// TestApplyProfileFill_CancelledMidBatch_EveryRowHasItsRef is Ruling 16
// under the fill's batch.
func TestApplyProfileFill_CancelledMidBatch_EveryRowHasItsRef(t *testing.T) {
	f, _ := newManyFillFixture(t, 8)
	plan := f.plan(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	installed := 0
	_, err := f.svc.ApplyProfileFill(ctx, f.game, plan, core.ProfileFillOptions{}, func(e core.Event) {
		if fe, ok := e.(core.FlowEvent); ok && fe.FlowPhase() == core.ImportModInstalled {
			if installed++; installed == 3 {
				cancel()
			}
		}
	})
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 3, requireEveryRowHasItsRef(t, f.svc, f.game.ID, "Coll"))
}

// TestApplyBookkeepingTiming measures, with LMM_PERF_661 set, `lmm profile
// apply` of a profile listing n plain mods, none installed: a converge
// that still writes one ref per item, which #661 leaves for a follow-up.
func TestApplyBookkeepingTiming(t *testing.T) {
	if os.Getenv("LMM_PERF_661") == "" {
		t.Skip("set LMM_PERF_661=1 to measure")
	}
	for _, n := range []int{150, 300, 600} {
		ids := modIDs("a", n)
		mods := make([]acquireMod, len(ids))
		refs := make([]domain.ModReference, len(ids))
		for i, id := range ids {
			mods[i] = plainMod(id, "Mod "+id)
			refs[i] = domain.ModReference{SourceID: "repo", ModID: id, Version: "1.0.0"}
		}
		fx := newAcquireFixture(t, mods...)
		require.NoError(t, config.SaveProfile(fx.svc.ConfigDir(), &domain.Profile{Name: "default", GameID: fx.game.ID, IsDefault: true, Mods: refs}))
		plan, err := fx.svc.PlanProfileApply(context.Background(), fx.game, "default")
		require.NoError(t, err)
		before := fx.svc.ProfileSavesForTest()
		start := time.Now()
		result, err := fx.svc.ApplyProfileApply(context.Background(), fx.game, plan, core.ProfileApplyOptions{}, nil)
		elapsed := time.Since(start)
		require.NoError(t, err)
		require.Equal(t, n, result.Installed)
		t.Logf("items=%d apply=%.2fs saves=%d", n, elapsed.Seconds(), fx.svc.ProfileSavesForTest()-before)
	}
}

// TestApplyImport_OverridesReadOnceStillPlaceEveryCopy is the import
// loop's override read (#661: once per loop, not per mod): every mod the
// imported profile overrides a file of deploys the overridden copy.
func TestApplyImport_OverridesReadOnceStillPlaceEveryCopy(t *testing.T) {
	ids := modIDs("o", 4)
	mods := make([]acquireMod, len(ids))
	refs := make([]domain.ModReference, len(ids))
	for i, id := range ids {
		mods[i] = plainMod(id, "Mod "+id)
		refs[i] = domain.ModReference{SourceID: "repo", ModID: id, Version: "1.0.0"}
	}
	fx := newAcquireFixture(t, mods...)
	overrides := map[string][]byte{"o001.txt": []byte("mine 1"), "o003.txt": []byte("mine 3")}
	data, err := config.ExportProfile(&domain.Profile{Name: "target", GameID: fx.game.ID, Mods: refs, Overrides: overrides})
	require.NoError(t, err)
	plan, err := fx.svc.PlanImport(context.Background(), fx.game, data)
	require.NoError(t, err)
	_, err = fx.svc.ApplyImport(context.Background(), fx.game, plan, core.ProfileImportOptions{Install: true, AllowAnyOverrides: true}, nil)
	require.NoError(t, err)
	for _, id := range ids {
		path := filepath.Join(fx.game.ModPath, id+".txt")
		info, err := os.Lstat(path)
		require.NoError(t, err)
		got, err := os.ReadFile(path)
		require.NoError(t, err)
		if want, ok := overrides[id+".txt"]; ok {
			assert.True(t, info.Mode().IsRegular(), "%s is the overridden copy, not a link", id)
			assert.Equal(t, string(want), string(got))
		} else {
			assert.Equal(t, os.ModeSymlink, info.Mode()&os.ModeSymlink, "%s is linked", id)
			assert.Equal(t, "payload "+id, string(got))
		}
	}
}
