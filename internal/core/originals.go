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
// LAYOUT. The whole store lives under <DataDir>/snapshots/<game-id>/
// _originals/ - manifest.json (the manifest) beside files/ (the stored
// bytes). The leading underscore is load-bearing: snapshot documents are
// <DataDir>/snapshots/<game-id>/<name>.json, and validSnapshotName refuses
// a leading "_", so NO snapshot name can ever name a file in the store.
// (Before review finding 1 the manifest was originals.json in that same
// directory, which `snapshot create --name originals` overwrote and
// `snapshot delete originals` removed. Nothing released has written either
// path, so there is no migration.) The stored tree is split by ROOT -
// files/mod_path/... and files/install_path/... - because the two call
// sites above resolve their relative paths against DIFFERENT directories
// (game.ModPath and game.InstallPath), and a single flat tree would
// silently conflate "Data/a.esp" under one with "Data/a.esp" under the
// other.
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
	// Mode is the original's PERMISSION bits (info.Mode().Perm()), so an
	// executable file lmm replaced comes back executable (review finding
	// 6). Additive and omitzero: a row written before this build carries
	// none, and a restore falls back to 0644 for it. The type is uint32
	// because a wire document should not encode Go's fs.FileMode bit
	// layout.
	Mode uint32 `json:"mode,omitzero"`
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

	// warn is ServiceConfig.WarnWriter: the always-on user-facing channel
	// (review finding 5). A capture failure is the moment lmm is about to
	// overwrite an irreplaceable file and could not preserve it, and the
	// CLI's default --log-level is "off" - so the diagnostic logger alone
	// meant no output at all until the restore that could not put the file
	// back. nil is silent, which is what the white-box tests get.
	warn io.Writer
	// failures collects one line per capture that could not be taken since
	// the last drain, so the flow that is running can put them on its
	// result's Warnings as well (takeFailures).
	failures []string
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
// directory. A nil logger discards; a nil warn writer is silent.
func newOriginalsStore(dataDir, gameID string, log *slog.Logger, warn io.Writer) *originalsStore {
	if log == nil {
		log = discardLogger
	}
	return &originalsStore{dir: snapshotsDirFor(dataDir, gameID), log: log, warn: warn}
}

// noteFailure records a capture that could not be taken: onto the always-on
// user channel immediately, and onto the pending list for whichever flow
// drains it next (review finding 5).
//
// Non-fatal, still - a backup that blocks the operation it exists to
// protect is worse than no backup - but never silent.
func (s *originalsStore) noteFailure(msg string) {
	s.mu.Lock()
	s.failures = append(s.failures, msg)
	s.mu.Unlock()
	if s.warn != nil {
		fmt.Fprintf(s.warn, "warning: %s\n", msg) //nolint:errcheck // best effort; a warning that cannot be printed is not worth failing a deploy for
	}
}

// takeFailures drains the pending capture failures, so a flow can put them
// on its own result's Warnings.
func (s *originalsStore) takeFailures() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.failures
	s.failures = nil
	return out
}

// originalsStoreDirName is the store's own subdirectory of a game's
// snapshot directory. It starts with "_" because validSnapshotName refuses
// a leading underscore, which is what makes the store unreachable from the
// snapshot-name namespace (review finding 1).
const originalsStoreDirName = "_originals"

// storeDir is the store's own directory, <DataDir>/snapshots/<game>/_originals.
func (s *originalsStore) storeDir() string {
	return filepath.Join(s.dir, originalsStoreDirName)
}

// manifestPath is where the manifest lives.
func (s *originalsStore) manifestPath() string {
	return filepath.Join(s.storeDir(), "manifest.json")
}

