package core

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"
	"github.com/google/uuid"
)

// ImportOptions configures the import operation
type ImportOptions struct {
	SourceID    string // Explicit source (empty = auto-detect or "local")
	ModID       string // Explicit mod ID (empty = auto-detect or generate)
	ProfileName string // Target profile
}

// ImportResult contains the outcome of importing a local mod
type ImportResult struct {
	Mod            *domain.Mod
	FilesExtracted int
	LinkedSource   string // "nexusmods", "local", etc.
	AutoDetected   bool   // true if source/ID was parsed from filename
	// RetainedFileID is the identity Import used to key a retained compile
	// source (cache.RetainedSourceName) - only set for the DeployCompile
	// ".exmodz" branch, where it is the archive's own filename (Import has
	// no other stable identity to retain under; see that branch's own doc
	// comment). Callers MUST fold this into the InstalledMod/ModReference's
	// FileIDs (#197 C1 fix): enabledMergeSources walks FileIDs to find each
	// mod's retained source, and a row whose FileIDs never includes this
	// value is invisible to every future merge - silently, forever, since
	// it is also excluded from the staleness fingerprint on both sides of
	// the comparison. Empty for every other deploy mode.
	RetainedFileID string
}

// Importer handles importing mods from local archive files
type Importer struct {
	cache     *cache.Cache
	extractor *Extractor
	// stagingRoot is where archives are extracted before being committed to the
	// cache. Empty means fall back to $TMPDIR — see newStagingDir.
	stagingRoot string
	// resolveMergeCompiler resolves the MergeCompiler-capable source mapped
	// to a DeployCompile game's registry entry (#197), consulted for every
	// import into such a game (#256: the compile source now answers the
	// native/convertible format questions too) — Import has no per-archive
	// source pinned the way DownloadModToCache does, so it must look up the
	// game's configured sources instead. nil when the Importer was built
	// via the standalone NewImporter (no Service context): a DeployCompile
	// import through such an Importer fails loud rather than silently
	// caching an unvalidated archive.
	resolveMergeCompiler func(gameID string) (source.MergeCompiler, error)
}

// NewImporter creates a new Importer that stages extraction in the OS temp dir.
// Prefer Service.NewImporter, which stages under the data dir instead.
func NewImporter(cache *cache.Cache) *Importer {
	return &Importer{
		cache:     cache,
		extractor: NewExtractor(),
	}
}

// NewImporter creates an Importer for game, staging archive extraction under the
// service's data dir rather than $TMPDIR.
func (s *Service) NewImporter(game *domain.Game) *Importer {
	imp := NewImporter(s.GetGameCache(game))
	imp.stagingRoot = s.stagingRoot()
	imp.resolveMergeCompiler = s.mergeCompilerSourceForGame
	return imp
}

