// Package core: snapshot.go is `lmm snapshot create|list|delete` (#350) -
// the named point a restore can return a game to.
//
// A snapshot is METADATA. The mod bytes are already in the cache, and the
// files lmm replaced are already in the originals store
// (internal/core/originals.go), so what a snapshot records is the
// arrangement: the profile document (desired state - portable, and it
// carries lock state, since a lock lives on the profile ref), the installed
// rows verbatim (versions, update policies, convert_paks, enabled and
// deployed flags), the deployed-files manifest with a checksum per path,
// and the originals in force at that moment.
//
// One file per snapshot, at <DataDir>/snapshots/<game-id>/<name>.json,
// beside the originals/ tree those rows point into.
//
// The deployed-files manifest IS hashed at create time, which makes the
// cost O(deployed bytes) rather than O(1). That is deliberate: a manifest
// of paths and sizes is a guess about what was deployed, and a manifest
// with checksums is a record of it - which is the entire value of a
// snapshot as evidence. `lmm verify`'s full tier already hashes the same
// tree, so this is a cost the product already pays somewhere, and
// cancellation is honoured between files.
package core

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
)

// ErrSnapshotNotFound is returned when no snapshot of that name exists for
// the game. Typed because both frontends branch on it: the CLI prints the
// name and lists what does exist, and `lmm serve` answers 404.
var ErrSnapshotNotFound = errors.New("snapshot not found")

// ErrSnapshotExists is returned by CreateSnapshot when the name is already
// taken. A snapshot is a record of a moment, so overwriting one silently
// would destroy the only copy of an arrangement the user asked lmm to
// remember - delete it explicitly instead.
var ErrSnapshotExists = errors.New("snapshot already exists")

// ErrInvalidSnapshotName is returned for a name that is not usable as a
// single file name - empty, or carrying a path separator or "..". A
// snapshot name becomes a path, so this is the same rule a game id and a
// profile name already follow.
var ErrInvalidSnapshotName = errors.New("invalid snapshot name")

// snapshotFileExt is the extension every snapshot document carries, and
// what ListSnapshots recognises a snapshot by - so the _originals/
// directory sitting beside them is never mistaken for one.
const snapshotFileExt = ".json"

// Snapshot is one recorded point: everything needed to describe the
// arrangement, and nothing that is already recoverable from the cache.
//
// It is the on-disk document AND the wire document (`lmm snapshot show`
// has no CLI form today, but /api/v1 returns these rows), so its json tags
// are a contract.
type Snapshot struct {
	Name    string `json:"name"`
	GameID  string `json:"game_id"`
	Profile string `json:"profile"`
	// CreatedAt is when the snapshot was taken, in UTC.
	CreatedAt time.Time `json:"created_at"`
	// Auto marks a snapshot taken automatically before a deploy, switch or
	// update (config.yaml's auto_snapshot), rather than one the user asked
	// for by name.
	Auto bool `json:"auto,omitempty"`

	// ProfileDocument is the profile's portable export - the DESIRED state
	// a restore converges to, including each ref's pinned version and lock.
	// Named ProfileDocument rather than Profile because the name member
	// above already answers "which profile".
	ProfileDocument *domain.ExportedProfile `json:"profile_document"`

	// Installed is the installed rows verbatim, in GetInstalledMods' order.
	// The profile document carries what the user WANTS; these carry what
	// the database actually held - update policy, convert_paks, the
	// previous version a rollback would use - none of which a profile ref
	// can express.
	Installed []domain.InstalledMod `json:"installed"`

	// DeployedFiles is what was in the game directory, with a checksum
	// per path.
	DeployedFiles []SnapshotFile `json:"deployed_files"`

	// Originals is the originals store's manifest as it stood - the files
	// lmm had replaced by this point, and therefore exactly the set a
	// restore of THIS snapshot puts back.
	Originals []OriginalFile `json:"originals"`
}

// SnapshotFile is one row of a snapshot's deployed-files manifest.
type SnapshotFile struct {
	// RelativePath is the path under the game's mod directory, in slash
	// form.
	RelativePath string `json:"relative_path"`
	// SourceID/ModID name the mod that deployed it, from the
	// deployed_files table.
	SourceID string `json:"source_id"`
	ModID    string `json:"mod_id"`
	// SHA256 is the deployed content's checksum, hex-encoded. Empty when
	// Missing is set, or when the file could not be read.
	SHA256 string `json:"sha256,omitempty"`
	// Size is the deployed content's length in bytes.
	Size int64 `json:"size,omitempty"`
	// Missing marks a tracked path that was NOT on disk when the snapshot
	// was taken - a real state (`lmm verify` reports it as a finding), and
	// one a snapshot must record honestly rather than by omitting the row:
	// "this path was tracked and absent" is different from "this path was
	// never tracked".
	Missing bool `json:"missing,omitempty"`
}

