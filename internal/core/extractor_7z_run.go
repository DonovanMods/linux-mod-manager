package core

// Every run of the system 7z - a listing, a .7z/.rar extraction, the zip
// fallback (#656), a collection recipe - goes through run7z (#658). No
// fixed time limit bounds it: a multi-gigabyte texture pack on a slow disk
// legitimately takes longer than any fixed limit would allow. A run ends
// early only when the caller's ctx is cancelled, or when the watchdog sees
// it make no progress for sevenZipStallLimit: no byte written under the
// directory it extracts into, no new entry there, and no output printed.
// That is a corrupt archive 7z is stuck on, a stalled disk, or 7z waiting
// on a prompt, and the run is killed with an error naming the archive.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// sevenZipStallLimit is how long a 7z run may go without progress before
// the watchdog kills it.
const sevenZipStallLimit = 2 * time.Minute

// sevenZipWatch is the watchdog's timing: stall is sevenZipStallLimit, poll
// how often progress is measured. A seam: tests scale both down.
var sevenZipWatch = struct{ stall, poll time.Duration }{
	stall: sevenZipStallLimit,
	poll:  sevenZipStallLimit / 24,
}

// sevenZipWaitDelay bounds how long a killed 7z's output pipes may stay
// open (a child it started still holding them) before the run returns.
const sevenZipWaitDelay = 5 * time.Second

// errSevenZipStalled marks a 7z run the watchdog killed.
var errSevenZipStalled = errors.New("7z stopped making progress")

// syncBuffer is a bytes.Buffer safe for 7z's writes and the watchdog's
// reads at once.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// Write appends p, as 7z prints it.
func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// Len is the bytes written so far.
func (b *syncBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Len()
}

// Bytes is a copy of what was written.
func (b *syncBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Clone(b.buf.Bytes())
}

// run7z runs `7z args...` on archivePath and returns its stdout and stderr.
// what names the run in an error ("listing", "extraction"). writeDir is the
// directory the run extracts into, whose growth counts as progress; ""
// for a listing, whose output is its only progress. A cancelled ctx
// returns an error that is ctx's; a stall one that is errSevenZipStalled,
// naming the archive.
func run7z(ctx context.Context, archivePath, what, writeDir string, args ...string) (stdout, stderr []byte, err error) {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var out, errOut syncBuffer
	cmd := exec.CommandContext(runCtx, "7z", args...)
	cmd.Stdout, cmd.Stderr = &out, &errOut
	cmd.WaitDelay = sevenZipWaitDelay
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("starting 7z: %w", err)
	}

	var stalled atomic.Bool
	done := make(chan struct{})
	watched := make(chan struct{})
	go func() {
		defer close(watched)
		watch7z(done, cancel, &stalled, func() int64 {
			return int64(out.Len()+errOut.Len()) + treeProgress(writeDir)
		})
	}()
	err = cmd.Wait()
	close(done)
	<-watched

	stdout, stderr = out.Bytes(), errOut.Bytes()
	switch {
	case stalled.Load():
		return stdout, stderr, fmt.Errorf("%w: the 7z %s of %s made no progress for %v (a damaged archive, or a stalled disk)",
			errSevenZipStalled, what, filepath.Base(archivePath), sevenZipWatch.stall)
	case ctx.Err() != nil:
		return stdout, stderr, fmt.Errorf("the 7z %s of %s was cancelled: %w", what, filepath.Base(archivePath), ctx.Err())
	}
	return stdout, stderr, err
}

// watch7z polls progress until done closes, and when it has not grown for
// sevenZipWatch.stall, marks the run stalled and kills it (cancel).
func watch7z(done <-chan struct{}, cancel context.CancelFunc, stalled *atomic.Bool, progress func() int64) {
	ticker := time.NewTicker(sevenZipWatch.poll)
	defer ticker.Stop()
	last, lastAt := progress(), time.Now()
	for {
		select {
		case <-done:
			return
		case now := <-ticker.C:
			if p := progress(); p != last {
				last, lastAt = p, now
				continue
			}
			if now.Sub(lastAt) >= sevenZipWatch.stall {
				stalled.Store(true)
				cancel()
				return
			}
		}
	}
}

// treeProgress is how far an extraction into dir has got: the bytes its
// files hold plus its entry count, so a run writing many empty files is
// progressing too. Never follows a link; "" or an unreadable entry counts
// as nothing.
func treeProgress(dir string) int64 {
	if dir == "" {
		return 0
	}
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		n++
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}

// sevenZipOutput joins a run's stdout and stderr, for an error message.
func sevenZipOutput(stdout, stderr []byte) string {
	return string(stdout) + string(stderr)
}
