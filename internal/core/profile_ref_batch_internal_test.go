package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/fomod"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// refBatchProfile is a hand-written profile (comments and all, so the
// in-place save's layout keeping is exercised) holding every kind of ref
// an install loop's upsert treats specially: a locked one, one with FOMOD
// choices, one per-file FOMOD, one a collection brought in and one it
// substituted, one with a to-do, one turned off, and one listed twice.
const refBatchProfile = `# my collection profile
name: col
game_id: g1
mods:
    # the lock's target is 1.0
    - source_id: src
      mod_id: locked
      version: "1.0"
      locked: true
    - source_id: src
      mod_id: lockedsame
      version: "2.0"
      file_ids: [ls-1]
      locked: true
    - source_id: src
      mod_id: fomodkeep
      version: "1.0"
      fomod:
        config_sha256: aaaa
        steps:
            - name: Main
              groups:
                - name: Texture
                  choices:
                    - name: 2K
    - source_id: src
      mod_id: fomodclear
      version: "1.0"
      fomod:
        config_sha256: bbbb
        steps:
            - name: Main
              groups:
                - name: Texture
                  choices:
                    - name: 2K
    - source_id: src
      mod_id: fomodset
      version: "1.0"
    - source_id: src
      mod_id: collected
      version: "1.0"
      from_collection: true
    - source_id: src
      mod_id: substituted
      version: "1.1"
      file_ids: [sub-2]
      from_collection: true
      substituted_for:
        version: "1.0"
        file_ids: [sub-1]
    - source_id: src
      mod_id: todo
      version: "1.0"
      file_ids: [todo-1]
      from_collection: true
      todo:
        reason: download_failed
        name: Todo Mod
        detail: HTTP 503
    - source_id: src
      mod_id: off
      version: "1.0"
      disabled: true # the user turned it off
    - source_id: src
      mod_id: twice
      version: "1.0"
    - source_id: src
      mod_id: twice
      version: "0.9"
`

// refBatchUpsert is one item's upsert in an install loop.
type refBatchUpsert struct {
	ref domain.ModReference
	rec fomodRecord
}

func refBatchUpserts() []refBatchUpsert {
	choices := func(option string) domain.FomodChoices {
		return domain.FomodChoices{ConfigSHA256: "cccc", Steps: []fomod.StepChoice{{Name: "Main", Groups: []fomod.GroupChoice{
			{Name: "Texture", Choices: []fomod.OptionChoice{{Name: option}}},
		}}}}
	}
	keep := fomodRecord{op: fomodKeep}
	return []refBatchUpsert{
		{domain.ModReference{SourceID: "src", ModID: "fomodkeep", Version: "1.1", FileIDs: []string{"fk-2"}}, keep},
		// A locked ref at another version refuses; the batch goes on.
		{domain.ModReference{SourceID: "src", ModID: "locked", Version: "2.0", FileIDs: []string{"l-2"}}, keep},
		{domain.ModReference{SourceID: "src", ModID: "lockedsame", Version: "2.0", FileIDs: []string{"ls-2"}}, keep},
		{domain.ModReference{SourceID: "src", ModID: "fomodclear", Version: "1.0", FileIDs: []string{"fc-1"}}, fomodRecord{op: fomodClear}},
		{domain.ModReference{SourceID: "src", ModID: "fomodset", Version: "1.0", FileIDs: []string{"fs-1"}}, fomodRecord{op: fomodSet, rec: domain.FomodRecord{Choices: choices("4K")}}},
		{domain.ModReference{SourceID: "src", ModID: "collected", Version: "1.2"}, fomodRecord{op: fomodSet, rec: domain.FomodRecord{
			Files: domain.FomodFiles{"c-1": choices("1K"), "c-2": choices("8K")}, Order: []string{"c-2"}}}},
		{domain.ModReference{SourceID: "src", ModID: "substituted", Version: "1.1", FileIDs: []string{"sub-2"}}, keep},
		{domain.ModReference{SourceID: "src", ModID: "todo", Version: "1.0", FileIDs: []string{"todo-1"}}, keep},
		{domain.ModReference{SourceID: "src", ModID: "off", Version: "1.5"}, keep},
		{domain.ModReference{SourceID: "src", ModID: "twice", Version: "1.0", FileIDs: []string{"tw-1"}}, keep},
		// A mod the document does not list is appended.
		{domain.ModReference{SourceID: "src", ModID: "fresh", Version: "3.0", FileIDs: []string{"fr-1"}, Category: "UI"}, keep},
		{domain.ModReference{SourceID: "src", ModID: "fomodset", Version: "1.0", FileIDs: []string{"fs-1", "fs-2"}}, keep},
	}
}