// SnapshotResult is `lmm snapshot create --json`'s document.
type SnapshotResult struct {
	Name      string    `json:"name"`
	GameID    string    `json:"game_id"`
	Profile   string    `json:"profile"`
	CreatedAt time.Time `json:"created_at"`
	Auto      bool      `json:"auto,omitempty"`
	// Path is the snapshot document's own file, so a user can copy it
	// somewhere safe.
	Path string `json:"path"`
	// Mods/DeployedFiles/Originals are the three counts that describe what
	// was recorded, which is what a console line and a card both render.
	Mods          int `json:"mods"`
	DeployedFiles int `json:"deployed_files"`
	Originals     int `json:"originals"`
	// SizeBytes is the snapshot document's own size PLUS the total size of
	// the originals it references. Originals are SHARED between snapshots
	// of the same game (the store keeps one copy of each replaced file), so
	// summing this across a game's snapshots over-counts - it answers
	// "how much would a restore of this one have to put back", not "how
	// much disk do my snapshots use".
	SizeBytes int64 `json:"size_bytes"`
}

// SnapshotInfo is one row of `lmm snapshot list`. Same members as
// SnapshotResult minus Path, which is create's own answer to "where did it
// go" rather than something a listing repeats per row.
type SnapshotInfo struct {
	Name          string    `json:"name"`
	GameID        string    `json:"game_id"`
	Profile       string    `json:"profile"`
	CreatedAt     time.Time `json:"created_at"`
	Auto          bool      `json:"auto,omitempty"`
	Mods          int       `json:"mods"`
	DeployedFiles int       `json:"deployed_files"`
	Originals     int       `json:"originals"`
	SizeBytes     int64     `json:"size_bytes"`
}

// SnapshotListing is `lmm snapshot list --json`'s document.
type SnapshotListing struct {
	GameID string `json:"game_id"`
	// Snapshots is NEWEST FIRST - the order a user reads a backup list in,
	// and the order the web UI's card renders. Ties break on name so the
	// order is total.
	Snapshots []SnapshotInfo `json:"snapshots"`
	// Warnings names a snapshot file that could not be read or parsed. A
	// listing must not fail because one file is corrupt - the others are
	// still restorable, and the broken one is what the user needs told
	// about.
	Warnings []string `json:"warnings,omitempty"`
}

// SnapshotDeleteResult is `lmm snapshot delete --json`'s document.
type SnapshotDeleteResult struct {
	Name   string `json:"name"`
	GameID string `json:"game_id"`
	// Deleted is always true on a successful call - present so the
	// document says something rather than being two echoed inputs, and so
	// a future "nothing to delete" answer has somewhere to live.
	Deleted bool `json:"deleted"`
}

// reservedSnapshotNames are names no snapshot may take because lmm's own
// files in the same directory answer to them (review finding 1).
//
// "_originals" is the store's directory and is ALREADY unreachable - a
// validated name cannot start with "_" - so it is listed for the sake of
// the message a user gets rather than for safety. "originals" is where the
// manifest lived before finding 1 moved it; a name that once destroyed the
// only copy of a user's stock files should stay refused rather than become
// quietly available again.
var reservedSnapshotNames = map[string]bool{
	originalsStoreDirName: true,
	"originals":           true,
}

