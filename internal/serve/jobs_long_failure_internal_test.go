package serve

// #655: a job that emits far more events than its ring holds, and then
// fails, never loses its end. The terminal state, the time it ended and the
// failure (with its per-mod items) live on the job, not in the ring; a
// session-stream watcher too slow for the burst is dropped, and the
// snapshot a reconnect opens with carries the ending.

import (
	"context"
	"errors"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// itemsError is a failure naming several mods, as a refused collection
// import's does.
type itemsError struct{ items []core.ItemFailure }

func (e itemsError) Error() string                    { return "nexusmods:1: gone\nnexusmods:2: bad zip" }
func (e itemsError) ItemFailures() []core.ItemFailure { return e.items }

func TestJobRegistry_ALongFailedJobKeepsItsEnding(t *testing.T) {
	r := newTestRegistry(t, t.Context(), 1024, 50)
	_, live, cancel := r.watch(8) // far too small for the burst below
	defer cancel()

	items := []core.ItemFailure{
		{SourceID: "nexusmods", ModID: "1", Name: "One", Reason: "gone"},
		{SourceID: "nexusmods", ModID: "2", Name: "Two", Reason: "bad zip"},
	}
	id, err := r.Start("profile_import", func(_ context.Context, sink core.EventSink) (any, error) {
		for i := range 3000 {
			sink(core.ModEvent{Scope: core.Scope{Op: core.OpImport, Index: i%600 + 1, Total: 600}, Phase: core.ImportModInstalling})
		}
		return nil, itemsError{items: items}
	})
	require.NoError(t, err)
	j, ok := r.job(id)
	require.True(t, ok)
	waitFor(t, j.done(), "job completion")

	frames := 0
	for range live {
		frames++
	}
	assert.Less(t, frames, 3000, "the watcher saw only what fitted before it was dropped")
	assert.Zero(t, r.watcherCount(), "the slow watcher was dropped, not waited on")

	snapshot, _, cancelAgain := r.watch(8)
	defer cancelAgain()
	require.Len(t, snapshot, 1)
	ended := snapshot[0]
	assert.Equal(t, jobFailed, ended.State, "the reconnect's snapshot carries the ending")
	assert.False(t, ended.EndedAt.IsZero(), "ended_at is set for a failed job")
	assert.Equal(t, 3000, ended.EventCount)
	assert.Equal(t, 3000-1024, ended.DroppedEvents)
	require.NotNil(t, ended.Error)
	assert.Equal(t, items, ended.Error.Items)

	var asItems interface{ ItemFailures() []core.ItemFailure }
	assert.True(t, errors.As(j.failure(), &asItems), "the typed error survives too")
}

func TestJobRegistry_ASucceededJobHasAnEndedAt(t *testing.T) {
	r := newTestRegistry(t, t.Context(), 8, 50)
	id, err := r.Start("deploy", func(context.Context, core.EventSink) (any, error) { return nil, nil })
	require.NoError(t, err)
	j, _ := r.job(id)
	waitFor(t, j.done(), "job completion")
	rows := r.list()
	require.Len(t, rows, 1)
	assert.Equal(t, jobSucceeded, rows[0].State)
	assert.False(t, rows[0].EndedAt.IsZero(), "ended_at is set for a succeeded job")
}
