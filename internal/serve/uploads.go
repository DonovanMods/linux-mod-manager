// uploads.go implements the archive upload half of #333's import surface:
// POST /api/v1/uploads streams one mod archive from the browser into the
// service's staging directory and hands back an opaque id, which the
// "import_archive" plan kind then plans over (kind_import_archive.go).
//
// Why an upload at all. `lmm import <archive>` takes a PATH, and the
// process that would resolve it is on the server: a browser cannot hand a
// server a path, and a server must not accept one from a browser (that is
// an arbitrary-file-read with a nice form around it). So the file itself
// travels, once, and everything downstream works with a path this package
// chose.
//
// Three rules make that safe and bounded:
//
//   - The id is OPAQUE and never a path. What the client holds is a random
//     handle into an in-memory map; the staged file's location is chosen
//     here, from core's staging root, and is never derived from anything
//     the client sent. The uploaded filename is kept only as a BASENAME,
//     for the identity the import parses out of it, and is re-validated
//     before it is used.
//   - The body is CAPPED and STREAMED. maxUploadBytes bounds it via
//     http.MaxBytesReader, and the multipart part is copied straight to
//     disk - never buffered in memory, and never through
//     ParseMultipartForm, which would spill into its own temp root.
//   - Uploads EXPIRE, like plans. An abandoned upload is hundreds of
//     megabytes of disk, so the store sweeps on every Put and every lookup,
//     removing the staging directory as well as the entry.
package serve

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"sync"
	"time"
)

// defaultUploadTTL is how long a staged archive survives unused. Longer
// than a plan's ten minutes: an upload is the SLOW half of the flow (the
// user picks a file, it transfers, they read the plan, they decide), and
// re-uploading a 400 MB archive because a confirm dialog sat open is a far
// worse failure than holding it a little longer.
const defaultUploadTTL = 30 * time.Minute

// defaultUploadStoreCap bounds how many staged archives are held at once.
// Deliberately small - each entry is a real file of up to maxUploadBytes -
// and the oldest is evicted (and deleted) when a new upload would exceed
// it, the same policy planStore applies to its own entries.
const defaultUploadStoreCap = 4

// maxUploadBytes caps a single upload's body. Mod archives are routinely
// hundreds of megabytes and occasionally more, so this is deliberately
// generous; what it exists to refuse is the unbounded case - a client, or
// a bug, streaming until the disk fills. It is enforced by
// http.MaxBytesReader on the request body, so the cap applies to the whole
// multipart body, not just the file part inside it.
const maxUploadBytes = 2 << 30 // 2 GiB

// uploadID is the opaque handle a client round-trips instead of a path.
// Random, like planID, and for the same reason: one browser tab must not
// be able to guess (or consume, or delete) another's staged file.
type uploadID string

// stagedUpload is one archive sitting in the staging root, waiting to be
// imported.
type stagedUpload struct {
	// ID is the handle Put issued.
	ID uploadID
	// Filename is the client's own basename, kept because the import
	// parses a mod's identity out of it (core's filename_parser.go). It is
	// a basename with an accepted archive extension and nothing else - see
	// safeUploadName.
	Filename string
	// Size is how many bytes were written.
	Size int64
	// Dir is the staging directory this upload OWNS; removing the upload
	// removes the directory.
	Dir string
	// Path is the staged file itself - the path handed to
	// PlanImportArchive.
	Path string
	// StoredAt is when Put accepted it, by the store's clock.
	StoredAt time.Time
}

// uploadStore is the in-memory, TTL'd index of staged archives. Safe for
// concurrent use; every method takes mu, and every removal deletes the
// entry's directory from disk as well as the entry from the map.
type uploadStore struct {
	ttl time.Duration
	cap int
	// now is the clock seam - time.Now in production, a hand-advanced fake
	// in the TTL tests, exactly as planStore does it.
	now func() time.Time

	mu      sync.Mutex
	uploads map[uploadID]*stagedUpload
}

// newUploadStore builds an empty store whose entries expire ttl after they
// were Put, measured by now, holding at most cap of them at once (a
// non-positive cap takes defaultUploadStoreCap).
func newUploadStore(ttl time.Duration, cap int, now func() time.Time) *uploadStore {
	if cap < 1 {
		cap = defaultUploadStoreCap
	}
	return &uploadStore{ttl: ttl, cap: cap, now: now, uploads: map[uploadID]*stagedUpload{}}
}