// validSnapshotName refuses a name that cannot be a single file name, or
// that names something lmm owns, and returns the cleaned value otherwise.
//
// The character set is an ALLOW-list - letters, digits, ".", "_" and "-" -
// rather than a list of the separators that are known to be dangerous: a
// snapshot name becomes a path segment, a URL segment (DELETE
// /api/v1/snapshots/{name}) and a shell argument, and "everything except
// what I thought of" is the wrong default for all three. A leading "." (a
// dotfile, and the "." / ".." traversal pair) and a leading "_" (lmm's own
// namespace) are refused on top of that.
func validSnapshotName(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", fmt.Errorf("%w: a name is required", ErrInvalidSnapshotName)
	}
	for _, r := range trimmed {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.', r == '_', r == '-':
		default:
			return "", fmt.Errorf("%w: %q may use only letters, digits, %q, %q and %q", ErrInvalidSnapshotName, trimmed, ".", "_", "-")
		}
	}
	if strings.HasPrefix(trimmed, ".") || strings.HasPrefix(trimmed, "_") {
		return "", fmt.Errorf("%w: %q must not start with %q or %q", ErrInvalidSnapshotName, trimmed, ".", "_")
	}
	if reservedSnapshotNames[strings.ToLower(trimmed)] {
		return "", fmt.Errorf("%w: %q is reserved for lmm's own store of the files it replaced", ErrInvalidSnapshotName, trimmed)
	}
	return trimmed, nil
}

// DefaultSnapshotName is the name a snapshot gets when the user did not
// choose one - `lmm snapshot create` with no --name, and the web UI's
// "Snapshot now" button, which has no text input at all.
//
// Exported so BOTH frontends produce the same name for the same gesture:
// the format is a fact about snapshots (it sorts as a timeline, beside the
// automatic ones), not a detail of either frontend, and two copies of a
// time format string is exactly the kind of thing that drifts.
func DefaultSnapshotName(at time.Time) string {
	return at.UTC().Format("20060102-150405")
}

// AutoSnapshotName is the name an automatic snapshot is given: the op that
// triggered it plus a sortable UTC stamp, so the listing reads as a
// timeline and two ops in the same second cannot collide with a name the
// user chose (no user name starts with "auto-").
func AutoSnapshotName(op Op, at time.Time) string {
	return fmt.Sprintf("auto-%s-%s", op, at.UTC().Format("20060102-150405"))
}

// snapshotPath is where a named snapshot's document lives.
func (s *Service) snapshotPath(gameID, name string) string {
	return filepath.Join(snapshotsDirFor(s.dataDir, gameID), name+snapshotFileExt)
}

// CreateSnapshot records the current arrangement of game/profileName under
// name, under the Service's mutation slot.
//
// It is a MUTATION rather than a query even though it changes nothing a
// user can see: it reads the installed rows, the profile and the whole
// deployed tree, and a snapshot taken while a deploy is running would
// describe neither the before nor the after. The slot is what makes it a
// coherent point in time.
//
// An existing name is refused with ErrSnapshotExists rather than
// overwritten.
func (s *Service) CreateSnapshot(ctx context.Context, game *domain.Game, profileName, name string) (*SnapshotResult, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	return s.createSnapshot(ctx, game, profileName, name, false)
}

// createSnapshot is CreateSnapshot without the mutation slot, for the
// auto-snapshot callers that are already inside one (deploy, switch,
// update) and for a restore's own pre-restore safety copy. auto marks the
// document.
func (s *Service) createSnapshot(ctx context.Context, game *domain.Game, profileName, name string, auto bool) (*SnapshotResult, error) {
	if s.dataDir == "" {
		return nil, errors.New("snapshots need a data directory; this Service was constructed without one")
	}
	clean, err := validSnapshotName(name)
	if err != nil {
		return nil, err
	}
	path := s.snapshotPath(game.ID, clean)
	if _, err := os.Stat(path); err == nil {
		return nil, fmt.Errorf("%w: %s (delete it first, or choose another name)", ErrSnapshotExists, clean)
	}

	doc, err := s.buildSnapshot(ctx, game, profileName, clean, auto)
	if err != nil {
		return nil, err
	}

	data, err := json.Marshal(doc, json.Deterministic(true), jsontext.WithIndent("  "))
	if err != nil {
		return nil, fmt.Errorf("encoding the snapshot: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("creating the snapshot directory: %w", err)
	}
	// Written under O_EXCL, so two concurrent creates of the same name
	// cannot both believe they won - the Stat above is the friendly
	// message, this is the guarantee.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return nil, fmt.Errorf("%w: %s", ErrSnapshotExists, clean)
		}
		return nil, fmt.Errorf("creating the snapshot %s: %w", path, err)
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("writing the snapshot %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return nil, fmt.Errorf("closing the snapshot %s: %w", path, err)
	}

	result := snapshotResultOf(doc, path)
	if info, err := os.Stat(path); err == nil {
		result.SizeBytes += info.Size()
	}
	s.logger().Info("recorded a snapshot", "game", game.ID, "profile", profileName, "name", clean, "path", path)
	return result, nil
}

