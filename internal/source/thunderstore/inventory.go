// Package thunderstore: this file is the index INVENTORY (#410) - listing
// the community indexes on disk, and removing one.
//
// Removal is the only place this package deletes anything it did not just
// stage itself, so it is FAIL-CLOSED: a community directory is removed only
// when it can be proved, at that moment and under the build lock, to hold
// nothing but this source's own index files. A symbolic link anywhere
// below the cache root, a subdirectory, or a single file lmm did not write
// is a refusal with nothing removed. Files are removed one by one and the
// directory with a plain rmdir, so even a check this file got wrong could
// not take anything with it that it had not named.
package thunderstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

var _ source.IndexInventory = (*Source)(nil)

// indexFileNames are the files a finished index consists of.
var indexFileNames = []string{watermarkFileName, indexFileName, packagesFileName, lockFileName}

// stagingPrefixes are the temp files a build or a watermark write creates
// beside them (builder, stageFile), which an interrupted build can leave.
var stagingPrefixes = []string{".packages-", ".index-", ".stage-"}

// maxCreateTempDigits is the longest suffix os.CreateTemp gives a name: a
// uint32 in decimal.
const maxCreateTempDigits = 10

// isIndexName reports whether name is one this package gives a file in a
// community directory: an index file's exact name, or a staging prefix
// followed by exactly the decimal suffix os.CreateTemp appends (T3 review
// F6) - so ".index-my-backup.json" is not lmm's, however it starts.
func isIndexName(name string) bool {
	for _, known := range indexFileNames {
		if name == known {
			return true
		}
	}
	for _, prefix := range stagingPrefixes {
		if suffix, ok := strings.CutPrefix(name, prefix); ok {
			return suffix != "" && len(suffix) <= maxCreateTempDigits && strings.Trim(suffix, "0123456789") == ""
		}
	}
	return false
}

// ownedHeads are the bytes each kind of file lmm writes begins with - the
// first thing its writer puts there. A file with an lmm name and other
// content is not lmm's.
func ownedHeads(name string) []string {
	switch {
	case name == lockFileName:
		return nil // lmm never writes a byte to its lock file
	case name == indexFileName, strings.HasPrefix(name, ".index-"):
		return []string{`{"schema":`}
	case name == packagesFileName, strings.HasPrefix(name, ".packages-"):
		// A record, or - for a community with no packages - the trailer.
		return []string{`{"full_name":`, `{"generation":`}
	default: // watermark.json and its .stage- staging copy
		return []string{`{"last_modified":`}
	}
}

// ownedHeadLen is how much of a file isOwnedContent needs to see: at least
// the longest of ownedHeads.
const ownedHeadLen = 32

// isOwnedContent reports whether a file named name, of size bytes and
// beginning with head, is one lmm wrote (T3 review F6). An EMPTY file is:
// an interrupted build leaves one, and it holds nothing to lose.
func isOwnedContent(name string, size int64, head []byte) bool {
	if size == 0 {
		return true
	}
	for _, want := range ownedHeads(name) {
		if strings.HasPrefix(string(head), want) {
			return true
		}
	}
	return false
}

// readHead reads up to ownedHeadLen bytes from f.
func readHead(f *os.File) ([]byte, error) {
	head := make([]byte, ownedHeadLen)
	n, err := io.ReadFull(f, head)
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		err = nil
	}
	return head[:n], err
}

// CachedIndexes implements source.IndexInventory: every community
// directory under the index root, usable or not, with what it costs and
// whether it could be removed. A name no community can have, and anything
// that is not a directory or a link, is not this source's and is left out.
//
// An index root that is itself a symbolic link is an ERROR: nothing below
// it is provably lmm's, and a caller that could not list must not prune.
//
// An index root that is itself a symbolic link is LISTED - lmm builds and
// searches indexes through it, and hiding what is there behind "no
// indexes" was not honest (T3 review F7) - with every entry refused for
// removal: nothing below a link lmm did not make is provably lmm's.
func (s *Source) CachedIndexes(ctx context.Context) ([]source.CachedIndex, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, ok, err := s.store.safeRoot()
	linked := ""
	var link *rootLinkError
	if errors.As(err, &link) {
		root, ok, err, linked = s.store.root, true, nil, link.Error()
	}
	if err != nil || !ok {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", root, err)
	}
	var out []source.CachedIndex
	for _, e := range entries {
		name := e.Name()
		if !communityPattern.MatchString(name) {
			continue
		}
		switch {
		case e.Type()&fs.ModeSymlink != 0:
			out = append(out, source.CachedIndex{GameID: name, Reason: "it is a symbolic link, which lmm never removes anything through"})
		case e.IsDir():
			ci := s.inspect(name)
			if linked != "" {
				ci.Removable, ci.Reason = false, linked
			}
			out = append(out, ci)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GameID < out[j].GameID })
	return out, nil
}

