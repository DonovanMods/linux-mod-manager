package core

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
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
			return fmt.Errorf("%w: %s", ErrUnsupportedArchive, ext)
		}

		return fmt.Errorf("%w for path: %s", ErrUnsupportedArchive, archivePath)
	}

	// Create destination directory
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("creating destination directory: %w", err)
	}

	switch format {
	case "zip":
		return e.extractZip(ctx, archivePath, destDir)
	case "7z", "rar":
		return e.extract7z(ctx, archivePath, destDir, format)
	default:
		return fmt.Errorf("%w: %s", ErrUnsupportedArchive, format)
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
	defer func() { _ = file.Close() }()

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

// extractZip extracts a ZIP archive using Go's native archive/zip package,
// or - when any member uses a compression method archive/zip cannot decode
// (#656) - through the system 7z (extractZipWith7z). The choice is made for
// the whole archive before anything is written.
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

	if f := zipNeeds7z(r.File); f != nil {
		return e.extractZipWith7z(ctx, archivePath, destDir, f)
	}
	if err := checkFreeSpace(archivePath, destDir, zipDeclaredSize(r.File)); err != nil {
		return err
	}

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
// reseeded into staging by prepareStaging's seedFromCache.
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

// ErrUnsupportedArchive is an archive whose format lmm cannot extract
// (#660): its text is the "unsupported archive format" every such failure
// already began with.
var ErrUnsupportedArchive = errors.New("unsupported archive format")

// ErrSevenZipMissing is the system 7z command not being on PATH: lmm reads
// .7z and .rar archives - every NexusMods collection's recipe among them
// (#607) - through it.
var ErrSevenZipMissing = errors.New("7z command not found")

// extract7z extracts a .7z or .rar archive (format names which) through
// the system 7z, by the route every 7z extraction takes (extractThrough7z,
// #658): the listing is judged before anything is written, 7z writes into
// a private directory, and that tree is checked - no links, no special
// files, no escaping or reserved names - before anything moves into
// destDir. The no-progress watchdog (run7z), not a fixed time limit, ends
// a run stuck on a corrupt archive.
func (e *Extractor) extract7z(ctx context.Context, archivePath, destDir, format string) error {
	if _, err := exec.LookPath("7z"); err != nil {
		return fmt.Errorf("%w: install p7zip-full to extract .7z and .rar files", ErrSevenZipMissing)
	}
	members, declared, err := list7zArchive(ctx, archivePath, format)
	if err != nil {
		return err
	}
	return e.extractThrough7z(ctx, archivePath, destDir, format, members, declared)
}