// Import imports a mod from a local archive file
func (i *Importer) Import(ctx context.Context, archivePath string, game *domain.Game, opts ImportOptions) (result *ImportResult, err error) {
	// Validate archive exists
	if _, err := os.Stat(archivePath); err != nil {
		return nil, fmt.Errorf("archive not found: %w", err)
	}

	filename := filepath.Base(archivePath)

	// Try to parse NexusMods filename pattern
	var sourceID, modID, version string
	var autoDetected bool

	if opts.SourceID != "" && opts.ModID != "" {
		// Explicit linking provided
		sourceID = opts.SourceID
		modID = opts.ModID
		version = domain.ExtractVersionFromName(strings.TrimSuffix(filename, filepath.Ext(filename)))
		if version == "" {
			version = "unknown"
		}
	} else if parsed := ParseNexusModsFilename(filename); parsed != nil {
		// Auto-detected from filename
		sourceID = domain.SourceLocal // Still local until verified via API
		modID = parsed.ModID
		version = parsed.Version
		autoDetected = true
	} else {
		// No pattern - pure local mod
		sourceID = domain.SourceLocal
		modID = uuid.New().String()
		version = domain.ExtractVersionFromName(strings.TrimSuffix(filename, filepath.Ext(filename)))
		if version == "" {
			version = "unknown"
		}
	}

	var modName string
	var fileCount int
	var retainedFileID string

	// #256: whether filename is the game's NATIVE merge format
	// (mc.IsNativeMergeSource - the seam-routed successor to core's static
	// ".exmodz" test) or a convertible artifact (mc.IsConvertibleArtifact)
	// is the compile source's call, so the compiler is resolved up front
	// for EVERY DeployCompile import, and a resolution failure is a hard
	// error for every one of them - not just the native case the old
	// static test could single out. Falling through instead would be
	// unsafe: with no compiler, core cannot tell a native merge archive
	// from anything else, and the legacy extract path CONTENT-SNIFFS zip
	// magic (detectFormatFromPath), so a real, zip-backed native archive
	// would be silently extracted and cached without ValidateSource -
	// exactly the "never silently cache an unvalidated native archive"
	// invariant the resolver error protects. This supersedes #221 I1's
	// import-side pak fall-through: that was safe only while core itself
	// knew which files were native. (The DOWNLOAD path's I1 fall-through
	// stands - it pins eligibility to the file's own source, a per-archive
	// signal Import does not have.) The resolver error names the fix
	// ("map a source implementing source.MergeCompiler"), which is
	// accurate for any import into a compile game whose compiler is
	// missing or ambiguous.
	//
	// A NIL RESOLVER fails the same way for the same reason, with its own
	// message: core.NewImporter (no Service context) cannot answer the
	// format question for ANY file, and there is a correct importer to use
	// instead. Production only ever constructs the service-backed importer
	// (Service.NewImporter); this path is reachable only by direct
	// core.NewImporter use.
	var mc source.MergeCompiler
	if game.DeployMode == domain.DeployCompile {
		if i.resolveMergeCompiler == nil {
			return nil, fmt.Errorf("game %q requires DeployCompile to import %q, but this Importer was constructed without service context (via core.NewImporter, not Service.NewImporter) and has no compiler resolver to consult - import via the service-backed importer instead", game.ID, filename)
		}
		var mcErr error
		if mc, mcErr = i.resolveMergeCompiler(game.ID); mcErr != nil {
			return nil, mcErr
		}
	}
	mergeEligible := mc != nil && mc.IsNativeMergeSource(filename)
	convertEligiblePak := mc != nil && isConvertEligibleArtifact(game, mc, filename)

	// Handle based on game's deploy mode
	if game.DeployMode == domain.DeployCompile && (mergeEligible || convertEligiblePak) {
		// Validate mode (#197): Import has no real source file ID the way a
		// download does (DownloadableFile.ID is resolved later, outside
		// Import, only when --id was given), so the retained source is
		// keyed by the archive's own filename instead - stable across
		// re-imports of the same name, and the ONLY identity Import ever
		// has for this content.
		if err := mc.ValidateSource(archivePath); err != nil {
			return nil, fmt.Errorf("validating %s: %w", filename, err)
		}

		modName = strings.TrimSuffix(filename, filepath.Ext(filename))
		if version != "" && version != "unknown" {
			if idx := strings.LastIndex(modName, version); idx > 0 {
				modName = strings.TrimRight(modName[:idx], "-_ ")
			}
		}

		cacheMod := &domain.Mod{ID: modID, SourceID: sourceID, Version: version, GameID: game.ID}
		cachePath, stagePath, err := prepareUnseededStaging(i.cache, game, cacheMod)
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(stagePath) //nolint:errcheck

		if err := os.MkdirAll(stagePath, 0755); err != nil {
			return nil, fmt.Errorf("preparing cache staging: %w", err)
		}
		retainedPath := filepath.Join(stagePath, cache.RetainedSourceName(filename))
		if err := copyFileStreaming(archivePath, retainedPath); err != nil {
			return nil, fmt.Errorf("retaining %s: %w", filename, err)
		}
		// pak (#221): ALSO keep a deployable copy as the sole member, so the
		// default state is raw-deploy until the first successful merge
		// flips the manifest - mirrors DownloadModToCache's identical
		// widening. exmodz keeps fileCount 0 (#197: merged-only, no per-mod
		// deployment artifact).
		if convertEligiblePak {
			deployablePath := filepath.Join(stagePath, filename)
			if err := copyFileStreaming(archivePath, deployablePath); err != nil {
				return nil, fmt.Errorf("staging deployable pak %s: %w", filename, err)
			}
			fileCount = 1
		}
		if err := commitStagedCache(cachePath, stagePath); err != nil {
			return nil, err
		}
		retainedFileID = filename
	} else if game.DeployMode == domain.DeployCopy {
		// Copy mode: just copy the file as-is to cache (don't extract)
		modName = strings.TrimSuffix(filename, filepath.Ext(filename))
		if version != "" && version != "unknown" {
			if idx := strings.LastIndex(modName, version); idx > 0 {
				modName = strings.TrimRight(modName[:idx], "-_ ")
			}
		}

		cachePath := i.cache.ModPath(game.ID, sourceID, modID, version)

		// Remove existing cache if present (re-import case)
		// Ignore errors - if removal fails, MkdirAll/copy will error anyway
		os.RemoveAll(cachePath)
		if err := os.MkdirAll(cachePath, 0755); err != nil {
			return nil, fmt.Errorf("creating cache directory: %w", err)
		}

		// Copy the file to cache using streaming to avoid memory spikes
		destPath := filepath.Join(cachePath, filename)
		if err := copyFileStreaming(archivePath, destPath); err != nil {
			return nil, fmt.Errorf("copying to cache: %w", err)
		}
		fileCount = 1
	} else {
		// Extract mode: validate and extract the archive
		if !i.extractor.CanExtract(archivePath) {
			return nil, fmt.Errorf("unsupported archive format: %s", filepath.Ext(archivePath))
		}

		// Extract to a staging directory first
		tempDir, err := newStagingDir(i.stagingRoot, "lmm-import-*")
		if err != nil {
			return nil, err
		}
		defer func() {
			if cerr := os.RemoveAll(tempDir); err == nil && cerr != nil {
				err = fmt.Errorf("removing temp directory: %w", cerr)
			}
		}()

		extractedPath := filepath.Join(tempDir, "extracted")
		if err := i.extractor.Extract(archivePath, extractedPath); err != nil {
			return nil, fmt.Errorf("extracting archive: %w", err)
		}

		// Detect mod name from extracted content
		modName = DetectModName(extractedPath, filename)

		// Move extracted files to cache
		cachePath := i.cache.ModPath(game.ID, sourceID, modID, version)
		if err := os.MkdirAll(filepath.Dir(cachePath), 0755); err != nil {
			return nil, fmt.Errorf("creating cache directory: %w", err)
		}

		// Remove existing cache if present (re-import case)
		if err := os.RemoveAll(cachePath); err != nil {
			return nil, fmt.Errorf("removing existing cache for re-import: %w", err)
		}

		if err := os.Rename(extractedPath, cachePath); err != nil {
			// If rename fails (cross-device), fall back to copy
			if err := copyDir(extractedPath, cachePath); err != nil {
				return nil, fmt.Errorf("moving to cache: %w", err)
			}
		}

		// Count extracted files
		files, err := i.cache.ListFiles(game.ID, sourceID, modID, version)
		if err != nil {
			return nil, fmt.Errorf("listing cached files: %w", err)
		}
		fileCount = len(files)
	}

	mod := &domain.Mod{
		ID:       modID,
		SourceID: sourceID,
		Name:     modName,
		Version:  version,
		GameID:   game.ID,
	}

	return &ImportResult{
		Mod:            mod,
		FilesExtracted: fileCount,
		LinkedSource:   sourceID,
		AutoDetected:   autoDetected,
		RetainedFileID: retainedFileID,
	}, nil
}

