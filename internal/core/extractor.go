package core

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"
)

// Extractor handles archive extraction for mod files
type Extractor struct{}

// NewExtractor creates a new Extractor
func NewExtractor() *Extractor {
	return &Extractor{}
}

// Extract extracts an archive to the destination directory
// Supports .zip (native), .7z and .rar (via system 7z command)
func (e *Extractor) Extract(ctx context.Context, archivePath, destDir string) error {
	if _, err := os.Stat(archivePath); err != nil {
		return fmt.Errorf("accessing archive %q: %w", archivePath, err)
	}

	format := e.detectFormatFromPath(archivePath)
	if format == "" {
		ext := filepath.Ext(archivePath)
		if ext != "" {
			return fmt.Errorf("unsupported archive format: %s", ext)
		}

		return fmt.Errorf("unsupported archive format for path: %s", archivePath)
	}

	// Create destination directory
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("creating destination directory: %w", err)
	}

	switch format {
	case "zip":
		return e.extractZip(ctx, archivePath, destDir)
	case "7z", "rar":
		return e.extract7z(ctx, archivePath, destDir)
	default:
		return fmt.Errorf("unsupported archive format: %s", format)
	}
}

// CanExtract returns true if the extractor can handle the given filename
func (e *Extractor) CanExtract(filename string) bool {
	return e.detectFormatFromPath(filename) != ""
}

// DetectFormat returns the archive format based on filename extension
func (e *Extractor) DetectFormat(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".zip":
		return "zip"
	case ".7z":
		return "7z"
	case ".rar":
		return "rar"
	default:
		return ""
	}
}

func (e *Extractor) detectFormatFromPath(path string) string {
	if format := e.DetectFormat(path); format != "" {
		return format
	}

	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return ""
	}

	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()

	header := make([]byte, 8)
	n, err := io.ReadFull(file, header)
	if err != nil && err != io.ErrUnexpectedEOF {
		return ""
	}
	header = header[:n]

	switch {
	case len(header) >= 4 && string(header[:4]) == "PK\x03\x04":
		return "zip"
	case len(header) >= 4 && string(header[:4]) == "PK\x05\x06":
		return "zip"
	case len(header) >= 4 && string(header[:4]) == "PK\x07\x08":
		return "zip"
	case len(header) >= 6 && string(header[:6]) == "7z\xBC\xAF\x27\x1C":
		return "7z"
	case len(header) >= 7 && string(header[:7]) == "Rar!\x1A\x07\x00":
		return "rar"
	case len(header) >= 8 && string(header[:8]) == "Rar!\x1A\x07\x01\x00":
		return "rar"
	default:
		return ""
	}
}

// extractZip extracts a ZIP archive using Go's native archive/zip package
func (e *Extractor) extractZip(ctx context.Context, archivePath, destDir string) (err error) {
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("opening zip: %w", err)
	}
	defer func() {
		if cerr := r.Close(); err == nil && cerr != nil {
			err = fmt.Errorf("closing zip: %w", cerr)
		}
	}()

	for _, f := range r.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := e.extractZipFile(f, destDir); err != nil {
			return err
		}
	}

	return nil
}

// extractZipFile extracts a single file from a ZIP archive
func (e *Extractor) extractZipFile(f *zip.File, destDir string) (err error) {
	// Sanitize the file path to prevent zip slip attacks
	destPath, err := e.sanitizePath(destDir, f.Name)
	if err != nil {
		return err
	}

	// Handle directories
	if f.FileInfo().IsDir() {
		// Use 0755 for directories to ensure we can write files into them
		return os.MkdirAll(destPath, 0755)
	}

	// Create parent directories
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return fmt.Errorf("creating directory for %s: %w", f.Name, err)
	}

	// Open the source file
	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("opening file %s in archive: %w", f.Name, err)
	}
	defer func() {
		if cerr := rc.Close(); err == nil && cerr != nil {
			err = fmt.Errorf("closing archive entry %s: %w", f.Name, cerr)
		}
	}()

	// Create the destination file
	outFile, err := os.OpenFile(destPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
	if err != nil {
		return fmt.Errorf("creating file %s: %w", destPath, err)
	}
	defer func() {
		if cerr := outFile.Close(); err == nil && cerr != nil {
			err = fmt.Errorf("closing file %s: %w", destPath, cerr)
		}
	}()

	// Copy the contents
	if _, err = io.Copy(outFile, rc); err != nil {
		return fmt.Errorf("writing file %s: %w", destPath, err)
	}

	return nil
}

// hasReservedSegment reports whether any path segment of name uses the cache's
// reserved bookkeeping prefix. Checking every SEGMENT (not just the base name)
// is what rejects a member nested under a reserved DIRECTORY, which would
// otherwise hide its whole subtree from ListFiles.
func hasReservedSegment(name string) bool {
	for _, segment := range strings.Split(filepath.ToSlash(filepath.Clean(name)), "/") {
		if strings.HasPrefix(segment, cache.ReservedPrefix) {
			return true
		}
	}
	return false
}