func writeRefBatchProfile(t *testing.T) (configDir, path string) {
	t.Helper()
	configDir = t.TempDir()
	path, err := config.ProfilePath(configDir, "g1", "col")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(refBatchProfile), 0o644))
	return configDir, path
}

// TestProfileRefBatch_EndsWithThePerItemDocument holds the batch to the
// per-item writes it replaces (#661): the same upserts, through upsertMod
// one at a time and through a batch at every size, leave byte-identical
// profile files and the same errors, the lock refusal included.
func TestProfileRefBatch_EndsWithThePerItemDocument(t *testing.T) {
	ctx := context.Background()
	upserts := refBatchUpserts()

	wantDir, wantPath := writeRefBatchProfile(t)
	pm := NewProfileManager(wantDir, nil)
	var wantErrs []string
	for _, u := range upserts {
		err := pm.upsertMod(ctx, "g1", "col", u.ref, u.rec)
		wantErrs = append(wantErrs, errText(err))
	}
	require.Contains(t, wantErrs[1], "refusing to record v2.0", "the fixture's locked ref refuses")
	want, err := os.ReadFile(wantPath)
	require.NoError(t, err)

	for _, items := range []int{1, 2, 3, 5, len(upserts), 100} {
		dir, path := writeRefBatchProfile(t)
		b := &profileRefBatch{pm: NewProfileManager(dir, nil), gameID: "g1", profileName: "col",
			items: items, window: time.Hour, now: time.Now}
		var errs []string
		for _, u := range upserts {
			errs = append(errs, errText(b.upsert(ctx, u.ref, u.rec)))
		}
		require.NoError(t, b.flush())
		got, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, wantErrs, errs, "batch of %d: the same refusals", items)
		assert.Equal(t, string(want), string(got), "batch of %d: the same document", items)
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// TestProfileRefBatch_SavesAtItsBounds pins when a batch writes: at its
// count, once its oldest pending upsert is refBatchWindow old, at once for
// an upsert that appends a ref (the order changed, and the flow's own
// reads of the file go by order), and never for a refusal.
func TestProfileRefBatch_SavesAtItsBounds(t *testing.T) {
	ctx := context.Background()
	ref := func(id, version string) domain.ModReference {
		return domain.ModReference{SourceID: "src", ModID: id, Version: version}
	}
	keep := fomodRecord{op: fomodKeep}
	newBatch := func(t *testing.T) (*profileRefBatch, *atomic.Int64, *time.Time) {
		dir, _ := writeRefBatchProfile(t)
		pm := NewProfileManager(dir, nil)
		saves := &atomic.Int64{}
		pm.saves = saves
		clock := time.Unix(1_000_000, 0)
		return &profileRefBatch{pm: pm, gameID: "g1", profileName: "col", items: 3, window: 2 * time.Second,
			now: func() time.Time { return clock }}, saves, &clock
	}

	t.Run("count", func(t *testing.T) {
		b, saves, _ := newBatch(t)
		for i, id := range []string{"fomodkeep", "fomodset", "collected", "todo", "off"} {
			require.NoError(t, b.upsert(ctx, ref(id, "9"), keep), "upsert %d", i)
		}
		assert.Equal(t, int64(1), saves.Load(), "the third upsert saves; the fourth and fifth are pending")
		require.NoError(t, b.flush())
		assert.Equal(t, int64(2), saves.Load())
		require.NoError(t, b.flush())
		assert.Equal(t, int64(2), saves.Load(), "nothing pending, nothing written")
	})
	t.Run("window", func(t *testing.T) {
		b, saves, clock := newBatch(t)
		require.NoError(t, b.upsert(ctx, ref("fomodkeep", "9"), keep))
		*clock = clock.Add(1999 * time.Millisecond)
		require.NoError(t, b.upsert(ctx, ref("fomodset", "9"), keep))
		assert.Zero(t, saves.Load())
		*clock = clock.Add(time.Millisecond)
		require.NoError(t, b.upsert(ctx, ref("collected", "9"), keep))
		assert.Equal(t, int64(1), saves.Load(), "the oldest pending upsert is 2s old")
	})
	t.Run("append", func(t *testing.T) {
		b, saves, _ := newBatch(t)
		require.NoError(t, b.upsert(ctx, ref("fomodkeep", "9"), keep))
		require.NoError(t, b.upsert(ctx, ref("fresh", "9"), keep))
		assert.Equal(t, int64(1), saves.Load(), "an appended ref is saved at once")
		saved, err := config.LoadProfile(b.pm.configDir, "g1", "col")
		require.NoError(t, err)
		assert.Equal(t, "9", saved.FindRef("src", "fomodkeep").Version, "with what was pending")
		assert.NotNil(t, saved.FindRef("src", "fresh"))
	})
	t.Run("refusal", func(t *testing.T) {
		b, saves, _ := newBatch(t)
		for range 5 {
			err := b.upsert(ctx, ref("locked", "2.0"), keep)
			require.ErrorIs(t, err, ErrModLocked)
		}
		assert.Zero(t, saves.Load(), "a refusal changes nothing, so nothing is pending")
		assert.Zero(t, b.pending)
	})
	t.Run("a cancelled ctx records nothing", func(t *testing.T) {
		b, saves, _ := newBatch(t)
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		require.ErrorIs(t, b.upsert(cctx, ref("fomodkeep", "9"), keep), context.Canceled)
		assert.Zero(t, b.pending)
		require.NoError(t, b.upsert(context.WithoutCancel(cctx), ref("fomodkeep", "9"), keep),
			"completeProfileWrite's ctx records it")
		require.NoError(t, b.flush())
		assert.Equal(t, int64(1), saves.Load())
	})
}

// TestProfileRefBatch_AFailedSaveKeepsItsUpserts pins a save that fails
// mid-batch: the upsert that reached the bound reports it, as the per-item
// write reported its own failed save, and nothing is dropped - the next
// save writes every pending ref.
func TestProfileRefBatch_AFailedSaveKeepsItsUpserts(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes through a read-only directory")
	}
	ctx := context.Background()
	dir, path := writeRefBatchProfile(t)
	b := &profileRefBatch{pm: NewProfileManager(dir, nil), gameID: "g1", profileName: "col",
		items: 2, window: time.Hour, now: time.Now}
	keep := fomodRecord{op: fomodKeep}
	require.NoError(t, b.upsert(ctx, domain.ModReference{SourceID: "src", ModID: "fomodkeep", Version: "7"}, keep))

	require.NoError(t, os.Chmod(path, 0o444))
	require.NoError(t, os.Chmod(filepath.Dir(path), 0o555))
	t.Cleanup(func() { _ = os.Chmod(filepath.Dir(path), 0o755) })
	err := b.upsert(ctx, domain.ModReference{SourceID: "src", ModID: "fomodset", Version: "8"}, keep)
	require.Error(t, err, "the save at the bound fails")
	assert.Equal(t, 2, b.pending, "and keeps both upserts")

	require.NoError(t, os.Chmod(filepath.Dir(path), 0o755))
	require.NoError(t, os.Chmod(path, 0o644))
	require.NoError(t, b.flush())
	saved, err := config.LoadProfile(dir, "g1", "col")
	require.NoError(t, err)
	assert.Equal(t, "7", saved.FindRef("src", "fomodkeep").Version)
	assert.Equal(t, "8", saved.FindRef("src", "fomodset").Version)
}

