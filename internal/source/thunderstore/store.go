// Package thunderstore: this file is the index ON DISK - its layout, its
// two record shapes, and the write-then-rename discipline that makes a
// torn refresh read as cold rather than as valid-but-wrong.
//
// Layout, under <CacheDir>/_thunderstore/<community>/:
//
//	index.json        {"schema", "rows":[...], "generation"} - the searchable
//	                  projection + an offset table
//	packages.jsonl    one full package record per line, byte-addressed by
//	                  index.json, ending in a {"generation"} trailer line
//	watermark.json    {"last_modified", "fetched_at", "packages", "schema",
//	                  "generation"}
//
// The GENERATION is what makes the three files one index rather than three
// files (T1 review #2). It is the SHA-256 of the record stream, so it is a
// pure function of the document indexed - a second build of the same
// document is still byte-for-byte identical - and it appears in all three
// files. index.json addresses THIS packages.jsonl only if the trailer at
// the end of packages.jsonl names index.json's own generation and lies
// exactly at the end of the file; anything else is a directory two builds
// left behind, and reads as cold.
//
// The searchable projection is 2.6% of the decoded document (measured), so
// a search loads a few megabytes rather than a few hundred, and the detail
// store is never read at all until something asks about one package.
package thunderstore

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"os"
	"path/filepath"
	"time"
)

const (
	// indexSchema is the on-disk format version. A watermark written by a
	// different schema reads as COLD: the files beside it may have any
	// shape at all, and rebuilding costs one request.
	//
	// 2 added the generation stamp (T1 review #2), which changed the shape
	// of both data files: index.json gained its object wrapper and
	// packages.jsonl its trailer line.
	indexSchema = 2
	// rootDirName is the cache root's subdirectory. The "_" prefix is
	// unreachable as a game slug (core.DeriveGameID never emits one), so
	// this tree can never collide with the game-scoped mod cache that
	// shares the root - steamworkshop's convention, for its reason.
	rootDirName = "_thunderstore"

	indexFileName     = "index.json"
	packagesFileName  = "packages.jsonl"
	watermarkFileName = "watermark.json"
)

// watermark is what makes an index on disk VALID. It is renamed into place
// last, after both data files, and removed before either of them is
// replaced - so a crash mid-refresh leaves a directory that reads as cold.
type watermark struct {
	// LastModified is the upstream Last-Modified header verbatim, sent back
	// as If-Modified-Since on the next refresh.
	LastModified string `json:"last_modified"`
	// FetchedAt is when the copy on disk was last confirmed current (a 304
	// stamps it without touching anything else), as a Unix second. Judged
	// against the TTL here rather than trusted from a file's mtime, which a
	// backup restore or a copy would move.
	FetchedAt int64 `json:"fetched_at"`
	Packages  int   `json:"packages"`
	Schema    int   `json:"schema"`
	// Generation is the build this watermark belongs to - the SHA-256 of
	// the record stream, repeated in both data files. A watermark whose
	// generation is not the one index.json carries belongs to a DIFFERENT
	// build of this community, which is the state two processes committing
	// at once used to leave behind.
	Generation string `json:"generation"`
}

// indexFile is index.json: the row table plus the generation that says
// which packages.jsonl those rows address. Written a row at a time by
// builder (which is why nothing marshals this type), read whole.
type indexFile struct {
	Schema     int        `json:"schema"`
	Rows       []indexRow `json:"rows"`
	Generation string     `json:"generation"`
}

// indexRow is one row of index.json: everything a search needs, plus where
// the full record lives in packages.jsonl. Rows are written as fixed-shape
// ARRAYS rather than objects - repeating the field names would be a third
// of the file - so the two methods below are the format.
type indexRow struct {
	FullName      string
	Description   string
	Categories    []string
	DateUpdated   string
	LatestVersion string
	Deprecated    bool
	// Offset and Length address the record in packages.jsonl, excluding its
	// trailing newline: bytes [Offset, Offset+Length).
	Offset int64
	Length int64
}

