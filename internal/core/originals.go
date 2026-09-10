// Package core: originals.go is the ORIGINALS STORE - the half of #350 that
// has to happen before a snapshot is ever taken.
//
// Everything else lmm does is reconstructible. A deployed mod file comes
// back from the cache; a profile comes back from its YAML; an installed row
// comes back from the database. The ONE thing lmm cannot rebuild is a file
// it REPLACED: stock game content, or a file some other tool put there,
// overwritten by a deploy the user accepted or by a profile override. Once
// those bytes are gone they are gone, and `lmm purge` puts the directory
// back to "no mods", not back to "as shipped".
//
// So before any such write, the original is copied aside and recorded. Two
// call sites reach this, and between them they cover every replacing write
// in the product:
//
//   - Installer.Install/Replace, immediately before linker.Deploy - the
//     path EVERY mod-file deploy funnels through, whether it came from
//     `lmm install`, `lmm deploy`, an accepted Overwrite conflict, an
//     archive import, a profile switch/apply, or a DeployCompile game's
//     merged artifact (which deploys as an ordinary synthetic mod). That
//     is the generalisation #350 asks for, and it needed no per-format
//     code: the rule is "a deploy that replaces a file lmm does not own",
//     which subsumes the compile case wherever it actually arises - a game
//     whose mod_path IS its install directory, the shape #267's BepInEx
//     spike describes. (On Icarus specifically the base pak is only ever
//     READ - ResolveBaseArtifact points into Content/Data while the merged
//     artifact deploys into Content/Paks/mods - so nothing is replaced
//     there at all.)
//   - ApplyProfileOverrides, immediately before it writes an override over
//     a file in the game's install directory.
//
// LAYOUT. <DataDir>/snapshots/<game-id>/ holds originals/ (the stored
// bytes) and originals.json (the manifest). The stored tree is split by
// ROOT - originals/mod_path/... and originals/install_path/... - because
// the two call sites above resolve their relative paths against DIFFERENT
// directories (game.ModPath and game.InstallPath), and a single flat tree
// would silently conflate "Data/a.esp" under one with "Data/a.esp" under
// the other.
//
// FIRST ORIGINAL WINS. Capture is idempotent per (root, relative path): a
// path already in the manifest is left alone. The second write's "original"
// is lmm's OWN first write, so overwriting the stored copy would replace
// the only surviving stock bytes with a mod's.
package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// OriginalRoot names which of a game's two directories an original's
// relative path is relative to. It is a wire value (the manifest, and every
// snapshot document that carries the originals in force), so it is typed
// rather than a bare string: a stray "modpath" cannot compile into a switch.
type OriginalRoot string

const (
	// OriginalRootModPath means the path is relative to game.ModPath - the
	// root every mod-file deploy writes under.
	OriginalRootModPath OriginalRoot = "mod_path"
	// OriginalRootInstallPath means the path is relative to
	// game.InstallPath - the root a profile override writes under.
	OriginalRootInstallPath OriginalRoot = "install_path"
)

// String returns the root's wire name.
func (r OriginalRoot) String() string { return string(r) }

// MarshalText implements encoding.TextMarshaler.
func (r OriginalRoot) MarshalText() ([]byte, error) { return []byte(r), nil }

// UnmarshalText implements encoding.TextUnmarshaler, refusing a root this
// build does not know rather than carrying it forward as a directory name.
func (r *OriginalRoot) UnmarshalText(b []byte) error {
	switch OriginalRoot(b) {
	case OriginalRootModPath, OriginalRootInstallPath:
		*r = OriginalRoot(b)
		return nil
	default:
		return fmt.Errorf("unknown original root %q", b)
	}
}

// Op values recorded on an OriginalFile. They name the KIND of write that
// replaced the file, not the command that drove it: every mod-file deploy
// arrives through one code path whatever flow called it, and the mod
// identity recorded beside it is the more useful provenance anyway.
const (
	// OriginalOpDeploy means a mod's file was deployed over it.
	OriginalOpDeploy = "deploy"
	// OriginalOpProfileOverride means a profile's configuration override
	// was written over it.
	OriginalOpProfileOverride = "profile_override"
)

// OriginalFile is one manifest row: a file lmm replaced, and enough to put
// it back and to say what replaced it.
type OriginalFile struct {
	// Root says which game directory RelativePath is relative to.
	Root OriginalRoot `json:"root"`
	// RelativePath is the file's path under Root, in slash form on the
	// wire so a manifest is portable.
	RelativePath string `json:"relative_path"`
	// SHA256 is the stored copy's checksum, hex-encoded. A restore
	// re-hashes the stored copy and refuses to write one that does not
	// match, rather than putting corrupted bytes into a game directory.
	SHA256 string `json:"sha256"`
	// Size is the original's length in bytes.
	Size int64 `json:"size"`
	// CapturedAt is when the copy was taken.
	CapturedAt time.Time `json:"captured_at"`
	// Op is OriginalOpDeploy or OriginalOpProfileOverride.
	Op string `json:"op"`
	// SourceID/ModID name the mod whose file replaced it, for the deploy
	// op. Empty for a profile override, which belongs to no mod.
	SourceID string `json:"source_id,omitempty"`
	ModID    string `json:"mod_id,omitempty"`
	// Profile is the profile that was active when the write happened.
	Profile string `json:"profile,omitempty"`
}