// inspect describes one community directory.
func (s *Source) inspect(community string) source.CachedIndex {
	ci := source.CachedIndex{GameID: community, Bytes: s.store.dirFootprint(community)}
	if _, err := s.store.provablyIndex(community); err != nil {
		ci.Reason = err.Error()
	} else if err := s.store.removable(community); err != nil {
		ci.Reason = err.Error()
	} else {
		ci.Removable = true
	}
	if wm, ok := s.store.rawWatermark(community); ok {
		ci.Packages = wm.Packages
		if wm.FetchedAt > 0 {
			ci.FetchedAt = time.Unix(wm.FetchedAt, 0).UTC()
		}
	}
	// The cheap check, not the full one: a listing reads every index on
	// disk and must not parse each one's row table to say so. A torn index
	// that passes it is still refused by the next search's full check.
	if _, ok := s.store.state(community); ok {
		ci.Present = true
	}
	return ci
}

// RemoveIndex implements source.IndexInventory: delete one community's
// index, or refuse with nothing deleted.
//
// It works through DIRECTORY HANDLES, not paths (#410 review): the index
// root and the community directory are each opened once, proved to be the
// real directories that were inspected, and every listing, check and
// removal after that goes through the handle - so a directory swapped for a
// symbolic link between the check and the act is an error, never a
// redirection. Only the names the proof approved are removed, each
// re-checked as a regular file first.
//
// It takes the same two locks a build does - this process's per-community
// mutex and the cross-process flock, reached through the same handle - so
// it can never remove files a build is writing, and it removes the
// directory itself while still holding them. ifFetchedAt, when set, must
// still be the index's fetched_at under the lock: a removal decided on an
// index's age is refused once a refresh has replaced that index.
func (s *Source) RemoveIndex(ctx context.Context, community string, ifFetchedAt time.Time) (int64, error) {
	if err := validateCommunity(community); err != nil {
		return 0, err
	}
	rootPath, ok, err := s.store.safeRoot()
	if err != nil || !ok {
		return 0, err
	}
	if _, err := os.Lstat(filepath.Join(rootPath, community)); errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}

	lock := s.communityLock(community)
	lock.Lock()
	defer lock.Unlock()

	root, err := openRealDir(rootPath)
	if err != nil {
		return 0, fmt.Errorf("not removing the %s index: %w", community, err)
	}
	defer func() { _ = root.Close() }()
	dir, err := openRealSubdir(root, community)
	if err != nil {
		return 0, fmt.Errorf("not removing the %s index: %w", community, err)
	}
	defer func() { _ = dir.Close() }()

	release, err := acquireLock(ctx, community, lockTarget{
		open: func() (*os.File, error) { return openLockAt(dir, community) },
		stat: func() (os.FileInfo, error) { return dir.Lstat(lockFileName) },
	})
	if err != nil {
		return 0, fmt.Errorf("not removing the %s index: %w", community, err)
	}
	defer release()

	names, freed, err := provableEntries(dir, community)
	if err != nil {
		return 0, fmt.Errorf("not removing the %s index: %w", community, err)
	}
	if !ifFetchedAt.IsZero() {
		if err := checkFetchedAt(dir, community, ifFetchedAt); err != nil {
			return 0, err
		}
	}

	// The watermark goes first, so an interrupted removal reads as cold -
	// the same ordering rule a build's commit follows - and the lock file
	// last, while it is still held.
	sort.SliceStable(names, func(i, j int) bool { return removalRank(names[i]) < removalRank(names[j]) })
	s.dropResident(community)
	for _, name := range names {
		info, err := dir.Lstat(name)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() {
			return 0, fmt.Errorf("not removing the rest of the %s index: %s changed while it was being removed", community, name)
		}
		if err := dir.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return 0, fmt.Errorf("removing %s from the %s index: %w", name, community, err)
		}
	}
	// The directory goes while the lock is still held. It can fail only if
	// another process re-created its lock file there in the meantime,
	// which leaves an empty directory with nothing of the index in it -
	// harmless, listed as unbuilt, and removed by the next prune - so the
	// index is reported removed either way.
	_ = root.Remove(community)
	return freed, nil
}