// TestProfileRefBatch_KeepsNoSliceOfTheCaller pins that the held document
// owns what it records: a caller reusing its FileIDs slice after the
// upsert changes nothing the batch later saves.
func TestProfileRefBatch_KeepsNoSliceOfTheCaller(t *testing.T) {
	ctx := context.Background()
	dir, _ := writeRefBatchProfile(t)
	b := &profileRefBatch{pm: NewProfileManager(dir, nil), gameID: "g1", profileName: "col",
		items: 100, window: time.Hour, now: time.Now}
	ids := []string{"a-1"}
	require.NoError(t, b.upsert(ctx, domain.ModReference{SourceID: "src", ModID: "fomodset", Version: "2", FileIDs: ids}, fomodRecord{op: fomodKeep}))
	ids[0] = "changed"
	require.NoError(t, b.flush())
	saved, err := config.LoadProfile(dir, "g1", "col")
	require.NoError(t, err)
	assert.Equal(t, []string{"a-1"}, saved.FindRef("src", "fomodset").FileIDs)
}

// TestRefBookkeepingTiming is #661's measurement of the bookkeeping alone,
// as the issue measured it: n ref upserts against an n-entry profile, one
// load and save each (upsertMod) against one batch. With LMM_PERF_661
// set; run it without -race.
func TestRefBookkeepingTiming(t *testing.T) {
	if os.Getenv("LMM_PERF_661") == "" {
		t.Skip("set LMM_PERF_661=1 to measure")
	}
	ctx := context.Background()
	keep := fomodRecord{op: fomodKeep}
	for _, n := range []int{40, 150, 300, 600} {
		profile := &domain.Profile{Name: "col", GameID: "g1"}
		for i := range n {
			profile.Mods = append(profile.Mods, domain.ModReference{SourceID: "src", ModID: fmt.Sprintf("m%04d", i), Version: "1.0"})
		}
		timed := func(upsert func(domain.ModReference) error, done func() error) time.Duration {
			start := time.Now()
			for i := range n {
				require.NoError(t, upsert(domain.ModReference{SourceID: "src", ModID: fmt.Sprintf("m%04d", i), Version: "1.0", FileIDs: []string{"f"}}))
			}
			require.NoError(t, done())
			return time.Since(start)
		}
		dir := t.TempDir()
		require.NoError(t, config.SaveProfile(dir, profile))
		pm := NewProfileManager(dir, nil)
		perItem := timed(func(r domain.ModReference) error { return pm.upsertMod(ctx, "g1", "col", r, keep) }, func() error { return nil })

		dir = t.TempDir()
		require.NoError(t, config.SaveProfile(dir, profile))
		b := &profileRefBatch{pm: NewProfileManager(dir, nil), gameID: "g1", profileName: "col", items: refBatchItems, window: refBatchWindow, now: time.Now}
		batched := timed(func(r domain.ModReference) error { return b.upsert(ctx, r, keep) }, b.flush)
		t.Logf("items=%d per-item=%.2fs batched=%.2fs", n, perItem.Seconds(), batched.Seconds())
	}
}

