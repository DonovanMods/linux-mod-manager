package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// The prefix originals (#616): the list files a game had in its Wine prefix
// before lmm first wrote them, kept so `lmm purge` can put them back.
//
// They cannot live in the originals store's own manifest (#350): its
// OriginalRoot has two values, mod_path and install_path, and an older
// binary's UnmarshalText refuses any other - so a third root would make an
// older lmm fail to read the whole manifest, which breaks purge after a
// downgrade (design §5, §11 Q3). They get a manifest of their own beside
// it, <DataDir>/snapshots/<game>/_originals/originals-prefix.json, which no
// older binary opens, and their bytes under _originals/prefix/, outside the
// files/ tree the originals store reads.
//
// First original wins, as in the originals store: once a file is
// recorded, later writes keep the record, because the file there is then
// lmm's own.

// prefixOriginalsFile is the manifest's name in the originals directory.
const prefixOriginalsFile = "originals-prefix.json"

// prefixOriginal is one manifest row: a list file in a prefix, what it held
// before lmm's first write, and whether it existed at all.
type prefixOriginal struct {
	// Prefix and User are the prefix and Windows user directory the file
	// was in.
	Prefix string `json:"prefix"`
	User   string `json:"user"`
	// Path is the file's path under the user directory, as found on disk
	// ("AppData/Local/Skyrim Special Edition/Plugins.txt").
	Path string `json:"path"`
	// Existed is false for a file lmm created: putting it back removes it.
	Existed bool `json:"existed"`
	// SHA256, Size and Mode describe the stored bytes, for a file that
	// existed.
	SHA256 string `json:"sha256,omitempty"`
	Size   int64  `json:"size,omitzero"`
	Mode   uint32 `json:"mode,omitzero"`
	// Stored is the stored copy's file name under _originals/prefix/.
	Stored string `json:"stored,omitempty"`
	// Owned names the plugins lmm deployed that its last write listed in
	// the file: what a purge takes out again, keeping everything else the
	// file holds by then (#616 review B2).
	Owned      []string  `json:"owned,omitempty"`
	CapturedAt time.Time `json:"captured_at"`
}

// key is the row's identity: the same file, however its name is cased.
func (o prefixOriginal) key() string {
	return o.Prefix + "\x00" + o.User + "\x00" + strings.ToLower(o.Path)
}

// prefixOriginalsManifest is the manifest document.
type prefixOriginalsManifest struct {
	Files []prefixOriginal `json:"files"`
}

// prefixOriginals is one game's prefix originals. It holds no lock: every
// caller runs inside a mutation (beginOp), which serializes them, and a new
// value is made per call, so a lock here would guard nothing (#616 review
// 11).
type prefixOriginals struct {
	dir string // <DataDir>/snapshots/<game>/_originals
}

// prefixOriginalsFor is gameID's store, or nil without a data directory.
func (s *Service) prefixOriginalsFor(gameID string) *prefixOriginals {
	if s == nil || s.dataDir == "" {
		return nil
	}
	return &prefixOriginals{dir: filepath.Join(snapshotsDirFor(s.dataDir, gameID), originalsStoreDirName)}
}

func (p *prefixOriginals) manifestPath() string { return filepath.Join(p.dir, prefixOriginalsFile) }

func (p *prefixOriginals) read() (prefixOriginalsManifest, error) {
	var m prefixOriginalsManifest
	data, err := os.ReadFile(p.manifestPath())
	if errors.Is(err, fs.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return m, fmt.Errorf("reading %s: %w", p.manifestPath(), err)
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, fmt.Errorf("parsing %s: %w", p.manifestPath(), err)
	}
	return m, nil
}

func (p *prefixOriginals) write(m prefixOriginalsManifest) error {
	if err := os.MkdirAll(p.dir, 0o700); err != nil {
		return err
	}
	if len(m.Files) == 0 {
		if err := os.Remove(p.manifestPath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	data, err := json.Marshal(m, json.Deterministic(true), jsontext.WithIndent("  "))
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(p.dir, "originals-prefix-*.json")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) //nolint:errcheck // a successful rename removes it already
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	// Flushed before the rename, so a crash leaves the old manifest or the
	// new one, never an empty one (#616 review 7).
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, p.manifestPath())
}

// list is every row, oldest first.
func (p *prefixOriginals) list() ([]prefixOriginal, error) {
	m, err := p.read()
	return m.Files, err
}

// capture records row - the file before lmm's write - unless one is
// recorded already. data is its content when row.Existed.
func (p *prefixOriginals) capture(row prefixOriginal, data []byte) error {
	m, err := p.read()
	if err != nil {
		return err
	}
	for _, have := range m.Files {
		if have.key() == row.key() {
			return nil
		}
	}
	row.CapturedAt = time.Now().UTC()
	if row.Existed {
		sum := sha256.Sum256(data)
		row.SHA256 = hex.EncodeToString(sum[:])
		row.Size = int64(len(data))
		row.Stored = fmt.Sprintf("%s-%d", row.SHA256[:16], row.CapturedAt.UnixNano())
		store := filepath.Join(p.dir, "prefix")
		if err := os.MkdirAll(store, 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(store, row.Stored), data, 0o600); err != nil {
			return err
		}
	}
	m.Files = append(m.Files, row)
	return p.write(m)
}

// stored reads row's stored bytes, refusing a copy whose checksum no longer
// matches.
func (p *prefixOriginals) stored(row prefixOriginal) ([]byte, error) {
	if !row.Existed {
		return nil, nil
	}
	if row.Stored == "" || strings.ContainsAny(row.Stored, `/\`) || strings.HasPrefix(row.Stored, ".") {
		return nil, fmt.Errorf("the prefix originals manifest names no usable copy of %s", row.Path)
	}
	data, err := os.ReadFile(filepath.Join(p.dir, "prefix", row.Stored))
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != row.SHA256 {
		return nil, fmt.Errorf("the stored copy of %s does not match its checksum, so lmm will not put it back", row.Path)
	}
	return data, nil
}

// forget drops row and its stored copy.
func (p *prefixOriginals) forget(row prefixOriginal) error {
	m, err := p.read()
	if err != nil {
		return err
	}
	kept := m.Files[:0]
	for _, have := range m.Files {
		if have.key() != row.key() {
			kept = append(kept, have)
		}
	}
	m.Files = kept
	if err := p.write(m); err != nil {
		return err
	}
	if row.Stored != "" && !strings.ContainsAny(row.Stored, `/\`) {
		if err := os.Remove(filepath.Join(p.dir, "prefix", row.Stored)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// setOwned records owned on the row for prefix/user/path, if lmm keeps one
// (it has replaced or created that file), writing the manifest only when
// the set changed.
func (p *prefixOriginals) setOwned(prefix, user, path string, owned []string) error {
	m, err := p.read()
	if err != nil {
		return err
	}
	key := prefixOriginal{Prefix: prefix, User: user, Path: path}.key()
	for i := range m.Files {
		if m.Files[i].key() != key {
			continue
		}
		if slices.Equal(m.Files[i].Owned, owned) {
			return nil
		}
		m.Files[i].Owned = slices.Clone(owned)
		return p.write(m)
	}
	return nil
}

// rekey moves row to the prefix and user of moved (rekeyPrefixOriginal).
func (p *prefixOriginals) rekey(row, moved prefixOriginal) error {
	m, err := p.read()
	if err != nil {
		return err
	}
	for i := range m.Files {
		if m.Files[i].key() == row.key() {
			m.Files[i].Prefix, m.Files[i].User = moved.Prefix, moved.User
			return p.write(m)
		}
	}
	return nil
}