// originalsManifest is the on-disk manifest document. A named wrapper
// rather than a bare array so the file can grow a sibling member later
// without every existing manifest becoming unreadable.
type originalsManifest struct {
	Originals []OriginalFile `json:"originals"`
}

// originalsStore is one game's originals directory plus its manifest.
//
// Concurrency: the Service serializes mutations through beginOp, so only
// one flow captures at a time in production. The mutex is still here
// because a single flow's deploy loop and its override write are two
// separate callers into the same store, and a manifest is read-modify
// -written - so nothing about the correctness of "first original wins"
// should depend on the caller's serialization.
type originalsStore struct {
	dir string // <DataDir>/snapshots/<game-id>
	log *slog.Logger
	mu  sync.Mutex
}

// snapshotsDirFor returns a game's snapshot directory,
// <DataDir>/snapshots/<game-id> - the one place the layout is spelled, so
// the originals store, `snapshot create` and `snapshot list` cannot
// disagree about where they live.
func snapshotsDirFor(dataDir, gameID string) string {
	return filepath.Join(dataDir, "snapshots", gameID)
}

// newOriginalsStore returns the store for gameID under dataDir. Nothing is
// created here: a game that never has a file replaced never grows a
// directory. A nil logger discards.
func newOriginalsStore(dataDir, gameID string, log *slog.Logger) *originalsStore {
	if log == nil {
		log = discardLogger
	}
	return &originalsStore{dir: snapshotsDirFor(dataDir, gameID), log: log}
}

// manifestPath is where the manifest lives.
func (s *originalsStore) manifestPath() string {
	return filepath.Join(s.dir, "originals.json")
}

// storedPath is where a captured original's bytes live.
func (s *originalsStore) storedPath(root OriginalRoot, relPath string) string {
	return filepath.Join(s.dir, "originals", string(root), filepath.FromSlash(relPath))
}

// list returns the manifest, oldest capture first. A store that has never
// captured anything returns no rows and no error - that is a normal state,
// not a missing file.
func (s *originalsStore) list() ([]OriginalFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.read()
	if err != nil {
		return nil, err
	}
	return m.Originals, nil
}

// read loads the manifest without taking the lock; callers hold it.
func (s *originalsStore) read() (originalsManifest, error) {
	var m originalsManifest
	data, err := os.ReadFile(s.manifestPath())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return m, nil
		}
		return m, fmt.Errorf("reading the originals manifest %s: %w", s.manifestPath(), err)
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, fmt.Errorf("parsing the originals manifest %s: %w", s.manifestPath(), err)
	}
	return m, nil
}

// write replaces the manifest atomically - a temp file in the same
// directory, then a rename - so a crash mid-write cannot leave a manifest
// that names originals whose bytes are not all there, or truncate one that
// was complete.
func (s *originalsStore) write(m originalsManifest) error {
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return fmt.Errorf("creating the snapshot directory %s: %w", s.dir, err)
	}
	data, err := json.Marshal(m, json.Deterministic(true), jsontext.WithIndent("  "))
	if err != nil {
		return fmt.Errorf("encoding the originals manifest: %w", err)
	}
	tmp, err := os.CreateTemp(s.dir, "originals-*.json")
	if err != nil {
		return fmt.Errorf("creating a temporary originals manifest: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name) //nolint:errcheck // best effort; a successful rename removes it already
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing the originals manifest: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing the originals manifest: %w", err)
	}
	if err := os.Rename(name, s.manifestPath()); err != nil {
		return fmt.Errorf("replacing the originals manifest: %w", err)
	}
	return nil
}

// capture copies absPath aside and records it, unless there is nothing to
// capture. It answers nil - not an error - for every "nothing to do" case,
// because a caller about to deploy a file is not in a position to treat
// "the destination is empty" as a failure:
//
//   - the manifest already has this (root, relative path): first original
//     wins, and the stored copy is never replaced;
//   - absPath does not exist: nothing is being replaced;
//   - absPath is not a regular file: a symlink is lmm's own deployment (or
//     another manager's link), and a directory is not a file being
//     overwritten.
//
// A relative path that escapes its root is refused - a manifest is a list
// of things a restore will WRITE, so it must never be able to name
// something outside the game's directories.
func (s *originalsStore) capture(row OriginalFile, absPath string) error {
	rel, err := cleanOriginalRelPath(row.RelativePath)
	if err != nil {
		return err
	}
	row.RelativePath = rel

	info, err := os.Lstat(absPath)
	if err != nil || !info.Mode().IsRegular() {
		return nil //nolint:nilerr // nothing there to preserve; see the doc comment
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	m, err := s.read()
	if err != nil {
		return err
	}
	for _, existing := range m.Originals {
		if existing.Root == row.Root && existing.RelativePath == rel {
			return nil
		}
	}

	dest := s.storedPath(row.Root, rel)
	if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
		return fmt.Errorf("creating the originals directory for %s: %w", rel, err)
	}
	sum, size, err := copyAndHash(absPath, dest)
	if err != nil {
		return err
	}

	row.SHA256, row.Size = sum, size
	if row.CapturedAt.IsZero() {
		row.CapturedAt = time.Now().UTC()
	}
	m.Originals = append(m.Originals, row)
	if err := s.write(m); err != nil {
		// The bytes are on disk but unrecorded, which a later capture
		// would silently treat as "not captured yet" and overwrite. Remove
		// them so the store stays consistent with its manifest.
		_ = os.Remove(dest)
		return err
	}
	s.log.Debug("stored the original of a file a write replaced",
		"root", row.Root, "path", rel, "op", row.Op, "sha256", row.SHA256)
	return nil
}