// TestProfileRefBatch_CloseIsTheLastWrite pins close when its save keeps
// failing: it tries twice, reports how many installed mods the profile does
// not record and names them (a whole batch can be behind one note, where a
// failed per-item save lost one ref), and afterwards the flow's deferred
// flush writes nothing over what the flow saved since.
func TestProfileRefBatch_CloseIsTheLastWrite(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes through a read-only directory")
	}
	ctx := context.Background()
	dir, path := writeRefBatchProfile(t)
	pm := NewProfileManager(dir, nil)
	saves := &atomic.Int64{}
	pm.saves = saves
	b := &profileRefBatch{pm: pm, gameID: "g1", profileName: "col", items: 10, window: time.Hour, now: time.Now}
	ids := []string{"fomodkeep", "fomodset", "collected", "todo", "off", "twice", "fomodkeep"}
	for _, id := range ids {
		require.NoError(t, b.upsert(ctx, domain.ModReference{SourceID: "src", ModID: id, Version: "7"}, fomodRecord{op: fomodKeep}))
	}

	require.NoError(t, os.Chmod(path, 0o444))
	require.NoError(t, os.Chmod(filepath.Dir(path), 0o555))
	t.Cleanup(func() { _ = os.Chmod(filepath.Dir(path), 0o755) })
	result := &ProfileImportResult{}
	var events []Event
	flushImportedRefs(b, Scope{Op: OpImport}, result, func(e Event) { events = append(events, e) })
	assert.Equal(t, int64(2), saves.Load(), "the save is tried twice")
	require.Len(t, result.Notes, 1, "the failure is one note")
	assert.Contains(t, result.Notes[0], "Warning: could not update profile: ")
	assert.Contains(t, result.Notes[0], "; 6 installed mods are not recorded in it (src:fomodkeep, src:fomodset, src:collected, src:todo, src:off, ...)")
	require.Len(t, events, 1)
	require.NoError(t, os.Chmod(filepath.Dir(path), 0o755))
	require.NoError(t, os.Chmod(path, 0o644))

	before := saves.Load()
	require.NoError(t, b.flush())
	require.NoError(t, b.close())
	assert.Equal(t, before, saves.Load(), "nothing is written after close")
}

