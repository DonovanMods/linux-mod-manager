// Package core: this file keeps a DOWNLOADED archive across an ingest that
// refused it for a reason the user can fix without re-fetching the bytes
// (#359 review F9).
//
// There is exactly one such refusal today: the BepInEx loader precondition.
// A downloaded archive's SHAPE is not knowable until it is extracted, so
// PlanInstall cannot answer "does this game need a loader declared?" and the
// refusal necessarily lands mid-ingest - after the file is on disk. The
// remedy is one `lmm game edit <game> --loader bepinex` away and the bytes
// are identical afterwards, so discarding them makes the fix cost a second
// download; on a NexusMods free-tier account that is a second MANUAL
// download, through the browser, with a wait.
//
// It is deliberately not a general download cache. Only a refusal the user
// answers by reconfiguring the GAME retains anything: a checksum mismatch, a
// truncated body or a source error means the bytes are suspect, and keeping
// those would turn a transient failure into a permanently poisoned retry.
package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// retainedDownloadsDirName is the staging-root subdirectory holding them.
// Under the staging root rather than the cache because the content has NOT
// been accepted: nothing may list it as an installed file, and the ordinary
// staging privacy (0700) applies.
const retainedDownloadsDirName = "retained"

// retainedDownloadTTL bounds how long an archive nobody came back for
// survives. A user who abandons the install should not find a multi-gigabyte
// file in their data directory a year later; a user who reads the error and
// runs the two commands is back within seconds.
const retainedDownloadTTL = 7 * 24 * time.Hour

// retainedDownloadFile is the sidecar written beside the archive, so a reuse
// reconstructs the DownloadResult the download produced rather than
// re-hashing the file (and rather than skipping the integrity checks that
// result feeds).
const retainedDownloadFile = "download.json"

// retainedDownload is that sidecar's content.
type retainedDownload struct {
	FileName string `json:"file_name"`
	Size     int64  `json:"size"`
	Checksum string `json:"checksum"`
	SHA256   string `json:"sha256"`
}

// retainedDownloadDir names the directory for one (source, mod, file), or ""
// when this service has no data dir to stage under (in which case nothing is
// retained at all - a $TMPDIR fallback would not survive a reboot and is not
// where multi-gigabyte archives belong).
//
// The directory name is a hash rather than the three ids joined: a source id
// is user-chosen and a file id is source-controlled, so neither is safe to
// use as a path component.
func (s *Service) retainedDownloadDir(sourceID, modID, fileID string) string {
	root := s.stagingRoot()
	if root == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(sourceID + "\x00" + modID + "\x00" + fileID))
	return filepath.Join(root, retainedDownloadsDirName, hex.EncodeToString(sum[:16]))
}

// reuseRetainedDownload returns the archive a previous refused ingest kept
// for this (source, mod, file), if there is one.
//
// It does NOT consume the entry: a retry that is refused again - the user
// has not run the edit yet - must still have the bytes for the next one.
// dropRetainedDownload is what removes it, once the ingest succeeds.
func (s *Service) reuseRetainedDownload(sourceID, modID, fileID string) (string, *DownloadResult, bool) {
	dir := s.retainedDownloadDir(sourceID, modID, fileID)
	if dir == "" {
		return "", nil, false
	}
	data, err := os.ReadFile(filepath.Join(dir, retainedDownloadFile))
	if err != nil {
		return "", nil, false
	}
	var meta retainedDownload
	if err := json.Unmarshal(data, &meta); err != nil || meta.FileName == "" {
		return "", nil, false
	}
	archivePath := filepath.Join(dir, meta.FileName)
	info, err := os.Stat(archivePath)
	if err != nil || !info.Mode().IsRegular() {
		return "", nil, false
	}
	s.logger().Debug("reusing the archive a refused ingest kept", "path", archivePath)
	return archivePath, &DownloadResult{
		Path: archivePath, Size: meta.Size, Checksum: meta.Checksum, SHA256: meta.SHA256,
	}, true
}

// retainRefusedDownload keeps archivePath for the retry when err is the
// loader precondition, and does nothing otherwise.
//
// Best-effort by design: a failure to retain costs the user one repeated
// download, which is exactly what happened before this existed, so it is
// logged rather than allowed to replace the refusal the caller is about to
// report. It is also a no-op when the archive already IS the retained copy
// (a second refused retry).
func (s *Service) retainRefusedDownload(err error, sourceID, modID, fileID, archivePath string, result *DownloadResult) {
	var loaderErr *LoaderRequiredError
	if !errors.As(err, &loaderErr) || result == nil {
		return
	}
	dir := s.retainedDownloadDir(sourceID, modID, fileID)
	if dir == "" {
		return
	}
	if filepath.Dir(archivePath) == dir {
		return
	}
	if rerr := s.storeRetainedDownload(dir, archivePath, result); rerr != nil {
		s.logger().Debug("keeping the refused download failed", "path", archivePath, "err", rerr)
	}
}

// storeRetainedDownload does the work: sweep, replace, move, describe.
//
// The archive is MOVED (the caller's staging directory is about to be
// removed) and the sidecar is written last, so a crash mid-move leaves an
// entry reuseRetainedDownload rejects rather than one it trusts.
func (s *Service) storeRetainedDownload(dir, archivePath string, result *DownloadResult) error {
	s.sweepRetainedDownloads()
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("clearing %s: %w", dir, err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	name := filepath.Base(archivePath)
	dest := filepath.Join(dir, name)
	if err := os.Rename(archivePath, dest); err != nil {
		// A staging root on a different filesystem from the download's own
		// temp dir is not a shape lmm creates, but a copy is cheap
		// insurance against one.
		if cerr := copyFileStreaming(archivePath, dest); cerr != nil {
			return fmt.Errorf("moving %s: %w", archivePath, err)
		}
	}
	meta, err := json.Marshal(retainedDownload{
		FileName: name, Size: result.Size, Checksum: result.Checksum, SHA256: result.SHA256,
	})
	if err != nil {
		return fmt.Errorf("describing %s: %w", dest, err)
	}
	return os.WriteFile(filepath.Join(dir, retainedDownloadFile), meta, 0o600)
}

// dropRetainedDownload removes the entry for one (source, mod, file). Called
// on EVERY successful ingest of that file, whichever branch reached the
// cache (service.go defers it for exactly that reason), because the archive
// is in the cache afterwards and the copy here is dead weight.
func (s *Service) dropRetainedDownload(sourceID, modID, fileID string) {
	dir := s.retainedDownloadDir(sourceID, modID, fileID)
	if dir == "" {
		return
	}
	if err := os.RemoveAll(dir); err != nil {
		s.logger().Debug("removing a retained download failed", "path", dir, "err", err)
	}
}

// sweepRetainedDownloads removes entries older than retainedDownloadTTL.
//
// Run on the way IN to a retention - the only thing that creates entries is
// a refused ingest, so that is the only moment the set can GROW - and once
// more when a Service opens. The second caller is what makes the TTL real:
// a user who abandons one install and never has another ingest refused
// would otherwise keep that archive forever, since a retention would only
// ever be swept by a later retention.
func (s *Service) sweepRetainedDownloads() {
	root := s.stagingRoot()
	if root == "" {
		return
	}
	base := filepath.Join(root, retainedDownloadsDirName)
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-retainedDownloadTTL)
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		if rerr := os.RemoveAll(filepath.Join(base, e.Name())); rerr != nil {
			s.logger().Debug("sweeping a retained download failed", "name", e.Name(), "err", rerr)
		}
	}
}
