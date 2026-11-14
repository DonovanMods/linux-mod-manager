// collection_bundle.go is the bundled half of the `.lmmc` container (#623;
// design docs/plans/2026-11-10-collections-design.md §6 and §12, spec
// docs/collection-format.md "Bundled files"): an author's own files - a
// patch, a config pack - carried in the container as files/<item>/…, and
// named in the document by a mods: entry whose source is lmm-collection.
//
// The container is a stranger's file, so its files/ tree is held to the
// rules L1 holds the whole container to, and more:
//
//   - every member is a regular file or a directory - never a link or a
//     device - whose name is relative, has no "." or ".." segment and no
//     empty one, holds no backslash and no control or formatting
//     character, and passes the extractor's zip-slip and reserved-name
//     guards. A name that breaks a rule is refused, never rewritten;
//   - an item's name is validated like a mod id (validBundleItem);
//   - the member, ratio, per-file, per-item and total caps hold before a
//     byte is inflated, and every member is read through a limit that
//     its header's size must match exactly;
//   - each item's content has a digest (bundleDigest), the version a
//     profile records it under: the document states it, and an item whose
//     files do not hash to it is refused. Same content, same version - so
//     a cache entry is never shared by two different trees.
//
// Code is held back like an override that is not text config (import_
// gates.go): an item holding native code, a system script or a loader
// configuration is imported only with ProfileImportOptions.
// AllowAnyOverrides (bundleRestrictions). Nothing in a bundled tree is
// ever read as lmm configuration - no hooks, no sources - and its files
// are written without execute permission.
package core

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	// maxBundleItems caps the bundled items one container carries.
	maxBundleItems = 256
	// maxBundleFileBytes caps one bundled file, inflated.
	maxBundleFileBytes = 64 << 20
	// maxBundleBytes caps every bundled file of a container together,
	// inflated: what an import reads, hashes and keeps in its cache.
	maxBundleBytes = 256 << 20
	// bundleDigestLen is how many hex digits of the sha256 a bundled
	// item's version keeps: 64 bits, which no accident collides and which
	// still reads as a version.
	bundleDigestLen = 16
)

// bundleItemPattern is a bundled item's name: what files/<item>/ is
// called, and the second half of its mod id. Validated like a mod id - a
// single path segment of plain characters - and refused, never rewritten.
var bundleItemPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// bundleCollectionIDPattern is the collection id a bundled item's mod id
// starts with. No dot, so "<collection id>.<item>" splits one way only.
var bundleCollectionIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

// validBundleItem reports whether name can name a bundled item.
func validBundleItem(name string) bool {
	return bundleItemPattern.MatchString(name) && !strings.Contains(name, "..")
}

// validBundleCollectionID reports whether id can lead a bundled item's
// mod id.
func validBundleCollectionID(id string) bool {
	return bundleCollectionIDPattern.MatchString(id)
}

// BundledModID is the profile's mod id of collectionID's bundled item:
// "<collection id>.<item>". The document names the item alone; the
// collection's own id keeps two collections' items of the same name apart.
func BundledModID(collectionID, item string) string {
	return collectionID + "." + item
}

// splitBundledModID splits a bundled mod id into its collection id and
// item, reporting whether it is one.
func splitBundledModID(modID string) (collectionID, item string, ok bool) {
	collectionID, item, ok = strings.Cut(modID, ".")
	if !ok || !validBundleCollectionID(collectionID) || !validBundleItem(item) {
		return "", "", false
	}
	return collectionID, item, true
}

// ErrInvalidBundle refuses a container's files/ tree, or a document's
// bundled item: the caller's input.
var ErrInvalidBundle = errors.New("invalid bundled files")

// bundleMember is one regular file of a bundled item.
type bundleMember struct {
	// rel is its path under files/<item>/, slash-separated.
	rel  string
	size int64
	open func() (io.ReadCloser, error)
}

// bundledItem is one files/<item>/ tree: its members in path order, and
// what reading them found.
type bundledItem struct {
	name string
	// display is the name the document gives the item, when it was read
	// from a kept container (bundledItemName); "" otherwise.
	display string
	members []bundleMember
	bytes   int64
	// digest is the item's version (bundleDigest), set once read.
	digest string
	// restricted lists the members held back as code, with why.
	restricted []RestrictedOverride
}

// collectionBundle is a container's files/ tree, by item name.
type collectionBundle struct {
	items map[string]*bundledItem
}

