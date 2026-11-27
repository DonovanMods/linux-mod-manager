package cache

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// This file is the cache's read of itself (#678): which entries a game's
// cache holds, how much room each takes, and the removal of one. The
// policy - which entries go - is internal/core's (cache_prune.go); this
// file only reports what is on disk and removes what it is told to.

// pruneTempPrefix names an entry a removal has moved aside and not yet
// finished deleting. Reserved, so no scan lists it as an entry; the next
// scan of its container deletes it.
const pruneTempPrefix = ReservedPrefix + "prune-"

// backupSuffix is commitStagedCache's name for the entry a commit moved
// aside while it swapped the new one in (internal/core/service.go).
const backupSuffix = ".backup"

// ScannedEntry is one cache entry found on disk.
type ScannedEntry struct {
	// Version and Variant are the entry's identity: its identity marker's,
	// or, for a Legacy entry, the directory name read as a plain version.
	Version string
	Variant string
	// Legacy is an entry without a current identity marker (CheckKey).
	Legacy bool
	// Path is the entry's directory.
	Path string
	// ModTime is the directory's modification time, which anything that
	// touches the entry moves.
	ModTime time.Time
	// Committed is when the entry was first committed (CommittedName), and
	// Stamped whether it carries that stamp: an entry from before stamps
	// has only ModTime.
	Committed time.Time
	Stamped   bool
}

// CachedAt is when the entry was cached, as the prune ranks versions: its
// commit stamp, or its modification time for an entry without one.
func (e ScannedEntry) CachedAt() time.Time {
	if e.Stamped {
		return e.Committed
	}
	return e.ModTime
}

// ScannedMod is one mod's container under a cache root: the directory
// every version of the mod lives under.
type ScannedMod struct {
	// Dir is the container's name, ModDirName of the mod's source and ID.
	Dir string
	// Path is the container's directory.
	Path string
	// Entries are the container's cache entries.
	Entries []ScannedEntry
	// Unrecognised names the container's other directories and links,
	// which no marker vouches for as an entry: an entry of a version with
	// a path separator (spelled as nested directories), a commit's
	// backup, anything put there by hand. Nothing removes them.
	Unrecognised []string
}

// Root is the directory gameID's mod containers live in: the base itself
// for a per-game cache_path, else the base's gameID directory.
func (c *Cache) Root(gameID string) string {
	if c.gameScoped {
		return c.basePath
	}
	return filepath.Join(c.basePath, gameID)
}

// ModDirName is the container name of sourceID's mod modID: the segment
// ModPath spells for it.
func ModDirName(sourceID, modID string) string {
	return Key{SourceID: sourceID, ModID: modID}.modDir()
}