// MarshalJSON writes the row as its fixed 8-element array.
func (r indexRow) MarshalJSON() ([]byte, error) {
	cats := r.Categories
	if cats == nil {
		cats = []string{}
	}
	return json.Marshal([]any{
		r.FullName, r.Description, cats, r.DateUpdated, r.LatestVersion, r.Deprecated, r.Offset, r.Length,
	})
}

// UnmarshalJSON reads the fixed 8-element array MarshalJSON writes. A row
// of any other shape is a corrupt index, which the caller treats as cold.
func (r *indexRow) UnmarshalJSON(data []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if len(raw) != 8 {
		return fmt.Errorf("index row has %d fields, want 8", len(raw))
	}
	fields := []any{
		&r.FullName, &r.Description, &r.Categories, &r.DateUpdated,
		&r.LatestVersion, &r.Deprecated, &r.Offset, &r.Length,
	}
	for i, target := range fields {
		if err := json.Unmarshal(raw[i], target); err != nil {
			return fmt.Errorf("index row field %d: %w", i, err)
		}
	}
	return nil
}

// versionRow is one version inside a packages.jsonl record, written as the
// fixed array [version_number, file_size, date_created, dependencies[]].
// download_url, icon and the per-version name are NOT stored: each is a
// pure function of owner/name/version, which is 30% of the file saved for
// a fmt.Sprintf.
type versionRow struct {
	Version      string
	FileSize     int64
	DateCreated  string
	Dependencies []string
}

// MarshalJSON writes the version as its fixed 4-element array.
func (v versionRow) MarshalJSON() ([]byte, error) {
	deps := v.Dependencies
	if deps == nil {
		deps = []string{}
	}
	return json.Marshal([]any{v.Version, v.FileSize, v.DateCreated, deps})
}

// UnmarshalJSON reads the fixed 4-element array MarshalJSON writes.
func (v *versionRow) UnmarshalJSON(data []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if len(raw) != 4 {
		return fmt.Errorf("version row has %d fields, want 4", len(raw))
	}
	fields := []any{&v.Version, &v.FileSize, &v.DateCreated, &v.Dependencies}
	for i, target := range fields {
		if err := json.Unmarshal(raw[i], target); err != nil {
			return fmt.Errorf("version row field %d: %w", i, err)
		}
	}
	return nil
}

// packageRecord is one line of packages.jsonl: the lean form of a package.
// Versions are NOT capped - a cap of 10 saves a third of the file and costs
// the ability to roll back to anything older, and this sits beside a mod
// cache measured in gigabytes.
type packageRecord struct {
	FullName    string       `json:"full_name"`
	DateUpdated string       `json:"date_updated"`
	Categories  []string     `json:"categories"`
	Deprecated  bool         `json:"is_deprecated"`
	Description string       `json:"description"`
	WebsiteURL  string       `json:"website_url"`
	Versions    []versionRow `json:"versions"`
}

// store owns the index tree. A zero-value root (no CacheDir configured)
// disables it entirely: every read reports "absent" and every write is
// refused, which surfaces as ErrIndexUnavailable rather than as a silent
// re-download per query.
type store struct{ root string }

func newStore(cacheDir string) *store {
	if cacheDir == "" {
		return &store{}
	}
	return &store{root: filepath.Join(cacheDir, rootDirName)}
}

// dir returns community's index directory, or "" when there is no cache
// root. The slug is validated by validateCommunity before it ever reaches
// here - this function joins, it does not sanitize.
func (st *store) dir(community string) string {
	if st.root == "" {
		return ""
	}
	return filepath.Join(st.root, community)
}

// state reports the watermark of an index for community and whether the
// directory holds one at all. CHEAP by design - a watermark of this schema
// and two non-empty data files beside it - because Search asks on every
// query. verify is the full check.
func (st *store) state(community string) (watermark, bool) {
	dir := st.dir(community)
	if dir == "" {
		return watermark{}, false
	}
	data, err := os.ReadFile(filepath.Join(dir, watermarkFileName))
	if err != nil {
		return watermark{}, false
	}
	var wm watermark
	if err := json.Unmarshal(data, &wm); err != nil || wm.Schema != indexSchema {
		return watermark{}, false
	}
	for _, name := range []string{indexFileName, packagesFileName} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil || info.Size() == 0 {
			return watermark{}, false
		}
	}
	return wm, true
}