// matchImportedFile picks the source file an imported archive corresponds
// to (#139): exact FileName match first, then - only when allowVersionFallback
// is set (the --id archive path, where the user asserted the mod identity) -
// the sole file carrying the imported version. Any ambiguity (several
// candidates) resolves to nil rather than a guess: a marker stamped with the
// wrong file ID is worse provenance than no marker at all. version "" and
// the importer's "unknown" sentinel never version-match. A file whose ID the
// cache layer cannot round-trip through a marker filename
// (cache.VerifiableFileID) is no candidate at all: stamping it would
// silently no-op and HasFileIDs could never verify it, so recording it buys
// nothing.
func matchImportedFile(files []domain.DownloadableFile, archiveFilename, version string, allowVersionFallback bool) *domain.DownloadableFile {
	var exact *domain.DownloadableFile
	for i := range files {
		if files[i].FileName != archiveFilename || !cache.VerifiableFileID(files[i].ID) {
			continue
		}
		if exact != nil {
			return nil
		}
		exact = &files[i]
	}
	if exact != nil {
		return exact
	}
	if !allowVersionFallback || version == "" || version == "unknown" {
		return nil
	}
	var byVersion *domain.DownloadableFile
	for i := range files {
		if files[i].Version != version || !cache.VerifiableFileID(files[i].ID) {
			continue
		}
		if byVersion != nil {
			return nil
		}
		byVersion = &files[i]
	}
	return byVersion
}

