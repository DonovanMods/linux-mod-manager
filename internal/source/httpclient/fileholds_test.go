package httpclient

// #675: the persisted hold store, lifted from thunderstore/hold.go so
// NexusMods' holds outlive the process too. Each "process" here is a new
// FileHolds over the same path, which is all a second lmm process shares
// with the first.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testScopePattern = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

// newTestFileHolds is one process's store over path.
func newTestFileHolds(path string, clock *fakeClock) *FileHolds {
	return NewFileHolds(FileHoldsConfig{
		Path:        path,
		ScopesField: "scopes",
		ValidScope:  testScopePattern.MatchString,
		Now:         clock.Now,
		Threshold:   3,
		Cooldown:    5 * time.Minute,
		MaxHold:     24 * time.Hour,
	})
}

func TestFileHolds_AHoldOneProcessRecordsHoldsTheNext(t *testing.T) {
	path, clock := filepath.Join(t.TempDir(), "holds.json"), newFakeClock()
	newTestFileHolds(path, clock).Named("", clock.Now().Add(10*time.Minute), "asked for ten minutes")

	held, scope, ok := newTestFileHolds(path, clock).Active("")
	require.True(t, ok, "a second process honours the first one's hold")
	assert.Empty(t, scope)
	assert.Equal(t, clock.Now().Add(10*time.Minute), held.Until)
	assert.Equal(t, "asked for ten minutes", held.Reason)
}

func TestFileHolds_ABreakerTripsAcrossProcesses(t *testing.T) {
	path, clock := filepath.Join(t.TempDir(), "holds.json"), newFakeClock()
	for range 3 {
		newTestFileHolds(path, clock).Failed("", "HTTP 503")
	}
	held, _, ok := newTestFileHolds(path, clock).Active("")
	require.True(t, ok, "three failures in a row, one per process, trip the breaker")
	assert.True(t, strings.HasPrefix(held.Reason, "suspended after 3 failed requests in a row; the last: HTTP 503"), held.Reason)

	newTestFileHolds(path, clock).Succeeded("")
	_, _, ok = newTestFileHolds(path, clock).Active("")
	assert.False(t, ok, "a success in any process lifts a breaker lmm tripped")
}

func TestFileHolds_AScopedHoldHoldsOnlyItsScope(t *testing.T) {
	path, clock := filepath.Join(t.TempDir(), "holds.json"), newFakeClock()
	newTestFileHolds(path, clock).Named("alpha", clock.Now().Add(time.Hour), "alpha's quota")

	other := newTestFileHolds(path, clock)
	held, scope, ok := other.Active("alpha")
	require.True(t, ok)
	assert.Equal(t, "alpha", scope)
	assert.Equal(t, "alpha's quota", held.Reason)

	_, _, ok = other.Active("beta")
	assert.False(t, ok, "another scope is not held by alpha's hold")
	_, _, ok = other.Active("")
	assert.False(t, ok, "the host is not held by a scope's hold")

	other.Named("", clock.Now().Add(2*time.Hour), "host throttle")
	held, scope, ok = other.Active("beta")
	require.True(t, ok, "a host hold holds every scope")
	assert.Empty(t, scope)
	held, scope, ok = other.Active("alpha")
	require.True(t, ok)
	assert.Empty(t, scope, "the hold that ends later is the one in force")
	assert.Equal(t, clock.Now().Add(2*time.Hour), held.Until)
}

func TestFileHolds_AnExpiredHoldIsIgnoredAndCleanedUp(t *testing.T) {
	path, clock := filepath.Join(t.TempDir(), "holds.json"), newFakeClock()
	newTestFileHolds(path, clock).Named("alpha", clock.Now().Add(time.Minute), "alpha's quota")
	newTestFileHolds(path, clock).Named("", clock.Now().Add(time.Minute), "host throttle")
	require.FileExists(t, path)

	clock.Advance(time.Minute + time.Second)
	holds := newTestFileHolds(path, clock)
	_, _, ok := holds.Active("alpha")
	assert.False(t, ok, "an expired hold holds nothing")
	assert.Empty(t, holds.List())
	assert.NoFileExists(t, path, "a file of nothing but expired holds is removed")
}

func TestFileHolds_AnExpiredScopeIsPrunedFromTheFile(t *testing.T) {
	path, clock := filepath.Join(t.TempDir(), "holds.json"), newFakeClock()
	newTestFileHolds(path, clock).Named("alpha", clock.Now().Add(time.Minute), "alpha's quota")
	clock.Advance(2 * time.Minute)
	newTestFileHolds(path, clock).Named("beta", clock.Now().Add(time.Hour), "beta's quota")

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "alpha", "an expired scope is dropped on the next write")
	assert.Contains(t, string(data), "beta")
}