// verify is state plus the check state is too hot to make: that the two
// data files are the SAME BUILD, exactly.
//
// The size check this replaced ("packages.jsonl is long enough to contain
// every byte range index.json addresses") catches a truncated file and
// nothing else. Two builds of one community are close in size, so an
// index.json left beside another build's packages.jsonl passed it while
// every offset in it addressed another document (T1 review #2). The
// generation is exact: it names the build, it appears in all three files,
// and packages.jsonl's copy of it sits at a position only ITS OWN last row
// can point at.
//
// Paid once per index generation per process (see Source.usable): the
// resident copy that follows has already been read out of these same
// bytes, so re-checking on every query would buy nothing.
// It RETURNS the rows it parsed (T1 review #4). Checking the index means
// reading and parsing the whole of index.json, and the caller's very next
// act is to build the search copy out of those same bytes - so parsing
// them twice, once to answer "is this valid" and once to answer "what is
// in it", was 97 ms and 8.22 MB of the largest community's warm search
// spent computing an answer that was already on the stack.
func (st *store) verify(community string) (watermark, []indexRow, bool) {
	wm, ok := st.state(community)
	if !ok {
		return watermark{}, nil, false
	}
	idx, err := st.loadIndex(community)
	if err != nil {
		return watermark{}, nil, false
	}
	if idx.Generation == "" || idx.Generation != wm.Generation {
		return watermark{}, nil, false
	}
	if !st.packagesCarry(community, idx) {
		return watermark{}, nil, false
	}
	return wm, idx.Rows, true
}

// packagesCarry reports whether packages.jsonl is the file idx addresses:
// its trailer names idx's generation AND begins exactly where idx's last
// row ends AND runs to the end of the file.
//
// Three facts for one ReadAt, because the trailer's POSITION is as much
// of the check as its content: a truncated file fails the size test, a
// file from another build fails the generation test, and a file with an
// extra or a missing record fails both by displacing the trailer.
func (st *store) packagesCarry(community string, idx indexFile) bool {
	at := int64(0)
	if n := len(idx.Rows); n > 0 {
		last := idx.Rows[n-1]
		at = last.Offset + last.Length + 1
	}
	if at < 0 {
		return false // a corrupt row table, whose offsets address nothing
	}

	trailer := packagesTrailer(idx.Generation)
	path := filepath.Join(st.dir(community), packagesFileName)
	info, err := os.Stat(path)
	if err != nil || info.Size() != at+int64(len(trailer)) {
		return false
	}
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = file.Close() }()

	buf := make([]byte, len(trailer))
	if _, err := file.ReadAt(buf, at); err != nil {
		return false
	}
	return string(buf) == trailer
}

// packagesTrailer is the last line of packages.jsonl for generation: the
// stamp that ties the file to one build. Fixed shape, so verify can read
// it with one positioned read rather than scanning backwards for a
// newline.
func packagesTrailer(generation string) string {
	return fmt.Sprintf("{\"generation\":%q}\n", generation)
}

// loadIndex reads index.json whole.
func (st *store) loadIndex(community string) (indexFile, error) {
	dir := st.dir(community)
	if dir == "" {
		return indexFile{}, fmt.Errorf("no cache directory configured")
	}
	data, err := os.ReadFile(filepath.Join(dir, indexFileName))
	if err != nil {
		return indexFile{}, err
	}
	var idx indexFile
	if err := json.Unmarshal(data, &idx); err != nil {
		return indexFile{}, fmt.Errorf("reading %s: %w", indexFileName, err)
	}
	return idx, nil
}

// footprint is the index's on-disk size, for the frontends that show what a
// community costs. Best-effort: an unreadable file contributes nothing
// rather than failing a status read.
func (st *store) footprint(community string) int64 {
	dir := st.dir(community)
	if dir == "" {
		return 0
	}
	var total int64
	for _, name := range []string{indexFileName, packagesFileName, watermarkFileName} {
		if info, err := os.Stat(filepath.Join(dir, name)); err == nil {
			total += info.Size()
		}
	}
	return total
}