// ResolveImportedFile resolves the source file an imported archive
// corresponds to (#139): sourceMod is the already-fetched source mod (the
// import flow fetched it for metadata enrichment or scan matching), version
// the version the import recorded locally. Matching follows
// matchImportedFile's rules; a clean no-match or ambiguity is (nil, nil), an
// error only reports a failed source listing - callers treat it as
// non-fatal and keep the import marker-less.
func (s *Service) ResolveImportedFile(ctx context.Context, sourceID string, sourceMod *domain.Mod, archiveFilename, version string, allowVersionFallback bool) (*domain.DownloadableFile, error) {
	files, err := s.GetModFiles(ctx, sourceID, sourceMod)
	if err != nil {
		return nil, fmt.Errorf("listing source files: %w", err)
	}
	return matchImportedFile(files, archiveFilename, version, allowVersionFallback), nil
}

// MarkImportedFileComplete stamps fileID's completion marker - with the
// entry's member manifest - onto mod's import-written cache entry (#139), so
// the file-granular cache-first guards (Cache.HasFileIDs) recognize the
// entry instead of forcing one redundant redownload, and provenance-based
// undeploy narrowing can attribute its members. Import writes the cache
// directly (no staging commit), so the marker is stamped after the fact;
// the members are whatever the entry actually holds.
func (s *Service) MarkImportedFileComplete(game *domain.Game, mod *domain.Mod, fileID string) error {
	gameCache := s.GetGameCache(game)
	members, err := gameCache.ListFiles(game.ID, mod.SourceID, mod.ID, mod.Version)
	if err != nil {
		return fmt.Errorf("listing cache members: %w", err)
	}
	versionDir := gameCache.ModPath(game.ID, mod.SourceID, mod.ID, mod.Version)
	return cache.MarkFileCompleteWithMembers(versionDir, fileID, members)
}

// copyDir recursively copies a directory using streaming I/O to avoid loading
// entire files into memory (important for large mod archives). It is the
// cross-device-rename fallback in Import, so it must handle whatever an
// extracted archive contains - including directory symlinks (some mod
// packages vendor shared assets that way) - while treating that content as
// untrusted (it came from a downloaded archive).
//
// It deliberately does not use filepath.Walk: Walk classifies entries with
// Lstat, which never follows symlinks, so a symlinked subdirectory reports
// IsDir()==false and falls through to a plain file copy. Reading a directory
// fd as a byte stream then fails with EISDIR. copyDir instead os.Stat's each
// entry (following symlinks) to classify it, and materializes symlinked
// directories as real directories in the destination.
//
// The copy root (src) itself may be, or resolve through, a symlink - that's
// the intended top-level symlinked-mod-dir feature (#52) - so it is resolved
// once up front and exempted from the containment check below. Every NESTED
// symlink (directory or file, at any depth), however, must resolve to a path
// within that resolved root: an extracted archive is untrusted content, and
// a nested symlink to e.g. /etc or $HOME would otherwise pull arbitrary
// external content into the mod cache (path traversal). Escaping symlinks
// fail the copy with an error naming the entry, rather than being silently
// skipped - the mod is malformed or hostile, and the user should see why.
func copyDir(src, dst string) error {
	resolvedRoot, err := filepath.EvalSymlinks(src)
	if err != nil {
		return fmt.Errorf("resolving %s: %w", src, err)
	}
	return copyDirFollowing(src, dst, resolvedRoot, map[string]bool{})
}