func TestFileHolds_ACorruptFileHoldsNothing(t *testing.T) {
	clock := newFakeClock()
	far := clock.Now().Add(10 * 365 * 24 * time.Hour).Format(time.RFC3339)
	soon := clock.Now().Add(time.Hour).Format(time.RFC3339)
	for name, content := range map[string]string{
		"garbage":        "{not json",
		"empty":          "",
		"wrong schema":   `{"schema":99,"host":{"until":"` + soon + `","reason":"x"}}`,
		"a decade ahead": `{"schema":1,"host":{"until":"` + far + `","reason":"x"}}`,
		"bad scope":      `{"schema":1,"scopes":{"../etc":{"until":"` + soon + `","reason":"x"}}}`,
		"scopes not map": `{"schema":1,"host":{"until":"` + soon + `","reason":"x"},"scopes":[1,2]}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "holds.json")
			require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
			holds := newTestFileHolds(path, clock)
			_, _, ok := holds.Active("")
			assert.False(t, ok)
			_, _, ok = holds.Active("etc")
			assert.False(t, ok)
			assert.Empty(t, holds.List())

			// And a corrupt file never blocks a write: the next hold replaces it.
			holds.Named("", clock.Now().Add(time.Minute), "fresh")
			_, _, ok = newTestFileHolds(path, clock).Active("")
			assert.True(t, ok)
		})
	}
}

func TestFileHolds_AnUnreadableFileHoldsNothing(t *testing.T) {
	dir, clock := t.TempDir(), newFakeClock()
	path := filepath.Join(dir, "holds.json")
	require.NoError(t, os.Mkdir(path, 0o700), "a directory where the file should be")
	holds := newTestFileHolds(path, clock)
	_, _, ok := holds.Active("")
	assert.False(t, ok)

	// A hold this process cannot write still holds it, in memory.
	holds.Named("", clock.Now().Add(time.Minute), "kept in memory")
	_, _, ok = holds.Active("")
	assert.True(t, ok)
}

func TestFileHolds_TheFileIsWrittenAtomicallyAndPrivately(t *testing.T) {
	dir, clock := filepath.Join(t.TempDir(), "data"), newFakeClock()
	path := filepath.Join(dir, "holds.json")
	holds := NewFileHolds(FileHoldsConfig{
		Path: path, ValidScope: testScopePattern.MatchString, Now: clock.Now,
		Threshold: 3, Cooldown: 5 * time.Minute, MaxHold: 24 * time.Hour, DirMode: 0o700,
	})
	holds.Named("", clock.Now().Add(time.Minute), "x")

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	dirInfo, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), dirInfo.Mode().Perm())

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	assert.ElementsMatch(t, []string{"holds.json", "holds.lock"}, names, "no staged file is left behind")
}

func TestFileHolds_ConcurrentProcessesKeepEveryFailure(t *testing.T) {
	path, clock := filepath.Join(t.TempDir(), "holds.json"), newFakeClock()
	holds := NewFileHolds(FileHoldsConfig{
		Path: path, ValidScope: testScopePattern.MatchString, Now: clock.Now,
		Threshold: 1000, Cooldown: 5 * time.Minute, MaxHold: 24 * time.Hour,
	})
	const writers = 16
	var wg sync.WaitGroup
	for range writers {
		wg.Go(func() {
			NewFileHolds(FileHoldsConfig{
				Path: path, ValidScope: testScopePattern.MatchString, Now: clock.Now,
				Threshold: 1000, Cooldown: 5 * time.Minute, MaxHold: 24 * time.Hour,
			}).Failed("alpha", "x")
		})
	}
	wg.Wait()
	_, scopes := holds.Snapshot()
	assert.Equal(t, writers, scopes["alpha"].Failures)
}

func TestFileHolds_WithNoPathTheHoldsLiveInMemory(t *testing.T) {
	clock := newFakeClock()
	holds := newTestFileHolds("", clock)
	holds.Named("alpha", clock.Now().Add(time.Minute), "x")
	_, _, ok := holds.Active("alpha")
	assert.True(t, ok)
	_, _, ok = newTestFileHolds("", clock).Active("alpha")
	assert.False(t, ok, "nothing is shared without a file")
}

func TestFileHolds_ScopesAreKeptUnderTheConfiguredMember(t *testing.T) {
	path, clock := filepath.Join(t.TempDir(), "holds.json"), newFakeClock()
	holds := NewFileHolds(FileHoldsConfig{
		Path: path, ScopesField: "communities", ValidScope: testScopePattern.MatchString,
		Now: clock.Now, Threshold: 3, Cooldown: 5 * time.Minute, MaxHold: 24 * time.Hour,
	})
	holds.Named("alpha", clock.Now().Add(time.Minute), "x")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"communities":{"alpha":`)
	assert.Contains(t, string(data), `"schema":1`)
}

// TestFileHolds_ABreakerTripsWhileTheFileCannotBeWritten (#675 review): a
// store whose writes fail keeps its failure streak in memory, so the
// breaker still trips - an unwritable file must not disarm it.
func TestFileHolds_ABreakerTripsWhileTheFileCannotBeWritten(t *testing.T) {
	dir, clock := t.TempDir(), newFakeClock()
	path := filepath.Join(dir, "holds.json")
	require.NoError(t, os.Mkdir(path, 0o700), "a directory where the file should be: every publish fails")
	require.NoError(t, os.WriteFile(filepath.Join(path, "keep"), nil, 0o600), "and one that is never removed")
	holds := newTestFileHolds(path, clock)

	holds.Failed("", "HTTP 503")
	holds.Failed("", "HTTP 503")
	_, _, ok := holds.Active("")
	require.False(t, ok, "two failures are not three")
	_, scopes := holds.Snapshot()
	assert.Empty(t, scopes)
	host, _ := holds.Snapshot()
	assert.Equal(t, 2, host.Failures, "the streak survives the failed writes")

	held := holds.Failed("", "HTTP 503")
	assert.True(t, held.ActiveAt(clock.Now()))
	_, _, ok = holds.Active("")
	assert.True(t, ok, "the third failure trips the breaker, in memory")

	// A streak past its cooldown is over, written or not.
	clock.Advance(6 * time.Minute)
	other := newTestFileHolds(path, clock)
	other.Failed("alpha", "x")
	clock.Advance(6 * time.Minute)
	assert.Equal(t, 1, other.Failed("alpha", "x").Failures, "a stale in-memory streak does not count")
}