// ScanRoot lists the mod containers under root and the entries each
// holds. A directory is an entry only when lmm's markers vouch for it - an
// identity marker, a completion marker or a commit stamp in it - so a
// directory lmm did not write is never taken for one. A missing root is an
// empty cache. It only reads: SweepPruneLeftovers removes what an
// interrupted removal left.
func ScanRoot(root string) ([]ScannedMod, error) {
	containers, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading cache directory %s: %w", root, err)
	}
	var out []ScannedMod
	for _, ce := range containers {
		if !ce.IsDir() || isReserved(ce.Name()) {
			continue
		}
		m, err := scanContainer(root, ce.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

// ScanContainers is ScanRoot for the named mod containers only, in the
// order given; one that does not exist is left out.
func ScanContainers(root string, names []string) ([]ScannedMod, error) {
	var out []ScannedMod
	for _, name := range names {
		if name == "" || name != filepath.Base(name) || isReserved(name) {
			continue
		}
		info, err := os.Lstat(filepath.Join(root, name))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("reading cache directory %s: %w", root, err)
		}
		if !info.IsDir() {
			continue
		}
		m, err := scanContainer(root, name)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

// scanContainer reads one mod container.
func scanContainer(root, name string) (ScannedMod, error) {
	m := ScannedMod{Dir: name, Path: filepath.Join(root, name)}
	children, err := os.ReadDir(m.Path)
	if err != nil {
		return m, fmt.Errorf("reading cache directory %s: %w", m.Path, err)
	}
	for _, child := range children {
		childName := child.Name()
		path := filepath.Join(m.Path, childName)
		if isReserved(childName) {
			continue
		}
		if !child.IsDir() {
			if child.Type()&fs.ModeSymlink != 0 {
				m.Unrecognised = append(m.Unrecognised, childName)
			}
			continue
		}
		e, ok, err := scanEntry(path)
		if err != nil {
			return m, err
		}
		if !ok || strings.HasSuffix(childName, backupSuffix) {
			m.Unrecognised = append(m.Unrecognised, childName)
			continue
		}
		m.Entries = append(m.Entries, e)
	}
	return m, nil
}

// SweepPruneLeftovers deletes what interrupted removals (RemoveEntry) left
// in root's mod containers - the named ones, or every one when names is
// nil. It writes, so a caller holds the mutation slot. Best-effort: what
// it cannot delete stays for the next sweep.
func SweepPruneLeftovers(root string, names []string) {
	if names == nil {
		containers, err := os.ReadDir(root)
		if err != nil {
			return
		}
		for _, ce := range containers {
			if ce.IsDir() && !isReserved(ce.Name()) {
				names = append(names, ce.Name())
			}
		}
	}
	for _, name := range names {
		if name == "" || name != filepath.Base(name) || isReserved(name) {
			continue
		}
		children, err := os.ReadDir(filepath.Join(root, name))
		if err != nil {
			continue
		}
		for _, child := range children {
			if strings.HasPrefix(child.Name(), pruneTempPrefix) {
				_ = os.RemoveAll(filepath.Join(root, name, child.Name()))
			}
		}
	}
}

// scanEntry reads dir as a cache entry: ok when a marker vouches for it.
func scanEntry(dir string) (ScannedEntry, bool, error) {
	children, err := os.ReadDir(dir)
	if err != nil {
		return ScannedEntry{}, false, fmt.Errorf("reading cache entry %s: %w", dir, err)
	}
	marked := false
	for _, c := range children {
		if c.Type().IsRegular() && (c.Name() == KeyMarkerName || c.Name() == CommittedName || FileMarkerName(c.Name())) {
			marked = true
			break
		}
	}
	if !marked {
		return ScannedEntry{}, false, nil
	}
	info, err := os.Stat(dir)
	if err != nil {
		return ScannedEntry{}, false, fmt.Errorf("reading cache entry %s: %w", dir, err)
	}
	e := ScannedEntry{Path: dir, ModTime: info.ModTime(), Version: filepath.Base(dir), Legacy: true}
	e.Committed, e.Stamped = CommittedAt(dir)
	if body, err := os.ReadFile(filepath.Join(dir, KeyMarkerName)); err == nil {
		if version, variant, ok := parseKeyMarker(body); ok && spells(dir, Key{Version: version, Variant: variant}.versionDir()) {
			e.Version, e.Variant, e.Legacy = version, variant, false
		}
	}
	return e, true, nil
}

// DiskUsage is the bytes every regular file under dir takes - lmm's own
// bookkeeping and retained sources included, since removing the directory
// frees them too - and whether another entry's identity marker sits below
// dir: an entry nested inside it, as a version with a path separator is
// spelled, which removing dir would take with it. Symlinks are counted as
// links and never followed.
func DiskUsage(dir string) (bytes int64, nested bool, err error) {
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			return nil
		}
		if d.Name() == KeyMarkerName && filepath.Dir(path) != dir {
			nested = true
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			bytes += info.Size()
		}
		return nil
	})
	if err != nil {
		return 0, false, fmt.Errorf("sizing cache entry %s: %w", dir, err)
	}
	return bytes, nested, nil
}

// RemoveEntry removes the cache entry at dir. It is first renamed aside
// within its container, so the entry is gone in one step and a removal
// that stops part way never leaves a partial entry behind under the
// entry's own name; whatever the delete leaves is swept by the next
// SweepPruneLeftovers.
func RemoveEntry(dir string) error {
	aside := filepath.Join(filepath.Dir(dir), pruneTempPrefix+filepath.Base(dir)+"-"+strconv.FormatInt(time.Now().UnixNano(), 36))
	if err := os.Rename(dir, aside); err != nil {
		return fmt.Errorf("removing cache entry %s: %w", dir, err)
	}
	if err := os.RemoveAll(aside); err != nil {
		return fmt.Errorf("removing cache entry %s: %w", dir, err)
	}
	return nil
}