// openLockAt opens community's lock file relative to the proven directory
// handle dir, never through a symbolic link (T3 review F5).
//
// Not dir.OpenFile: os.Root adds O_NOFOLLOW itself and then FOLLOWS any
// link whose target stays inside the root, so a ".lock" linked to
// index.json locked the index - and a dangling one had lmm create its
// target. openat(2) with O_NOFOLLOW, on the directory's own descriptor,
// refuses a link outright, and O_NONBLOCK keeps a FIFO planted there from
// blocking the open.
func openLockAt(dir *os.Root, community string) (*os.File, error) {
	handle, err := dir.Open(".")
	if err != nil {
		return nil, fmt.Errorf("opening the %s index directory: %w", community, err)
	}
	defer func() { _ = handle.Close() }()
	fd, err := syscall.Openat(int(handle.Fd()), lockFileName,
		syscall.O_RDWR|syscall.O_CREAT|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0o600)
	if errors.Is(err, syscall.ELOOP) {
		return nil, fmt.Errorf("the %s index lock is a symbolic link, which lmm never follows", community)
	}
	if err != nil {
		return nil, fmt.Errorf("opening the %s index lock: %w", community, err)
	}
	file := os.NewFile(uintptr(fd), lockFileName)
	if info, err := file.Stat(); err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, fmt.Errorf("the %s index lock is not a plain file: not locking through it", community)
	}
	return file, nil
}

// openRealDir opens path as a directory handle and proves the handle is the
// directory at path itself, not something a symbolic link there leads to.
func openRealDir(path string) (*os.Root, error) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	named, err := os.Lstat(path)
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	opened, err := root.Stat(".")
	if err != nil || named.Mode()&fs.ModeSymlink != 0 || !os.SameFile(named, opened) {
		_ = root.Close()
		return nil, fmt.Errorf("%s is not the directory lmm inspected (a symbolic link, or replaced): not touching it", path)
	}
	return root, nil
}

// openRealSubdir is openRealDir for name inside root.
func openRealSubdir(root *os.Root, name string) (*os.Root, error) {
	named, err := root.Lstat(name)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", name, err)
	}
	if named.Mode()&fs.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s is a symbolic link, which lmm never removes anything through", name)
	}
	if !named.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", name)
	}
	sub, err := root.OpenRoot(name)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", name, err)
	}
	opened, err := sub.Stat(".")
	if err != nil || !os.SameFile(named, opened) {
		_ = sub.Close()
		return nil, fmt.Errorf("%s was replaced while it was being checked: not touching it", name)
	}
	return sub, nil
}

// provableEntries lists dir through its handle and proves every entry is a
// regular file this package writes, returning their names and total size.
func provableEntries(dir *os.Root, community string) ([]string, int64, error) {
	handle, err := dir.Open(".")
	if err != nil {
		return nil, 0, fmt.Errorf("reading the %s index: %w", community, err)
	}
	defer func() { _ = handle.Close() }()
	entries, err := handle.ReadDir(-1)
	if err != nil {
		return nil, 0, fmt.Errorf("reading the %s index: %w", community, err)
	}
	names := make([]string, 0, len(entries))
	var total int64
	for _, e := range entries {
		if !e.Type().IsRegular() {
			return nil, 0, fmt.Errorf("the %s index directory holds %s, which is not a plain file lmm wrote", community, e.Name())
		}
		if !isIndexName(e.Name()) {
			return nil, 0, fmt.Errorf("the %s index directory holds %s, which is not part of an index", community, e.Name())
		}
		info, err := e.Info()
		if err != nil {
			return nil, 0, fmt.Errorf("reading %s: %w", e.Name(), err)
		}
		if err := ownedAt(dir, community, e.Name(), info); err != nil {
			return nil, 0, err
		}
		names = append(names, e.Name())
		total += info.Size()
	}
	return names, total, nil
}

// ownedAt proves the file name in dir, described by listed, holds what lmm
// writes there. It is opened through the handle without following a link
// or blocking on a FIFO, and must still be the file that was listed.
func ownedAt(dir *os.Root, community, name string, listed os.FileInfo) error {
	f, err := dir.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return fmt.Errorf("reading %s in the %s index: %w", name, community, err)
	}
	defer func() { _ = f.Close() }()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(listed, opened) {
		return fmt.Errorf("the %s index directory's %s changed while it was being checked", community, name)
	}
	head, err := readHead(f)
	if err != nil {
		return fmt.Errorf("reading %s in the %s index: %w", name, community, err)
	}
	if !isOwnedContent(name, opened.Size(), head) {
		return fmt.Errorf("the %s index directory holds %s, whose content lmm did not write", community, name)
	}
	return nil
}

// checkFetchedAt refuses unless the index in dir was last fetched at want.
func checkFetchedAt(dir *os.Root, community string, want time.Time) error {
	f, err := dir.Open(watermarkFileName)
	if err != nil {
		return fmt.Errorf("not removing the %s index: its age can no longer be read: %w", community, err)
	}
	defer func() { _ = f.Close() }()
	var wm watermark
	if err := json.NewDecoder(f).Decode(&wm); err != nil {
		return fmt.Errorf("not removing the %s index: its age can no longer be read: %w", community, err)
	}
	if wm.FetchedAt != want.Unix() {
		return fmt.Errorf("not removing the %s index: it was refreshed after the prune decided to remove it", community)
	}
	return nil
}