// TestProfileRefBatch_CloseRetriesItsSave pins the retry: a save that
// fails once at close is tried again, and its refs are written.
func TestProfileRefBatch_CloseRetriesItsSave(t *testing.T) {
	ctx := context.Background()
	dir, _ := writeRefBatchProfile(t)
	b := &profileRefBatch{pm: NewProfileManager(dir, nil), gameID: "g1", profileName: "col", items: 10, window: time.Hour, now: time.Now}
	failures := 1
	b.saveDoc = func(p *domain.Profile) error {
		if failures > 0 {
			failures--
			return errors.New("disk hiccup")
		}
		return b.pm.save(p)
	}
	require.NoError(t, b.upsert(ctx, domain.ModReference{SourceID: "src", ModID: "fomodkeep", Version: "7"}, fomodRecord{op: fomodKeep}))
	require.NoError(t, b.close(), "the second try saved it")
	saved, err := config.LoadProfile(dir, "g1", "col")
	require.NoError(t, err)
	assert.Equal(t, "7", saved.FindRef("src", "fomodkeep").Version)
}

// TestUnrecordedRefs pins the note's count and names: singular, and the
// first five then an ellipsis.
func TestUnrecordedRefs(t *testing.T) {
	assert.Equal(t, "1 installed mod is not recorded in it (a:1)", unrecordedRefs([]string{"a:1"}))
	assert.Equal(t, "2 installed mods are not recorded in it (a:1, a:2)", unrecordedRefs([]string{"a:1", "a:2"}))
	assert.Equal(t, "5 installed mods are not recorded in it (a:1, a:2, a:3, a:4, a:5)", unrecordedRefs([]string{"a:1", "a:2", "a:3", "a:4", "a:5"}))
}

// TestInstaller_ReadOverridesOnce_AFailedReadIsNotKept pins the memo
// against a transient read failure: the profile cannot be read at the
// first deploy, so that deploy places no override, but the next deploy
// reads it again and places it - as the per-mod read did. A successful read
// with no overrides is kept, though: a later change is not seen.
func TestInstaller_ReadOverridesOnce_AFailedReadIsNotKept(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads an unreadable file")
	}
	svc := newBareService(t)
	root := t.TempDir()
	game := &domain.Game{ID: "g1", InstallPath: root, ModPath: root}
	require.NoError(t, config.SaveProfile(svc.configDir, &domain.Profile{Name: "p", GameID: "g1",
		Overrides: map[string][]byte{"a.txt": []byte("mine")}}))
	require.NoError(t, config.SaveProfile(svc.configDir, &domain.Profile{Name: "empty", GameID: "g1"}))
	path, err := config.ProfilePath(svc.configDir, "g1", "p")
	require.NoError(t, err)

	i := svc.newInstallerWithLinker(game, svc.getLinker(domain.LinkSymlink))
	i.readOverridesOnce()
	require.NoError(t, os.Chmod(path, 0o000))
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })
	assert.Empty(t, i.overridesFor(game, "p"), "an unreadable profile overrides nothing")
	require.NoError(t, os.Chmod(path, 0o644))
	got := i.overridesFor(game, "p")
	assert.Equal(t, []byte("mine"), got[domain.ModPathFile("a.txt")], "the next deploy reads it again")

	assert.Empty(t, i.overridesFor(game, "empty"))
	require.NoError(t, config.SaveProfile(svc.configDir, &domain.Profile{Name: "empty", GameID: "g1",
		Overrides: map[string][]byte{"a.txt": []byte("later")}}))
	assert.Empty(t, i.overridesFor(game, "empty"), "a read that found no overrides is kept")
}

// TestInstaller_ReadOverridesOnce pins the loop's override reads (#661):
// one read per profile, whatever the number of mods deployed, and each
// profile its own answer.
func TestInstaller_ReadOverridesOnce(t *testing.T) {
	reads := map[string]int{}
	i := &Installer{overridden: func(_ *domain.Game, profile string) (overriddenFiles, error) {
		reads[profile]++
		return overriddenFiles{domain.ModPathFile(profile): []byte(profile)}, nil
	}}
	i.readOverridesOnce()
	game := &domain.Game{ID: "g1"}
	for range 5 {
		assert.Equal(t, overriddenFiles{domain.ModPathFile("a"): []byte("a")}, i.overridesFor(game, "a"))
	}
	assert.Equal(t, overriddenFiles{domain.ModPathFile("b"): []byte("b")}, i.overridesFor(game, "b"))
	assert.Equal(t, map[string]int{"a": 1, "b": 1}, reads)

	none := &Installer{}
	none.readOverridesOnce()
	assert.Nil(t, none.overridesFor(game, "a"), "an Installer built without a Service still reads none")
}
