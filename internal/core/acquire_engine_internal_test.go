package core

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAcquireModeSelection pins the engine's mode-selection rule
// (acquire_engine.go): install-shaped Applies run PHASED, and INTERLEAVED is
// an explicit declaration only the update batch makes (#324: each item is
// re-planned against the state the previous one left). A new flow that
// wants it must make that case here, by name.
func TestAcquireModeSelection(t *testing.T) {
	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	fset := token.NewFileSet()
	declaredIn := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
		require.NoError(t, err)
		ast.Inspect(file, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && id.Name == "acquireInterleaved" {
				declaredIn[name] = true
			}
			return true
		})
	}
	files := make([]string, 0, len(declaredIn))
	for name := range declaredIn {
		files = append(files, name)
	}
	sort.Strings(files)
	assert.Equal(t, []string{"acquire_engine.go", "update_batch.go"}, files,
		"only the update batch declares INTERLEAVED; every other install-shaped Apply is PHASED")
}

// fakeItem is a runAcquire item in the unit tests below.
type fakeItem struct{ id int }

// engineLog records what a fake run did, in order.
type engineLog struct{ steps []string }

func (l *engineLog) add(s string) { l.steps = append(l.steps, s) }

// fakeRun builds a run whose item i fails acquire with failures[i] (nil:
// clean), logging every acquire, begin, settle, commit and release.
func fakeRun(policy acquirePolicy, log *engineLog, failures ...error) acquireRun[fakeItem] {
	return acquireRun[fakeItem]{
		policy: policy,
		n:      len(failures),
		acquire: func(_ context.Context, i int) (fakeItem, *acquireFailure) {
			log.add("acquire " + string(rune('0'+i)))
			if failures[i] == nil {
				return fakeItem{id: i}, nil
			}
			return fakeItem{}, &acquireFailure{
				refusal: AcquireRefusal{SourceID: "s", ModID: string(rune('0' + i)), Err: failures[i]},
				settle:  func() { log.add("failed " + string(rune('0'+i))) },
			}
		},
		begin: func(context.Context) error { log.add("begin"); return nil },
		commit: func(_ context.Context, i int, _ fakeItem) error {
			log.add("commit " + string(rune('0'+i)))
			return nil
		},
		release: func(it fakeItem, blocked bool) {
			if blocked {
				log.add("retain " + string(rune('0'+it.id)))
			}
		},
	}
}

var (
	errFlaky  = errors.New("download failed")
	errLoader = &LoaderRequiredError{GameID: "g", Kind: "bepinex", ModName: "Thing"}
)

func TestRunAcquire_PhasedAcquiresEverythingFirst(t *testing.T) {
	var log engineLog
	require.NoError(t, runAcquire(context.Background(), fakeRun(acquirePolicy{}, &log, nil, errFlaky, nil)))
	assert.Equal(t, []string{
		"acquire 0", "acquire 1", "acquire 2",
		"begin",
		"commit 0", "failed 1", "commit 2",
	}, log.steps, "an unanswerable failure settles in its place in the order")
}

func TestRunAcquire_AnAnswerableRefusalBlocksWithNothingWritten(t *testing.T) {
	var log engineLog
	err := runAcquire(context.Background(), fakeRun(acquirePolicy{}, &log, nil, errFlaky, errLoader))
	var refused *AcquireRefusedError
	require.ErrorAs(t, err, &refused)
	require.Len(t, refused.Refusals, 1, "only the answerable refusal blocks; the flaky download is not one")
	var loaderErr *LoaderRequiredError
	require.ErrorAs(t, err, &loaderErr)
	assert.Equal(t, []string{
		"acquire 0", "acquire 1", "acquire 2",
		"retain 0",
	}, log.steps, "no begin, no commit, no settled failure; the clean item is retained")
}

func TestRunAcquire_BlockAllRefusesOnAnyFailure(t *testing.T) {
	var log engineLog
	err := runAcquire(context.Background(), fakeRun(acquirePolicy{blockAll: true}, &log, nil, errFlaky, nil))
	var refused *AcquireRefusedError
	require.ErrorAs(t, err, &refused)
	require.Len(t, refused.Refusals, 1)
	assert.ErrorIs(t, err, errFlaky)
	assert.Equal(t, []string{"acquire 0", "acquire 1", "acquire 2", "retain 0", "retain 2"}, log.steps)
}

func TestRunAcquire_InterleavedSettlesEachItemBeforeTheNext(t *testing.T) {
	var log engineLog
	require.NoError(t, runAcquire(context.Background(), fakeRun(acquirePolicy{mode: acquireInterleaved}, &log, nil, errLoader, nil)))
	assert.Equal(t, []string{
		"begin",
		"acquire 0", "commit 0",
		"acquire 1", "failed 1",
		"acquire 2", "commit 2",
	}, log.steps, "interleaved never blocks: there is no decide step to block at")
}