// item is the bundle's item name, or nil - a nil bundle holds none.
func (b *collectionBundle) item(name string) *bundledItem {
	if b == nil {
		return nil
	}
	return b.items[name]
}

// names lists the bundle's items in name order.
func (b *collectionBundle) names() []string {
	if b == nil {
		return nil
	}
	out := make([]string, 0, len(b.items))
	for name := range b.items {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// checkBundleMemberName refuses a files/ member name that is not plain:
// the rules every bundled name is held to, before the extractor's own
// guards. rel is the name with "files/" cut off.
func checkBundleMemberName(name string) error {
	switch {
	case hasUnsafeRunes(name):
		return fmt.Errorf("%w: the member %q holds a control or formatting character", ErrInvalidBundle, name)
	case strings.Contains(name, `\`):
		return fmt.Errorf("%w: the member %q holds a backslash", ErrInvalidBundle, name)
	case strings.HasPrefix(name, "/"):
		return fmt.Errorf("%w: the member %q is an absolute path", ErrInvalidBundle, name)
	}
	segments := strings.Split(strings.TrimSuffix(name, "/"), "/")
	for _, seg := range segments {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("%w: the member %q has an empty, \".\" or \"..\" path segment", ErrInvalidBundle, name)
		}
	}
	return nil
}

// addBundleMember files one container member under files/ into b, or
// refuses it. f's name has passed the extractor's guards already.
func (b *collectionBundle) addBundleMember(f *zip.File) error {
	if err := checkBundleMemberName(f.Name); err != nil {
		return err
	}
	rest := strings.TrimPrefix(f.Name, collectionFilesDir)
	if rest == "" {
		return nil // the files/ directory entry itself
	}
	item, rel, nested := strings.Cut(rest, "/")
	if !validBundleItem(item) {
		return fmt.Errorf("%w: files/%s: a bundled item's name is 1 to 64 letters, digits, '.', '_' or '-', starting with a letter or digit", ErrInvalidBundle, item)
	}
	if !nested {
		return fmt.Errorf("%w: files/%s is a file; a bundled item is a directory, files/%s/…", ErrInvalidBundle, item, item)
	}
	entry := b.items[item]
	if entry == nil {
		if len(b.items) == maxBundleItems {
			return fmt.Errorf("%w: the container bundles more than %d items", ErrInvalidBundle, maxBundleItems)
		}
		entry = &bundledItem{name: item}
		b.items[item] = entry
	}
	mode := f.Mode()
	if rel == "" || strings.HasSuffix(rel, "/") {
		if mode&^(fs.ModePerm|fs.ModeDir) != 0 {
			return fmt.Errorf("%w: %s is not a plain directory", ErrInvalidBundle, f.Name)
		}
		return nil // a directory entry: its files say what it holds
	}
	if !mode.IsRegular() {
		return fmt.Errorf("%w: %s is not a regular file (a link, a device or a directory is refused)", ErrInvalidBundle, f.Name)
	}
	if f.UncompressedSize64 > maxBundleFileBytes {
		return fmt.Errorf("%w: %s is %d bytes, over the %d-byte limit for one bundled file", ErrInvalidBundle, f.Name, f.UncompressedSize64, maxBundleFileBytes)
	}
	if f.UncompressedSize64 > ratioFloor && f.UncompressedSize64 > maxCollectionRatio*max(f.CompressedSize64, 1) {
		return fmt.Errorf("%w: %s's compression ratio is over %d:1", ErrInvalidBundle, f.Name, maxCollectionRatio)
	}
	for _, m := range entry.members {
		if m.rel == rel {
			return fmt.Errorf("%w: the container holds %s more than once", ErrInvalidBundle, f.Name)
		}
	}
	entry.members = append(entry.members, bundleMember{rel: rel, size: int64(f.UncompressedSize64), open: f.Open})
	entry.bytes += int64(f.UncompressedSize64)
	return nil
}

// finish checks the bundle as a whole and reads every item: its total
// size, a file in every item, and each item's digest and restrictions.
func (b *collectionBundle) finish() error {
	var total int64
	for _, item := range b.items {
		total += item.bytes
	}
	if total > maxBundleBytes {
		return fmt.Errorf("%w: the bundled files are %d bytes together, over the %d-byte limit", ErrInvalidBundle, total, maxBundleBytes)
	}
	for _, name := range b.names() {
		item := b.items[name]
		if len(item.members) == 0 {
			return fmt.Errorf("%w: files/%s/ holds no file", ErrInvalidBundle, name)
		}
		if err := item.read(); err != nil {
			return err
		}
	}
	return nil
}

// read hashes every member of item - through a limit its header's size
// must match exactly - into its digest, and finds the code it holds.
func (item *bundledItem) read() error {
	sort.Slice(item.members, func(i, j int) bool { return item.members[i].rel < item.members[j].rel })
	sums := make([]string, len(item.members))
	for i, m := range item.members {
		sum, head, err := hashBundleMember(m)
		if err != nil {
			return fmt.Errorf("%w: files/%s/%s: %v", ErrInvalidBundle, item.name, m.rel, err)
		}
		sums[i] = sum
		if reason := bundledCodeReason(m.rel, head); reason != "" {
			item.restricted = append(item.restricted, RestrictedOverride{Path: m.rel, Reason: reason})
		}
	}
	item.digest = bundleDigest(item.members, sums)
	return nil
}

// hashBundleMember reads m whole, through a limit: its sha256 and its
// first bytes (for bundledCodeReason). A member whose content is not the
// size its header claims is refused.
func hashBundleMember(m bundleMember) (sum string, head []byte, err error) {
	rc, err := m.open()
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = rc.Close() }()
	h := sha256.New()
	var first bytes.Buffer
	n, err := io.Copy(io.MultiWriter(h, &headWriter{buf: &first, limit: 8}), io.LimitReader(rc, m.size+1))
	if err != nil {
		return "", nil, err
	}
	if n != m.size {
		return "", nil, fmt.Errorf("its content is %d bytes, not the %d its header claims", n, m.size)
	}
	return hex.EncodeToString(h.Sum(nil)), first.Bytes(), nil
}

// headWriter keeps the first limit bytes written to it.
type headWriter struct {
	buf   *bytes.Buffer
	limit int
}

// Write implements io.Writer.
func (w *headWriter) Write(p []byte) (int, error) {
	if room := w.limit - w.buf.Len(); room > 0 {
		w.buf.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

// bundleDigest is a bundled item's version: the first bundleDigestLen hex
// digits of the sha256 of one line per file, in path order -
// "<path>\x00<size>\x00<sha256 hex>\n". It depends on the files' names and
// contents only, so the exporter (reading lmm's cache) and the importer
// (reading the container) agree, and the same tree has the same version
// wherever it came from. sums[i] is members[i]'s sha256.
func bundleDigest(members []bundleMember, sums []string) string {
	h := sha256.New()
	for i, m := range members {
		_, _ = io.WriteString(h, m.rel+"\x00"+strconv.FormatInt(m.size, 10)+"\x00"+sums[i]+"\n")
	}
	return hex.EncodeToString(h.Sum(nil))[:bundleDigestLen]
}

// bundledCodeExts are file types a bundled item may hold only with
// AllowAnyOverrides: native code and scripts the system runs. Game
// scripts the engine runs (.lua, .reds, …) are a mod's ordinary content.
var bundledCodeExts = map[string]bool{
	".dll": true, ".exe": true, ".so": true, ".dylib": true, ".asi": true,
	".sys": true, ".com": true, ".scr": true, ".msi": true, ".pyd": true,
	".bat": true, ".cmd": true, ".ps1": true, ".psm1": true, ".vbs": true,
	".sh": true, ".jar": true,
}

// nativeCodeMagic are the first bytes of native executables and libraries
// (PE, ELF, Mach-O), so a renamed DLL is held back as one.
var nativeCodeMagic = [][]byte{
	[]byte("MZ"), []byte("\x7fELF"),
	{0xfe, 0xed, 0xfa, 0xce}, {0xfe, 0xed, 0xfa, 0xcf}, {0xce, 0xfa, 0xed, 0xfe}, {0xcf, 0xfa, 0xed, 0xfe},
	{0xca, 0xfe, 0xba, 0xbe},
}

// bundledCodeReason is why the bundled file rel (whose content starts with
// head) is held back, or "" when it is not.
func bundledCodeReason(rel string, head []byte) string {
	base := path.Base(rel)
	if isLoaderConfig(base) {
		return "loader configuration: can make the game load a DLL"
	}
	if bundledCodeExts[strings.ToLower(path.Ext(base))] {
		return "code the system runs (" + strings.ToLower(path.Ext(base)) + ")"
	}
	if textConfigOverrideExts[strings.ToLower(path.Ext(base))] {
		return "" // a text config file, whatever its first bytes spell
	}
	for _, magic := range nativeCodeMagic {
		if bytes.HasPrefix(head, magic) {
			return "native code (an executable or a library, whatever its name)"
		}
	}
	return ""
}