// cleanOriginalRelPath normalises a game-dir-relative path to slash form
// and refuses anything that is absolute, empty, or escapes its root.
func cleanOriginalRelPath(relPath string) (string, error) {
	cleaned := filepath.Clean(filepath.FromSlash(strings.TrimSpace(relPath)))
	if cleaned == "" || cleaned == "." || filepath.IsAbs(cleaned) {
		return "", fmt.Errorf("original path %q is not a relative path inside the game directory", relPath)
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("original path %q escapes the game directory", relPath)
	}
	return filepath.ToSlash(cleaned), nil
}

// copyAndHash copies src to dst and returns the content's hex SHA-256 and
// its length, hashing the bytes as they are written so the file is read
// once. dst is created 0600: an original may be game content the user's
// own umask would have made group-readable, and nothing else needs it.
func copyAndHash(src, dst string) (sum string, size int64, err error) {
	in, err := os.Open(src)
	if err != nil {
		return "", 0, fmt.Errorf("reading %s: %w", src, err)
	}
	defer in.Close() //nolint:errcheck // read-only

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return "", 0, fmt.Errorf("creating %s: %w", dst, err)
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(out, h), in)
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(dst)
		return "", 0, fmt.Errorf("copying %s to %s: %w", src, dst, copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(dst)
		return "", 0, fmt.Errorf("closing %s: %w", dst, closeErr)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// hashFile returns path's hex SHA-256 and length. Used by `snapshot
// create` for the deployed-files manifest and by a restore to verify a
// stored original before writing it back.
func hashFile(path string) (sum string, size int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, fmt.Errorf("reading %s: %w", path, err)
	}
	defer f.Close() //nolint:errcheck // read-only

	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, fmt.Errorf("hashing %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// originalsStoreFor returns the originals store for game, or nil when this
// Service has no data directory to keep one in (a struct-literal Service in
// a white-box test). A nil store makes every capture a no-op, which is what
// keeps those tests working unchanged.
func (s *Service) originalsStoreFor(gameID string) *originalsStore {
	if s == nil || s.dataDir == "" {
		return nil
	}
	return newOriginalsStore(s.dataDir, gameID, s.logger())
}

// restore writes a stored original back to absPath, verifying the stored
// copy's checksum first.
//
// The verification is the point: a stored original is the only surviving
// copy of stock content, and writing a corrupted copy into a game
// directory would turn "lmm kept your original" into "lmm broke your
// install". A mismatch or a missing stored copy is reported, never written.
func (s *originalsStore) restore(row OriginalFile, absPath string) error {
	stored := s.storedPath(row.Root, row.RelativePath)
	sum, _, err := hashFile(stored)
	if err != nil {
		return fmt.Errorf("reading the stored original of %s: %w", row.RelativePath, err)
	}
	if row.SHA256 != "" && sum != row.SHA256 {
		return fmt.Errorf("the stored original of %s does not match its recorded checksum (%s, expected %s); it is not safe to write back",
			row.RelativePath, sum, row.SHA256)
	}
	if err := os.MkdirAll(filepath.Dir(absPath), 0755); err != nil {
		return fmt.Errorf("creating the directory for %s: %w", row.RelativePath, err)
	}
	// The destination may still hold the mod file that replaced it - a
	// symlink into the cache, which a plain write would follow and thereby
	// CORRUPT THE CACHE ENTRY. Remove it first.
	if err := os.Remove(absPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("clearing %s before restoring the original: %w", absPath, err)
	}
	if _, _, err := copyAndHash(stored, absPath); err != nil {
		return err
	}
	// The stored copy is 0600 (it may be game content the user's umask
	// would have widened); what goes BACK into a game directory should
	// read like an ordinary game file again.
	if err := os.Chmod(absPath, 0644); err != nil {
		return fmt.Errorf("setting permissions on the restored %s: %w", row.RelativePath, err)
	}
	return nil
}

// verify reports whether row's stored copy is present and matches its
// recorded checksum, so a PLAN can say what a restore would do without
// writing anything.
func (s *originalsStore) verify(row OriginalFile) error {
	stored := s.storedPath(row.Root, row.RelativePath)
	sum, _, err := hashFile(stored)
	if err != nil {
		return err
	}
	if row.SHA256 != "" && sum != row.SHA256 {
		return fmt.Errorf("checksum %s does not match the recorded %s", sum, row.SHA256)
	}
	return nil
}