// buildSnapshot assembles the document without writing it.
func (s *Service) buildSnapshot(ctx context.Context, game *domain.Game, profileName, name string, auto bool) (*Snapshot, error) {
	profile, err := config.LoadProfile(s.configDir, game.ID, profileName)
	if err != nil {
		return nil, fmt.Errorf("loading profile %s: %w", profileName, err)
	}
	installed, err := s.GetInstalledMods(ctx, game.ID, profileName)
	if err != nil {
		return nil, fmt.Errorf("loading installed mods: %w", err)
	}
	deployed, err := s.snapshotDeployedFiles(ctx, game, profileName)
	if err != nil {
		return nil, err
	}
	var originals []OriginalFile
	if store := s.originalsStoreFor(game.ID); store != nil {
		if originals, err = store.list(); err != nil {
			return nil, err
		}
	}
	return &Snapshot{
		Name:    name,
		GameID:  game.ID,
		Profile: profileName,
		// Truncated to the second: a snapshot is a moment at the
		// resolution every renderer shows, and sub-second digits make the
		// document's own byte length vary run to run - which would make
		// size_bytes (and therefore a golden) unstable for no gain.
		CreatedAt:       time.Now().UTC().Truncate(time.Second),
		Auto:            auto,
		ProfileDocument: config.ExportProfileValue(profile),
		Installed:       installed,
		DeployedFiles:   deployed,
		Originals:       originals,
	}, nil
}

// snapshotDeployedFiles reads the deployed_files rows and hashes each path
// that is actually on disk. A tracked path that is absent is recorded as
// Missing rather than dropped; a path that exists but cannot be read is
// recorded with no checksum, because "it was there and unreadable" is
// still more than the row alone says.
//
// Cancellation is checked between files, the convention every per-item loop
// in this package follows.
func (s *Service) snapshotDeployedFiles(ctx context.Context, game *domain.Game, profileName string) ([]SnapshotFile, error) {
	rows, err := s.db.ListDeployedFiles(ctx, game.ID, profileName)
	if err != nil {
		return nil, fmt.Errorf("loading deployed files: %w", err)
	}
	out := make([]SnapshotFile, 0, len(rows))
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		file := SnapshotFile{
			RelativePath: filepath.ToSlash(row.RelativePath),
			SourceID:     row.SourceID,
			ModID:        row.ModID,
		}
		abs := filepath.Join(game.ModPath, filepath.FromSlash(row.RelativePath))
		if _, statErr := os.Stat(abs); statErr != nil {
			file.Missing = true
			out = append(out, file)
			continue
		}
		if sum, size, hashErr := hashFile(abs); hashErr == nil {
			file.SHA256, file.Size = sum, size
		} else {
			s.logger().Debug("could not hash a deployed file for the snapshot", "path", abs, "err", hashErr)
		}
		out = append(out, file)
	}
	return out, nil
}

// snapshotResultOf derives create's document from the snapshot it wrote.
// SizeBytes starts as the referenced originals' total; the caller adds the
// document's own file size.
func snapshotResultOf(doc *Snapshot, path string) *SnapshotResult {
	var originalBytes int64
	for _, o := range doc.Originals {
		originalBytes += o.Size
	}
	return &SnapshotResult{
		Name: doc.Name, GameID: doc.GameID, Profile: doc.Profile,
		CreatedAt: doc.CreatedAt, Auto: doc.Auto, Path: path,
		Mods: len(doc.Installed), DeployedFiles: len(doc.DeployedFiles),
		Originals: len(doc.Originals), SizeBytes: originalBytes,
	}
}

// LoadSnapshot reads one snapshot document.
//
// Exported because it is the read behind `snapshot restore`'s preview and
// behind a frontend showing what a snapshot contains, and because
// PlanSnapshotRestore is not the only reason to want it (Phase 3 Ruling
// 10: a serve-facing query needs the same lookup the CLI uses).
func (s *Service) LoadSnapshot(ctx context.Context, gameID, name string) (*Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	clean, err := validSnapshotName(name)
	if err != nil {
		return nil, err
	}
	path := s.snapshotPath(gameID, clean)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrSnapshotNotFound, clean)
		}
		return nil, fmt.Errorf("reading the snapshot %s: %w", path, err)
	}
	var doc Snapshot
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parsing the snapshot %s: %w", path, err)
	}
	return &doc, nil
}

