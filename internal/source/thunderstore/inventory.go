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
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

var _ source.IndexInventory = (*Source)(nil)

// indexFileNames are the files a finished index consists of.
var indexFileNames = []string{watermarkFileName, indexFileName, packagesFileName, lockFileName}

// stagingPrefixes are the temp files a build or a watermark write creates
// beside them (builder, stageFile), which an interrupted build can leave.
var stagingPrefixes = []string{".packages-", ".index-", ".stage-"}

// isIndexFile reports whether name is one this package writes into a
// community directory.
func isIndexFile(name string) bool {
	for _, known := range indexFileNames {
		if name == known {
			return true
		}
	}
	for _, prefix := range stagingPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// CachedIndexes implements source.IndexInventory: every community
// directory under the index root, usable or not, with what it costs and
// whether it could be removed. A name no community can have, and anything
// that is not a directory or a link, is not this source's and is left out.
//
// An index root that is itself a symbolic link is an ERROR: nothing below
// it is provably lmm's, and a caller that could not list must not prune.
func (s *Source) CachedIndexes(ctx context.Context) ([]source.CachedIndex, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, ok, err := s.store.safeRoot()
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
			out = append(out, source.CachedIndex{GameID: name, Reason: "it is a symbolic link, which lmm never follows"})
		case e.IsDir():
			out = append(out, s.inspect(name))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GameID < out[j].GameID })
	return out, nil
}

// inspect describes one community directory.
func (s *Source) inspect(community string) source.CachedIndex {
	ci := source.CachedIndex{GameID: community}
	bytes, err := s.store.provablyIndex(community)
	ci.Bytes = bytes
	if err != nil {
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
// It takes the same two locks a build does - this process's per-community
// mutex and the cross-process flock - so it can never remove files a build
// is writing, and it re-proves the directory under them.
func (s *Source) RemoveIndex(ctx context.Context, community string) (int64, error) {
	if err := validateCommunity(community); err != nil {
		return 0, err
	}
	root, ok, err := s.store.safeRoot()
	if err != nil || !ok {
		return 0, err
	}
	dir := filepath.Join(root, community)
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}

	lock := s.communityLock(community)
	lock.Lock()
	defer lock.Unlock()
	// Proved BEFORE the flock, too: lockCommunity creates its lock file, and
	// through a symlinked community directory that would be a write
	// somewhere lmm does not own.
	if _, err := s.store.provablyIndex(community); err != nil {
		return 0, fmt.Errorf("not removing the %s index: %w", community, err)
	}
	unlock, err := s.store.lockCommunity(ctx, community)
	if err != nil {
		return 0, fmt.Errorf("not removing the %s index: %w", community, err)
	}
	released := false
	defer func() {
		if !released {
			unlock()
		}
	}()

	freed, err := s.store.provablyIndex(community)
	if err != nil {
		return 0, fmt.Errorf("not removing the %s index: %w", community, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("reading %s: %w", dir, err)
	}
	// The watermark goes first, so an interrupted removal reads as cold -
	// the same ordering rule a build's commit follows - and the lock file
	// last, while it is still held.
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.SliceStable(names, func(i, j int) bool { return removalRank(names[i]) < removalRank(names[j]) })
	s.dropResident(community)
	for _, name := range names {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return 0, fmt.Errorf("removing %s: %w", filepath.Join(dir, name), err)
		}
	}
	unlock()
	released = true
	if err := os.Remove(dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return freed, fmt.Errorf("removing %s: %w", dir, err)
	}
	return freed, nil
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
		return "", false, fmt.Errorf("%s is a symbolic link, which lmm never follows: not touching anything under it", st.root)
	case !info.IsDir():
		return "", false, fmt.Errorf("%s is not a directory", st.root)
	}
	return st.root, true, nil
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
		return 0, fmt.Errorf("%s is a symbolic link, which lmm never follows", dir)
	}
	if !info.IsDir() {
		return 0, fmt.Errorf("%s is not a directory", dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("reading %s: %w", dir, err)
	}
	var total int64
	for _, e := range entries {
		if !e.Type().IsRegular() {
			return 0, fmt.Errorf("%s holds %s, which is not a plain file lmm wrote", dir, e.Name())
		}
		if !isIndexFile(e.Name()) {
			return 0, fmt.Errorf("%s holds %s, which is not part of an index", dir, e.Name())
		}
		fi, err := e.Info()
		if err != nil {
			return 0, fmt.Errorf("reading %s: %w", filepath.Join(dir, e.Name()), err)
		}
		total += fi.Size()
	}
	return total, nil
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