// storedPath is where a captured original's bytes live.
func (s *originalsStore) storedPath(root OriginalRoot, relPath string) string {
	return filepath.Join(s.storeDir(), "files", string(root), filepath.FromSlash(relPath))
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
	if err := os.MkdirAll(s.storeDir(), 0700); err != nil {
		return fmt.Errorf("creating the originals store directory %s: %w", s.storeDir(), err)
	}
	data, err := json.Marshal(m, json.Deterministic(true), jsontext.WithIndent("  "))
	if err != nil {
		return fmt.Errorf("encoding the originals manifest: %w", err)
	}
	tmp, err := os.CreateTemp(s.storeDir(), "manifest-*.json")
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
	row.Mode = uint32(info.Mode().Perm())
	if row.CapturedAt.IsZero() {
		// Truncated to the second, for the same reason Snapshot.CreatedAt
		// is: a manifest's byte length should not depend on nanoseconds.
		row.CapturedAt = time.Now().UTC().Truncate(time.Second)
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
	// MEMOISED per game. Two callers in one flow - the Installer's deploy
	// loop and applyProfileOverrides - must share a store, or a capture
	// failure recorded by one would not be drained by the other (review
	// finding 5). It also means "first original wins" is serialized by one
	// mutex per game rather than one per call.
	s.originalsMu.Lock()
	defer s.originalsMu.Unlock()
	if store, ok := s.originalsStores[gameID]; ok {
		return store
	}
	store := newOriginalsStore(s.dataDir, gameID, s.logger(), s.warnWriter)
	if s.originalsStores == nil {
		s.originalsStores = map[string]*originalsStore{}
	}
	s.originalsStores[gameID] = store
	return store
}

// takeCaptureWarnings drains gameID's pending originals failures onto
// warnings and emits one WarningEvent each, so a failed capture is visible
// at DEFAULT verbosity in every frontend (review finding 5). No-op when
// there is no store or nothing failed.
//
// "Capture" is the historical name; the pending list holds failed PUT-BACKS
// too (ruling (a)), which is why the removal flows drain it as well - a
// purge and an uninstall through re-review finding N2.
func (s *Service) takeCaptureWarnings(gameID string, op Op, phase DeployPhase, warnings *[]string, emit func(Event)) {
	store := s.originalsStoreFor(gameID)
	if store == nil {
		return
	}
	for _, msg := range store.takeFailures() {
		*warnings = append(*warnings, msg)
		if emit != nil {
			emit(WarningEvent{Scope: Scope{Op: op}, Phase: phase, Message: msg})
		}
	}
}

// restore writes a stored original back to absPath, verifying the stored
// copy's checksum first.
//
// The verification is the point: a stored original is the only surviving
// copy of stock content, and writing a corrupted copy into a game
// directory would turn "lmm kept your original" into "lmm broke your
// install". A mismatch or a missing stored copy is reported, never written.
func (s *originalsStore) restore(row OriginalFile, absPath string) error {
	// ALREADY back? Then this is done, whatever the store still holds.
	// A restore purges first, and a purge now puts each original back as it
	// removes the file that replaced it (coordinator ruling (a) on the
	// review's note 13), taking the stored copy with it - so by the time
	// the originals stage runs, the file it is there to write is often
	// already exactly right. Reporting that as "the stored copy is
	// missing" would turn a correct restore into a partial one.
	if row.SHA256 != "" {
		if sum, _, err := hashFile(absPath); err == nil && sum == row.SHA256 {
			return applyOriginalMode(absPath, row)
		}
	}

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
	// would have widened); what goes BACK into a game directory is the
	// mode the file HAD - review finding 6: an unconditional 0644 came
	// back non-executable, which for the mod_path == install_path shape
	// this store exists to cover means launcher scripts, wrappers and
	// shipped binaries. 0644 remains the fallback for a row written before
	// the mode was recorded.
	return applyOriginalMode(absPath, row)
}

// applyOriginalMode puts row's recorded permission bits back on absPath,
// falling back to 0644 for a row written before the mode was recorded
// (review finding 6).
func applyOriginalMode(absPath string, row OriginalFile) error {
	mode := fs.FileMode(0644)
	if row.Mode != 0 {
		mode = fs.FileMode(row.Mode).Perm()
	}
	if err := os.Chmod(absPath, mode); err != nil {
		return fmt.Errorf("setting permissions on the restored %s: %w", row.RelativePath, err)
	}
	return nil
}

// release puts back the original recorded for (root, relPath) at absPath,
// for the moment lmm REMOVES the file that replaced it - an uninstall, a
// purge, a convergence, or the rollback of a failed install.
//
// Coordinator ruling on review note 13. "Undo what lmm did" has to include
// the file lmm displaced: without this, an ordinary uninstall left a HOLE
// where stock content used to be, and the only way back was `snapshot
// restore`, which is a whole-state operation nobody wants for one mod.
// `snapshot restore` stays the whole-state path; this is the per-file one.
//
// The manifest row survives until the bytes are actually back in place: a
// restore that fails leaves the row (and the stored copy) exactly where a
// later `snapshot restore` can still find them. Once the original IS back,
// the row goes - lmm no longer holds the only copy, and a later deploy over
// that same file captures it afresh.
//
// Returns whether anything was put back. A row this store has never heard
// of is (false, nil), not an error: most removals are of files that
// replaced nothing.
func (s *originalsStore) release(root OriginalRoot, relPath, absPath string) (bool, error) {
	rel, err := cleanOriginalRelPath(relPath)
	if err != nil {
		return false, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	m, err := s.read()
	if err != nil {
		return false, err
	}
	idx := -1
	for j, existing := range m.Originals {
		if existing.Root == root && existing.RelativePath == rel {
			idx = j
			break
		}
	}
	if idx < 0 {
		return false, nil
	}
	row := m.Originals[idx]
	if err := s.restore(row, absPath); err != nil {
		return false, err
	}

	stored := s.storedPath(row.Root, row.RelativePath)
	m.Originals = append(m.Originals[:idx:idx], m.Originals[idx+1:]...)
	if err := s.write(m); err != nil {
		// The file is back; the row merely outlives it. A later
		// `snapshot restore` would rewrite identical bytes over it, which
		// is harmless - so this is reported, not fatal.
		return true, err
	}
	_ = os.Remove(stored) //nolint:errcheck // the record is what matters; orphaned bytes are inert
	return true, nil
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