// Put stores u under a fresh id, which it also sets on u, and returns it.
// It sweeps expired entries first and then evicts the oldest survivors
// until there is room - both of which delete real files, which is the whole
// point: this store is the only thing that ever cleans the staging
// directories it created.
func (s *uploadStore) Put(u *stagedUpload) uploadID {
	now := s.now()
	u.ID = uploadID(newRandomHandle())
	u.StoredAt = now

	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now)
	s.evictOldestLocked()
	s.uploads[u.ID] = u
	return u.ID
}

// Get returns the upload stored under id WITHOUT consuming it - unlike a
// plan, a staged archive is used more than once (a plan is computed over
// it, then an apply reads it, and a failed apply keeps it for the retry).
// An expired entry answers false and is removed, files and all, so a
// lookup is also a sweep.
func (s *uploadStore) Get(id uploadID) (*stagedUpload, bool) {
	now := s.now()

	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now)

	u, ok := s.uploads[id]
	return u, ok
}

// Remove deletes the entry under id and its staging directory, reporting
// whether there was one. It is what DELETE /api/v1/uploads/{id} calls, and
// what a successful import calls once the archive has been ingested.
func (s *uploadStore) Remove(id uploadID) bool {
	s.mu.Lock()
	u, ok := s.uploads[id]
	delete(s.uploads, id)
	s.mu.Unlock()

	if !ok {
		return false
	}
	removeStagingDir(u)
	return true
}

// PurgeAll removes every entry and its staging directory. Called once the
// server has finished draining, so an orderly shutdown leaves nothing
// behind; a hard kill still leaves the staged files, exactly as it does for
// an interrupted download.
func (s *uploadStore) PurgeAll() {
	s.mu.Lock()
	staged := make([]*stagedUpload, 0, len(s.uploads))
	for id, u := range s.uploads {
		staged = append(staged, u)
		delete(s.uploads, id)
	}
	s.mu.Unlock()

	for _, u := range staged {
		removeStagingDir(u)
	}
}

// len reports how many live entries the store holds. Test-facing.
func (s *uploadStore) len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.uploads)
}

// evictOldestLocked drops (and deletes) the oldest-stored entries until the
// store holds fewer than cap, leaving room for the one Put is about to
// insert. The caller must hold mu.
func (s *uploadStore) evictOldestLocked() {
	for len(s.uploads) >= s.cap {
		var oldestID uploadID
		var oldest *stagedUpload
		for id, u := range s.uploads {
			if oldest == nil || u.StoredAt.Before(oldest.StoredAt) {
				oldestID, oldest = id, u
			}
		}
		delete(s.uploads, oldestID)
		removeStagingDir(oldest)
	}
}

// sweepLocked drops (and deletes) every expired entry. The caller must hold mu.
func (s *uploadStore) sweepLocked(now time.Time) {
	for id, u := range s.uploads {
		if !now.Before(u.StoredAt.Add(s.ttl)) {
			delete(s.uploads, id)
			removeStagingDir(u)
		}
	}
}

// removeStagingDir deletes an upload's staging directory. A failure is not
// actionable - the entry is already gone from the index, so nothing will
// try to use the file again - but it must not be silent either, since what
// is left behind is a large file; the caller logs nothing here because the
// store has no logger, so the failure is folded into the next start's
// staging directory being non-empty. Deliberately tolerant of an
// already-removed directory.
func removeStagingDir(u *stagedUpload) {
	if u == nil || u.Dir == "" {
		return
	}
	_ = os.RemoveAll(u.Dir) //nolint:errcheck // best-effort cleanup of a directory nothing references any more
}

// newRandomHandle returns a fresh unguessable hex handle from crypto/rand
// (GO.md: security-sensitive randomness never comes from math/rand).
func newRandomHandle() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// The OS entropy source being broken is not a recoverable
		// request-time condition; fail the same way newPlanID does.
		panic(fmt.Errorf("serve: generating upload id: %w", err))
	}
	return hex.EncodeToString(b)
}