// copyDirFollowing does the recursive work for copyDir. resolvedRoot is the
// symlink-free real path of the copy root, fixed for the whole walk, against
// which every nested symlink is containment-checked. visited holds the
// resolved (symlink-free) real path of every directory currently on the
// active recursion stack (root down to src) - not every directory ever seen.
// It is added on entry and removed via defer on exit, so it only ever
// contains true ancestors of the node being processed. That distinction
// matters: two sibling symlinks/dirs that both resolve to the same real path
// are legal (just duplicated content) and must not be flagged, but a symlink
// that points back at a directory still being walked - an actual cycle -
// would recurse forever without this guard.
func copyDirFollowing(src, dst, resolvedRoot string, visited map[string]bool) error {
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("stat %s: %w", src, err)
	}

	real, err := filepath.EvalSymlinks(src)
	if err != nil {
		return fmt.Errorf("resolving %s: %w", src, err)
	}
	if visited[real] {
		return fmt.Errorf("copying %s: symlink cycle detected", src)
	}
	visited[real] = true
	defer delete(visited, real)

	if err := os.MkdirAll(dst, info.Mode()); err != nil {
		return err
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return fmt.Errorf("reading %s: %w", src, err)
	}

	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())

		// Containment check happens before anything else touches this entry
		// (both directories and files: copyFileStreaming's os.Open follows
		// symlinks too, so a symlinked file is just as capable of escaping
		// the root as a symlinked directory).
		lst, err := os.Lstat(srcPath)
		if err != nil {
			return fmt.Errorf("stat %s: %w", srcPath, err)
		}
		if lst.Mode()&os.ModeSymlink != 0 {
			resolvedTarget, err := filepath.EvalSymlinks(srcPath)
			if err != nil {
				return fmt.Errorf("resolving symlink %s: %w", srcPath, err)
			}
			if !pathWithinRoot(resolvedRoot, resolvedTarget) {
				return fmt.Errorf("copying %s: symlink target %s escapes the mod directory %s; refusing to follow", srcPath, resolvedTarget, resolvedRoot)
			}
		}

		entryInfo, err := os.Stat(srcPath) // follow symlinks for classification
		if err != nil {
			return fmt.Errorf("stat %s: %w", srcPath, err)
		}

		if entryInfo.IsDir() {
			if err := copyDirFollowing(srcPath, dstPath, resolvedRoot, visited); err != nil {
				return err
			}
			continue
		}

		if err := copyFileStreaming(srcPath, dstPath); err != nil {
			return err
		}
	}

	return nil
}

// pathWithinRoot reports whether target is root itself or a descendant of
// root. Both must already be resolved (symlink-free, filepath.Clean'd)
// absolute paths. A plain strings.HasPrefix(target, root) would wrongly
// match a sibling like "/root-evil" against root "/root"; requiring the
// match be exact or followed by a path separator rules that out.
func pathWithinRoot(root, target string) bool {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	if target == root {
		return true
	}
	return strings.HasPrefix(target, root+string(filepath.Separator))
}

// ScanResult contains the outcome of scanning a single mod file
type ScanResult struct {
	FilePath       string      // Original path in mod_path
	FileName       string      // Base filename
	Mod            *domain.Mod // Detected/created mod info
	MatchedSource  string      // a configured source's ID (any registered source, not just curseforge/nexusmods), or "local"
	AlreadyTracked bool        // True if already in lmm database
	Error          error       // Any error during processing

	// ResolvedFile is the matched source's own file this scanned archive
	// corresponds to (#139), when the import flow could resolve one (exact
	// FileName match via ResolveImportedFile); nil otherwise. Set by the
	// import flow after source matching, not by ScanModPath itself.
	ResolvedFile *domain.DownloadableFile
}

// ScanOptions configures the scan operation
type ScanOptions struct {
	ProfileName string
	DryRun      bool // If true, don't actually import, just report what would be done
}

// ScanModPath scans the game's mod_path for untracked mods
func (i *Importer) ScanModPath(ctx context.Context, game *domain.Game, installedMods []domain.InstalledMod, opts ScanOptions) ([]ScanResult, error) {
	if game.ModPath == "" {
		return nil, fmt.Errorf("game has no mod_path configured")
	}

	// Expand mod path
	modPath := game.ModPath
	if strings.HasPrefix(modPath, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			modPath = filepath.Join(home, modPath[2:])
		}
	}

	// Check if mod_path exists
	if _, err := os.Stat(modPath); err != nil {
		return nil, fmt.Errorf("mod_path does not exist: %s", modPath)
	}

	var results []ScanResult

	// Scan based on deploy mode
	if game.DeployMode == domain.DeployCopy {
		// For copy mode, scan for files (.jar, .zip, etc.)
		entries, err := os.ReadDir(modPath)
		if err != nil {
			return nil, fmt.Errorf("reading mod_path: %w", err)
		}

		for _, entry := range entries {
			if entry.IsDir() {
				continue // Skip directories in copy mode
			}

			filename := entry.Name()
			ext := strings.ToLower(filepath.Ext(filename))

			// Only process mod file extensions
			if ext != ".jar" && ext != ".zip" {
				continue
			}

			filePath := filepath.Join(modPath, filename)

			// Check if it's a symlink (already deployed by lmm)
			info, err := os.Lstat(filePath)
			if err == nil && info.Mode()&os.ModeSymlink != 0 {
				// It's a symlink - already tracked
				results = append(results, ScanResult{
					FilePath:       filePath,
					FileName:       filename,
					AlreadyTracked: true,
				})
				continue
			}

			// Check if already tracked by comparing to installed mods
			alreadyTracked := i.isFileTracked(filename, installedMods)

			result := ScanResult{
				FilePath:       filePath,
				FileName:       filename,
				AlreadyTracked: alreadyTracked,
			}

			if !alreadyTracked {
				// Try to detect mod info from filename
				result.Mod = i.detectModFromFilename(filename, game.ID)
				result.MatchedSource = domain.SourceLocal // Default, can be upgraded later
			}

			results = append(results, result)
		}
	} else {
		// For extract mode, scan for directories
		entries, err := os.ReadDir(modPath)
		if err != nil {
			return nil, fmt.Errorf("reading mod_path: %w", err)
		}

		for _, entry := range entries {
			if !entry.IsDir() {
				continue // Skip files in extract mode
			}

			dirName := entry.Name()
			dirPath := filepath.Join(modPath, dirName)

			// Check if already tracked
			alreadyTracked := i.isFileTracked(dirName, installedMods)

			result := ScanResult{
				FilePath:       dirPath,
				FileName:       dirName,
				AlreadyTracked: alreadyTracked,
			}

			if !alreadyTracked {
				// Create mod info from directory name
				result.Mod = i.detectModFromFilename(dirName, game.ID)
				result.MatchedSource = domain.SourceLocal
			}

			results = append(results, result)
		}
	}

	return results, nil
}

