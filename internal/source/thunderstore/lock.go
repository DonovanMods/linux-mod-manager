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

// lockTarget is how one lock file is reached: open creates-or-opens it,
// and stat describes whatever is at its name NOW, without following a
// link. A build reaches it by path; a removal reaches it through the
// directory handle it has already proved (inventory.go).
type lockTarget struct {
	open func() (*os.File, error)
	stat func() (os.FileInfo, error)
}

// lockCommunity takes the exclusive advisory lock on community's index
// directory and returns the release. With no cache root configured there
// is no directory to lock and nothing to serialise: the release is a no-op
// and the caller fails a few lines later on the real problem.
//
// The lock file is never opened through a symbolic link: plain
// os.OpenFile honours O_NOFOLLOW (os.Root does not - see RemoveIndex), so a
// link planted at .lock is a refusal rather than lmm creating or locking a
// file somewhere it does not own.
func (st *store) lockCommunity(ctx context.Context, community string) (func(), error) {
	dir := st.dir(community)
	if dir == "" {
		return func() {}, nil
	}
	path := filepath.Join(dir, lockFileName)
	return acquireLock(ctx, community, lockTarget{
		open: func() (*os.File, error) {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("creating %s: %w", dir, err)
			}
			file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
			if err != nil {
				return nil, fmt.Errorf("opening the index lock: %w", err)
			}
			return file, nil
		},
		stat: func() (os.FileInfo, error) { return os.Lstat(path) },
	})
}

// acquireLock takes the flock on target and returns the release.
//
// A lock is only a lock on the file that is AT the name (#410): a prune
// removes the lock file while holding it, and a waiter that then won the
// flock on the removed inode would hold nothing a newcomer could see. So
// the name is re-checked after every acquisition, and a lock on a file that
// is no longer there is dropped and taken again on the one that is.
//
// That loop is bounded like the wait itself (T3 review F5): by the caller's
// context and by indexLockWait. A name that never settles on the file lmm
// locked is a failure, never a spin.
func acquireLock(ctx context.Context, community string, target lockTarget) (func(), error) {
	deadline := time.Now().Add(indexLockWait)
	for {
		release, current, err := tryLock(ctx, community, target, deadline)
		if err != nil || current {
			return release, err
		}
		release()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("the %s index lock kept changing under lmm for %s", community, indexLockWait)
		}
	}
}

// tryLock takes the flock on whatever file target opens, and reports
// whether that file is still the one at its name once the lock is held.
func tryLock(ctx context.Context, community string, target lockTarget, deadline time.Time) (func(), bool, error) {
	file, err := target.open()
	if err != nil {
		return nil, false, err
	}
	for {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			release := func() {
				_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
				_ = file.Close()
			}
			now, statErr := target.stat()
			if statErr == nil && !now.Mode().IsRegular() {
				// Something other than a plain file sits at the name - a
				// link planted after the open. Nothing lmm can lock is the
				// lock then, and retrying would only find it again.
				release()
				return nil, false, fmt.Errorf("the %s index lock is not a plain file (a symbolic link?): not locking through it", community)
			}
			return release, statErr == nil && sameFile(file, now), nil
		}
		if err != syscall.EWOULDBLOCK { //nolint:errorlint // Flock returns a bare syscall.Errno
			_ = file.Close()
			return nil, false, fmt.Errorf("locking the %s index: %w", community, err)
		}
		if time.Now().After(deadline) {
			_ = file.Close()
			return nil, false, fmt.Errorf("another lmm process has been building the %s index for over %s", community, indexLockWait)
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, false, ctx.Err()
		case <-time.After(indexLockPoll):
		}
	}
}

// sameFile reports whether the open file is the one now describes.
func sameFile(file *os.File, now os.FileInfo) bool {
	held, err := file.Stat()
	if err != nil {
		return false
	}
	return os.SameFile(held, now)
}
