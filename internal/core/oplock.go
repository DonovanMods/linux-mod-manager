package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// opLockWait is how long beginOp waits for the cross-process lock before
// refusing (#317). Long enough that a mutation about to finish does not
// fail its neighbour - most flows hold the lock for a few milliseconds of
// bookkeeping around work they do OUTSIDE it - and short enough that a
// second lmm invocation says something rather than appearing to hang. A
// deploy of a large profile can hold it for much longer than this, which
// is the case the refusal exists to report, not to wait out.
const opLockWait = 2 * time.Second

// opLockPoll is how often the wait retries. flock has no timed variant, so
// the bounded wait is a poll; 25ms is far below the wait itself and far
// above the syscall's own cost.
const opLockPoll = 25 * time.Millisecond

// ErrOperationInProgress is the sentinel behind OperationInProgressError,
// so a caller that only cares THAT another lmm holds the mutation lock
// needs no type assertion.
var ErrOperationInProgress = errors.New("another lmm operation is in progress")

// OperationInProgressError is returned by a mutation that could not take
// the cross-process lock within opLockWait (#317): another lmm process -
// a second CLI invocation, or a running `lmm serve` - is mid-mutation on
// the same data directory.
//
// PID and StartedAt name the holder, best-effort: the holder writes them
// into the lock file as it acquires, so an unreadable or half-written file
// leaves them zero rather than failing the refusal. A frontend renders
// them ("another lmm operation is in progress (pid 4242, since …)") so the
// user knows what to wait for rather than being told only that something
// is.
type OperationInProgressError struct {
	// PID is the holder's process id, 0 when the lock file could not be read.
	PID int
	// StartedAt is when the holder took the lock, zero when unknown.
	StartedAt time.Time
	// Path is the lock file itself, for a diagnostic that has to name it.
	Path string
}

// Error reports the sentinel's text plus whatever is known about the holder.
func (e *OperationInProgressError) Error() string {
	switch {
	case e.PID != 0 && !e.StartedAt.IsZero():
		return fmt.Sprintf("%v (pid %d, since %s)", ErrOperationInProgress, e.PID, e.StartedAt.Format(time.RFC3339))
	case e.PID != 0:
		return fmt.Sprintf("%v (pid %d)", ErrOperationInProgress, e.PID)
	default:
		return ErrOperationInProgress.Error()
	}
}

// Unwrap makes errors.Is(err, ErrOperationInProgress) true.
func (e *OperationInProgressError) Unwrap() error { return ErrOperationInProgress }

// Details returns the payload the --json error envelope attaches under
// "details" (Ruling 3), so a script sees the holder as data rather than
// having to parse the sentence. The type is unexported deliberately - it is
// a wire shape for the envelope, not a core contract type.
func (e *OperationInProgressError) Details() any {
	d := operationInProgressDetails{PID: e.PID}
	if !e.StartedAt.IsZero() {
		d.StartedAt = e.StartedAt.Format(time.RFC3339)
	}
	return d
}

type operationInProgressDetails struct {
	// PID is omitzero rather than always present: an unreadable lock file
	// makes "pid": 0 a claim about process 0 rather than an absence.
	PID       int    `json:"pid,omitzero"`
	StartedAt string `json:"started_at,omitzero"`
}

// opLock is one held cross-process lock: the open descriptor the flock
// belongs to, released exactly once.
type opLock struct {
	file *os.File
	once sync.Once
}

// release drops the flock and closes the descriptor. Idempotent, and safe
// on a nil receiver so beginOp's release func can be written one way
// whether or not a lock path was configured.
func (l *opLock) release() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
		_ = l.file.Close()
	})
}

// acquireOpLock takes an exclusive advisory lock on path, waiting up to
// opLockWait for a holder to let go (#317).
//
// flock rather than a lock file's mere existence, for the property a
// crash-safe lock needs: the kernel drops it when the descriptor closes,
// so an lmm that is killed mid-deploy leaves nothing behind for the next
// one to clean up or override. It is per OPEN FILE DESCRIPTION, so two
// descriptors contend whether they are two processes or one - which is
// also what lets this be tested without spawning a subprocess.
//
// The lock file's CONTENT is documentation, never the lock: the holder
// stamps its pid and start time so a refusal can name it, and a failure to
// write that is ignored. A reader that finds it empty or half-written
// reports the refusal without a holder rather than failing differently.
//
// Reads never call this. Only beginOp does, which is exactly the set of
// mutations the in-process semaphore already serializes.
func acquireOpLock(ctx context.Context, path string) (*opLock, error) {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, fmt.Errorf("opening the operation lock: %w", err)
	}

	deadline := time.Now().Add(opLockWait)
	for {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			stampOpLockHolder(file)
			return &opLock{file: file}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			_ = file.Close()
			return nil, fmt.Errorf("locking %s: %w", path, err)
		}
		if time.Now().After(deadline) {
			pid, startedAt := readOpLockHolder(path)
			_ = file.Close()
			return nil, &OperationInProgressError{PID: pid, StartedAt: startedAt, Path: path}
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, ctx.Err()
		case <-time.After(opLockPoll):
		}
	}
}

// stampOpLockHolder writes "<pid>\n<RFC3339>\n" into the freshly locked
// file. Best-effort throughout: the lock is held by the flock, not by this
// text, so a failed truncate or write costs a refusal its holder detail and
// nothing more.
func stampOpLockHolder(file *os.File) {
	if err := file.Truncate(0); err != nil {
		return
	}
	if _, err := file.Seek(0, 0); err != nil {
		return
	}
	_, _ = fmt.Fprintf(file, "%d\n%s\n", os.Getpid(), time.Now().Format(time.RFC3339))
	_ = file.Sync()
}

// readOpLockHolder reads back what stampOpLockHolder wrote. Every failure -
// missing file, empty file, garbage, a half-written line - answers "no
// holder detail", because this runs only to decorate a refusal that has
// already been decided.
func readOpLockHolder(path string) (pid int, startedAt time.Time) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, time.Time{}
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) > 0 {
		pid, _ = strconv.Atoi(strings.TrimSpace(lines[0]))
	}
	if len(lines) > 1 {
		if ts, err := time.Parse(time.RFC3339, strings.TrimSpace(lines[1])); err == nil {
			startedAt = ts
		}
	}
	return pid, startedAt
}
