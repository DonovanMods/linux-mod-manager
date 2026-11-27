package cache

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CommittedName is an entry's commit stamp (#678): when the entry was
// first committed to the cache, which is what ranks a mod's versions
// newest first for the cache prune. It is written ONCE per entry - by the
// commit that creates it (internal/core's commitStagedCache, an import or
// adopt writing a new entry, a copy) - and carried, never rewritten, by
// every later commit to the same entry, so a re-stamp of the identity
// marker, a completion marker written later, `verify --fix` or a copy of
// the cache never makes an old version look new. The directory's own
// modification time moves with all of those; this does not. Reserved
// (ReservedPrefix), so it is never listed, sized for deploy or deployed.
const CommittedName = ReservedPrefix + "committed"

// CommittedAt reads dir's commit stamp. ok is false when dir has none (an
// entry from before stamps existed) or it cannot be read.
func CommittedAt(dir string) (at time.Time, ok bool) {
	body, err := os.ReadFile(filepath.Join(dir, CommittedName))
	if err != nil {
		return time.Time{}, false
	}
	at, err = time.Parse(time.RFC3339Nano, strings.TrimSpace(string(body)))
	return at, err == nil
}

// MarkCommitted stamps dir as committed at at, unless it already carries a
// stamp: the first commit's time stands.
func MarkCommitted(dir string, at time.Time) error {
	if _, ok := CommittedAt(dir); ok {
		return nil
	}
	return writeCommitted(dir, at)
}

// CarryCommitted stamps stage, the directory about to replace (or become)
// the entry at entry, with the entry's own commit time when it has one,
// else the one stage already carries, else now. A commit to an existing
// entry therefore keeps the time it was first committed.
func CarryCommitted(stage, entry string, now time.Time) error {
	if at, ok := CommittedAt(entry); ok {
		return writeCommitted(stage, at)
	}
	return MarkCommitted(stage, now)
}

// CarryCommittedFrom stamps dst, a copy of the entry at src, with src's
// commit time, or - for a src from before stamps - src's modification
// time, which is the best record of when it was committed. A copy is not
// a new version.
func CarryCommittedFrom(dst, src string) error {
	if at, ok := CommittedAt(src); ok {
		return writeCommitted(dst, at)
	}
	info, err := os.Stat(src)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return MarkCommitted(dst, time.Now())
		}
		return fmt.Errorf("reading cache entry %s: %w", src, err)
	}
	return writeCommitted(dst, info.ModTime())
}

// writeCommitted writes dir's commit stamp, replacing any.
func writeCommitted(dir string, at time.Time) error {
	path := filepath.Join(dir, CommittedName)
	tmp := path + ".tmp"
	if err := writeSynced(tmp, []byte(at.UTC().Format(time.RFC3339Nano)+"\n")); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("writing cache commit stamp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("writing cache commit stamp: %w", err)
	}
	return nil
}
