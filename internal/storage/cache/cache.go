package cache

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Cache manages the central mod file cache
type Cache struct {
	basePath   string
	gameScoped bool // when true, basePath is game-specific; omit gameID from ModPath
}

// New creates a new cache manager for the global cache (basePath/gameID/source-mod/version).
func New(basePath string) *Cache {
	return &Cache{basePath: basePath}
}

// NewGameScoped creates a cache for a per-game cache_path.
// Paths are basePath/source-mod/version (no gameID); the base is already game-specific.
func NewGameScoped(basePath string) *Cache {
	return &Cache{basePath: basePath, gameScoped: true}
}

// ModPath returns the path where a mod version's files are stored
func (c *Cache) ModPath(gameID, sourceID, modID, version string) string {
	modKey := fmt.Sprintf("%s-%s", sourceID, modID)
	if c.gameScoped {
		return filepath.Join(c.basePath, modKey, version)
	}
	return filepath.Join(c.basePath, gameID, modKey, version)
}

// Exists checks if a mod version is cached
func (c *Cache) Exists(gameID, sourceID, modID, version string) bool {
	path := c.ModPath(gameID, sourceID, modID, version)
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// ReservedPrefix marks lmm's own bookkeeping entries inside a cache version
// directory. Nothing under it is mod content: reserved entries are excluded
// from every enumerator of a version directory (ListFiles, Size) so they can
// never be deployed, counted, checksummed, or conflict-matched.
//
// It is exported so the archive extractor (internal/core) can enforce the
// same namespace from the other side: mod archives are untrusted content, and
// a member shipped under this prefix could otherwise forge a completion
// marker (making the cache-first guard skip a real download) or hide itself
// from deploy. One constant, both sides.
const ReservedPrefix = ".lmm-"

// fileMarkerPrefix names the per-source-file completion markers written by
// MarkFileComplete and read by HasFileIDs.
const fileMarkerPrefix = ReservedPrefix + "file-"

// isReserved reports whether a cache directory entry is lmm bookkeeping
// rather than mod content.
func isReserved(name string) bool {
	return strings.HasPrefix(name, ReservedPrefix)
}

// verifiableFileID reports whether a source file ID can be round-tripped
// through a marker filename. A blank ID has nothing to name, and one carrying
// a path separator would place (or look for) the marker outside the version
// directory entirely. Both are refused rather than sanitized: two distinct
// IDs sanitizing to the same marker would let one file's completion vouch for
// another's.
func verifiableFileID(fileID string) bool {
	return fileID != "" && !strings.ContainsAny(fileID, `/\`+"\x00")
}

// MarkFileComplete writes the zero-byte completion marker for fileID into
// versionDir, recording that this source file's content has been fully
// committed to that cache entry. It is the write side of HasFileIDs.
//
// A bare (zero-byte) marker vouches for completion but records no member
// manifest - FileManifests reports it as Recorded=false. Production commits
// go through MarkFileCompleteWithMembers instead; this remains the legacy
// shape every pre-manifest cache entry already has on disk.
//
// versionDir is a raw directory path rather than a cache key because the
// marker is written into the STAGING directory just before it is swapped into
// place (see internal/core/service.go's commitStagedCacheWithMarker), so the
// marker and the content it vouches for become visible in the same atomic
// rename - a marker can never appear without its content.
//
// An unverifiable fileID (blank, or carrying a path separator - see
// verifiableFileID) is skipped rather than rejected: HasFileIDs refuses those
// same IDs, so the entry simply reads as incomplete and costs a redundant
// re-download, which is the safe direction and never a write outside
// versionDir.
func MarkFileComplete(versionDir, fileID string) error {
	return writeFileMarker(versionDir, fileID, nil)
}

// manifestHeader is the first line of a marker body that carries a member
// manifest. Its presence is what distinguishes "recorded an empty member set"
// (header only) from a legacy bare marker (empty body, no manifest at all) -
// the absence-vs-empty distinction FileManifests' consumers depend on.
const manifestHeader = "lmm-manifest/1"

// MarkFileCompleteWithMembers is MarkFileComplete plus provenance: the marker
// body records WHICH versionDir-relative members this file ID contributed
// (slash-separated, one per line, under the manifestHeader line), so the
// same-version update path can later undeploy members owned solely by a
// superseded file (#144 item 4). Read back via FileManifests.
//
// A member name a line-oriented body cannot represent faithfully (embedded
// newline/carriage return) degrades the WHOLE write to a bare legacy marker:
// completion is still vouched for, but no manifest is recorded, so consumers
// fall back to today's union behavior rather than ever trusting a corrupted
// member list.
func MarkFileCompleteWithMembers(versionDir, fileID string, members []string) error {
	lines := make([]string, 0, len(members)+1)
	lines = append(lines, manifestHeader)
	for _, m := range members {
		if m == "" {
			continue
		}
		if strings.ContainsAny(m, "\n\r") {
			return writeFileMarker(versionDir, fileID, nil)
		}
		lines = append(lines, filepath.ToSlash(m))
	}
	return writeFileMarker(versionDir, fileID, []byte(strings.Join(lines, "\n")+"\n"))
}

// writeFileMarker writes fileID's completion marker with the given body (nil
// for a bare legacy marker). See MarkFileComplete for the skip-unverifiable
// contract.
func writeFileMarker(versionDir, fileID string, body []byte) error {
	if !verifiableFileID(fileID) {
		return nil
	}
	if err := os.MkdirAll(versionDir, 0755); err != nil {
		return fmt.Errorf("creating cache dir for file marker: %w", err)
	}
	path := filepath.Join(versionDir, fileMarkerPrefix+fileID)
	if err := os.WriteFile(path, body, 0644); err != nil {
		return fmt.Errorf("writing cache file marker: %w", err)
	}
	return nil
}

// FileManifest is one completion marker's recorded member manifest.
type FileManifest struct {
	// Members holds the version-dir-relative paths (OS separators) this file
	// ID contributed to the cache entry. Meaningful only when Recorded.
	Members []string
	// Recorded is false for a legacy bare marker: the file's completion is
	// vouched for, but WHICH members it contributed was never written down.
	// Consumers must treat that as unknown provenance - never as "none".
	Recorded bool
}

// FileManifests reads every completion marker in the version directory and
// returns each file ID's manifest. A directory (or entry) with no markers
// returns an empty map, never an error. A marker whose body is empty or
// carries an unrecognized header parses as Recorded=false - the safe,
// union-fallback direction - so pre-manifest entries and any future format
// revision both degrade silently rather than misreport provenance.
func (c *Cache) FileManifests(gameID, sourceID, modID, version string) (map[string]FileManifest, error) {
	versionDir := c.ModPath(gameID, sourceID, modID, version)
	entries, err := os.ReadDir(versionDir)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]FileManifest{}, nil
		}
		return nil, fmt.Errorf("reading cache markers: %w", err)
	}

	manifests := make(map[string]FileManifest)
	for _, entry := range entries {
		fileID, ok := strings.CutPrefix(entry.Name(), fileMarkerPrefix)
		if !ok || entry.IsDir() || !verifiableFileID(fileID) {
			continue
		}
		body, err := os.ReadFile(filepath.Join(versionDir, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("reading cache marker %s: %w", entry.Name(), err)
		}
		manifests[fileID] = parseManifest(body)
	}
	return manifests, nil
}

// parseManifest decodes a marker body into its FileManifest. Anything that
// isn't a well-formed manifestHeader body reads as a bare marker.
func parseManifest(body []byte) FileManifest {
	lines := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	if len(lines) == 0 || lines[0] != manifestHeader {
		return FileManifest{}
	}
	var members []string
	for _, line := range lines[1:] {
		if line == "" {
			continue
		}
		members = append(members, filepath.FromSlash(line))
	}
	return FileManifest{Members: members, Recorded: true}
}

// HasFileIDs reports whether every named source file has been fully committed
// to the given (gameID, sourceID, modID, version) cache entry - a stronger
// guard than Exists alone, which only checks the version directory's presence
// and can return true for a PARTIALLY populated entry (e.g. a previous
// download run that stored file 1 of 2 before failing - each file is
// committed to the cache individually, so a broken-off run leaves the
// directory present but incomplete). Callers doing a "cache already has this,
// skip downloading" check (see internal/core/flows.go's ApplyProfileSwitch
// and cmd/lmm/profile.go's doProfileApply, both #96) should use this instead
// of Exists to decide whether a re-download is genuinely unnecessary.
//
// Completeness is judged by the per-file markers MarkFileComplete writes, NOT
// by on-disk filenames: under the default DeployExtract mode a cache version
// directory holds an archive's EXTRACTED MEMBERS, whose names have nothing to
// do with the DownloadableFile's own FileName, so a filename-based check
// would report false for essentially every archive-based mod and redownload
// despite a complete cache.
//
// LEGACY ENTRIES: a cache directory populated before markers existed (or by
// an `lmm import` that could not resolve the archive against its source's
// file listing - source-linked imports that do resolve stamp the marker at
// import time, #139) carries no markers and therefore reads as incomplete.
// That costs exactly one redundant redownload, which commits markers on the
// way through and makes every later check a hit.
//
// An empty/nil fileIDs degrades to Exists - there is nothing left to verify
// beyond the directory's presence. An unverifiable ID (blank, or carrying a
// path separator) reports false rather than being skipped.
func (c *Cache) HasFileIDs(gameID, sourceID, modID, version string, fileIDs []string) bool {
	if !c.Exists(gameID, sourceID, modID, version) {
		return false
	}
	versionDir := c.ModPath(gameID, sourceID, modID, version)
	for _, id := range fileIDs {
		if !verifiableFileID(id) {
			return false
		}
		if _, err := os.Stat(filepath.Join(versionDir, fileMarkerPrefix+id)); err != nil {
			return false
		}
	}
	return true
}

// Store saves a file to the cache
func (c *Cache) Store(gameID, sourceID, modID, version, relativePath string, content []byte) error {
	modPath := c.ModPath(gameID, sourceID, modID, version)
	fullPath := filepath.Join(modPath, relativePath)

	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating cache dir: %w", err)
	}

	if err := os.WriteFile(fullPath, content, 0644); err != nil {
		return fmt.Errorf("writing cached file: %w", err)
	}

	return nil
}

// ListFiles returns all files in a cached mod version.
//
// This is the choke point every consumer of a cache entry's contents goes
// through - Installer.Install/Replace/Undeploy, DetectConflicts, `lmm
// verify`'s file-count check, DownloadModResult.FilesExtracted - so lmm's own
// bookkeeping entries (the .lmm-* completion markers, see MarkFileComplete)
// are excluded here and never reach any of them. A version directory holding
// ONLY markers correctly reports zero files.
//
// CloneMod is the sole exception and walks with includeReserved - see there.
func (c *Cache) ListFiles(gameID, sourceID, modID, version string) ([]string, error) {
	return walkEntries(c.ModPath(gameID, sourceID, modID, version), false)
}

// walkEntries lists modPath's files relative to modPath. When includeReserved
// is false (every caller but CloneMod) lmm's own .lmm-* bookkeeping entries
// are omitted.
func walkEntries(modPath string, includeReserved bool) ([]string, error) {
	var files []string
	err := filepath.WalkDir(modPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Never skip the version directory itself, whose own name is a
			// version string and has nothing to do with the reserved prefix.
			if !includeReserved && path != modPath && isReserved(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if !includeReserved && isReserved(d.Name()) {
			return nil
		}
		// Skip symlinks to avoid traversing outside cache root
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}

		relPath, err := filepath.Rel(modPath, path)
		if err != nil {
			return err
		}
		files = append(files, relPath)
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("listing cached files: %w", err)
	}

	return files, nil
}

// Delete removes a cached mod version
func (c *Cache) Delete(gameID, sourceID, modID, version string) error {
	modPath := c.ModPath(gameID, sourceID, modID, version)
	if err := os.RemoveAll(modPath); err != nil {
		return fmt.Errorf("deleting cached mod: %w", err)
	}
	return nil
}

// GetFilePath returns the full path to a cached file
func (c *Cache) GetFilePath(gameID, sourceID, modID, version, relativePath string) string {
	return filepath.Join(c.ModPath(gameID, sourceID, modID, version), relativePath)
}

// CloneMod copies a cached mod version into another cache.
//
// Unlike every other walker it deliberately INCLUDES lmm's own .lmm-*
// bookkeeping entries: a clone is meant to reproduce the entry itself, not
// enumerate its mod content, and the reinstall cache transaction
// (internal/core/flows.go) round-trips a live entry through a staged/snapshot
// cache and back. Dropping the completion markers on that round trip would
// silently downgrade a complete entry to a pre-marker one and cost a
// redundant redownload on the next cache-first check.
func (c *Cache) CloneMod(dest *Cache, gameID, sourceID, modID, version string) error {
	files, err := walkEntries(c.ModPath(gameID, sourceID, modID, version), true)
	if err != nil {
		return err
	}
	for _, file := range files {
		srcPath := c.GetFilePath(gameID, sourceID, modID, version, file)
		dstPath := dest.GetFilePath(gameID, sourceID, modID, version, file)
		if err := os.MkdirAll(filepath.Dir(dstPath), 0755); err != nil {
			return fmt.Errorf("creating destination dir: %w", err)
		}
		if err := copyFile(srcPath, dstPath); err != nil {
			return err
		}
	}
	return nil
}

// Size returns the total size of cached files for a mod version. lmm's own
// .lmm-* bookkeeping entries are excluded, same as in ListFiles.
func (c *Cache) Size(gameID, sourceID, modID, version string) (int64, error) {
	modPath := c.ModPath(gameID, sourceID, modID, version)

	var totalSize int64
	err := filepath.WalkDir(modPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != modPath && isReserved(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if isReserved(d.Name()) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		totalSize += info.Size()
		return nil
	})

	if err != nil {
		return 0, fmt.Errorf("calculating cache size: %w", err)
	}

	return totalSize, nil
}

func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("opening source: %w", err)
	}
	defer srcFile.Close()

	srcInfo, err := srcFile.Stat()
	if err != nil {
		return fmt.Errorf("stat source: %w", err)
	}

	dstFile, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, srcInfo.Mode())
	if err != nil {
		return fmt.Errorf("creating destination: %w", err)
	}
	defer dstFile.Close()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return fmt.Errorf("copying file: %w", err)
	}
	return nil
}