// isFileTracked checks if a file is already tracked by comparing to installed mods
func (i *Importer) isFileTracked(filename string, installedMods []domain.InstalledMod) bool {
	// Strip extension for comparison
	baseName := strings.TrimSuffix(filename, filepath.Ext(filename))
	baseNameLower := strings.ToLower(baseName)
	filenameLower := strings.ToLower(filename)

	for _, mod := range installedMods {
		modNameLower := strings.ToLower(mod.Name)

		// Check various ways the mod might be identified (case-insensitive)
		if modNameLower == baseNameLower || modNameLower == filenameLower {
			return true
		}
		// Check if the mod's cached filename matches
		if strings.Contains(filenameLower, strings.ToLower(mod.ID)) {
			return true
		}
	}
	return false
}

// findDuplicateMod checks if a mod with similar name already exists (for duplicate prevention)
func (i *Importer) FindDuplicateMod(modName string, installedMods []domain.InstalledMod) *domain.InstalledMod {
	modNameLower := strings.ToLower(modName)
	// Normalize: remove common suffixes like version numbers, underscores, dashes
	normalized := normalizeModName(modNameLower)

	for idx := range installedMods {
		existingNorm := normalizeModName(strings.ToLower(installedMods[idx].Name))
		if existingNorm == normalized {
			return &installedMods[idx]
		}
	}
	return nil
}

// normalizeModName removes version suffixes and normalizes separators for comparison
func normalizeModName(name string) string {
	// Remove common version patterns like -1.0.5, _v2, etc.
	re := regexp.MustCompile(`[-_]?v?\d+(\.\d+)*$`)
	name = re.ReplaceAllString(name, "")
	// Normalize separators
	name = strings.ReplaceAll(name, "-", "")
	name = strings.ReplaceAll(name, "_", "")
	name = strings.ReplaceAll(name, " ", "")
	return name
}

// detectModFromFilename attempts to parse mod info from a filename
func (i *Importer) detectModFromFilename(filename string, gameID string) *domain.Mod {
	// Strip extension
	baseName := strings.TrimSuffix(filename, filepath.Ext(filename))

	// Try to extract version
	version := domain.ExtractVersionFromName(baseName)

	// Extract mod name (everything before the version)
	name := baseName
	if version != "" {
		// Find where the version starts and take everything before
		idx := strings.LastIndex(baseName, version)
		if idx > 0 {
			name = strings.TrimRight(baseName[:idx], "-_ ")
		}
	}

	return &domain.Mod{
		ID:       uuid.New().String(),
		SourceID: domain.SourceLocal,
		Name:     name,
		Version:  version,
		GameID:   gameID,
	}
}

// copyFileStreaming copies a file using streaming to avoid loading it all into memory
func copyFileStreaming(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("opening source: %w", err)
	}
	defer srcFile.Close()

	srcInfo, err := srcFile.Stat()
	if err != nil {
		return fmt.Errorf("stat source: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return fmt.Errorf("creating destination directory: %w", err)
	}

	dstFile, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, srcInfo.Mode())
	if err != nil {
		return fmt.Errorf("creating destination: %w", err)
	}
	defer dstFile.Close()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return fmt.Errorf("copying: %w", err)
	}

	return dstFile.Sync()
}