// removalRank orders a directory's files for removal.
func removalRank(name string) int {
	switch name {
	case watermarkFileName:
		return 0
	case lockFileName:
		return 2
	default:
		return 1
	}
}

// safeRoot returns the index root, whether it exists, and an error when it
// is not a real directory. The cache directory ABOVE it may be a link - a
// user keeping lmm's cache on another disk is the ordinary case - but the
// root itself is lmm's to create, and a link there points somewhere lmm
// did not.
func (st *store) safeRoot() (string, bool, error) {
	if st.root == "" {
		return "", false, nil
	}
	info, err := os.Lstat(st.root)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return st.root, false, nil
	case err != nil:
		return "", false, fmt.Errorf("reading %s: %w", st.root, err)
	case info.Mode()&fs.ModeSymlink != 0:
		return "", false, &rootLinkError{root: st.root}
	case !info.IsDir():
		return "", false, fmt.Errorf("%s is not a directory", st.root)
	}
	return st.root, true, nil
}

// rootLinkError is safeRoot's refusal of an index root that is a symbolic
// link.
type rootLinkError struct{ root string }

func (e *rootLinkError) Error() string {
	return fmt.Sprintf("%s is a symbolic link: lmm builds and searches indexes through it, but never removes anything through one "+
		"(to keep indexes on another disk, link lmm's whole cache directory instead)", e.root)
}

// access(2) modes: write, and search (execute) on a directory.
const (
	accessWrite  = 0x2
	accessSearch = 0x1
)

// removable reports why lmm could not remove community's directory even
// though it is provably an index: it has no write permission on it, or on
// the root that holds it (T3 review F11). A dry run then says so, rather
// than promising a removal the real run reports as failed.
func (st *store) removable(community string) error {
	for _, dir := range []string{st.dir(community), st.root} {
		if err := syscall.Access(dir, accessWrite|accessSearch); err != nil {
			return fmt.Errorf("lmm cannot remove this index: it has no write permission on %s (%w)", dir, err)
		}
	}
	return nil
}

// provablyIndex proves community's directory holds nothing but this
// source's own files, and returns their total size. Any doubt is an error.
func (st *store) provablyIndex(community string) (int64, error) {
	root, ok, err := st.safeRoot()
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, fmt.Errorf("no index root")
	}
	dir := filepath.Join(root, community)
	info, err := os.Lstat(dir)
	if err != nil {
		return 0, fmt.Errorf("reading %s: %w", dir, err)
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return 0, fmt.Errorf("%s is a symbolic link, which lmm never removes anything through", dir)
	}
	if !info.IsDir() {
		return 0, fmt.Errorf("%s is not a directory", dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("reading %s: %w", dir, err)
	}
	handle, err := os.OpenRoot(dir)
	if err != nil {
		return 0, fmt.Errorf("reading %s: %w", dir, err)
	}
	defer func() { _ = handle.Close() }()
	var total int64
	for _, e := range entries {
		if !e.Type().IsRegular() {
			return 0, fmt.Errorf("%s holds %s, which is not a plain file lmm wrote", dir, e.Name())
		}
		if !isIndexName(e.Name()) {
			return 0, fmt.Errorf("%s holds %s, which is not part of an index", dir, e.Name())
		}
		fi, err := e.Info()
		if err != nil {
			return 0, fmt.Errorf("reading %s: %w", filepath.Join(dir, e.Name()), err)
		}
		if err := ownedAt(handle, community, e.Name(), fi); err != nil {
			return 0, err
		}
		total += fi.Size()
	}
	return total, nil
}

// dirFootprint is what a community directory costs on disk - every regular
// file directly in it, whether or not lmm wrote it, because that is what a
// user deciding whether to prune is weighing. Best-effort: an unreadable
// entry contributes nothing.
func (st *store) dirFootprint(community string) int64 {
	dir := st.dir(community)
	if dir == "" {
		return 0
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	var total int64
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		if info, err := e.Info(); err == nil {
			total += info.Size()
		}
	}
	return total
}

// rawWatermark reads a community's watermark whatever its schema: an index
// an older lmm wrote is unusable, and its age is still worth knowing.
func (st *store) rawWatermark(community string) (watermark, bool) {
	dir := st.dir(community)
	if dir == "" {
		return watermark{}, false
	}
	data, err := os.ReadFile(filepath.Join(dir, watermarkFileName))
	if err != nil {
		return watermark{}, false
	}
	var wm watermark
	if err := json.Unmarshal(data, &wm); err != nil {
		return watermark{}, false
	}
	return wm, true
}
