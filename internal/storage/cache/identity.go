package cache

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// KeyMarkerName is the identity marker in a cache entry (#614): it records
// which key's entry the directory is. Key's spelling is not injective - a
// plain version "1.0@abcdef123456" and the variant {Version: "1.0",
// Variant: "abcdef123456"} name one directory - so the directory name alone
// cannot say whose files it holds. Reserved (ReservedPrefix), so it is
// never listed, sized or deployed; CloneMod and CopyModAs carry it like
// every other marker.
//
// It records only the version and the variant, the part of the key the
// directory name spells ambiguously. The game, source and mod are left out
// on purpose: a copy to another identity (CopyModAs) and a shared
// per-game cache_path both keep the entry's version and variant while the
// rest of its key differs.
const KeyMarkerName = ReservedPrefix + "key"

// keyMarkerHeader starts every marker, so a file that is not one is never
// read as a key.
const keyMarkerHeader = "lmm cache key 1"

// KeyCollisionError refuses a cache operation on a directory that belongs
// to a different key spelling the same path (see KeyMarkerName).
type KeyCollisionError struct {
	// Path is the entry's directory.
	Path string
	// Want is the key the operation was for.
	Want Key
	// HaveVersion and HaveVariant are what the entry's marker records;
	// both empty when Legacy or Unreadable.
	HaveVersion, HaveVariant string
	// Legacy is an entry without a marker: written before markers existed,
	// so a plain entry, which a variant key may not take over.
	Legacy bool
	// Unreadable is an entry whose marker could not be read or parsed.
	Unreadable bool
}

// Error describes the collision.
func (e *KeyCollisionError) Error() string {
	want := describeKey(e.Want.Version, e.Want.Variant)
	var have string
	switch {
	case e.Unreadable:
		have = "an entry whose identity marker cannot be read"
	case e.Legacy:
		have = "the plain entry of version " + strconv.Quote(filepath.Base(e.Path))
	default:
		have = "the entry of " + describeKey(e.HaveVersion, e.HaveVariant)
	}
	return fmt.Sprintf("cache directory %s already holds %s, not %s; remove it, or the installed mod that uses it, and retry", e.Path, have, want)
}

func describeKey(version, variant string) string {
	if variant == "" {
		return "version " + strconv.Quote(version)
	}
	return fmt.Sprintf("version %q, variant %s", version, variant)
}

// StampKey writes k's identity marker into dir (an entry, or the staging
// directory about to become one), replacing any marker already there. The
// marker is written to a temporary file, synced, and renamed into place, so
// a crash leaves the old marker or the new one, never an empty file.
func StampKey(dir string, k Key) error {
	body := keyMarkerHeader + "\nversion " + strconv.Quote(k.Version) + "\nvariant " + strconv.Quote(k.Variant) + "\n"
	path := filepath.Join(dir, KeyMarkerName)
	tmp := path + ".tmp"
	if err := writeSynced(tmp, []byte(body)); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("writing cache identity marker: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("writing cache identity marker: %w", err)
	}
	return nil
}

// writeSynced writes body to path and syncs it to disk before closing.
func writeSynced(path string, body []byte) (err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}()
	if _, err := f.Write(body); err != nil {
		return err
	}
	return f.Sync()
}

// CheckKey reports whether dir, an existing entry, is k's.
//
// A marker is a real collision only when it is CURRENT - the key it records
// spells exactly the directory it sits in - and that key is not k: then the
// directory is another key's entry. Every other state is LEGACY, an entry
// whose marker vouches for no key: no marker (written before markers
// existed), an empty or unparseable one (a crash mid-write on an older
// filesystem, a hand edit), or a STALE one, whose key spells another
// directory (an entry renamed after it was stamped - verify --fix's version
// repair, an import's identity rename, a manual mv - whose restamp did not
// happen). A legacy entry is plain, as every entry was before variants
// existed: a plain key owns it (and its next commit restamps it), and a
// variant key is refused, since nothing vouches that the files are that
// variant's.
//
// A marker that cannot be read at all (an unreadable directory) does not
// stop a plain key either, as it would not have before markers existed. A
// dir that does not exist is not checked here; see Claim.
func CheckKey(dir string, k Key) error {
	legacy := func(unreadable bool) error {
		if k.Variant == "" {
			return nil
		}
		return &KeyCollisionError{Path: dir, Want: k, Legacy: !unreadable, Unreadable: unreadable}
	}
	body, err := os.ReadFile(filepath.Join(dir, KeyMarkerName))
	if errors.Is(err, os.ErrNotExist) {
		return legacy(false)
	}
	if err != nil {
		return legacy(true)
	}
	version, variant, ok := parseKeyMarker(body)
	if !ok {
		return legacy(true)
	}
	if !spells(dir, Key{Version: version, Variant: variant}.versionDir()) {
		return legacy(false) // stale: stamped for another directory
	}
	if version != k.Version || variant != k.Variant {
		return &KeyCollisionError{Path: dir, Want: k, HaveVersion: version, HaveVariant: variant}
	}
	return nil
}

// parseKeyMarker reads StampKey's format.
func parseKeyMarker(body []byte) (version, variant string, ok bool) {
	lines := strings.Split(string(bytes.TrimSuffix(body, []byte("\n"))), "\n")
	if len(lines) != 3 || lines[0] != keyMarkerHeader {
		return "", "", false
	}
	field := func(line, name string) (string, bool) {
		raw, found := strings.CutPrefix(line, name+" ")
		if !found {
			return "", false
		}
		v, err := strconv.Unquote(raw)
		return v, err == nil
	}
	version, okV := field(lines[1], "version")
	variant, okR := field(lines[2], "variant")
	return version, variant, okV && okR
}

// Claim reports whether k may write its entry: the directory is absent, or
// already k's (CheckKey). Every writer that creates or replaces an entry
// asks first, in both directions: a variant entry is never minted over a
// plain version's directory, nor a plain version written over a variant's.
func (c *Cache) Claim(k Key) error {
	dir := c.ModPath(k)
	if _, err := os.Lstat(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("checking cache entry %s: %w", dir, err)
	}
	return CheckKey(dir, k)
}

// owns reports whether the existing directory dir is k's entry.
func (c *Cache) owns(dir string, k Key) bool {
	if err := CheckKey(dir, k); err != nil {
		c.log.Debug("cache entry belongs to another key", "path", dir, "err", err)
		return false
	}
	return true
}

// spells reports whether dir is the directory versionDir names: its last
// path elements are exactly versionDir's. A free-text version may contain
// a separator, which ModPath spells as nested directories, so this compares
// the whole version path, not only the last element.
func spells(dir, versionDir string) bool {
	want := filepath.Clean(filepath.FromSlash(versionDir))
	got := filepath.Clean(dir)
	if want == "." || want == "" {
		return false
	}
	return got == want || strings.HasSuffix(got, string(filepath.Separator)+want)
}