// ListSnapshots returns gameID's snapshots, newest first. A game with no
// snapshot directory returns an empty listing, not an error.
//
// A file that cannot be read or parsed becomes a Warnings entry rather
// than failing the call: the other snapshots are still usable, and the
// broken one is precisely what the user needs to be told about.
func (s *Service) ListSnapshots(ctx context.Context, gameID string) (*SnapshotListing, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	listing := &SnapshotListing{GameID: gameID, Snapshots: []SnapshotInfo{}}
	dir := snapshotsDirFor(s.dataDir, gameID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return listing, nil
		}
		return nil, fmt.Errorf("reading the snapshot directory %s: %w", dir, err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), snapshotFileExt) {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), snapshotFileExt)
		// The originals store is a DIRECTORY (_originals/), skipped
		// above, so a listing needs no name-based exclusion any more
		// (review finding 1). A stray file that is not a snapshot
		// document still becomes a warning rather than an error.
		doc, err := s.LoadSnapshot(ctx, gameID, name)
		if err != nil {
			listing.Warnings = append(listing.Warnings, fmt.Sprintf("%s could not be read: %v", entry.Name(), err))
			continue
		}
		info := snapshotResultOf(doc, "")
		row := SnapshotInfo{
			Name: doc.Name, GameID: doc.GameID, Profile: doc.Profile,
			CreatedAt: doc.CreatedAt, Auto: doc.Auto,
			Mods: info.Mods, DeployedFiles: info.DeployedFiles,
			Originals: info.Originals, SizeBytes: info.SizeBytes,
		}
		if fi, statErr := entry.Info(); statErr == nil {
			row.SizeBytes += fi.Size()
		}
		listing.Snapshots = append(listing.Snapshots, row)
	}

	sort.Slice(listing.Snapshots, func(i, j int) bool {
		a, b := listing.Snapshots[i], listing.Snapshots[j]
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.After(b.CreatedAt)
		}
		return a.Name < b.Name
	})
	return listing, nil
}

// DeleteSnapshot removes one snapshot document, under the mutation slot.
//
// The ORIGINALS it referenced are deliberately left alone. They are shared
// between every snapshot of the game and, more importantly, they are the
// only copy of files lmm replaced - deleting a snapshot is "I no longer
// need this arrangement recorded", never "throw away the stock content".
// Nothing else can put those bytes back, so nothing removes them but the
// user, by hand.
func (s *Service) DeleteSnapshot(ctx context.Context, gameID, name string) (*SnapshotDeleteResult, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	clean, err := validSnapshotName(name)
	if err != nil {
		return nil, err
	}
	path := s.snapshotPath(gameID, clean)
	// A snapshot document is a REGULAR FILE. Nothing else in the directory
	// is deletable through this command - the originals store is a
	// directory there, and a validated name cannot name it anyway (review
	// finding 1) - so refuse rather than remove whatever happens to sit at
	// the path.
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrSnapshotNotFound, clean)
		}
		return nil, fmt.Errorf("reading the snapshot %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s is not a snapshot document", ErrSnapshotNotFound, clean)
	}
	if err := os.Remove(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrSnapshotNotFound, clean)
		}
		return nil, fmt.Errorf("deleting the snapshot %s: %w", path, err)
	}
	return &SnapshotDeleteResult{Name: clean, GameID: gameID, Deleted: true}, nil
}

// autoSnapshot records a pre-operation snapshot when config.yaml's
// auto_snapshot is on, and reports the diagnostic (if any) rather than
// deciding what to do about it.
//
// It is called from INSIDE a flow's mutation slot, so it uses the
// unexported createSnapshot. The returned string pair is (name, warning):
// exactly one is non-empty, and a caller records the warning on its own
// result rather than failing - the operation the user asked for must not be
// blocked by the backup taken to protect it.
//
// Off is the common case and costs one bool read: nothing is stat-ed,
// hashed or written for a user who has not opted in.
func (s *Service) autoSnapshot(ctx context.Context, game *domain.Game, profileName string, op Op) (name, warning string) {
	if s.config == nil || !s.config.AutoSnapshot {
		return "", ""
	}
	result, err := s.createSnapshot(ctx, game, profileName, AutoSnapshotName(op, time.Now()), true)
	if err != nil {
		return "", fmt.Sprintf("could not record an automatic snapshot before this %s: %v", op, err)
	}
	return result.Name, ""
}