// errReservedName builds the rejection error for an archive member that
// claims a name in lmm's reserved cache namespace.
func errReservedName(name string) error {
	return fmt.Errorf("reserved name detected: %s (archive members may not use lmm's reserved %q prefix)", name, cache.ReservedPrefix)
}

// sanitizePath ensures the extracted file path is within the destination directory
// This prevents "zip slip" attacks where malicious archives contain paths like "../../../etc/passwd"
//
// It also rejects members claiming lmm's reserved cache namespace (see
// cache.ReservedPrefix). Mod archives are untrusted content, and a member
// named e.g. ".lmm-file-<someOtherFileID>" would forge that file's cache
// completion marker - making the cache-first convergence guard skip a
// download that never happened and deploy the mod with a genuinely missing
// file, silently. A ".lmm-"-prefixed member also hides itself from ListFiles
// and therefore from deploy entirely. Both are refused rather than skipped,
// matching this repo's other untrusted-archive guards (the zip slip check
// right below, and copyDir's symlink containment check): the archive is
// malformed or hostile and the user should see why. Legitimate markers never
// arrive by extraction - they are written by cache.MarkFileComplete and
// reseeded into staging by prepareStaging's copyDir.
func (e *Extractor) sanitizePath(destDir, filePath string) (string, error) {
	// Clean the path to remove any . or .. components
	cleanPath := filepath.Clean(filePath)

	if hasReservedSegment(cleanPath) {
		return "", errReservedName(filePath)
	}

	// Join with destination directory
	destPath := filepath.Join(destDir, cleanPath)

	// Verify the resulting path is still within destDir
	// This catches cases like filePath = "../../../etc/passwd"
	if !strings.HasPrefix(filepath.Clean(destPath)+string(os.PathSeparator), filepath.Clean(destDir)+string(os.PathSeparator)) {
		// Also check exact match for the destDir itself
		if filepath.Clean(destPath) != filepath.Clean(destDir) {
			return "", fmt.Errorf("path traversal detected: %s", filePath)
		}
	}

	return destPath, nil
}

// extract7zTimeout is the maximum time allowed for 7z extraction (corrupted archives or hangs).
const extract7zTimeout = 5 * time.Minute

// extract7z extracts archives using the system 7z command.
// This handles .7z and .rar files. A timeout prevents hangs on corrupted archives.
func (e *Extractor) extract7z(ctx context.Context, archivePath, destDir string) error {
	_, err := exec.LookPath("7z")
	if err != nil {
		return fmt.Errorf("7z command not found: install p7zip-full to extract .7z and .rar files")
	}

	ctx, cancel := context.WithTimeout(ctx, extract7zTimeout)
	defer cancel()

	// The reserved-namespace check the native zip path makes per member (see
	// sanitizePath) has no equivalent hook here - 7z writes the members
	// itself - so it has to be made against what 7z actually produced.
	// destDir is NOT necessarily empty: prepareStaging reseeds it from the
	// existing cache entry before each file of a multi-file mod, so genuine
	// markers from earlier files are legitimately already present. Only
	// reserved names this extraction ADDED are rejected.
	preexisting, err := reservedNamesIn(destDir)
	if err != nil {
		return err
	}

	// -y: assume yes to all queries; -o: output directory (no space between -o and path)
	cmd := exec.CommandContext(ctx, "7z", "x", "-y", "-o"+destDir, archivePath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("7z extraction timed out after %v", extract7zTimeout)
		}
		return fmt.Errorf("7z extraction failed: %w\nOutput: %s", err, string(output))
	}

	return rejectNewReservedNames(destDir, preexisting)
}

// reservedNamesIn returns the destDir-relative paths of entries already using
// the reserved cache prefix, so a later sweep can tell them apart from ones an
// extraction introduced. Reserved directories are recorded but not descended
// into, matching rejectNewReservedNames.
func reservedNamesIn(destDir string) (map[string]bool, error) {
	found := make(map[string]bool)
	if _, err := os.Stat(destDir); err != nil {
		if os.IsNotExist(err) {
			return found, nil
		}
		return nil, fmt.Errorf("inspecting destination directory: %w", err)
	}

	err := filepath.WalkDir(destDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == destDir || !strings.HasPrefix(d.Name(), cache.ReservedPrefix) {
			return nil
		}
		rel, err := filepath.Rel(destDir, path)
		if err != nil {
			return err
		}
		found[rel] = true
		if d.IsDir() {
			return fs.SkipDir
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("inspecting destination directory: %w", err)
	}
	return found, nil
}

// rejectNewReservedNames fails when an extraction introduced any entry using
// lmm's reserved cache namespace - see sanitizePath for why such a member is
// refused rather than skipped.
func rejectNewReservedNames(destDir string, preexisting map[string]bool) error {
	return filepath.WalkDir(destDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == destDir || !strings.HasPrefix(d.Name(), cache.ReservedPrefix) {
			return nil
		}
		rel, err := filepath.Rel(destDir, path)
		if err != nil {
			return err
		}
		if preexisting[rel] {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		return errReservedName(rel)
	})
}
