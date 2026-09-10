// Package thunderstore: this file is the CROSS-PROCESS half of "one build
// at a time" (T1 review #2).
//
// Source.communityLock serialises goroutines inside one binary, which is
// not the configuration that matters: `lmm serve` refreshing while the
// user types `lmm search` is two processes over one directory, and the
// shared TTL is what makes them want to refresh at the same moment. Two
// commits interleaving there can leave one build's index.json beside the
// other's packages.jsonl - every offset addressing another document - so
// the whole build-and-commit runs under an advisory lock on the community
// directory, exactly as every lmm MUTATION runs under core's own
// OpLockPath flock.
//
// flock, not a lock file's existence, for the property that matters: the
// kernel drops it when the descriptor closes, so an lmm killed mid-build
// leaves nothing for the next one to clean up or override.
package thunderstore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const (
	// lockFileName is the flock target inside a community's directory. The
	// "." prefix keeps it out of the way of the three files that ARE the
	// index, and out of the way of the ".packages-*"/".index-*" staging
	// files a build creates beside it.
	lockFileName = ".lock"
	// indexLockWait is how long a build waits for another process's build
	// of the SAME community. Generous on purpose: what it is waiting for is
	// a download of tens of megabytes, and waiting for that is the whole
	// point - the alternative is a second download that then has to be
	// reconciled with the first. A caller that gives up sooner cancels its
	// ctx, which this respects.
	indexLockWait = 2 * time.Minute
	// indexLockPoll is how often the wait retries: flock has no timed
	// variant, so a bounded wait is a poll (core's opLockPoll, and its
	// reasoning).
	indexLockPoll = 25 * time.Millisecond
)

// lockCommunity takes the exclusive advisory lock on community's index
// directory and returns the release. With no cache root configured there
// is no directory to lock and nothing to serialise: the release is a no-op
// and the caller fails a few lines later on the real problem.
func (st *store) lockCommunity(ctx context.Context, community string) (func(), error) {
	dir := st.dir(community)
	if dir == "" {
		return func() {}, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating %s: %w", dir, err)
	}
	path := filepath.Join(dir, lockFileName)
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening the index lock: %w", err)
	}

	deadline := time.Now().Add(indexLockWait)
	for {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
				_ = file.Close()
			}, nil
		}
		if err != syscall.EWOULDBLOCK { //nolint:errorlint // Flock returns a bare syscall.Errno
			_ = file.Close()
			return nil, fmt.Errorf("locking %s: %w", path, err)
		}
		if time.Now().After(deadline) {
			_ = file.Close()
			return nil, fmt.Errorf("another lmm process has been building the %s index for over %s", community, indexLockWait)
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, ctx.Err()
		case <-time.After(indexLockPoll):
		}
	}
}