// stampWatermark rewrites the watermark with a new fetched_at and nothing
// else - what a 304 earns: the copy on disk is current, so the TTL restarts
// without a byte moving.
func (st *store) stampWatermark(community string, wm watermark, at time.Time) (watermark, error) {
	wm.FetchedAt = at.Unix()
	if err := st.writeWatermark(community, wm); err != nil {
		return wm, err
	}
	return wm, nil
}

// writeWatermark writes the watermark write-then-rename, so a reader never
// sees a half-written one.
func (st *store) writeWatermark(community string, wm watermark) error {
	dir := st.dir(community)
	if dir == "" {
		return fmt.Errorf("no cache directory configured")
	}
	data, err := json.Marshal(wm)
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, watermarkFileName), data)
}

// builder writes one whole index. Nothing it produces is visible to a
// reader until commit renames it into place; abort leaves the previous
// index exactly as it was.
//
// BOTH files stream. The rows could have been accumulated and marshalled at
// the end - it reads more simply - but that would put the whole searchable
// projection, plus the buffer it encodes into, on the heap at once, which
// is the thing this design exists to avoid. Written a row at a time, a
// build's peak allocation does not grow with the size of the community.
type builder struct {
	dir      string
	packages *os.File
	index    *os.File
	pbuf     *bufio.Writer
	ibuf     *bufio.Writer
	offset   int64
	count    int
	closed   bool
	// digest is fed exactly the record bytes packages.jsonl receives, and
	// its sum is the build's generation. A HASH rather than a random id so
	// that two builds of the same document still produce byte-identical
	// files - idempotence is a property this index is tested for, and a
	// random stamp would have thrown it away.
	digest hash.Hash
}

func newBuilder(dir string) (*builder, error) {
	if dir == "" {
		return nil, fmt.Errorf("no cache directory configured")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating %s: %w", dir, err)
	}
	packages, err := os.CreateTemp(dir, ".packages-*")
	if err != nil {
		return nil, fmt.Errorf("creating a staging file in %s: %w", dir, err)
	}
	index, err := os.CreateTemp(dir, ".index-*")
	if err != nil {
		_ = packages.Close()
		_ = os.Remove(packages.Name())
		return nil, fmt.Errorf("creating a staging file in %s: %w", dir, err)
	}
	b := &builder{
		dir:      dir,
		packages: packages,
		index:    index,
		pbuf:     bufio.NewWriterSize(packages, 256*1024),
		ibuf:     bufio.NewWriterSize(index, 64*1024),
		digest:   sha256.New(),
	}
	if _, err := b.ibuf.WriteString(fmt.Sprintf(`{"schema":%d,"rows":[`, indexSchema)); err != nil {
		b.abort()
		return nil, fmt.Errorf("writing %s: %w", indexFileName, err)
	}
	return b, nil
}

// add appends one package's record to packages.jsonl and its row to
// index.json, recording in the row exactly where the record landed.
func (b *builder) add(rec packageRecord, row indexRow) error {
	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("encoding %s: %w", rec.FullName, err)
	}
	if _, err := b.pbuf.Write(line); err != nil {
		return fmt.Errorf("writing %s: %w", rec.FullName, err)
	}
	if err := b.pbuf.WriteByte('\n'); err != nil {
		return fmt.Errorf("writing %s: %w", rec.FullName, err)
	}
	_, _ = b.digest.Write(line) // hash.Hash never returns an error
	row.Offset = b.offset
	row.Length = int64(len(line))
	b.offset += row.Length + 1

	encoded, err := json.Marshal(row)
	if err != nil {
		return fmt.Errorf("encoding the index row for %s: %w", rec.FullName, err)
	}
	if b.count > 0 {
		if err := b.ibuf.WriteByte(','); err != nil {
			return fmt.Errorf("writing %s: %w", indexFileName, err)
		}
	}
	if _, err := b.ibuf.Write(encoded); err != nil {
		return fmt.Errorf("writing %s: %w", indexFileName, err)
	}
	b.count++
	return nil
}