func TestRunAcquire_ACancelledAcquireWritesNothing(t *testing.T) {
	var log engineLog
	ctx, cancel := context.WithCancel(context.Background())
	run := fakeRun(acquirePolicy{}, &log, nil, nil)
	acquire := run.acquire
	run.acquire = func(ctx context.Context, i int) (fakeItem, *acquireFailure) {
		item, f := acquire(ctx, i)
		if i == 1 {
			cancel()
		}
		return item, f
	}
	require.ErrorIs(t, runAcquire(ctx, run), context.Canceled)
	assert.Equal(t, []string{"acquire 0", "acquire 1"}, log.steps, "no begin, no commit, nothing retained")
}

func TestCombineRefusals(t *testing.T) {
	other := errors.New("boom")
	t.Run("one refusal is itself", func(t *testing.T) {
		err := combineRefusals([]AcquireRefusal{{SourceID: "s", ModID: "m", Err: other}})
		assert.Equal(t, other, err)
	})
	t.Run("loader refusals merge", func(t *testing.T) {
		second := &LoaderRequiredError{GameID: "g", Kind: "bepinex", ModName: "Other"}
		err := combineRefusals([]AcquireRefusal{{Err: errLoader}, {Err: second}})
		var merged *LoaderRequiredError
		require.ErrorAs(t, err, &merged)
		assert.Equal(t, []string{"Thing", "Other"}, merged.Mods)
		assert.Equal(t, "Thing and Other", merged.ModName)
		assert.Empty(t, errLoader.Mods, "the original is not mutated")
	})
	t.Run("a mixture joins, keeping each reachable", func(t *testing.T) {
		err := combineRefusals([]AcquireRefusal{{SourceID: "s", ModID: "a", Err: errLoader}, {SourceID: "s", ModID: "b", Err: other}})
		assert.ErrorIs(t, err, other)
		var loaderErr *LoaderRequiredError
		assert.ErrorAs(t, err, &loaderErr)
		assert.Contains(t, err.Error(), "s:b: boom")
	})
	t.Run("names", func(t *testing.T) {
		assert.Equal(t, "A, B and C", joinNames([]string{"A", "B", "C"}))
	})
}

// #602 review N1: two items for one mod would share one acquire tree
// (acquireTreePath) and one cache entry, and the second claimTree would
// delete the first's tree. With a key, the engine acquires, commits and
// reports the FIRST occurrence only - in both modes.
func TestRunAcquire_ADuplicateItemIsAcquiredOnce(t *testing.T) {
	keys := []string{"a", "b", "a"}
	for name, mode := range map[string]acquireMode{"phased": acquirePhased, "interleaved": acquireInterleaved} {
		t.Run(name, func(t *testing.T) {
			var log engineLog
			run := fakeRun(acquirePolicy{mode: mode}, &log, nil, nil, nil)
			run.key = func(i int) string { return keys[i] }
			require.NoError(t, runAcquire(context.Background(), run))
			assert.NotContains(t, log.steps, "acquire 2")
			assert.NotContains(t, log.steps, "commit 2")
			assert.Contains(t, log.steps, "commit 0")
			assert.Contains(t, log.steps, "commit 1")
		})
	}
}

// Every acquired item is released exactly once, whatever happens - a
// panicking commit included (#602 review S4).
func TestRunAcquire_APanickingCommitReleasesEveryUnsettledItem(t *testing.T) {
	released := map[int]int{}
	run := acquireRun[fakeItem]{
		n:       3,
		acquire: func(_ context.Context, i int) (fakeItem, *acquireFailure) { return fakeItem{id: i}, nil },
		commit: func(_ context.Context, i int, _ fakeItem) error {
			if i == 1 {
				panic("boom")
			}
			return nil
		},
		release: func(it fakeItem, _ bool) { released[it.id]++ },
	}
	func() {
		defer func() { require.Equal(t, "boom", recover()) }()
		_ = runAcquire(context.Background(), run)
	}()
	assert.Equal(t, map[int]int{0: 1, 1: 1, 2: 1}, released)
}

// A failed item's partial acquisition (a mod's earlier files) is released
// with the same blocked flag as the clean items: retained when the run is
// refused (#602 review S1).
func TestRunAcquire_AFailedItemsPartialFilesAreRetainedWhenBlocked(t *testing.T) {
	var retained []bool
	run := acquireRun[fakeItem]{
		n: 1,
		acquire: func(context.Context, int) (fakeItem, *acquireFailure) {
			return fakeItem{}, &acquireFailure{
				refusal: AcquireRefusal{Err: errLoader},
				release: func(blocked bool) { retained = append(retained, blocked) },
			}
		},
		commit:  func(context.Context, int, fakeItem) error { return nil },
		release: func(fakeItem, bool) {},
	}
	var refused *AcquireRefusedError
	require.ErrorAs(t, runAcquire(context.Background(), run), &refused)
	assert.Equal(t, []bool{true}, retained, "released once, retained")
}
