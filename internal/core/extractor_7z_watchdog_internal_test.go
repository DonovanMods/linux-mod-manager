package core

// #658: no fixed time limit bounds a 7z run - a multi-gigabyte texture
// pack on a slow disk legitimately takes longer than any fixed limit. A run
// ends early only when the caller's ctx is cancelled, or when the watchdog
// sees no progress (bytes written under the private directory, or output
// printed) for sevenZipStallLimit. Tests scale the watchdog's timing down
// through its seam: here a "stall limit" is 500ms, so the old fixed
// 5-minute limit is 500ms * 5/2 = 1250ms in the same scale.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scaleSevenZipWatch shrinks the watchdog's timing for one test.
func scaleSevenZipWatch(t *testing.T) {
	t.Helper()
	saved := sevenZipWatch
	t.Cleanup(func() { sevenZipWatch = saved })
	sevenZipWatch.stall = 500 * time.Millisecond
	sevenZipWatch.poll = 20 * time.Millisecond
}

// oldFixedLimitScaled is the old 5-minute limit in the scaled time.
const oldFixedLimitScaled = 1250 * time.Millisecond

// installStallingListing7z puts a fake 7z on PATH whose listing never
// prints anything and never ends.
func installStallingListing7z(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	script := "#!/bin/sh\nexec /bin/sleep 30\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "7z"), []byte(script), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func Test7zWatchdog_AStalledListingIsKilledNamingTheArchive(t *testing.T) {
	scaleSevenZipWatch(t)
	installStallingListing7z(t)
	archive := fakeArchive(t, "7z")

	start := time.Now()
	_, err := listArchiveMembers(context.Background(), NewExtractor(), archive)
	require.Error(t, err)
	assert.True(t, errors.Is(err, errSevenZipStalled), "got %v", err)
	assert.Contains(t, err.Error(), "mod.7z")
	assert.Less(t, time.Since(start), 10*time.Second, "the watchdog, not the fake's own end, stopped it")
}

func Test7zWatchdog_AStalledExtractionIsKilledNamingTheArchive(t *testing.T) {
	for _, format := range sevenZipFormats {
		t.Run(format, func(t *testing.T) {
			scaleSevenZipWatch(t)
			install7zFake(t, []fake7zMember{{path: "a.txt", attrs: "A -rw-r--r--", size: 2}},
				`/bin/echo a > "$out/a.txt"; exec /bin/sleep 30`)
			archive := fakeArchive(t, format)
			dest := t.TempDir()

			start := time.Now()
			err := NewExtractor().Extract(context.Background(), archive, dest)
			require.Error(t, err)
			assert.True(t, errors.Is(err, errSevenZipStalled), "got %v", err)
			assert.Contains(t, err.Error(), "mod."+format)
			assert.Less(t, time.Since(start), 10*time.Second)
			assert.Empty(t, entriesUnder(t, dest), "nothing moved, and the private directory is gone")
		})
	}
}

func Test7zWatchdog_ASlowButSteadyExtractionOutlivesTheOldFixedLimit(t *testing.T) {
	scaleSevenZipWatch(t)
	// 15 writes 100ms apart: 1.5s in all, longer than the old limit in
	// this scale, but never 500ms without a byte written.
	install7zFake(t, []fake7zMember{{path: "big.bin", attrs: "A -rw-r--r--", size: 100}},
		`i=0; while [ $i -lt 15 ]; do /bin/echo chunk >> "$out/big.bin"; /bin/sleep 0.1; i=$((i+1)); done`)
	archive := fakeArchive(t, "7z")
	dest := t.TempDir()

	start := time.Now()
	require.NoError(t, NewExtractor().Extract(context.Background(), archive, dest))
	assert.Greater(t, time.Since(start), oldFixedLimitScaled, "the run really outlasted the old limit")
	got, err := os.ReadFile(filepath.Join(dest, "big.bin"))
	require.NoError(t, err)
	assert.Len(t, got, 15*len("chunk\n"))
}

func Test7zWatchdog_ASlowButSteadyZipFallbackOutlivesTheOldFixedLimit(t *testing.T) {
	scaleSevenZipWatch(t)
	install7zFake(t, []fake7zMember{{path: "a.txt", attrs: "A -rw-r--r--", size: 3}},
		`i=0; while [ $i -lt 15 ]; do /bin/echo chunk >> "$out/a.txt"; /bin/sleep 0.1; i=$((i+1)); done`)
	archive := writeRawZip(t, 12, "a.txt")

	start := time.Now()
	require.NoError(t, NewExtractor().Extract(context.Background(), archive, t.TempDir()))
	assert.Greater(t, time.Since(start), oldFixedLimitScaled)
}

func Test7zWatchdog_ASlowButSteadyListingOutlivesTheOldFixedLimit(t *testing.T) {
	scaleSevenZipWatch(t)
	bin := t.TempDir()
	script := `#!/bin/sh
/bin/echo "----------"
i=0; while [ $i -lt 15 ]; do /bin/echo "Path = f$i"; /bin/echo "Size = 1"; /bin/echo; /bin/sleep 0.1; i=$((i+1)); done
`
	require.NoError(t, os.WriteFile(filepath.Join(bin, "7z"), []byte(script), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	start := time.Now()
	members, err := listArchiveMembers(context.Background(), NewExtractor(), fakeArchive(t, "rar"))
	require.NoError(t, err)
	assert.Len(t, members, 15)
	assert.Greater(t, time.Since(start), oldFixedLimitScaled)
}

func Test7zWatchdog_CancellingCtxKillsTheRun(t *testing.T) {
	// The watchdog at its real timing: only the cancellation can end it.
	install7zFake(t, []fake7zMember{{path: "a.txt", attrs: "A -rw-r--r--", size: 2}},
		`/bin/echo a > "$out/a.txt"; exec /bin/sleep 30`)
	archive := fakeArchive(t, "7z")
	dest := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	start := time.Now()
	err := NewExtractor().Extract(ctx, archive, dest)
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled), "got %v", err)
	assert.False(t, errors.Is(err, errSevenZipStalled))
	assert.Less(t, time.Since(start), 10*time.Second)
	assert.Empty(t, entriesUnder(t, dest))
}

func TestSevenZipStallLimit(t *testing.T) {
	assert.Equal(t, 2*time.Minute, sevenZipStallLimit)
	assert.Equal(t, sevenZipStallLimit, sevenZipWatch.stall, "the seam defaults to the constant")
	assert.Less(t, sevenZipWatch.poll, sevenZipWatch.stall)
}