// commit publishes the index. The ORDER is the whole point:
//
//  1. both data files are complete in staging;
//  2. the watermark is REMOVED, which makes the directory read as cold;
//  3. the two data files are renamed into place;
//  4. the watermark is written last.
//
// Step 2 is what step 4's "watermark last" rule actually needs: renaming
// packages.jsonl under an index.json that still addresses the old one would
// otherwise leave a directory that looks valid and reads garbage. Anything
// that stops the process between 2 and 4 leaves a cold index - one wasted
// refresh, never a wrong answer.
func (b *builder) commit(wm watermark) (watermark, error) {
	generation := hex.EncodeToString(b.digest.Sum(nil))
	if _, err := b.pbuf.WriteString(packagesTrailer(generation)); err != nil {
		return wm, fmt.Errorf("writing %s: %w", packagesFileName, err)
	}
	if _, err := b.ibuf.WriteString(fmt.Sprintf(`],"generation":%q}`, generation)); err != nil {
		return wm, fmt.Errorf("writing %s: %w", indexFileName, err)
	}
	if err := b.close(); err != nil {
		return wm, err
	}
	wm.Schema = indexSchema
	wm.Packages = b.count
	wm.Generation = generation

	if err := os.Remove(filepath.Join(b.dir, watermarkFileName)); err != nil && !os.IsNotExist(err) {
		return wm, fmt.Errorf("invalidating %s: %w", watermarkFileName, err)
	}
	if err := os.Rename(b.packages.Name(), filepath.Join(b.dir, packagesFileName)); err != nil {
		return wm, fmt.Errorf("publishing %s: %w", packagesFileName, err)
	}
	if err := os.Rename(b.index.Name(), filepath.Join(b.dir, indexFileName)); err != nil {
		return wm, fmt.Errorf("publishing %s: %w", indexFileName, err)
	}
	if err := writeFileAtomic(filepath.Join(b.dir, watermarkFileName), mustMarshal(wm)); err != nil {
		return wm, fmt.Errorf("publishing %s: %w", watermarkFileName, err)
	}
	return wm, nil
}

// close flushes and closes both staging files.
func (b *builder) close() error {
	if b.closed {
		return nil
	}
	b.closed = true
	for _, f := range []struct {
		name string
		buf  *bufio.Writer
		file *os.File
	}{
		{packagesFileName, b.pbuf, b.packages},
		{indexFileName, b.ibuf, b.index},
	} {
		if err := f.buf.Flush(); err != nil {
			return fmt.Errorf("flushing %s: %w", f.name, err)
		}
		if err := f.file.Close(); err != nil {
			return fmt.Errorf("closing %s: %w", f.name, err)
		}
	}
	return nil
}

// abort discards a build in progress. Every failure path calls it, so a
// cancelled or failed refresh leaves no staging file behind and the
// previous index untouched. Harmless after a successful commit, whose
// renames have already emptied the staging paths.
func (b *builder) abort() {
	_ = b.close()
	_ = os.Remove(b.packages.Name())
	_ = os.Remove(b.index.Name())
}

// stageFile writes data to a new temp file in dir and returns its path,
// leaving the caller to rename it into place.
func stageFile(dir, pattern string, data []byte) (string, error) {
	tmp, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", fmt.Errorf("creating a staging file in %s: %w", dir, err)
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return "", fmt.Errorf("writing %s: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return "", fmt.Errorf("closing %s: %w", name, err)
	}
	return name, nil
}

// writeFileAtomic writes data to path write-then-rename.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	tmp, err := stageFile(dir, ".stage-*", data)
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("publishing %s: %w", path, err)
	}
	return nil
}

// mustMarshal encodes a watermark, whose fields are four scalars and
// therefore cannot fail to encode.
func mustMarshal(wm watermark) []byte {
	data, err := json.Marshal(wm)
	if err != nil {
		panic(fmt.Sprintf("thunderstore: encoding the watermark: %v", err))
	}
	return data
}
